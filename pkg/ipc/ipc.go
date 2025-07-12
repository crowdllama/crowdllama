// Package ipc provides IPC (Inter-Process Communication) functionality for CrowdLlama.
package ipc

import (
	"encoding/json"
	"fmt"
	"net"
	"os"

	"go.uber.org/zap"
)

// Message represents a message structure for communication with Electron
type Message struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
	ID      string      `json:"id,omitempty"`
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

	// Try to parse as JSON
	var ipcMsg Message
	if err := json.Unmarshal(message, &ipcMsg); err != nil {
		s.logger.Warn("Failed to parse IPC message as JSON",
			zap.Error(err),
			zap.String("raw", string(message)))
		return
	}

	s.logger.Info("Parsed IPC message",
		zap.String("type", ipcMsg.Type),
		zap.String("id", ipcMsg.ID),
		zap.Any("payload", ipcMsg.Payload))

	// Handle different message types
	var response Message
	switch ipcMsg.Type {
	case "ping":
		response = Message{
			Type:    "pong",
			Payload: "pong",
			ID:      ipcMsg.ID,
		}
		s.logger.Info("Responding to ping with pong")
	default:
		// Echo back for other message types
		response = Message{
			Type:    "response",
			Payload: "Message received",
			ID:      ipcMsg.ID,
		}
	}

	responseBytes, err := json.Marshal(response)
	if err != nil {
		s.logger.Error("Failed to marshal IPC response", zap.Error(err))
		return
	}

	if _, err := conn.Write(responseBytes); err != nil {
		s.logger.Error("Failed to write IPC response", zap.Error(err))
	}
}
