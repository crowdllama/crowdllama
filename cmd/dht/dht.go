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

	"github.com/libp2p/go-libp2p/core/crypto"
	"go.uber.org/zap"

	"github.com/crowdllama/crowdllama/internal/keys"
	"github.com/crowdllama/crowdllama/pkg/config"
	"github.com/crowdllama/crowdllama/pkg/dht"
	"github.com/crowdllama/crowdllama/pkg/logutil"
	"github.com/crowdllama/crowdllama/pkg/version"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: crowdllama-dht <command> [options]")
		fmt.Println("Commands:")
		fmt.Println("  version   Print the version information")
		fmt.Println("  start     Start the DHT server")
		return
	}

	switch os.Args[1] {
	case "version":
		fmt.Println(version.String())
	case "start":
		if err := runDHTServer(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		return
	}
}

func runDHTServer() error {
	startCmd := flag.NewFlagSet("start", flag.ExitOnError)
	cfg, err := parseDHTConfig(startCmd)
	if err != nil {
		return err
	}

	logger := logutil.NewAppLogger("dht", cfg.IsVerbose())
	defer func() {
		if syncErr := logger.Sync(); syncErr != nil {
			fmt.Fprintf(os.Stderr, "failed to sync logger: %v\n", syncErr)
		}
	}()

	logger.Info("CrowdLlama version", zap.String("version", version.String()))

	if cfg.IsVerbose() {
		logger.Info("Verbose mode enabled")
	}
	logger.Info("Starting DHT server")

	privKey, err := getDHTPrivateKey(cfg, logger)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server, err := dht.NewDHTServer(ctx, privKey, logger)
	if err != nil {
		return fmt.Errorf("failed to create DHT server: %w", err)
	}

	printHostInfo(server, logger)

	primaryAddr, err := server.Start()
	if err != nil {
		return fmt.Errorf("failed to start DHT server: %w", err)
	}

	logger.Info("DHT server started successfully", zap.String("primary_addr", primaryAddr))
	logger.Info("DHT server running. Press Ctrl+C to exit.")

	waitForShutdown(logger, server)
	return nil
}

func parseDHTConfig(startCmd *flag.FlagSet) (*config.Configuration, error) {
	cfg := config.NewConfiguration()
	cfg.ParseFlags(startCmd)
	if err := startCmd.Parse(os.Args[2:]); err != nil {
		return nil, fmt.Errorf("failed to parse args: %w", err)
	}
	return cfg, nil
}

func getDHTPrivateKey(cfg *config.Configuration, logger *zap.Logger) (crypto.PrivKey, error) {
	keyPath := cfg.KeyPath
	if keyPath == "" {
		defaultPath, err := keys.GetDefaultKeyPath("dht")
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

func printHostInfo(server *dht.Server, logger *zap.Logger) {
	logger.Info("DHT Server information",
		zap.String("peer_id", server.GetPeerID()))

	logger.Debug("DHT Server multiaddresses:")
	for _, addr := range server.GetPeerAddrs() {
		logger.Debug("  Listening address", zap.String("address", addr))
	}
}

func waitForShutdown(logger *zap.Logger, server *dht.Server) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	logger.Info("Shutting down DHT server...")
	server.Stop()
	time.Sleep(1 * time.Second)
}
