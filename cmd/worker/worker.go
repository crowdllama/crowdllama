// Package main provides the worker command for CrowdLlama.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"go.uber.org/zap"

	"github.com/libp2p/go-libp2p/core/crypto"

	"github.com/matiasinsaurralde/crowdllama/internal/keys"
	"github.com/matiasinsaurralde/crowdllama/pkg/config"
	"github.com/matiasinsaurralde/crowdllama/pkg/crowdllama"
	"github.com/matiasinsaurralde/crowdllama/pkg/version"
	"github.com/matiasinsaurralde/crowdllama/pkg/worker"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: crowdllama <command> [options]")
		fmt.Println("Commands:")
		fmt.Println("  version   Print the version information")
		fmt.Println("  start     Start the application")
		return
	}

	switch os.Args[1] {
	case "version":
		fmt.Println(version.String())
	case "start":
		if err := runWorker(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		return
	}
}

func runWorker() error {
	startCmd := flag.NewFlagSet("start", flag.ExitOnError)

	cfg, err := parseWorkerConfig(startCmd)
	if err != nil {
		return err
	}

	logger, err := setupWorkerLogger(cfg)
	if err != nil {
		return err
	}
	defer func() {
		if syncErr := logger.Sync(); syncErr != nil {
			fmt.Fprintf(os.Stderr, "failed to sync logger: %v\n", syncErr)
		}
	}()

	logger.Info("CrowdLlama version", zap.String("version", version.String()))

	if cfg.IsVerbose() {
		logger.Info("Verbose mode enabled")
	}
	logger.Info("Starting crowdllama worker")

	privKey, err := getWorkerPrivateKey(cfg, logger)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := worker.NewWorker(ctx, privKey)
	if err != nil {
		return fmt.Errorf("failed to start worker: %w", err)
	}
	logger.Info("Worker initialized", zap.String("peer_id", w.Host.ID().String()))
	logger.Info("Worker addresses", zap.Any("addresses", w.Host.Addrs()))

	setupWorkerMetadata(w)
	w.AdvertiseModel(ctx, crowdllama.WorkerNamespace)
	startMetadataPublisher(ctx, w, logger)

	logger.Info("Worker running. Press Ctrl+C to exit.")
	for {
		time.Sleep(10 * time.Second)
	}
}

func parseWorkerConfig(startCmd *flag.FlagSet) (*config.Configuration, error) {
	cfg := config.NewConfiguration()
	cfg.ParseFlags(startCmd)
	if err := startCmd.Parse(os.Args[2:]); err != nil {
		return nil, fmt.Errorf("failed to parse args: %w", err)
	}
	return cfg, nil
}

func setupWorkerLogger(cfg *config.Configuration) (*zap.Logger, error) {
	if err := cfg.SetupLogger(); err != nil {
		return nil, fmt.Errorf("failed to setup logger: %w", err)
	}
	return cfg.GetLogger(), nil
}

func getWorkerPrivateKey(cfg *config.Configuration, logger *zap.Logger) (crypto.PrivKey, error) {
	keyPath := cfg.KeyPath
	if keyPath == "" {
		defaultPath, err := keys.GetDefaultKeyPath("worker")
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

func setupWorkerMetadata(w *worker.Worker) {
	w.SetupMetadataHandler()
	// Start the periodic metadata updates
	w.StartMetadataUpdates()
}

func startMetadataPublisher(ctx context.Context, w *worker.Worker, logger *zap.Logger) {
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
}
