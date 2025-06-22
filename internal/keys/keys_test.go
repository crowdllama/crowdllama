package keys

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"go.uber.org/zap"
)

func TestNewKeyManager(t *testing.T) {
	logger := zap.NewNop()
	keyPath := "/tmp/test.key"

	km := NewKeyManager(keyPath, logger)

	if km == nil {
		t.Fatal("Expected KeyManager to be created, got nil")
	}

	if km.keyPath != keyPath {
		t.Errorf("Expected keyPath %s, got %s", keyPath, km.keyPath)
	}

	if km.logger != logger {
		t.Error("Expected logger to be set correctly")
	}
}

func TestGetDefaultKeyPath(t *testing.T) {
	tests := []struct {
		component string
		expected  string
	}{
		{"dht", "dht.key"},
		{"worker", "worker.key"},
		{"consumer", "consumer.key"},
		{"test", "test.key"},
	}

	for _, tt := range tests {
		t.Run(tt.component, func(t *testing.T) {
			path, err := GetDefaultKeyPath(tt.component)
			if err != nil {
				t.Fatalf("GetDefaultKeyPath failed: %v", err)
			}

			// Check that the path ends with the expected filename
			if filepath.Base(path) != tt.expected {
				t.Errorf("Expected path to end with %s, got %s", tt.expected, filepath.Base(path))
			}

			// Check that the path contains the .crowdllama directory
			if !filepath.HasPrefix(path, filepath.Join(os.Getenv("HOME"), DefaultKeyDir)) {
				t.Errorf("Expected path to be in .crowdllama directory, got %s", path)
			}
		})
	}
}

func TestGetOrCreatePrivateKey_NewKey(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	keyPath := filepath.Join(tempDir, "test.key")
	logger := zap.NewNop()

	km := NewKeyManager(keyPath, logger)

	// Test creating a new key
	privKey, err := km.GetOrCreatePrivateKey()
	if err != nil {
		t.Fatalf("GetOrCreatePrivateKey failed: %v", err)
	}

	if privKey == nil {
		t.Fatal("Expected private key to be created, got nil")
	}

	// Verify the key file was created
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		t.Error("Expected key file to be created")
	}

	// Verify the key can be loaded again
	loadedKey, err := km.LoadPrivateKey()
	if err != nil {
		t.Fatalf("LoadPrivateKey failed: %v", err)
	}

	// Compare the keys
	keyBytes1, err := crypto.MarshalPrivateKey(privKey)
	if err != nil {
		t.Fatalf("Failed to marshal original key: %v", err)
	}

	keyBytes2, err := crypto.MarshalPrivateKey(loadedKey)
	if err != nil {
		t.Fatalf("Failed to marshal loaded key: %v", err)
	}

	if string(keyBytes1) != string(keyBytes2) {
		t.Error("Loaded key does not match original key")
	}
}

func TestGetOrCreatePrivateKey_ExistingKey(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	keyPath := filepath.Join(tempDir, "test.key")
	logger := zap.NewNop()

	km := NewKeyManager(keyPath, logger)

	// Create a key first
	originalKey, err := km.GetOrCreatePrivateKey()
	if err != nil {
		t.Fatalf("Failed to create initial key: %v", err)
	}

	// Test loading the existing key
	loadedKey, err := km.GetOrCreatePrivateKey()
	if err != nil {
		t.Fatalf("GetOrCreatePrivateKey failed to load existing key: %v", err)
	}

	if loadedKey == nil {
		t.Fatal("Expected private key to be loaded, got nil")
	}

	// Compare the keys
	keyBytes1, err := crypto.MarshalPrivateKey(originalKey)
	if err != nil {
		t.Fatalf("Failed to marshal original key: %v", err)
	}

	keyBytes2, err := crypto.MarshalPrivateKey(loadedKey)
	if err != nil {
		t.Fatalf("Failed to marshal loaded key: %v", err)
	}

	if string(keyBytes1) != string(keyBytes2) {
		t.Error("Loaded key does not match original key")
	}
}

func TestLoadPrivateKey_FileNotExists(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	keyPath := filepath.Join(tempDir, "nonexistent.key")
	logger := zap.NewNop()

	km := NewKeyManager(keyPath, logger)

	// Test loading a non-existent key
	_, err := km.LoadPrivateKey()
	if err == nil {
		t.Fatal("Expected error when loading non-existent key")
	}

	// Check that the error message is helpful
	if err.Error() == "" {
		t.Error("Expected error message to be non-empty")
	}
}

func TestLoadPrivateKey_InvalidKey(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	keyPath := filepath.Join(tempDir, "invalid.key")
	logger := zap.NewNop()

	km := NewKeyManager(keyPath, logger)

	// Create an invalid key file
	invalidData := []byte("this is not a valid private key")
	err := os.WriteFile(keyPath, invalidData, 0600)
	if err != nil {
		t.Fatalf("Failed to write invalid key file: %v", err)
	}

	// Test loading an invalid key
	_, err = km.LoadPrivateKey()
	if err == nil {
		t.Fatal("Expected error when loading invalid key")
	}

	// Check that the error message is helpful
	if err.Error() == "" {
		t.Error("Expected error message to be non-empty")
	}
}

func TestGetOrCreatePrivateKey_CreatesDirectory(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	keyDir := filepath.Join(tempDir, "newdir")
	keyPath := filepath.Join(keyDir, "test.key")
	logger := zap.NewNop()

	km := NewKeyManager(keyPath, logger)

	// Verify the directory doesn't exist initially
	if _, err := os.Stat(keyDir); !os.IsNotExist(err) {
		t.Fatal("Expected directory to not exist initially")
	}

	// Test creating a key (should create the directory)
	_, err := km.GetOrCreatePrivateKey()
	if err != nil {
		t.Fatalf("GetOrCreatePrivateKey failed: %v", err)
	}

	// Verify the directory was created
	if _, err := os.Stat(keyDir); os.IsNotExist(err) {
		t.Error("Expected directory to be created")
	}

	// Verify the key file was created
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		t.Error("Expected key file to be created")
	}
}

func TestGetPeerIDFromKey(t *testing.T) {
	// Generate a test key
	privKey, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate test key: %v", err)
	}

	// Test extracting peer ID
	peerID := getPeerIDFromKey(privKey)

	if peerID == "" {
		t.Error("Expected peer ID to be non-empty")
	}

	if peerID == "unknown" {
		t.Error("Expected valid peer ID, got 'unknown'")
	}

	// Verify the peer ID format (should be a valid multihash)
	if len(peerID) < 10 {
		t.Errorf("Expected peer ID to be longer, got %s", peerID)
	}
}

func TestKeyManager_ConcurrentAccess(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	keyPath := filepath.Join(tempDir, "concurrent.key")
	logger := zap.NewNop()

	km := NewKeyManager(keyPath, logger)

	// Test concurrent access to key creation
	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- true }()

			_, err := km.GetOrCreatePrivateKey()
			if err != nil {
				t.Errorf("Concurrent key creation failed: %v", err)
			}
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify only one key file was created
	files, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("Failed to read temp directory: %v", err)
	}

	if len(files) != 1 {
		t.Errorf("Expected 1 key file, found %d", len(files))
	}
}

func TestKeyManager_FilePermissions(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	keyPath := filepath.Join(tempDir, "permissions.key")
	logger := zap.NewNop()

	km := NewKeyManager(keyPath, logger)

	// Create a key
	_, err := km.GetOrCreatePrivateKey()
	if err != nil {
		t.Fatalf("GetOrCreatePrivateKey failed: %v", err)
	}

	// Check file permissions
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("Failed to stat key file: %v", err)
	}

	// Check that the file has restrictive permissions (0600)
	mode := info.Mode()
	if mode&0777 != 0600 {
		t.Errorf("Expected file permissions 0600, got %o", mode&0777)
	}

	// Note: We don't test directory permissions here because t.TempDir() creates
	// directories with different permissions than what we set in the actual code.
	// The actual .crowdllama directory will have 0700 permissions when created
	// by the KeyManager, but the test temp directory has different permissions.
}
