package consumer

import (
	"context"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"go.uber.org/zap"

	"github.com/crowdllama/crowdllama/pkg/config"
)

func TestWorkerHealthManagement(t *testing.T) {
	ctx := context.Background()
	logger, _ := zap.NewDevelopment()
	privKey, _, _ := crypto.GenerateKeyPair(crypto.Ed25519, 256)

	cfg := config.NewConfiguration()
	consumer, err := NewConsumerWithConfig(ctx, logger, privKey, cfg)
	if err != nil {
		t.Fatalf("Failed to create consumer: %v", err)
	}

	// Test health status endpoint
	healthStatus := consumer.GetWorkerHealthStatus()
	if healthStatus == nil {
		t.Error("Expected health status to be non-nil")
	}

	// Test that no workers are available initially
	availableWorkers := consumer.GetAvailableWorkers()
	if len(availableWorkers) != 0 {
		t.Errorf("Expected 0 available workers, got %d", len(availableWorkers))
	}

	// Test that peer manager is working
	healthyPeers := consumer.peerManager.GetHealthyPeers()
	if len(healthyPeers) != 0 {
		t.Errorf("Expected 0 healthy peers, got %d", len(healthyPeers))
	}
}
