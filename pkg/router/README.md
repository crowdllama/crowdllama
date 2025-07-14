# Router Package

The router package provides a comprehensive DHT-based routing system with advanced ping monitoring, sliding window metrics, and configurable intervals for the CrowdLlama P2P network.

## Features

### Core Components

- **Router**: Main component that combines DHT functionality with ping monitoring
- **Metrics System**: Sliding window algorithm for latency and uptime tracking
- **Configurable Intervals**: All timing settings are programmatically configurable
- **Test Helpers**: Comprehensive testing infrastructure with intermittent connectivity simulation

### Key Capabilities

- **Real-time Ping Monitoring**: Pings all known peers every 1 second with configurable intervals
- **Sliding Window Metrics**: Maintains 30-tick sliding window of latency and uptime data
- **Intermittent Connectivity Simulation**: Test peers with realistic network behavior patterns
- **Comprehensive Reporting**: JSON and Markdown reports with detailed performance metrics

## Architecture

```
┌─────────────┐    ┌─────────────┐    ┌─────────────┐
│   Router    │    │   Metrics   │    │ TestHelpers │
│             │    │             │    │             │
│ • DHT       │◄──►│ • Sliding   │    │ • Behaviors │
│ • Ping      │    │   Window    │    │ • Simulation│
│ • Discovery │    │ • Latency   │    │ • Patterns  │
│ • Config    │    │ • Uptime    │    │ • Reports   │
└─────────────┘    └─────────────┘    └─────────────┘
```

## Configuration

### Router Config

```go
type Config struct {
    // Ping monitoring settings
    PingInterval        time.Duration // How often to ping peers (default: 1s)
    PingTimeout         time.Duration // Timeout for ping responses (default: 5s)
    MetricsWindowSize   int           // Number of ticks in sliding window (default: 30)
    MetricsTickDuration time.Duration // Duration of each tick (default: 1s)

    // DHT settings
    DiscoveryInterval      time.Duration // How often to discover peers (default: 10s)
    AdvertisingInterval    time.Duration // How often to advertise (default: 30s)
    MetadataUpdateInterval time.Duration // How often to update metadata (default: 60s)

    // Peer health settings
    StalePeerTimeout    time.Duration // When to consider peer stale (default: 1m)
    HealthCheckInterval time.Duration // How often to health check (default: 20s)
    MaxFailedAttempts   int           // Max failures before unhealthy (default: 3)
    BackoffBase         time.Duration // Base backoff time (default: 10s)
    MetadataTimeout     time.Duration // Timeout for metadata requests (default: 5s)
    MaxMetadataAge      time.Duration // Max age for valid metadata (default: 1m)

    // Connection settings
    ConnectionTimeout  time.Duration // Timeout for connections (default: 10s)
    MaxConcurrentPings int          // Max concurrent pings (default: 10)
}
```

### Predefined Configurations

- **DefaultConfig()**: Production-optimized settings
- **TestConfig()**: Fast intervals for testing
- **IntermittentTestConfig()**: Optimized for intermittent connectivity testing

## Usage

### Basic Router Setup

```go
import (
    "context"
    "github.com/crowdllama/crowdllama/pkg/router"
    "go.uber.org/zap"
)

// Create router with default config
config := router.DefaultConfig()
router, err := router.NewRouter(ctx, host, dht, config, logger)
if err != nil {
    log.Fatal(err)
}

// Start router
err = router.Start()
if err != nil {
    log.Fatal(err)
}

// Get peer metrics
metrics := router.GetAllPeerMetrics()
for peerID, metric := range metrics {
    log.Printf("Peer %s: latency=%v, uptime=%.2f%%", 
        peerID, metric.AverageLatency, metric.UptimePercent)
}
```

### Custom Configuration

```go
// Create custom config
config := &router.Config{
    PingInterval:        500 * time.Millisecond, // Ping every 500ms
    PingTimeout:         2 * time.Second,        // 2s timeout
    MetricsWindowSize:   60,                     // 60-tick window
    DiscoveryInterval:   5 * time.Second,        // Discover every 5s
    AdvertisingInterval: 15 * time.Second,       // Advertise every 15s
    // ... other settings
}

router, err := router.NewRouter(ctx, host, dht, config, logger)
```

### Metrics and Monitoring

```go
// Get metrics for specific peer
peerMetrics, exists := router.GetPeerMetrics(peerID)
if exists {
    fmt.Printf("Average Latency: %v\n", peerMetrics.AverageLatency)
    fmt.Printf("Uptime: %.2f%%\n", peerMetrics.UptimePercent)
    fmt.Printf("Data Points: %d\n", peerMetrics.DataPoints)
}

// Get healthy peers
healthyPeers := router.GetHealthyPeers(100*time.Millisecond, 80.0)
fmt.Printf("Found %d healthy peers\n", len(healthyPeers))

// Get JSON summary
jsonData, err := router.GetSummaryJSON()
if err == nil {
    fmt.Println(string(jsonData))
}
```

## Testing

### Intermittent Connectivity Test

The package includes a comprehensive test scenario that simulates real-world network conditions:

```bash
cd pkg/router
go test -v -timeout=20m -run TestRouterIntermittentConnectivity
```

### Test Scenario

- **4 Bootstrap Routers**: Act as DHT bootstrap nodes
- **10 Test Peers**: Simulate various network behaviors
- **10-15 Minute Duration**: Long-running realistic test
- **Intermittent Connectivity**: Peers randomly disconnect/reconnect
- **Latency Simulation**: Variable response times and packet loss

### Test Peer Behaviors

1. **Stable Peer**: 50ms latency, 1% disconnect probability, 1% packet loss
2. **Unstable Peer**: 100ms latency, 10% disconnect probability, 5% packet loss
3. **Intermittent Peer**: Fixed 45s connected / 15s disconnected pattern
4. **Slow Peer**: 500ms latency, additional 1s response delay
5. **Fast Peer**: 10ms latency, very reliable connection

### Creating Custom Test Peers

```go
// Define custom behavior
behavior := router.TestPeerBehavior{
    Name:                  "Custom Peer",
    BaseLatency:           75 * time.Millisecond,
    LatencyVariation:      25 * time.Millisecond,
    DisconnectProbability: 0.05, // 5% per minute
    ReconnectDelay:        8 * time.Second,
    PacketLoss:            0.02, // 2% packet loss
}

// Create test peer
testPeer, err := router.NewTestPeer(ctx, behavior, bootstrapAddrs, logger)
if err != nil {
    log.Fatal(err)
}

// Start peer
err = testPeer.Start()
if err != nil {
    log.Fatal(err)
}
```

## Metrics System

### Sliding Window Algorithm

The metrics system uses a sliding window approach to track peer performance:

- **Window Size**: Configurable number of ticks (default: 30)
- **Tick Duration**: Time per tick (default: 1 second)
- **Data Retention**: Automatically maintains window size
- **Real-time Calculation**: Metrics updated on every ping result

### Tracked Metrics

```go
type PeerMetricsSnapshot struct {
    PeerID         peer.ID       // Peer identifier
    AverageLatency time.Duration // Average latency over window
    UptimePercent  float64       // Uptime percentage
    LastPingTime   time.Time     // Last ping attempt
    ConnectedAt    time.Time     // When peer was first seen
    LastSeenAt     time.Time     // Last successful ping
    WindowSize     int           // Size of sliding window
    DataPoints     int           // Number of data points
    RecentResults  []PingResult  // Recent ping results
}
```

### Health Determination

Peers are considered healthy if:
- Average latency ≤ threshold (configurable)
- Uptime percentage ≥ threshold (configurable)
- At least 3 data points available
- Seen within last 30 seconds

## Report Generation

### JSON Report

The system generates comprehensive JSON reports with:

```json
{
  "test_duration": "10m0s",
  "start_time": "2024-01-15T10:00:00Z",
  "end_time": "2024-01-15T10:10:00Z",
  "routers": [
    {
      "router_id": "12D3KooW...",
      "known_peers": 10,
      "healthy_peers": 8,
      "summary": {
        "average_latency": "75ms",
        "average_uptime": 85.5,
        "success_rate": 92.3
      },
      "peer_metrics": {
        "12D3KooW...": {
          "average_latency": "50ms",
          "uptime_percent": 95.0,
          "data_points": 600
        }
      }
    }
  ],
  "test_peers": [...],
  "summary": {
    "total_routers": 4,
    "total_test_peers": 10,
    "average_latency": "78ms",
    "overall_uptime": 87.2,
    "network_stability": 0.89
  }
}
```

### Markdown Report

Automatically generates human-readable markdown reports:

```markdown
# Router Intermittent Connectivity Test Report

## Test Summary
- **Test Duration**: 10m0s
- **Total Routers**: 4
- **Total Test Peers**: 10

## Network Summary
- **Average Latency**: 78ms
- **Overall Uptime**: 87.20%
- **Network Stability**: 89.00%

## Router Performance
### Router 1 (12D3KooW...)
- **Known Peers**: 10
- **Healthy Peers**: 8
- **Average Latency**: 75ms
- **Success Rate**: 92.30%
```

## Integration with Existing Code

### Refactored DHT Integration

The router package integrates seamlessly with existing DHT code:

```go
// Old way - fixed intervals
discovery.SetTestMode() // Only test/production modes

// New way - fully configurable
config := router.TestConfig()
config.DiscoveryInterval = 3 * time.Second
config.AdvertisingInterval = 8 * time.Second
router, err := router.NewRouter(ctx, host, dht, config, logger)
```

### Peer Manager Integration

The router uses the existing peer manager with configurable intervals:

```go
// Router automatically configures peer manager
pmConfig := &peermanager.Config{
    DiscoveryInterval:      config.DiscoveryInterval,
    AdvertisingInterval:    config.AdvertisingInterval,
    MetadataUpdateInterval: config.MetadataUpdateInterval,
    // ... health config from router config
}
```

## Performance Considerations

### Memory Usage

- **Sliding Window**: O(window_size) per peer
- **Default Window**: 30 ticks × number of peers
- **Automatic Cleanup**: Old data automatically removed

### CPU Usage

- **Ping Frequency**: 1 second intervals (configurable)
- **Concurrent Pings**: Limited by semaphore (default: 10)
- **Metric Calculation**: O(window_size) per update

### Network Usage

- **Ping Protocol**: Lightweight custom protocol
- **Bandwidth**: ~100 bytes per ping/pong
- **Rate Limiting**: Configurable concurrent ping limit

## Best Practices

### Production Deployment

1. **Use DefaultConfig()** for production settings
2. **Monitor metrics** regularly via GetSummaryJSON()
3. **Set appropriate timeouts** for your network conditions
4. **Configure health thresholds** based on requirements

### Testing

1. **Use TestConfig()** for faster test cycles
2. **Create diverse test peers** with different behaviors
3. **Run long-duration tests** for realistic results
4. **Analyze reports** for performance insights

### Monitoring

1. **Set up periodic reporting** to track network health
2. **Monitor latency trends** over time
3. **Track uptime patterns** to identify issues
4. **Use health checks** for automated peer selection

## Troubleshooting

### Common Issues

1. **High Latency**: Check network conditions, increase timeouts
2. **Low Uptime**: Investigate connectivity issues, adjust thresholds
3. **Memory Usage**: Reduce window size or ping frequency
4. **CPU Usage**: Limit concurrent pings, increase intervals

### Debug Logging

Enable debug logging to see detailed ping operations:

```go
logger := zap.NewDevelopment()
router, err := router.NewRouter(ctx, host, dht, config, logger)
```

## Future Enhancements

- **Adaptive Intervals**: Dynamic adjustment based on network conditions
- **Geographic Metrics**: Location-based latency analysis
- **Predictive Health**: ML-based peer health prediction
- **Load Balancing**: Intelligent peer selection based on metrics
- **Historical Analysis**: Long-term trend analysis and reporting