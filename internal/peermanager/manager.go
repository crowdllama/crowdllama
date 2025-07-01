// Package peermanager provides shared logic for peer health, backoff, and cleanup.
package peermanager

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"

	"github.com/matiasinsaurralde/crowdllama/pkg/crowdllama"
)

// Config holds peer management settings
//
// StalePeerTimeout: how long to keep a peer after last seen (default 1 minute)
// HealthCheckInterval: how often to check peer health
// MaxFailedAttempts: max failures before removal
// BackoffBase: base time for exponential backoff
// MetadataTimeout: timeout for metadata/health check
// MaxMetadataAge: max age for metadata to be considered fresh
type Config struct {
	StalePeerTimeout    time.Duration
	HealthCheckInterval time.Duration
	MaxFailedAttempts   int
	BackoffBase         time.Duration
	MetadataTimeout     time.Duration
	MaxMetadataAge      time.Duration
}

// DefaultConfig returns the default peer manager config
func DefaultConfig() *Config {
	return &Config{
		StalePeerTimeout:    1 * time.Minute,
		HealthCheckInterval: 20 * time.Second,
		MaxFailedAttempts:   3,
		BackoffBase:         10 * time.Second,
		MetadataTimeout:     5 * time.Second,
		MaxMetadataAge:      1 * time.Minute,
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

// Manager manages peer health and cleanup
//
// Use Start/Stop to run background health and cleanup loops.
type Manager struct {
	host   host.Host
	logger *zap.Logger
	config *Config
	peers  map[string]*PeerInfo
	// recentlyRemoved tracks peers that were recently removed to prevent re-adding them
	recentlyRemoved map[string]time.Time
	mu              sync.RWMutex
	ctx             context.Context
	cancel          context.CancelFunc
}

func NewManager(ctx context.Context, host host.Host, logger *zap.Logger, config *Config) *Manager {
	ctx, cancel := context.WithCancel(ctx)
	return &Manager{
		host:            host,
		logger:          logger,
		config:          config,
		peers:           make(map[string]*PeerInfo),
		recentlyRemoved: make(map[string]time.Time),
		ctx:             ctx,
		cancel:          cancel,
	}
}

func (pm *Manager) Start() {
	go pm.healthCheckLoop()
	go pm.cleanupLoop()
}

func (pm *Manager) Stop() {
	if pm.cancel != nil {
		pm.cancel()
	}
}

func (pm *Manager) AddOrUpdatePeer(peerID string, metadata *crowdllama.Resource) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	now := time.Now()
	if info, exists := pm.peers[peerID]; exists {
		info.Metadata = metadata
		info.LastSeen = now
		info.IsHealthy = true
		info.FailedAttempts = 0
		info.LastMetadataAge = time.Since(metadata.LastUpdated)
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
	}
}

func (pm *Manager) RemovePeer(peerID string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	delete(pm.peers, peerID)
	// Add to recently removed list to prevent re-adding
	pm.recentlyRemoved[peerID] = time.Now()
	pm.logger.Info("Removed peer from manager", zap.String("peer_id", peerID))
}

func (pm *Manager) GetHealthyPeers() map[string]*PeerInfo {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	healthy := make(map[string]*PeerInfo)
	for peerID, info := range pm.peers {
		if info.IsHealthy {
			healthy[peerID] = info
		}
	}
	return healthy
}

func (pm *Manager) GetAllPeers() map[string]*PeerInfo {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	result := make(map[string]*PeerInfo)
	for peerID, info := range pm.peers {
		result[peerID] = info
	}
	return result
}

// IsPeerUnhealthy checks if a peer is marked as unhealthy or was recently removed
func (pm *Manager) IsPeerUnhealthy(peerID string) bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	// Check if peer is currently in the manager and unhealthy
	if info, exists := pm.peers[peerID]; exists {
		return !info.IsHealthy || info.FailedAttempts >= pm.config.MaxFailedAttempts
	}

	// Check if peer was recently removed (within the last 5 minutes)
	if removedTime, wasRemoved := pm.recentlyRemoved[peerID]; wasRemoved {
		if time.Since(removedTime) < 5*time.Minute {
			return true // Consider recently removed peers as unhealthy
		}
		// Clean up old entries
		delete(pm.recentlyRemoved, peerID)
	}

	return false
}

func (pm *Manager) healthCheckLoop() {
	ticker := time.NewTicker(pm.config.HealthCheckInterval)
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

func (pm *Manager) cleanupLoop() {
	ticker := time.NewTicker(pm.config.HealthCheckInterval)
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

func (pm *Manager) performHealthChecks() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	now := time.Now()
	for peerID, info := range pm.peers {
		if now.Sub(info.LastHealthCheck) < pm.config.HealthCheckInterval {
			continue
		}
		if info.FailedAttempts > 0 {
			backoffDuration := time.Duration(info.FailedAttempts) * pm.config.BackoffBase
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
			if info.FailedAttempts >= pm.config.MaxFailedAttempts {
				pm.logger.Info("Marking peer for removal due to repeated failures", zap.String("peer_id", peerID), zap.Int("failed_attempts", info.FailedAttempts))
			}
		}
	}
}

func (pm *Manager) performCleanup() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	now := time.Now()
	removedCount := 0
	for peerID, info := range pm.peers {
		shouldRemove := false
		reason := ""
		if now.Sub(info.LastSeen) > pm.config.StalePeerTimeout {
			shouldRemove = true
			reason = "stale (not seen recently)"
		}
		if info.FailedAttempts >= pm.config.MaxFailedAttempts {
			shouldRemove = true
			reason = "repeated health check failures"
		}
		if info.Metadata != nil && time.Since(info.Metadata.LastUpdated) > pm.config.MaxMetadataAge {
			shouldRemove = true
			reason = "metadata too old"
		}
		if shouldRemove {
			delete(pm.peers, peerID)
			removedCount++
			pm.logger.Info("Removed peer", zap.String("peer_id", peerID), zap.String("reason", reason), zap.Int("failed_attempts", info.FailedAttempts))

		}
	}
	if removedCount > 0 {
		pm.logger.Info("Cleaned up peers", zap.Int("removed_count", removedCount), zap.Int("remaining_peers", len(pm.peers)))
	}
}

func (pm *Manager) healthCheckPeer(peerID string) bool {
	pid, err := peer.Decode(peerID)
	if err != nil {
		pm.logger.Debug("Invalid peer ID for health check", zap.String("peer_id", peerID), zap.Error(err))
		return false
	}
	healthCtx, cancel := context.WithTimeout(pm.ctx, pm.config.MetadataTimeout)
	defer cancel()
	stream, err := pm.host.NewStream(healthCtx, pid, crowdllama.MetadataProtocol)
	if err != nil {
		pm.logger.Debug("Health check failed - could not open stream", zap.String("peer_id", peerID), zap.Error(err))
		return false
	}
	defer func() {
		if closeErr := stream.Close(); closeErr != nil {
			pm.logger.Debug("Failed to close health check stream", zap.Error(closeErr))
		}
	}()
	if setDeadlineErr := stream.SetReadDeadline(time.Now().Add(pm.config.MetadataTimeout)); setDeadlineErr != nil {
		pm.logger.Debug("Failed to set read deadline for health check", zap.Error(setDeadlineErr))
	}
	buf := make([]byte, 1)
	_, readErr := stream.Read(buf)
	if readErr != nil && readErr.Error() != "EOF" {
		pm.logger.Debug("Health check failed - could not read from stream", zap.String("peer_id", peerID), zap.Error(readErr))
		return false
	}
	pm.logger.Debug("Health check passed", zap.String("peer_id", peerID))
	return true
}

// ValidateMetadata checks if metadata is recent and valid
func (pm *Manager) ValidateMetadata(metadata *crowdllama.Resource) error {
	if metadata == nil {
		return fmt.Errorf("metadata is nil")
	}
	if time.Since(metadata.LastUpdated) > pm.config.MaxMetadataAge {
		return fmt.Errorf("metadata is too old: %v", metadata.LastUpdated)
	}
	return nil
}
