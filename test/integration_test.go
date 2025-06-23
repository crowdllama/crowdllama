package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/matiasinsaurralde/crowdllama/internal/keys"
	"github.com/matiasinsaurralde/crowdllama/pkg/consumer"
	"github.com/matiasinsaurralde/crowdllama/pkg/dht"
	"github.com/matiasinsaurralde/crowdllama/pkg/worker"
)

// MockOllamaServer represents a mock Ollama API server
type MockOllamaServer struct {
	server *http.Server
	port   int
}

// MockOllamaRequest represents the request structure for the mock Ollama API
type MockOllamaRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

// MockOllamaResponse represents the response structure from the mock Ollama API
type MockOllamaResponse struct {
	Model      string    `json:"model"`
	CreatedAt  time.Time `json:"created_at"`
	Message    Message   `json:"message"`
	Stream     bool      `json:"stream"`
	DoneReason string    `json:"done_reason"`
	Done       bool      `json:"done"`
}

// Message represents a message in the mock Ollama API
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// NewMockOllamaServer creates a new mock Ollama server
func NewMockOllamaServer(port int) *MockOllamaServer {
	mux := http.NewServeMux()

	// Handle the /api/chat endpoint
	mux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Parse the request
		var req MockOllamaRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		// Validate the request
		if req.Model == "" || len(req.Messages) == 0 {
			http.Error(w, "Model and messages are required", http.StatusBadRequest)
			return
		}

		// Create a mock response
		response := MockOllamaResponse{
			Model:     req.Model,
			CreatedAt: time.Now(),
			Message: Message{
				Role:    "assistant",
				Content: "This is a mock response from the Ollama API. You asked: " + req.Messages[0].Content,
			},
			Stream:     false,
			Done:       true,
			DoneReason: "done",
		}

		// Send the response
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	})

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	return &MockOllamaServer{
		server: server,
		port:   port,
	}
}

// Start starts the mock Ollama server
func (m *MockOllamaServer) Start() error {
	return m.server.ListenAndServe()
}

// Stop stops the mock Ollama server
func (m *MockOllamaServer) Stop(ctx context.Context) error {
	return m.server.Shutdown(ctx)
}

// GetPort returns the port the server is running on
func (m *MockOllamaServer) GetPort() int {
	return m.port
}

// TestFullIntegration tests the complete end-to-end flow
func TestFullIntegration(t *testing.T) {
	// Skip this test if not running integration tests
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	logger, _ := zap.NewDevelopment()

	// Create temporary directory for keys
	tempDir, err := os.MkdirTemp("", "crowdllama-full-integration-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create temporary keys for all components
	dhtKeyPath := filepath.Join(tempDir, "dht.key")
	workerKeyPath := filepath.Join(tempDir, "worker.key")
	consumerKeyPath := filepath.Join(tempDir, "consumer.key")

	dhtKeyManager := keys.NewKeyManager(dhtKeyPath, logger)
	workerKeyManager := keys.NewKeyManager(workerKeyPath, logger)
	consumerKeyManager := keys.NewKeyManager(consumerKeyPath, logger)

	dhtPrivKey, err := dhtKeyManager.GetOrCreatePrivateKey()
	if err != nil {
		t.Fatalf("Failed to create DHT private key: %v", err)
	}

	workerPrivKey, err := workerKeyManager.GetOrCreatePrivateKey()
	if err != nil {
		t.Fatalf("Failed to create worker private key: %v", err)
	}

	consumerPrivKey, err := consumerKeyManager.GetOrCreatePrivateKey()
	if err != nil {
		t.Fatalf("Failed to create consumer private key: %v", err)
	}

	// Step 1: Start Mock Ollama Server on a different port to avoid conflicts
	t.Log("Step 1: Starting Mock Ollama Server")
	mockOllamaPort := 11435 // Use different port to avoid conflicts
	mockOllama := NewMockOllamaServer(mockOllamaPort)
	go func() {
		if err := mockOllama.Start(); err != nil && err != http.ErrServerClosed {
			t.Errorf("Mock Ollama server failed: %v", err)
		}
	}()
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		mockOllama.Stop(shutdownCtx)
	}()

	// Give the mock server time to start
	time.Sleep(1 * time.Second)

	// Step 2: Initialize DHT Server
	t.Log("Step 2: Initializing DHT Server")
	dhtServer, err := dht.NewDHTServer(ctx, dhtPrivKey, logger)
	if err != nil {
		t.Fatalf("Failed to create DHT server: %v", err)
	}
	defer dhtServer.Stop()

	// Start the DHT server
	dhtPeerAddr, err := dhtServer.Start()
	if err != nil {
		t.Fatalf("Failed to start DHT server: %v", err)
	}

	t.Logf("DHT server started with peer address: %s", dhtPeerAddr)
	t.Logf("DHT server peer ID: %s", dhtServer.GetPeerID())

	// Step 3: Initialize Worker with DHT server as bootstrap peer
	t.Log("Step 3: Initializing Worker with DHT server as bootstrap peer")
	mockOllamaURL := fmt.Sprintf("http://localhost:%d/api/chat", mockOllamaPort)
	workerInstance, err := worker.NewWorkerWithBootstrapPeersAndOllamaURL(ctx, workerPrivKey, []string{dhtPeerAddr}, mockOllamaURL)
	if err != nil {
		t.Fatalf("Failed to create worker: %v", err)
	}

	// Setup worker metadata and handlers
	workerInstance.SetupMetadataHandler()
	workerInstance.UpdateMetadata(
		[]string{"tinyllama"}, // Supported models
		100.0,                 // Tokens throughput
		8,                     // VRAM GB
		0.1,                   // Load
		"RTX 4090",            // GPU model
	)

	// Start advertising the worker
	workerInstance.AdvertiseModel(ctx, "tinyllama")

	t.Logf("Worker created with peer ID: %s", workerInstance.Host.ID().String())
	t.Logf("Worker peer addresses: %v", workerInstance.Host.Addrs())

	// Step 4: Initialize Consumer with DHT server as bootstrap peer
	t.Log("Step 4: Initializing Consumer with DHT server as bootstrap peer")
	consumerInstance, err := consumer.NewConsumerWithBootstrapPeers(ctx, logger, consumerPrivKey, []string{dhtPeerAddr})
	if err != nil {
		t.Fatalf("Failed to create consumer: %v", err)
	}

	// Start background discovery
	consumerInstance.StartBackgroundDiscovery()
	defer consumerInstance.StopBackgroundDiscovery()

	t.Logf("Consumer created with peer ID: %s", consumerInstance.GetPeerID())
	t.Logf("Consumer peer addresses: %v", consumerInstance.GetPeerAddrs())

	// Step 5: Start Consumer HTTP Server on a specific port
	t.Log("Step 5: Starting Consumer HTTP Server")
	consumerPort := 9003 // Use different port to avoid conflicts
	go func() {
		if err := consumerInstance.StartHTTPServer(consumerPort); err != nil && err != http.ErrServerClosed {
			t.Errorf("Consumer HTTP server failed: %v", err)
		}
	}()
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		consumerInstance.StopHTTPServer(shutdownCtx)
	}()

	// Give the HTTP server time to start
	time.Sleep(2 * time.Second)

	// Step 6: Wait for both nodes to show up in DHT
	t.Log("Step 6: Waiting for both nodes to show up in DHT")

	// Wait for worker to be discovered
	maxAttempts := 20
	attempt := 0
	workerFound := false
	workerPeerID := workerInstance.Host.ID().String()

	for attempt < maxAttempts {
		attempt++
		t.Logf("Attempt %d/%d: Checking if worker is discovered", attempt, maxAttempts)

		// Check if worker is connected to DHT
		if dhtServer.HasPeer(workerPeerID) {
			t.Logf("Worker peer ID %s found in DHT server's connected peers", workerPeerID)
			workerFound = true
			break
		}

		// Wait before next attempt
		time.Sleep(1 * time.Second)
	}

	if !workerFound {
		t.Errorf("Worker peer ID %s was not found in DHT server after %d attempts", workerPeerID, maxAttempts)
	} else {
		t.Logf("✅ SUCCESS: Worker peer ID %s found in DHT server", workerPeerID)
	}

	// Wait for consumer to be discovered
	attempt = 0
	consumerFound := false
	consumerPeerID := consumerInstance.GetPeerID()

	for attempt < maxAttempts {
		attempt++
		t.Logf("Attempt %d/%d: Checking if consumer is discovered", attempt, maxAttempts)

		// Check if consumer is connected to DHT
		if dhtServer.HasPeer(consumerPeerID) {
			t.Logf("Consumer peer ID %s found in DHT server's connected peers", consumerPeerID)
			consumerFound = true
			break
		}

		// Wait before next attempt
		time.Sleep(1 * time.Second)
	}

	if !consumerFound {
		t.Errorf("Consumer peer ID %s was not found in DHT server after %d attempts", consumerPeerID, maxAttempts)
	} else {
		t.Logf("✅ SUCCESS: Consumer peer ID %s found in DHT server", consumerPeerID)
	}

	// Step 7: Wait for consumer to discover the worker
	t.Log("Step 7: Waiting for consumer to discover the worker")

	attempt = 0
	workerDiscovered := false

	for attempt < maxAttempts {
		attempt++
		t.Logf("Attempt %d/%d: Checking if consumer discovered the worker", attempt, maxAttempts)

		availableWorkers := consumerInstance.GetAvailableWorkers()
		if len(availableWorkers) > 0 {
			t.Logf("Consumer discovered %d workers", len(availableWorkers))
			for workerID, workerInfo := range availableWorkers {
				t.Logf("Discovered worker: %s with models: %v", workerID, workerInfo.SupportedModels)
				if workerID == workerPeerID {
					workerDiscovered = true
					break
				}
			}
		}

		if workerDiscovered {
			break
		}

		// Wait before next attempt
		time.Sleep(1 * time.Second)
	}

	if !workerDiscovered {
		t.Errorf("Worker was not discovered by consumer after %d attempts", maxAttempts)
	} else {
		t.Logf("✅ SUCCESS: Worker discovered by consumer")
	}

	// Step 8: Send HTTP request to consumer and validate response
	t.Log("Step 8: Sending HTTP request to consumer and validating response")

	// Prepare the request
	requestBody := map[string]interface{}{
		"model": "tinyllama",
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": "Hello, how are you?",
			},
		},
		"stream": false,
	}

	requestJSON, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	// Send HTTP request to consumer
	url := fmt.Sprintf("http://localhost:%d/api/chat", consumerPort)
	t.Logf("Sending request to: %s", url)

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(requestJSON))
	if err != nil {
		t.Fatalf("Failed to send HTTP request: %v", err)
	}
	defer resp.Body.Close()

	// Check HTTP status code
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected HTTP 200, got %d", resp.StatusCode)
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("Response body: %s", string(body))
	} else {
		t.Logf("✅ SUCCESS: HTTP request returned status 200")
	}

	// Read and parse the response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	t.Logf("Response body: %s", string(respBody))

	// Parse the JSON response
	var response consumer.GenerateResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	// Validate the response structure
	if response.Model != "tinyllama" {
		t.Errorf("Expected model 'tinyllama', got '%s'", response.Model)
	}

	if response.Message.Role != "assistant" {
		t.Errorf("Expected message role 'assistant', got '%s'", response.Message.Role)
	}

	if response.Message.Content == "" {
		t.Error("Expected non-empty message content")
	}

	if !response.Done {
		t.Error("Expected response to be done")
	}

	if response.DoneReason != "done" {
		t.Errorf("Expected done reason 'done', got '%s'", response.DoneReason)
	}

	// Validate that the response content matches our mock server's expected output
	expectedContentPrefix := "This is a mock response from the Ollama API. You asked: Hello, how are you?"
	if response.Message.Content != expectedContentPrefix {
		t.Errorf("Expected response content to start with '%s', got '%s'", expectedContentPrefix, response.Message.Content)
	}

	t.Logf("✅ SUCCESS: Response validation passed")
	t.Logf("✅ SUCCESS: Full integration test completed successfully")
}

// TestMockOllamaServer tests the mock Ollama server independently
func TestMockOllamaServer(t *testing.T) {
	// Skip this test if not running integration tests
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Start mock server on a different port
	mockOllama := NewMockOllamaServer(11435)
	go func() {
		if err := mockOllama.Start(); err != nil && err != http.ErrServerClosed {
			t.Errorf("Mock Ollama server failed: %v", err)
		}
	}()
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		mockOllama.Stop(shutdownCtx)
	}()

	// Give the server time to start
	time.Sleep(1 * time.Second)

	// Test the mock server
	requestBody := MockOllamaRequest{
		Model: "tinyllama",
		Messages: []Message{
			{
				Role:    "user",
				Content: "Test message",
			},
		},
		Stream: false,
	}

	requestJSON, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	url := fmt.Sprintf("http://localhost:%d/api/chat", mockOllama.GetPort())
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(requestJSON))
	if err != nil {
		t.Fatalf("Failed to send HTTP request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected HTTP 200, got %d", resp.StatusCode)
	}

	var response MockOllamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if response.Model != "tinyllama" {
		t.Errorf("Expected model 'tinyllama', got '%s'", response.Model)
	}

	if response.Message.Role != "assistant" {
		t.Errorf("Expected message role 'assistant', got '%s'", response.Message.Role)
	}

	expectedContent := "This is a mock response from the Ollama API. You asked: Test message"
	if response.Message.Content != expectedContent {
		t.Errorf("Expected content '%s', got '%s'", expectedContent, response.Message.Content)
	}

	t.Logf("✅ SUCCESS: Mock Ollama server test passed")
}
