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
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"go.uber.org/zap"

	"github.com/crowdllama/crowdllama/internal/discovery"
	"github.com/crowdllama/crowdllama/internal/keys"
	"github.com/crowdllama/crowdllama/pkg/config"
	"github.com/crowdllama/crowdllama/pkg/crowdllama"
	"github.com/crowdllama/crowdllama/pkg/dht"
	"github.com/crowdllama/crowdllama/pkg/gateway"
	"github.com/crowdllama/crowdllama/pkg/peer"
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

	// Use the actual DHT server peer ID for bootstrap
	dhtPeerID := dhtServer.GetPeerID()
	t.Logf("DHT server peer ID: %s", dhtPeerID)

	workerPeer := stepInitWorkerPeer(ctx, t, workerPrivKey, dhtPeerAddr, mockOllamaPort, logger)
	stepSetupWorkerMetadata(t, workerPeer)
	stepAdvertiseWorker(ctx, t, workerPeer)

	consumerPeer := stepInitConsumerPeer(ctx, t, consumerPrivKey, dhtPeerAddr, logger)
	stepStartConsumerDiscovery(t, consumerPeer)
	defer consumerPeer.StopMetadataUpdates()

	consumerPort := 9003
	gateway := stepStartConsumerGateway(ctx, t, consumerPeer, consumerPort, logger)
	defer stepShutdownConsumerGateway(t, gateway)

	stepWaitForDiscovery(t, dhtServer, workerPeer, consumerPeer)

	// Wait for a worker with the correct model to be discovered by the consumer
	stepWaitForWorkerWithModel(t, consumerPeer, "llama3.2")
	stepSendAndValidateRequest(ctx, t, consumerPort)
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
		logger.Info("Running in CI environment")
	}

	// Use custom addresses to avoid port conflicts
	customAddrs := []string{
		"/ip4/127.0.0.1/tcp/9001",
		"/ip4/127.0.0.1/udp/9001/quic-v1",
	}

	server, err := dht.NewDHTServerWithAddrs(ctx, dhtPrivKey, logger, customAddrs)
	if err != nil {
		t.Fatalf("Failed to create DHT server: %v", err)
	}

	return server
}

func stepStartDHTServerFull(t *testing.T, dhtServer *dht.Server) string {
	t.Helper()

	primaryAddr, err := dhtServer.Start()
	if err != nil {
		t.Fatalf("Failed to start DHT server: %v", err)
	}

	t.Logf("DHT server started with primary address: %s", primaryAddr)
	return primaryAddr
}

func stepInitWorkerPeer(
	ctx context.Context,
	t *testing.T,
	workerPrivKey crypto.PrivKey,
	dhtPeerAddr string,
	mockOllamaPort int,
	logger *zap.Logger,
) *peer.Peer {
	t.Helper()

	// Create configuration for worker
	cfg := config.NewConfiguration()
	cfg.BootstrapPeers = []string{dhtPeerAddr}
	cfg.OllamaBaseURL = fmt.Sprintf("http://localhost:%d", mockOllamaPort)

	t.Logf("Using DHT bootstrap address: %s", dhtPeerAddr)

	// Create worker peer
	workerPeer, err := peer.NewPeerWithConfig(ctx, workerPrivKey, cfg, true, logger) // true for worker mode
	if err != nil {
		t.Fatalf("Failed to create worker peer: %v", err)
	}

	// Start the peer manager
	workerPeer.PeerManager.Start()

	t.Logf("Worker peer created with ID: %s", workerPeer.Host.ID().String())
	return workerPeer
}

func stepSetupWorkerMetadata(t *testing.T, workerPeer *peer.Peer) {
	t.Helper()

	// Setup metadata handler
	workerPeer.SetupMetadataHandler()

	// Create worker metadata
	metadata := crowdllama.NewCrowdLlamaResource(workerPeer.Host.ID().String())
	metadata.WorkerMode = true
	metadata.GPUModel = "RTX 4090"
	metadata.VRAMGB = 24
	metadata.TokensThroughput = 100.0
	metadata.SupportedModels = []string{"llama3.2", "mistral"}
	metadata.Load = 0.1

	// Set the metadata
	workerPeer.Metadata = metadata

	t.Logf("Worker metadata set up with GPU: %s, VRAM: %dGB", metadata.GPUModel, metadata.VRAMGB)
}

func stepAdvertiseWorker(ctx context.Context, t *testing.T, workerPeer *peer.Peer) {
	t.Helper()

	// Start metadata updates
	workerPeer.StartMetadataUpdates()

	// Wait a moment for the initial metadata update to complete
	time.Sleep(100 * time.Millisecond)

	// Patch the metadata to ensure SupportedModels includes "llama3.2"
	if workerPeer.Metadata != nil {
		workerPeer.Metadata.SupportedModels = []string{"llama3.2", "mistral"}
	}

	// Advertise the peer
	workerPeer.AdvertisePeer(ctx, crowdllama.PeerNamespace)

	t.Logf("Worker peer advertising started")
}

func stepInitConsumerPeer(
	ctx context.Context,
	t *testing.T,
	consumerPrivKey crypto.PrivKey,
	dhtPeerAddr string,
	logger *zap.Logger,
) *peer.Peer {
	t.Helper()

	// Create configuration for consumer
	cfg := config.NewConfiguration()
	cfg.BootstrapPeers = []string{dhtPeerAddr}

	t.Logf("Consumer using DHT bootstrap address: %s", dhtPeerAddr)

	// Create consumer peer
	consumerPeer, err := peer.NewPeerWithConfig(ctx, consumerPrivKey, cfg, false, logger) // false for consumer mode
	if err != nil {
		t.Fatalf("Failed to create consumer peer: %v", err)
	}

	// Start the peer manager
	consumerPeer.PeerManager.Start()

	t.Logf("Consumer peer created with ID: %s", consumerPeer.Host.ID().String())
	return consumerPeer
}

func stepStartConsumerDiscovery(t *testing.T, consumerPeer *peer.Peer) {
	t.Helper()

	// Setup metadata handler
	consumerPeer.SetupMetadataHandler()

	// Start metadata updates
	consumerPeer.StartMetadataUpdates()

	// Advertise the peer
	consumerPeer.AdvertisePeer(context.Background(), crowdllama.PeerNamespace)

	t.Logf("Consumer peer discovery started")
}

func stepStartConsumerGateway(
	ctx context.Context,
	t *testing.T,
	consumerPeer *peer.Peer,
	consumerPort int,
	logger *zap.Logger,
) *gateway.Gateway {
	t.Helper()

	// Create gateway using the consumer peer
	g, err := gateway.NewGateway(ctx, logger, consumerPeer)
	if err != nil {
		t.Fatalf("Failed to create gateway: %v", err)
	}

	// Start HTTP server
	go func() {
		if err := g.StartHTTPServer(consumerPort); err != nil {
			t.Logf("Failed to start HTTP server: %v", err)
		}
	}()

	t.Logf("Consumer gateway started on port %d", consumerPort)
	return g
}

func stepShutdownConsumerGateway(t *testing.T, gateway *gateway.Gateway) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	gateway.StopBackgroundDiscovery()
	if err := gateway.StopHTTPServer(ctx); err != nil {
		t.Logf("Failed to stop HTTP server: %v", err)
	}

	t.Logf("Consumer gateway shutdown complete")
}

func stepWaitForDiscovery(
	t *testing.T,
	dhtServer *dht.Server,
	workerPeer *peer.Peer,
	consumerPeer *peer.Peer,
) {
	t.Helper()

	// Wait for worker to be discovered by consumer
	stepWaitForWorkerDiscoveryByConsumer(t, consumerPeer, workerPeer)

	// Wait for consumer to be discovered by DHT
	stepWaitForConsumerDiscoveryByDHT(t, dhtServer, consumerPeer)

	t.Logf("Discovery complete - worker and consumer found each other")
}

func stepWaitForWorkerDiscoveryByConsumer(t *testing.T, consumerPeer *peer.Peer, workerPeer *peer.Peer) {
	t.Helper()

	workerPeerID := workerPeer.Host.ID().String()
	timeout := 30 * time.Second
	if os.Getenv("CI") == ciEnvironment {
		timeout = 60 * time.Second
	}

	t.Logf("Waiting for consumer to discover worker %s", workerPeerID)

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		healthyPeers := consumerPeer.PeerManager.GetHealthyPeers()
		for peerID, peerInfo := range healthyPeers {
			if peerID == workerPeerID && peerInfo.Metadata != nil && peerInfo.Metadata.WorkerMode {
				t.Logf("Worker discovered by consumer")
				return
			}
		}
		time.Sleep(1 * time.Second)
	}

	t.Fatalf("Worker not discovered by consumer within %v", timeout)
}

func stepWaitForConsumerDiscoveryByDHT(t *testing.T, dhtServer *dht.Server, consumerPeer *peer.Peer) {
	t.Helper()

	consumerPeerID := consumerPeer.Host.ID().String()
	timeout := 30 * time.Second
	if os.Getenv("CI") == ciEnvironment {
		timeout = 60 * time.Second
	}

	t.Logf("Waiting for DHT to discover consumer %s", consumerPeerID)

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		healthyPeers := dhtServer.GetHealthyPeers()
		for peerID := range healthyPeers {
			if peerID == consumerPeerID {
				t.Logf("Consumer discovered by DHT")
				return
			}
		}
		time.Sleep(1 * time.Second)
	}

	t.Fatalf("Consumer not discovered by DHT within %v", timeout)
}

func stepSendAndValidateRequest(_ context.Context, t *testing.T, consumerPort int) {
	t.Helper()

	// Wait a moment for the server to be ready
	time.Sleep(2 * time.Second)

	// Create a test request
	requestBody := map[string]interface{}{
		"model": "llama3.2",
		"messages": []map[string]string{
			{"role": "user", "content": "Hello, how are you?"},
		},
		"stream": false,
	}

	requestJSON, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	// Send request to the consumer gateway
	resp, err := http.Post(
		fmt.Sprintf("http://localhost:%d/api/chat", consumerPort),
		"application/json",
		bytes.NewBuffer(requestJSON),
	)
	if err != nil {
		t.Fatalf("Failed to send request: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Logf("Warning: failed to close response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Expected status 200, got %d: %s", resp.StatusCode, string(body))
	}

	// Parse and validate the response
	var response gateway.GenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Validate response structure
	if response.Model != "llama3.2" {
		t.Errorf("Expected model 'llama3.2', got '%s'", response.Model)
	}
	if response.Message.Content == "" {
		t.Error("Response message content is empty")
	}
	if !response.Done {
		t.Error("Response should be marked as done")
	}

	t.Logf("Request completed successfully")
}

func stepStartMockOllamaServer(t *testing.T, port int) *MockOllamaServer {
	t.Helper()

	mockOllama := NewMockOllamaServer(port)

	go func() {
		if err := mockOllama.Start(); err != nil {
			t.Logf("Mock Ollama server error: %v", err)
		}
	}()

	// Wait for server to start
	time.Sleep(1 * time.Second)

	t.Logf("Mock Ollama server started on port %d", port)
	return mockOllama
}

func stepShutdownMockOllamaServer(t *testing.T, mockOllama *MockOllamaServer) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := mockOllama.Stop(ctx); err != nil {
		t.Logf("Failed to stop mock Ollama server: %v", err)
	}

	t.Logf("Mock Ollama server stopped")
}

func TestMockOllamaServer(t *testing.T) {
	mockOllama := setupMockOllamaServer(t)
	defer shutdownMockOllamaServer(t, mockOllama)

	testMockOllamaRequest(t, mockOllama)
}

func setupMockOllamaServer(t *testing.T) *MockOllamaServer {
	t.Helper()
	port := 11435
	mockOllama := NewMockOllamaServer(port)

	go func() {
		if err := mockOllama.Start(); err != nil {
			t.Logf("Mock server error: %v", err)
		}
	}()

	// Wait for server to start
	time.Sleep(1 * time.Second)

	return mockOllama
}

func shutdownMockOllamaServer(t *testing.T, mockOllama *MockOllamaServer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := mockOllama.Stop(ctx); err != nil {
		t.Logf("Failed to stop mock server: %v", err)
	}
}

func testMockOllamaRequest(t *testing.T, mockOllama *MockOllamaServer) {
	t.Helper()
	requestBody := map[string]interface{}{
		"model": "llama3.2",
		"messages": []map[string]string{
			{"role": "user", "content": "Hello, how are you?"},
		},
		"stream": false,
	}

	requestJSON, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	resp, err := http.Post(
		fmt.Sprintf("http://localhost:%d/api/chat", mockOllama.GetPort()),
		"application/json",
		bytes.NewBuffer(requestJSON),
	)
	if err != nil {
		t.Fatalf("Failed to send request: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Logf("Warning: failed to close response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Expected status 200, got %d: %s", resp.StatusCode, string(body))
	}

	var response MockOllamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	validateMockOllamaResponse(t, &response)
}

func validateMockOllamaResponse(t *testing.T, response *MockOllamaResponse) {
	t.Helper()
	if response.Model != "llama3.2" {
		t.Errorf("Expected model 'llama3.2', got '%s'", response.Model)
	}

	if response.Message.Role != "assistant" {
		t.Errorf("Expected role 'assistant', got '%s'", response.Message.Role)
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
}

// stepWaitForWorkerWithModel waits until the consumer peer discovers a worker with the required model in its metadata
func stepWaitForWorkerWithModel(t *testing.T, consumerPeer *peer.Peer, requiredModel string) {
	t.Helper()
	timeout := 30 * time.Second
	if os.Getenv("CI") == ciEnvironment {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)
	t.Logf("Waiting for consumer to discover a worker with model %s", requiredModel)
	for time.Now().Before(deadline) {
		healthyPeers := consumerPeer.PeerManager.GetHealthyPeers()
		for peerID, peerInfo := range healthyPeers {
			if peerInfo.Metadata != nil && peerInfo.Metadata.WorkerMode {
				for _, model := range peerInfo.Metadata.SupportedModels {
					if model == requiredModel {
						t.Logf("Consumer discovered worker %s with model %s", peerID, requiredModel)
						return
					}
				}
			}
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("Consumer did not discover a worker with model %s within %v", requiredModel, timeout)
}
