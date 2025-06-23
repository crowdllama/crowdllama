package consumer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/matiasinsaurralde/crowdllama/internal/keys"
	"github.com/matiasinsaurralde/crowdllama/pkg/dht"
)

func TestConsumerDHTIntegration(t *testing.T) {
	// Skip this test if not running integration tests
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	logger, _ := zap.NewDevelopment()

	// Create temporary directory for keys
	tempDir, err := os.MkdirTemp("", "crowdllama-consumer-integration-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tempDir); removeErr != nil {
			t.Logf("Failed to remove temp dir: %v", removeErr)
		}
	}()

	// Create temporary keys for DHT server and consumer
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

	// Step 2: Initialize Consumer with DHT server as bootstrap peer
	t.Log("Step 2: Initializing Consumer with DHT server as bootstrap peer")
	consumer, err := NewConsumerWithBootstrapPeers(ctx, logger, consumerPrivKey, []string{dhtPeerAddr})
	if err != nil {
		t.Fatalf("Failed to create consumer: %v", err)
	}

	t.Logf("Consumer created with peer ID: %s", consumer.GetPeerID())
	t.Logf("Consumer peer addresses: %v", consumer.GetPeerAddrs())

	// Step 3: Wait for consumer to connect to DHT and be discovered
	t.Log("Step 3: Waiting for consumer to connect and be discovered")

	// Give some time for the consumer to connect to the DHT
	time.Sleep(2 * time.Second)

	// Check if DHT server knows about the consumer
	maxAttempts := 10
	attempt := 0
	consumerFound := false
	consumerPeerID := consumer.GetPeerID()

	for attempt < maxAttempts {
		attempt++
		t.Logf("Attempt %d/%d: Checking if DHT server knows about consumer", attempt, maxAttempts)

		// Check if consumer is connected to DHT
		if dhtServer.HasPeer(consumerPeerID) {
			t.Logf("Consumer peer ID %s found in DHT server's connected peers", consumerPeerID)
			consumerFound = true
			break
		}

		// Also check if consumer is advertising in the DHT
		connectedPeers := dhtServer.GetPeers()
		t.Logf("DHT server connected peers: %v", connectedPeers)

		if len(connectedPeers) > 0 {
			t.Logf("DHT server has %d connected peers", len(connectedPeers))
		}

		// Wait before next attempt
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

	// Step 4: Validate consumer can discover workers (even if none exist)
	t.Log("Step 4: Validating consumer can discover workers")

	// Start background discovery
	consumer.StartBackgroundDiscovery()
	defer consumer.StopBackgroundDiscovery()

	// Give some time for discovery to run
	time.Sleep(3 * time.Second)

	// Check if consumer has any workers (should be empty in this test)
	availableWorkers := consumer.GetAvailableWorkers()
	t.Logf("Consumer has %d available workers", len(availableWorkers))

	// Step 5: Test consumer peer information methods
	t.Log("Step 5: Testing consumer peer information methods")

	// Test peer ID
	peerID := consumer.GetPeerID()
	if peerID == "" {
		t.Error("Expected consumer to have a peer ID")
	} else {
		t.Logf("✅ Consumer peer ID: %s", peerID)
	}

	// Test peer addresses
	peerAddrs := consumer.GetPeerAddrs()
	if len(peerAddrs) == 0 {
		t.Error("Expected consumer to have peer addresses")
	} else {
		t.Logf("✅ Consumer peer addresses: %v", peerAddrs)
	}

	// Test primary peer address
	primaryAddr := consumer.GetPrimaryPeerAddr()
	if primaryAddr == "" {
		t.Error("Expected consumer to have a primary peer address")
	} else {
		t.Logf("✅ Consumer primary address: %s", primaryAddr)
	}

	// Test connected peers
	connectedPeers := consumer.GetPeers()
	t.Logf("✅ Consumer connected peers: %v", connectedPeers)
	t.Logf("✅ Consumer connected peers count: %d", consumer.GetConnectedPeersCount())

	// Step 6: Validate bidirectional connection
	t.Log("Step 6: Validating bidirectional connection")

	// Check if consumer knows about the DHT server
	dhtServerPeerID := dhtServer.GetPeerID()
	if consumer.HasPeer(dhtServerPeerID) {
		t.Logf("✅ SUCCESS: Consumer knows about DHT server peer ID %s", dhtServerPeerID)
	} else {
		t.Logf("❌ Consumer does not know about DHT server peer ID %s", dhtServerPeerID)
	}

	t.Log("Integration test completed successfully")
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
