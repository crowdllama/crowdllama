package crowdllama

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/protocol"
)

func TestNewCrowdLlamaResource(t *testing.T) {
	peerID := "test-peer-id"
	resource := NewCrowdLlamaResource(peerID)

	if resource.PeerID != peerID {
		t.Errorf("Expected PeerID to be %s, got %s", peerID, resource.PeerID)
	}

	if resource.SupportedModels == nil {
		t.Error("Expected SupportedModels to be initialized as empty slice")
	}

	if resource.TokensThroughput != 0 {
		t.Errorf("Expected TokensThroughput to be 0, got %f", resource.TokensThroughput)
	}

	if resource.VRAMGB != 0 {
		t.Errorf("Expected VRAMGB to be 0, got %d", resource.VRAMGB)
	}

	if resource.Load != 0 {
		t.Errorf("Expected Load to be 0, got %f", resource.Load)
	}

	if resource.GPUModel != "" {
		t.Errorf("Expected GPUModel to be empty, got %s", resource.GPUModel)
	}
}

func TestToJSON(t *testing.T) {
	resource := NewCrowdLlamaResource("test-peer")
	resource.SupportedModels = []string{"model1", "model2"}
	resource.TokensThroughput = 100.5
	resource.VRAMGB = 8
	resource.Load = 0.75
	resource.GPUModel = "RTX 4090"
	resource.LastUpdated = time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	jsonData, err := resource.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}

	// Verify it's valid JSON
	var parsedResource CrowdLlamaResource
	err = json.Unmarshal(jsonData, &parsedResource)
	if err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}

	if parsedResource.PeerID != resource.PeerID {
		t.Errorf("Expected PeerID %s, got %s", resource.PeerID, parsedResource.PeerID)
	}

	if len(parsedResource.SupportedModels) != len(resource.SupportedModels) {
		t.Errorf("Expected %d models, got %d", len(resource.SupportedModels), len(parsedResource.SupportedModels))
	}
}

func TestFromJSON(t *testing.T) {
	originalResource := NewCrowdLlamaResource("test-peer")
	originalResource.SupportedModels = []string{"model1", "model2"}
	originalResource.TokensThroughput = 100.5
	originalResource.VRAMGB = 8
	originalResource.Load = 0.75
	originalResource.GPUModel = "RTX 4090"
	originalResource.LastUpdated = time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	jsonData, err := originalResource.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}

	parsedResource, err := FromJSON(jsonData)
	if err != nil {
		t.Fatalf("FromJSON failed: %v", err)
	}

	if parsedResource.PeerID != originalResource.PeerID {
		t.Errorf("Expected PeerID %s, got %s", originalResource.PeerID, parsedResource.PeerID)
	}

	if parsedResource.TokensThroughput != originalResource.TokensThroughput {
		t.Errorf("Expected TokensThroughput %f, got %f", originalResource.TokensThroughput, parsedResource.TokensThroughput)
	}

	if parsedResource.VRAMGB != originalResource.VRAMGB {
		t.Errorf("Expected VRAMGB %d, got %d", originalResource.VRAMGB, parsedResource.VRAMGB)
	}

	if parsedResource.Load != originalResource.Load {
		t.Errorf("Expected Load %f, got %f", originalResource.Load, parsedResource.Load)
	}

	if parsedResource.GPUModel != originalResource.GPUModel {
		t.Errorf("Expected GPUModel %s, got %s", originalResource.GPUModel, parsedResource.GPUModel)
	}
}

func TestFromJSONInvalid(t *testing.T) {
	invalidJSON := []byte(`{"invalid": "json"`)
	_, err := FromJSON(invalidJSON)
	if err == nil {
		t.Error("Expected error for invalid JSON, got nil")
	}
}

func TestGetDHTKey(t *testing.T) {
	resource := NewCrowdLlamaResource("test-peer-id")
	key := resource.GetDHTKey()

	expectedKey := "/ipns/test-peer-id"
	if key != expectedKey {
		t.Errorf("Expected DHT key %s, got %s", expectedKey, key)
	}
}

func TestGetProtocol(t *testing.T) {
	protocolID := GetProtocol()
	expectedProtocol := protocol.ID(CrowdLlamaProtocol)

	if protocolID != expectedProtocol {
		t.Errorf("Expected protocol %s, got %s", expectedProtocol, protocolID)
	}
}
