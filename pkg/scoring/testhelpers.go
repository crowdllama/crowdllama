// Package scoring provides test helpers for the scoring system.
package scoring

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"

	"github.com/crowdllama/crowdllama/pkg/crowdllama"
	"github.com/crowdllama/crowdllama/pkg/dht"
)

// PeerProfile defines the characteristics of a test peer
type PeerProfile struct {
	Name                string
	LatencyRange        LatencyRange
	UptimeProfile       UptimeProfile
	PerformanceProfile  PerformanceProfile
	ReliabilityProfile  ReliabilityProfile
}

// LatencyRange defines the latency characteristics
type LatencyRange struct {
	Min       time.Duration
	Max       time.Duration
	Variation float64 // 0-1, how much variation in latency
}

// UptimeProfile defines uptime characteristics
type UptimeProfile struct {
	BaseUptime       float64 // 0-1, base uptime percentage
	DowntimePatterns []DowntimePattern
}

// DowntimePattern defines patterns of disconnections
type DowntimePattern struct {
	Frequency time.Duration // How often this pattern occurs
	Duration  time.Duration // How long each downtime lasts
}

// PerformanceProfile defines performance characteristics
type PerformanceProfile struct {
	TokensThroughput float64
	VRAMGB          int
	LoadPattern     LoadPattern
	GPUModel        string
}

// LoadPattern defines how load changes over time
type LoadPattern struct {
	BaseLoad    float64 // 0-1, base load level
	LoadSpikes  []LoadSpike
}

// LoadSpike defines temporary load increases
type LoadSpike struct {
	Frequency time.Duration
	Duration  time.Duration
	Magnitude float64 // Additional load (0-1)
}

// ReliabilityProfile defines connection reliability
type ReliabilityProfile struct {
	ConnectionFailureRate float64 // 0-1, rate of connection failures
	RecoveryTime         time.Duration
}

// TestPeer represents a test peer with simulated characteristics
type TestPeer struct {
	Host           host.Host
	PeerID         string
	Profile        PeerProfile
	DHT            *dht.Server
	ScoringManager *ScoringManager
	ctx            context.Context
	cancel         context.CancelFunc
	logger         *zap.Logger
	
	// Simulation state
	currentLoad      float64
	isOnline         bool
	lastDisconnect   time.Time
	simulationStart  time.Time
	connectionCount  int
	disconnectCount  int
}

// PredefinedProfiles provides common peer profiles for testing
var PredefinedProfiles = map[string]PeerProfile{
	"HighPerformance": {
		Name: "High Performance Peer",
		LatencyRange: LatencyRange{
			Min:       5 * time.Millisecond,
			Max:       20 * time.Millisecond,
			Variation: 0.2,
		},
		UptimeProfile: UptimeProfile{
			BaseUptime: 0.98,
			DowntimePatterns: []DowntimePattern{
				{Frequency: 24 * time.Hour, Duration: 5 * time.Minute},
			},
		},
		PerformanceProfile: PerformanceProfile{
			TokensThroughput: 150,
			VRAMGB:          24,
			GPUModel:        "RTX 4090",
			LoadPattern: LoadPattern{
				BaseLoad: 0.3,
				LoadSpikes: []LoadSpike{
					{Frequency: 30 * time.Minute, Duration: 5 * time.Minute, Magnitude: 0.4},
				},
			},
		},
		ReliabilityProfile: ReliabilityProfile{
			ConnectionFailureRate: 0.05,
			RecoveryTime:         30 * time.Second,
		},
	},
	"LowLatency": {
		Name: "Low Latency Peer",
		LatencyRange: LatencyRange{
			Min:       2 * time.Millisecond,
			Max:       10 * time.Millisecond,
			Variation: 0.1,
		},
		UptimeProfile: UptimeProfile{
			BaseUptime: 0.95,
			DowntimePatterns: []DowntimePattern{
				{Frequency: 12 * time.Hour, Duration: 2 * time.Minute},
			},
		},
		PerformanceProfile: PerformanceProfile{
			TokensThroughput: 80,
			VRAMGB:          16,
			GPUModel:        "RTX 3080",
			LoadPattern: LoadPattern{
				BaseLoad: 0.2,
				LoadSpikes: []LoadSpike{
					{Frequency: 1 * time.Hour, Duration: 10 * time.Minute, Magnitude: 0.3},
				},
			},
		},
		ReliabilityProfile: ReliabilityProfile{
			ConnectionFailureRate: 0.03,
			RecoveryTime:         15 * time.Second,
		},
	},
	"Unreliable": {
		Name: "Unreliable Peer",
		LatencyRange: LatencyRange{
			Min:       50 * time.Millisecond,
			Max:       200 * time.Millisecond,
			Variation: 0.8,
		},
		UptimeProfile: UptimeProfile{
			BaseUptime: 0.75,
			DowntimePatterns: []DowntimePattern{
				{Frequency: 2 * time.Hour, Duration: 15 * time.Minute},
				{Frequency: 6 * time.Hour, Duration: 5 * time.Minute},
			},
		},
		PerformanceProfile: PerformanceProfile{
			TokensThroughput: 40,
			VRAMGB:          8,
			GPUModel:        "GTX 1080",
			LoadPattern: LoadPattern{
				BaseLoad: 0.6,
				LoadSpikes: []LoadSpike{
					{Frequency: 20 * time.Minute, Duration: 15 * time.Minute, Magnitude: 0.3},
				},
			},
		},
		ReliabilityProfile: ReliabilityProfile{
			ConnectionFailureRate: 0.25,
			RecoveryTime:         2 * time.Minute,
		},
	},
	"Stable": {
		Name: "Stable Peer",
		LatencyRange: LatencyRange{
			Min:       25 * time.Millisecond,
			Max:       40 * time.Millisecond,
			Variation: 0.15,
		},
		UptimeProfile: UptimeProfile{
			BaseUptime: 0.999,
			DowntimePatterns: []DowntimePattern{
				{Frequency: 7 * 24 * time.Hour, Duration: 30 * time.Second},
			},
		},
		PerformanceProfile: PerformanceProfile{
			TokensThroughput: 90,
			VRAMGB:          12,
			GPUModel:        "RTX 3070",
			LoadPattern: LoadPattern{
				BaseLoad: 0.4,
				LoadSpikes: []LoadSpike{
					{Frequency: 2 * time.Hour, Duration: 30 * time.Minute, Magnitude: 0.2},
				},
			},
		},
		ReliabilityProfile: ReliabilityProfile{
			ConnectionFailureRate: 0.01,
			RecoveryTime:         5 * time.Second,
		},
	},
	"LoadBalanced": {
		Name: "Load Balanced Peer",
		LatencyRange: LatencyRange{
			Min:       30 * time.Millisecond,
			Max:       50 * time.Millisecond,
			Variation: 0.25,
		},
		UptimeProfile: UptimeProfile{
			BaseUptime: 0.96,
			DowntimePatterns: []DowntimePattern{
				{Frequency: 18 * time.Hour, Duration: 3 * time.Minute},
			},
		},
		PerformanceProfile: PerformanceProfile{
			TokensThroughput: 110,
			VRAMGB:          16,
			GPUModel:        "RTX 4080",
			LoadPattern: LoadPattern{
				BaseLoad: 0.1, // Very low base load
				LoadSpikes: []LoadSpike{
					{Frequency: 45 * time.Minute, Duration: 20 * time.Minute, Magnitude: 0.5},
				},
			},
		},
		ReliabilityProfile: ReliabilityProfile{
			ConnectionFailureRate: 0.08,
			RecoveryTime:         45 * time.Second,
		},
	},
}

// CreateTestPeer creates a test peer with the specified profile
func CreateTestPeer(ctx context.Context, profile PeerProfile, port int, logger *zap.Logger) (*TestPeer, error) {
	// Generate key pair
	privKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 256)
	if err != nil {
		return nil, fmt.Errorf("failed to generate key pair: %w", err)
	}
	
	// Create libp2p host
	listenAddr := fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", port)
	host, err := libp2p.New(
		libp2p.Identity(privKey),
		libp2p.ListenAddrStrings(listenAddr),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create libp2p host: %w", err)
	}
	
	ctx, cancel := context.WithCancel(ctx)
	
	// Create DHT server
	dhtServer, err := dht.NewDHTServerWithAddrs(ctx, privKey, logger, []string{listenAddr})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create DHT server: %w", err)
	}
	
	// Create scoring manager
	scoringManager := NewScoringManager(ctx, host, logger)
	
	testPeer := &TestPeer{
		Host:           host,
		PeerID:         host.ID().String(),
		Profile:        profile,
		DHT:            dhtServer,
		ScoringManager: scoringManager,
		ctx:            ctx,
		cancel:         cancel,
		logger:         logger,
		currentLoad:    profile.PerformanceProfile.LoadPattern.BaseLoad,
		isOnline:       true,
		simulationStart: time.Now(),
	}
	
	return testPeer, nil
}

// Start starts the test peer with simulation
func (tp *TestPeer) Start() error {
	// Start DHT server
	if _, err := tp.DHT.Start(); err != nil {
		return fmt.Errorf("failed to start DHT server: %w", err)
	}
	
	// Start scoring manager
	tp.ScoringManager.Start()
	
	// Start simulation loops
	go tp.simulateLatency()
	go tp.simulateUptime()
	go tp.simulateLoad()
	go tp.simulateReliability()
	
	tp.logger.Info("Test peer started",
		zap.String("peer_id", tp.PeerID),
		zap.String("profile", tp.Profile.Name))
	
	return nil
}

// Stop stops the test peer
func (tp *TestPeer) Stop() {
	tp.logger.Info("Stopping test peer", zap.String("peer_id", tp.PeerID))
	
	tp.ScoringManager.Stop()
	tp.DHT.Stop()
	tp.cancel()
	
	if err := tp.Host.Close(); err != nil {
		tp.logger.Error("Failed to close host", zap.Error(err))
	}
}

// ConnectToPeer connects this peer to another peer
func (tp *TestPeer) ConnectToPeer(ctx context.Context, targetPeer *TestPeer) error {
	// Get target peer's addresses
	targetAddrs := targetPeer.Host.Addrs()
	if len(targetAddrs) == 0 {
		return fmt.Errorf("target peer has no addresses")
	}
	
	// Create peer info
	targetPeerInfo := peer.AddrInfo{
		ID:    targetPeer.Host.ID(),
		Addrs: targetAddrs,
	}
	
	// Connect
	if err := tp.Host.Connect(ctx, targetPeerInfo); err != nil {
		tp.logger.Error("Failed to connect to peer",
			zap.String("target_peer", targetPeer.PeerID),
			zap.Error(err))
		return fmt.Errorf("failed to connect to peer: %w", err)
	}
	
	tp.connectionCount++
	tp.logger.Debug("Connected to peer",
		zap.String("target_peer", targetPeer.PeerID))
	
	return nil
}

// GetCurrentResource returns the current resource state
func (tp *TestPeer) GetCurrentResource() *crowdllama.Resource {
	return &crowdllama.Resource{
		PeerID:           tp.PeerID,
		SupportedModels:  []string{"llama3.2", "mistral"},
		TokensThroughput: tp.Profile.PerformanceProfile.TokensThroughput,
		VRAMGB:          tp.Profile.PerformanceProfile.VRAMGB,
		Load:            tp.currentLoad,
		GPUModel:        tp.Profile.PerformanceProfile.GPUModel,
		LastUpdated:     time.Now(),
		Version:         "test-version",
		WorkerMode:      true,
	}
}

// Simulation methods

// simulateLatency simulates latency variations
func (tp *TestPeer) simulateLatency() {
	// This would normally be handled by the network stack
	// For testing purposes, we'll inject simulated ping results
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			// Simulate ping results based on profile
			latency := tp.generateLatency()
			
			// Create fake ping result
			result := PingResult{
				PeerID:    tp.PeerID,
				Latency:   latency,
				Success:   true,
				Timestamp: time.Now(),
			}
			
			// Add to our own metrics (simulating external ping)
			tp.ScoringManager.recordPingResult(tp.PeerID, result.Latency, result.Success, result.Timestamp)
			
		case <-tp.ctx.Done():
			return
		}
	}
}

// simulateUptime simulates uptime patterns
func (tp *TestPeer) simulateUptime() {
	for {
		select {
		case <-tp.ctx.Done():
			return
		default:
			// Check if we should go offline based on profile
			if tp.shouldGoOffline() {
				tp.simulateDisconnection()
			}
			
			time.Sleep(1 * time.Second)
		}
	}
}

// simulateLoad simulates load changes
func (tp *TestPeer) simulateLoad() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			// Update load based on profile
			tp.updateLoad()
			
			// Update resource in scoring manager
			resource := tp.GetCurrentResource()
			tp.ScoringManager.AddPeer(tp.PeerID, resource)
			
		case <-tp.ctx.Done():
			return
		}
	}
}

// simulateReliability simulates connection reliability
func (tp *TestPeer) simulateReliability() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			// Simulate connection failures based on profile
			if tp.shouldFailConnection() {
				tp.simulateConnectionFailure()
			}
			
		case <-tp.ctx.Done():
			return
		}
	}
}

// Helper methods for simulation

// generateLatency generates a latency value based on the profile
func (tp *TestPeer) generateLatency() time.Duration {
	latRange := tp.Profile.LatencyRange
	
	// Base latency within range
	minMs := latRange.Min.Milliseconds()
	maxMs := latRange.Max.Milliseconds()
	
	baseLatency := minMs + (maxMs-minMs)/2
	
	// Add variation
	variation := float64(maxMs-minMs) * latRange.Variation
	
	// Generate random variation
	randomVariation, _ := rand.Int(rand.Reader, big.NewInt(int64(variation*2)))
	finalLatency := baseLatency + randomVariation.Int64() - int64(variation)
	
	if finalLatency < minMs {
		finalLatency = minMs
	}
	
	return time.Duration(finalLatency) * time.Millisecond
}

// shouldGoOffline determines if the peer should go offline
func (tp *TestPeer) shouldGoOffline() bool {
	if !tp.isOnline {
		return false
	}
	
	uptime := tp.Profile.UptimeProfile
	
	// Simple probability based on uptime profile
	uptimeProb := uptime.BaseUptime
	
	// Check downtime patterns
	now := time.Now()
	for _, pattern := range uptime.DowntimePatterns {
		// Simple simulation: if we're in a downtime window
		timeSinceStart := now.Sub(tp.simulationStart)
		cyclePos := time.Duration(math.Mod(float64(timeSinceStart), float64(pattern.Frequency)))
		
		if cyclePos < pattern.Duration {
			return true
		}
	}
	
	// Random chance based on uptime
	randomNum, _ := rand.Int(rand.Reader, big.NewInt(1000))
	return randomNum.Int64() > int64(uptimeProb*1000)
}

// updateLoad updates the current load based on the profile
func (tp *TestPeer) updateLoad() {
	loadPattern := tp.Profile.PerformanceProfile.LoadPattern
	
	// Start with base load
	newLoad := loadPattern.BaseLoad
	
	// Check for load spikes
	now := time.Now()
	for _, spike := range loadPattern.LoadSpikes {
		timeSinceStart := now.Sub(tp.simulationStart)
		cyclePos := time.Duration(math.Mod(float64(timeSinceStart), float64(spike.Frequency)))
		
		if cyclePos < spike.Duration {
			newLoad += spike.Magnitude
		}
	}
	
	// Add some random variation
	randomVariation, _ := rand.Int(rand.Reader, big.NewInt(100))
	variation := (float64(randomVariation.Int64()) - 50) / 1000 // -0.05 to +0.05
	newLoad += variation
	
	// Clamp to valid range
	if newLoad < 0 {
		newLoad = 0
	}
	if newLoad > 1.0 {
		newLoad = 1.0
	}
	
	tp.currentLoad = newLoad
}

// shouldFailConnection determines if a connection should fail
func (tp *TestPeer) shouldFailConnection() bool {
	reliability := tp.Profile.ReliabilityProfile
	
	randomNum, _ := rand.Int(rand.Reader, big.NewInt(1000))
	return randomNum.Int64() < int64(reliability.ConnectionFailureRate*1000)
}

// simulateDisconnection simulates going offline
func (tp *TestPeer) simulateDisconnection() {
	if !tp.isOnline {
		return
	}
	
	tp.isOnline = false
	tp.lastDisconnect = time.Now()
	tp.disconnectCount++
	
	tp.logger.Info("Peer going offline (simulated)",
		zap.String("peer_id", tp.PeerID))
	
	// Notify scoring manager
	tp.ScoringManager.OnPeerDisconnected(tp.PeerID)
	
	// Schedule reconnection
	go tp.scheduleReconnection()
}

// simulateConnectionFailure simulates a connection failure
func (tp *TestPeer) simulateConnectionFailure() {
	tp.logger.Debug("Simulating connection failure",
		zap.String("peer_id", tp.PeerID))
	
	// Simulate temporary disconnection
	tp.ScoringManager.OnPeerDisconnected(tp.PeerID)
	
	// Schedule quick recovery
	go func() {
		time.Sleep(tp.Profile.ReliabilityProfile.RecoveryTime)
		tp.ScoringManager.OnPeerConnected(tp.PeerID)
	}()
}

// scheduleReconnection schedules the peer to come back online
func (tp *TestPeer) scheduleReconnection() {
	// Determine reconnection time based on profile
	downtime := tp.Profile.UptimeProfile.DowntimePatterns[0].Duration
	if len(tp.Profile.UptimeProfile.DowntimePatterns) > 1 {
		// Use a random pattern
		randomIdx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(tp.Profile.UptimeProfile.DowntimePatterns))))
		downtime = tp.Profile.UptimeProfile.DowntimePatterns[randomIdx.Int64()].Duration
	}
	
	time.Sleep(downtime)
	
	if tp.ctx.Err() != nil {
		return
	}
	
	tp.isOnline = true
	tp.connectionCount++
	
	tp.logger.Info("Peer coming back online (simulated)",
		zap.String("peer_id", tp.PeerID))
	
	// Notify scoring manager
	tp.ScoringManager.OnPeerConnected(tp.PeerID)
	
	// Re-add to scoring manager
	resource := tp.GetCurrentResource()
	tp.ScoringManager.AddPeer(tp.PeerID, resource)
}

// GetStats returns statistics about the test peer
func (tp *TestPeer) GetStats() map[string]interface{} {
	return map[string]interface{}{
		"peer_id":           tp.PeerID,
		"profile":           tp.Profile.Name,
		"is_online":         tp.isOnline,
		"current_load":      tp.currentLoad,
		"connection_count":  tp.connectionCount,
		"disconnect_count":  tp.disconnectCount,
		"uptime_since_start": time.Since(tp.simulationStart).Seconds(),
	}
}