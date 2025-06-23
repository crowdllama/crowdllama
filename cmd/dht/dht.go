// Package main provides the DHT command for CrowdLlama.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/matiasinsaurralde/crowdllama/internal/keys"
	"github.com/matiasinsaurralde/crowdllama/pkg/config"
	"github.com/matiasinsaurralde/crowdllama/pkg/dht"
)

func main() {
	if err := runDHTServer(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}
}

func runDHTServer() error {
	// Parse command line flags
	startCmd := flag.NewFlagSet("start", flag.ExitOnError)

	// Initialize configuration
	cfg := config.NewConfiguration()
	cfg.ParseFlags(startCmd)

	if err := startCmd.Parse(os.Args[1:]); err != nil {
		return fmt.Errorf("failed to parse args: %w", err)
	}

	// Setup logger
	if err := cfg.SetupLogger(); err != nil {
		return fmt.Errorf("failed to setup logger: %w", err)
	}
	logger := cfg.GetLogger()
	defer func() {
		if err := logger.Sync(); err != nil {
			fmt.Fprintf(os.Stderr, "failed to sync logger: %v\n", err)
		}
	}()

	if cfg.IsVerbose() {
		logger.Info("Verbose mode enabled")
	}

	logger.Info("Starting DHT server")

	// Determine key path
	keyPath := cfg.KeyPath
	if keyPath == "" {
		defaultPath, err := keys.GetDefaultKeyPath("dht")
		if err != nil {
			return fmt.Errorf("failed to get default key path: %w", err)
		}
		keyPath = defaultPath
	}

	// Initialize key manager
	keyManager := keys.NewKeyManager(keyPath, logger)

	// Get or create private key
	privKey, err := keyManager.GetOrCreatePrivateKey()
	if err != nil {
		return fmt.Errorf("failed to get or create private key: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create DHT server using the new package
	server, err := dht.NewDHTServer(ctx, privKey, logger)
	if err != nil {
		return fmt.Errorf("failed to create DHT server: %w", err)
	}

	// Print host information
	printHostInfo(server, logger)

	// Start the DHT server
	primaryAddr, err := server.Start()
	if err != nil {
		return fmt.Errorf("failed to start DHT server: %w", err)
	}

	logger.Info("DHT server started successfully", zap.String("primary_addr", primaryAddr))
	logger.Info("DHT server running. Press Ctrl+C to exit.")

	waitForShutdown(logger, server)
	return nil
}

func printHostInfo(server *dht.DHTServer, logger *zap.Logger) {
	logger.Info("DHT Server information",
		zap.String("peer_id", server.GetPeerID()))

	logger.Debug("DHT Server multiaddresses:")
	for _, addr := range server.GetPeerAddrs() {
		logger.Debug("  Listening address", zap.String("address", addr))
	}
}

func waitForShutdown(logger *zap.Logger, server *dht.DHTServer) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	logger.Info("Shutting down DHT server...")
	server.Stop()
	time.Sleep(1 * time.Second)
}
