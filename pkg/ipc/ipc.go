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
	workerInstance   interface{} // *worker.Worker
	consumerInstance interface{} // *consumer.Consumer
	currentMode      string
}

// NewServer creates a new IPC server
func NewServer(socketPath string, logger *zap.Logger) *Server {
	return &Server{
		socketPath: socketPath,
		logger:     logger,
	}
}

// SetWorkerInstance sets the worker instance
func (s *Server) SetWorkerInstance(worker interface{}) {
	s.workerInstance = worker
	s.currentMode = ModeWorker
}

// SetConsumerInstance sets the consumer instance
func (s *Server) SetConsumerInstance(consumer interface{}) {
	s.consumerInstance = consumer
	s.currentMode = ModeConsumer
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

	s.logger.Info("Received ping message", zap.String("id", pingMsg.ID))

	pongMsg := &PongMessage{
		BaseMessage: BaseMessage{
			Type: MessageTypePong,
			ID:   pingMsg.ID,
		},
		Payload: "pong",
	}

	if err := s.sendToIPC(conn, pongMsg, "pong"); err != nil {
		s.logger.Error("Failed to send pong message", zap.Error(err))
	}
}

// sendToIPC sends a message to the IPC client with centralized logging
func (s *Server) sendToIPC(conn net.Conn, message interface{}, messageType string) error {
	s.writeMutex.Lock()
	defer s.writeMutex.Unlock()

	responseBytes, err := json.Marshal(message)
	if err != nil {
		s.logger.Error("Failed to marshal IPC message", zap.Error(err), zap.String("type", messageType))
		return fmt.Errorf("failed to marshal IPC message: %w", err)
	}

	// Add newline for message framing
	responseBytes = append(responseBytes, '\n')

	if _, err := conn.Write(responseBytes); err != nil {
		s.logger.Error("Failed to write IPC message", zap.Error(err), zap.String("type", messageType))
		return fmt.Errorf("failed to write IPC message: %w", err)
	}

	s.logger.Info("Sent message to IPC", zap.String("type", messageType), zap.ByteString("raw", responseBytes))
	return nil
}

// sendInitializeResponse sends an initialization response
func (s *Server) sendInitializeResponse(conn net.Conn, mode string) {
	response := &ResponseMessage{
		BaseMessage: BaseMessage{
			Type: MessageTypeResponse,
		},
		Success: true,
		Payload: map[string]interface{}{
			"mode":   mode,
			"status": "initialized",
		},
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
		Success: false,
		Error:   errorMsg,
	}
	if err := s.sendToIPC(conn, response, "error_response"); err != nil {
		s.logger.Error("Failed to send error response", zap.Error(err))
	}
}

// emitInitializeStatus emits an initialize status message to the IPC client
func (s *Server) emitInitializeStatus(conn net.Conn, text string) {
	statusMsg := &InitializeStatusMessage{
		Type: MessageTypeInitializeStatus,
		Text: text,
	}

	if err := s.sendToIPC(conn, statusMsg, "initialize_status"); err != nil {
		s.logger.Error("Failed to send initialize status", zap.Error(err))
	}
}

// handleWorkerInitialization handles worker mode initialization
func (s *Server) handleWorkerInitialization(conn net.Conn) {
	s.emitInitializeStatus(conn, "Starting worker initialization...")
	s.currentMode = ModeWorker
	s.emitInitializeStatus(conn, "Connecting to bootstrap peers...")
	s.emitInitializeStatus(conn, "Setting up worker metadata...")
	s.emitInitializeStatus(conn, "Advertising worker services...")
	s.emitInitializeStatus(conn, "Worker mode initialized successfully.")
}

// handleConsumerInitialization handles consumer mode initialization
func (s *Server) handleConsumerInitialization(conn net.Conn) {
	s.emitInitializeStatus(conn, "Starting consumer initialization...")
	s.currentMode = ModeConsumer
	s.emitInitializeStatus(conn, "Connecting to bootstrap peers...")
	s.emitInitializeStatus(conn, "Starting worker discovery...")
	s.emitInitializeStatus(conn, "Starting HTTP server...")
	s.emitInitializeStatus(conn, "Consumer mode initialized successfully.")
}

// handleInitializeMessage handles initialization messages
func (s *Server) handleInitializeMessage(message []byte, conn net.Conn) {
	var initMsg InitializeMessage
	if err := json.Unmarshal(message, &initMsg); err != nil {
		s.logger.Warn("Failed to parse initialize message", zap.Error(err))
		s.sendErrorResponse(conn, "Failed to parse initialize message")
		return
	}

	// Validate mode
	switch initMsg.Mode {
	case ModeWorker:
		s.logger.Info("Initializing in worker mode")
		s.handleWorkerInitialization(conn)
		s.sendInitializeResponse(conn, initMsg.Mode)
	case ModeConsumer:
		s.logger.Info("Initializing in consumer mode")
		s.handleConsumerInitialization(conn)
		s.sendInitializeResponse(conn, initMsg.Mode)
	default:
		s.logger.Warn("Invalid mode specified", zap.String("mode", initMsg.Mode))
		s.sendErrorResponse(conn, fmt.Sprintf("Invalid mode: %s. Valid modes are: %s, %s", initMsg.Mode, ModeWorker, ModeConsumer))
	}
}

// handlePromptMessage handles prompt messages
func (s *Server) handlePromptMessage(message []byte, conn net.Conn) {
	var promptMsg PromptMessage
	if err := json.Unmarshal(message, &promptMsg); err != nil {
		s.logger.Warn("Failed to parse prompt message", zap.Error(err))
		response := &PromptResponseMessage{
			Type:    MessageTypePromptResponse,
			Content: "Failed to parse prompt message",
			Error:   err.Error(),
		}
		if err := s.sendToIPC(conn, response, "prompt_response"); err != nil {
			s.logger.Error("Failed to send prompt response", zap.Error(err))
		}
		return
	}

	s.logger.Info("Received prompt message", zap.String("prompt", promptMsg.Prompt), zap.String("model", promptMsg.Model))

	// Use the provided model, fallback to tinyllama if not set
	model := promptMsg.Model
	if model == "" {
		model = "tinyllama"
	}

	// Send the prompt to the appropriate endpoint based on current mode
	var response string
	var err error

	switch s.currentMode {
	case ModeConsumer:
		response, err = s.sendPromptToConsumer(promptMsg.Prompt, model)
	case ModeWorker:
		response, err = s.sendPromptToOllama(promptMsg.Prompt, model)
	default:
		err = fmt.Errorf("unknown mode: %s", s.currentMode)
	}

	if err != nil {
		s.logger.Error("Failed to process prompt", zap.Error(err))
		response := &PromptResponseMessage{
			Type:    MessageTypePromptResponse,
			Content: "",
			ID:      promptMsg.ID,
			Error:   err.Error(),
		}
		if err := s.sendToIPC(conn, response, "prompt_response"); err != nil {
			s.logger.Error("Failed to send prompt response", zap.Error(err))
		}
		return
	}

	result := &PromptResponseMessage{
		Type:    MessageTypePromptResponse,
		Content: response,
		ID:      promptMsg.ID,
	}

	if err := s.sendToIPC(conn, result, "prompt_response"); err != nil {
		s.logger.Error("Failed to send prompt response", zap.Error(err))
	}
}

// sendHTTPRequest sends an HTTP POST request and returns the response content
func (s *Server) sendHTTPRequest(url string, requestBody map[string]interface{}) (string, error) {
	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	// Create request with context
	req, err := http.NewRequestWithContext(context.Background(), "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Send request
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			s.logger.Error("Failed to close response body", zap.Error(closeErr))
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("server returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse the response
	var response map[string]interface{}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	// Extract the message content
	if message, ok := response["message"].(map[string]interface{}); ok {
		if content, ok := message["content"].(string); ok {
			return content, nil
		}
	}

	return "No response content received", nil
}

// sendPromptToConsumer sends a prompt to the consumer's HTTP server
func (s *Server) sendPromptToConsumer(prompt, model string) (string, error) {
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
	return s.sendHTTPRequest("http://localhost:9001/api/chat", requestBody)
}

// sendPromptToOllama sends a prompt to the Ollama server
func (s *Server) sendPromptToOllama(prompt, model string) (string, error) {
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
	return s.sendHTTPRequest("http://localhost:11434/api/chat", requestBody)
}

// handleUnknownMessage handles unknown message types
func (s *Server) handleUnknownMessage(baseMsg BaseMessage, conn net.Conn) {
	s.logger.Info("Received unknown message type", zap.String("type", baseMsg.Type))

	response := &ResponseMessage{
		BaseMessage: BaseMessage{
			Type: MessageTypeResponse,
		},
		Success: false,
		Error:   fmt.Sprintf("Unknown message type: %s", baseMsg.Type),
	}

	if err := s.sendToIPC(conn, response, "error_response"); err != nil {
		s.logger.Error("Failed to send error response", zap.Error(err))
	}
}
