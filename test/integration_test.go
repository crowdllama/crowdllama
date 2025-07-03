package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
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
	consumerpkg "github.com/matiasinsaurralde/crowdllama/pkg/consumer"
	"github.com/matiasinsaurralde/crowdllama/pkg/crowdllama"
	"github.com/matiasinsaurralde/crowdllama/pkg/dht"
	workerpkg "github.com/matiasinsaurralde/crowdllama/pkg/worker"
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
) *workerpkg.Worker {
	t.Helper()
	mockOllamaBaseURL := fmt.Sprintf("http://localhost:%d", mockOllamaPort)

	// Create config with custom Ollama base URL and bootstrap peers
	cfg := config.NewConfiguration()
	cfg.OllamaBaseURL = mockOllamaBaseURL
	cfg.BootstrapPeers = []string{dhtPeerAddr}

	// Try to create worker with retry logic
	var workerInstance *workerpkg.Worker
	var err error
	maxRetries := 5

	for attempt := 1; attempt <= maxRetries; attempt++ {
		t.Logf("Attempt %d/%d: Creating worker with bootstrap peer: %s", attempt, maxRetries, dhtPeerAddr)

		workerInstance, err = workerpkg.NewWorkerWithConfig(ctx, workerPrivKey, cfg)
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

func stepSetupWorkerMetadataFull(t *testing.T, workerInstance *workerpkg.Worker) {
	t.Helper()
	workerInstance.SetupMetadataHandler()
	if err := workerInstance.UpdateMetadata(); err != nil {
		t.Fatalf("UpdateMetadata failed: %v", err)
	}
}

func stepAdvertiseWorkerFull(ctx context.Context, t *testing.T, workerInstance *workerpkg.Worker) {
	t.Helper()
	workerInstance.AdvertiseModel(ctx, crowdllama.WorkerNamespace)
}

func stepInitConsumerFull(
	ctx context.Context,
	t *testing.T,
	logger *zap.Logger,
	consumerPrivKey crypto.PrivKey,
	dhtPeerAddr string,
) *consumerpkg.Consumer {
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
	var consumerInstance *consumerpkg.Consumer
	var err error
	maxRetries := 5

	for attempt := 1; attempt <= maxRetries; attempt++ {
		t.Logf("Attempt %d/%d: Creating consumer with bootstrap peer: %s", attempt, maxRetries, dhtPeerAddr)

		cfg := config.NewConfiguration()
		cfg.BootstrapPeers = []string{dhtPeerAddr}
		consumerInstance, err = consumerpkg.NewConsumerWithConfig(ctx, logger, consumerPrivKey, cfg)
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

func stepStartConsumerDiscoveryFull(t *testing.T, consumerInstance *consumerpkg.Consumer) {
	t.Helper()
	consumerInstance.StartBackgroundDiscovery()
}

func stepStartConsumerHTTPServerFull(t *testing.T, consumerInstance *consumerpkg.Consumer, consumerPort int) {
	t.Helper()
	go func() {
		if startErr := consumerInstance.StartHTTPServer(consumerPort); startErr != nil && startErr != http.ErrServerClosed {
			t.Errorf("Consumer HTTP server failed: %v", startErr)
		}
	}()
	time.Sleep(2 * time.Second)
}

func stepShutdownConsumerHTTPServerFull(t *testing.T, consumerInstance *consumerpkg.Consumer) {
	t.Helper()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if stopErr := consumerInstance.StopHTTPServer(shutdownCtx); stopErr != nil {
		t.Logf("Failed to stop HTTP server: %v", stopErr)
	}
}

func stepWaitForDiscoveryFull(
	t *testing.T,
	dhtServer *dht.Server,
	workerInstance *workerpkg.Worker,
	consumerInstance *consumerpkg.Consumer,
) {
	t.Helper()
	stepWaitForWorkerDiscovery(t, dhtServer, workerInstance)
	stepWaitForWorkerDiscoveryByConsumer(
		t,
		consumerInstance,
		workerInstance,
	)
}

func stepWaitForWorkerDiscovery(t *testing.T, dhtServer *dht.Server, workerInstance *workerpkg.Worker) {
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

func stepWaitForWorkerDiscoveryByConsumer(t *testing.T, consumerInstance *consumerpkg.Consumer, workerInstance *workerpkg.Worker) {
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
// TestSetup holds all the components needed for stale peer tests
type TestSetup struct {
	ctx         context.Context
	logger      *zap.Logger
	cfg         *config.Configuration
	consumerKey crypto.PrivKey
	dhtKey      crypto.PrivKey
	workerKey   crypto.PrivKey
}

// Helper function to setup test environment
func setupStalePeerTest(t *testing.T) *TestSetup {
	t.Helper()
	if err := os.Setenv("CROWDLLAMA_TEST_MODE", "1"); err != nil {
		t.Fatalf("Failed to set test mode: %v", err)
	}
	logger := zap.NewNop()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(func() {
		cancel()
		if err := os.Unsetenv("CROWDLLAMA_TEST_MODE"); err != nil {
			t.Fatalf("Failed to unset test mode: %v", err)
		}
	})
	cfg := &config.Configuration{BootstrapPeers: []string{}}
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
	return &TestSetup{
		ctx:         ctx,
		logger:      logger,
		cfg:         cfg,
		consumerKey: consumerKey,
		dhtKey:      dhtKey,
		workerKey:   workerKey,
	}
}

// Helper function to setup DHT server
func setupDHTForStalePeerTest(
	ctx context.Context,
	t *testing.T,
	logger *zap.Logger,
	dhtKey crypto.PrivKey,
) (dhtServer *dht.Server, dhtAddrs []string) {
	t.Helper()
	var err error
	dhtServer, err = dht.NewDHTServerWithAddrs(ctx, dhtKey, logger, []string{"/ip4/127.0.0.1/tcp/0"})
	if err != nil {
		t.Fatalf("Failed to create DHT server: %v", err)
	}
	_, err = dhtServer.Start()
	if err != nil {
		t.Fatalf("Failed to start DHT server: %v", err)
	}
	time.Sleep(3 * time.Second)
	dhtAddrs = dhtServer.GetPeerAddrs()
	if len(dhtAddrs) == 0 {
		t.Fatal("DHT server has no addresses")
	}
	return
}

// Helper function to verify initial discovery
func verifyInitialDiscovery(t *testing.T, consumer *consumerpkg.Consumer, worker *workerpkg.Worker, dhtServer *dht.Server) string {
	t.Helper()
	initialWorkers := consumer.GetAvailableWorkers()
	if len(initialWorkers) == 0 {
		t.Fatal("No workers discovered initially")
	}
	workerPeerID := worker.Host.ID().String()
	if _, exists := initialWorkers[workerPeerID]; !exists {
		t.Fatalf("Worker %s not found in consumer's worker list", workerPeerID)
	}
	t.Logf("Initial discovery successful: found %d workers", len(initialWorkers))
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
	return workerPeerID
}

// Helper function to verify cleanup after worker stops
func verifyCleanupAfterWorkerStop(t *testing.T, consumer *consumerpkg.Consumer, dhtServer *dht.Server, workerPeerID string) {
	t.Helper()
	remainingWorkers := consumer.GetAvailableWorkers()
	if len(remainingWorkers) > 0 {
		t.Errorf("Expected no workers after timeout, but found %d", len(remainingWorkers))
		for peerID := range remainingWorkers {
			t.Errorf("Unexpected worker still present: %s", peerID)
		}
	}
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
	healthStatus := consumer.GetWorkerHealthStatus()
	if len(healthStatus) > 0 {
		t.Errorf("Expected no health status entries after cleanup, but found %d", len(healthStatus))
	}
}

func TestStalePeerRemovalIntegration(t *testing.T) {
	setup := setupStalePeerTest(t)
	dhtServer, dhtAddrs := setupDHTForStalePeerTest(setup.ctx, t, setup.logger, setup.dhtKey)
	defer dhtServer.Stop()
	setup.cfg.BootstrapPeers = dhtAddrs
	consumer, err := consumerpkg.NewConsumerWithConfig(setup.ctx, setup.logger, setup.consumerKey, setup.cfg)
	if err != nil {
		t.Fatalf("Failed to create consumer: %v", err)
	}
	defer consumer.StopBackgroundDiscovery()
	worker, err := workerpkg.NewWorkerWithConfig(setup.ctx, setup.workerKey, setup.cfg)
	if err != nil {
		t.Fatalf("Failed to create worker: %v", err)
	}
	defer worker.StopMetadataUpdates()
	worker.StartMetadataUpdates()
	worker.SetupMetadataHandler()
	worker.AdvertiseModel(setup.ctx, crowdllama.WorkerNamespace)
	time.Sleep(2 * time.Second)
	consumer.StartBackgroundDiscovery()
	time.Sleep(5 * time.Second)
	workerPeerID := verifyInitialDiscovery(t, consumer, worker, dhtServer)
	t.Log("Stopping worker to simulate offline behavior...")
	worker.StopMetadataUpdates()
	t.Log("Waiting for stale peer timeout...")
	time.Sleep(35 * time.Second)
	verifyCleanupAfterWorkerStop(t, consumer, dhtServer, workerPeerID)
	t.Log("Stale peer removal test completed successfully")
}

func createAndStartWorker(ctx context.Context, t *testing.T, workerKey crypto.PrivKey, cfg *config.Configuration) *workerpkg.Worker {
	t.Helper()
	worker, err := workerpkg.NewWorker(ctx, workerKey, cfg)
	if err != nil {
		t.Fatalf("Failed to create worker: %v", err)
	}

	// Set up metadata handler and start updates
	worker.SetupMetadataHandler()
	worker.StartMetadataUpdates()

	// Advertise the worker on the DHT
	worker.AdvertiseModel(ctx, crowdllama.WorkerNamespace)

	return worker
}

func createAndStartConsumer(
	ctx context.Context,
	t *testing.T,
	logger *zap.Logger,
	consumerKey crypto.PrivKey,
	cfg *config.Configuration,
) *consumerpkg.Consumer {
	t.Helper()
	consumer, err := consumerpkg.NewConsumer(ctx, logger, consumerKey, cfg)
	if err != nil {
		t.Fatalf("Failed to create consumer: %v", err)
	}
	consumer.StartBackgroundDiscovery()
	return consumer
}

func assertWorkerMetadata(t *testing.T, workerResource *crowdllama.Resource) {
	t.Helper()
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
}

// Helper to create an isolated DHT bootstrap node for each test
func createIsolatedTestDHT(
	ctx context.Context,
	t *testing.T,
	logger *zap.Logger,
	port int,
) (dhtServer *dht.Server, bootstrapAddr string) {
	t.Helper()
	bootstrapKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 256)
	if err != nil {
		t.Fatalf("Failed to generate bootstrap key: %v", err)
	}

	// Use specific port for isolation
	listenAddr := fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", port)
	dhtServer, err = dht.NewDHTServerWithAddrs(ctx, bootstrapKey, logger, []string{listenAddr})
	if err != nil {
		t.Fatalf("Failed to create isolated DHT server: %v", err)
	}

	_, err = dhtServer.Start()
	if err != nil {
		t.Fatalf("Failed to start isolated DHT server: %v", err)
	}

	// Wait for DHT to be ready
	time.Sleep(2 * time.Second)

	addrs := dhtServer.GetPeerAddrs()
	if len(addrs) == 0 {
		t.Fatalf("Isolated DHT server has no addresses")
	}
	bootstrapAddr = addrs[0]

	// Additional wait to ensure DHT is fully ready
	time.Sleep(1 * time.Second)

	return dhtServer, bootstrapAddr
}

// Helper to get a unique port for each test
func getTestPort(t *testing.T) int {
	t.Helper()
	// Use test name hash to get a consistent port for each test
	hash := fnv.New32a()
	if _, err := hash.Write([]byte(t.Name())); err != nil {
		t.Fatalf("Failed to hash test name for port: %v", err)
	}
	port := 10000 + int(hash.Sum32()%5000) // Ports 10000-14999
	return port
}

// Update setupTestConfigAndKeys to accept bootstrapAddrs
func setupTestConfigAndKeysWithBootstrap(
	t *testing.T,
	bootstrapAddrs []string,
) (cfg *config.Configuration, consumerKey, workerKey crypto.PrivKey) {
	t.Helper()
	cfg = &config.Configuration{BootstrapPeers: bootstrapAddrs}
	var err error
	consumerKey, _, err = crypto.GenerateKeyPair(crypto.Ed25519, 256)
	if err != nil {
		t.Fatalf("Failed to generate consumer key: %v", err)
	}
	workerKey, _, err = crypto.GenerateKeyPair(crypto.Ed25519, 256)
	if err != nil {
		t.Fatalf("Failed to generate worker key: %v", err)
	}
	return
}

// Helper function to reduce cyclomatic complexity
func waitForWorkers(t *testing.T, consumer *consumerpkg.Consumer, expectedCount int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		workers := consumer.GetAvailableWorkers()
		if len(workers) == expectedCount {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Expected %d workers, but found %d after %v", expectedCount, len(consumer.GetAvailableWorkers()), timeout)
}

// Helper function to wait for worker to be discovered and healthy
func waitForWorkerHealthy(t *testing.T, consumer *consumerpkg.Consumer, workerPeerID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		healthStatus := consumer.GetWorkerHealthStatus()
		if len(healthStatus) > 0 {
			if workerHealth, exists := healthStatus[workerPeerID]; exists {
				if workerHealth["is_healthy"].(bool) {
					return // Worker is healthy
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Worker %s did not become healthy within %v", workerPeerID, timeout)
}

// Helper function to wait for worker to become unhealthy
func waitForWorkerUnhealthy(t *testing.T, consumer *consumerpkg.Consumer, workerPeerID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		healthStatus := consumer.GetWorkerHealthStatus()
		if len(healthStatus) > 0 {
			if workerHealth, exists := healthStatus[workerPeerID]; exists {
				if !workerHealth["is_healthy"].(bool) {
					return // Worker is unhealthy
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Worker %s did not become unhealthy within %v", workerPeerID, timeout)
}

// Helper function to wait for worker to be removed from health status
func waitForWorkerRemoved(t *testing.T, consumer *consumerpkg.Consumer, workerPeerID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		healthStatus := consumer.GetWorkerHealthStatus()
		if len(healthStatus) == 0 {
			return // All workers removed
		}
		if _, exists := healthStatus[workerPeerID]; !exists {
			return // Specific worker removed
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Worker %s was not removed within %v", workerPeerID, timeout)
}

// Helper function to wait for worker discovery
func waitForWorkerDiscovery(t *testing.T, consumer *consumerpkg.Consumer, workerPeerID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		workers := consumer.GetAvailableWorkers()
		if _, exists := workers[workerPeerID]; exists {
			return // Worker discovered
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Worker %s was not discovered within %v", workerPeerID, timeout)
}

// Update all affected tests to use isolated DHT nodes
func TestPeerManagerHealthChecks(t *testing.T) {
	if err := os.Setenv("CROWDLLAMA_TEST_MODE", "1"); err != nil {
		t.Fatalf("Failed to set test mode: %v", err)
	}
	defer func() {
		if err := os.Unsetenv("CROWDLLAMA_TEST_MODE"); err != nil {
			t.Fatalf("Failed to unset test mode: %v", err)
		}
	}()

	logger := zap.NewNop()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Create isolated DHT for this test
	port := getTestPort(t)
	dhtServer, bootstrapAddr := createIsolatedTestDHT(ctx, t, logger, port)
	defer dhtServer.Stop()

	cfg, consumerKey, workerKey := setupTestConfigAndKeysWithBootstrap(t, []string{bootstrapAddr})

	// Create and start consumer first
	consumer := createAndStartConsumer(ctx, t, logger, consumerKey, cfg)
	defer consumer.StopBackgroundDiscovery()

	// Create and start worker
	worker := createAndStartWorker(ctx, t, workerKey, cfg)
	defer worker.StopMetadataUpdates()

	workerPeerID := worker.Host.ID().String()

	// Wait for worker to be discovered and healthy
	waitForWorkerDiscovery(t, consumer, workerPeerID, 15*time.Second)
	waitForWorkerHealthy(t, consumer, workerPeerID, 10*time.Second)

	// Stop the worker
	worker.StopMetadataUpdates()

	// Wait for worker to become unhealthy (should still be in health status)
	waitForWorkerUnhealthy(t, consumer, workerPeerID, 15*time.Second)

	// Verify the worker is marked as unhealthy but still in health status
	updatedHealthStatus := consumer.GetWorkerHealthStatus()
	updatedWorkerHealth, exists := updatedHealthStatus[workerPeerID]
	if !exists {
		t.Fatal("Worker should still be in health status even if unhealthy")
	}
	if updatedWorkerHealth["is_healthy"].(bool) {
		t.Error("Worker should be marked as unhealthy after stopping")
	}
	if updatedWorkerHealth["failed_attempts"].(int) == 0 {
		t.Error("Failed attempts should be greater than 0 for unhealthy worker")
	}

	// Wait for worker to be completely removed from health status
	waitForWorkerRemoved(t, consumer, workerPeerID, 20*time.Second)

	// Final verification that no health status entries remain
	finalHealthStatus := consumer.GetWorkerHealthStatus()
	if len(finalHealthStatus) > 0 {
		t.Errorf("Expected no health status entries after removal, but found %d", len(finalHealthStatus))
	}
}

func TestMetadataValidation(t *testing.T) {
	if err := os.Setenv("CROWDLLAMA_TEST_MODE", "1"); err != nil {
		t.Fatalf("Failed to set test mode: %v", err)
	}
	defer func() {
		if err := os.Unsetenv("CROWDLLAMA_TEST_MODE"); err != nil {
			t.Fatalf("Failed to unset test mode: %v", err)
		}
	}()

	logger := zap.NewNop()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create isolated DHT for this test
	port := getTestPort(t)
	dhtServer, bootstrapAddr := createIsolatedTestDHT(ctx, t, logger, port)
	defer dhtServer.Stop()

	cfg, consumerKey, workerKey := setupTestConfigAndKeysWithBootstrap(t, []string{bootstrapAddr})

	// Create and start consumer first
	consumer := createAndStartConsumer(ctx, t, logger, consumerKey, cfg)
	defer consumer.StopBackgroundDiscovery()

	// Create and start worker
	worker := createAndStartWorker(ctx, t, workerKey, cfg)
	defer worker.StopMetadataUpdates()

	// Wait for worker discovery
	waitForWorkerDiscovery(t, consumer, worker.Host.ID().String(), 15*time.Second)

	workers := consumer.GetAvailableWorkers()
	if len(workers) == 0 {
		t.Fatal("No workers discovered")
	}
	workerResource, exists := workers[worker.Host.ID().String()]
	if !exists {
		t.Fatalf("Worker %s not found", worker.Host.ID().String())
	}
	assertWorkerMetadata(t, workerResource)
}

// Helper function to wait for a specific number of workers to remain
func waitForWorkerCount(t *testing.T, consumer *consumerpkg.Consumer, expectedCount int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		workers := consumer.GetAvailableWorkers()
		if len(workers) == expectedCount {
			return // Expected number of workers found
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Expected %d workers, but found %d after %v", expectedCount, len(consumer.GetAvailableWorkers()), timeout)
}

func TestConcurrentPeerManagement(t *testing.T) {
	if err := os.Setenv("CROWDLLAMA_TEST_MODE", "1"); err != nil {
		t.Fatalf("Failed to set test mode: %v", err)
	}
	defer func() {
		if err := os.Unsetenv("CROWDLLAMA_TEST_MODE"); err != nil {
			t.Fatalf("Failed to unset test mode: %v", err)
		}
	}()

	logger := zap.NewNop()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Create isolated DHT for this test
	port := getTestPort(t)
	dhtServer, bootstrapAddr := createIsolatedTestDHT(ctx, t, logger, port)
	defer dhtServer.Stop()

	cfg := &config.Configuration{BootstrapPeers: []string{bootstrapAddr}}
	consumerKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 256)
	if err != nil {
		t.Fatalf("Failed to generate consumer key: %v", err)
	}

	// Create and start consumer first
	consumer := createAndStartConsumer(ctx, t, logger, consumerKey, cfg)
	defer consumer.StopBackgroundDiscovery()

	numWorkers := 5
	workers := make([]*workerpkg.Worker, numWorkers)
	workerPeerIDs := make([]string, numWorkers)

	// Create all workers
	for i := 0; i < numWorkers; i++ {
		workerKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 256)
		if err != nil {
			t.Fatalf("Failed to generate worker key %d: %v", i, err)
		}
		workers[i] = createAndStartWorker(ctx, t, workerKey, cfg)
		workerPeerIDs[i] = workers[i].Host.ID().String()
	}

	defer func() {
		for _, w := range workers {
			w.StopMetadataUpdates()
		}
	}()

	// Wait for all workers to be discovered
	waitForWorkers(t, consumer, numWorkers, 15*time.Second)

	// Stop half the workers
	for i := 0; i < numWorkers/2; i++ {
		workers[i].StopMetadataUpdates()
	}

	// Wait for the expected number of workers to remain
	expectedRemaining := numWorkers - numWorkers/2
	waitForWorkerCount(t, consumer, expectedRemaining, 30*time.Second)

	// Final verification
	remainingWorkers := consumer.GetAvailableWorkers()
	if len(remainingWorkers) != expectedRemaining {
		t.Errorf("Expected %d remaining workers, but found %d", expectedRemaining, len(remainingWorkers))
	}
}
