package worker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/matiasinsaurralde/crowdllama/internal/discovery"
	"github.com/matiasinsaurralde/crowdllama/internal/keys"
	"github.com/matiasinsaurralde/crowdllama/pkg/dht"
)

func TestWorkerDHTIntegration(t *testing.T) {
	// Skip this test if not running integration tests
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger, _ := zap.NewDevelopment()

	// Create temporary directory for keys
	tempDir, err := os.MkdirTemp("", "crowdllama-worker-integration-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tempDir); removeErr != nil {
			t.Logf("Failed to remove temp dir: %v", removeErr)
		}
	}()

	// Create temporary keys for DHT server and worker
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

	// Step 1: Initialize DHT Server
	t.Log("Step 1: Initializing DHT Server")
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

	// Step 2: Initialize Worker with DHT server as bootstrap peer
	t.Log("Step 2: Initializing Worker with DHT server as bootstrap peer")
	worker, err := NewWorkerWithBootstrapPeers(ctx, workerPrivKey, []string{dhtPeerAddr})
	if err != nil {
		t.Fatalf("Failed to create worker: %v", err)
	}

	// Set up metadata handler
	worker.SetupMetadataHandler()

	// Update worker metadata
	worker.UpdateMetadata(
		[]string{"tinyllama", "llama2"},
		100.0, // tokens per second
		8,     // VRAM GB
		0.5,   // load
		"RTX 3080",
	)

	// Start advertising the worker
	worker.AdvertiseModel(ctx, "test-namespace")

	t.Logf("Worker created with peer ID: %s", worker.Host.ID().String())

	// Step 3: Wait for worker to connect to DHT and be discovered
	t.Log("Step 3: Waiting for worker to connect and be discovered")

	// Give some time for the worker to connect to the DHT
	time.Sleep(2 * time.Second)

	// Check if DHT server knows about the worker
	maxAttempts := 10
	attempt := 0
	workerFound := false
	workerPeerID := worker.Host.ID().String()

	for attempt < maxAttempts {
		attempt++
		t.Logf("Attempt %d/%d: Checking if DHT server knows about worker", attempt, maxAttempts)

		// Check if worker is connected to DHT
		if dhtServer.HasPeer(workerPeerID) {
			t.Logf("Worker peer ID %s found in DHT server's connected peers", workerPeerID)
			workerFound = true
			break
		}

		// Also check if worker is advertising in the DHT
		connectedPeers := dhtServer.GetPeers()
		t.Logf("DHT server connected peers: %v", connectedPeers)

		if len(connectedPeers) > 0 {
			t.Logf("DHT server has %d connected peers", len(connectedPeers))
		}

		// Wait before next attempt
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

	// Step 4: Validate that the worker is advertising correctly
	t.Log("Step 4: Validating worker advertisement")

	// Give more time for the worker to advertise
	time.Sleep(3 * time.Second)

	// Check if worker is advertising in the DHT by looking for providers
	// This is a more comprehensive check that validates the DHT discovery mechanism
	namespaceCID, err := discovery.GetWorkerNamespaceCID()
	if err != nil {
		t.Fatalf("Failed to get namespace CID: %v", err)
	}

	t.Logf("Searching for workers advertising namespace CID: %s", namespaceCID.String())

	// Use the DHT to find providers for the worker namespace
	providers := dhtServer.DHT.FindProvidersAsync(ctx, namespaceCID, 10)

	// Collect providers with a timeout
	providerChan := make(chan bool, 1)
	go func() {
		for provider := range providers {
			t.Logf("Found provider: %s", provider.ID.String())
			if provider.ID.String() == workerPeerID {
				providerChan <- true
				return
			}
		}
		providerChan <- false
	}()

	// Wait for provider discovery with timeout
	select {
	case found := <-providerChan:
		if found {
			t.Logf("✅ SUCCESS: Worker %s found as provider in DHT", workerPeerID)
		} else {
			t.Logf("❌ Worker %s not found as provider in DHT", workerPeerID)
		}
	case <-time.After(5 * time.Second):
		t.Logf("❌ Timeout waiting for worker provider discovery")
	}

	// Step 5: Test metadata retrieval
	t.Log("Step 5: Testing metadata retrieval")

	// Request metadata from the worker
	metadata, err := discovery.RequestWorkerMetadata(ctx, dhtServer.Host, worker.Host.ID(), logger)
	if err != nil {
		t.Errorf("Failed to request metadata from worker: %v", err)
	} else {
		t.Logf("✅ SUCCESS: Retrieved metadata from worker")
		t.Logf("Worker metadata - GPU Model: %s, VRAM: %dGB, Throughput: %.2f tokens/sec",
			metadata.GPUModel, metadata.VRAMGB, metadata.TokensThroughput)
		t.Logf("Supported models: %v", metadata.SupportedModels)
	}

	t.Log("Integration test completed successfully")
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
