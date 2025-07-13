// Package peer provides unified peer functionality for CrowdLlama.
package peer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/ipfs/go-cid"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/multiformats/go-multihash"

	"github.com/crowdllama/crowdllama/internal/discovery"
	"github.com/crowdllama/crowdllama/pkg/config"
	"github.com/crowdllama/crowdllama/pkg/crowdllama"
	"github.com/crowdllama/crowdllama/pkg/peermanager"
	"github.com/crowdllama/crowdllama/pkg/version"
	"go.uber.org/zap"
)

// InferenceProtocol is the protocol identifier for inference requests
const InferenceProtocol = "/crowdllama/inference/1.0.0"

// MetadataUpdateInterval is the interval at which peer metadata is updated
var MetadataUpdateInterval = 30 * time.Second

// SetMetadataUpdateInterval allows programmatically setting the metadata update interval
func SetMetadataUpdateInterval(interval time.Duration) {
	MetadataUpdateInterval = interval
}

// GetMetadataUpdateInterval returns the current metadata update interval
func GetMetadataUpdateInterval() time.Duration {
	return MetadataUpdateInterval
}

// OllamaRequest represents the request structure for Ollama API
type OllamaRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

// Message represents a message in the Ollama API
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// OllamaResponse represents the response structure from Ollama API
type OllamaResponse struct {
	Model      string    `json:"model"`
	CreatedAt  time.Time `json:"created_at"`
	Message    Message   `json:"message"`
	Stream     bool      `json:"stream"`
	DoneReason string    `json:"done_reason"`
	Done       bool      `json:"done"`
}

// Peer represents a CrowdLlama peer node (can be either worker or consumer)
type Peer struct {
	Host       host.Host
	DHT        *dht.IpfsDHT
	Metadata   *crowdllama.Resource
	Config     *config.Configuration
	WorkerMode bool // true if this peer is in worker mode

	// Peer management
	PeerManager *peermanager.Manager

	// Logger
	logger *zap.Logger

	// Metadata update management
	metadataCtx    context.Context
	metadataCancel context.CancelFunc

	// Advertising management
	advertisingCtx    context.Context
	advertisingCancel context.CancelFunc

	// Bootstrap peers for reconnection
	bootstrapPeers []string
}

// NewPeerWithConfig creates a new peer instance using the provided configuration
func NewPeerWithConfig(
	ctx context.Context,
	privKey crypto.PrivKey,
	cfg *config.Configuration,
	workerMode bool,
	logger *zap.Logger,
) (*Peer, error) {
	h, kadDHT, err := discovery.NewHostAndDHT(ctx, privKey, logger)
	if err != nil {
		return nil, fmt.Errorf("new host and DHT: %w", err)
	}

	// Bootstrap with custom peers if provided, otherwise use defaults
	if len(cfg.BootstrapPeers) > 0 {
		if err := discovery.BootstrapDHTWithPeers(ctx, h, kadDHT, cfg.BootstrapPeers, logger); err != nil {
			return nil, fmt.Errorf("bootstrap DHT with custom peers: %w", err)
		}
	} else {
		if err := discovery.BootstrapDHT(ctx, h, kadDHT, logger); err != nil {
			return nil, fmt.Errorf("bootstrap DHT: %w", err)
		}
	}

	logger.Debug("BootstrapDHT completed successfully")

	// Initialize metadata
	metadata := crowdllama.NewCrowdLlamaResource(h.ID().String())
	metadata.WorkerMode = workerMode

	// Create metadata context for managing metadata updates
	metadataCtx, metadataCancel := context.WithCancel(ctx)

	// Create advertising context for managing advertising
	advertisingCtx, advertisingCancel := context.WithCancel(ctx)

	// Initialize peer manager
	peerManagerConfig := peermanager.DefaultConfig()
	if os.Getenv("CROWDLLAMA_TEST_MODE") == "1" {
		peerManagerConfig = &peermanager.Config{
			DiscoveryInterval:      2 * time.Second,
			AdvertisingInterval:    5 * time.Second,
			MetadataUpdateInterval: 5 * time.Second,
			PeerHealthConfig: &peermanager.PeerHealthConfig{
				StalePeerTimeout:    30 * time.Second, // Shorter for testing
				HealthCheckInterval: 5 * time.Second,
				MaxFailedAttempts:   2,
				BackoffBase:         5 * time.Second,
				MetadataTimeout:     2 * time.Second,
				MaxMetadataAge:      30 * time.Second,
			},
		}
	}

	peer := &Peer{
		Host:              h,
		DHT:               kadDHT,
		Metadata:          metadata,
		Config:            cfg,
		WorkerMode:        workerMode,
		PeerManager:       peermanager.NewManager(ctx, h, kadDHT, logger, peerManagerConfig),
		metadataCtx:       metadataCtx,
		metadataCancel:    metadataCancel,
		advertisingCtx:    advertisingCtx,
		advertisingCancel: advertisingCancel,
		bootstrapPeers:    cfg.BootstrapPeers,
		logger:            logger,
	}

	// Set up stream handler with the peer instance
	h.SetStreamHandler(InferenceProtocol, func(s network.Stream) {
		peer.handleInferenceRequest(ctx, s)
	})

	return peer, nil
}

// NewPeer creates a new peer instance
func NewPeer(ctx context.Context, privKey crypto.PrivKey, cfg *config.Configuration, workerMode bool, logger *zap.Logger) (*Peer, error) {
	return NewPeerWithConfig(ctx, privKey, cfg, workerMode, logger)
}

// handleInferenceRequest processes an inference request from a consumer
func (p *Peer) handleInferenceRequest(ctx context.Context, s network.Stream) {
	defer func() {
		if err := s.Close(); err != nil {
			p.logger.Debug("Failed to close stream", zap.Error(err))
		}
	}()

	p.logger.Debug("StreamHandler called for inference request")

	// Only worker peers can handle inference requests
	if !p.WorkerMode {
		p.logger.Debug("Consumer peer received inference request, ignoring")
		return
	}

	input, err := p.readInferenceInput(s)
	if err != nil {
		p.logger.Debug("Failed to read inference input", zap.Error(err))
		return
	}

	output, err := p.callOllamaAPI(ctx, input, "/api/chat")
	if err != nil {
		p.logger.Debug("Failed to call Ollama API", zap.Error(err))
		return
	}

	if err := p.writeInferenceResponse(s, output); err != nil {
		p.logger.Debug("Failed to write inference response", zap.Error(err))
		return
	}

	p.logger.Debug("Worker sent response", zap.String("output", output))
	p.logger.Debug("StreamHandler completed")
}

func (p *Peer) readInferenceInput(s network.Stream) (string, error) {
	if err := s.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return "", fmt.Errorf("failed to set read deadline: %w", err)
	}

	buf := make([]byte, 1024)
	n, err := s.Read(buf)
	if err != nil {
		return "", fmt.Errorf("failed to read from stream: %w", err)
	}

	input := string(buf[:n])
	p.logger.Debug("Worker received inference request", zap.Int("bytes", n), zap.String("input", input))
	return input, nil
}

func (p *Peer) callOllamaAPI(ctx context.Context, input, apiPath string) (string, error) {
	ollamaReq := OllamaRequest{
		Model: "tinyllama",
		Messages: []Message{
			{
				Role:    "user",
				Content: input,
			},
		},
		Stream: false,
	}

	reqBody, err := json.Marshal(ollamaReq)
	if err != nil {
		return "", fmt.Errorf("failed to marshal Ollama request: %w", err)
	}

	baseURL := p.Config.GetOllamaBaseURL()
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}

	fullURL := baseURL + apiPath
	req, err := http.NewRequestWithContext(ctx, "POST", fullURL, bytes.NewBuffer(reqBody))
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to make HTTP request to Ollama: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			p.logger.Debug("Failed to close response body", zap.Error(closeErr))
		}
	}()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read Ollama response: %w", err)
	}

	var ollamaResp OllamaResponse
	if err := json.Unmarshal(respBody, &ollamaResp); err != nil {
		return "", fmt.Errorf("failed to unmarshal Ollama response: %w", err)
	}

	output := ollamaResp.Message.Content
	if output == "" {
		output = "No response content received from Ollama"
	}

	return output, nil
}

func (p *Peer) writeInferenceResponse(s network.Stream, output string) error {
	responseBytes := []byte(output)
	p.logger.Debug("Worker writing response", zap.Int("bytes", len(responseBytes)), zap.String("output", output))

	_, err := s.Write(responseBytes)
	if err != nil {
		return fmt.Errorf("failed to write response: %w", err)
	}

	return nil
}

// SetupMetadataHandler sets up the metadata request handler
func (p *Peer) SetupMetadataHandler() {
	p.logger.Debug("Setting up metadata handler for protocol", zap.String("protocol", crowdllama.MetadataProtocol))

	p.Host.SetStreamHandler(crowdllama.MetadataProtocol, func(s network.Stream) {
		defer func() {
			if err := s.Close(); err != nil {
				p.logger.Debug("Failed to close stream", zap.Error(err))
			}
		}()
		p.logger.Debug("Peer received metadata request", zap.String("peer", s.Conn().RemotePeer().String()))

		// Serialize metadata to JSON
		metadataJSON, err := p.Metadata.ToJSON()
		if err != nil {
			p.logger.Debug("Failed to serialize metadata", zap.Error(err))
			return
		}

		p.logger.Debug("Peer sending metadata", zap.Int("bytes", len(metadataJSON)), zap.String("metadata", string(metadataJSON)))

		// Send metadata response
		_, err = s.Write(metadataJSON)
		if err != nil {
			p.logger.Debug("Failed to send metadata", zap.Error(err))
			return
		}

		// Close the stream after writing to signal EOF
		p.logger.Debug("Peer sent metadata successfully, closing stream")
	})

	p.logger.Debug("Metadata handler setup complete")
}

// UpdateMetadata updates the peer's internal metadata
func (p *Peer) UpdateMetadata() error {
	if p.WorkerMode {
		// Worker mode: use hardcoded values as requested
		models := []string{"llama-2-7b", "llama-2-13b", "mistral-7b", "tinyllama"}
		tokensThroughput := 150.0 // tokens/sec
		vramGB := 24              // VRAM GB
		load := 0.3               // current load (0.0 to 1.0)
		gpuModel := "RTX 4090"

		// Update the metadata
		p.Metadata.SupportedModels = models
		p.Metadata.TokensThroughput = tokensThroughput
		p.Metadata.VRAMGB = vramGB
		p.Metadata.Load = load
		p.Metadata.GPUModel = gpuModel
		p.Metadata.LastUpdated = time.Now()
		p.Metadata.Version = version.CommitHash // Set the CrowdLlama version

		p.logger.Debug("Updated worker peer metadata", zap.Strings("models", models), zap.Float64("throughput", tokensThroughput), zap.Int("vram", vramGB), zap.Float64("load", load), zap.String("gpu", gpuModel), zap.String("version", p.Metadata.Version))
	} else {
		// Consumer mode: empty resource advertisement
		p.Metadata.SupportedModels = []string{}
		p.Metadata.TokensThroughput = 0.0
		p.Metadata.VRAMGB = 0
		p.Metadata.Load = 0.0
		p.Metadata.GPUModel = ""
		p.Metadata.LastUpdated = time.Now()
		p.Metadata.Version = version.CommitHash

		p.logger.Debug("Updated consumer peer metadata", zap.String("version", p.Metadata.Version))
	}

	return nil
}

// StartMetadataUpdates starts periodic metadata updates
func (p *Peer) StartMetadataUpdates() {
	go func() {
		// Use shorter interval for testing environments
		updateInterval := MetadataUpdateInterval
		if os.Getenv("CROWDLLAMA_TEST_MODE") == "1" {
			updateInterval = 5 * time.Second
		}

		ticker := time.NewTicker(updateInterval)
		defer ticker.Stop()

		// Run initial update immediately
		if err := p.UpdateMetadata(); err != nil {
			p.logger.Debug("Failed to perform initial metadata update", zap.Error(err))
		}

		for {
			select {
			case <-ticker.C:
				if err := p.UpdateMetadata(); err != nil {
					p.logger.Debug("Failed to update metadata", zap.Error(err))
				}
			case <-p.metadataCtx.Done():
				p.logger.Debug("Metadata update loop stopped")
				return
			}
		}
	}()
}

// StopMetadataUpdates stops the periodic metadata updates
func (p *Peer) StopMetadataUpdates() {
	if p.metadataCancel != nil {
		p.metadataCancel()
	}
	// Stop advertising to the DHT
	p.stopAdvertising()
	// Remove metadata handler
	p.removeMetadataHandler()
}

// removeMetadataHandler removes the metadata protocol handler
func (p *Peer) removeMetadataHandler() {
	p.Host.RemoveStreamHandler(crowdllama.MetadataProtocol)
	p.logger.Debug("Removed metadata handler for protocol", zap.String("protocol", crowdllama.MetadataProtocol))
}

// PublishMetadata publishes the peer's metadata to the DHT
func (p *Peer) PublishMetadata(ctx context.Context) error {
	// Check if DHT is connected before attempting to publish
	if !p.IsDHTConnected() {
		p.logger.Debug("DHT is disconnected, attempting to reconnect to bootstrap peers...")
		if err := p.AttemptBootstrapReconnection(ctx); err != nil {
			return fmt.Errorf("failed to reconnect to bootstrap peers: %w", err)
		}

		// Give the DHT a moment to establish connections
		time.Sleep(2 * time.Second)

		// Check again if reconnection was successful
		if !p.IsDHTConnected() {
			return fmt.Errorf("DHT is still disconnected after reconnection attempt")
		}
	}

	data, err := p.Metadata.ToJSON()
	if err != nil {
		return fmt.Errorf("failed to serialize metadata: %w", err)
	}

	// Create a CID from the metadata
	mh, err := multihash.Sum(data, multihash.SHA2_256, -1)
	if err != nil {
		return fmt.Errorf("failed to create multihash: %w", err)
	}

	c := cid.NewCidV1(cid.Raw, mh)

	// Use Provide instead of PutValue
	err = p.DHT.Provide(ctx, c, true)
	if err != nil {
		return fmt.Errorf("failed to provide metadata to DHT: %w", err)
	}

	p.logger.Debug("Published metadata to DHT", zap.String("cid", c.String()))
	return nil
}

// AdvertisePeer periodically announces peer availability and advertises the peer using Provide
func (p *Peer) AdvertisePeer(_ context.Context, namespace string) {
	go func() {
		// Use shorter interval for testing environments
		advertiseInterval := 1 * time.Second
		if os.Getenv("CROWDLLAMA_TEST_MODE") == "1" {
			advertiseInterval = 500 * time.Millisecond
		}

		ticker := time.NewTicker(advertiseInterval)
		defer ticker.Stop()

		// Get the namespace CID using the provided namespace parameter
		mh, err := multihash.Sum([]byte(namespace), multihash.IDENTITY, -1)
		if err != nil {
			panic("Failed to create multihash for namespace: " + err.Error())
		}
		namespaceCID := cid.NewCidV1(cid.Raw, mh)

		p.logger.Debug("Peer advertising with namespace", zap.String("namespace", namespace), zap.String("cid", namespaceCID.String()))

		for {
			select {
			case <-ticker.C:
				// Check if DHT is connected before attempting to advertise
				if !p.IsDHTConnected() {
					p.logger.Debug("DHT is disconnected, attempting to reconnect to bootstrap peers...")
					if err := p.AttemptBootstrapReconnection(p.advertisingCtx); err != nil {
						p.logger.Debug("Failed to reconnect to bootstrap peers", zap.Error(err))
						continue // Skip this advertising cycle and try again next time
					}

					// Give the DHT a moment to establish connections
					time.Sleep(2 * time.Second)

					// Check again if reconnection was successful
					if !p.IsDHTConnected() {
						p.logger.Debug("DHT is still disconnected after reconnection attempt, skipping advertisement")
						continue // Skip this advertising cycle and try again next time
					}
				}

				// Advertise the peer using Provide
				err := p.DHT.Provide(p.advertisingCtx, namespaceCID, true)
				if err != nil {
					p.logger.Debug("Failed to advertise peer", zap.Error(err))
				} else {
					p.logger.Debug("Peer advertised with CID", zap.String("cid", namespaceCID.String()))
				}
			case <-p.advertisingCtx.Done():
				p.logger.Debug("Peer advertising stopped")
				return
			}
		}
	}()
}

// stopAdvertising stops the advertising process
func (p *Peer) stopAdvertising() {
	if p.advertisingCancel != nil {
		p.advertisingCancel()
	}
}

// IsDHTConnected checks if the DHT has any connected peers
func (p *Peer) IsDHTConnected() bool {
	peers := p.DHT.RoutingTable().ListPeers()
	return len(peers) > 0
}

// AttemptBootstrapReconnection attempts to reconnect to bootstrap peers
func (p *Peer) AttemptBootstrapReconnection(ctx context.Context) error {
	if len(p.bootstrapPeers) > 0 {
		return discovery.BootstrapDHTWithPeers(ctx, p.Host, p.DHT, p.bootstrapPeers, p.logger)
	}
	return discovery.BootstrapDHT(ctx, p.Host, p.DHT, p.logger)
}
