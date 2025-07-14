// Package crowdllama provides core types and utilities for the CrowdLlama system.
package crowdllama

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/protocol"
)

const (
	// CrowdLlamaProtocol is the custom protocol for CrowdLlama DHT operations
	CrowdLlamaProtocol = "/crowdllama/1.0.0"

	// MetadataProtocol is the protocol for requesting peer metadata
	MetadataProtocol = "/crowdllama/metadata/1.0.0"

	// InferenceProtocol is the protocol identifier for inference requests
	InferenceProtocol = "/crowdllama/inference/1.0.0"

	// PeerMetadataPrefix is the DHT key prefix for peer metadata
	PeerMetadataPrefix = "/crowdllama/peer/"

	// PeerNamespace is the namespace used for peer discovery in the DHT
	PeerNamespace = "crowdllama-ns"
)

// Resource represents a CrowdLlama resource (peer metadata)
type Resource struct {
	PeerID           string    `json:"peer_id"`
	SupportedModels  []string  `json:"supported_models"`
	TokensThroughput float64   `json:"tokens_throughput"` // tokens/sec
	VRAMGB           int       `json:"vram_gb"`
	Load             float64   `json:"load"` // current load (0.0 to 1.0)
	GPUModel         string    `json:"gpu_model"`
	LastUpdated      time.Time `json:"last_updated"`
	Version          string    `json:"version"`     // CrowdLlama version (git commit hash)
	WorkerMode       bool      `json:"worker_mode"` // true if this peer is in worker mode
}

// NewCrowdLlamaResource creates a new resource with the given peer ID
func NewCrowdLlamaResource(peerID string) *Resource {
	return &Resource{
		PeerID:           peerID,
		SupportedModels:  []string{},
		TokensThroughput: 0.0,
		VRAMGB:           0,
		Load:             0.0,
		GPUModel:         "",
		LastUpdated:      time.Now(),
		Version:          "unknown", // Will be set during metadata update
		WorkerMode:       false,     // Default to consumer mode
	}
}

// ToJSON serializes the resource to JSON
func (r *Resource) ToJSON() ([]byte, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("marshal resource: %w", err)
	}
	return data, nil
}

// FromJSON creates a CrowdLlamaResource from JSON bytes
func FromJSON(data []byte) (*Resource, error) {
	var resource Resource
	err := json.Unmarshal(data, &resource)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal CrowdLlamaResource: %w", err)
	}
	return &resource, nil
}

// GetDHTKey returns the DHT key for this peer's metadata
func (r *Resource) GetDHTKey() string {
	return "/ipns/" + r.PeerID
}

// GetProtocol returns the CrowdLlama protocol ID
func GetProtocol() protocol.ID {
	return protocol.ID(CrowdLlamaProtocol)
}
