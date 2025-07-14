// Package ipc provides IPC (Inter-Process Communication) functionality for CrowdLlama.
package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"

	"go.uber.org/zap"

	llamav1 "github.com/crowdllama/crowdllama-pb/llama/v1"
	"github.com/crowdllama/crowdllama/pkg/crowdllama"
	"github.com/crowdllama/crowdllama/pkg/peer"
	"google.golang.org/protobuf/proto"
)

// Mode constants for initialization
const (
	ModeWorker   = "worker"
	ModeConsumer = "consumer"
)

// MessageType constants for message types
const (
	MessageTypePing             = "ping"
	MessageTypePong             = "pong"
	MessageTypeInitialize       = "initialize"
	MessageTypeInitializeStatus = "initialize_status"
	MessageTypePrompt           = "prompt"
	MessageTypePromptResponse   = "prompt_response"
	MessageTypeResponse         = "response"
)

// BaseMessage represents the common structure for all IPC messages
type BaseMessage struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
}

// InitializeMessage represents an initialization message
type InitializeMessage struct {
	Type string `json:"type"`
	Mode string `json:"mode"`
}

// InitializeStatusMessage represents an initialization status message
type InitializeStatusMessage struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// PingMessage represents a ping message
type PingMessage struct {
	BaseMessage
	Timestamp int64 `json:"timestamp,omitempty"`
}

// PongMessage represents a pong response
type PongMessage struct {
	BaseMessage
	Payload string `json:"payload"`
}

// ResponseMessage represents a generic response
type ResponseMessage struct {
	BaseMessage
	Payload interface{} `json:"payload"`
	Success bool        `json:"success"`
	Error   string      `json:"error,omitempty"`
}

// Server represents an IPC server
type Server struct {
	socketPath string
	logger     *zap.Logger
	listener   net.Listener
	writeMutex sync.Mutex // Protects concurrent writes to connections

	// Application state
	peerInstance interface{} // *peer.Peer
	currentMode  string
	apiHandler   crowdllama.UnifiedAPIHandler
}

// NewServer creates a new IPC server
func NewServer(socketPath string, logger *zap.Logger) *Server {
	return &Server{
		socketPath: socketPath,
		logger:     logger,
		apiHandler: crowdllama.DefaultAPIHandler,
	}
}

// SetWorkerInstance sets the peer instance (for backward compatibility)
func (s *Server) SetWorkerInstance(peerInstance interface{}) {
	s.peerInstance = peerInstance
	if p, ok := peerInstance.(*peer.Peer); ok {
		if p.WorkerMode {
			s.currentMode = ModeWorker
		} else {
			s.currentMode = ModeConsumer
		}
	}
}

// SetConsumerInstance sets the peer instance (for backward compatibility)
func (s *Server) SetConsumerInstance(peerInstance interface{}) {
	s.peerInstance = peerInstance
	if p, ok := peerInstance.(*peer.Peer); ok {
		if p.WorkerMode {
			s.currentMode = ModeWorker
		} else {
			s.currentMode = ModeConsumer
		}
	}
}

// SetPeerInstance sets the peer instance
func (s *Server) SetPeerInstance(peerInstance interface{}) {
	s.peerInstance = peerInstance
	if p, ok := peerInstance.(*peer.Peer); ok {
		if p.WorkerMode {
			s.currentMode = ModeWorker
		} else {
			s.currentMode = ModeConsumer
		}
	}
}

// SetAPIHandler sets the unified API handler for processing inference requests
func (s *Server) SetAPIHandler(handler crowdllama.UnifiedAPIHandler) {
	s.apiHandler = handler
}

// GetCurrentMode returns the current mode
func (s *Server) GetCurrentMode() string {
	return s.currentMode
}

// Start starts the IPC server
func (s *Server) Start() error {
	// Remove existing socket file if it exists
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove existing socket: %w", err)
	}

	// Create Unix domain socket listener
	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("failed to create IPC socket: %w", err)
	}
	s.listener = listener

	// Set socket permissions for Electron to access (0600 is most secure)
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		s.logger.Error("Failed to set socket permissions", zap.Error(err))
	}

	s.logger.Info("IPC server started", zap.String("socket", s.socketPath))

	// Accept connections
	for {
		conn, err := listener.Accept()
		if err != nil {
			s.logger.Error("Failed to accept IPC connection", zap.Error(err))
			continue
		}

		go s.handleConnection(conn)
	}
}

// Stop stops the IPC server
func (s *Server) Stop() error {
	if s.listener != nil {
		if err := s.listener.Close(); err != nil {
			return fmt.Errorf("failed to close IPC listener: %w", err)
		}
	}
	return nil
}

// handleConnection handles individual IPC connections
func (s *Server) handleConnection(conn net.Conn) {
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			s.logger.Error("Failed to close IPC connection", zap.Error(closeErr))
		}
	}()

	s.logger.Info("IPC connection established", zap.String("remote", conn.RemoteAddr().String()))

	for {
		// Try to read 4 bytes for a length prefix
		lengthBytes := make([]byte, 4)
		_, err := conn.Read(lengthBytes)
		if err != nil {
			if err.Error() == "EOF" {
				s.logger.Info("IPC connection closed by client (EOF)")
			} else {
				s.logger.Error("Failed to read from IPC connection", zap.Error(err))
			}
			break
		}

		length := int(uint32(lengthBytes[0])<<24 | uint32(lengthBytes[1])<<16 | uint32(lengthBytes[2])<<8 | uint32(lengthBytes[3]))
		if length > 0 && length < 10*1024*1024 {
			// Looks like a valid length-prefixed protobuf message
			msgBytes := make([]byte, length)
			_, err := conn.Read(msgBytes)
			if err != nil {
				s.logger.Error("Failed to read protobuf message from IPC connection", zap.Error(err))
				break
			}
			s.handleProtobufMessage(msgBytes, conn)
			continue
		}

		// If not a valid length, treat as JSON (read until EOF or newline)
		// We'll read up to 4096 bytes for a JSON message
		buffer := make([]byte, 4096)
		copy(buffer, lengthBytes)
		n, err := conn.Read(buffer[4:])
		if err != nil {
			if err.Error() == "EOF" {
				s.logger.Info("IPC connection closed by client (EOF)")
			} else {
				s.logger.Error("Failed to read from IPC connection (JSON fallback)", zap.Error(err))
			}
			break
		}
		totalLen := 4 + n
		s.handleMessage(buffer[:totalLen], conn)
	}

	s.logger.Info("IPC connection closed", zap.String("remote", conn.RemoteAddr().String()))
}

// handleMessage processes a single IPC message
func (s *Server) handleMessage(message []byte, conn net.Conn) {
	s.logger.Info("Received IPC message",
		zap.String("message", string(message)),
		zap.Int("length", len(message)))

	// First, try to parse as a base message to get the type
	var baseMsg BaseMessage
	if err := json.Unmarshal(message, &baseMsg); err != nil {
		s.logger.Warn("Failed to parse IPC message as JSON, trying as protobuf",
			zap.Error(err),
			zap.String("raw", string(message)))

		// Try to handle as protobuf message
		s.handleProtobufMessage(message, conn)
		return
	}

	s.logger.Info("Parsed base IPC message",
		zap.String("type", baseMsg.Type),
		zap.String("id", baseMsg.ID))

	// Handle different message types with proper parsing
	switch baseMsg.Type {
	case MessageTypePing:
		s.handlePingMessage(message, conn)
	case MessageTypeInitialize:
		s.handleInitializeMessage(message, conn)
	case MessageTypePrompt:
		s.handlePromptMessage(message, conn)
	default:
		s.handleUnknownMessage(baseMsg, conn)
	}
}

// handleProtobufMessage handles protobuf messages directly
func (s *Server) handleProtobufMessage(message []byte, conn net.Conn) {
	var pbReq llamav1.BaseMessage
	if err := proto.Unmarshal(message, &pbReq); err != nil {
		s.logger.Error("Failed to unmarshal protobuf message", zap.Error(err))
		s.sendErrorResponse(conn, "Invalid protobuf message format")
		return
	}

	generateReq := pbReq.GetGenerateRequest()
	if generateReq == nil {
		s.logger.Error("No GenerateRequest in protobuf message")
		s.sendErrorResponse(conn, "No GenerateRequest in protobuf message")
		return
	}

	s.logger.Info("Received protobuf prompt message",
		zap.String("model", generateReq.GetModel()),
		zap.String("prompt", generateReq.GetPrompt()))

	ctx := context.Background()
	pbResp, err := s.apiHandler(ctx, &pbReq)
	if err != nil {
		s.logger.Error("Failed to process protobuf prompt", zap.Error(err))
		s.sendErrorResponse(conn, fmt.Sprintf("Failed to process prompt: %v", err))
		return
	}

	// Use length-prefixed format for response
	if err := crowdllama.WriteLengthPrefixedPB(conn, pbResp); err != nil {
		s.logger.Error("Failed to write length-prefixed PB response", zap.Error(err))
		s.sendErrorResponse(conn, "Failed to write PB response")
		return
	}

	s.logger.Info("Sent length-prefixed protobuf response")
}

// handlePingMessage handles ping messages
func (s *Server) handlePingMessage(message []byte, conn net.Conn) {
	var pingMsg PingMessage
	if err := json.Unmarshal(message, &pingMsg); err != nil {
		s.logger.Error("Failed to parse ping message", zap.Error(err))
		return
	}

	s.logger.Info("Received ping message", zap.Int64("timestamp", pingMsg.Timestamp))

	// Send pong response
	pongMsg := PongMessage{
		BaseMessage: BaseMessage{
			Type: MessageTypePong,
			ID:   pingMsg.ID,
		},
		Payload: "pong",
	}

	if err := s.sendToIPC(conn, pongMsg, MessageTypePong); err != nil {
		s.logger.Error("Failed to send pong response", zap.Error(err))
	}
}

// sendToIPC sends a message to the IPC client
func (s *Server) sendToIPC(conn net.Conn, message interface{}, messageType string) error {
	s.writeMutex.Lock()
	defer s.writeMutex.Unlock()

	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal IPC message: %w", err)
	}

	_, err = conn.Write(data)
	if err != nil {
		return fmt.Errorf("failed to write to IPC connection: %w", err)
	}

	s.logger.Info("Sent IPC message", zap.String("type", messageType))
	return nil
}

// sendInitializeResponse sends an initialization response
func (s *Server) sendInitializeResponse(conn net.Conn, mode string) {
	response := InitializeStatusMessage{
		Type: MessageTypeInitializeStatus,
		Text: fmt.Sprintf("Initialized in %s mode", mode),
	}

	if err := s.sendToIPC(conn, response, MessageTypeInitializeStatus); err != nil {
		s.logger.Error("Failed to send initialize response", zap.Error(err))
	}
}

// sendErrorResponse sends an error response
func (s *Server) sendErrorResponse(conn net.Conn, errorMsg string) {
	response := ResponseMessage{
		BaseMessage: BaseMessage{
			Type: MessageTypeResponse,
		},
		Success: false,
		Error:   errorMsg,
	}

	if err := s.sendToIPC(conn, response, MessageTypeResponse); err != nil {
		s.logger.Error("Failed to send error response", zap.Error(err))
	}
}

// emitInitializeStatus emits initialization status
func (s *Server) emitInitializeStatus(conn net.Conn, text string) {
	status := InitializeStatusMessage{
		Type: MessageTypeInitializeStatus,
		Text: text,
	}

	if err := s.sendToIPC(conn, status, MessageTypeInitializeStatus); err != nil {
		s.logger.Error("Failed to emit initialize status", zap.Error(err))
	}
}

// handleWorkerInitialization handles worker mode initialization
func (s *Server) handleWorkerInitialization(conn net.Conn) {
	s.emitInitializeStatus(conn, "Starting worker mode...")
	s.emitInitializeStatus(conn, "Worker mode initialized")
}

// handleConsumerInitialization handles consumer mode initialization
func (s *Server) handleConsumerInitialization(conn net.Conn) {
	s.emitInitializeStatus(conn, "Starting consumer mode...")
	s.emitInitializeStatus(conn, "Consumer mode initialized")
}

// handleInitializeMessage handles initialization messages
func (s *Server) handleInitializeMessage(message []byte, conn net.Conn) {
	var initMsg InitializeMessage
	if err := json.Unmarshal(message, &initMsg); err != nil {
		s.logger.Error("Failed to parse initialize message", zap.Error(err))
		s.sendErrorResponse(conn, "Invalid initialize message format")
		return
	}

	s.logger.Info("Received initialize message", zap.String("mode", initMsg.Mode))

	switch initMsg.Mode {
	case ModeWorker:
		s.currentMode = ModeWorker
		s.handleWorkerInitialization(conn)
	case ModeConsumer:
		s.currentMode = ModeConsumer
		s.handleConsumerInitialization(conn)
	default:
		s.logger.Error("Unknown initialization mode", zap.String("mode", initMsg.Mode))
		s.sendErrorResponse(conn, "Unknown initialization mode")
		return
	}

	s.sendInitializeResponse(conn, s.currentMode)
}

// handlePromptMessage handles prompt messages using PB-based inference
func (s *Server) handlePromptMessage(message []byte, conn net.Conn) {
	var pbReq llamav1.BaseMessage
	if err := proto.Unmarshal(message, &pbReq); err != nil {
		s.logger.Error("Failed to unmarshal PB prompt message", zap.Error(err))
		s.sendErrorResponse(conn, "Invalid PB prompt message format")
		return
	}

	generateReq := pbReq.GetGenerateRequest()
	if generateReq == nil {
		s.logger.Error("No GenerateRequest in PB message")
		s.sendErrorResponse(conn, "No GenerateRequest in PB message")
		return
	}

	s.logger.Info("Received PB prompt message",
		zap.String("model", generateReq.GetModel()),
		zap.String("prompt", generateReq.GetPrompt()))

	ctx := context.Background()
	pbResp, err := s.apiHandler(ctx, &pbReq)
	if err != nil {
		s.logger.Error("Failed to process prompt", zap.Error(err))
		s.sendErrorResponse(conn, fmt.Sprintf("Failed to process prompt: %v", err))
		return
	}

	respBytes, err := proto.Marshal(pbResp)
	if err != nil {
		s.logger.Error("Failed to marshal PB response", zap.Error(err))
		s.sendErrorResponse(conn, "Failed to marshal PB response")
		return
	}

	s.writeMutex.Lock()
	defer s.writeMutex.Unlock()
	_, err = conn.Write(respBytes)
	if err != nil {
		s.logger.Error("Failed to write PB response to IPC connection", zap.Error(err))
	}
}

// handleUnknownMessage handles unknown message types
func (s *Server) handleUnknownMessage(baseMsg BaseMessage, conn net.Conn) {
	s.logger.Warn("Received unknown message type", zap.String("type", baseMsg.Type))
	s.sendErrorResponse(conn, fmt.Sprintf("Unknown message type: %s", baseMsg.Type))
}
