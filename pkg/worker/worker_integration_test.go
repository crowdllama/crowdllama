package worker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/matiasinsaurralde/crowdllama/internal/discovery"
	"github.com/matiasinsaurralde/crowdllama/internal/keys"
	"github.com/matiasinsaurralde/crowdllama/pkg/dht"
)

func TestWorkerDHTIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
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

	dhtServer := stepInitDHTServerWorker(t, ctx, dhtPrivKey, logger)
	defer dhtServer.Stop()
	dhtPeerAddr := stepStartDHTServerWorker(t, dhtServer)
	stepLogDHTServerInfoWorker(t, dhtServer, dhtPeerAddr)

	worker := stepInitWorker(t, ctx, workerPrivKey, dhtPeerAddr)
	stepSetupWorkerMetadata(t, worker)
	stepAdvertiseWorker(t, ctx, worker)
	stepLogWorkerInfo(t, worker)
	stepWaitForWorkerDiscovery(t, dhtServer, worker)
	stepValidateWorkerAdvertisement(t, ctx, dhtServer, worker, logger)
	stepTestMetadataRetrieval(t, ctx, dhtServer, worker, logger)
	t.Log("Integration test completed successfully")
}

func stepInitDHTServerWorker(t *testing.T, ctx context.Context, dhtPrivKey crypto.PrivKey, logger *zap.Logger) *dht.Server {
	t.Helper()
	dhtServer, err := dht.NewDHTServer(ctx, dhtPrivKey, logger)
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
	return dhtPeerAddr
}

func stepLogDHTServerInfoWorker(t *testing.T, dhtServer *dht.Server, dhtPeerAddr string) {
	t.Helper()
	t.Logf("DHT server started with peer address: %s", dhtPeerAddr)
	t.Logf("DHT server peer ID: %s", dhtServer.GetPeerID())
}

func stepInitWorker(t *testing.T, ctx context.Context, workerPrivKey crypto.PrivKey, dhtPeerAddr string) *Worker {
	t.Helper()
	worker, err := NewWorkerWithBootstrapPeers(ctx, workerPrivKey, []string{dhtPeerAddr})
	if err != nil {
		t.Fatalf("Failed to create worker: %v", err)
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

func stepAdvertiseWorker(t *testing.T, ctx context.Context, worker *Worker) {
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

func stepValidateWorkerAdvertisement(t *testing.T, ctx context.Context, dhtServer *dht.Server, worker *Worker, logger *zap.Logger) {
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

func stepTestMetadataRetrieval(t *testing.T, ctx context.Context, dhtServer *dht.Server, worker *Worker, logger *zap.Logger) {
	t.Helper()
	t.Log("Step 5: Testing metadata retrieval")
	metadata, err := discovery.RequestWorkerMetadata(ctx, dhtServer.Host, worker.Host.ID(), logger)
	if err != nil {
		t.Errorf("Failed to request metadata from worker: %v", err)
	} else {
		t.Logf("✅ SUCCESS: Retrieved metadata from worker")
		t.Logf("Worker metadata - GPU Model: %s, VRAM: %dGB, Throughput: %.2f tokens/sec", metadata.GPUModel, metadata.VRAMGB, metadata.TokensThroughput)
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
