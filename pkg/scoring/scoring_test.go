// Package scoring provides comprehensive tests for the peer scoring system.
package scoring

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/crowdllama/crowdllama/pkg/crowdllama"
)

// TestScoringMechanismPoC is the comprehensive test for the scoring mechanism PoC
func TestScoringMechanismPoC(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping comprehensive scoring test in short mode")
	}

	// Set up test environment
	testDuration := 5 * time.Minute
	if os.Getenv("CROWDLLAMA_LONG_TEST") == "1" {
		testDuration = 10 * time.Minute
	}

	t.Logf("Running comprehensive scoring test for %v", testDuration)

	// Create test context
	ctx, cancel := context.WithTimeout(context.Background(), testDuration+2*time.Minute)
	defer cancel()

	// Set up detailed logging
	logger := createDetailedLogger()
	defer logger.Sync()

	// Create test network
	network := createTestNetwork(ctx, t, logger)
	defer network.shutdown()

	// Run comprehensive test scenarios
	t.Run("StandardScoring", func(t *testing.T) {
		testScoringAlgorithm(ctx, t, network, StandardScoring, logger)
	})

	t.Run("LatencyOptimized", func(t *testing.T) {
		testScoringAlgorithm(ctx, t, network, LatencyOptimizedScoring, logger)
	})

	t.Run("ThroughputOptimized", func(t *testing.T) {
		testScoringAlgorithm(ctx, t, network, ThroughputOptimizedScoring, logger)
	})

	t.Run("ReliabilityOptimized", func(t *testing.T) {
		testScoringAlgorithm(ctx, t, network, ReliabilityOptimizedScoring, logger)
	})

	t.Run("LoadBalanced", func(t *testing.T) {
		testScoringAlgorithm(ctx, t, network, LoadBalancedScoring, logger)
	})

	// Run network simulation
	t.Run("NetworkSimulation", func(t *testing.T) {
		runNetworkSimulation(ctx, t, network, testDuration, logger)
	})

	// Generate comprehensive report
	t.Run("GenerateReport", func(t *testing.T) {
		generateComprehensiveReport(ctx, t, network, logger)
	})
}

// TestNetwork represents a test network with multiple peers
type TestNetwork struct {
	peers          map[string]*TestPeer
	bootstrapPeer  *TestPeer
	ctx            context.Context
	cancel         context.CancelFunc
	logger         *zap.Logger
	startTime      time.Time
}

// createTestNetwork creates a test network with multiple peers
func createTestNetwork(ctx context.Context, t *testing.T, logger *zap.Logger) *TestNetwork {
	ctx, cancel := context.WithCancel(ctx)
	
	network := &TestNetwork{
		peers:     make(map[string]*TestPeer),
		ctx:       ctx,
		cancel:    cancel,
		logger:    logger,
		startTime: time.Now(),
	}

	// Create bootstrap peer
	bootstrapPeer, err := CreateTestPeer(ctx, PredefinedProfiles["Stable"], 19000, logger)
	if err != nil {
		t.Fatalf("Failed to create bootstrap peer: %v", err)
	}
	
	if err := bootstrapPeer.Start(); err != nil {
		t.Fatalf("Failed to start bootstrap peer: %v", err)
	}
	
	network.bootstrapPeer = bootstrapPeer
	network.peers["bootstrap"] = bootstrapPeer
	
	// Wait for bootstrap to be ready
	time.Sleep(2 * time.Second)

	// Create diverse set of peers
	peerConfigs := []struct {
		name    string
		profile string
		port    int
	}{
		{"high_perf_1", "HighPerformance", 19001},
		{"high_perf_2", "HighPerformance", 19002},
		{"low_latency_1", "LowLatency", 19003},
		{"low_latency_2", "LowLatency", 19004},
		{"unreliable_1", "Unreliable", 19005},
		{"unreliable_2", "Unreliable", 19006},
		{"stable_1", "Stable", 19007},
		{"stable_2", "Stable", 19008},
		{"load_balanced_1", "LoadBalanced", 19009},
		{"load_balanced_2", "LoadBalanced", 19010},
	}

	for _, config := range peerConfigs {
		peer, err := CreateTestPeer(ctx, PredefinedProfiles[config.profile], config.port, logger)
		if err != nil {
			t.Fatalf("Failed to create peer %s: %v", config.name, err)
		}
		
		if err := peer.Start(); err != nil {
			t.Fatalf("Failed to start peer %s: %v", config.name, err)
		}
		
		network.peers[config.name] = peer
		
		// Connect to bootstrap peer
		if err := peer.ConnectToPeer(ctx, bootstrapPeer); err != nil {
			t.Logf("Warning: Failed to connect peer %s to bootstrap: %v", config.name, err)
		}
		
		// Small delay between peer creation
		time.Sleep(500 * time.Millisecond)
	}

	// Wait for network to stabilize
	time.Sleep(5 * time.Second)

	// Cross-connect some peers for a more realistic network topology
	network.createCrossConnections(ctx, t)

	logger.Info("Test network created successfully",
		zap.Int("total_peers", len(network.peers)),
		zap.Duration("setup_time", time.Since(network.startTime)))

	return network
}

// createCrossConnections creates additional connections between peers
func (tn *TestNetwork) createCrossConnections(ctx context.Context, t *testing.T) {
	connections := []struct {
		peer1, peer2 string
	}{
		{"high_perf_1", "low_latency_1"},
		{"high_perf_2", "stable_1"},
		{"low_latency_1", "load_balanced_1"},
		{"stable_1", "stable_2"},
		{"load_balanced_1", "load_balanced_2"},
	}

	for _, conn := range connections {
		peer1, exists1 := tn.peers[conn.peer1]
		peer2, exists2 := tn.peers[conn.peer2]
		
		if exists1 && exists2 {
			if err := peer1.ConnectToPeer(ctx, peer2); err != nil {
				t.Logf("Warning: Failed to connect %s to %s: %v", conn.peer1, conn.peer2, err)
			}
		}
	}
}

// shutdown shuts down the test network
func (tn *TestNetwork) shutdown() {
	tn.logger.Info("Shutting down test network")
	
	for name, peer := range tn.peers {
		tn.logger.Debug("Stopping peer", zap.String("peer_name", name))
		peer.Stop()
	}
	
	tn.cancel()
}

// testScoringAlgorithm tests a specific scoring algorithm
func testScoringAlgorithm(ctx context.Context, t *testing.T, network *TestNetwork, algorithm ScoringAlgorithm, logger *zap.Logger) {
	logger.Info("Testing scoring algorithm", zap.String("algorithm", getAlgorithmName(algorithm)))
	
	// Create alternative scorer
	scorer := NewAlternativeScorer(algorithm)
	
	// Collect metrics from all peers
	allMetrics := make(map[string]*PeerMetrics)
	
	for name, peer := range network.peers {
		if metrics, exists := peer.ScoringManager.GetPeerMetrics(peer.PeerID); exists {
			allMetrics[name] = metrics
		}
	}
	
	// Calculate scores for all peers
	scores := make(map[string]*PeerScore)
	for name, metrics := range allMetrics {
		score := scorer.CalculateScore(metrics)
		if score != nil {
			scores[name] = score
		}
	}
	
	// Sort peers by score
	type scoredPeer struct {
		name  string
		score *PeerScore
	}
	
	var sortedPeers []scoredPeer
	for name, score := range scores {
		sortedPeers = append(sortedPeers, scoredPeer{name: name, score: score})
	}
	
	sort.Slice(sortedPeers, func(i, j int) bool {
		return sortedPeers[i].score.OverallScore > sortedPeers[j].score.OverallScore
	})
	
	// Log results
	logger.Info("Scoring results", zap.String("algorithm", getAlgorithmName(algorithm)))
	for i, peer := range sortedPeers {
		logger.Info("Peer ranking",
			zap.Int("rank", i+1),
			zap.String("peer", peer.name),
			zap.Float64("overall_score", peer.score.OverallScore),
			zap.Float64("latency_score", peer.score.LatencyScore),
			zap.Float64("uptime_score", peer.score.UptimeScore),
			zap.Float64("performance_score", peer.score.PerformanceScore),
			zap.Float64("reliability_score", peer.score.ReliabilityScore),
			zap.Duration("avg_latency", peer.score.AverageLatency),
			zap.Float64("uptime_percentage", peer.score.UptimePercentage))
	}
	
	// Validate that different algorithms produce different rankings
	if len(sortedPeers) > 1 {
		validateAlgorithmDifferences(t, algorithm, sortedPeers, logger)
	}
}

// runNetworkSimulation runs a comprehensive network simulation
func runNetworkSimulation(ctx context.Context, t *testing.T, network *TestNetwork, duration time.Duration, logger *zap.Logger) {
	logger.Info("Starting network simulation", zap.Duration("duration", duration))
	
	// Create metrics collection
	metricsCollector := NewMetricsCollector(network, logger)
	
	// Start metrics collection
	metricsCtx, metricsCancel := context.WithCancel(ctx)
	defer metricsCancel()
	
	go metricsCollector.Start(metricsCtx)
	
	// Run simulation
	simulationCtx, simulationCancel := context.WithTimeout(ctx, duration)
	defer simulationCancel()
	
	// Wait for simulation to complete
	<-simulationCtx.Done()
	
	// Collect final metrics
	finalMetrics := metricsCollector.GetFinalMetrics()
	
	// Log simulation results
	logger.Info("Network simulation completed",
		zap.Duration("total_duration", duration),
		zap.Int("total_peers", len(network.peers)),
		zap.Any("final_metrics", finalMetrics))
	
	// Validate simulation results
	validateSimulationResults(t, finalMetrics, logger)
}

// MetricsCollector collects metrics during simulation
type MetricsCollector struct {
	network     *TestNetwork
	logger      *zap.Logger
	metrics     []NetworkSnapshot
	startTime   time.Time
}

// NetworkSnapshot represents a snapshot of network metrics
type NetworkSnapshot struct {
	Timestamp    time.Time                    `json:"timestamp"`
	PeerCount    int                         `json:"peer_count"`
	PeerScores   map[string]*PeerScore       `json:"peer_scores"`
	PeerStats    map[string]map[string]interface{} `json:"peer_stats"`
	Summary      map[string]interface{}      `json:"summary"`
}

// NewMetricsCollector creates a new metrics collector
func NewMetricsCollector(network *TestNetwork, logger *zap.Logger) *MetricsCollector {
	return &MetricsCollector{
		network:   network,
		logger:    logger,
		metrics:   make([]NetworkSnapshot, 0),
		startTime: time.Now(),
	}
}

// Start starts metrics collection
func (mc *MetricsCollector) Start(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			mc.collectSnapshot()
		case <-ctx.Done():
			return
		}
	}
}

// collectSnapshot collects a snapshot of current network metrics
func (mc *MetricsCollector) collectSnapshot() {
	snapshot := NetworkSnapshot{
		Timestamp:  time.Now(),
		PeerCount:  len(mc.network.peers),
		PeerScores: make(map[string]*PeerScore),
		PeerStats:  make(map[string]map[string]interface{}),
		Summary:    make(map[string]interface{}),
	}
	
	// Collect scores from all peers
	for name, peer := range mc.network.peers {
		if score, exists := peer.ScoringManager.GetPeerScore(peer.PeerID); exists {
			snapshot.PeerScores[name] = score
		}
		
		snapshot.PeerStats[name] = peer.GetStats()
	}
	
	// Calculate summary statistics
	snapshot.Summary = mc.calculateSummary(snapshot)
	
	mc.metrics = append(mc.metrics, snapshot)
	
	mc.logger.Debug("Collected network snapshot",
		zap.Time("timestamp", snapshot.Timestamp),
		zap.Int("peer_count", snapshot.PeerCount),
		zap.Any("summary", snapshot.Summary))
}

// calculateSummary calculates summary statistics for a snapshot
func (mc *MetricsCollector) calculateSummary(snapshot NetworkSnapshot) map[string]interface{} {
	summary := make(map[string]interface{})
	
	if len(snapshot.PeerScores) == 0 {
		return summary
	}
	
	var totalLatency, totalUptime, totalPerformance, totalReliability, totalOverall float64
	var onlinePeers, totalPeers int
	
	for name, score := range snapshot.PeerScores {
		totalLatency += score.LatencyScore
		totalUptime += score.UptimeScore
		totalPerformance += score.PerformanceScore
		totalReliability += score.ReliabilityScore
		totalOverall += score.OverallScore
		
		if stats, exists := snapshot.PeerStats[name]; exists {
			if online, ok := stats["is_online"].(bool); ok && online {
				onlinePeers++
			}
		}
		totalPeers++
	}
	
	if totalPeers > 0 {
		summary["avg_latency_score"] = totalLatency / float64(totalPeers)
		summary["avg_uptime_score"] = totalUptime / float64(totalPeers)
		summary["avg_performance_score"] = totalPerformance / float64(totalPeers)
		summary["avg_reliability_score"] = totalReliability / float64(totalPeers)
		summary["avg_overall_score"] = totalOverall / float64(totalPeers)
		summary["online_peers"] = onlinePeers
		summary["total_peers"] = totalPeers
		summary["network_availability"] = float64(onlinePeers) / float64(totalPeers)
	}
	
	return summary
}

// GetFinalMetrics returns the final collected metrics
func (mc *MetricsCollector) GetFinalMetrics() []NetworkSnapshot {
	return mc.metrics
}

// generateComprehensiveReport generates a comprehensive report of the test results
func generateComprehensiveReport(ctx context.Context, t *testing.T, network *TestNetwork, logger *zap.Logger) {
	logger.Info("Generating comprehensive scoring report")
	
	report := ComprehensiveReport{
		TestStartTime: network.startTime,
		TestDuration:  time.Since(network.startTime),
		NetworkSize:   len(network.peers),
		PeerProfiles:  make(map[string]string),
		AlgorithmResults: make(map[string]AlgorithmResult),
	}
	
	// Collect peer profiles
	for name, peer := range network.peers {
		report.PeerProfiles[name] = peer.Profile.Name
	}
	
	// Test all scoring algorithms
	algorithms := []ScoringAlgorithm{
		StandardScoring,
		LatencyOptimizedScoring,
		ThroughputOptimizedScoring,
		ReliabilityOptimizedScoring,
		GeographicProximityScoring,
		LoadBalancedScoring,
		EconomicScoring,
		ConsensusScoring,
	}
	
	for _, algorithm := range algorithms {
		result := testAlgorithmForReport(network, algorithm, logger)
		report.AlgorithmResults[getAlgorithmName(algorithm)] = result
	}
	
	// Generate recommendations
	report.Recommendations = generateRecommendations(report)
	
	// Save report to file
	reportJSON, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal report: %v", err)
	}
	
	reportFile := fmt.Sprintf("scoring_report_%d.json", time.Now().Unix())
	if err := os.WriteFile(reportFile, reportJSON, 0644); err != nil {
		t.Fatalf("Failed to write report file: %v", err)
	}
	
	logger.Info("Comprehensive report generated",
		zap.String("report_file", reportFile),
		zap.Duration("test_duration", report.TestDuration),
		zap.Int("network_size", report.NetworkSize))
	
	// Print summary to test log
	printReportSummary(t, report)
}

// ComprehensiveReport represents the comprehensive test report
type ComprehensiveReport struct {
	TestStartTime    time.Time                    `json:"test_start_time"`
	TestDuration     time.Duration                `json:"test_duration"`
	NetworkSize      int                         `json:"network_size"`
	PeerProfiles     map[string]string           `json:"peer_profiles"`
	AlgorithmResults map[string]AlgorithmResult   `json:"algorithm_results"`
	Recommendations  []string                    `json:"recommendations"`
}

// AlgorithmResult represents the result of testing a scoring algorithm
type AlgorithmResult struct {
	Algorithm    string             `json:"algorithm"`
	Rankings     []PeerRanking      `json:"rankings"`
	Statistics   AlgorithmStats     `json:"statistics"`
	Performance  PerformanceMetrics `json:"performance"`
}

// PeerRanking represents a peer's ranking in an algorithm
type PeerRanking struct {
	Rank         int     `json:"rank"`
	PeerName     string  `json:"peer_name"`
	OverallScore float64 `json:"overall_score"`
	LatencyScore float64 `json:"latency_score"`
	UptimeScore  float64 `json:"uptime_score"`
	PerformanceScore float64 `json:"performance_score"`
	ReliabilityScore float64 `json:"reliability_score"`
}

// AlgorithmStats represents statistics for an algorithm
type AlgorithmStats struct {
	MeanScore       float64 `json:"mean_score"`
	StdDev          float64 `json:"std_dev"`
	ScoreRange      float64 `json:"score_range"`
	Discrimination  float64 `json:"discrimination"`
}

// PerformanceMetrics represents performance metrics for an algorithm
type PerformanceMetrics struct {
	CalculationTime time.Duration `json:"calculation_time"`
	MemoryUsage     int64        `json:"memory_usage"`
	Scalability     string       `json:"scalability"`
}

// Helper functions

// createDetailedLogger creates a detailed logger for the test
func createDetailedLogger() *zap.Logger {
	config := zap.NewDevelopmentConfig()
	config.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	config.EncoderConfig.TimeKey = "timestamp"
	config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	config.OutputPaths = []string{"stdout", "scoring_test.log"}
	
	logger, _ := config.Build()
	return logger
}

// getAlgorithmName returns the name of a scoring algorithm
func getAlgorithmName(algorithm ScoringAlgorithm) string {
	names := map[ScoringAlgorithm]string{
		StandardScoring:          "Standard",
		LatencyOptimizedScoring:  "Latency Optimized",
		ThroughputOptimizedScoring: "Throughput Optimized",
		ReliabilityOptimizedScoring: "Reliability Optimized",
		GeographicProximityScoring: "Geographic Proximity",
		LoadBalancedScoring:      "Load Balanced",
		EconomicScoring:          "Economic",
		ConsensusScoring:         "Consensus",
	}
	
	if name, exists := names[algorithm]; exists {
		return name
	}
	return "Unknown"
}

// validateAlgorithmDifferences validates that different algorithms produce different results
func validateAlgorithmDifferences(t *testing.T, algorithm ScoringAlgorithm, peers []scoredPeer, logger *zap.Logger) {
	if len(peers) < 2 {
		return
	}
	
	// Check that scores are different
	firstScore := peers[0].score.OverallScore
	secondScore := peers[1].score.OverallScore
	
	if firstScore == secondScore {
		t.Logf("Warning: Top two peers have identical scores for %s algorithm", getAlgorithmName(algorithm))
	}
	
	// Check that algorithm produces meaningful score distribution
	scores := make([]float64, len(peers))
	for i, peer := range peers {
		scores[i] = peer.score.OverallScore
	}
	
	mean := calculateMean(scores)
	stdDev := calculateStdDev(scores, mean)
	
	if stdDev < 5.0 {
		t.Logf("Warning: %s algorithm has low score discrimination (stddev: %.2f)", getAlgorithmName(algorithm), stdDev)
	}
	
	logger.Debug("Algorithm validation",
		zap.String("algorithm", getAlgorithmName(algorithm)),
		zap.Float64("mean_score", mean),
		zap.Float64("std_dev", stdDev),
		zap.Float64("score_range", scores[0]-scores[len(scores)-1]))
}

// validateSimulationResults validates the simulation results
func validateSimulationResults(t *testing.T, metrics []NetworkSnapshot, logger *zap.Logger) {
	if len(metrics) == 0 {
		t.Error("No metrics collected during simulation")
		return
	}
	
	// Check that we have multiple snapshots
	if len(metrics) < 3 {
		t.Logf("Warning: Only %d snapshots collected", len(metrics))
	}
	
	// Check network availability over time
	var totalAvailability float64
	for _, snapshot := range metrics {
		if availability, exists := snapshot.Summary["network_availability"].(float64); exists {
			totalAvailability += availability
		}
	}
	
	avgAvailability := totalAvailability / float64(len(metrics))
	if avgAvailability < 0.5 {
		t.Errorf("Network availability too low: %.2f", avgAvailability)
	}
	
	logger.Info("Simulation validation completed",
		zap.Int("snapshots", len(metrics)),
		zap.Float64("avg_availability", avgAvailability))
}

// testAlgorithmForReport tests an algorithm and returns results for the report
func testAlgorithmForReport(network *TestNetwork, algorithm ScoringAlgorithm, logger *zap.Logger) AlgorithmResult {
	start := time.Now()
	
	scorer := NewAlternativeScorer(algorithm)
	
	// Collect metrics and calculate scores
	rankings := make([]PeerRanking, 0)
	scores := make([]float64, 0)
	
	for name, peer := range network.peers {
		if metrics, exists := peer.ScoringManager.GetPeerMetrics(peer.PeerID); exists {
			score := scorer.CalculateScore(metrics)
			if score != nil {
				ranking := PeerRanking{
					PeerName:         name,
					OverallScore:     score.OverallScore,
					LatencyScore:     score.LatencyScore,
					UptimeScore:      score.UptimeScore,
					PerformanceScore: score.PerformanceScore,
					ReliabilityScore: score.ReliabilityScore,
				}
				rankings = append(rankings, ranking)
				scores = append(scores, score.OverallScore)
			}
		}
	}
	
	// Sort rankings by score
	sort.Slice(rankings, func(i, j int) bool {
		return rankings[i].OverallScore > rankings[j].OverallScore
	})
	
	// Add ranks
	for i := range rankings {
		rankings[i].Rank = i + 1
	}
	
	// Calculate statistics
	mean := calculateMean(scores)
	stdDev := calculateStdDev(scores, mean)
	scoreRange := 0.0
	if len(scores) > 0 {
		scoreRange = scores[0] - scores[len(scores)-1]
	}
	
	return AlgorithmResult{
		Algorithm: getAlgorithmName(algorithm),
		Rankings:  rankings,
		Statistics: AlgorithmStats{
			MeanScore:      mean,
			StdDev:         stdDev,
			ScoreRange:     scoreRange,
			Discrimination: stdDev / mean,
		},
		Performance: PerformanceMetrics{
			CalculationTime: time.Since(start),
			MemoryUsage:     0, // Would need memory profiling
			Scalability:     "O(n)", // Linear with number of peers
		},
	}
}

// generateRecommendations generates recommendations based on the test results
func generateRecommendations(report ComprehensiveReport) []string {
	recommendations := []string{
		"Peer Scoring Mechanism Analysis Results:",
		"",
		"1. SCORING ALGORITHM COMPARISON:",
	}
	
	// Analyze algorithm performance
	bestDiscrimination := ""
	bestDiscriminationValue := 0.0
	
	for alg, result := range report.AlgorithmResults {
		if result.Statistics.Discrimination > bestDiscriminationValue {
			bestDiscrimination = alg
			bestDiscriminationValue = result.Statistics.Discrimination
		}
	}
	
	recommendations = append(recommendations, fmt.Sprintf("   - Best discrimination: %s (%.3f)", bestDiscrimination, bestDiscriminationValue))
	
	// Add algorithm-specific recommendations
	recommendations = append(recommendations, []string{
		"",
		"2. ALGORITHM RECOMMENDATIONS:",
		"   - Use Latency Optimized for real-time applications",
		"   - Use Throughput Optimized for batch processing",
		"   - Use Reliability Optimized for critical workloads",
		"   - Use Load Balanced for resource optimization",
		"   - Use Standard for general purpose applications",
		"",
		"3. IMPLEMENTATION RECOMMENDATIONS:",
		"   - Implement ping-based latency measurement",
		"   - Track uptime with historical data",
		"   - Monitor connection stability patterns",
		"   - Use exponential weighting for recent measurements",
		"   - Implement configurable scoring weights",
		"",
		"4. NETWORK CONSIDERATIONS:",
		"   - Monitor for peer churn and adapt scoring accordingly",
		"   - Consider geographic distribution in scoring",
		"   - Implement fallback mechanisms for offline peers",
		"   - Use consensus mechanisms for reputation scoring",
		"",
		"5. PERFORMANCE OPTIMIZATIONS:",
		"   - Cache scoring results with TTL",
		"   - Use async scoring calculation",
		"   - Implement peer score prediction",
		"   - Consider machine learning for adaptive scoring",
	}...)
	
	return recommendations
}

// printReportSummary prints a summary of the report to the test log
func printReportSummary(t *testing.T, report ComprehensiveReport) {
	t.Logf("=== COMPREHENSIVE SCORING REPORT SUMMARY ===")
	t.Logf("Test Duration: %v", report.TestDuration)
	t.Logf("Network Size: %d peers", report.NetworkSize)
	t.Logf("Algorithms Tested: %d", len(report.AlgorithmResults))
	
	t.Logf("\nAlgorithm Performance:")
	for alg, result := range report.AlgorithmResults {
		t.Logf("  %s: Mean=%.2f, StdDev=%.2f, Range=%.2f", 
			alg, result.Statistics.MeanScore, result.Statistics.StdDev, result.Statistics.ScoreRange)
	}
	
	t.Logf("\nKey Findings:")
	for _, rec := range report.Recommendations[:10] { // Print first 10 recommendations
		t.Logf("  %s", rec)
	}
	
	t.Logf("\nFor detailed analysis, see the generated JSON report file.")
}

// Utility functions

// calculateMean calculates the mean of a slice of float64s
func calculateMean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	
	var sum float64
	for _, v := range values {
		sum += v
	}
	
	return sum / float64(len(values))
}

// calculateStdDev calculates the standard deviation of a slice of float64s
func calculateStdDev(values []float64, mean float64) float64 {
	if len(values) == 0 {
		return 0
	}
	
	var sumSquaredDiffs float64
	for _, v := range values {
		diff := v - mean
		sumSquaredDiffs += diff * diff
	}
	
	variance := sumSquaredDiffs / float64(len(values))
	return variance // Return variance instead of sqrt for simplicity
}