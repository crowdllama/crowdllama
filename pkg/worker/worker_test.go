package worker

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"go.uber.org/zap"

	"github.com/matiasinsaurralde/crowdllama/pkg/config"
	"github.com/matiasinsaurralde/crowdllama/pkg/crowdllama"
	"github.com/matiasinsaurralde/crowdllama/pkg/testhelpers"
)

func TestUpdateMetadata(t *testing.T) {
	// Create a worker with mocked dependencies
	worker := &Worker{
		Metadata: crowdllama.NewCrowdLlamaResource("test-peer"),
	}

	// Store initial timestamp
	initialTime := worker.Metadata.LastUpdated

	// Wait a bit to ensure timestamp difference
	time.Sleep(10 * time.Millisecond)

	// Call the new UpdateMetadata method
	err := worker.UpdateMetadata()
	if err != nil {
		t.Fatalf("UpdateMetadata failed: %v", err)
	}

	// Verify metadata was updated with hardcoded values
	expectedModels := []string{"llama-2-7b", "llama-2-13b", "mistral-7b", "tinyllama"}
	if len(worker.Metadata.SupportedModels) != len(expectedModels) {
		t.Errorf("Expected %d models, got %d", len(expectedModels), len(worker.Metadata.SupportedModels))
	}

	for i, model := range expectedModels {
		if worker.Metadata.SupportedModels[i] != model {
			t.Errorf("Expected model %s at index %d, got %s", model, i, worker.Metadata.SupportedModels[i])
		}
	}

	expectedThroughput := 150.0
	if worker.Metadata.TokensThroughput != expectedThroughput {
		t.Errorf("Expected TokensThroughput %f, got %f", expectedThroughput, worker.Metadata.TokensThroughput)
	}

	expectedVRAM := 24
	if worker.Metadata.VRAMGB != expectedVRAM {
		t.Errorf("Expected VRAMGB %d, got %d", expectedVRAM, worker.Metadata.VRAMGB)
	}

	expectedLoad := 0.3
	if worker.Metadata.Load != expectedLoad {
		t.Errorf("Expected Load %f, got %f", expectedLoad, worker.Metadata.Load)
	}

	expectedGPU := "RTX 4090"
	if worker.Metadata.GPUModel != expectedGPU {
		t.Errorf("Expected GPUModel %s, got %s", expectedGPU, worker.Metadata.GPUModel)
	}

	// Verify Version was set (should not be "unknown" after update)
	if worker.Metadata.Version == "unknown" {
		t.Logf("Version is 'unknown' - this is expected for local builds without linker flags")
	} else {
		t.Logf("Version set to: %s", worker.Metadata.Version)
	}

	// Verify LastUpdated was updated
	if worker.Metadata.LastUpdated.Equal(initialTime) {
		t.Error("Expected LastUpdated to be updated, got same time")
	}

	if worker.Metadata.LastUpdated.IsZero() {
		t.Error("Expected LastUpdated to be set, got zero time")
	}
}

func TestUpdateMetadataInterval(t *testing.T) {
	// Test that the metadata update interval can be set and retrieved
	originalInterval := GetMetadataUpdateInterval()

	// Set a new interval
	newInterval := 60 * time.Second
	SetMetadataUpdateInterval(newInterval)

	// Verify it was set correctly
	if GetMetadataUpdateInterval() != newInterval {
		t.Errorf("Expected interval %v, got %v", newInterval, GetMetadataUpdateInterval())
	}

	// Restore original interval
	SetMetadataUpdateInterval(originalInterval)
}

func TestWorkerDHTReconnection_Isolated(t *testing.T) {
	logger := zap.NewNop()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. Start isolated DHT with a specific private key
	port := testhelpers.GetTestPort(t)
	dhtPrivKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 256)
	if err != nil {
		t.Fatalf("Failed to generate DHT key: %v", err)
	}

	dhtServer, bootstrapAddr := testhelpers.CreateIsolatedTestDHT(ctx, t, logger, port, dhtPrivKey)
	defer dhtServer.Stop()

	// 2. Start worker with DHT as bootstrap peer
	cfg := &config.Configuration{BootstrapPeers: []string{bootstrapAddr}}
	privKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 256)
	if err != nil {
		t.Fatalf("Failed to generate key pair: %v", err)
	}
	worker, err := NewWorkerWithConfig(ctx, privKey, cfg)
	if err != nil {
		t.Fatalf("Failed to create worker: %v", err)
	}
	defer worker.StopMetadataUpdates()

	// 3. Wait for worker to connect to DHT
	waitForCondition(t, 10*time.Second, func() bool {
		return worker.IsDHTConnected()
	}, "worker to connect to DHT")

	// 4. Stop the DHT server
	dhtServer.Stop()

	// 5. Wait for worker to detect DHT disconnection
	waitForCondition(t, 10*time.Second, func() bool {
		_ = worker.PublishMetadata(ctx) // Trigger DHT activity to detect disconnection
		return !worker.IsDHTConnected()
	}, "worker to detect DHT disconnection")

	// 6. Restart DHT server with the same private key
	dhtServer2, _ := testhelpers.CreateIsolatedTestDHT(ctx, t, logger, port, dhtPrivKey)
	defer dhtServer2.Stop()

	// 7. Wait for worker to reconnect
	waitForCondition(t, 10*time.Second, func() bool {
		_ = worker.PublishMetadata(ctx) // Actively trigger DHT activity to prompt reconnection
		return worker.IsDHTConnected()
	}, "worker to reconnect to DHT")
}

// waitForCondition polls until the condition returns true or times out.
func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool, desc string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Condition not met within %v: %s", timeout, desc)
}
