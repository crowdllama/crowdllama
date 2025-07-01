package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"

	"github.com/matiasinsaurralde/crowdllama/internal/discovery"
	"github.com/matiasinsaurralde/crowdllama/internal/keys"
	"github.com/matiasinsaurralde/crowdllama/pkg/config"
	"github.com/matiasinsaurralde/crowdllama/pkg/consumer"
	"github.com/matiasinsaurralde/crowdllama/pkg/dht"
	"github.com/matiasinsaurralde/crowdllama/pkg/worker"
)

const (
	ciEnvironment = "true"
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
		if err := json.NewEncoder(w).Encode(response); err != nil {
			http.Error(w, "Failed to encode response", http.StatusInternalServerError)
			return
		}
	})

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           mux,
		ReadHeaderTimeout: 30 * time.Second,
	}

	return &MockOllamaServer{
		server: server,
		port:   port,
	}
}

// Start starts the mock Ollama server
func (m *MockOllamaServer) Start() error {
	if err := m.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("mock server listen and serve: %w", err)
	}
	return nil
}

// Stop stops the mock Ollama server
func (m *MockOllamaServer) Stop(ctx context.Context) error {
	if err := m.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("mock server shutdown: %w", err)
	}
	return nil
}

// GetPort returns the port the server is running on
func (m *MockOllamaServer) GetPort() int {
	return m.port
}

// getRandomPort returns a random available port
func getRandomPort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		return 0, fmt.Errorf("failed to resolve TCP address: %w", err)
	}

	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, fmt.Errorf("failed to listen on TCP address: %w", err)
	}
	defer func() {
		if closeErr := l.Close(); closeErr != nil {
			// Log the error but don't fail the test for this
			fmt.Printf("Warning: failed to close listener: %v\n", closeErr)
		}
	}()

	return l.Addr().(*net.TCPAddr).Port, nil
}

// TestFullIntegration tests the complete end-to-end flow
func TestFullIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Set test mode environment variable for shorter intervals
	if err := os.Setenv("CROW DLLAMA_TEST_MODE", "1"); err != nil {
		t.Logf("Failed to set test mode environment variable: %v", err)
	}

	// Enable test mode for shorter intervals
	discovery.SetTestMode()

	// Increase timeout for CI environment which is slower
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second) // 5 minutes for CI
	defer cancel()
	logger, _ := zap.NewDevelopment()

	tempDir := stepCreateTempDir(t)
	defer stepCleanupTempDir(t, tempDir)

	dhtPrivKey, workerPrivKey, consumerPrivKey := stepCreateKeys(t, tempDir, logger)

	mockOllamaPort := 11435
	mockOllama := stepStartMockOllamaServer(t, mockOllamaPort)
	defer stepShutdownMockOllamaServer(t, mockOllama)
	dhtServer := stepInitDHTServerFull(ctx, t, dhtPrivKey, logger)
	defer dhtServer.Stop()
	dhtPeerAddr := stepStartDHTServerFull(t, dhtServer)
	workerInstance := stepInitWorkerFull(ctx, t, workerPrivKey, dhtPeerAddr, mockOllamaPort)
	stepSetupWorkerMetadataFull(t, workerInstance)
	stepAdvertiseWorkerFull(ctx, t, workerInstance)
	consumerInstance := stepInitConsumerFull(ctx, t, logger, consumerPrivKey, dhtPeerAddr)
	stepStartConsumerDiscoveryFull(t, consumerInstance)
	defer consumerInstance.StopBackgroundDiscovery()
	consumerPort := 9003
	stepStartConsumerHTTPServerFull(t, consumerInstance, consumerPort)
	defer stepShutdownConsumerHTTPServerFull(t, consumerInstance)
	stepWaitForDiscoveryFull(t, dhtServer, workerInstance, consumerInstance)
	stepSendAndValidateRequestFull(ctx, t, consumerPort)
}

func stepCreateTempDir(t *testing.T) string {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "crowdllama-full-integration-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	return tempDir
}

func stepCleanupTempDir(t *testing.T, tempDir string) {
	t.Helper()
	if removeErr := os.RemoveAll(tempDir); removeErr != nil {
		t.Logf("Failed to remove temp dir: %v", removeErr)
	}
}

func stepCreateKeys(t *testing.T, tempDir string, logger *zap.Logger) (dhtPrivKey, workerPrivKey, consumerPrivKey crypto.PrivKey) {
	t.Helper()
	dhtKeyPath := filepath.Join(tempDir, "dht.key")
	workerKeyPath := filepath.Join(tempDir, "worker.key")
	consumerKeyPath := filepath.Join(tempDir, "consumer.key")
	dhtKeyManager := keys.NewKeyManager(dhtKeyPath, logger)
	workerKeyManager := keys.NewKeyManager(workerKeyPath, logger)
	consumerKeyManager := keys.NewKeyManager(consumerKeyPath, logger)
	var err error
	dhtPrivKey, err = dhtKeyManager.GetOrCreatePrivateKey()
	if err != nil {
		t.Fatalf("Failed to create DHT private key: %v", err)
	}
	workerPrivKey, err = workerKeyManager.GetOrCreatePrivateKey()
	if err != nil {
		t.Fatalf("Failed to create worker private key: %v", err)
	}
	consumerPrivKey, err = consumerKeyManager.GetOrCreatePrivateKey()
	if err != nil {
		t.Fatalf("Failed to create consumer private key: %v", err)
	}
	return
}

func stepInitDHTServerFull(ctx context.Context, t *testing.T, dhtPrivKey crypto.PrivKey, logger *zap.Logger) *dht.Server {
	t.Helper()

	// Debug: Log network interfaces in CI
	if os.Getenv("CI") == ciEnvironment {
		t.Logf("🔍 CI Environment detected - debugging network interfaces")
		addrs, err := net.InterfaceAddrs()
		if err != nil {
			t.Logf("Failed to get interface addresses: %v", err)
		} else {
			for _, addr := range addrs {
				t.Logf("Network interface: %s", addr.String())
			}
		}
	}

	// Get a random available port to avoid conflicts between tests
	dhtPort, err := getRandomPort()
	if err != nil {
		t.Fatalf("Failed to get random port for DHT server: %v", err)
	}

	// Use localhost addresses with dynamic port for testing to avoid network interface issues
	testListenAddrs := []string{
		fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", dhtPort),
		fmt.Sprintf("/ip4/127.0.0.1/udp/%d/quic-v1", dhtPort),
	}

	t.Logf("Using DHT server port: %d", dhtPort)

	dhtServer, err := dht.NewDHTServerWithAddrs(ctx, dhtPrivKey, logger, testListenAddrs)
	if err != nil {
		t.Fatalf("Failed to create DHT server: %v", err)
	}
	return dhtServer
}

func stepStartDHTServerFull(t *testing.T, dhtServer *dht.Server) string {
	t.Helper()

	// Debug: Log DHT server peer ID in CI
	if os.Getenv("CI") == ciEnvironment {
		t.Logf("🔍 CI Debug: DHT server peer ID: %s", dhtServer.GetPeerID())
		t.Logf("🔍 CI Debug: DHT server all addresses: %v", dhtServer.GetPeerAddrs())
	}

	dhtPeerAddr, err := dhtServer.Start()
	if err != nil {
		t.Fatalf("Failed to start DHT server: %v", err)
	}

	// Add a delay to ensure the DHT server is fully ready
	time.Sleep(3 * time.Second)

	return dhtPeerAddr
}

func stepInitWorkerFull(
	ctx context.Context,
	t *testing.T,
	workerPrivKey crypto.PrivKey,
	dhtPeerAddr string,
	mockOllamaPort int,
) *worker.Worker {
	t.Helper()
	mockOllamaBaseURL := fmt.Sprintf("http://localhost:%d", mockOllamaPort)

	// Create config with custom Ollama base URL and bootstrap peers
	cfg := config.NewConfiguration()
	cfg.OllamaBaseURL = mockOllamaBaseURL
	cfg.BootstrapPeers = []string{dhtPeerAddr}

	// Try to create worker with retry logic
	var workerInstance *worker.Worker
	var err error
	maxRetries := 5

	for attempt := 1; attempt <= maxRetries; attempt++ {
		t.Logf("Attempt %d/%d: Creating worker with bootstrap peer: %s", attempt, maxRetries, dhtPeerAddr)

		workerInstance, err = worker.NewWorkerWithConfig(ctx, workerPrivKey, cfg)
		if err == nil {
			t.Logf("✅ Worker created successfully on attempt %d", attempt)
			break
		}

		t.Logf("❌ Failed to create worker on attempt %d: %v", attempt, err)
		if attempt < maxRetries {
			t.Logf("Retrying in 3 seconds...")
			time.Sleep(3 * time.Second)
		}
	}

	if err != nil {
		t.Fatalf("Failed to create worker after %d attempts: %v", maxRetries, err)
	}

	return workerInstance
}

func stepSetupWorkerMetadataFull(t *testing.T, workerInstance *worker.Worker) {
	t.Helper()
	workerInstance.SetupMetadataHandler()
	if err := workerInstance.UpdateMetadata(); err != nil {
		t.Fatalf("UpdateMetadata failed: %v", err)
	}
}

func stepAdvertiseWorkerFull(ctx context.Context, t *testing.T, workerInstance *worker.Worker) {
	t.Helper()
	workerInstance.AdvertiseModel(ctx, "tinyllama")
}

func stepInitConsumerFull(
	ctx context.Context,
	t *testing.T,
	logger *zap.Logger,
	consumerPrivKey crypto.PrivKey,
	dhtPeerAddr string,
) *consumer.Consumer {
	t.Helper()

	// Debug: Log peer IDs in CI
	if os.Getenv("CI") == ciEnvironment {
		consumerPeerID, err := peer.IDFromPublicKey(consumerPrivKey.GetPublic())
		if err != nil {
			t.Logf("Failed to get consumer peer ID: %v", err)
		} else {
			t.Logf("🔍 CI Debug: Consumer private key peer ID: %s", consumerPeerID.String())
		}
		t.Logf("🔍 CI Debug: DHT peer address: %s", dhtPeerAddr)
	}

	// Try to create consumer with retry logic
	var consumerInstance *consumer.Consumer
	var err error
	maxRetries := 5

	for attempt := 1; attempt <= maxRetries; attempt++ {
		t.Logf("Attempt %d/%d: Creating consumer with bootstrap peer: %s", attempt, maxRetries, dhtPeerAddr)

		cfg := config.NewConfiguration()
		cfg.BootstrapPeers = []string{dhtPeerAddr}
		consumerInstance, err = consumer.NewConsumerWithConfig(ctx, logger, consumerPrivKey, cfg)
		if err == nil {
			t.Logf("✅ Consumer created successfully on attempt %d", attempt)
			break
		}

		t.Logf("❌ Failed to create consumer on attempt %d: %v", attempt, err)
		if attempt < maxRetries {
			t.Logf("Retrying in 2 seconds...")
			time.Sleep(2 * time.Second)
		}
	}

	if err != nil {
		t.Fatalf("Failed to create consumer after %d attempts: %v", maxRetries, err)
	}

	return consumerInstance
}

func stepStartConsumerDiscoveryFull(t *testing.T, consumerInstance *consumer.Consumer) {
	t.Helper()
	consumerInstance.StartBackgroundDiscovery()
}

func stepStartConsumerHTTPServerFull(t *testing.T, consumerInstance *consumer.Consumer, consumerPort int) {
	t.Helper()
	go func() {
		if startErr := consumerInstance.StartHTTPServer(consumerPort); startErr != nil && startErr != http.ErrServerClosed {
			t.Errorf("Consumer HTTP server failed: %v", startErr)
		}
	}()
	time.Sleep(2 * time.Second)
}

func stepShutdownConsumerHTTPServerFull(t *testing.T, consumerInstance *consumer.Consumer) {
	t.Helper()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if stopErr := consumerInstance.StopHTTPServer(shutdownCtx); stopErr != nil {
		t.Logf("Failed to stop HTTP server: %v", stopErr)
	}
}

func stepWaitForDiscoveryFull(t *testing.T, dhtServer *dht.Server, workerInstance *worker.Worker, consumerInstance *consumer.Consumer) {
	t.Helper()
	stepWaitForWorkerDiscovery(t, dhtServer, workerInstance)
	stepWaitForConsumerDiscovery(t, dhtServer, consumerInstance)
	stepWaitForWorkerDiscoveryByConsumer(t, consumerInstance, workerInstance)
}

func stepWaitForWorkerDiscovery(t *testing.T, dhtServer *dht.Server, workerInstance *worker.Worker) {
	t.Helper()
	attempt := 0
	workerFound := false
	workerPeerID := workerInstance.Host.ID().String()
	for {
		attempt++
		t.Logf("Attempt %d: Checking if worker is discovered", attempt)
		if dhtServer.HasPeer(workerPeerID) {
			t.Logf("Worker peer ID %s found in DHT server's connected peers", workerPeerID)
			workerFound = true
			break
		}
		time.Sleep(500 * time.Millisecond) // Shorter interval for faster CI testing
	}
	if !workerFound {
		t.Errorf("Worker peer ID %s was not found in DHT server after %d attempts", workerPeerID, attempt)
	} else {
		t.Logf("✅ SUCCESS: Worker peer ID %s found in DHT server", workerPeerID)
	}
}

func stepWaitForConsumerDiscovery(t *testing.T, dhtServer *dht.Server, consumerInstance *consumer.Consumer) {
	t.Helper()
	attempt := 0
	consumerFound := false
	consumerPeerID := consumerInstance.Host.ID().String()

	// Debug: Log consumer connection status in CI
	if os.Getenv("CI") == ciEnvironment {
		t.Logf("🔍 CI Debug: Consumer peer ID to find: %s", consumerPeerID)
		t.Logf("🔍 CI Debug: Consumer connected peers: %v", consumerInstance.Host.Network().Peers())
		t.Logf("🔍 CI Debug: Consumer addresses: %v", consumerInstance.Host.Addrs())
	}

	for {
		attempt++
		t.Logf("Attempt %d: Checking if consumer is discovered", attempt)
		if dhtServer.HasPeer(consumerPeerID) {
			t.Logf("Consumer peer ID %s found in DHT server's connected peers", consumerPeerID)
			consumerFound = true
			break
		}

		// Debug: Log more details in CI
		if os.Getenv("CI") == ciEnvironment && attempt%10 == 0 {
			t.Logf("🔍 CI Debug: DHT server connected peers: %v", dhtServer.GetPeers())
			t.Logf("🔍 CI Debug: Consumer still trying to connect...")
		}

		time.Sleep(500 * time.Millisecond) // Shorter interval for faster CI testing
	}
	if !consumerFound {
		t.Errorf("Consumer peer ID %s was not found in DHT server after %d attempts", consumerPeerID, attempt)
	} else {
		t.Logf("✅ SUCCESS: Consumer peer ID %s found in DHT server", consumerPeerID)
	}
}

func stepWaitForWorkerDiscoveryByConsumer(t *testing.T, consumerInstance *consumer.Consumer, workerInstance *worker.Worker) {
	t.Helper()
	attempt := 0
	workerFound := false
	workerPeerID := workerInstance.Host.ID().String()
	for {
		attempt++
		t.Logf("Attempt %d: Checking if consumer discovered the worker", attempt)
		availableWorkers := consumerInstance.GetAvailableWorkers()
		if len(availableWorkers) > 0 {
			t.Logf("Consumer discovered %d workers", len(availableWorkers))
			for workerID, workerInfo := range availableWorkers {
				t.Logf("Discovered worker: %s with models: %v", workerID, workerInfo.SupportedModels)
				if workerID == workerPeerID {
					workerFound = true
					break
				}
			}
		}
		if workerFound {
			break
		}
		time.Sleep(500 * time.Millisecond) // Shorter interval for faster CI testing
	}
	if !workerFound {
		t.Errorf("Worker was not discovered by consumer after %d attempts", attempt)
	} else {
		t.Logf("✅ SUCCESS: Worker discovered by consumer")
	}
}

func stepSendAndValidateRequestFull(ctx context.Context, t *testing.T, consumerPort int) {
	t.Helper()
	url := fmt.Sprintf("http://localhost:%d/api/chat", consumerPort)
	t.Logf("Sending request to: %s", url)

	// Create a longer timeout for CI environment
	client := &http.Client{
		Timeout: 30 * time.Second, // Increased timeout for CI
	}

	requestBody := map[string]interface{}{
		"model": "tinyllama",
		"messages": []map[string]string{
			{"role": "user", "content": "Hello, how are you?"},
		},
		"stream": false,
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("Failed to marshal request body: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to send HTTP request: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Logf("Failed to close response body: %v", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	} else {
		t.Logf("✅ SUCCESS: HTTP request returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	t.Logf("Response body: %s", string(body))
	t.Logf("✅ SUCCESS: Response validation passed")
	t.Logf("✅ SUCCESS: Full integration test completed successfully")
}

// TestMockOllamaServer tests the mock Ollama server independently
func TestMockOllamaServer(t *testing.T) {
	// Skip this test if not running integration tests
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mockOllama := setupMockOllamaServer(t)
	defer shutdownMockOllamaServer(t, mockOllama)

	// Give the server time to start
	time.Sleep(1 * time.Second)

	testMockOllamaRequest(t, mockOllama)
}

func setupMockOllamaServer(t *testing.T) *MockOllamaServer {
	t.Helper()
	mockOllama := NewMockOllamaServer(11435)
	go func() {
		if err := mockOllama.Start(); err != nil && err != http.ErrServerClosed {
			t.Errorf("Mock Ollama server failed: %v", err)
		}
	}()
	return mockOllama
}

func shutdownMockOllamaServer(t *testing.T, mockOllama *MockOllamaServer) {
	t.Helper()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if stopErr := mockOllama.Stop(shutdownCtx); stopErr != nil {
		t.Logf("Failed to stop mock Ollama server: %v", stopErr)
	}
}

func testMockOllamaRequest(t *testing.T, mockOllama *MockOllamaServer) {
	t.Helper()
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

	if !strings.HasPrefix(url, "http://localhost:") {
		t.Fatalf("Invalid URL for testing: %s", url)
	}

	req, reqErr := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewBuffer(requestJSON))
	if reqErr != nil {
		t.Fatalf("Failed to create HTTP request: %v", reqErr)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to send HTTP request: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Logf("Failed to close response body: %v", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected HTTP 200, got %d", resp.StatusCode)
	}

	var response MockOllamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	validateMockOllamaResponse(t, &response)
}

func validateMockOllamaResponse(t *testing.T, response *MockOllamaResponse) {
	t.Helper()
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

func stepStartMockOllamaServer(t *testing.T, port int) *MockOllamaServer {
	t.Helper()
	mockOllama := NewMockOllamaServer(port)
	go func() {
		if startErr := mockOllama.Start(); startErr != nil && startErr != http.ErrServerClosed {
			t.Errorf("Mock Ollama server failed: %v", startErr)
		}
	}()
	time.Sleep(1 * time.Second)
	return mockOllama
}

func stepShutdownMockOllamaServer(t *testing.T, mockOllama *MockOllamaServer) {
	t.Helper()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if stopErr := mockOllama.Stop(shutdownCtx); stopErr != nil {
		t.Logf("Failed to stop mock Ollama server: %v", stopErr)
	}
}
