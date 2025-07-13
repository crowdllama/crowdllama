// Package peermanager provides peer discovery, advertising, and management functionality.
package peermanager

import (
	"context"
	"fmt"
	"os"
	"time"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"go.uber.org/zap"

	"github.com/crowdllama/crowdllama/internal/discovery"
	"github.com/crowdllama/crowdllama/internal/peermanager"
	"github.com/crowdllama/crowdllama/pkg/crowdllama"
)

// Manager handles peer discovery, advertising, and management
type Manager struct {
	host   host.Host
	dht    *dht.IpfsDHT
	logger *zap.Logger
	ctx    context.Context
	cancel context.CancelFunc

	// Peer management
	peerManager *peermanager.Manager

	// Discovery management
	discoveryCtx    context.Context
	discoveryCancel context.CancelFunc

	// Advertising management
	advertisingCtx    context.Context
	advertisingCancel context.CancelFunc

	// Metadata management
	metadataCtx    context.Context
	metadataCancel context.CancelFunc

	// Configuration
	config *Config
}

// Config holds configuration for the peer manager
type Config struct {
	DiscoveryInterval      time.Duration
	AdvertisingInterval    time.Duration
	MetadataUpdateInterval time.Duration
	PeerManagerConfig      *peermanager.Config
}

// DefaultConfig returns default configuration
func DefaultConfig() *Config {
	return &Config{
		DiscoveryInterval:      10 * time.Second,
		AdvertisingInterval:    30 * time.Second,
		MetadataUpdateInterval: 30 * time.Second,
		PeerManagerConfig:      peermanager.DefaultConfig(),
	}
}

// NewManager creates a new peer manager
func NewManager(
	ctx context.Context,
	host host.Host,
	dht *dht.IpfsDHT,
	logger *zap.Logger,
	config *Config,
) *Manager {
	if config == nil {
		config = DefaultConfig()
	}

	// Create contexts for different operations
	discoveryCtx, discoveryCancel := context.WithCancel(ctx)
	advertisingCtx, advertisingCancel := context.WithCancel(ctx)
	metadataCtx, metadataCancel := context.WithCancel(ctx)

	// Initialize the underlying peer manager
	peerManager := peermanager.NewManager(ctx, host, logger, config.PeerManagerConfig)

	return &Manager{
		host:              host,
		dht:               dht,
		logger:            logger,
		ctx:               ctx,
		cancel:            nil, // Will be set by Start()
		peerManager:       peerManager,
		discoveryCtx:      discoveryCtx,
		discoveryCancel:   discoveryCancel,
		advertisingCtx:    advertisingCtx,
		advertisingCancel: advertisingCancel,
		metadataCtx:       metadataCtx,
		metadataCancel:    metadataCancel,
		config:            config,
	}
}

// Start starts the peer manager
func (pm *Manager) Start() {
	pm.logger.Info("Starting peer manager")

	// Start the underlying peer manager
	pm.peerManager.Start()

	// Start background operations
	go pm.startDiscovery()
	go pm.startMetadataUpdates()
}

// Stop stops the peer manager
func (pm *Manager) Stop() {
	pm.logger.Info("Stopping peer manager")

	// Cancel all contexts
	pm.discoveryCancel()
	pm.advertisingCancel()
	pm.metadataCancel()

	// Stop the underlying peer manager
	pm.peerManager.Stop()
}

// GetAvailablePeers returns all available peers with their details
func (pm *Manager) GetAvailablePeers() map[string]*crowdllama.Resource {
	result := make(map[string]*crowdllama.Resource)
	for peerID, info := range pm.peerManager.GetHealthyPeers() {
		if info.Metadata != nil {
			result[peerID] = info.Metadata
		}
	}
	return result
}

// GetAvailableWorkers returns only worker peers
func (pm *Manager) GetAvailableWorkers() map[string]*crowdllama.Resource {
	result := make(map[string]*crowdllama.Resource)
	for peerID, info := range pm.peerManager.GetHealthyPeers() {
		if info.Metadata != nil && info.Metadata.WorkerMode {
			result[peerID] = info.Metadata
		}
	}
	return result
}

// GetAvailableConsumers returns only consumer peers
func (pm *Manager) GetAvailableConsumers() map[string]*crowdllama.Resource {
	result := make(map[string]*crowdllama.Resource)
	for peerID, info := range pm.peerManager.GetHealthyPeers() {
		if info.Metadata != nil && !info.Metadata.WorkerMode {
			result[peerID] = info.Metadata
		}
	}
	return result
}

// GetPeerStatistics returns statistics about available peers
func (pm *Manager) GetPeerStatistics() PeerStatistics {
	peers := pm.GetAvailablePeers()

	stats := PeerStatistics{
		TotalPeers:    len(peers),
		WorkerPeers:   0,
		ConsumerPeers: 0,
	}

	for _, peer := range peers {
		if peer.WorkerMode {
			stats.WorkerPeers++
		} else {
			stats.ConsumerPeers++
		}
	}

	return stats
}

// PeerStatistics holds statistics about peers
type PeerStatistics struct {
	TotalPeers    int `json:"total_peers"`
	WorkerPeers   int `json:"worker_peers"`
	ConsumerPeers int `json:"consumer_peers"`
}

// FindBestWorker finds the best available worker for a specific model
func (pm *Manager) FindBestWorker(requiredModel string) *crowdllama.Resource {
	workers := pm.GetAvailableWorkers()
	if len(workers) == 0 {
		return nil
	}

	// Filter workers that support the required model
	suitableWorkers := make([]*crowdllama.Resource, 0)
	for _, worker := range workers {
		// Check if the worker supports the required model
		supportsModel := false
		for _, model := range worker.SupportedModels {
			if model == requiredModel {
				supportsModel = true
				break
			}
		}

		if supportsModel {
			suitableWorkers = append(suitableWorkers, worker)
		}
	}

	if len(suitableWorkers) == 0 {
		return nil
	}

	// Select the best worker based on criteria (lowest load, highest throughput)
	var selectedWorker *crowdllama.Resource
	bestScore := float64(0)

	for _, worker := range suitableWorkers {
		// Simple scoring: tokens_throughput / (1 + current_load)
		// This favors workers with high throughput and low current load
		score := worker.TokensThroughput / (1 + worker.Load)
		if score > bestScore {
			bestScore = score
			selectedWorker = worker
		}
	}

	pm.logger.Info("Selected best worker",
		zap.String("worker_id", selectedWorker.PeerID),
		zap.String("gpu_model", selectedWorker.GPUModel),
		zap.Float64("tokens_throughput", selectedWorker.TokensThroughput),
		zap.Float64("current_load", selectedWorker.Load),
		zap.Int("total_suitable_workers", len(suitableWorkers)))

	return selectedWorker
}

// Advertise starts advertising this peer in the network
func (pm *Manager) Advertise(namespace string) {
	pm.logger.Info("Starting peer advertisement", zap.String("namespace", namespace))

	go func() {
		ticker := time.NewTicker(pm.config.AdvertisingInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				discovery.AdvertiseModel(pm.advertisingCtx, pm.dht, namespace)
			case <-pm.advertisingCtx.Done():
				pm.logger.Info("Peer advertisement stopped")
				return
			}
		}
	}()
}

// StopAdvertising stops advertising this peer
func (pm *Manager) StopAdvertising() {
	pm.advertisingCancel()
}

// UpdateMetadata updates the peer's metadata
func (pm *Manager) UpdateMetadata(metadata *crowdllama.Resource) error {
	if metadata == nil {
		return fmt.Errorf("metadata cannot be nil")
	}

	// Update the metadata in the peer manager
	pm.peerManager.AddOrUpdatePeer(pm.host.ID().String(), metadata)

	pm.logger.Debug("Updated peer metadata",
		zap.String("peer_id", metadata.PeerID),
		zap.Bool("worker_mode", metadata.WorkerMode),
		zap.String("gpu_model", metadata.GPUModel))

	return nil
}

// startDiscovery starts the background peer discovery process
func (pm *Manager) startDiscovery() {
	pm.logger.Info("Starting background peer discovery")

	// Use shorter interval for testing environments
	discoveryInterval := pm.config.DiscoveryInterval
	if os.Getenv("CROWDLLAMA_TEST_MODE") == "1" {
		discoveryInterval = 2 * time.Second
	}

	ticker := time.NewTicker(discoveryInterval)
	defer ticker.Stop()

	// Run initial discovery immediately
	pm.runDiscovery()

	for {
		select {
		case <-ticker.C:
			pm.runDiscovery()
		case <-pm.discoveryCtx.Done():
			pm.logger.Info("Background discovery stopped")
			return
		}
	}
}

// runDiscovery performs a single discovery run
func (pm *Manager) runDiscovery() {
	ctx, cancel := context.WithTimeout(pm.discoveryCtx, 10*time.Second)
	defer cancel()

	peers, err := discovery.DiscoverPeers(ctx, pm.dht, pm.logger, pm.peerManager)
	if err != nil {
		pm.logger.Warn("Background discovery failed", zap.Error(err))
		return
	}

	updatedCount := 0
	skippedCount := 0
	for _, peer := range peers {
		// Check if this peer is already marked as unhealthy or recently removed
		if pm.peerManager.IsPeerUnhealthy(peer.PeerID) {
			pm.logger.Debug("Skipping unhealthy peer",
				zap.String("peer_id", peer.PeerID))
			skippedCount++
			continue
		}

		// Additional check: skip peers with old metadata
		if time.Since(peer.LastUpdated) > pm.peerManager.GetConfig().MaxMetadataAge {
			pm.logger.Debug("Skipping peer with old metadata",
				zap.String("peer_id", peer.PeerID),
				zap.Time("last_updated", peer.LastUpdated))
			skippedCount++
			continue
		}

		pm.peerManager.AddOrUpdatePeer(peer.PeerID, peer)
		updatedCount++
	}

	if updatedCount > 0 || skippedCount > 0 {
		pm.logger.Info("Background discovery completed",
			zap.Int("updated_count", updatedCount),
			zap.Int("skipped_count", skippedCount),
			zap.Int("total_peers", len(pm.peerManager.GetAllPeers())))
	}
}

// startMetadataUpdates starts periodic metadata updates
func (pm *Manager) startMetadataUpdates() {
	pm.logger.Info("Starting metadata updates")

	ticker := time.NewTicker(pm.config.MetadataUpdateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// This will be implemented by the peer that owns this manager
			// The peer should call UpdateMetadata with its current metadata
		case <-pm.metadataCtx.Done():
			pm.logger.Info("Metadata updates stopped")
			return
		}
	}
}

// GetHost returns the underlying host
func (pm *Manager) GetHost() host.Host {
	return pm.host
}

// GetDHT returns the underlying DHT
func (pm *Manager) GetDHT() *dht.IpfsDHT {
	return pm.dht
}

// GetLogger returns the logger
func (pm *Manager) GetLogger() *zap.Logger {
	return pm.logger
}
