package keys

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

const (
	// DefaultKeyDir is the default directory for storing keys
	DefaultKeyDir = ".crowdllama"
)

// KeyManager handles private key operations for libp2p components
type KeyManager struct {
	keyPath string
	logger  *zap.Logger
}

// NewKeyManager creates a new key manager
func NewKeyManager(keyPath string, logger *zap.Logger) *KeyManager {
	return &KeyManager{
		keyPath: keyPath,
		logger:  logger,
	}
}

// GetOrCreatePrivateKey ensures that a private key exists at the specified path
// If it doesn't exist, it creates the directory and generates a new key
func (km *KeyManager) GetOrCreatePrivateKey() (crypto.PrivKey, error) {
	// Check if the directory exists
	keyDir := filepath.Dir(km.keyPath)
	if _, err := os.Stat(keyDir); os.IsNotExist(err) {
		km.logger.Info("Creating key directory", zap.String("path", keyDir))
		if err := os.MkdirAll(keyDir, 0700); err != nil {
			return nil, fmt.Errorf("failed to create directory %s: %w", keyDir, err)
		}
	}

	// Check if the key file exists
	if _, err := os.Stat(km.keyPath); os.IsNotExist(err) {
		km.logger.Info("Generating new private key", zap.String("path", km.keyPath))

		// Generate new private key
		privKey, _, err := crypto.GenerateEd25519Key(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("failed to generate private key: %w", err)
		}

		// Save to disk
		keyBytes, err := crypto.MarshalPrivateKey(privKey)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal private key: %w", err)
		}

		if err := os.WriteFile(km.keyPath, keyBytes, 0600); err != nil {
			return nil, fmt.Errorf("failed to write private key to %s: %w", km.keyPath, err)
		}

		peerID := getPeerIDFromKey(privKey)
		km.logger.Info("Successfully generated and saved private key",
			zap.String("path", km.keyPath),
			zap.String("peer_id", peerID))

		return privKey, nil
	}

	// Load existing key
	km.logger.Info("Loading existing private key", zap.String("path", km.keyPath))

	keyBytes, err := os.ReadFile(km.keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read private key from %s: %w", km.keyPath, err)
	}

	privKey, err := crypto.UnmarshalPrivateKey(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal private key from %s: %w", km.keyPath, err)
	}

	peerID := getPeerIDFromKey(privKey)
	km.logger.Info("Successfully loaded private key",
		zap.String("path", km.keyPath),
		zap.String("peer_id", peerID))

	return privKey, nil
}

// LoadPrivateKey loads a private key from the specified path without creating one
func (km *KeyManager) LoadPrivateKey() (crypto.PrivKey, error) {
	km.logger.Info("Loading private key", zap.String("path", km.keyPath))

	keyBytes, err := os.ReadFile(km.keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read private key from %s: %w", km.keyPath, err)
	}

	privKey, err := crypto.UnmarshalPrivateKey(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal private key from %s: %w", km.keyPath, err)
	}

	peerID := getPeerIDFromKey(privKey)
	km.logger.Info("Successfully loaded private key",
		zap.String("path", km.keyPath),
		zap.String("peer_id", peerID))

	return privKey, nil
}

// GetDefaultKeyPath returns the default key path for a component
func GetDefaultKeyPath(component string) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	return filepath.Join(homeDir, DefaultKeyDir, component+".key"), nil
}

// getPeerIDFromKey extracts the peer ID from a private key for logging
func getPeerIDFromKey(privKey crypto.PrivKey) string {
	pubKey := privKey.GetPublic()
	peerID, err := peer.IDFromPublicKey(pubKey)
	if err != nil {
		return "unknown"
	}
	return peerID.String()
}
