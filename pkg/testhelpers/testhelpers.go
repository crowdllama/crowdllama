// Package testhelpers provides utility functions for testing CrowdLlama components.
package testhelpers

import (
	"context"
	"fmt"
	"hash/fnv"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"go.uber.org/zap"

	"github.com/matiasinsaurralde/crowdllama/pkg/dht"
)

// CreateIsolatedTestDHT starts a DHT server on a unique port and returns the server and its bootstrap address.
// If privKey is nil, a new key pair will be generated.
func CreateIsolatedTestDHT(
	ctx context.Context,
	t *testing.T,
	logger *zap.Logger,
	port int,
	privKey crypto.PrivKey,
) (dhtServer *dht.Server, bootstrapAddr string) {
	t.Helper()

	var err error
	if privKey == nil {
		privKey, _, err = crypto.GenerateKeyPair(crypto.Ed25519, 256)
		if err != nil {
			t.Fatalf("Failed to generate bootstrap key: %v", err)
		}
	}

	listenAddr := fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", port)
	dhtServer, err = dht.NewDHTServerWithAddrs(ctx, privKey, logger, []string{listenAddr})
	if err != nil {
		t.Fatalf("Failed to create DHT server: %v", err)
	}

	_, err = dhtServer.Start()
	if err != nil {
		t.Fatalf("Failed to start DHT server: %v", err)
	}

	// Wait for DHT to be ready
	time.Sleep(2 * time.Second)

	addrs := dhtServer.GetPeerAddrs()
	if len(addrs) == 0 {
		t.Fatalf("DHT server has no addresses")
	}
	bootstrapAddr = addrs[0]

	// Additional wait to ensure DHT is fully ready
	time.Sleep(1 * time.Second)

	return dhtServer, bootstrapAddr
}

// GetTestPort returns a unique port for each test based on the test name.
func GetTestPort(t *testing.T) int {
	t.Helper()
	hash := fnv.New32a()
	if _, err := hash.Write([]byte(t.Name())); err != nil {
		t.Fatalf("Failed to hash test name for port: %v", err)
	}
	port := 10000 + int(hash.Sum32()%5000) // Ports 10000-14999
	return port
}
