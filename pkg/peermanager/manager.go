// Package peermanager provides peer discovery, advertising, and management functionality.
package peermanager

import (
	"context"
	"fmt"
	"sync"
	"time"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"

	"github.com/crowdllama/crowdllama/internal/discovery"
	"github.com/crowdllama/crowdllama/pkg/crowdllama"
)

// I defines the interface for peer management functionality
type I interface {
	GetHealthyPeers() map[string]*PeerInfo
	GetAllPeers() map[string]*PeerInfo
	GetAvailablePeers() map[string]*crowdllama.Resource
	GetAvailableWorkers() map[string]*crowdllama.Resource
	GetAvailableConsumers() map[string]*crowdllama.Resource
	FindBestWorker(requiredModel string) *crowdllama.Resource
	AddOrUpdatePeer(peerID string, metadata *crowdllama.Resource)
	RemovePeer(peerID string)
	IsPeerUnhealthy(peerID string) bool
	MarkPeerAsRecentlyRemoved(peerID string)
	Start()
	Stop()
	GetPeerStatistics() PeerStatistics
}

// Manager handles peer discovery, advertising, and management
type Manager struct {
	host   host.Host
	dht    *dht.IpfsDHT
	logger *zap.Logger
	ctx    context.Context
	cancel context.CancelFunc

	// Peer health management
	peers           map[string]*PeerInfo
	recentlyRemoved map[string]time.Time
	peerMu          sync.RWMutex

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
	PeerHealthConfig       *PeerHealthConfig
}

// PeerHealthConfig holds peer health management settings
type PeerHealthConfig struct {
	StalePeerTimeout    time.Duration
	HealthCheckInterval time.Duration
	MaxFailedAttempts   int
	BackoffBase         time.Duration
	MetadataTimeout     time.Duration
	MaxMetadataAge      time.Duration
}

// DefaultPeerHealthConfig returns the default peer health config
func DefaultPeerHealthConfig() *PeerHealthConfig {
	return &PeerHealthConfig{
		StalePeerTimeout:    1 * time.Minute,
		HealthCheckInterval: 20 * time.Second,
		MaxFailedAttempts:   3,
		BackoffBase:         10 * time.Second,
		MetadataTimeout:     5 * time.Second,
		MaxMetadataAge:      1 * time.Minute,
	}
}

// DefaultConfig returns default configuration
func DefaultConfig() *Config {
	return &Config{
		DiscoveryInterval:      10 * time.Second,
		AdvertisingInterval:    30 * time.Second,
		MetadataUpdateInterval: 30 * time.Second,
		PeerHealthConfig:       DefaultPeerHealthConfig(),
	}
}

// PeerInfo holds health and metadata for a peer
type PeerInfo struct {
	PeerID          string
	Metadata        *crowdllama.Resource
	LastSeen        time.Time
	LastHealthCheck time.Time
	FailedAttempts  int
	LastFailure     time.Time
	IsHealthy       bool
	LastMetadataAge time.Duration
}

// NewManager creates a new peer manager
func NewManager(
	ctx context.Context,
	hostInstance host.Host,
	dhtInstance *dht.IpfsDHT,
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

	return &Manager{
		host:              hostInstance,
		dht:               dhtInstance,
		logger:            logger,
		ctx:               ctx,
		cancel:            nil, // Will be set by Start()
		peers:             make(map[string]*PeerInfo),
		recentlyRemoved:   make(map[string]time.Time),
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

	// Start background operations
	go pm.startDiscovery()
	go pm.startMetadataUpdates()
	go pm.healthCheckLoop()
	go pm.cleanupLoop()
}

// Stop stops the peer manager
func (pm *Manager) Stop() {
	pm.logger.Info("Stopping peer manager")

	// Cancel all contexts
	pm.discoveryCancel()
	pm.advertisingCancel()
	pm.metadataCancel()

	if pm.cancel != nil {
		pm.cancel()
	}
}

// AddOrUpdatePeer adds a new peer or updates an existing peer's metadata
func (pm *Manager) AddOrUpdatePeer(peerID string, metadata *crowdllama.Resource) {
	pm.peerMu.Lock()
	defer pm.peerMu.Unlock()
	now := time.Now()
	if info, exists := pm.peers[peerID]; exists {
		info.Metadata = metadata
		info.LastSeen = now
		info.IsHealthy = true
		info.FailedAttempts = 0
		info.LastMetadataAge = time.Since(metadata.LastUpdated)
		pm.logger.Debug("Updated existing peer",
			zap.String("peer_id", peerID),
			zap.Bool("worker_mode", metadata.WorkerMode),
			zap.String("gpu_model", metadata.GPUModel))
	} else {
		pm.peers[peerID] = &PeerInfo{
			PeerID:          peerID,
			Metadata:        metadata,
			LastSeen:        now,
			LastHealthCheck: now,
			FailedAttempts:  0,
			IsHealthy:       true,
			LastMetadataAge: time.Since(metadata.LastUpdated),
		}
		pm.logger.Info("Added new peer to manager",
			zap.String("peer_id", peerID),
			zap.Bool("worker_mode", metadata.WorkerMode),
			zap.String("gpu_model", metadata.GPUModel),
			zap.Int("total_peers", len(pm.peers)))
	}
}

// RemovePeer removes a peer from the manager and marks it as recently removed
func (pm *Manager) RemovePeer(peerID string) {
	pm.peerMu.Lock()
	defer pm.peerMu.Unlock()
	delete(pm.peers, peerID)
	// Add to recently removed list to prevent re-adding
	pm.recentlyRemoved[peerID] = time.Now()
	pm.logger.Info("Removed peer from manager", zap.String("peer_id", peerID))
}

// MarkPeerAsRecentlyRemoved marks a peer as recently removed without removing it from the peers map
// This is useful for peers that fail to connect during discovery
func (pm *Manager) MarkPeerAsRecentlyRemoved(peerID string) {
	pm.peerMu.Lock()
	defer pm.peerMu.Unlock()
	pm.recentlyRemoved[peerID] = time.Now()
	pm.logger.Debug("Marked peer as recently removed", zap.String("peer_id", peerID))
}

// GetHealthyPeers returns all peers that are currently marked as healthy
func (pm *Manager) GetHealthyPeers() map[string]*PeerInfo {
	pm.peerMu.RLock()
	defer pm.peerMu.RUnlock()
	healthy := make(map[string]*PeerInfo)
	for peerID, info := range pm.peers {
		if info.IsHealthy {
			healthy[peerID] = info
		}
	}
	return healthy
}

// GetAllPeers returns all peers in the manager regardless of health status
func (pm *Manager) GetAllPeers() map[string]*PeerInfo {
	pm.peerMu.RLock()
	defer pm.peerMu.RUnlock()
	result := make(map[string]*PeerInfo)
	for peerID, info := range pm.peers {
		result[peerID] = info
	}
	return result
}

// IsPeerUnhealthy checks if a peer is marked as unhealthy or was recently removed
func (pm *Manager) IsPeerUnhealthy(peerID string) bool {
	pm.peerMu.RLock()
	defer pm.peerMu.RUnlock()

	// Check if peer is currently in the manager and unhealthy
	if info, exists := pm.peers[peerID]; exists {
		return !info.IsHealthy || info.FailedAttempts >= pm.config.PeerHealthConfig.MaxFailedAttempts
	}

	// Check if peer was recently removed (within the last 10 minutes)
	if removedTime, wasRemoved := pm.recentlyRemoved[peerID]; wasRemoved {
		if time.Since(removedTime) < 10*time.Minute {
			return true // Consider recently removed peers as unhealthy
		}
		// Clean up old entries
		delete(pm.recentlyRemoved, peerID)
	}

	return false
}

// GetAvailablePeers returns all available peers with their details
func (pm *Manager) GetAvailablePeers() map[string]*crowdllama.Resource {
	result := make(map[string]*crowdllama.Resource)
	for peerID, info := range pm.GetHealthyPeers() {
		if info.Metadata != nil {
			result[peerID] = info.Metadata
		}
	}
	return result
}

// GetAvailableWorkers returns only worker peers
func (pm *Manager) GetAvailableWorkers() map[string]*crowdllama.Resource {
	result := make(map[string]*crowdllama.Resource)
	for peerID, info := range pm.GetHealthyPeers() {
		if info.Metadata != nil && info.Metadata.WorkerMode {
			result[peerID] = info.Metadata
		}
	}
	return result
}

// GetAvailableConsumers returns only consumer peers
func (pm *Manager) GetAvailableConsumers() map[string]*crowdllama.Resource {
	result := make(map[string]*crowdllama.Resource)
	for peerID, info := range pm.GetHealthyPeers() {
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
				discovery.AdvertiseModel(pm.advertisingCtx, pm.dht, namespace, pm.logger)
			case <-pm.advertisingCtx.Done():
				return
			}
		}
	}()
}

// StopAdvertising stops advertising this peer
func (pm *Manager) StopAdvertising() {
	pm.advertisingCancel()
}

// UpdateMetadata updates the metadata for this peer
func (pm *Manager) UpdateMetadata(metadata *crowdllama.Resource) error {
	if metadata == nil {
		return fmt.Errorf("metadata cannot be nil")
	}

	// Validate metadata
	if err := pm.ValidateMetadata(metadata); err != nil {
		return fmt.Errorf("invalid metadata: %w", err)
	}

	// Update metadata
	metadata.LastUpdated = time.Now()
	pm.logger.Debug("Updated peer metadata", zap.String("peer_id", metadata.PeerID))

	return nil
}

// ValidateMetadata validates peer metadata
func (pm *Manager) ValidateMetadata(metadata *crowdllama.Resource) error {
	if metadata.PeerID == "" {
		return fmt.Errorf("peer ID cannot be empty")
	}
	return nil
}

// startDiscovery starts the peer discovery process
func (pm *Manager) startDiscovery() {
	pm.logger.Info("Starting peer discovery", zap.Duration("interval", pm.config.DiscoveryInterval))

	go func() {
		ticker := time.NewTicker(pm.config.DiscoveryInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				pm.runDiscovery()
			case <-pm.discoveryCtx.Done():
				return
			}
		}
	}()
}

// runDiscovery performs a single discovery round
func (pm *Manager) runDiscovery() {
	pm.logger.Debug("Running peer discovery")

	// Discover peers using the discovery package
	discoveredPeers, err := discovery.DiscoverPeers(pm.discoveryCtx, pm.dht, pm.logger, pm)
	if err != nil {
		pm.logger.Error("Failed to discover peers", zap.Error(err))
		return
	}

	// Process discovered peers
	for _, peer := range discoveredPeers {
		// Skip if peer is already known and healthy
		if !pm.IsPeerUnhealthy(peer.PeerID) {
			// Add or update peer in manager
			pm.AddOrUpdatePeer(peer.PeerID, peer)
			pm.logger.Info("Discovered new peer",
				zap.String("peer_id", peer.PeerID),
				zap.Bool("worker_mode", peer.WorkerMode))
		}
	}
}

// startMetadataUpdates starts periodic metadata updates
func (pm *Manager) startMetadataUpdates() {
	pm.logger.Info("Starting metadata updates", zap.Duration("interval", pm.config.MetadataUpdateInterval))

	go func() {
		ticker := time.NewTicker(pm.config.MetadataUpdateInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				// Update metadata for all known peers
				for _, info := range pm.GetAllPeers() {
					if info.Metadata != nil {
						info.Metadata.LastUpdated = time.Now()
						info.LastMetadataAge = time.Since(info.Metadata.LastUpdated)
					}
				}
			case <-pm.metadataCtx.Done():
				return
			}
		}
	}()
}

// healthCheckLoop runs periodic health checks on peers
func (pm *Manager) healthCheckLoop() {
	ticker := time.NewTicker(pm.config.PeerHealthConfig.HealthCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			pm.performHealthChecks()
		case <-pm.ctx.Done():
			return
		}
	}
}

// cleanupLoop runs periodic cleanup of stale peers
func (pm *Manager) cleanupLoop() {
	ticker := time.NewTicker(pm.config.PeerHealthConfig.HealthCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			pm.performCleanup()
		case <-pm.ctx.Done():
			return
		}
	}
}

// performHealthChecks performs health checks on all peers
func (pm *Manager) performHealthChecks() {
	pm.peerMu.Lock()
	defer pm.peerMu.Unlock()
	now := time.Now()
	for peerID, info := range pm.peers {
		if now.Sub(info.LastHealthCheck) < pm.config.PeerHealthConfig.HealthCheckInterval {
			continue
		}
		if info.FailedAttempts > 0 {
			backoffDuration := time.Duration(info.FailedAttempts) * pm.config.PeerHealthConfig.BackoffBase
			if now.Sub(info.LastFailure) < backoffDuration {
				continue
			}
		}
		info.LastHealthCheck = now
		isHealthy := pm.healthCheckPeer(peerID)
		if isHealthy {
			if !info.IsHealthy {
				pm.logger.Info("Peer recovered from unhealthy state", zap.String("peer_id", peerID))
			}
			info.IsHealthy = true
			info.FailedAttempts = 0
		} else {
			info.FailedAttempts++
			info.LastFailure = now
			info.IsHealthy = false
			pm.logger.Warn("Peer health check failed", zap.String("peer_id", peerID), zap.Int("failed_attempts", info.FailedAttempts))
		}
	}
}

// performCleanup removes stale peers
func (pm *Manager) performCleanup() {
	pm.peerMu.Lock()
	defer pm.peerMu.Unlock()
	now := time.Now()
	staleTimeout := pm.config.PeerHealthConfig.StalePeerTimeout

	// Remove stale peers
	for peerID, info := range pm.peers {
		if now.Sub(info.LastSeen) > staleTimeout {
			delete(pm.peers, peerID)
			pm.recentlyRemoved[peerID] = now
			pm.logger.Info("Removed stale peer", zap.String("peer_id", peerID))
		}
	}

	// Clean up old recently removed entries
	for peerID, removedTime := range pm.recentlyRemoved {
		if now.Sub(removedTime) > 10*time.Minute {
			delete(pm.recentlyRemoved, peerID)
		}
	}
}

// healthCheckPeer performs a health check on a specific peer
func (pm *Manager) healthCheckPeer(peerID string) bool {
	// Convert string to peer.ID
	peerIDObj, err := peer.Decode(peerID)
	if err != nil {
		pm.logger.Error("Invalid peer ID for health check", zap.String("peer_id", peerID), zap.Error(err))
		return false
	}

	// Check if peer is connected
	if pm.host.Network().Connectedness(peerIDObj) != network.Connected {
		return false
	}

	// Try to get metadata as a health check
	ctx, cancel := context.WithTimeout(pm.ctx, pm.config.PeerHealthConfig.MetadataTimeout)
	defer cancel()

	metadata, err := discovery.RequestPeerMetadata(ctx, pm.host, peerIDObj, pm.logger)
	if err != nil {
		return false
	}

	// Update peer info with fresh metadata
	if info, exists := pm.peers[peerID]; exists {
		info.Metadata = metadata
		info.LastSeen = time.Now()
		info.LastMetadataAge = time.Since(metadata.LastUpdated)
	}

	return true
}

// GetHost returns the libp2p host
func (pm *Manager) GetHost() host.Host {
	return pm.host
}

// GetDHT returns the DHT instance
func (pm *Manager) GetDHT() *dht.IpfsDHT {
	return pm.dht
}

// GetLogger returns the logger
func (pm *Manager) GetLogger() *zap.Logger {
	return pm.logger
}
