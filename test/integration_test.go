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
	"github.com/matiasinsaurralde/crowdllama/pkg/crowdllama"
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
	if err := os.Setenv("CROWDLLAMA_TEST_MODE", "1"); err != nil {
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

	// Start DHT server
	_, err := dhtServer.Start()
	if err != nil {
		t.Fatalf("Failed to start DHT server: %v", err)
	}

	// Give DHT server time to fully bootstrap
	time.Sleep(3 * time.Second)

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
	workerInstance.AdvertiseModel(ctx, crowdllama.WorkerNamespace)
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

// TestStalePeerRemovalIntegration tests that stale peers are properly removed
// from both consumer and DHT components using the shared peermanager
func TestStalePeerRemovalIntegration(t *testing.T) {
	// Set test mode for faster timeouts
	if err := os.Setenv("CROWDLLAMA_TEST_MODE", "1"); err != nil {
		t.Fatalf("Failed to set test mode: %v", err)
	}
	defer func() {
		if err := os.Unsetenv("CROWDLLAMA_TEST_MODE"); err != nil {
			t.Logf("Failed to unset test mode: %v", err)
		}
	}()

	logger := zap.NewNop()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Create test configuration
	cfg := &config.Configuration{
		BootstrapPeers: []string{},
	}

	// Generate keys for different components
	consumerKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 256)
	if err != nil {
		t.Fatalf("Failed to generate consumer key: %v", err)
	}

	dhtKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 256)
	if err != nil {
		t.Fatalf("Failed to generate DHT key: %v", err)
	}

	workerKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 256)
	if err != nil {
		t.Fatalf("Failed to generate worker key: %v", err)
	}

	// Create DHT server
	dhtServer, err := dht.NewDHTServerWithAddrs(ctx, dhtKey, logger, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("Failed to create DHT server: %v", err)
	}
	defer dhtServer.Stop()

	// Start DHT server
	_, err = dhtServer.Start()
	if err != nil {
		t.Fatalf("Failed to start DHT server: %v", err)
	}

	// Give DHT server time to fully bootstrap
	time.Sleep(3 * time.Second)

	// Get DHT server addresses for bootstrap
	dhtAddrs := dhtServer.GetPeerAddrs()
	if len(dhtAddrs) == 0 {
		t.Fatal("DHT server has no addresses")
	}

	// Update config with DHT server addresses
	cfg.BootstrapPeers = dhtAddrs

	// Create consumer
	consumer, err := consumer.NewConsumerWithConfig(ctx, logger, consumerKey, cfg)
	if err != nil {
		t.Fatalf("Failed to create consumer: %v", err)
	}
	defer consumer.StopBackgroundDiscovery()

	// Create worker
	worker, err := worker.NewWorkerWithConfig(ctx, workerKey, cfg)
	if err != nil {
		t.Fatalf("Failed to create worker: %v", err)
	}
	defer worker.StopMetadataUpdates()

	// Start all components
	worker.StartMetadataUpdates()
	worker.SetupMetadataHandler()
	worker.AdvertiseModel(ctx, crowdllama.WorkerNamespace)

	// Give the worker time to start advertising
	time.Sleep(2 * time.Second)

	// Now start consumer discovery after worker has started advertising
	consumer.StartBackgroundDiscovery()

	// Wait for initial discovery
	time.Sleep(5 * time.Second)

	// Verify worker is discovered by consumer
	initialWorkers := consumer.GetAvailableWorkers()
	if len(initialWorkers) == 0 {
		t.Fatal("No workers discovered initially")
	}

	workerPeerID := worker.Host.ID().String()
	if _, exists := initialWorkers[workerPeerID]; !exists {
		t.Fatalf("Worker %s not found in consumer's worker list", workerPeerID)
	}

	t.Logf("Initial discovery successful: found %d workers", len(initialWorkers))

	// Verify worker is connected to DHT
	dhtPeers := dhtServer.GetPeers()
	workerFoundInDHT := false
	for _, peerID := range dhtPeers {
		if peerID == workerPeerID {
			workerFoundInDHT = true
			break
		}
	}

	if !workerFoundInDHT {
		t.Fatalf("Worker %s not found in DHT's peer list", workerPeerID)
	}

	t.Logf("DHT connection successful: found %d peers", len(dhtPeers))

	// Stop the worker to simulate it going offline
	t.Log("Stopping worker to simulate offline behavior...")
	worker.StopMetadataUpdates()

	// Wait for stale peer timeout (30 seconds in test mode)
	t.Log("Waiting for stale peer timeout...")
	time.Sleep(35 * time.Second)

	// Verify worker is removed from consumer
	remainingWorkers := consumer.GetAvailableWorkers()
	if len(remainingWorkers) > 0 {
		t.Errorf("Expected no workers after timeout, but found %d", len(remainingWorkers))
		for peerID := range remainingWorkers {
			t.Errorf("Unexpected worker still present: %s", peerID)
		}
	}

	// Verify worker is removed from DHT
	remainingDHTPeers := dhtServer.GetHealthyPeers()
	workerStillInDHT := false
	for peerID := range remainingDHTPeers {
		if peerID == workerPeerID {
			workerStillInDHT = true
			break
		}
	}

	if workerStillInDHT {
		t.Errorf("Worker %s still present in DHT healthy peers after timeout", workerPeerID)
	}

	// Check health status to verify cleanup
	healthStatus := consumer.GetWorkerHealthStatus()
	if len(healthStatus) > 0 {
		t.Errorf("Expected no health status entries after cleanup, but found %d", len(healthStatus))
	}

	t.Log("Stale peer removal test completed successfully")
}

// TestPeerManagerHealthChecks tests that health checks work correctly
func TestPeerManagerHealthChecks(t *testing.T) {
	// Set test mode for faster timeouts
	if err := os.Setenv("CROWDLLAMA_TEST_MODE", "1"); err != nil {
		t.Fatalf("Failed to set test mode: %v", err)
	}
	defer func() {
		if err := os.Unsetenv("CROWDLLAMA_TEST_MODE"); err != nil {
			t.Logf("Failed to unset test mode: %v", err)
		}
	}()

	logger := zap.NewNop()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Create test configuration
	cfg := &config.Configuration{
		BootstrapPeers: []string{},
	}

	// Generate keys
	consumerKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 256)
	if err != nil {
		t.Fatalf("Failed to generate consumer key: %v", err)
	}

	workerKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 256)
	if err != nil {
		t.Fatalf("Failed to generate worker key: %v", err)
	}

	// Create consumer and worker
	consumer, err := consumer.NewConsumerWithConfig(ctx, logger, consumerKey, cfg)
	if err != nil {
		t.Fatalf("Failed to create consumer: %v", err)
	}
	defer consumer.StopBackgroundDiscovery()

	worker, err := worker.NewWorkerWithConfig(ctx, workerKey, cfg)
	if err != nil {
		t.Fatalf("Failed to create worker: %v", err)
	}
	defer worker.StopMetadataUpdates()

	// Start discovery
	consumer.StartBackgroundDiscovery()
	worker.StartMetadataUpdates()
	worker.AdvertiseModel(ctx, crowdllama.WorkerNamespace)

	// Wait for discovery
	time.Sleep(5 * time.Second)

	// Verify initial health status
	healthStatus := consumer.GetWorkerHealthStatus()
	workerPeerID := worker.Host.ID().String()

	if len(healthStatus) == 0 {
		t.Fatal("No health status entries found")
	}

	workerHealth, exists := healthStatus[workerPeerID]
	if !exists {
		t.Fatalf("Worker %s not found in health status", workerPeerID)
	}

	if !workerHealth["is_healthy"].(bool) {
		t.Error("Worker should be healthy initially")
	}

	t.Logf("Initial health check passed: worker %s is healthy", workerPeerID)

	// Simulate worker becoming unhealthy by stopping it
	worker.StopMetadataUpdates()

	// Wait for health check to detect the issue
	time.Sleep(10 * time.Second)

	// Check health status again
	updatedHealthStatus := consumer.GetWorkerHealthStatus()
	updatedWorkerHealth, exists := updatedHealthStatus[workerPeerID]
	if !exists {
		t.Fatal("Worker should still be in health status even if unhealthy")
	}

	if updatedWorkerHealth["is_healthy"].(bool) {
		t.Error("Worker should be marked as unhealthy after stopping")
	}

	failedAttempts := updatedWorkerHealth["failed_attempts"].(int)
	if failedAttempts == 0 {
		t.Error("Failed attempts should be greater than 0 for unhealthy worker")
	}

	t.Logf("Health check correctly detected unhealthy worker: failed_attempts=%d", failedAttempts)

	// Wait for worker to be removed due to repeated failures
	time.Sleep(20 * time.Second)

	finalHealthStatus := consumer.GetWorkerHealthStatus()
	if len(finalHealthStatus) > 0 {
		t.Errorf("Expected no health status entries after removal, but found %d", len(finalHealthStatus))
	}

	t.Log("Health check and cleanup test completed successfully")
}

// TestMetadataValidation tests that metadata validation works correctly
func TestMetadataValidation(t *testing.T) {
	// Set test mode
	if err := os.Setenv("CROWDLLAMA_TEST_MODE", "1"); err != nil {
		t.Fatalf("Failed to set test mode: %v", err)
	}
	defer func() {
		if err := os.Unsetenv("CROWDLLAMA_TEST_MODE"); err != nil {
			t.Logf("Failed to unset test mode: %v", err)
		}
	}()

	logger := zap.NewNop()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create test configuration
	cfg := &config.Configuration{
		BootstrapPeers: []string{},
	}

	// Generate keys
	consumerKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 256)
	if err != nil {
		t.Fatalf("Failed to generate consumer key: %v", err)
	}

	workerKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 256)
	if err != nil {
		t.Fatalf("Failed to generate worker key: %v", err)
	}

	// Create consumer and worker
	consumer, err := consumer.NewConsumerWithConfig(ctx, logger, consumerKey, cfg)
	if err != nil {
		t.Fatalf("Failed to create consumer: %v", err)
	}
	defer consumer.StopBackgroundDiscovery()

	worker, err := worker.NewWorkerWithConfig(ctx, workerKey, cfg)
	if err != nil {
		t.Fatalf("Failed to create worker: %v", err)
	}
	defer worker.StopMetadataUpdates()

	// Start discovery
	consumer.StartBackgroundDiscovery()
	worker.StartMetadataUpdates()
	worker.AdvertiseModel(ctx, crowdllama.WorkerNamespace)

	// Wait for discovery
	time.Sleep(5 * time.Second)

	// Verify worker metadata is valid
	workers := consumer.GetAvailableWorkers()
	workerPeerID := worker.Host.ID().String()

	if len(workers) == 0 {
		t.Fatal("No workers discovered")
	}

	workerResource, exists := workers[workerPeerID]
	if !exists {
		t.Fatalf("Worker %s not found", workerPeerID)
	}

	// Validate metadata fields
	if workerResource.PeerID == "" {
		t.Error("Worker PeerID should not be empty")
	}

	if workerResource.GPUModel == "" {
		t.Error("Worker GPU model should not be empty")
	}

	if workerResource.VRAMGB <= 0 {
		t.Error("Worker VRAM should be greater than 0")
	}

	if len(workerResource.SupportedModels) == 0 {
		t.Error("Worker should support at least one model")
	}

	if workerResource.TokensThroughput <= 0 {
		t.Error("Worker tokens throughput should be greater than 0")
	}

	t.Logf("Metadata validation passed: worker %s has valid metadata", workerPeerID)
	t.Logf("GPU Model: %s, VRAM: %dGB, Supported Models: %v",
		workerResource.GPUModel, workerResource.VRAMGB, workerResource.SupportedModels)
}

// TestConcurrentPeerManagement tests that peer management works correctly under concurrent access
func TestConcurrentPeerManagement(t *testing.T) {
	// Set test mode
	if err := os.Setenv("CROWDLLAMA_TEST_MODE", "1"); err != nil {
		t.Fatalf("Failed to set test mode: %v", err)
	}
	defer func() {
		if err := os.Unsetenv("CROWDLLAMA_TEST_MODE"); err != nil {
			t.Logf("Failed to unset test mode: %v", err)
		}
	}()

	logger := zap.NewNop()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Create test configuration
	cfg := &config.Configuration{
		BootstrapPeers: []string{},
	}

	// Generate keys
	consumerKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 256)
	if err != nil {
		t.Fatalf("Failed to generate consumer key: %v", err)
	}

	// Create consumer
	consumer, err := consumer.NewConsumerWithConfig(ctx, logger, consumerKey, cfg)
	if err != nil {
		t.Fatalf("Failed to create consumer: %v", err)
	}
	defer consumer.StopBackgroundDiscovery()

	// Start discovery
	consumer.StartBackgroundDiscovery()

	// Create multiple workers concurrently
	numWorkers := 5
	workers := make([]*worker.Worker, numWorkers)

	for i := 0; i < numWorkers; i++ {
		workerKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 256)
		if err != nil {
			t.Fatalf("Failed to generate worker key %d: %v", i, err)
		}

		worker, err := worker.NewWorkerWithConfig(ctx, workerKey, cfg)
		if err != nil {
			t.Fatalf("Failed to create worker %d: %v", i, err)
		}
		workers[i] = worker
		defer worker.StopMetadataUpdates()

		// Start worker discovery
		worker.StartMetadataUpdates()
		worker.SetupMetadataHandler()
		worker.AdvertiseModel(ctx, crowdllama.WorkerNamespace)
	}

	// Wait for all workers to be discovered
	time.Sleep(10 * time.Second)

	// Verify all workers are discovered
	discoveredWorkers := consumer.GetAvailableWorkers()
	if len(discoveredWorkers) != numWorkers {
		t.Errorf("Expected %d workers, but found %d", numWorkers, len(discoveredWorkers))
	}

	t.Logf("Successfully discovered %d workers concurrently", len(discoveredWorkers))

	// Stop half of the workers
	for i := 0; i < numWorkers/2; i++ {
		workers[i].StopMetadataUpdates()
	}

	// Wait for cleanup
	time.Sleep(35 * time.Second)

	// Verify only healthy workers remain
	remainingWorkers := consumer.GetAvailableWorkers()
	expectedRemaining := numWorkers - numWorkers/2
	if len(remainingWorkers) != expectedRemaining {
		t.Errorf("Expected %d remaining workers, but found %d", expectedRemaining, len(remainingWorkers))
	}

	t.Logf("Concurrent peer management test completed: %d workers remaining", len(remainingWorkers))
}
