// Package scoring provides peer scoring functionality for CrowdLlama P2P network.
package scoring

import (
	"context"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"

	"github.com/crowdllama/crowdllama/pkg/crowdllama"
)

const (
	// PingProtocol is the protocol used for ping measurements
	PingProtocol = "/crowdllama/ping/1.0.0"
	
	// DefaultPingInterval is how often to ping each peer
	DefaultPingInterval = 30 * time.Second
	
	// DefaultPingTimeout is the timeout for ping requests
	DefaultPingTimeout = 5 * time.Second
	
	// MaxPingHistory is the maximum number of ping results to keep
	MaxPingHistory = 100
	
	// MaxUptimeHistory is the maximum number of uptime records to keep
	MaxUptimeHistory = 1000
)

// PeerScore represents a comprehensive score for a peer
type PeerScore struct {
	PeerID           string    `json:"peer_id"`
	OverallScore     float64   `json:"overall_score"`     // 0-100, higher is better
	LatencyScore     float64   `json:"latency_score"`     // 0-100, lower latency = higher score
	UptimeScore      float64   `json:"uptime_score"`      // 0-100, higher uptime = higher score
	PerformanceScore float64   `json:"performance_score"` // 0-100, based on throughput and load
	ReliabilityScore float64   `json:"reliability_score"` // 0-100, based on connection stability
	LastUpdated      time.Time `json:"last_updated"`
	
	// Detailed metrics
	AverageLatency    time.Duration `json:"average_latency"`
	UptimePercentage  float64       `json:"uptime_percentage"`
	ThroughputRating  float64       `json:"throughput_rating"`
	LoadRating        float64       `json:"load_rating"`
	ConnectionStability float64     `json:"connection_stability"`
}

// PingResult stores the result of a ping to a peer
type PingResult struct {
	PeerID    string        `json:"peer_id"`
	Latency   time.Duration `json:"latency"`
	Success   bool          `json:"success"`
	Timestamp time.Time     `json:"timestamp"`
}

// UptimeRecord tracks uptime for a peer
type UptimeRecord struct {
	PeerID    string    `json:"peer_id"`
	Online    bool      `json:"online"`
	Timestamp time.Time `json:"timestamp"`
}

// PeerMetrics stores comprehensive metrics for a peer
type PeerMetrics struct {
	PeerID           string                    `json:"peer_id"`
	PingHistory      []PingResult              `json:"ping_history"`
	UptimeHistory    []UptimeRecord            `json:"uptime_history"`
	FirstSeen        time.Time                 `json:"first_seen"`
	LastSeen         time.Time                 `json:"last_seen"`
	ConnectionCount  int                       `json:"connection_count"`
	DisconnectionCount int                     `json:"disconnection_count"`
	LastResource     *crowdllama.Resource      `json:"last_resource"`
	mu               sync.RWMutex
}

// ScoringManager manages peer scoring for the network
type ScoringManager struct {
	host            host.Host
	logger          *zap.Logger
	ctx             context.Context
	cancel          context.CancelFunc
	
	// Metrics storage
	peerMetrics     map[string]*PeerMetrics
	peerScores      map[string]*PeerScore
	mu              sync.RWMutex
	
	// Configuration
	pingInterval    time.Duration
	pingTimeout     time.Duration
	
	// Scoring weights (should sum to 1.0)
	weights         ScoringWeights
}

// ScoringWeights defines the importance of different scoring factors
type ScoringWeights struct {
	Latency     float64 `json:"latency"`     // Weight for latency score
	Uptime      float64 `json:"uptime"`      // Weight for uptime score
	Performance float64 `json:"performance"` // Weight for performance score
	Reliability float64 `json:"reliability"` // Weight for reliability score
}

// DefaultScoringWeights returns balanced scoring weights
func DefaultScoringWeights() ScoringWeights {
	return ScoringWeights{
		Latency:     0.25,
		Uptime:      0.25,
		Performance: 0.30,
		Reliability: 0.20,
	}
}

// NewScoringManager creates a new scoring manager
func NewScoringManager(ctx context.Context, host host.Host, logger *zap.Logger) *ScoringManager {
	ctx, cancel := context.WithCancel(ctx)
	
	sm := &ScoringManager{
		host:         host,
		logger:       logger,
		ctx:          ctx,
		cancel:       cancel,
		peerMetrics:  make(map[string]*PeerMetrics),
		peerScores:   make(map[string]*PeerScore),
		pingInterval: DefaultPingInterval,
		pingTimeout:  DefaultPingTimeout,
		weights:      DefaultScoringWeights(),
	}
	
	// Set up ping protocol handler
	host.SetStreamHandler(PingProtocol, sm.handlePingStream)
	
	return sm
}

// Start begins the scoring system
func (sm *ScoringManager) Start() {
	sm.logger.Info("Starting peer scoring manager")
	
	// Start ping loop
	go sm.pingLoop()
	
	// Start scoring calculation loop
	go sm.scoringLoop()
}

// Stop stops the scoring system
func (sm *ScoringManager) Stop() {
	sm.logger.Info("Stopping peer scoring manager")
	sm.cancel()
}

// AddPeer adds a new peer to the scoring system
func (sm *ScoringManager) AddPeer(peerID string, resource *crowdllama.Resource) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	if metrics, exists := sm.peerMetrics[peerID]; exists {
		metrics.mu.Lock()
		metrics.LastSeen = time.Now()
		metrics.LastResource = resource
		metrics.mu.Unlock()
	} else {
		sm.peerMetrics[peerID] = &PeerMetrics{
			PeerID:           peerID,
			PingHistory:      make([]PingResult, 0),
			UptimeHistory:    make([]UptimeRecord, 0),
			FirstSeen:        time.Now(),
			LastSeen:         time.Now(),
			ConnectionCount:  0,
			DisconnectionCount: 0,
			LastResource:     resource,
		}
	}
	
	// Record uptime event
	sm.recordUptimeEvent(peerID, true)
}

// RemovePeer removes a peer from the scoring system
func (sm *ScoringManager) RemovePeer(peerID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	// Record uptime event
	sm.recordUptimeEvent(peerID, false)
	
	if metrics, exists := sm.peerMetrics[peerID]; exists {
		metrics.mu.Lock()
		metrics.DisconnectionCount++
		metrics.mu.Unlock()
	}
}

// OnPeerConnected handles peer connection events
func (sm *ScoringManager) OnPeerConnected(peerID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	if metrics, exists := sm.peerMetrics[peerID]; exists {
		metrics.mu.Lock()
		metrics.ConnectionCount++
		metrics.LastSeen = time.Now()
		metrics.mu.Unlock()
	}
	
	sm.recordUptimeEvent(peerID, true)
}

// OnPeerDisconnected handles peer disconnection events
func (sm *ScoringManager) OnPeerDisconnected(peerID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	if metrics, exists := sm.peerMetrics[peerID]; exists {
		metrics.mu.Lock()
		metrics.DisconnectionCount++
		metrics.mu.Unlock()
	}
	
	sm.recordUptimeEvent(peerID, false)
}

// GetPeerScore returns the current score for a peer
func (sm *ScoringManager) GetPeerScore(peerID string) (*PeerScore, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	score, exists := sm.peerScores[peerID]
	return score, exists
}

// GetAllPeerScores returns all peer scores
func (sm *ScoringManager) GetAllPeerScores() map[string]*PeerScore {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	scores := make(map[string]*PeerScore)
	for peerID, score := range sm.peerScores {
		scores[peerID] = score
	}
	
	return scores
}

// GetBestPeers returns the top N peers by score
func (sm *ScoringManager) GetBestPeers(n int) []*PeerScore {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	scores := make([]*PeerScore, 0, len(sm.peerScores))
	for _, score := range sm.peerScores {
		scores = append(scores, score)
	}
	
	// Sort by overall score (descending)
	for i := 0; i < len(scores)-1; i++ {
		for j := i + 1; j < len(scores); j++ {
			if scores[i].OverallScore < scores[j].OverallScore {
				scores[i], scores[j] = scores[j], scores[i]
			}
		}
	}
	
	if n > len(scores) {
		n = len(scores)
	}
	
	return scores[:n]
}

// pingLoop runs the ping measurement loop
func (sm *ScoringManager) pingLoop() {
	ticker := time.NewTicker(sm.pingInterval)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			sm.pingAllPeers()
		case <-sm.ctx.Done():
			return
		}
	}
}

// pingAllPeers pings all known peers
func (sm *ScoringManager) pingAllPeers() {
	sm.mu.RLock()
	peerIDs := make([]string, 0, len(sm.peerMetrics))
	for peerID := range sm.peerMetrics {
		peerIDs = append(peerIDs, peerID)
	}
	sm.mu.RUnlock()
	
	for _, peerID := range peerIDs {
		go sm.pingPeer(peerID)
	}
}

// pingPeer pings a specific peer
func (sm *ScoringManager) pingPeer(peerID string) {
	ctx, cancel := context.WithTimeout(sm.ctx, sm.pingTimeout)
	defer cancel()
	
	start := time.Now()
	
	// Convert string to peer.ID
	pid, err := peer.Decode(peerID)
	if err != nil {
		sm.logger.Error("Failed to decode peer ID", zap.String("peer_id", peerID), zap.Error(err))
		return
	}
	
	// Open stream for ping
	stream, err := sm.host.NewStream(ctx, pid, PingProtocol)
	if err != nil {
		sm.recordPingResult(peerID, 0, false, start)
		return
	}
	defer stream.Close()
	
	// Send ping
	if _, err := stream.Write([]byte("ping")); err != nil {
		sm.recordPingResult(peerID, 0, false, start)
		return
	}
	
	// Read pong
	response := make([]byte, 4)
	if _, err := stream.Read(response); err != nil {
		sm.recordPingResult(peerID, 0, false, start)
		return
	}
	
	latency := time.Since(start)
	sm.recordPingResult(peerID, latency, true, start)
}

// handlePingStream handles incoming ping requests
func (sm *ScoringManager) handlePingStream(stream network.Stream) {
	defer stream.Close()
	
	// Read ping
	buffer := make([]byte, 4)
	if _, err := stream.Read(buffer); err != nil {
		return
	}
	
	// Send pong
	if _, err := stream.Write([]byte("pong")); err != nil {
		return
	}
}

// recordPingResult records the result of a ping
func (sm *ScoringManager) recordPingResult(peerID string, latency time.Duration, success bool, timestamp time.Time) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	metrics, exists := sm.peerMetrics[peerID]
	if !exists {
		return
	}
	
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	
	result := PingResult{
		PeerID:    peerID,
		Latency:   latency,
		Success:   success,
		Timestamp: timestamp,
	}
	
	metrics.PingHistory = append(metrics.PingHistory, result)
	
	// Keep only recent history
	if len(metrics.PingHistory) > MaxPingHistory {
		metrics.PingHistory = metrics.PingHistory[1:]
	}
	
	if success {
		sm.logger.Debug("Ping successful", 
			zap.String("peer_id", peerID),
			zap.Duration("latency", latency))
	} else {
		sm.logger.Debug("Ping failed", zap.String("peer_id", peerID))
	}
}

// recordUptimeEvent records an uptime event
func (sm *ScoringManager) recordUptimeEvent(peerID string, online bool) {
	metrics, exists := sm.peerMetrics[peerID]
	if !exists {
		return
	}
	
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	
	record := UptimeRecord{
		PeerID:    peerID,
		Online:    online,
		Timestamp: time.Now(),
	}
	
	metrics.UptimeHistory = append(metrics.UptimeHistory, record)
	
	// Keep only recent history
	if len(metrics.UptimeHistory) > MaxUptimeHistory {
		metrics.UptimeHistory = metrics.UptimeHistory[1:]
	}
}

// scoringLoop runs the scoring calculation loop
func (sm *ScoringManager) scoringLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			sm.calculateAllScores()
		case <-sm.ctx.Done():
			return
		}
	}
}

// calculateAllScores calculates scores for all peers
func (sm *ScoringManager) calculateAllScores() {
	sm.mu.RLock()
	peerIDs := make([]string, 0, len(sm.peerMetrics))
	for peerID := range sm.peerMetrics {
		peerIDs = append(peerIDs, peerID)
	}
	sm.mu.RUnlock()
	
	for _, peerID := range peerIDs {
		score := sm.calculatePeerScore(peerID)
		if score != nil {
			sm.mu.Lock()
			sm.peerScores[peerID] = score
			sm.mu.Unlock()
		}
	}
}

// calculatePeerScore calculates the score for a specific peer
func (sm *ScoringManager) calculatePeerScore(peerID string) *PeerScore {
	sm.mu.RLock()
	metrics, exists := sm.peerMetrics[peerID]
	sm.mu.RUnlock()
	
	if !exists {
		return nil
	}
	
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	
	score := &PeerScore{
		PeerID:      peerID,
		LastUpdated: time.Now(),
	}
	
	// Calculate latency score
	score.LatencyScore, score.AverageLatency = sm.calculateLatencyScore(metrics.PingHistory)
	
	// Calculate uptime score
	score.UptimeScore, score.UptimePercentage = sm.calculateUptimeScore(metrics.UptimeHistory)
	
	// Calculate performance score
	score.PerformanceScore, score.ThroughputRating, score.LoadRating = sm.calculatePerformanceScore(metrics.LastResource)
	
	// Calculate reliability score
	score.ReliabilityScore, score.ConnectionStability = sm.calculateReliabilityScore(metrics)
	
	// Calculate overall score
	score.OverallScore = (score.LatencyScore * sm.weights.Latency) +
		(score.UptimeScore * sm.weights.Uptime) +
		(score.PerformanceScore * sm.weights.Performance) +
		(score.ReliabilityScore * sm.weights.Reliability)
	
	return score
}

// calculateLatencyScore calculates the latency score (0-100)
func (sm *ScoringManager) calculateLatencyScore(pingHistory []PingResult) (float64, time.Duration) {
	if len(pingHistory) == 0 {
		return 0, 0
	}
	
	var totalLatency time.Duration
	successCount := 0
	
	for _, ping := range pingHistory {
		if ping.Success {
			totalLatency += ping.Latency
			successCount++
		}
	}
	
	if successCount == 0 {
		return 0, 0
	}
	
	avgLatency := totalLatency / time.Duration(successCount)
	
	// Score based on latency (lower is better)
	// 10ms = 100 points, 100ms = 50 points, 1s = 0 points
	score := 100.0 - (float64(avgLatency.Milliseconds()) / 10.0)
	if score < 0 {
		score = 0
	}
	
	return score, avgLatency
}

// calculateUptimeScore calculates the uptime score (0-100)
func (sm *ScoringManager) calculateUptimeScore(uptimeHistory []UptimeRecord) (float64, float64) {
	if len(uptimeHistory) == 0 {
		return 0, 0
	}
	
	onlineCount := 0
	for _, record := range uptimeHistory {
		if record.Online {
			onlineCount++
		}
	}
	
	percentage := float64(onlineCount) / float64(len(uptimeHistory)) * 100
	return percentage, percentage
}

// calculatePerformanceScore calculates the performance score (0-100)
func (sm *ScoringManager) calculatePerformanceScore(resource *crowdllama.Resource) (float64, float64, float64) {
	if resource == nil {
		return 0, 0, 0
	}
	
	// Throughput score (higher is better)
	throughputScore := resource.TokensThroughput / 100.0 * 100 // Assume 100 tokens/sec is excellent
	if throughputScore > 100 {
		throughputScore = 100
	}
	
	// Load score (lower is better)
	loadScore := (1.0 - resource.Load) * 100
	
	// Combined performance score
	performanceScore := (throughputScore * 0.7) + (loadScore * 0.3)
	
	return performanceScore, throughputScore, loadScore
}

// calculateReliabilityScore calculates the reliability score (0-100)
func (sm *ScoringManager) calculateReliabilityScore(metrics *PeerMetrics) (float64, float64) {
	if metrics.ConnectionCount == 0 {
		return 0, 0
	}
	
	// Connection stability (fewer disconnections is better)
	stability := 1.0 - (float64(metrics.DisconnectionCount) / float64(metrics.ConnectionCount))
	if stability < 0 {
		stability = 0
	}
	
	score := stability * 100
	return score, stability
}

// GetPeerMetrics returns the metrics for a specific peer
func (sm *ScoringManager) GetPeerMetrics(peerID string) (*PeerMetrics, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	metrics, exists := sm.peerMetrics[peerID]
	return metrics, exists
}

// GetSummaryStats returns summary statistics for the scoring system
func (sm *ScoringManager) GetSummaryStats() map[string]interface{} {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	stats := map[string]interface{}{
		"total_peers":      len(sm.peerMetrics),
		"scored_peers":     len(sm.peerScores),
		"avg_latency":      0.0,
		"avg_uptime":       0.0,
		"avg_performance":  0.0,
		"avg_reliability":  0.0,
		"avg_overall":      0.0,
	}
	
	if len(sm.peerScores) == 0 {
		return stats
	}
	
	var totalLatency, totalUptime, totalPerformance, totalReliability, totalOverall float64
	
	for _, score := range sm.peerScores {
		totalLatency += score.LatencyScore
		totalUptime += score.UptimeScore
		totalPerformance += score.PerformanceScore
		totalReliability += score.ReliabilityScore
		totalOverall += score.OverallScore
	}
	
	count := float64(len(sm.peerScores))
	stats["avg_latency"] = totalLatency / count
	stats["avg_uptime"] = totalUptime / count
	stats["avg_performance"] = totalPerformance / count
	stats["avg_reliability"] = totalReliability / count
	stats["avg_overall"] = totalOverall / count
	
	return stats
}