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
	"github.com/crowdllama/crowdllama/pkg/consumer"
	"github.com/crowdllama/crowdllama/pkg/crowdllama"
	"github.com/crowdllama/crowdllama/pkg/ipc"
	"github.com/crowdllama/crowdllama/pkg/version"
	"github.com/crowdllama/crowdllama/pkg/worker"
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
	startCmd.Flags().IntVar(&port, "port", consumer.DefaultHTTPPort, "HTTP server port (consumer mode only)")

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

	// Setup logger
	if err := cfg.SetupLogger(); err != nil {
		return fmt.Errorf("failed to setup logger: %w", err)
	}

	logger = cfg.GetLogger()

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
	fmt.Println("Network status: Placeholder - implementation pending")
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

	w, err := worker.NewWorkerWithConfig(ctx, privKey, cfg)
	if err != nil {
		logger.Error("Failed to start worker", zap.Error(err))
		return
	}

	// Set the worker instance in IPC server if available
	if ipcServer != nil {
		ipcServer.SetWorkerInstance(w)
	}

	logger.Info("Worker initialized", zap.String("peer_id", w.Host.ID().String()))
	logger.Info("Worker addresses", zap.Any("addresses", w.Host.Addrs()))

	// Setup worker metadata
	w.SetupMetadataHandler()
	w.StartMetadataUpdates()
	w.AdvertiseModel(ctx, crowdllama.WorkerNamespace)

	// Start metadata publisher
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := w.PublishMetadata(ctx); err != nil {
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
	logger.Info("Starting Ollama server for worker mode...")

	// Prevent recursion if already running serve_ollama
	for _, arg := range os.Args[1:] {
		if arg == "serve_ollama" {
			logger.Info("Already running serve_ollama, not re-invoking.")
			return
		}
	}

	// Simulate CLI args and execute serve_ollama
	ollamaCmd.SetArgs([]string{"serve_ollama"})
	if err := ollamaCmd.Execute(); err != nil {
		logger.Error("Failed to execute serve_ollama command", zap.Error(err))
	}
}

func runConsumerMode() {
	privKey, err := commonInit()
	if err != nil {
		logger.Error("Failed to initialize consumer", zap.Error(err))
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c, err := consumer.NewConsumer(ctx, logger, privKey, cfg)
	if err != nil {
		logger.Error("Failed to initialize consumer", zap.Error(err))
		return
	}

	// Set the consumer instance in IPC server if available
	if ipcServer != nil {
		ipcServer.SetConsumerInstance(c)
	}

	// Start consumer services
	c.StartBackgroundDiscovery()
	logger.Info("Background worker discovery started")

	go func() {
		if err := c.StartHTTPServer(port); err != nil {
			logger.Fatal("HTTP server failed", zap.Error(err))
		}
	}()

	// Wait for shutdown signal
	waitForShutdownSignal(c, logger)
}

func waitForShutdownSignal(c *consumer.Consumer, logger *zap.Logger) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logger.Info("Shutting down consumer...")

	c.StopBackgroundDiscovery()
	logger.Info("Background discovery stopped")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := c.StopHTTPServer(shutdownCtx); err != nil {
		logger.Error("Failed to shutdown HTTP server gracefully", zap.Error(err))
	}

	logger.Info("Consumer shutdown complete")
}
