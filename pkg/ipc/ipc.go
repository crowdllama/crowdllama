// Package ipc provides IPC (Inter-Process Communication) functionality for CrowdLlama.
package ipc

import (
	"encoding/json"
	"fmt"
	"net"
	"os"

	"go.uber.org/zap"
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
}

// NewServer creates a new IPC server
func NewServer(socketPath string, logger *zap.Logger) *Server {
	return &Server{
		socketPath: socketPath,
		logger:     logger,
	}
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

	buffer := make([]byte, 4096)
	for {
		n, err := conn.Read(buffer)
		if err != nil {
			// EOF is normal when client closes connection after sending message
			if err.Error() == "EOF" {
				s.logger.Info("IPC connection closed by client (EOF)")
			} else {
				s.logger.Error("Failed to read from IPC connection", zap.Error(err))
			}
			break
		}

		if n > 0 {
			s.handleMessage(buffer[:n], conn)
		}
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
		s.logger.Warn("Failed to parse IPC message as JSON",
			zap.Error(err),
			zap.String("raw", string(message)))
		return
	}

	s.logger.Info("Parsed base IPC message",
		zap.String("type", baseMsg.Type),
		zap.String("id", baseMsg.ID))

	// Handle different message types with proper parsing
	var response interface{}
	switch baseMsg.Type {
	case MessageTypePing:
		response = s.handlePingMessage(message)
	case MessageTypeInitialize:
		response = s.handleInitializeMessage(message, conn)
	default:
		response = s.handleUnknownMessage(baseMsg)
	}

	// Send response
	responseBytes, err := json.Marshal(response)
	if err != nil {
		s.logger.Error("Failed to marshal IPC response", zap.Error(err))
		return
	}

	if _, err := conn.Write(responseBytes); err != nil {
		s.logger.Error("Failed to write IPC response", zap.Error(err))
	}
}

// handlePingMessage handles ping messages
func (s *Server) handlePingMessage(message []byte) *PongMessage {
	var pingMsg PingMessage
	if err := json.Unmarshal(message, &pingMsg); err != nil {
		s.logger.Warn("Failed to parse ping message", zap.Error(err))
		return &PongMessage{
			BaseMessage: BaseMessage{
				Type: MessageTypePong,
				ID:   pingMsg.ID,
			},
			Payload: "pong",
		}
	}

	s.logger.Info("Responding to ping with pong", zap.Int64("timestamp", pingMsg.Timestamp))
	return &PongMessage{
		BaseMessage: BaseMessage{
			Type: MessageTypePong,
			ID:   pingMsg.ID,
		},
		Payload: "pong",
	}
}

// emitInitializeStatus sends an initialize_status message
func (s *Server) emitInitializeStatus(conn net.Conn, text string) {
	statusMsg := &InitializeStatusMessage{
		Type: MessageTypeInitializeStatus,
		Text: text,
	}
	statusBytes, _ := json.Marshal(statusMsg)
	if _, err := conn.Write(statusBytes); err != nil {
		s.logger.Error("Failed to write initialize_status message", zap.Error(err))
	}
}

// handleInitializeMessage handles initialization messages
func (s *Server) handleInitializeMessage(message []byte, conn net.Conn) *ResponseMessage {
	var initMsg InitializeMessage
	if err := json.Unmarshal(message, &initMsg); err != nil {
		s.logger.Warn("Failed to parse initialize message", zap.Error(err))
		return &ResponseMessage{
			BaseMessage: BaseMessage{
				Type: MessageTypeResponse,
			},
			Success: false,
			Error:   "Failed to parse initialize message",
		}
	}

	// Validate mode
	switch initMsg.Mode {
	case ModeWorker:
		s.logger.Info("Initializing in worker mode")
		s.emitInitializeStatus(conn, "Worker mode initialized.")
		return &ResponseMessage{
			BaseMessage: BaseMessage{
				Type: MessageTypeResponse,
			},
			Success: true,
			Payload: map[string]interface{}{
				"mode":   initMsg.Mode,
				"status": "initialized",
			},
		}
	case ModeConsumer:
		s.logger.Info("Initializing in consumer mode")
		s.emitInitializeStatus(conn, "Consumer mode initialized.")
		return &ResponseMessage{
			BaseMessage: BaseMessage{
				Type: MessageTypeResponse,
			},
			Success: true,
			Payload: map[string]interface{}{
				"mode":   initMsg.Mode,
				"status": "initialized",
			},
		}
	default:
		s.logger.Warn("Invalid mode specified", zap.String("mode", initMsg.Mode))
		return &ResponseMessage{
			BaseMessage: BaseMessage{
				Type: MessageTypeResponse,
			},
			Success: false,
			Error:   fmt.Sprintf("Invalid mode: %s. Valid modes are: %s, %s", initMsg.Mode, ModeWorker, ModeConsumer),
		}
	}
}

// handleUnknownMessage handles unknown message types
func (s *Server) handleUnknownMessage(baseMsg BaseMessage) *ResponseMessage {
	s.logger.Info("Received unknown message type", zap.String("type", baseMsg.Type))
	return &ResponseMessage{
		BaseMessage: BaseMessage{
			Type: MessageTypeResponse,
			ID:   baseMsg.ID,
		},
		Success: false,
		Error:   fmt.Sprintf("Unknown message type: %s", baseMsg.Type),
	}
}
