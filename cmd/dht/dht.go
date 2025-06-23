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
	startCmd := flag.NewFlagSet("start", flag.ExitOnError)
	cfg, err := parseDHTConfig(startCmd)
	if err != nil {
		return err
	}

	logger, err := setupDHTLogger(cfg)
	if err != nil {
		return err
	}
	defer func() {
		if syncErr := logger.Sync(); syncErr != nil {
			fmt.Fprintf(os.Stderr, "failed to sync logger: %v\n", syncErr)
		}
	}()

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
	if err := startCmd.Parse(os.Args[1:]); err != nil {
		return nil, fmt.Errorf("failed to parse args: %w", err)
	}
	return cfg, nil
}

func setupDHTLogger(cfg *config.Configuration) (*zap.Logger, error) {
	if err := cfg.SetupLogger(); err != nil {
		return nil, fmt.Errorf("failed to setup logger: %w", err)
	}
	return cfg.GetLogger(), nil
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
