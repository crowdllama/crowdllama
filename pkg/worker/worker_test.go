package worker

import (
	"testing"

	"github.com/matiasinsaurralde/crowdllama/pkg/crowdllama"
)

func TestUpdateMetadata(t *testing.T) {
	// Create a worker with mocked dependencies
	worker := &Worker{
		Metadata: crowdllama.NewCrowdLlamaResource("test-peer"),
	}

	models := []string{"model1", "model2", "model3"}
	tokensThroughput := 150.5
	vramGB := 16
	load := 0.75
	gpuModel := "RTX 4090"

	worker.UpdateMetadata(models, tokensThroughput, vramGB, load, gpuModel)

	// Verify metadata was updated
	if len(worker.Metadata.SupportedModels) != len(models) {
		t.Errorf("Expected %d models, got %d", len(models), len(worker.Metadata.SupportedModels))
	}

	for i, model := range models {
		if worker.Metadata.SupportedModels[i] != model {
			t.Errorf("Expected model %s at index %d, got %s", model, i, worker.Metadata.SupportedModels[i])
		}
	}

	if worker.Metadata.TokensThroughput != tokensThroughput {
		t.Errorf("Expected TokensThroughput %f, got %f", tokensThroughput, worker.Metadata.TokensThroughput)
	}

	if worker.Metadata.VRAMGB != vramGB {
		t.Errorf("Expected VRAMGB %d, got %d", vramGB, worker.Metadata.VRAMGB)
	}

	if worker.Metadata.Load != load {
		t.Errorf("Expected Load %f, got %f", load, worker.Metadata.Load)
	}

	if worker.Metadata.GPUModel != gpuModel {
		t.Errorf("Expected GPUModel %s, got %s", gpuModel, worker.Metadata.GPUModel)
	}

	// Verify LastUpdated was set
	if worker.Metadata.LastUpdated.IsZero() {
		t.Error("Expected LastUpdated to be set, got zero time")
	}
}

func TestUpdateMetadataEmptyModels(t *testing.T) {
	worker := &Worker{
		Metadata: crowdllama.NewCrowdLlamaResource("test-peer"),
	}

	// Test with empty models slice
	worker.UpdateMetadata([]string{}, 100.0, 8, 0.5, "GTX 1080")

	if len(worker.Metadata.SupportedModels) != 0 {
		t.Errorf("Expected 0 models, got %d", len(worker.Metadata.SupportedModels))
	}
}

func TestUpdateMetadataZeroValues(t *testing.T) {
	worker := &Worker{
		Metadata: crowdllama.NewCrowdLlamaResource("test-peer"),
	}

	// Test with zero values
	worker.UpdateMetadata([]string{"test"}, 0.0, 0, 0.0, "")

	if worker.Metadata.TokensThroughput != 0.0 {
		t.Errorf("Expected TokensThroughput 0.0, got %f", worker.Metadata.TokensThroughput)
	}

	if worker.Metadata.VRAMGB != 0 {
		t.Errorf("Expected VRAMGB 0, got %d", worker.Metadata.VRAMGB)
	}

	if worker.Metadata.Load != 0.0 {
		t.Errorf("Expected Load 0.0, got %f", worker.Metadata.Load)
	}

	if worker.Metadata.GPUModel != "" {
		t.Errorf("Expected GPUModel empty string, got %s", worker.Metadata.GPUModel)
	}
}
