// Package ipc provides IPC (Inter-Process Communication) functionality for CrowdLlama.
package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/crowdllama/crowdllama/pkg/peer"
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

// PromptMessage represents a prompt request
// Updated to match the expected IPC message format
// {"type":"prompt","prompt":"is the sky blue?","model":"gpt-4"}
type PromptMessage struct {
	Type   string `json:"type"`
	Prompt string `json:"prompt"`
	Model  string `json:"model"`
	ID     string `json:"id,omitempty"`
}

// PromptResponseMessage represents a prompt response
type PromptResponseMessage struct {
	Type    string `json:"type"`
	Content string `json:"content"`
	ID      string `json:"id,omitempty"`
	Error   string `json:"error,omitempty"`
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
}

// NewServer creates a new IPC server
func NewServer(socketPath string, logger *zap.Logger) *Server {
	return &Server{
		socketPath: socketPath,
		logger:     logger,
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

// handlePingMessage handles ping messages
func (s *Server) handlePingMessage(message []byte, conn net.Conn) {
	var pingMsg PingMessage
	if err := json.Unmarshal(message, &pingMsg); err != nil {
		s.logger.Warn("Failed to parse ping message", zap.Error(err))
		pongMsg := &PongMessage{
			BaseMessage: BaseMessage{
				Type: MessageTypePong,
			},
			Payload: "Failed to parse ping message",
		}
		if err := s.sendToIPC(conn, pongMsg, "pong"); err != nil {
			s.logger.Error("Failed to send pong message", zap.Error(err))
		}
		return
	}

	// Create pong response
	pongMsg := &PongMessage{
		BaseMessage: BaseMessage{
			Type: MessageTypePong,
			ID:   pingMsg.ID,
		},
		Payload: fmt.Sprintf("pong at %d", time.Now().Unix()),
	}

	if err := s.sendToIPC(conn, pongMsg, "pong"); err != nil {
		s.logger.Error("Failed to send pong message", zap.Error(err))
	}
}

// sendToIPC sends a message to the IPC client
func (s *Server) sendToIPC(conn net.Conn, message interface{}, messageType string) error {
	s.writeMutex.Lock()
	defer s.writeMutex.Unlock()

	jsonData, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal %s message: %w", messageType, err)
	}

	_, err = conn.Write(jsonData)
	if err != nil {
		return fmt.Errorf("failed to write %s message: %w", messageType, err)
	}

	s.logger.Info("Sent IPC message", zap.String("type", messageType))
	return nil
}

// sendInitializeResponse sends an initialization response
func (s *Server) sendInitializeResponse(conn net.Conn, mode string) {
	response := &ResponseMessage{
		BaseMessage: BaseMessage{
			Type: MessageTypeResponse,
		},
		Payload: map[string]string{
			"mode": mode,
		},
		Success: true,
	}

	if err := s.sendToIPC(conn, response, "initialize_response"); err != nil {
		s.logger.Error("Failed to send initialize response", zap.Error(err))
	}
}

// sendErrorResponse sends an error response
func (s *Server) sendErrorResponse(conn net.Conn, errorMsg string) {
	response := &ResponseMessage{
		BaseMessage: BaseMessage{
			Type: MessageTypeResponse,
		},
		Payload: map[string]string{
			"error": errorMsg,
		},
		Success: false,
		Error:   errorMsg,
	}

	if err := s.sendToIPC(conn, response, "error_response"); err != nil {
		s.logger.Error("Failed to send error response", zap.Error(err))
	}
}

// emitInitializeStatus emits initialization status updates
func (s *Server) emitInitializeStatus(conn net.Conn, text string) {
	statusMsg := &InitializeStatusMessage{
		Type: MessageTypeInitializeStatus,
		Text: text,
	}

	if err := s.sendToIPC(conn, statusMsg, "initialize_status"); err != nil {
		s.logger.Error("Failed to emit initialize status", zap.Error(err))
	}
}

// handleWorkerInitialization handles worker mode initialization
func (s *Server) handleWorkerInitialization(conn net.Conn) {
	s.emitInitializeStatus(conn, "Worker mode initialized")
	s.sendInitializeResponse(conn, ModeWorker)
}

// handleConsumerInitialization handles consumer mode initialization
func (s *Server) handleConsumerInitialization(conn net.Conn) {
	s.emitInitializeStatus(conn, "Consumer mode initialized")
	s.sendInitializeResponse(conn, ModeConsumer)
}

// handleInitializeMessage handles initialization messages
func (s *Server) handleInitializeMessage(message []byte, conn net.Conn) {
	var initMsg InitializeMessage
	if err := json.Unmarshal(message, &initMsg); err != nil {
		s.logger.Warn("Failed to parse initialize message", zap.Error(err))
		s.sendErrorResponse(conn, "Failed to parse initialize message")
		return
	}

	s.logger.Info("Received initialize message", zap.String("mode", initMsg.Mode))

	// Check if we have a peer instance
	if s.peerInstance == nil {
		s.logger.Error("No peer instance available for initialization")
		s.sendErrorResponse(conn, "No peer instance available")
		return
	}

	// Determine mode based on peer instance
	if p, ok := s.peerInstance.(*peer.Peer); ok {
		if p.WorkerMode {
			s.handleWorkerInitialization(conn)
		} else {
			s.handleConsumerInitialization(conn)
		}
	} else {
		s.logger.Error("Invalid peer instance type")
		s.sendErrorResponse(conn, "Invalid peer instance type")
	}
}

// handlePromptMessage handles prompt messages
func (s *Server) handlePromptMessage(message []byte, conn net.Conn) {
	var promptMsg PromptMessage
	if err := json.Unmarshal(message, &promptMsg); err != nil {
		s.logger.Warn("Failed to parse prompt message", zap.Error(err))
		s.sendErrorResponse(conn, "Failed to parse prompt message")
		return
	}

	s.logger.Info("Received prompt message",
		zap.String("prompt", promptMsg.Prompt),
		zap.String("model", promptMsg.Model),
		zap.String("id", promptMsg.ID))

	// Check if we have a peer instance
	if s.peerInstance == nil {
		s.logger.Error("No peer instance available for prompt handling")
		s.sendErrorResponse(conn, "No peer instance available")
		return
	}

	// Handle prompt based on peer mode
	if p, ok := s.peerInstance.(*peer.Peer); ok {
		if p.WorkerMode {
			// Worker mode: handle locally with Ollama
			response, err := s.sendPromptToOllama(promptMsg.Prompt, promptMsg.Model)
			if err != nil {
				s.logger.Error("Failed to send prompt to Ollama", zap.Error(err))
				s.sendErrorResponse(conn, fmt.Sprintf("Failed to send prompt to Ollama: %v", err))
				return
			}

			// Send response
			promptResponse := &PromptResponseMessage{
				Type:    MessageTypePromptResponse,
				Content: response,
				ID:      promptMsg.ID,
			}

			if err := s.sendToIPC(conn, promptResponse, "prompt_response"); err != nil {
				s.logger.Error("Failed to send prompt response", zap.Error(err))
			}
		} else {
			// Consumer mode: send to network (placeholder for now)
			s.logger.Info("Consumer mode prompt handling not yet implemented")
			s.sendErrorResponse(conn, "Consumer mode prompt handling not yet implemented")
		}
	} else {
		s.logger.Error("Invalid peer instance type")
		s.sendErrorResponse(conn, "Invalid peer instance type")
	}
}

// sendHTTPRequest sends an HTTP request and returns the response
func (s *Server) sendHTTPRequest(url string, requestBody map[string]interface{}) (string, error) {
	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send HTTP request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			// Log error but don't fail the operation
			fmt.Printf("Warning: failed to close response body: %v\n", closeErr)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP request failed with status %d: %s", resp.StatusCode, string(body))
	}

	return string(body), nil
}

// sendPromptToOllama sends a prompt to Ollama
func (s *Server) sendPromptToOllama(prompt, model string) (string, error) {
	// Use the model from the request, or default to "tinyllama"
	if model == "" {
		model = "tinyllama"
	}

	requestBody := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": prompt,
			},
		},
		"stream": false,
	}

	// Use localhost Ollama server
	url := "http://localhost:11434/api/chat"

	response, err := s.sendHTTPRequest(url, requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to send prompt to Ollama: %w", err)
	}

	// Parse the response to extract the content
	var ollamaResp map[string]interface{}
	if err := json.Unmarshal([]byte(response), &ollamaResp); err != nil {
		return "", fmt.Errorf("failed to parse Ollama response: %w", err)
	}

	// Extract the message content
	if message, ok := ollamaResp["message"].(map[string]interface{}); ok {
		if content, ok := message["content"].(string); ok {
			return content, nil
		}
	}

	return "No response content received from Ollama", nil
}

// handleUnknownMessage handles unknown message types
func (s *Server) handleUnknownMessage(baseMsg BaseMessage, conn net.Conn) {
	s.logger.Warn("Received unknown message type", zap.String("type", baseMsg.Type))
	s.sendErrorResponse(conn, fmt.Sprintf("Unknown message type: %s", baseMsg.Type))
}
