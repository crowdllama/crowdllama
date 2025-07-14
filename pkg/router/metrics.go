package router

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// PingResult represents the result of a ping operation
type PingResult struct {
	Timestamp time.Time     `json:"timestamp"`
	Latency   time.Duration `json:"latency"`
	Success   bool          `json:"success"`
	Error     string        `json:"error,omitempty"`
}

// PeerMetrics holds sliding window metrics for a peer
type PeerMetrics struct {
	PeerID         peer.ID       `json:"peer_id"`
	WindowSize     int           `json:"window_size"`
	TickDuration   time.Duration `json:"tick_duration"`
	
	// Sliding window data
	PingResults    []PingResult  `json:"ping_results"`
	UptimeHistory  []bool        `json:"uptime_history"`
	
	// Cached calculations
	AverageLatency time.Duration `json:"average_latency"`
	UptimePercent  float64       `json:"uptime_percent"`
	LastPingTime   time.Time     `json:"last_ping_time"`
	
	// Connection tracking
	ConnectedAt    time.Time     `json:"connected_at"`
	LastSeenAt     time.Time     `json:"last_seen_at"`
	
	mu sync.RWMutex `json:"-"`
}

// NewPeerMetrics creates a new peer metrics instance
func NewPeerMetrics(peerID peer.ID, windowSize int, tickDuration time.Duration) *PeerMetrics {
	now := time.Now()
	return &PeerMetrics{
		PeerID:         peerID,
		WindowSize:     windowSize,
		TickDuration:   tickDuration,
		PingResults:    make([]PingResult, 0, windowSize),
		UptimeHistory:  make([]bool, 0, windowSize),
		ConnectedAt:    now,
		LastSeenAt:     now,
	}
}

// AddPingResult adds a new ping result to the sliding window
func (pm *PeerMetrics) AddPingResult(result PingResult) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	
	pm.LastPingTime = result.Timestamp
	
	// Add to sliding window
	pm.PingResults = append(pm.PingResults, result)
	pm.UptimeHistory = append(pm.UptimeHistory, result.Success)
	
	// Maintain window size
	if len(pm.PingResults) > pm.WindowSize {
		pm.PingResults = pm.PingResults[1:]
	}
	if len(pm.UptimeHistory) > pm.WindowSize {
		pm.UptimeHistory = pm.UptimeHistory[1:]
	}
	
	// Update last seen time if successful
	if result.Success {
		pm.LastSeenAt = result.Timestamp
	}
	
	// Recalculate metrics
	pm.calculateMetrics()
}

// calculateMetrics recalculates cached metrics (must be called with lock held)
func (pm *PeerMetrics) calculateMetrics() {
	if len(pm.PingResults) == 0 {
		pm.AverageLatency = 0
		pm.UptimePercent = 0
		return
	}
	
	// Calculate average latency (only successful pings)
	var totalLatency time.Duration
	successCount := 0
	
	for _, result := range pm.PingResults {
		if result.Success {
			totalLatency += result.Latency
			successCount++
		}
	}
	
	if successCount > 0 {
		pm.AverageLatency = totalLatency / time.Duration(successCount)
	} else {
		pm.AverageLatency = 0
	}
	
	// Calculate uptime percentage
	upCount := 0
	for _, up := range pm.UptimeHistory {
		if up {
			upCount++
		}
	}
	
	pm.UptimePercent = float64(upCount) / float64(len(pm.UptimeHistory)) * 100
}

// GetMetrics returns a snapshot of current metrics
func (pm *PeerMetrics) GetMetrics() PeerMetricsSnapshot {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	
	return PeerMetricsSnapshot{
		PeerID:         pm.PeerID,
		AverageLatency: pm.AverageLatency,
		UptimePercent:  pm.UptimePercent,
		LastPingTime:   pm.LastPingTime,
		ConnectedAt:    pm.ConnectedAt,
		LastSeenAt:     pm.LastSeenAt,
		WindowSize:     pm.WindowSize,
		DataPoints:     len(pm.PingResults),
		RecentResults:  pm.getRecentResults(10), // Last 10 results
	}
}

// getRecentResults returns the most recent ping results (must be called with lock held)
func (pm *PeerMetrics) getRecentResults(count int) []PingResult {
	if len(pm.PingResults) == 0 {
		return nil
	}
	
	start := len(pm.PingResults) - count
	if start < 0 {
		start = 0
	}
	
	results := make([]PingResult, len(pm.PingResults)-start)
	copy(results, pm.PingResults[start:])
	return results
}

// PeerMetricsSnapshot represents a point-in-time view of peer metrics
type PeerMetricsSnapshot struct {
	PeerID         peer.ID       `json:"peer_id"`
	AverageLatency time.Duration `json:"average_latency"`
	UptimePercent  float64       `json:"uptime_percent"`
	LastPingTime   time.Time     `json:"last_ping_time"`
	ConnectedAt    time.Time     `json:"connected_at"`
	LastSeenAt     time.Time     `json:"last_seen_at"`
	WindowSize     int           `json:"window_size"`
	DataPoints     int           `json:"data_points"`
	RecentResults  []PingResult  `json:"recent_results"`
}

// IsHealthy returns true if the peer is considered healthy based on recent metrics
func (pm *PeerMetrics) IsHealthy(maxLatency time.Duration, minUptimePercent float64) bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	
	// Need at least some data points
	if len(pm.PingResults) < 3 {
		return false
	}
	
	// Check if we've seen the peer recently
	if time.Since(pm.LastSeenAt) > 30*time.Second {
		return false
	}
	
	// Check latency and uptime thresholds
	return pm.AverageLatency <= maxLatency && pm.UptimePercent >= minUptimePercent
}

// RouterMetrics manages metrics for all peers known to a router
type RouterMetrics struct {
	RouterID   peer.ID                    `json:"router_id"`
	StartTime  time.Time                  `json:"start_time"`
	PeerMetrics map[peer.ID]*PeerMetrics `json:"peer_metrics"`
	
	mu sync.RWMutex `json:"-"`
}

// NewRouterMetrics creates a new router metrics instance
func NewRouterMetrics(routerID peer.ID) *RouterMetrics {
	return &RouterMetrics{
		RouterID:    routerID,
		StartTime:   time.Now(),
		PeerMetrics: make(map[peer.ID]*PeerMetrics),
	}
}

// AddPeer adds a new peer to track
func (rm *RouterMetrics) AddPeer(peerID peer.ID, windowSize int, tickDuration time.Duration) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	
	if _, exists := rm.PeerMetrics[peerID]; !exists {
		rm.PeerMetrics[peerID] = NewPeerMetrics(peerID, windowSize, tickDuration)
	}
}

// RemovePeer removes a peer from tracking
func (rm *RouterMetrics) RemovePeer(peerID peer.ID) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	
	delete(rm.PeerMetrics, peerID)
}

// RecordPingResult records a ping result for a peer
func (rm *RouterMetrics) RecordPingResult(peerID peer.ID, result PingResult) {
	rm.mu.RLock()
	peerMetrics, exists := rm.PeerMetrics[peerID]
	rm.mu.RUnlock()
	
	if exists {
		peerMetrics.AddPingResult(result)
	}
}

// GetPeerMetrics returns metrics for a specific peer
func (rm *RouterMetrics) GetPeerMetrics(peerID peer.ID) (PeerMetricsSnapshot, bool) {
	rm.mu.RLock()
	peerMetrics, exists := rm.PeerMetrics[peerID]
	rm.mu.RUnlock()
	
	if !exists {
		return PeerMetricsSnapshot{}, false
	}
	
	return peerMetrics.GetMetrics(), true
}

// GetAllMetrics returns metrics for all peers
func (rm *RouterMetrics) GetAllMetrics() map[peer.ID]PeerMetricsSnapshot {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	
	result := make(map[peer.ID]PeerMetricsSnapshot)
	for peerID, peerMetrics := range rm.PeerMetrics {
		result[peerID] = peerMetrics.GetMetrics()
	}
	
	return result
}

// GetHealthyPeers returns peers that meet health criteria
func (rm *RouterMetrics) GetHealthyPeers(maxLatency time.Duration, minUptimePercent float64) []peer.ID {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	
	var healthy []peer.ID
	for peerID, peerMetrics := range rm.PeerMetrics {
		if peerMetrics.IsHealthy(maxLatency, minUptimePercent) {
			healthy = append(healthy, peerID)
		}
	}
	
	return healthy
}

// GetSummaryJSON returns a JSON summary of all peer metrics
func (rm *RouterMetrics) GetSummaryJSON() ([]byte, error) {
	summary := struct {
		RouterID    peer.ID                         `json:"router_id"`
		StartTime   time.Time                       `json:"start_time"`
		Uptime      time.Duration                   `json:"uptime"`
		TotalPeers  int                            `json:"total_peers"`
		PeerMetrics map[peer.ID]PeerMetricsSnapshot `json:"peer_metrics"`
	}{
		RouterID:    rm.RouterID,
		StartTime:   rm.StartTime,
		Uptime:      time.Since(rm.StartTime),
		TotalPeers:  len(rm.PeerMetrics),
		PeerMetrics: rm.GetAllMetrics(),
	}
	
	return json.MarshalIndent(summary, "", "  ")
}