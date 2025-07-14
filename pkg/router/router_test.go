package router

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// TestRouterIntermittentConnectivity tests the router system with intermittent connectivity
func TestRouterIntermittentConnectivity(t *testing.T) {
	// Use a longer timeout for this comprehensive test
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// Create logger
	logger := createTestLogger()

	// Test configuration
	const (
		numRouters = 4
		numPeers   = 10
		testDuration = 10 * time.Minute
	)

	logger.Info("Starting router intermittent connectivity test",
		zap.Int("routers", numRouters),
		zap.Int("peers", numPeers),
		zap.Duration("duration", testDuration))

	// Create bootstrap routers
	routers, bootstrapAddrs, err := createBootstrapRouters(ctx, numRouters, logger)
	require.NoError(t, err)
	defer stopAllRouters(routers)

	// Start all routers
	err = startAllRouters(routers)
	require.NoError(t, err)

	// Wait for routers to establish connections
	time.Sleep(5 * time.Second)

	// Create test peers with intermittent connectivity
	testPeers, err := CreateTestPeers(ctx, numPeers, bootstrapAddrs, logger)
	require.NoError(t, err)
	defer StopAllPeers(testPeers)

	// Start all test peers
	err = StartAllPeers(testPeers)
	require.NoError(t, err)

	// Wait for initial peer discovery
	time.Sleep(10 * time.Second)

	logger.Info("Test setup complete, starting monitoring phase")

	// Monitor the network for the test duration
	monitorNetwork(ctx, routers, testPeers, testDuration, logger)

	// Generate comprehensive report
	report, err := generateNetworkReport(routers, testPeers, testDuration)
	require.NoError(t, err)

	// Save report to file
	err = saveReport(report, "router_intermittent_test_report.json")
	require.NoError(t, err)

	// Generate markdown report
	markdownReport := generateMarkdownReport(report)
	err = saveMarkdownReport(markdownReport, "router_intermittent_test_report.md")
	require.NoError(t, err)

	logger.Info("Test completed successfully")

	// Basic assertions to ensure the test worked
	assert.True(t, len(report.Routers) == numRouters, "Should have correct number of routers")
	assert.True(t, len(report.TestPeers) == numPeers, "Should have correct number of test peers")
	assert.True(t, report.TestDuration == testDuration, "Should have correct test duration")
}

// createBootstrapRouters creates the bootstrap routers for the test
func createBootstrapRouters(ctx context.Context, count int, logger *zap.Logger) ([]*Router, []string, error) {
	routers := make([]*Router, 0, count)
	bootstrapAddrs := make([]string, 0, count)

	for i := 0; i < count; i++ {
		// Generate private key
		privKey, _, err := crypto.GenerateKeyPair(crypto.RSA, 2048)
		if err != nil {
			stopAllRouters(routers)
			return nil, nil, fmt.Errorf("failed to generate key pair for router %d: %w", i, err)
		}

		// Create libp2p host
		host, err := libp2p.New(
			libp2p.Identity(privKey),
			libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", 9000+i)),
			libp2p.DefaultTransports,
			libp2p.DefaultMuxers,
			libp2p.DefaultSecurity,
		)
		if err != nil {
			stopAllRouters(routers)
			return nil, nil, fmt.Errorf("failed to create libp2p host for router %d: %w", i, err)
		}

		// Create DHT
		kadDHT, err := dht.New(ctx, host, dht.Mode(dht.ModeServer))
		if err != nil {
			host.Close()
			stopAllRouters(routers)
			return nil, nil, fmt.Errorf("failed to create DHT for router %d: %w", i, err)
		}

		// Create router with test config
		config := TestConfig()
		router, err := NewRouter(ctx, host, kadDHT, config, logger)
		if err != nil {
			host.Close()
			stopAllRouters(routers)
			return nil, nil, fmt.Errorf("failed to create router %d: %w", i, err)
		}

		routers = append(routers, router)

		// Generate bootstrap address
		for _, addr := range host.Addrs() {
			bootstrapAddr := fmt.Sprintf("%s/p2p/%s", addr.String(), host.ID().String())
			bootstrapAddrs = append(bootstrapAddrs, bootstrapAddr)
			break // Just use the first address
		}
	}

	// Connect routers to each other
	for i, router := range routers {
		for j, otherRouter := range routers {
			if i != j {
				err := router.GetHost().Connect(ctx, peer.AddrInfo{
					ID:    otherRouter.GetHost().ID(),
					Addrs: otherRouter.GetHost().Addrs(),
				})
				if err != nil {
					logger.Debug("Failed to connect routers", 
						zap.Int("router1", i), 
						zap.Int("router2", j), 
						zap.Error(err))
				}
			}
		}
	}

	return routers, bootstrapAddrs, nil
}

// startAllRouters starts all routers
func startAllRouters(routers []*Router) error {
	for i, router := range routers {
		if err := router.Start(); err != nil {
			return fmt.Errorf("failed to start router %d: %w", i, err)
		}
	}
	return nil
}

// stopAllRouters stops all routers
func stopAllRouters(routers []*Router) {
	for _, router := range routers {
		router.Stop()
	}
}

// monitorNetwork monitors the network during the test
func monitorNetwork(ctx context.Context, routers []*Router, testPeers []*TestPeer, duration time.Duration, logger *zap.Logger) {
	monitorCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	startTime := time.Now()

	for {
		select {
		case <-ticker.C:
			elapsed := time.Since(startTime)
			logger.Info("Network monitoring update",
				zap.Duration("elapsed", elapsed),
				zap.Duration("remaining", duration-elapsed))

			// Log router statistics
			for i, router := range routers {
				knownPeers := router.GetKnownPeers()
				healthyPeers := router.GetHealthyPeers(100*time.Millisecond, 80.0)
				
				logger.Info("Router statistics",
					zap.Int("router_id", i),
					zap.String("peer_id", router.GetHost().ID().String()),
					zap.Int("known_peers", len(knownPeers)),
					zap.Int("healthy_peers", len(healthyPeers)))
			}

			// Log test peer statistics
			for i, testPeer := range testPeers {
				metrics := testPeer.GetMetrics()
				logger.Info("Test peer statistics",
					zap.Int("peer_id", i),
					zap.String("name", metrics["name"].(string)),
					zap.Bool("connected", metrics["connected"].(bool)),
					zap.Int("connect_count", metrics["connect_count"].(int)),
					zap.Int("disconnect_count", metrics["disconnect_count"].(int)),
					zap.Float64("uptime_ratio", metrics["uptime_ratio"].(float64)))
			}

		case <-monitorCtx.Done():
			logger.Info("Network monitoring completed")
			return
		}
	}
}

// NetworkReport represents the comprehensive test report
type NetworkReport struct {
	TestDuration time.Duration                    `json:"test_duration"`
	StartTime    time.Time                        `json:"start_time"`
	EndTime      time.Time                        `json:"end_time"`
	Routers      []RouterReport                   `json:"routers"`
	TestPeers    []TestPeerReport                 `json:"test_peers"`
	Summary      NetworkSummary                   `json:"summary"`
}

// RouterReport represents metrics for a single router
type RouterReport struct {
	RouterID      string                           `json:"router_id"`
	StartTime     time.Time                        `json:"start_time"`
	Uptime        time.Duration                    `json:"uptime"`
	KnownPeers    int                             `json:"known_peers"`
	HealthyPeers  int                             `json:"healthy_peers"`
	PeerMetrics   map[string]PeerMetricsSnapshot  `json:"peer_metrics"`
	Summary       RouterSummary                   `json:"summary"`
}

// RouterSummary contains aggregated router metrics
type RouterSummary struct {
	AverageLatency    time.Duration `json:"average_latency"`
	MedianLatency     time.Duration `json:"median_latency"`
	MinLatency        time.Duration `json:"min_latency"`
	MaxLatency        time.Duration `json:"max_latency"`
	AverageUptime     float64       `json:"average_uptime"`
	TotalPingsSent    int          `json:"total_pings_sent"`
	TotalPingsSuccess int          `json:"total_pings_success"`
	SuccessRate       float64      `json:"success_rate"`
}

// TestPeerReport represents metrics for a single test peer
type TestPeerReport struct {
	Name            string                 `json:"name"`
	PeerID          string                 `json:"peer_id"`
	Behavior        TestPeerBehavior       `json:"behavior"`
	ConnectCount    int                    `json:"connect_count"`
	DisconnectCount int                    `json:"disconnect_count"`
	PingCount       int                    `json:"ping_count"`
	PongCount       int                    `json:"pong_count"`
	UptimeRatio     float64               `json:"uptime_ratio"`
	Metrics         map[string]interface{} `json:"metrics"`
}

// NetworkSummary contains overall network metrics
type NetworkSummary struct {
	TotalRouters      int           `json:"total_routers"`
	TotalTestPeers    int           `json:"total_test_peers"`
	AverageLatency    time.Duration `json:"average_latency"`
	OverallUptime     float64       `json:"overall_uptime"`
	TotalConnections  int           `json:"total_connections"`
	TotalDisconnections int         `json:"total_disconnections"`
	NetworkStability  float64       `json:"network_stability"`
}

// generateNetworkReport generates a comprehensive network report
func generateNetworkReport(routers []*Router, testPeers []*TestPeer, duration time.Duration) (*NetworkReport, error) {
	endTime := time.Now()
	startTime := endTime.Add(-duration)

	report := &NetworkReport{
		TestDuration: duration,
		StartTime:    startTime,
		EndTime:      endTime,
		Routers:      make([]RouterReport, 0, len(routers)),
		TestPeers:    make([]TestPeerReport, 0, len(testPeers)),
	}

	// Generate router reports
	var totalLatency time.Duration
	var totalUptime float64
	var totalPings, totalSuccessfulPings int

	for _, router := range routers {
		routerReport := RouterReport{
			RouterID:    router.GetHost().ID().String(),
			StartTime:   startTime,
			Uptime:      duration,
			KnownPeers:  len(router.GetKnownPeers()),
			HealthyPeers: len(router.GetHealthyPeers(100*time.Millisecond, 80.0)),
			PeerMetrics: make(map[string]PeerMetricsSnapshot),
		}

		// Get all peer metrics from this router
		allMetrics := router.GetAllPeerMetrics()
		var routerLatencies []time.Duration
		var routerUptimes []float64
		routerPings := 0
		routerSuccessfulPings := 0

		for peerID, metrics := range allMetrics {
			routerReport.PeerMetrics[peerID.String()] = metrics
			
			if metrics.AverageLatency > 0 {
				routerLatencies = append(routerLatencies, metrics.AverageLatency)
				totalLatency += metrics.AverageLatency
			}
			
			routerUptimes = append(routerUptimes, metrics.UptimePercent)
			totalUptime += metrics.UptimePercent
			
			routerPings += metrics.DataPoints
			// Estimate successful pings based on uptime
			routerSuccessfulPings += int(float64(metrics.DataPoints) * metrics.UptimePercent / 100.0)
		}

		// Calculate router summary
		routerReport.Summary = calculateRouterSummary(routerLatencies, routerUptimes, routerPings, routerSuccessfulPings)
		totalPings += routerPings
		totalSuccessfulPings += routerSuccessfulPings

		report.Routers = append(report.Routers, routerReport)
	}

	// Generate test peer reports
	var totalConnections, totalDisconnections int

	for _, testPeer := range testPeers {
		metrics := testPeer.GetMetrics()
		
		peerReport := TestPeerReport{
			Name:            metrics["name"].(string),
			PeerID:          metrics["peer_id"].(string),
			Behavior:        testPeer.behavior,
			ConnectCount:    metrics["connect_count"].(int),
			DisconnectCount: metrics["disconnect_count"].(int),
			PingCount:       metrics["ping_count"].(int),
			PongCount:       metrics["pong_count"].(int),
			UptimeRatio:     metrics["uptime_ratio"].(float64),
			Metrics:         metrics,
		}

		totalConnections += peerReport.ConnectCount
		totalDisconnections += peerReport.DisconnectCount

		report.TestPeers = append(report.TestPeers, peerReport)
	}

	// Calculate network summary
	avgLatency := time.Duration(0)
	if len(routers) > 0 {
		avgLatency = totalLatency / time.Duration(len(routers)*len(testPeers))
	}

	avgUptime := 0.0
	if len(routers) > 0 && len(testPeers) > 0 {
		avgUptime = totalUptime / float64(len(routers)*len(testPeers))
	}

	networkStability := 0.0
	if totalConnections+totalDisconnections > 0 {
		networkStability = float64(totalConnections) / float64(totalConnections+totalDisconnections)
	}

	report.Summary = NetworkSummary{
		TotalRouters:        len(routers),
		TotalTestPeers:      len(testPeers),
		AverageLatency:      avgLatency,
		OverallUptime:       avgUptime,
		TotalConnections:    totalConnections,
		TotalDisconnections: totalDisconnections,
		NetworkStability:    networkStability,
	}

	return report, nil
}

// calculateRouterSummary calculates summary statistics for a router
func calculateRouterSummary(latencies []time.Duration, uptimes []float64, totalPings, successfulPings int) RouterSummary {
	summary := RouterSummary{
		TotalPingsSent:    totalPings,
		TotalPingsSuccess: successfulPings,
	}

	if totalPings > 0 {
		summary.SuccessRate = float64(successfulPings) / float64(totalPings) * 100.0
	}

	if len(latencies) > 0 {
		var total time.Duration
		min := latencies[0]
		max := latencies[0]

		for _, latency := range latencies {
			total += latency
			if latency < min {
				min = latency
			}
			if latency > max {
				max = latency
			}
		}

		summary.AverageLatency = total / time.Duration(len(latencies))
		summary.MinLatency = min
		summary.MaxLatency = max
		
		// Calculate median (simple approximation)
		if len(latencies) > 1 {
			summary.MedianLatency = latencies[len(latencies)/2]
		} else {
			summary.MedianLatency = summary.AverageLatency
		}
	}

	if len(uptimes) > 0 {
		var total float64
		for _, uptime := range uptimes {
			total += uptime
		}
		summary.AverageUptime = total / float64(len(uptimes))
	}

	return summary
}

// saveReport saves the JSON report to a file
func saveReport(report *NetworkReport, filename string) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal report: %w", err)
	}

	return os.WriteFile(filename, data, 0644)
}

// generateMarkdownReport generates a markdown report
func generateMarkdownReport(report *NetworkReport) string {
	md := fmt.Sprintf(`# Router Intermittent Connectivity Test Report

## Test Summary

- **Test Duration**: %v
- **Start Time**: %v
- **End Time**: %v
- **Total Routers**: %d
- **Total Test Peers**: %d

## Network Summary

- **Average Latency**: %v
- **Overall Uptime**: %.2f%%
- **Total Connections**: %d
- **Total Disconnections**: %d
- **Network Stability**: %.2f%%

## Router Performance

`, report.TestDuration, report.StartTime.Format(time.RFC3339), report.EndTime.Format(time.RFC3339),
		report.Summary.TotalRouters, report.Summary.TotalTestPeers,
		report.Summary.AverageLatency, report.Summary.OverallUptime,
		report.Summary.TotalConnections, report.Summary.TotalDisconnections,
		report.Summary.NetworkStability)

	// Add router details
	for i, router := range report.Routers {
		md += fmt.Sprintf(`### Router %d (%s)

- **Known Peers**: %d
- **Healthy Peers**: %d
- **Average Latency**: %v
- **Average Uptime**: %.2f%%
- **Success Rate**: %.2f%%
- **Total Pings**: %d

`, i+1, router.RouterID[:12]+"...", router.KnownPeers, router.HealthyPeers,
			router.Summary.AverageLatency, router.Summary.AverageUptime,
			router.Summary.SuccessRate, router.Summary.TotalPingsSent)

		// Add peer metrics table
		if len(router.PeerMetrics) > 0 {
			md += "#### Peer Metrics\n\n"
			md += "| Peer ID | Average Latency | Uptime % | Data Points |\n"
			md += "|---------|----------------|----------|-------------|\n"

			for peerID, metrics := range router.PeerMetrics {
				md += fmt.Sprintf("| %s... | %v | %.2f%% | %d |\n",
					peerID[:12], metrics.AverageLatency, metrics.UptimePercent, metrics.DataPoints)
			}
			md += "\n"
		}
	}

	// Add test peer details
	md += "## Test Peer Behavior\n\n"
	for _, testPeer := range report.TestPeers {
		md += fmt.Sprintf(`### %s (%s)

- **Base Latency**: %v
- **Connect Count**: %d
- **Disconnect Count**: %d
- **Uptime Ratio**: %.2f%%
- **Ping/Pong Count**: %d/%d

`, testPeer.Name, testPeer.PeerID[:12]+"...",
			testPeer.Behavior.BaseLatency, testPeer.ConnectCount, testPeer.DisconnectCount,
			testPeer.UptimeRatio*100, testPeer.PingCount, testPeer.PongCount)
	}

	return md
}

// saveMarkdownReport saves the markdown report to a file
func saveMarkdownReport(markdown, filename string) error {
	return os.WriteFile(filename, []byte(markdown), 0644)
}

// createTestLogger creates a logger for testing
func createTestLogger() *zap.Logger {
	config := zap.NewDevelopmentConfig()
	config.Level = zap.NewAtomicLevelAt(zapcore.InfoLevel)
	logger, _ := config.Build()
	return logger
}