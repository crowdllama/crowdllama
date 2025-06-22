// Package main provides the worker command for CrowdLlama.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/matiasinsaurralde/crowdllama/internal/keys"
	"github.com/matiasinsaurralde/crowdllama/pkg/config"
	"github.com/matiasinsaurralde/crowdllama/pkg/crowdllama"
	"github.com/matiasinsaurralde/crowdllama/pkg/worker"
	"go.uber.org/zap"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: crowdllama <command> [options]")
		fmt.Println("Commands:")
		fmt.Println("  version   Print the version information")
		fmt.Println("  start     Start the application")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "version":
		fmt.Println("crowdllama version", version)
	case "start":
		startCmd := flag.NewFlagSet("start", flag.ExitOnError)

		// Initialize configuration
		cfg := config.NewConfiguration()
		cfg.ParseFlags(startCmd)

		if err := startCmd.Parse(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "failed to parse args: %v\n", err)
			os.Exit(1)
		}

		// Setup logger
		if err := cfg.SetupLogger(); err != nil {
			log.Fatalf("Failed to setup logger: %v", err)
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

		logger.Info("Starting crowdllama worker")

		// Determine key path
		keyPath := cfg.KeyPath
		if keyPath == "" {
			defaultPath, err := keys.GetDefaultKeyPath("worker")
			if err != nil {
				logger.Fatal("Failed to get default key path", zap.Error(err))
			}
			keyPath = defaultPath
		}

		// Initialize key manager
		keyManager := keys.NewKeyManager(keyPath, logger)

		// Get or create private key
		privKey, err := keyManager.GetOrCreatePrivateKey()
		if err != nil {
			logger.Fatal("Failed to get or create private key", zap.Error(err))
		}

		ctx, cancel := context.WithCancel(context.Background())

		w, err := worker.NewWorker(ctx, privKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to start worker: %v\n", err)
			os.Exit(1)
		}
		defer cancel()
		logger.Info("Worker initialized", zap.String("peer_id", w.Host.ID().String()))

		// Set up metadata handler
		w.SetupMetadataHandler()

		// Set sample metadata
		w.UpdateMetadata(
			[]string{"llama-2-7b", "llama-2-13b", "mistral-7b", "tinyllama"},
			150.0, // tokens/sec
			24,    // VRAM GB
			0.3,   // current load
			"RTX 4090",
		)

		// Generate a valid CID from a string namespace
		myNamespace := crowdllama.WorkerNamespace
		w.AdvertiseModel(ctx, myNamespace)

		// Periodically publish metadata to DHT
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

		// Keep running until interrupted
		logger.Info("Worker running. Press Ctrl+C to exit.")
		for {
			time.Sleep(10 * time.Second)
		}
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}
