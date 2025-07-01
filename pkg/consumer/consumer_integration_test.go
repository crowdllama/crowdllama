package consumer

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
	"github.com/matiasinsaurralde/crowdllama/pkg/config"
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

func TestConsumerDHTIntegration(t *testing.T) {
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

	cfg := config.NewConfiguration()
	cfg.BootstrapPeers = []string{dhtPeerAddr}
	consumer, err := NewConsumerWithConfig(ctx, logger, consumerPrivKey, cfg)
	if err != nil {
		t.Fatalf("Failed to create consumer: %v", err)
	}
	stepLogConsumerInfo(t, consumer)
	stepWaitForConsumerDiscovery(t, dhtServer, consumer)
	stepValidateWorkerDiscovery(t, consumer)
	stepTestConsumerPeerInfo(t, consumer)
	stepValidateBidirectionalConnection(t, consumer, dhtServer)
	t.Log("Integration test completed successfully")
}

func stepInitDHTServer(ctx context.Context, t *testing.T, dhtPrivKey crypto.PrivKey, logger *zap.Logger) *dht.Server {
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
	cfg := config.NewConfiguration()
	cfg.BootstrapPeers = []string{}
	consumer, err := NewConsumerWithConfig(ctx, logger, privKey, cfg)
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
	cfg := config.NewConfiguration()
	cfg.BootstrapPeers = nil
	consumer, err := NewConsumerWithConfig(ctx, logger, privKey, cfg)
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
