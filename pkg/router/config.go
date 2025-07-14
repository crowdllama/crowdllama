package router

import (
	"time"
)

// Config holds all configurable settings for the router
type Config struct {
	// Ping monitoring settings
	PingInterval        time.Duration // How often to ping peers
	PingTimeout         time.Duration // Timeout for ping responses
	MetricsWindowSize   int           // Number of ticks to keep in sliding window
	MetricsTickDuration time.Duration // Duration of each tick for metrics

	// DHT settings
	DiscoveryInterval      time.Duration // How often to discover new peers
	AdvertisingInterval    time.Duration // How often to advertise presence
	MetadataUpdateInterval time.Duration // How often to update metadata

	// Peer health settings
	StalePeerTimeout    time.Duration // When to consider peer stale
	HealthCheckInterval time.Duration // How often to perform health checks
	MaxFailedAttempts   int           // Max failed attempts before marking unhealthy
	BackoffBase         time.Duration // Base backoff time
	MetadataTimeout     time.Duration // Timeout for metadata requests
	MaxMetadataAge      time.Duration // Max age for metadata to be considered valid

	// Connection settings
	ConnectionTimeout time.Duration // Timeout for establishing connections
	MaxConcurrentPings int          // Max concurrent ping operations
}

// DefaultConfig returns default configuration optimized for production
func DefaultConfig() *Config {
	return &Config{
		// Ping monitoring (1 second intervals as requested)
		PingInterval:        1 * time.Second,
		PingTimeout:         5 * time.Second,
		MetricsWindowSize:   30, // 30 ticks = 30 seconds of history
		MetricsTickDuration: 1 * time.Second,

		// DHT settings
		DiscoveryInterval:      10 * time.Second,
		AdvertisingInterval:    30 * time.Second,
		MetadataUpdateInterval: 60 * time.Second,

		// Peer health
		StalePeerTimeout:    1 * time.Minute,
		HealthCheckInterval: 20 * time.Second,
		MaxFailedAttempts:   3,
		BackoffBase:         10 * time.Second,
		MetadataTimeout:     5 * time.Second,
		MaxMetadataAge:      1 * time.Minute,

		// Connection settings
		ConnectionTimeout:  10 * time.Second,
		MaxConcurrentPings: 10,
	}
}

// TestConfig returns configuration optimized for testing with shorter intervals
func TestConfig() *Config {
	return &Config{
		// Ping monitoring (faster for testing)
		PingInterval:        1 * time.Second,
		PingTimeout:         2 * time.Second,
		MetricsWindowSize:   30, // Still 30 ticks but faster
		MetricsTickDuration: 1 * time.Second,

		// DHT settings (faster for testing)
		DiscoveryInterval:      2 * time.Second,
		AdvertisingInterval:    5 * time.Second,
		MetadataUpdateInterval: 5 * time.Second,

		// Peer health (faster for testing)
		StalePeerTimeout:    30 * time.Second,
		HealthCheckInterval: 5 * time.Second,
		MaxFailedAttempts:   2,
		BackoffBase:         5 * time.Second,
		MetadataTimeout:     2 * time.Second,
		MaxMetadataAge:      30 * time.Second,

		// Connection settings
		ConnectionTimeout:  5 * time.Second,
		MaxConcurrentPings: 5,
	}
}

// IntermittentTestConfig returns configuration for testing with intermittent connectivity
func IntermittentTestConfig() *Config {
	config := TestConfig()
	// Adjust for intermittent connectivity testing
	config.PingTimeout = 3 * time.Second
	config.MaxFailedAttempts = 5 // More tolerant of failures
	config.BackoffBase = 2 * time.Second
	config.StalePeerTimeout = 45 * time.Second
	return config
}