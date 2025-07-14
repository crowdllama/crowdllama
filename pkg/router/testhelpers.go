package router

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"go.uber.org/zap"
)

// TestPeerBehavior defines how a test peer should behave
type TestPeerBehavior struct {
	Name                  string        // Human-readable name for the peer
	BaseLatency           time.Duration // Base latency for responses
	LatencyVariation      time.Duration // Random variation in latency
	DisconnectProbability float64       // Probability of disconnecting per minute
	ReconnectDelay        time.Duration // How long to stay disconnected
	ResponseDelay         time.Duration // Additional delay for ping responses
	PacketLoss            float64       // Probability of dropping ping responses
	
	// Intermittent connectivity patterns
	ConnectDuration    time.Duration // How long to stay connected
	DisconnectDuration time.Duration // How long to stay disconnected
	UseFixedPattern    bool          // Use fixed connect/disconnect pattern
}

// PredefinedBehaviors contains common test peer behaviors
var PredefinedBehaviors = map[string]TestPeerBehavior{
	"stable": {
		Name:                  "Stable Peer",
		BaseLatency:           50 * time.Millisecond,
		LatencyVariation:      10 * time.Millisecond,
		DisconnectProbability: 0.01, // 1% chance per minute
		ReconnectDelay:        5 * time.Second,
		ResponseDelay:         0,
		PacketLoss:            0.01, // 1% packet loss
	},
	"unstable": {
		Name:                  "Unstable Peer",
		BaseLatency:           100 * time.Millisecond,
		LatencyVariation:      50 * time.Millisecond,
		DisconnectProbability: 0.1, // 10% chance per minute
		ReconnectDelay:        15 * time.Second,
		ResponseDelay:         200 * time.Millisecond,
		PacketLoss:            0.05, // 5% packet loss
	},
	"intermittent": {
		Name:               "Intermittent Peer",
		BaseLatency:        30 * time.Millisecond,
		LatencyVariation:   20 * time.Millisecond,
		ConnectDuration:    45 * time.Second,
		DisconnectDuration: 15 * time.Second,
		UseFixedPattern:    true,
		ResponseDelay:      0,
		PacketLoss:         0.02, // 2% packet loss
	},
	"slow": {
		Name:                  "Slow Peer",
		BaseLatency:           500 * time.Millisecond,
		LatencyVariation:      200 * time.Millisecond,
		DisconnectProbability: 0.05, // 5% chance per minute
		ReconnectDelay:        10 * time.Second,
		ResponseDelay:         1 * time.Second,
		PacketLoss:            0.03, // 3% packet loss
	},
	"fast": {
		Name:                  "Fast Peer",
		BaseLatency:           10 * time.Millisecond,
		LatencyVariation:      5 * time.Millisecond,
		DisconnectProbability: 0.02, // 2% chance per minute
		ReconnectDelay:        3 * time.Second,
		ResponseDelay:         0,
		PacketLoss:            0.005, // 0.5% packet loss
	},
}

// TestPeer represents a test peer with configurable behavior
type TestPeer struct {
	host      host.Host
	dht       *dht.IpfsDHT
	router    *Router
	behavior  TestPeerBehavior
	logger    *zap.Logger
	
	// State management
	ctx           context.Context
	cancel        context.CancelFunc
	connected     bool
	lastToggle    time.Time
	mu            sync.RWMutex
	
	// Metrics
	connectCount    int
	disconnectCount int
	pingCount       int
	pongCount       int
}

// NewTestPeer creates a new test peer with specified behavior
func NewTestPeer(ctx context.Context, behavior TestPeerBehavior, bootstrapAddrs []string, logger *zap.Logger) (*TestPeer, error) {
	// Generate private key
	privKey, _, err := crypto.GenerateKeyPair(crypto.RSA, 2048)
	if err != nil {
		return nil, fmt.Errorf("failed to generate key pair: %w", err)
	}
	
	// Create libp2p host
	host, err := libp2p.New(
		libp2p.Identity(privKey),
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.DefaultTransports,
		libp2p.DefaultMuxers,
		libp2p.DefaultSecurity,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create libp2p host: %w", err)
	}
	
	// Create DHT
	kadDHT, err := dht.New(ctx, host, dht.Mode(dht.ModeClient))
	if err != nil {
		host.Close()
		return nil, fmt.Errorf("failed to create DHT: %w", err)
	}
	
	// Create router with intermittent test config
	config := IntermittentTestConfig()
	router, err := NewRouter(ctx, host, kadDHT, config, logger)
	if err != nil {
		host.Close()
		return nil, fmt.Errorf("failed to create router: %w", err)
	}
	
	peerCtx, cancel := context.WithCancel(ctx)
	
	peer := &TestPeer{
		host:      host,
		dht:       kadDHT,
		router:    router,
		behavior:  behavior,
		logger:    logger,
		ctx:       peerCtx,
		cancel:    cancel,
		connected: true,
		lastToggle: time.Now(),
	}
	
	// Set up custom ping handler with behavior simulation
	if err := peer.setupPingHandler(); err != nil {
		peer.Stop()
		return nil, fmt.Errorf("failed to setup ping handler: %w", err)
	}
	
	// Bootstrap to network
	if err := peer.bootstrap(bootstrapAddrs); err != nil {
		peer.Stop()
		return nil, fmt.Errorf("failed to bootstrap: %w", err)
	}
	
	// Start intermittent connectivity simulation
	peer.startConnectivitySimulation()
	
	return peer, nil
}

// setupPingHandler sets up a custom ping handler that simulates the behavior
func (tp *TestPeer) setupPingHandler() error {
	pingProtocol := protocol.ID("/crowdllama/ping/1.0.0")
	
	tp.host.SetStreamHandler(pingProtocol, func(stream network.Stream) {
		defer stream.Close()
		
		tp.mu.RLock()
		connected := tp.connected
		behavior := tp.behavior
		tp.mu.RUnlock()
		
		// Simulate disconnection
		if !connected {
			tp.logger.Debug("Simulating disconnection - ignoring ping",
				zap.String("peer", tp.behavior.Name),
				zap.String("from", stream.Conn().RemotePeer().String()))
			return
		}
		
		tp.pingCount++
		
		// Simulate packet loss
		if tp.shouldDropPacket(behavior.PacketLoss) {
			tp.logger.Debug("Simulating packet loss - dropping ping",
				zap.String("peer", tp.behavior.Name),
				zap.String("from", stream.Conn().RemotePeer().String()))
			return
		}
		
		// Read ping request
		buf := make([]byte, 1024)
		n, err := stream.Read(buf)
		if err != nil {
			tp.logger.Debug("Failed to read ping request", zap.Error(err))
			return
		}
		
		// Simulate response delay
		if behavior.ResponseDelay > 0 {
			time.Sleep(behavior.ResponseDelay)
		}
		
		// Simulate latency variation
		latency := tp.calculateLatency(behavior.BaseLatency, behavior.LatencyVariation)
		time.Sleep(latency)
		
		// Send pong response
		pongData := fmt.Sprintf("pong-%s-%d", tp.behavior.Name, time.Now().UnixNano())
		if _, err := stream.Write([]byte(pongData)); err != nil {
			tp.logger.Debug("Failed to write pong response", zap.Error(err))
			return
		}
		
		tp.pongCount++
		
		tp.logger.Debug("Handled ping with simulated behavior",
			zap.String("peer", tp.behavior.Name),
			zap.String("from", stream.Conn().RemotePeer().String()),
			zap.Duration("simulated_latency", latency),
			zap.String("data", string(buf[:n])))
	})
	
	return nil
}

// calculateLatency calculates latency with random variation
func (tp *TestPeer) calculateLatency(base, variation time.Duration) time.Duration {
	if variation == 0 {
		return base
	}
	
	// Generate random variation between -variation and +variation
	randomFactor := (2.0 * float64(time.Now().UnixNano()%1000000) / 1000000.0) - 1.0
	variationAmount := time.Duration(float64(variation) * randomFactor)
	
	result := base + variationAmount
	if result < 0 {
		result = base / 2 // Minimum latency
	}
	
	return result
}

// shouldDropPacket returns true if a packet should be dropped based on probability
func (tp *TestPeer) shouldDropPacket(probability float64) bool {
	if probability <= 0 {
		return false
	}
	
	randomValue := float64(time.Now().UnixNano()%1000000) / 1000000.0
	return randomValue < probability
}

// bootstrap connects to bootstrap peers
func (tp *TestPeer) bootstrap(bootstrapAddrs []string) error {
	for _, addr := range bootstrapAddrs {
		if err := tp.dht.Host().Connect(tp.ctx, peer.AddrInfo{}); err != nil {
			tp.logger.Debug("Failed to connect to bootstrap peer", zap.String("addr", addr), zap.Error(err))
		}
	}
	
	return tp.dht.Bootstrap(tp.ctx)
}

// startConnectivitySimulation starts the connectivity simulation goroutine
func (tp *TestPeer) startConnectivitySimulation() {
	go func() {
		if tp.behavior.UseFixedPattern {
			tp.runFixedConnectivityPattern()
		} else {
			tp.runRandomConnectivityPattern()
		}
	}()
}

// runFixedConnectivityPattern runs a fixed connect/disconnect pattern
func (tp *TestPeer) runFixedConnectivityPattern() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			tp.mu.Lock()
			now := time.Now()
			timeSinceToggle := now.Sub(tp.lastToggle)
			
			if tp.connected && timeSinceToggle >= tp.behavior.ConnectDuration {
				tp.simulateDisconnect()
				tp.lastToggle = now
			} else if !tp.connected && timeSinceToggle >= tp.behavior.DisconnectDuration {
				tp.simulateReconnect()
				tp.lastToggle = now
			}
			tp.mu.Unlock()
			
		case <-tp.ctx.Done():
			return
		}
	}
}

// runRandomConnectivityPattern runs a random disconnect pattern
func (tp *TestPeer) runRandomConnectivityPattern() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			tp.mu.Lock()
			if tp.connected {
				// Check if we should disconnect
				probability := tp.behavior.DisconnectProbability / 60.0 // Per second probability
				if tp.shouldDropPacket(probability) {
					tp.simulateDisconnect()
					
					// Schedule reconnection
					go func() {
						time.Sleep(tp.behavior.ReconnectDelay)
						tp.mu.Lock()
						tp.simulateReconnect()
						tp.mu.Unlock()
					}()
				}
			}
			tp.mu.Unlock()
			
		case <-tp.ctx.Done():
			return
		}
	}
}

// simulateDisconnect simulates peer disconnection (must be called with lock held)
func (tp *TestPeer) simulateDisconnect() {
	if !tp.connected {
		return
	}
	
	tp.connected = false
	tp.disconnectCount++
	
	tp.logger.Info("Simulating peer disconnection",
		zap.String("peer", tp.behavior.Name),
		zap.Int("disconnect_count", tp.disconnectCount))
}

// simulateReconnect simulates peer reconnection (must be called with lock held)
func (tp *TestPeer) simulateReconnect() {
	if tp.connected {
		return
	}
	
	tp.connected = true
	tp.connectCount++
	
	tp.logger.Info("Simulating peer reconnection",
		zap.String("peer", tp.behavior.Name),
		zap.Int("connect_count", tp.connectCount))
}

// Start starts the test peer
func (tp *TestPeer) Start() error {
	return tp.router.Start()
}

// Stop stops the test peer and cleans up resources
func (tp *TestPeer) Stop() error {
	tp.cancel()
	
	if tp.router != nil {
		tp.router.Stop()
	}
	
	if tp.host != nil {
		tp.host.Close()
	}
	
	return nil
}

// GetMetrics returns metrics about the test peer's behavior
func (tp *TestPeer) GetMetrics() map[string]interface{} {
	tp.mu.RLock()
	defer tp.mu.RUnlock()
	
	return map[string]interface{}{
		"name":             tp.behavior.Name,
		"peer_id":          tp.host.ID().String(),
		"connected":        tp.connected,
		"connect_count":    tp.connectCount,
		"disconnect_count": tp.disconnectCount,
		"ping_count":       tp.pingCount,
		"pong_count":       tp.pongCount,
		"uptime_ratio":     tp.calculateUptimeRatio(),
	}
}

// calculateUptimeRatio calculates the uptime ratio
func (tp *TestPeer) calculateUptimeRatio() float64 {
	totalEvents := tp.connectCount + tp.disconnectCount
	if totalEvents == 0 {
		return 1.0 // Initially connected
	}
	
	return float64(tp.connectCount) / float64(totalEvents)
}

// GetHost returns the libp2p host
func (tp *TestPeer) GetHost() host.Host {
	return tp.host
}

// GetRouter returns the router instance
func (tp *TestPeer) GetRouter() *Router {
	return tp.router
}

// CreateTestPeers creates multiple test peers with different behaviors
func CreateTestPeers(ctx context.Context, count int, bootstrapAddrs []string, logger *zap.Logger) ([]*TestPeer, error) {
	behaviorNames := []string{"stable", "unstable", "intermittent", "slow", "fast"}
	peers := make([]*TestPeer, 0, count)
	
	for i := 0; i < count; i++ {
		behaviorName := behaviorNames[i%len(behaviorNames)]
		behavior := PredefinedBehaviors[behaviorName]
		behavior.Name = fmt.Sprintf("%s-%d", behavior.Name, i+1)
		
		peer, err := NewTestPeer(ctx, behavior, bootstrapAddrs, logger)
		if err != nil {
			// Clean up previously created peers
			for _, p := range peers {
				p.Stop()
			}
			return nil, fmt.Errorf("failed to create test peer %d: %w", i, err)
		}
		
		peers = append(peers, peer)
	}
	
	return peers, nil
}

// StartAllPeers starts all test peers
func StartAllPeers(peers []*TestPeer) error {
	for i, peer := range peers {
		if err := peer.Start(); err != nil {
			return fmt.Errorf("failed to start peer %d: %w", i, err)
		}
	}
	return nil
}

// StopAllPeers stops all test peers
func StopAllPeers(peers []*TestPeer) {
	for _, peer := range peers {
		peer.Stop()
	}
}