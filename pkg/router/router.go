package router

import (
	"context"
	"fmt"
	"sync"
	"time"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"go.uber.org/zap"

	"github.com/crowdllama/crowdllama/pkg/peermanager"
)

// Router combines DHT functionality with ping monitoring and metrics collection
type Router struct {
	// Core components
	host        host.Host
	dht         *dht.IpfsDHT
	logger      *zap.Logger
	config      *Config
	
	// Context management
	ctx         context.Context
	cancel      context.CancelFunc
	
	// Metrics and monitoring
	metrics     *RouterMetrics
	
	// Peer management
	peerManager *peermanager.Manager
	
	// Ping monitoring
	pingCancel  context.CancelFunc
	pingSem     chan struct{} // Semaphore for concurrent pings
	
	// State management
	knownPeers  map[peer.ID]time.Time // Track when we first discovered each peer
	peersMu     sync.RWMutex
	
	// Callbacks
	onPeerDiscovered func(peer.ID)
	onPeerLost       func(peer.ID)
}

// NewRouter creates a new router instance
func NewRouter(ctx context.Context, h host.Host, kadDHT *dht.IpfsDHT, config *Config, logger *zap.Logger) (*Router, error) {
	ctx, cancel := context.WithCancel(ctx)
	
	// Create peer manager with router config
	pmConfig := &peermanager.Config{
		DiscoveryInterval:      config.DiscoveryInterval,
		AdvertisingInterval:    config.AdvertisingInterval,
		MetadataUpdateInterval: config.MetadataUpdateInterval,
		PeerHealthConfig: &peermanager.PeerHealthConfig{
			StalePeerTimeout:    config.StalePeerTimeout,
			HealthCheckInterval: config.HealthCheckInterval,
			MaxFailedAttempts:   config.MaxFailedAttempts,
			BackoffBase:         config.BackoffBase,
			MetadataTimeout:     config.MetadataTimeout,
			MaxMetadataAge:      config.MaxMetadataAge,
		},
	}
	
	router := &Router{
		host:        h,
		dht:         kadDHT,
		logger:      logger,
		config:      config,
		ctx:         ctx,
		cancel:      cancel,
		metrics:     NewRouterMetrics(h.ID()),
		peerManager: peermanager.NewManager(ctx, h, kadDHT, logger, pmConfig),
		pingSem:     make(chan struct{}, config.MaxConcurrentPings),
		knownPeers:  make(map[peer.ID]time.Time),
	}
	
	// Set up ping protocol handler
	if err := router.setupPingProtocol(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to setup ping protocol: %w", err)
	}
	
	return router, nil
}

// Start starts the router and all its components
func (r *Router) Start() error {
	r.logger.Info("Starting router", zap.String("peer_id", r.host.ID().String()))
	
	// Start peer manager
	r.peerManager.Start()
	
	// Start ping monitoring
	r.startPingMonitoring()
	
	// Start periodic peer discovery
	r.startPeerDiscovery()
	
	r.logger.Info("Router started successfully")
	return nil
}

// Stop stops the router and all its components
func (r *Router) Stop() error {
	r.logger.Info("Stopping router")
	
	// Cancel ping monitoring
	if r.pingCancel != nil {
		r.pingCancel()
	}
	
	// Stop peer manager
	r.peerManager.Stop()
	
	// Cancel main context
	r.cancel()
	
	r.logger.Info("Router stopped")
	return nil
}

// setupPingProtocol sets up the ping protocol handler
func (r *Router) setupPingProtocol() error {
	pingProtocol := protocol.ID("/crowdllama/ping/1.0.0")
	
	r.host.SetStreamHandler(pingProtocol, func(stream network.Stream) {
		defer stream.Close()
		
		// Read ping request
		buf := make([]byte, 1024)
		n, err := stream.Read(buf)
		if err != nil {
			r.logger.Debug("Failed to read ping request", zap.Error(err))
			return
		}
		
		// Send pong response
		pongData := fmt.Sprintf("pong-%d", time.Now().UnixNano())
		if _, err := stream.Write([]byte(pongData)); err != nil {
			r.logger.Debug("Failed to write pong response", zap.Error(err))
			return
		}
		
		r.logger.Debug("Handled ping request", 
			zap.String("from", stream.Conn().RemotePeer().String()),
			zap.String("data", string(buf[:n])))
	})
	
	return nil
}

// startPingMonitoring starts the ping monitoring goroutine
func (r *Router) startPingMonitoring() {
	pingCtx, pingCancel := context.WithCancel(r.ctx)
	r.pingCancel = pingCancel
	
	go r.pingMonitoringLoop(pingCtx)
}

// pingMonitoringLoop runs the main ping monitoring loop
func (r *Router) pingMonitoringLoop(ctx context.Context) {
	ticker := time.NewTicker(r.config.PingInterval)
	defer ticker.Stop()
	
	r.logger.Info("Starting ping monitoring loop", 
		zap.Duration("interval", r.config.PingInterval))
	
	for {
		select {
		case <-ticker.C:
			r.pingAllKnownPeers()
		case <-ctx.Done():
			r.logger.Info("Ping monitoring loop stopped")
			return
		}
	}
}

// pingAllKnownPeers pings all known peers concurrently
func (r *Router) pingAllKnownPeers() {
	r.peersMu.RLock()
	peers := make([]peer.ID, 0, len(r.knownPeers))
	for peerID := range r.knownPeers {
		peers = append(peers, peerID)
	}
	r.peersMu.RUnlock()
	
	if len(peers) == 0 {
		return
	}
	
	r.logger.Debug("Pinging all known peers", zap.Int("count", len(peers)))
	
	// Use semaphore to limit concurrent pings
	var wg sync.WaitGroup
	for _, peerID := range peers {
		wg.Add(1)
		go func(pid peer.ID) {
			defer wg.Done()
			r.pingPeer(pid)
		}(peerID)
	}
	
	wg.Wait()
}

// pingPeer pings a specific peer and records the result
func (r *Router) pingPeer(peerID peer.ID) {
	// Acquire semaphore
	r.pingSem <- struct{}{}
	defer func() { <-r.pingSem }()
	
	startTime := time.Now()
	result := PingResult{
		Timestamp: startTime,
		Success:   false,
	}
	
	// Ensure peer is tracked in metrics
	r.metrics.AddPeer(peerID, r.config.MetricsWindowSize, r.config.MetricsTickDuration)
	
	// Create ping context with timeout
	ctx, cancel := context.WithTimeout(r.ctx, r.config.PingTimeout)
	defer cancel()
	
	// Open stream to peer
	stream, err := r.host.NewStream(ctx, peerID, protocol.ID("/crowdllama/ping/1.0.0"))
	if err != nil {
		result.Error = fmt.Sprintf("failed to open stream: %v", err)
		r.metrics.RecordPingResult(peerID, result)
		r.logger.Debug("Failed to ping peer", 
			zap.String("peer_id", peerID.String()), 
			zap.Error(err))
		return
	}
	defer stream.Close()
	
	// Send ping
	pingData := fmt.Sprintf("ping-%d", startTime.UnixNano())
	if _, err := stream.Write([]byte(pingData)); err != nil {
		result.Error = fmt.Sprintf("failed to write ping: %v", err)
		r.metrics.RecordPingResult(peerID, result)
		r.logger.Debug("Failed to write ping", 
			zap.String("peer_id", peerID.String()), 
			zap.Error(err))
		return
	}
	
	// Read pong response
	buf := make([]byte, 1024)
	n, err := stream.Read(buf)
	if err != nil {
		result.Error = fmt.Sprintf("failed to read pong: %v", err)
		r.metrics.RecordPingResult(peerID, result)
		r.logger.Debug("Failed to read pong", 
			zap.String("peer_id", peerID.String()), 
			zap.Error(err))
		return
	}
	
	// Calculate latency
	endTime := time.Now()
	result.Latency = endTime.Sub(startTime)
	result.Success = true
	
	// Record successful ping
	r.metrics.RecordPingResult(peerID, result)
	
	r.logger.Debug("Ping successful", 
		zap.String("peer_id", peerID.String()),
		zap.Duration("latency", result.Latency),
		zap.String("response", string(buf[:n])))
}

// startPeerDiscovery starts periodic peer discovery
func (r *Router) startPeerDiscovery() {
	go func() {
		ticker := time.NewTicker(r.config.DiscoveryInterval)
		defer ticker.Stop()
		
		for {
			select {
			case <-ticker.C:
				r.discoverPeers()
			case <-r.ctx.Done():
				return
			}
		}
	}()
}

// discoverPeers discovers new peers and updates the known peers list
func (r *Router) discoverPeers() {
	healthyPeers := r.peerManager.GetHealthyPeers()
	
	r.peersMu.Lock()
	defer r.peersMu.Unlock()
	
	newPeers := 0
	for peerIDStr, peerInfo := range healthyPeers {
		peerID, err := peer.Decode(peerIDStr)
		if err != nil {
			r.logger.Warn("Failed to decode peer ID", zap.String("peer_id", peerIDStr), zap.Error(err))
			continue
		}
		
		// Skip self
		if peerID == r.host.ID() {
			continue
		}
		
		// Check if this is a new peer
		if _, exists := r.knownPeers[peerID]; !exists {
			r.knownPeers[peerID] = time.Now()
			newPeers++
			
			// Initialize metrics for new peer
			r.metrics.AddPeer(peerID, r.config.MetricsWindowSize, r.config.MetricsTickDuration)
			
			// Call discovery callback
			if r.onPeerDiscovered != nil {
				r.onPeerDiscovered(peerID)
			}
			
			r.logger.Info("Discovered new peer", 
				zap.String("peer_id", peerID.String()),
				zap.String("gpu_model", peerInfo.Metadata.GPUModel))
		}
	}
	
	if newPeers > 0 {
		r.logger.Info("Peer discovery completed", 
			zap.Int("new_peers", newPeers),
			zap.Int("total_known", len(r.knownPeers)))
	}
}

// GetPeerMetrics returns metrics for a specific peer
func (r *Router) GetPeerMetrics(peerID peer.ID) (PeerMetricsSnapshot, bool) {
	return r.metrics.GetPeerMetrics(peerID)
}

// GetAllPeerMetrics returns metrics for all peers
func (r *Router) GetAllPeerMetrics() map[peer.ID]PeerMetricsSnapshot {
	return r.metrics.GetAllMetrics()
}

// GetSummaryJSON returns a JSON summary of all peer metrics
func (r *Router) GetSummaryJSON() ([]byte, error) {
	return r.metrics.GetSummaryJSON()
}

// GetHealthyPeers returns peers that meet health criteria
func (r *Router) GetHealthyPeers(maxLatency time.Duration, minUptimePercent float64) []peer.ID {
	return r.metrics.GetHealthyPeers(maxLatency, minUptimePercent)
}

// GetKnownPeers returns all known peer IDs
func (r *Router) GetKnownPeers() []peer.ID {
	r.peersMu.RLock()
	defer r.peersMu.RUnlock()
	
	peers := make([]peer.ID, 0, len(r.knownPeers))
	for peerID := range r.knownPeers {
		peers = append(peers, peerID)
	}
	return peers
}

// SetPeerDiscoveryCallback sets a callback for when new peers are discovered
func (r *Router) SetPeerDiscoveryCallback(callback func(peer.ID)) {
	r.onPeerDiscovered = callback
}

// SetPeerLostCallback sets a callback for when peers are lost
func (r *Router) SetPeerLostCallback(callback func(peer.ID)) {
	r.onPeerLost = callback
}

// GetPeerManager returns the underlying peer manager
func (r *Router) GetPeerManager() *peermanager.Manager {
	return r.peerManager
}

// GetDHT returns the underlying DHT instance
func (r *Router) GetDHT() *dht.IpfsDHT {
	return r.dht
}

// GetHost returns the underlying libp2p host
func (r *Router) GetHost() host.Host {
	return r.host
}