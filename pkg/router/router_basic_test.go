package router

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestBasicRouterFunctionality tests basic router functionality
func TestBasicRouterFunctionality(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger := createTestLogger()

	// Create two routers
	router1, router2, err := createTwoRouters(ctx, logger)
	require.NoError(t, err)
	defer router1.Stop()
	defer router2.Stop()

	// Start both routers
	err = router1.Start()
	require.NoError(t, err)
	
	err = router2.Start()
	require.NoError(t, err)

	// Connect router2 to router1
	err = router2.GetHost().Connect(ctx, peer.AddrInfo{
		ID:    router1.GetHost().ID(),
		Addrs: router1.GetHost().Addrs(),
	})
	require.NoError(t, err)

	// Wait for connection to establish
	time.Sleep(2 * time.Second)

	// Test that routers can discover each other
	router1KnownPeers := router1.GetKnownPeers()
	router2KnownPeers := router2.GetKnownPeers()

	logger.Info("Router peer discovery results",
		zap.Int("router1_known_peers", len(router1KnownPeers)),
		zap.Int("router2_known_peers", len(router2KnownPeers)))

	// Wait for ping metrics to be collected
	time.Sleep(3 * time.Second)

	// Test metrics collection
	router1Metrics := router1.GetAllPeerMetrics()
	router2Metrics := router2.GetAllPeerMetrics()

	logger.Info("Router metrics results",
		zap.Int("router1_metrics", len(router1Metrics)),
		zap.Int("router2_metrics", len(router2Metrics)))

	// Test JSON summary generation
	summary1, err := router1.GetSummaryJSON()
	require.NoError(t, err)
	assert.NotEmpty(t, summary1)

	summary2, err := router2.GetSummaryJSON()
	require.NoError(t, err)
	assert.NotEmpty(t, summary2)

	logger.Info("Basic router functionality test completed successfully")
}

// TestRouterConfiguration tests router configuration
func TestRouterConfiguration(t *testing.T) {
	// Test default config
	defaultConfig := DefaultConfig()
	assert.Equal(t, 1*time.Second, defaultConfig.PingInterval)
	assert.Equal(t, 30, defaultConfig.MetricsWindowSize)
	assert.Equal(t, 10*time.Second, defaultConfig.DiscoveryInterval)

	// Test test config
	testConfig := TestConfig()
	assert.Equal(t, 1*time.Second, testConfig.PingInterval)
	assert.Equal(t, 2*time.Second, testConfig.DiscoveryInterval)
	assert.Equal(t, 5*time.Second, testConfig.AdvertisingInterval)

	// Test intermittent config
	intermittentConfig := IntermittentTestConfig()
	assert.Equal(t, 3*time.Second, intermittentConfig.PingTimeout)
	assert.Equal(t, 5, intermittentConfig.MaxFailedAttempts)
}

// TestMetricsSystem tests the metrics system
func TestMetricsSystem(t *testing.T) {
	peerID := generateTestPeerID()
	
	// Create metrics instance
	metrics := NewPeerMetrics(peerID, 5, 1*time.Second)
	
	// Add some ping results
	now := time.Now()
	results := []PingResult{
		{Timestamp: now, Latency: 50 * time.Millisecond, Success: true},
		{Timestamp: now.Add(1 * time.Second), Latency: 60 * time.Millisecond, Success: true},
		{Timestamp: now.Add(2 * time.Second), Latency: 0, Success: false, Error: "timeout"},
		{Timestamp: now.Add(3 * time.Second), Latency: 40 * time.Millisecond, Success: true},
		{Timestamp: now.Add(4 * time.Second), Latency: 70 * time.Millisecond, Success: true},
	}
	
	for _, result := range results {
		metrics.AddPingResult(result)
	}
	
	// Test metrics calculation
	snapshot := metrics.GetMetrics()
	assert.Equal(t, peerID, snapshot.PeerID)
	assert.Equal(t, 5, snapshot.DataPoints)
	assert.Equal(t, 80.0, snapshot.UptimePercent) // 4 out of 5 successful
	assert.True(t, snapshot.AverageLatency > 0)   // Should have calculated average
	
	// Test health check
	assert.True(t, metrics.IsHealthy(100*time.Millisecond, 75.0))
	assert.False(t, metrics.IsHealthy(30*time.Millisecond, 75.0)) // Latency too high
}

// createTwoRouters creates two routers for testing
func createTwoRouters(ctx context.Context, logger *zap.Logger) (*Router, *Router, error) {
	// Create first router
	privKey1, _, err := crypto.GenerateKeyPair(crypto.RSA, 2048)
	if err != nil {
		return nil, nil, err
	}

	host1, err := libp2p.New(
		libp2p.Identity(privKey1),
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.DefaultTransports,
		libp2p.DefaultMuxers,
		libp2p.DefaultSecurity,
	)
	if err != nil {
		return nil, nil, err
	}

	dht1, err := dht.New(ctx, host1, dht.Mode(dht.ModeServer))
	if err != nil {
		host1.Close()
		return nil, nil, err
	}

	config1 := TestConfig()
	router1, err := NewRouter(ctx, host1, dht1, config1, logger)
	if err != nil {
		host1.Close()
		return nil, nil, err
	}

	// Create second router
	privKey2, _, err := crypto.GenerateKeyPair(crypto.RSA, 2048)
	if err != nil {
		router1.Stop()
		return nil, nil, err
	}

	host2, err := libp2p.New(
		libp2p.Identity(privKey2),
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.DefaultTransports,
		libp2p.DefaultMuxers,
		libp2p.DefaultSecurity,
	)
	if err != nil {
		router1.Stop()
		return nil, nil, err
	}

	dht2, err := dht.New(ctx, host2, dht.Mode(dht.ModeServer))
	if err != nil {
		host2.Close()
		router1.Stop()
		return nil, nil, err
	}

	config2 := TestConfig()
	router2, err := NewRouter(ctx, host2, dht2, config2, logger)
	if err != nil {
		host2.Close()
		router1.Stop()
		return nil, nil, err
	}

	return router1, router2, nil
}

// generateTestPeerID generates a test peer ID
func generateTestPeerID() peer.ID {
	privKey, _, _ := crypto.GenerateKeyPair(crypto.RSA, 2048)
	peerID, _ := peer.IDFromPrivateKey(privKey)
	return peerID
}