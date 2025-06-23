package dht

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/matiasinsaurralde/crowdllama/internal/keys"
)

func TestNewDHTServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logger, _ := zap.NewDevelopment()

	// Create temporary directory for keys
	tempDir, err := os.MkdirTemp("", "crowdllama-dht-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tempDir); removeErr != nil {
			t.Logf("Failed to remove temp dir: %v", removeErr)
		}
	}()

	// Create a temporary key for testing
	keyPath := filepath.Join(tempDir, "dht.key")
	keyManager := keys.NewKeyManager(keyPath, logger)
	privKey, err := keyManager.GetOrCreatePrivateKey()
	if err != nil {
		t.Fatalf("Failed to create private key: %v", err)
	}

	// Create DHT server
	server, err := NewDHTServer(ctx, privKey, logger)
	if err != nil {
		t.Fatalf("Failed to create DHT server: %v", err)
	}
	defer server.Stop()

	// Verify server has peer addresses
	peerAddrs := server.GetPeerAddrs()
	if len(peerAddrs) == 0 {
		t.Error("Expected DHT server to have peer addresses")
	}

	peerID := server.GetPeerID()
	if peerID == "" {
		t.Error("Expected DHT server to have a peer ID")
	}

	primaryAddr := server.GetPrimaryPeerAddr()
	if primaryAddr == "" {
		t.Error("Expected DHT server to have a primary peer address")
	}

	t.Logf("DHT server created with peer ID: %s", peerID)
	t.Logf("DHT server peer addresses: %v", peerAddrs)
	t.Logf("DHT server primary address: %s", primaryAddr)

	// Verify the primary address format matches the expected format
	// Should be like: /ip4/0.0.0.0/tcp/9000/p2p/12D3KooW...
	if len(primaryAddr) < 20 {
		t.Errorf("Primary address too short: %s", primaryAddr)
	}

	// Should contain /p2p/ which indicates it's a libp2p address
	if primaryAddr != "" && primaryAddr[:4] != "/ip4" {
		t.Errorf("Primary address should start with /ip4: %s", primaryAddr)
	}

	// Should contain the default port 9000
	if !strings.Contains(primaryAddr, "/tcp/9000") && !strings.Contains(primaryAddr, "/udp/9000") {
		t.Errorf("Primary address should contain default port 9000: %s", primaryAddr)
	}
}

func TestNewDHTServerWithCustomAddrs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logger, _ := zap.NewDevelopment()

	// Create temporary directory for keys
	tempDir, err := os.MkdirTemp("", "crowdllama-dht-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tempDir); removeErr != nil {
			t.Logf("Failed to remove temp dir: %v", removeErr)
		}
	}()

	// Create a temporary key for testing
	keyPath := filepath.Join(tempDir, "dht.key")
	keyManager := keys.NewKeyManager(keyPath, logger)
	privKey, err := keyManager.GetOrCreatePrivateKey()
	if err != nil {
		t.Fatalf("Failed to create private key: %v", err)
	}

	// Custom listen addresses
	customAddrs := []string{
		"/ip4/0.0.0.0/tcp/9001",
		"/ip4/0.0.0.0/tcp/9002",
	}

	// Create DHT server with custom addresses
	server, err := NewDHTServerWithAddrs(ctx, privKey, logger, customAddrs)
	if err != nil {
		t.Fatalf("Failed to create DHT server: %v", err)
	}
	defer server.Stop()

	// Verify server has peer addresses
	peerAddrs := server.GetPeerAddrs()
	if len(peerAddrs) == 0 {
		t.Error("Expected DHT server to have peer addresses")
	}

	// Should have addresses for both custom ports
	if len(peerAddrs) < 2 {
		t.Errorf("Expected at least 2 peer addresses, got %d", len(peerAddrs))
	}

	t.Logf("DHT server created with custom addresses: %v", peerAddrs)

	// Verify addresses contain the custom ports using substring search
	foundPort9001 := false
	foundPort9002 := false
	for _, addr := range peerAddrs {
		if strings.Contains(addr, "/tcp/9001") {
			foundPort9001 = true
		}
		if strings.Contains(addr, "/tcp/9002") {
			foundPort9002 = true
		}
	}

	if !foundPort9001 {
		t.Error("Expected to find address with port 9001")
	}
	if !foundPort9002 {
		t.Error("Expected to find address with port 9002")
	}
}

func TestNewDHTServerWithEmptyAddrs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logger, _ := zap.NewDevelopment()

	// Create temporary directory for keys
	tempDir, err := os.MkdirTemp("", "crowdllama-dht-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tempDir); removeErr != nil {
			t.Logf("Failed to remove temp dir: %v", removeErr)
		}
	}()

	// Create a temporary key for testing
	keyPath := filepath.Join(tempDir, "dht.key")
	keyManager := keys.NewKeyManager(keyPath, logger)
	privKey, err := keyManager.GetOrCreatePrivateKey()
	if err != nil {
		t.Fatalf("Failed to create private key: %v", err)
	}

	// Create DHT server with empty addresses (should fallback to defaults)
	server, err := NewDHTServerWithAddrs(ctx, privKey, logger, []string{})
	if err != nil {
		t.Fatalf("Failed to create DHT server: %v", err)
	}
	defer server.Stop()

	// Verify server has peer addresses
	peerAddrs := server.GetPeerAddrs()
	if len(peerAddrs) == 0 {
		t.Error("Expected DHT server to have peer addresses after fallback to defaults")
	}

	// Should contain default ports (9000)
	foundDefaultPort := false
	for _, addr := range peerAddrs {
		if strings.Contains(addr, "/tcp/9000") || strings.Contains(addr, "/udp/9000") {
			foundDefaultPort = true
			break
		}
	}

	if !foundDefaultPort {
		t.Error("Expected to find address with default port 9000 after fallback")
	}

	t.Logf("DHT server created with fallback addresses: %v", peerAddrs)
}

func TestNewDHTServerWithNilAddrs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logger, _ := zap.NewDevelopment()

	// Create temporary directory for keys
	tempDir, err := os.MkdirTemp("", "crowdllama-dht-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tempDir); removeErr != nil {
			t.Logf("Failed to remove temp dir: %v", removeErr)
		}
	}()

	// Create a temporary key for testing
	keyPath := filepath.Join(tempDir, "dht.key")
	keyManager := keys.NewKeyManager(keyPath, logger)
	privKey, err := keyManager.GetOrCreatePrivateKey()
	if err != nil {
		t.Fatalf("Failed to create private key: %v", err)
	}

	// Create DHT server with nil addresses (should fallback to defaults)
	server, err := NewDHTServerWithAddrs(ctx, privKey, logger, nil)
	if err != nil {
		t.Fatalf("Failed to create DHT server: %v", err)
	}
	defer server.Stop()

	// Verify server has peer addresses
	peerAddrs := server.GetPeerAddrs()
	if len(peerAddrs) == 0 {
		t.Error("Expected DHT server to have peer addresses after fallback to defaults")
	}

	// Should contain default ports (9000)
	foundDefaultPort := false
	for _, addr := range peerAddrs {
		if strings.Contains(addr, "/tcp/9000") || strings.Contains(addr, "/udp/9000") {
			foundDefaultPort = true
			break
		}
	}

	if !foundDefaultPort {
		t.Error("Expected to find address with default port 9000 after fallback")
	}

	t.Logf("DHT server created with fallback addresses: %v", peerAddrs)
}

func TestDHTServerStart(t *testing.T) {
	// Skip this test if not running integration tests
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	logger, _ := zap.NewDevelopment()

	// Create temporary directory for keys
	tempDir, err := os.MkdirTemp("", "crowdllama-dht-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tempDir); removeErr != nil {
			t.Logf("Failed to remove temp dir: %v", removeErr)
		}
	}()

	// Create a temporary key for testing
	keyPath := filepath.Join(tempDir, "dht.key")
	keyManager := keys.NewKeyManager(keyPath, logger)
	privKey, err := keyManager.GetOrCreatePrivateKey()
	if err != nil {
		t.Fatalf("Failed to create private key: %v", err)
	}

	// Create DHT server
	server, err := NewDHTServer(ctx, privKey, logger)
	if err != nil {
		t.Fatalf("Failed to create DHT server: %v", err)
	}
	defer server.Stop()

	// Start the server
	primaryAddr, err := server.Start()
	if err != nil {
		t.Fatalf("Failed to start DHT server: %v", err)
	}

	t.Logf("DHT server started with primary address: %s", primaryAddr)

	// Verify the returned address matches the primary address
	if primaryAddr != server.GetPrimaryPeerAddr() {
		t.Errorf("Start() returned address %s, but GetPrimaryPeerAddr() returned %s",
			primaryAddr, server.GetPrimaryPeerAddr())
	}

	// Give some time for the server to bootstrap
	time.Sleep(2 * time.Second)

	// Verify the server is running by checking if it has a peer ID
	peerID := server.GetPeerID()
	if peerID == "" {
		t.Error("Expected DHT server to have a peer ID after starting")
	}

	t.Logf("DHT server running with peer ID: %s", peerID)
}
