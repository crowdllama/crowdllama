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

	// MetadataProtocol is the protocol for requesting worker metadata
	MetadataProtocol = "/crowdllama/metadata/1.0.0"

	// WorkerMetadataPrefix is the DHT key prefix for worker metadata
	WorkerMetadataPrefix = "/crowdllama/worker/"

	// WorkerNamespace is the namespace used for worker discovery in the DHT
	WorkerNamespace = "crowdllama-demo-namespace"
)

// Resource represents a CrowdLlama resource (worker metadata)
type Resource struct {
	PeerID           string    `json:"peer_id"`
	SupportedModels  []string  `json:"supported_models"`
	TokensThroughput float64   `json:"tokens_throughput"` // tokens/sec
	VRAMGB           int       `json:"vram_gb"`
	Load             float64   `json:"load"` // current load (0.0 to 1.0)
	GPUModel         string    `json:"gpu_model"`
	LastUpdated      time.Time `json:"last_updated"`
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

// GetDHTKey returns the DHT key for this worker's metadata
func (r *Resource) GetDHTKey() string {
	return "/ipns/" + r.PeerID
}

// GetProtocol returns the CrowdLlama protocol ID
func GetProtocol() protocol.ID {
	return protocol.ID(CrowdLlamaProtocol)
}
