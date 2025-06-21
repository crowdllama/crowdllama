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

	// DHT key prefix for worker metadata
	WorkerMetadataPrefix = "/crowdllama/worker/"

	// WorkerNamespace is the namespace used for worker discovery in the DHT
	WorkerNamespace = "crowdllama-demo-namespace"
)

// CrowdLlamaResource represents worker metadata stored in the DHT
type CrowdLlamaResource struct {
	PeerID           string    `json:"peer_id"`
	SupportedModels  []string  `json:"supported_models"`
	TokensThroughput float64   `json:"tokens_throughput"` // tokens/sec
	VRAMGB           int       `json:"vram_gb"`
	Load             float64   `json:"load"` // current load (0.0 to 1.0)
	GPUModel         string    `json:"gpu_model"`
	LastUpdated      time.Time `json:"last_updated"`
}

// NewCrowdLlamaResource creates a new CrowdLlamaResource with default values
func NewCrowdLlamaResource(peerID string) *CrowdLlamaResource {
	return &CrowdLlamaResource{
		PeerID:           peerID,
		SupportedModels:  []string{},
		TokensThroughput: 0.0,
		VRAMGB:           0,
		Load:             0.0,
		GPUModel:         "",
		LastUpdated:      time.Now(),
	}
}

// ToJSON converts the resource to JSON bytes
func (r *CrowdLlamaResource) ToJSON() ([]byte, error) {
	return json.Marshal(r)
}

// FromJSON creates a CrowdLlamaResource from JSON bytes
func FromJSON(data []byte) (*CrowdLlamaResource, error) {
	var resource CrowdLlamaResource
	err := json.Unmarshal(data, &resource)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal CrowdLlamaResource: %w", err)
	}
	return &resource, nil
}

// GetDHTKey returns the DHT key for this worker's metadata
func (r *CrowdLlamaResource) GetDHTKey() string {
	return "/ipns/" + r.PeerID
}

// GetProtocol returns the CrowdLlama protocol ID
func GetProtocol() protocol.ID {
	return protocol.ID(CrowdLlamaProtocol)
}
