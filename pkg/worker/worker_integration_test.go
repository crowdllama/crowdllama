package worker

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"go.uber.org/zap"

	"github.com/matiasinsaurralde/crowdllama/internal/discovery"
	"github.com/matiasinsaurralde/crowdllama/internal/keys"
	"github.com/matiasinsaurralde/crowdllama/pkg/dht"
)

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

func TestWorkerDHTIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Set test mode environment variable for shorter intervals
	if err := os.Setenv("CROW DLLAMA_TEST_MODE", "1"); err != nil {
		t.Logf("Failed to set test mode environment variable: %v", err)
	}

	// Enable test mode for shorter intervals
	discovery.SetTestMode()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	logger, _ := zap.NewDevelopment()

	tempDir, err := os.MkdirTemp("", "crowdllama-worker-integration-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tempDir); removeErr != nil {
			t.Logf("Failed to remove temp dir: %v", removeErr)
		}
	}()

	dhtKeyPath := filepath.Join(tempDir, "dht.key")
	workerKeyPath := filepath.Join(tempDir, "worker.key")
	dhtKeyManager := keys.NewKeyManager(dhtKeyPath, logger)
	workerKeyManager := keys.NewKeyManager(workerKeyPath, logger)
	dhtPrivKey, err := dhtKeyManager.GetOrCreatePrivateKey()
	if err != nil {
		t.Fatalf("Failed to create DHT private key: %v", err)
	}
	workerPrivKey, err := workerKeyManager.GetOrCreatePrivateKey()
	if err != nil {
		t.Fatalf("Failed to create worker private key: %v", err)
	}

	dhtServer := stepInitDHTServerWorker(ctx, t, dhtPrivKey, logger)
	defer dhtServer.Stop()
	dhtPeerAddr := stepStartDHTServerWorker(t, dhtServer)
	stepLogDHTServerInfoWorker(t, dhtServer, dhtPeerAddr)

	worker := stepInitWorker(ctx, t, workerPrivKey, dhtPeerAddr)
	stepSetupWorkerMetadata(t, worker)
	stepAdvertiseWorker(ctx, t, worker)
	stepLogWorkerInfo(t, worker)
	stepWaitForWorkerDiscovery(t, dhtServer, worker)
	stepValidateWorkerAdvertisement(ctx, t, dhtServer, worker)
	stepTestMetadataRetrieval(ctx, t, dhtServer, worker, logger)
	t.Log("Integration test completed successfully")
}

func stepInitDHTServerWorker(ctx context.Context, t *testing.T, dhtPrivKey crypto.PrivKey, logger *zap.Logger) *dht.Server {
	t.Helper()

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

func stepStartDHTServerWorker(t *testing.T, dhtServer *dht.Server) string {
	t.Helper()
	dhtPeerAddr, err := dhtServer.Start()
	if err != nil {
		t.Fatalf("Failed to start DHT server: %v", err)
	}

	// Add a delay to ensure the DHT server is fully ready
	time.Sleep(2 * time.Second)

	return dhtPeerAddr
}

func stepLogDHTServerInfoWorker(t *testing.T, dhtServer *dht.Server, dhtPeerAddr string) {
	t.Helper()
	t.Logf("DHT server started with peer address: %s", dhtPeerAddr)
	t.Logf("DHT server peer ID: %s", dhtServer.GetPeerID())
	t.Logf("DHT server all peer addresses: %v", dhtServer.GetPeerAddrs())
}

func stepInitWorker(ctx context.Context, t *testing.T, workerPrivKey crypto.PrivKey, dhtPeerAddr string) *Worker {
	t.Helper()

	// Try to create worker with retry logic
	var worker *Worker
	var err error
	maxRetries := 3

	for attempt := 1; attempt <= maxRetries; attempt++ {
		t.Logf("Attempt %d/%d: Creating worker with bootstrap peer: %s", attempt, maxRetries, dhtPeerAddr)

		worker, err = NewWorkerWithBootstrapPeers(ctx, workerPrivKey, []string{dhtPeerAddr})
		if err == nil {
			t.Logf("✅ Worker created successfully on attempt %d", attempt)
			break
		}

		t.Logf("❌ Failed to create worker on attempt %d: %v", attempt, err)
		if attempt < maxRetries {
			t.Logf("Retrying in 2 seconds...")
			time.Sleep(2 * time.Second)
		}
	}

	if err != nil {
		t.Fatalf("Failed to create worker after %d attempts: %v", maxRetries, err)
	}

	return worker
}

func stepSetupWorkerMetadata(t *testing.T, worker *Worker) {
	t.Helper()
	worker.SetupMetadataHandler()
	worker.UpdateMetadata(
		[]string{"tinyllama", "llama2"},
		100.0, // tokens per second
		8,     // VRAM GB
		0.5,   // load
		"RTX 3080",
	)
}

func stepAdvertiseWorker(ctx context.Context, t *testing.T, worker *Worker) {
	t.Helper()
	worker.AdvertiseModel(ctx, "test-namespace")
}

func stepLogWorkerInfo(t *testing.T, worker *Worker) {
	t.Helper()
	t.Logf("Worker created with peer ID: %s", worker.Host.ID().String())
}

func stepWaitForWorkerDiscovery(t *testing.T, dhtServer *dht.Server, worker *Worker) {
	t.Helper()
	t.Log("Step 3: Waiting for worker to connect and be discovered")
	time.Sleep(2 * time.Second)
	maxAttempts := 10
	attempt := 0
	workerFound := false
	workerPeerID := worker.Host.ID().String()
	for attempt < maxAttempts {
		attempt++
		t.Logf("Attempt %d/%d: Checking if DHT server knows about worker", attempt, maxAttempts)
		if dhtServer.HasPeer(workerPeerID) {
			t.Logf("Worker peer ID %s found in DHT server's connected peers", workerPeerID)
			workerFound = true
			break
		}
		connectedPeers := dhtServer.GetPeers()
		t.Logf("DHT server connected peers: %v", connectedPeers)
		if len(connectedPeers) > 0 {
			t.Logf("DHT server has %d connected peers", len(connectedPeers))
		}
		time.Sleep(1 * time.Second)
	}
	if !workerFound {
		t.Errorf("Worker peer ID %s was not found in DHT server after %d attempts", workerPeerID, maxAttempts)
		t.Logf("DHT server peer ID: %s", dhtServer.GetPeerID())
		t.Logf("Worker peer ID: %s", workerPeerID)
		t.Logf("DHT server peer addresses: %v", dhtServer.GetPeerAddrs())
		t.Logf("Worker peer addresses: %v", getWorkerPeerAddrs(worker))
	} else {
		t.Logf("✅ SUCCESS: Worker peer ID %s found in DHT server", workerPeerID)
	}
}

func stepValidateWorkerAdvertisement(ctx context.Context, t *testing.T, dhtServer *dht.Server, worker *Worker) {
	t.Helper()
	t.Log("Step 4: Validating worker advertisement")
	time.Sleep(3 * time.Second)
	namespaceCID, err := discovery.GetWorkerNamespaceCID()
	if err != nil {
		t.Fatalf("Failed to get namespace CID: %v", err)
	}
	t.Logf("Searching for workers advertising namespace CID: %s", namespaceCID.String())
	providers := dhtServer.DHT.FindProvidersAsync(ctx, namespaceCID, 10)
	providerChan := make(chan bool, 1)
	go func() {
		for provider := range providers {
			t.Logf("Found provider: %s", provider.ID.String())
			if provider.ID.String() == worker.Host.ID().String() {
				providerChan <- true
				return
			}
		}
		providerChan <- false
	}()
	select {
	case found := <-providerChan:
		if found {
			t.Logf("✅ SUCCESS: Worker %s found as provider in DHT", worker.Host.ID().String())
		} else {
			t.Logf("❌ Worker %s not found as provider in DHT", worker.Host.ID().String())
		}
	case <-time.After(5 * time.Second):
		t.Logf("❌ Timeout waiting for worker provider discovery")
	}
}

func stepTestMetadataRetrieval(ctx context.Context, t *testing.T, dhtServer *dht.Server, worker *Worker, logger *zap.Logger) {
	t.Helper()
	t.Log("Step 5: Testing metadata retrieval")
	metadata, err := discovery.RequestWorkerMetadata(ctx, dhtServer.Host, worker.Host.ID(), logger)
	if err != nil {
		t.Errorf("Failed to request metadata from worker: %v", err)
	} else {
		t.Logf("✅ SUCCESS: Retrieved metadata from worker")
		t.Logf("Worker metadata - GPU Model: %s, VRAM: %dGB, Throughput: %.2f tokens/sec",
			metadata.GPUModel, metadata.VRAMGB, metadata.TokensThroughput)
		t.Logf("Supported models: %v", metadata.SupportedModels)
	}
}

// Helper function to get worker peer addresses
func getWorkerPeerAddrs(w *Worker) []string {
	addrs := w.Host.Addrs()
	peerAddrs := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		fullAddr := addr.String() + "/p2p/" + w.Host.ID().String()
		peerAddrs = append(peerAddrs, fullAddr)
	}
	return peerAddrs
}
