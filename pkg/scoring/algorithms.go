// Package scoring provides alternative scoring algorithms for CrowdLlama P2P network.
package scoring

import (
	"math"
	"time"

	"github.com/crowdllama/crowdllama/pkg/crowdllama"
)

// ScoringAlgorithm defines different scoring approaches
type ScoringAlgorithm int

const (
	// StandardScoring uses balanced weighted scoring
	StandardScoring ScoringAlgorithm = iota
	
	// LatencyOptimizedScoring prioritizes low latency peers
	LatencyOptimizedScoring
	
	// ThroughputOptimizedScoring prioritizes high throughput peers
	ThroughputOptimizedScoring
	
	// ReliabilityOptimizedScoring prioritizes stable connections
	ReliabilityOptimizedScoring
	
	// GeographicProximityScoring considers geographic proximity
	GeographicProximityScoring
	
	// LoadBalancedScoring considers current load distribution
	LoadBalancedScoring
	
	// EconomicScoring considers cost/benefit ratios
	EconomicScoring
	
	// ConsensusScoring uses peer-to-peer reputation
	ConsensusScoring
)

// AlternativeScorer provides different scoring algorithms
type AlternativeScorer struct {
	algorithm ScoringAlgorithm
}

// NewAlternativeScorer creates a new alternative scorer
func NewAlternativeScorer(algorithm ScoringAlgorithm) *AlternativeScorer {
	return &AlternativeScorer{
		algorithm: algorithm,
	}
}

// CalculateScore calculates peer score using the specified algorithm
func (as *AlternativeScorer) CalculateScore(metrics *PeerMetrics) *PeerScore {
	switch as.algorithm {
	case LatencyOptimizedScoring:
		return as.calculateLatencyOptimizedScore(metrics)
	case ThroughputOptimizedScoring:
		return as.calculateThroughputOptimizedScore(metrics)
	case ReliabilityOptimizedScoring:
		return as.calculateReliabilityOptimizedScore(metrics)
	case GeographicProximityScoring:
		return as.calculateGeographicProximityScore(metrics)
	case LoadBalancedScoring:
		return as.calculateLoadBalancedScore(metrics)
	case EconomicScoring:
		return as.calculateEconomicScore(metrics)
	case ConsensusScoring:
		return as.calculateConsensusScore(metrics)
	default:
		return as.calculateStandardScore(metrics)
	}
}

// calculateLatencyOptimizedScore prioritizes low latency peers
func (as *AlternativeScorer) calculateLatencyOptimizedScore(metrics *PeerMetrics) *PeerScore {
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	
	score := &PeerScore{
		PeerID:      metrics.PeerID,
		LastUpdated: time.Now(),
	}
	
	// Calculate latency score with exponential weighting (recent pings matter more)
	score.LatencyScore, score.AverageLatency = as.calculateExponentialLatencyScore(metrics.PingHistory)
	
	// Calculate other scores
	score.UptimeScore, score.UptimePercentage = as.calculateUptimeScore(metrics.UptimeHistory)
	score.PerformanceScore, score.ThroughputRating, score.LoadRating = as.calculatePerformanceScore(metrics.LastResource)
	score.ReliabilityScore, score.ConnectionStability = as.calculateReliabilityScore(metrics)
	
	// Latency-optimized weighting
	score.OverallScore = (score.LatencyScore * 0.60) +
		(score.UptimeScore * 0.15) +
		(score.PerformanceScore * 0.15) +
		(score.ReliabilityScore * 0.10)
	
	return score
}

// calculateThroughputOptimizedScore prioritizes high throughput peers
func (as *AlternativeScorer) calculateThroughputOptimizedScore(metrics *PeerMetrics) *PeerScore {
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	
	score := &PeerScore{
		PeerID:      metrics.PeerID,
		LastUpdated: time.Now(),
	}
	
	// Calculate scores
	score.LatencyScore, score.AverageLatency = as.calculateLatencyScore(metrics.PingHistory)
	score.UptimeScore, score.UptimePercentage = as.calculateUptimeScore(metrics.UptimeHistory)
	score.PerformanceScore, score.ThroughputRating, score.LoadRating = as.calculateAdvancedPerformanceScore(metrics.LastResource)
	score.ReliabilityScore, score.ConnectionStability = as.calculateReliabilityScore(metrics)
	
	// Throughput-optimized weighting
	score.OverallScore = (score.LatencyScore * 0.10) +
		(score.UptimeScore * 0.20) +
		(score.PerformanceScore * 0.55) +
		(score.ReliabilityScore * 0.15)
	
	return score
}

// calculateReliabilityOptimizedScore prioritizes stable connections
func (as *AlternativeScorer) calculateReliabilityOptimizedScore(metrics *PeerMetrics) *PeerScore {
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	
	score := &PeerScore{
		PeerID:      metrics.PeerID,
		LastUpdated: time.Now(),
	}
	
	// Calculate scores
	score.LatencyScore, score.AverageLatency = as.calculateLatencyScore(metrics.PingHistory)
	score.UptimeScore, score.UptimePercentage = as.calculateAdvancedUptimeScore(metrics.UptimeHistory)
	score.PerformanceScore, score.ThroughputRating, score.LoadRating = as.calculatePerformanceScore(metrics.LastResource)
	score.ReliabilityScore, score.ConnectionStability = as.calculateAdvancedReliabilityScore(metrics)
	
	// Reliability-optimized weighting
	score.OverallScore = (score.LatencyScore * 0.15) +
		(score.UptimeScore * 0.35) +
		(score.PerformanceScore * 0.20) +
		(score.ReliabilityScore * 0.30)
	
	return score
}

// calculateGeographicProximityScore considers geographic proximity
func (as *AlternativeScorer) calculateGeographicProximityScore(metrics *PeerMetrics) *PeerScore {
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	
	score := &PeerScore{
		PeerID:      metrics.PeerID,
		LastUpdated: time.Now(),
	}
	
	// Calculate base scores
	score.LatencyScore, score.AverageLatency = as.calculateLatencyScore(metrics.PingHistory)
	score.UptimeScore, score.UptimePercentage = as.calculateUptimeScore(metrics.UptimeHistory)
	score.PerformanceScore, score.ThroughputRating, score.LoadRating = as.calculatePerformanceScore(metrics.LastResource)
	score.ReliabilityScore, score.ConnectionStability = as.calculateReliabilityScore(metrics)
	
	// Geographic proximity score (estimated from latency patterns)
	proximityScore := as.calculateProximityScore(metrics.PingHistory)
	
	// Geographic-optimized weighting
	score.OverallScore = (score.LatencyScore * 0.20) +
		(score.UptimeScore * 0.20) +
		(score.PerformanceScore * 0.25) +
		(score.ReliabilityScore * 0.15) +
		(proximityScore * 0.20)
	
	return score
}

// calculateLoadBalancedScore considers current load distribution
func (as *AlternativeScorer) calculateLoadBalancedScore(metrics *PeerMetrics) *PeerScore {
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	
	score := &PeerScore{
		PeerID:      metrics.PeerID,
		LastUpdated: time.Now(),
	}
	
	// Calculate base scores
	score.LatencyScore, score.AverageLatency = as.calculateLatencyScore(metrics.PingHistory)
	score.UptimeScore, score.UptimePercentage = as.calculateUptimeScore(metrics.UptimeHistory)
	score.PerformanceScore, score.ThroughputRating, score.LoadRating = as.calculatePerformanceScore(metrics.LastResource)
	score.ReliabilityScore, score.ConnectionStability = as.calculateReliabilityScore(metrics)
	
	// Load balancing bonus (lower current load gets bonus)
	loadBalanceScore := as.calculateLoadBalanceScore(metrics.LastResource)
	
	// Load-balanced weighting
	score.OverallScore = (score.LatencyScore * 0.20) +
		(score.UptimeScore * 0.20) +
		(score.PerformanceScore * 0.20) +
		(score.ReliabilityScore * 0.20) +
		(loadBalanceScore * 0.20)
	
	return score
}

// calculateEconomicScore considers cost/benefit ratios
func (as *AlternativeScorer) calculateEconomicScore(metrics *PeerMetrics) *PeerScore {
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	
	score := &PeerScore{
		PeerID:      metrics.PeerID,
		LastUpdated: time.Now(),
	}
	
	// Calculate base scores
	score.LatencyScore, score.AverageLatency = as.calculateLatencyScore(metrics.PingHistory)
	score.UptimeScore, score.UptimePercentage = as.calculateUptimeScore(metrics.UptimeHistory)
	score.PerformanceScore, score.ThroughputRating, score.LoadRating = as.calculatePerformanceScore(metrics.LastResource)
	score.ReliabilityScore, score.ConnectionStability = as.calculateReliabilityScore(metrics)
	
	// Economic efficiency score (performance per "cost" unit)
	economicScore := as.calculateEconomicEfficiencyScore(metrics)
	
	// Economic-optimized weighting
	score.OverallScore = (score.LatencyScore * 0.15) +
		(score.UptimeScore * 0.20) +
		(score.PerformanceScore * 0.25) +
		(score.ReliabilityScore * 0.20) +
		(economicScore * 0.20)
	
	return score
}

// calculateConsensusScore uses peer-to-peer reputation
func (as *AlternativeScorer) calculateConsensusScore(metrics *PeerMetrics) *PeerScore {
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	
	score := &PeerScore{
		PeerID:      metrics.PeerID,
		LastUpdated: time.Now(),
	}
	
	// Calculate base scores
	score.LatencyScore, score.AverageLatency = as.calculateLatencyScore(metrics.PingHistory)
	score.UptimeScore, score.UptimePercentage = as.calculateUptimeScore(metrics.UptimeHistory)
	score.PerformanceScore, score.ThroughputRating, score.LoadRating = as.calculatePerformanceScore(metrics.LastResource)
	score.ReliabilityScore, score.ConnectionStability = as.calculateReliabilityScore(metrics)
	
	// Consensus reputation score (based on peer behavior patterns)
	consensusScore := as.calculateConsensusReputationScore(metrics)
	
	// Consensus-optimized weighting
	score.OverallScore = (score.LatencyScore * 0.15) +
		(score.UptimeScore * 0.25) +
		(score.PerformanceScore * 0.25) +
		(score.ReliabilityScore * 0.15) +
		(consensusScore * 0.20)
	
	return score
}

// calculateStandardScore uses balanced weighted scoring
func (as *AlternativeScorer) calculateStandardScore(metrics *PeerMetrics) *PeerScore {
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	
	score := &PeerScore{
		PeerID:      metrics.PeerID,
		LastUpdated: time.Now(),
	}
	
	// Calculate standard scores
	score.LatencyScore, score.AverageLatency = as.calculateLatencyScore(metrics.PingHistory)
	score.UptimeScore, score.UptimePercentage = as.calculateUptimeScore(metrics.UptimeHistory)
	score.PerformanceScore, score.ThroughputRating, score.LoadRating = as.calculatePerformanceScore(metrics.LastResource)
	score.ReliabilityScore, score.ConnectionStability = as.calculateReliabilityScore(metrics)
	
	// Standard balanced weighting
	score.OverallScore = (score.LatencyScore * 0.25) +
		(score.UptimeScore * 0.25) +
		(score.PerformanceScore * 0.30) +
		(score.ReliabilityScore * 0.20)
	
	return score
}

// Helper methods for advanced scoring calculations

// calculateExponentialLatencyScore gives more weight to recent pings
func (as *AlternativeScorer) calculateExponentialLatencyScore(pingHistory []PingResult) (float64, time.Duration) {
	if len(pingHistory) == 0 {
		return 0, 0
	}
	
	var weightedLatency float64
	var totalWeight float64
	successCount := 0
	
	for i, ping := range pingHistory {
		if ping.Success {
			// Exponential weight favoring recent pings
			weight := math.Pow(0.9, float64(len(pingHistory)-i-1))
			weightedLatency += float64(ping.Latency.Milliseconds()) * weight
			totalWeight += weight
			successCount++
		}
	}
	
	if successCount == 0 || totalWeight == 0 {
		return 0, 0
	}
	
	avgLatency := time.Duration(weightedLatency/totalWeight) * time.Millisecond
	
	// Enhanced scoring curve for latency optimization
	score := 100.0 - (float64(avgLatency.Milliseconds()) / 5.0)
	if score < 0 {
		score = 0
	}
	
	return score, avgLatency
}

// calculateAdvancedPerformanceScore considers GPU utilization trends
func (as *AlternativeScorer) calculateAdvancedPerformanceScore(resource *crowdllama.Resource) (float64, float64, float64) {
	if resource == nil {
		return 0, 0, 0
	}
	
	// Enhanced throughput scoring with GPU model considerations
	baseScore := resource.TokensThroughput / 100.0 * 100
	
	// GPU model multiplier (rough estimates)
	gpuMultiplier := 1.0
	switch resource.GPUModel {
	case "RTX 4090":
		gpuMultiplier = 1.2
	case "RTX 4080":
		gpuMultiplier = 1.1
	case "RTX 3090":
		gpuMultiplier = 1.0
	case "RTX 3080":
		gpuMultiplier = 0.9
	}
	
	throughputScore := baseScore * gpuMultiplier
	if throughputScore > 100 {
		throughputScore = 100
	}
	
	// Advanced load scoring with saturation curve
	loadScore := math.Pow(1.0-resource.Load, 2) * 100
	
	// Combined performance with GPU efficiency
	performanceScore := (throughputScore * 0.8) + (loadScore * 0.2)
	
	return performanceScore, throughputScore, loadScore
}

// calculateAdvancedUptimeScore considers uptime trends and patterns
func (as *AlternativeScorer) calculateAdvancedUptimeScore(uptimeHistory []UptimeRecord) (float64, float64) {
	if len(uptimeHistory) == 0 {
		return 0, 0
	}
	
	// Calculate uptime percentage with recency weighting
	var weightedUptime float64
	var totalWeight float64
	
	for i, record := range uptimeHistory {
		weight := math.Pow(0.95, float64(len(uptimeHistory)-i-1))
		if record.Online {
			weightedUptime += weight
		}
		totalWeight += weight
	}
	
	if totalWeight == 0 {
		return 0, 0
	}
	
	percentage := (weightedUptime / totalWeight) * 100
	
	// Bonus for consistent uptime patterns
	consistencyBonus := as.calculateUptimeConsistency(uptimeHistory)
	score := percentage + consistencyBonus
	
	if score > 100 {
		score = 100
	}
	
	return score, percentage
}

// calculateAdvancedReliabilityScore considers connection patterns
func (as *AlternativeScorer) calculateAdvancedReliabilityScore(metrics *PeerMetrics) (float64, float64) {
	if metrics.ConnectionCount == 0 {
		return 0, 0
	}
	
	// Base stability
	stability := 1.0 - (float64(metrics.DisconnectionCount) / float64(metrics.ConnectionCount))
	if stability < 0 {
		stability = 0
	}
	
	// Longevity bonus (longer-lived peers get bonus)
	longevityBonus := as.calculateLongevityBonus(metrics.FirstSeen)
	
	// Connection frequency penalty (too many connections might indicate instability)
	connectionFrequency := float64(metrics.ConnectionCount) / time.Since(metrics.FirstSeen).Hours()
	frequencyPenalty := math.Min(connectionFrequency*0.1, 0.2)
	
	finalStability := stability + longevityBonus - frequencyPenalty
	if finalStability < 0 {
		finalStability = 0
	}
	
	score := finalStability * 100
	return score, finalStability
}

// calculateProximityScore estimates geographic proximity from latency patterns
func (as *AlternativeScorer) calculateProximityScore(pingHistory []PingResult) float64 {
	if len(pingHistory) == 0 {
		return 0
	}
	
	// Calculate latency variance (lower variance suggests closer proximity)
	var latencies []float64
	for _, ping := range pingHistory {
		if ping.Success {
			latencies = append(latencies, float64(ping.Latency.Milliseconds()))
		}
	}
	
	if len(latencies) == 0 {
		return 0
	}
	
	// Calculate mean
	var sum float64
	for _, lat := range latencies {
		sum += lat
	}
	mean := sum / float64(len(latencies))
	
	// Calculate variance
	var variance float64
	for _, lat := range latencies {
		variance += math.Pow(lat-mean, 2)
	}
	variance /= float64(len(latencies))
	
	// Lower variance = higher proximity score
	proximityScore := 100.0 - math.Min(variance, 100.0)
	
	return proximityScore
}

// calculateLoadBalanceScore gives bonus to less loaded peers
func (as *AlternativeScorer) calculateLoadBalanceScore(resource *crowdllama.Resource) float64 {
	if resource == nil {
		return 0
	}
	
	// Exponential bonus for low load
	loadBonus := math.Pow(1.0-resource.Load, 3) * 100
	
	return loadBonus
}

// calculateEconomicEfficiencyScore considers performance per resource unit
func (as *AlternativeScorer) calculateEconomicEfficiencyScore(metrics *PeerMetrics) float64 {
	if metrics.LastResource == nil {
		return 0
	}
	
	resource := metrics.LastResource
	
	// Simple efficiency: throughput per VRAM unit
	efficiency := resource.TokensThroughput / math.Max(float64(resource.VRAMGB), 1.0)
	
	// Normalize to 0-100 scale
	score := math.Min(efficiency*10, 100)
	
	return score
}

// calculateConsensusReputationScore based on peer behavior patterns
func (as *AlternativeScorer) calculateConsensusReputationScore(metrics *PeerMetrics) float64 {
	// Simplified reputation based on connection patterns and uptime
	var reputationScore float64
	
	// Longevity bonus
	longevityBonus := as.calculateLongevityBonus(metrics.FirstSeen)
	
	// Stability bonus
	stabilityBonus := 0.0
	if metrics.ConnectionCount > 0 {
		stabilityBonus = (1.0 - float64(metrics.DisconnectionCount)/float64(metrics.ConnectionCount)) * 50
	}
	
	reputationScore = longevityBonus + stabilityBonus
	
	if reputationScore > 100 {
		reputationScore = 100
	}
	
	return reputationScore
}

// calculateLongevityBonus gives bonus for long-lived peers
func (as *AlternativeScorer) calculateLongevityBonus(firstSeen time.Time) float64 {
	age := time.Since(firstSeen)
	
	// Bonus increases with age, capped at 20 points
	bonus := math.Min(age.Hours()/24.0, 20.0) // 1 point per day, max 20
	
	return bonus
}

// calculateUptimeConsistency gives bonus for consistent uptime patterns
func (as *AlternativeScorer) calculateUptimeConsistency(uptimeHistory []UptimeRecord) float64 {
	if len(uptimeHistory) < 10 {
		return 0
	}
	
	// Count consecutive uptime periods
	consecutiveUptime := 0
	maxConsecutive := 0
	
	for _, record := range uptimeHistory {
		if record.Online {
			consecutiveUptime++
			if consecutiveUptime > maxConsecutive {
				maxConsecutive = consecutiveUptime
			}
		} else {
			consecutiveUptime = 0
		}
	}
	
	// Bonus for long consecutive uptime periods
	consistencyBonus := math.Min(float64(maxConsecutive)/float64(len(uptimeHistory))*50, 10)
	
	return consistencyBonus
}

// Standard calculation methods (shared between approaches)

func (as *AlternativeScorer) calculateLatencyScore(pingHistory []PingResult) (float64, time.Duration) {
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
	score := 100.0 - (float64(avgLatency.Milliseconds()) / 10.0)
	if score < 0 {
		score = 0
	}
	
	return score, avgLatency
}

func (as *AlternativeScorer) calculateUptimeScore(uptimeHistory []UptimeRecord) (float64, float64) {
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

func (as *AlternativeScorer) calculatePerformanceScore(resource *crowdllama.Resource) (float64, float64, float64) {
	if resource == nil {
		return 0, 0, 0
	}
	
	// Throughput score (higher is better)
	throughputScore := resource.TokensThroughput / 100.0 * 100
	if throughputScore > 100 {
		throughputScore = 100
	}
	
	// Load score (lower is better)
	loadScore := (1.0 - resource.Load) * 100
	
	// Combined performance score
	performanceScore := (throughputScore * 0.7) + (loadScore * 0.3)
	
	return performanceScore, throughputScore, loadScore
}

func (as *AlternativeScorer) calculateReliabilityScore(metrics *PeerMetrics) (float64, float64) {
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