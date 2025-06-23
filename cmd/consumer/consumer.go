// Package main provides the consumer command for CrowdLlama.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/matiasinsaurralde/crowdllama/internal/keys"
	"github.com/matiasinsaurralde/crowdllama/pkg/config"
	"github.com/matiasinsaurralde/crowdllama/pkg/consumer"
	"go.uber.org/zap"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: crowdllama <command> [options]")
		fmt.Println("Commands:")
		fmt.Println("  version   Print the version information")
		fmt.Println("  start     Start the HTTP server")
		return
	}

	switch os.Args[1] {
	case "version":
		fmt.Println("crowdllama version", version)
	case "start":
		if err := runConsumer(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		return
	}
}

func runConsumer() error {
	startCmd := flag.NewFlagSet("start", flag.ExitOnError)
	port := startCmd.Int("port", consumer.DefaultHTTPPort, "HTTP server port")

	// Initialize configuration
	cfg := config.NewConfiguration()
	cfg.ParseFlags(startCmd)

	if err := startCmd.Parse(os.Args[2:]); err != nil {
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

	logger.Info("Starting CrowdLlama consumer")

	// Determine key path
	keyPath := cfg.KeyPath
	if keyPath == "" {
		defaultPath, err := keys.GetDefaultKeyPath("consumer")
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

	c, err := consumer.NewConsumer(ctx, logger, privKey)
	if err != nil {
		return fmt.Errorf("failed to initialize consumer: %w", err)
	}

	// Start background worker discovery
	c.StartBackgroundDiscovery()
	logger.Info("Background worker discovery started")

	// Start the HTTP server in a goroutine
	go func() {
		if err := c.StartHTTPServer(*port); err != nil {
			logger.Fatal("HTTP server failed", zap.Error(err))
		}
	}()

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logger.Info("Shutting down consumer...")

	// Stop background discovery
	c.StopBackgroundDiscovery()
	logger.Info("Background discovery stopped")

	// Gracefully shutdown the HTTP server
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := c.StopHTTPServer(shutdownCtx); err != nil {
		logger.Error("Failed to shutdown HTTP server gracefully", zap.Error(err))
	}

	logger.Info("Consumer shutdown complete")
	return nil
}
