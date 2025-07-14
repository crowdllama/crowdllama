// Package peer provides unified peer functionality for CrowdLlama.
package peer

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ipfs/go-cid"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/multiformats/go-multihash"
	"google.golang.org/protobuf/proto"

	"go.uber.org/zap"

	llamav1 "github.com/crowdllama/crowdllama-pb/llama/v1"
	"github.com/crowdllama/crowdllama/internal/discovery"
	"github.com/crowdllama/crowdllama/pkg/config"
	"github.com/crowdllama/crowdllama/pkg/crowdllama"
	"github.com/crowdllama/crowdllama/pkg/peermanager"
	"github.com/crowdllama/crowdllama/pkg/version"
)

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

// Peer represents a CrowdLlama peer node (can be either worker or consumer)
type Peer struct {
	Host       host.Host
	DHT        *dht.IpfsDHT
	Metadata   *crowdllama.Resource
	Config     *config.Configuration
	WorkerMode bool // true if this peer is in worker mode

	// API handler for processing inference requests
	APIHandler crowdllama.UnifiedAPIHandler

	// Peer management
	PeerManager peermanager.I

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

	if err := bootstrapDHT(ctx, h, kadDHT, cfg, logger); err != nil {
		return nil, err
	}

	logger.Debug("BootstrapDHT completed successfully")

	peer := createPeerInstance(ctx, h, kadDHT, cfg, workerMode, logger)
	setupStreamHandler(ctx, peer)

	return peer, nil
}

func bootstrapDHT(ctx context.Context, h host.Host, kadDHT *dht.IpfsDHT, cfg *config.Configuration, logger *zap.Logger) error {
	// Bootstrap with custom peers if provided, otherwise use defaults
	if len(cfg.BootstrapPeers) > 0 {
		if err := discovery.BootstrapDHTWithPeers(ctx, h, kadDHT, cfg.BootstrapPeers, logger); err != nil {
			return fmt.Errorf("bootstrap DHT with custom peers: %w", err)
		}
	} else {
		if err := discovery.BootstrapDHT(ctx, h, kadDHT, logger); err != nil {
			return fmt.Errorf("bootstrap DHT: %w", err)
		}
	}
	return nil
}

func createPeerInstance(
	ctx context.Context,
	h host.Host,
	kadDHT *dht.IpfsDHT,
	cfg *config.Configuration,
	workerMode bool,
	logger *zap.Logger,
) *Peer {
	// Initialize metadata
	metadata := crowdllama.NewCrowdLlamaResource(h.ID().String())
	metadata.WorkerMode = workerMode

	// Create metadata context for managing metadata updates
	metadataCtx, metadataCancel := context.WithCancel(ctx)

	// Create advertising context for managing advertising
	advertisingCtx, advertisingCancel := context.WithCancel(ctx)

	// Initialize peer manager
	peerManagerConfig := getPeerManagerConfig()

	// Set up API handler based on mode
	var apiHandler crowdllama.UnifiedAPIHandler
	if workerMode {
		apiHandler = crowdllama.WorkerAPIHandler(cfg.GetOllamaBaseURL())
	} else {
		apiHandler = crowdllama.DefaultAPIHandler
	}

	peer := &Peer{
		Host:              h,
		DHT:               kadDHT,
		Metadata:          metadata,
		Config:            cfg,
		WorkerMode:        workerMode,
		APIHandler:        apiHandler,
		PeerManager:       peermanager.NewManager(ctx, h, kadDHT, logger, peerManagerConfig),
		metadataCtx:       metadataCtx,
		metadataCancel:    metadataCancel,
		advertisingCtx:    advertisingCtx,
		advertisingCancel: advertisingCancel,
		bootstrapPeers:    cfg.BootstrapPeers,
		logger:            logger,
	}

	return peer
}

func getPeerManagerConfig() *peermanager.Config {
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
	return peerManagerConfig
}

func setupStreamHandler(ctx context.Context, peer *Peer) {
	// Set up stream handler with the peer instance
	peer.Host.SetStreamHandler(crowdllama.InferenceProtocol, func(s network.Stream) {
		peer.handleInferenceRequest(ctx, s)
	})
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

	// Read PB message from stream
	req, err := p.readPBMessage(s)
	if err != nil {
		p.logger.Debug("Failed to read PB message", zap.Error(err))
		return
	}

	// Log the inference request details
	if generateReq := req.GetGenerateRequest(); generateReq != nil {
		p.logger.Debug("Worker received inference request from network",
			zap.String("model", generateReq.Model),
			zap.String("prompt", generateReq.Prompt),
			zap.Bool("stream", generateReq.Stream),
			zap.String("remote_peer", s.Conn().RemotePeer().String()))
		p.logger.Info("Worker received generate request",
			zap.String("model", generateReq.Model),
			zap.String("prompt", generateReq.Prompt),
			zap.Bool("stream", generateReq.Stream),
			zap.String("remote_peer", s.Conn().RemotePeer().String()))
	}

	// Add logger to context for API handler
	type loggerKey struct{}
	ctxWithLogger := context.WithValue(ctx, loggerKey{}, p.logger)

	// Process the request using the API handler
	resp, err := p.APIHandler(ctxWithLogger, req)
	if err != nil {
		p.logger.Error("Failed to process inference request", zap.Error(err))
		// Create error response
		resp = &llamav1.BaseMessage{
			Message: &llamav1.BaseMessage_GenerateResponse{
				GenerateResponse: &llamav1.GenerateResponse{
					Response: fmt.Sprintf("Error: %v", err),
					Done:     true,
				},
			},
		}
	}

	// Write PB response to stream
	p.logger.Debug("Worker sending inference response to network",
		zap.String("remote_peer", s.Conn().RemotePeer().String()))
	if err := p.writePBMessage(s, resp); err != nil {
		p.logger.Debug("Failed to write PB response", zap.Error(err))
		return
	}

	p.logger.Info("Worker completed inference request",
		zap.String("remote_peer", s.Conn().RemotePeer().String()))
	p.logger.Debug("StreamHandler completed")
}

// readPBMessage reads a length-prefixed protobuf message from a network stream
func (p *Peer) readPBMessage(s network.Stream) (*llamav1.BaseMessage, error) {
	if err := s.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return nil, fmt.Errorf("failed to set read deadline: %w", err)
	}

	msg, err := crowdllama.ReadLengthPrefixedPB(s)
	if err != nil {
		return nil, fmt.Errorf("failed to read length-prefixed PB message: %w", err)
	}

	p.logger.Debug("Worker received PB request", zap.Int("bytes", proto.Size(msg)))
	return msg, nil
}

// writePBMessage writes a length-prefixed protobuf message to a network stream
func (p *Peer) writePBMessage(s network.Stream, msg *llamav1.BaseMessage) error {
	if err := crowdllama.WriteLengthPrefixedPB(s, msg); err != nil {
		return fmt.Errorf("failed to write length-prefixed PB message: %w", err)
	}

	p.logger.Debug("Worker sent PB response", zap.Int("bytes", proto.Size(msg)))
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

		p.logger.Debug("Updated worker peer metadata",
			zap.Strings("models", models),
			zap.Float64("throughput", tokensThroughput),
			zap.Int("vram", vramGB),
			zap.Float64("load", load),
			zap.String("gpu", gpuModel),
			zap.String("version", p.Metadata.Version))
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
		return fmt.Errorf("bootstrap DHT with peers: %w", discovery.BootstrapDHTWithPeers(ctx, p.Host, p.DHT, p.bootstrapPeers, p.logger))
	}
	return fmt.Errorf("bootstrap DHT: %w", discovery.BootstrapDHT(ctx, p.Host, p.DHT, p.logger))
}
