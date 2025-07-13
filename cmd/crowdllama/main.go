// Package main provides the main CLI command for CrowdLlama.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/ollama/ollama/cmd"

	"github.com/crowdllama/crowdllama/internal/keys"
	"github.com/crowdllama/crowdllama/pkg/config"
	"github.com/crowdllama/crowdllama/pkg/crowdllama"
	"github.com/crowdllama/crowdllama/pkg/gateway"
	"github.com/crowdllama/crowdllama/pkg/ipc"
	"github.com/crowdllama/crowdllama/pkg/logutil"
	"github.com/crowdllama/crowdllama/pkg/peer"
	"github.com/crowdllama/crowdllama/pkg/version"
)

var (
	cfg       *config.Configuration
	logger    *zap.Logger
	ollamaCmd *cobra.Command
	ipcServer *ipc.Server

	// Mode flags
	workerMode bool
	port       int
)

func main() {
	// Setup logging first
	if err := setupLogging(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to setup logging: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if syncErr := logger.Sync(); syncErr != nil {
			fmt.Fprintf(os.Stderr, "failed to sync logger: %v\n", syncErr)
		}
	}()

	// Initialize and customize the ollama CLI:
	ollamaCmd = cmd.NewCLI()
	ollamaCmd.Root().Use = "crowdllama"
	ollamaCmd.Root().Short = "CrowdLlama CLI - A distributed AI inference platform"
	ollamaCmd.Root().Long = `CrowdLlama CLI provides a command-line interface for the CrowdLlama distributed AI inference platform.`
	ollamaCmd.Root().SilenceUsage = true
	ollamaCmd.Root().SilenceErrors = true

	// Add our custom commands
	ollamaCmd.AddCommand(networkStatusCmd)
	ollamaCmd.AddCommand(versionCmd)
	ollamaCmd.AddCommand(startCmd)

	// Add flags to start command
	startCmd.Flags().BoolVar(&workerMode, "worker-mode", false, "Run in worker mode (default: consumer mode)")
	startCmd.Flags().IntVar(&port, "port", 9001, "HTTP server port (consumer mode only)")

	// Hack: Rename existing start command to start_ollama and store reference
	for _, command := range ollamaCmd.Commands() {
		if command.Use == "serve" {
			command.Use = "serve_ollama"
			command.Short = "Start the Ollama server (renamed from serve)"
			command.Aliases = nil // Remove all aliases, including 'start'
			logger.Info("Found and renamed 'serve' command to 'serve_ollama'")
		}
	}

	// Execute the ollama CLI with our modifications:
	if err := ollamaCmd.Execute(); err != nil {
		logger.Error("CLI execution failed", zap.Error(err))
		// Don't use os.Exit here as it will prevent defer from running
		return
	}
}

var networkStatusCmd = &cobra.Command{
	Use:   "network-status",
	Short: "Get the status of the network",
	Long:  `Get the status of the CrowdLlama network and connected peers.`,
	Run: func(_ *cobra.Command, _ []string) {
		runNetworkStatus()
	},
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version information",
	Long:  `Print detailed version information including commit hash, build date, and Go version.`,
	Run: func(_ *cobra.Command, _ []string) {
		runVersion()
	},
}

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the CrowdLlama platform",
	Long:  `Start the CrowdLlama distributed AI inference platform.`,
	Run: func(cmd *cobra.Command, args []string) {
		runStart(cmd, args)
	},
}

func setupLogging() error {
	// Initialize configuration
	cfg = config.NewConfiguration()

	// Parse command line flags
	flagSet := flag.NewFlagSet("crowdllama", flag.ExitOnError)
	cfg.ParseFlags(flagSet)

	// Load from environment variables
	cfg.LoadFromEnvironment()

	// Setup logger using unified logging
	logger = logutil.NewAppLogger("crowdllama", cfg.IsVerbose())

	// Log startup information
	logger.Info("CrowdLlama CLI version", zap.String("version", version.String()))

	if cfg.IsVerbose() {
		logger.Info("Verbose mode enabled")
	}

	// Read IPC socket from environment and start IPC server if configured
	if socketPath := os.Getenv("CROWDLLAMA_SOCKET"); socketPath != "" {
		logger.Info("IPC socket configured", zap.String("socket", socketPath))
		ipcServer = ipc.NewServer(socketPath, logger)
		go func() {
			if err := ipcServer.Start(); err != nil {
				logger.Error("Failed to start IPC server", zap.Error(err))
			}
		}()
	} else {
		logger.Info("No IPC socket configured, skipping IPC server")
	}

	return nil
}

func runVersion() {
	logger.Info("Displaying version information")
	fmt.Println(version.String())
}

func runNetworkStatus() {
	logger.Info("Checking network status")

	// TODO: Implement actual network status checking
	// For now, just display a placeholder message
	logger.Info("Network status check completed", zap.String("status", "placeholder"))
}

func runStart(cobraCmd *cobra.Command, _ []string) {
	logger.Info("Starting CrowdLlama platform")

	// Get flags from Cobra command
	workerMode, _ = cobraCmd.Flags().GetBool("worker-mode")
	port, _ = cobraCmd.Flags().GetInt("port")

	if workerMode {
		logger.Info("Starting in WORKER mode")
		runWorkerMode()
	} else {
		logger.Info("Starting in CONSUMER mode")
		runConsumerMode()
	}
}

// Common initialization logic for both worker and consumer
func commonInit() (crypto.PrivKey, error) {
	// Get private key based on mode
	keyType := "consumer"
	if workerMode {
		keyType = "worker"
	}

	keyPath := cfg.KeyPath
	if keyPath == "" {
		defaultPath, err := keys.GetDefaultKeyPath(keyType)
		if err != nil {
			return nil, fmt.Errorf("failed to get default key path: %w", err)
		}
		keyPath = defaultPath
	}

	keyManager := keys.NewKeyManager(keyPath, logger)
	privKey, err := keyManager.GetOrCreatePrivateKey()
	if err != nil {
		return nil, fmt.Errorf("failed to get or create private key: %w", err)
	}

	return privKey, nil
}

func runWorkerMode() {
	privKey, err := commonInit()
	if err != nil {
		logger.Error("Failed to initialize worker", zap.Error(err))
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p, err := peer.NewPeerWithConfig(ctx, privKey, cfg, true, logger) // true for worker mode
	if err != nil {
		logger.Error("Failed to start peer in worker mode", zap.Error(err))
		return
	}

	// Start the peer manager
	p.PeerManager.Start()

	// Set the peer instance in IPC server if available
	if ipcServer != nil {
		ipcServer.SetWorkerInstance(p)
	}

	logger.Info("Peer initialized in worker mode", zap.String("peer_id", p.Host.ID().String()))
	logger.Info("Peer addresses", zap.Any("addresses", p.Host.Addrs()))

	// Setup peer metadata
	p.SetupMetadataHandler()
	p.StartMetadataUpdates()
	p.AdvertisePeer(ctx, crowdllama.PeerNamespace)

	// Start metadata publisher
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := p.PublishMetadata(ctx); err != nil {
					logger.Error("Failed to publish metadata", zap.Error(err))
				} else {
					logger.Info("Published metadata to DHT")
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Start Ollama server in worker mode
	logger.Info("Starting Ollama server for worker mode")

	ollamaCmd.SetArgs([]string{"serve_ollama"})

	go func() {
		if err := ollamaCmd.Execute(); err != nil {
			logger.Error("Failed to execute serve_ollama command", zap.Error(err))
		}
	}()

	// Start peer statistics logging
	startPeerStatsLogging(ctx, p, logger)

	// Start peer discovery
	startPeerDiscovery(ctx, p, logger)

	waitForShutdownSignal(p, logger)
}

func runConsumerMode() {
	privKey, err := commonInit()
	if err != nil {
		logger.Error("Failed to initialize consumer", zap.Error(err))
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create peer for consumer mode
	p, err := peer.NewPeerWithConfig(ctx, privKey, cfg, false, logger) // false for consumer mode
	if err != nil {
		logger.Error("Failed to start peer in consumer mode", zap.Error(err))
		return
	}

	// Start the peer manager
	p.PeerManager.Start()

	// Create gateway using the peer
	g, err := gateway.NewGateway(ctx, logger, p)
	if err != nil {
		logger.Error("Failed to start gateway", zap.Error(err))
		return
	}

	logger.Info("Peer initialized in consumer mode", zap.String("peer_id", p.Host.ID().String()))
	logger.Info("Peer addresses", zap.Any("addresses", p.Host.Addrs()))

	// Setup peer metadata
	p.SetupMetadataHandler()
	p.StartMetadataUpdates()
	p.AdvertisePeer(ctx, crowdllama.PeerNamespace)

	// Start metadata publisher
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := p.PublishMetadata(ctx); err != nil {
					logger.Error("Failed to publish metadata", zap.Error(err))
				} else {
					logger.Info("Published metadata to DHT")
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Start HTTP server in a goroutine
	go func() {
		if err := g.StartHTTPServer(port); err != nil {
			logger.Error("Failed to start HTTP server", zap.Error(err))
		}
	}()

	// Start peer statistics logging
	startPeerStatsLogging(ctx, p, logger)

	// Start peer discovery
	startPeerDiscovery(ctx, p, logger)

	// Wait for shutdown signal
	waitForShutdownSignal(p, logger)

	// Stop background discovery and HTTP server
	g.StopBackgroundDiscovery()
	if err := g.StopHTTPServer(ctx); err != nil {
		logger.Error("Failed to stop HTTP server", zap.Error(err))
	}
}

// startPeerStatsLogging starts periodic logging of peer statistics
func startPeerStatsLogging(ctx context.Context, p *peer.Peer, logger *zap.Logger) {
	go func() {
		statsTicker := time.NewTicker(10 * time.Second)
		defer statsTicker.Stop()
		for {
			select {
			case <-statsTicker.C:
				logPeerStats(p, logger)
			case <-ctx.Done():
				return
			}
		}
	}()
}

// startPeerDiscovery starts periodic peer discovery and logs new peer discoveries
func startPeerDiscovery(ctx context.Context, p *peer.Peer, logger *zap.Logger) {
	go func() {
		discoveryTicker := time.NewTicker(15 * time.Second)
		defer discoveryTicker.Stop()

		for {
			select {
			case <-discoveryTicker.C:
				// The peer manager handles discovery internally, we just log the current state
				stats := p.PeerManager.GetPeerStatistics()
				logger.Info("Peer discovery status",
					zap.Int("total_peers", stats.TotalPeers),
					zap.Int("worker_peers", stats.WorkerPeers),
					zap.Int("consumer_peers", stats.ConsumerPeers))

			case <-ctx.Done():
				return
			}
		}
	}()
}

// logPeerStats logs current peer statistics including discovered peers
func logPeerStats(p *peer.Peer, logger *zap.Logger) {
	// Convert multiaddrs to strings for logging
	addrs := p.Host.Addrs()
	addrStrings := make([]string, len(addrs))
	for i, addr := range addrs {
		addrStrings[i] = addr.String()
	}

	// Get peer statistics from the peer manager
	stats := p.PeerManager.GetPeerStatistics()

	logger.Info("Peer statistics",
		zap.String("peer_id", p.Host.ID().String()),
		zap.Bool("worker_mode", p.WorkerMode),
		zap.Strings("peer_addresses", addrStrings),
		zap.Int("total_peers", stats.TotalPeers),
		zap.Int("worker_peers", stats.WorkerPeers),
		zap.Int("consumer_peers", stats.ConsumerPeers))
}

func waitForShutdownSignal(p *peer.Peer, logger *zap.Logger) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	logger.Info("Peer running. Press Ctrl+C to exit.")
	<-sigCh

	logger.Info("Shutdown signal received, stopping peer...")
	p.StopMetadataUpdates()
	logger.Info("Peer stopped")
}
