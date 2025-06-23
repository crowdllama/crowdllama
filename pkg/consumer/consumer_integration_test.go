package consumer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"go.uber.org/zap"

	"github.com/matiasinsaurralde/crowdllama/internal/keys"
	"github.com/matiasinsaurralde/crowdllama/pkg/dht"
)

func TestConsumerDHTIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	logger, _ := zap.NewDevelopment()

	tempDir, err := os.MkdirTemp("", "crowdllama-consumer-integration-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tempDir); removeErr != nil {
			t.Logf("Failed to remove temp dir: %v", removeErr)
		}
	}()

	dhtKeyPath := filepath.Join(tempDir, "dht.key")
	consumerKeyPath := filepath.Join(tempDir, "consumer.key")
	dhtKeyManager := keys.NewKeyManager(dhtKeyPath, logger)
	consumerKeyManager := keys.NewKeyManager(consumerKeyPath, logger)
	dhtPrivKey, err := dhtKeyManager.GetOrCreatePrivateKey()
	if err != nil {
		t.Fatalf("Failed to create DHT private key: %v", err)
	}
	consumerPrivKey, err := consumerKeyManager.GetOrCreatePrivateKey()
	if err != nil {
		t.Fatalf("Failed to create consumer private key: %v", err)
	}

	dhtServer := stepInitDHTServer(ctx, t, dhtPrivKey, logger)
	defer dhtServer.Stop()
	dhtPeerAddr := stepStartDHTServer(t, dhtServer)
	stepLogDHTServerInfo(t, dhtServer, dhtPeerAddr)

	consumer := stepInitConsumer(ctx, t, logger, consumerPrivKey, dhtPeerAddr)
	stepLogConsumerInfo(t, consumer)
	stepWaitForConsumerDiscovery(t, dhtServer, consumer)
	stepValidateWorkerDiscovery(t, consumer)
	stepTestConsumerPeerInfo(t, consumer)
	stepValidateBidirectionalConnection(t, consumer, dhtServer)
	t.Log("Integration test completed successfully")
}

func stepInitDHTServer(ctx context.Context, t *testing.T, dhtPrivKey crypto.PrivKey, logger *zap.Logger) *dht.Server {
	t.Helper()
	dhtServer, err := dht.NewDHTServer(ctx, dhtPrivKey, logger)
	if err != nil {
		t.Fatalf("Failed to create DHT server: %v", err)
	}
	return dhtServer
}

func stepStartDHTServer(t *testing.T, dhtServer *dht.Server) string {
	t.Helper()
	dhtPeerAddr, err := dhtServer.Start()
	if err != nil {
		t.Fatalf("Failed to start DHT server: %v", err)
	}
	return dhtPeerAddr
}

func stepLogDHTServerInfo(t *testing.T, dhtServer *dht.Server, dhtPeerAddr string) {
	t.Helper()
	t.Logf("DHT server started with peer address: %s", dhtPeerAddr)
	t.Logf("DHT server peer ID: %s", dhtServer.GetPeerID())
}

func stepInitConsumer(ctx context.Context, t *testing.T, logger *zap.Logger, consumerPrivKey crypto.PrivKey, dhtPeerAddr string) *Consumer {
	t.Helper()
	consumer, err := NewConsumerWithBootstrapPeers(ctx, logger, consumerPrivKey, []string{dhtPeerAddr})
	if err != nil {
		t.Fatalf("Failed to create consumer: %v", err)
	}
	return consumer
}

func stepLogConsumerInfo(t *testing.T, consumer *Consumer) {
	t.Helper()
	t.Logf("Consumer created with peer ID: %s", consumer.GetPeerID())
	t.Logf("Consumer peer addresses: %v", consumer.GetPeerAddrs())
}

func stepWaitForConsumerDiscovery(t *testing.T, dhtServer *dht.Server, consumer *Consumer) {
	t.Helper()
	t.Log("Step 3: Waiting for consumer to connect and be discovered")
	time.Sleep(2 * time.Second)
	maxAttempts := 10
	attempt := 0
	consumerFound := false
	consumerPeerID := consumer.GetPeerID()
	for attempt < maxAttempts {
		attempt++
		t.Logf("Attempt %d/%d: Checking if DHT server knows about consumer", attempt, maxAttempts)
		if dhtServer.HasPeer(consumerPeerID) {
			t.Logf("Consumer peer ID %s found in DHT server's connected peers", consumerPeerID)
			consumerFound = true
			break
		}
		connectedPeers := dhtServer.GetPeers()
		t.Logf("DHT server connected peers: %v", connectedPeers)
		if len(connectedPeers) > 0 {
			t.Logf("DHT server has %d connected peers", len(connectedPeers))
		}
		time.Sleep(1 * time.Second)
	}
	if !consumerFound {
		t.Errorf("Consumer peer ID %s was not found in DHT server after %d attempts", consumerPeerID, maxAttempts)
		t.Logf("DHT server peer ID: %s", dhtServer.GetPeerID())
		t.Logf("Consumer peer ID: %s", consumerPeerID)
		t.Logf("DHT server peer addresses: %v", dhtServer.GetPeerAddrs())
		t.Logf("Consumer peer addresses: %v", consumer.GetPeerAddrs())
	} else {
		t.Logf("✅ SUCCESS: Consumer peer ID %s found in DHT server", consumerPeerID)
	}
}

func stepValidateWorkerDiscovery(t *testing.T, consumer *Consumer) {
	t.Helper()
	consumer.StartBackgroundDiscovery()
	defer consumer.StopBackgroundDiscovery()
	time.Sleep(3 * time.Second)
	availableWorkers := consumer.GetAvailableWorkers()
	t.Logf("Consumer has %d available workers", len(availableWorkers))
}

func stepTestConsumerPeerInfo(t *testing.T, consumer *Consumer) {
	t.Helper()
	peerID := consumer.GetPeerID()
	if peerID == "" {
		t.Error("Expected consumer to have a peer ID")
	} else {
		t.Logf("✅ Consumer peer ID: %s", peerID)
	}
	peerAddrs := consumer.GetPeerAddrs()
	if len(peerAddrs) == 0 {
		t.Error("Expected consumer to have peer addresses")
	} else {
		t.Logf("✅ Consumer peer addresses: %v", peerAddrs)
	}
	primaryAddr := consumer.GetPrimaryPeerAddr()
	if primaryAddr == "" {
		t.Error("Expected consumer to have a primary peer address")
	} else {
		t.Logf("✅ Consumer primary address: %s", primaryAddr)
	}
	connectedPeers := consumer.GetPeers()
	t.Logf("✅ Consumer connected peers: %v", connectedPeers)
	t.Logf("✅ Consumer connected peers count: %d", consumer.GetConnectedPeersCount())
}

func stepValidateBidirectionalConnection(t *testing.T, consumer *Consumer, dhtServer *dht.Server) {
	t.Helper()
	dhtServerPeerID := dhtServer.GetPeerID()
	if consumer.HasPeer(dhtServerPeerID) {
		t.Logf("✅ SUCCESS: Consumer knows about DHT server peer ID %s", dhtServerPeerID)
	} else {
		t.Logf("❌ Consumer does not know about DHT server peer ID %s", dhtServerPeerID)
	}
}

func TestConsumerWithEmptyBootstrapPeers(t *testing.T) {
	// Skip this test if not running integration tests
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logger, _ := zap.NewDevelopment()

	// Create temporary directory for keys
	tempDir, err := os.MkdirTemp("", "crowdllama-consumer-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tempDir); removeErr != nil {
			t.Logf("Failed to remove temp dir: %v", removeErr)
		}
	}()

	// Create a temporary key for testing
	keyPath := filepath.Join(tempDir, "consumer.key")
	keyManager := keys.NewKeyManager(keyPath, logger)
	privKey, err := keyManager.GetOrCreatePrivateKey()
	if err != nil {
		t.Fatalf("Failed to create private key: %v", err)
	}

	// Create consumer with empty bootstrap peers (should fallback to defaults)
	consumer, err := NewConsumerWithBootstrapPeers(ctx, logger, privKey, []string{})
	if err != nil {
		t.Fatalf("Failed to create consumer: %v", err)
	}

	// Verify consumer has peer addresses
	peerAddrs := consumer.GetPeerAddrs()
	if len(peerAddrs) == 0 {
		t.Error("Expected consumer to have peer addresses after fallback to defaults")
	}

	peerID := consumer.GetPeerID()
	if peerID == "" {
		t.Error("Expected consumer to have a peer ID")
	}

	primaryAddr := consumer.GetPrimaryPeerAddr()
	if primaryAddr == "" {
		t.Error("Expected consumer to have a primary peer address")
	}

	t.Logf("Consumer created with peer ID: %s", peerID)
	t.Logf("Consumer peer addresses: %v", peerAddrs)
	t.Logf("Consumer primary address: %s", primaryAddr)
}

func TestConsumerWithNilBootstrapPeers(t *testing.T) {
	// Skip this test if not running integration tests
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logger, _ := zap.NewDevelopment()

	// Create temporary directory for keys
	tempDir, err := os.MkdirTemp("", "crowdllama-consumer-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tempDir); removeErr != nil {
			t.Logf("Failed to remove temp dir: %v", removeErr)
		}
	}()

	// Create a temporary key for testing
	keyPath := filepath.Join(tempDir, "consumer.key")
	keyManager := keys.NewKeyManager(keyPath, logger)
	privKey, err := keyManager.GetOrCreatePrivateKey()
	if err != nil {
		t.Fatalf("Failed to create private key: %v", err)
	}

	// Create consumer with nil bootstrap peers (should fallback to defaults)
	consumer, err := NewConsumerWithBootstrapPeers(ctx, logger, privKey, nil)
	if err != nil {
		t.Fatalf("Failed to create consumer: %v", err)
	}

	// Verify consumer has peer addresses
	peerAddrs := consumer.GetPeerAddrs()
	if len(peerAddrs) == 0 {
		t.Error("Expected consumer to have peer addresses after fallback to defaults")
	}

	peerID := consumer.GetPeerID()
	if peerID == "" {
		t.Error("Expected consumer to have a peer ID")
	}

	primaryAddr := consumer.GetPrimaryPeerAddr()
	if primaryAddr == "" {
		t.Error("Expected consumer to have a primary peer address")
	}

	t.Logf("Consumer created with peer ID: %s", peerID)
	t.Logf("Consumer peer addresses: %v", peerAddrs)
	t.Logf("Consumer primary address: %s", primaryAddr)
}
