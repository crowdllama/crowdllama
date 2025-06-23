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

	"go.uber.org/zap"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/matiasinsaurralde/crowdllama/internal/keys"
	"github.com/matiasinsaurralde/crowdllama/pkg/config"
	"github.com/matiasinsaurralde/crowdllama/pkg/consumer"
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

	cfg, err := parseConsumerConfig(startCmd)
	if err != nil {
		return err
	}

	logger, err := setupConsumerLogger(cfg)
	if err != nil {
		return err
	}
	defer func() {
		if err := logger.Sync(); err != nil {
			fmt.Fprintf(os.Stderr, "failed to sync logger: %v\n", err)
		}
	}()

	if cfg.IsVerbose() {
		logger.Info("Verbose mode enabled")
	}
	logger.Info("Starting CrowdLlama consumer")

	privKey, err := getConsumerPrivateKey(cfg, logger)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c, err := consumer.NewConsumer(ctx, logger, privKey)
	if err != nil {
		return fmt.Errorf("failed to initialize consumer: %w", err)
	}

	startConsumerServices(c, *port, logger)
	waitForShutdownSignal(c, logger)

	return nil
}

func parseConsumerConfig(startCmd *flag.FlagSet) (*config.Configuration, error) {
	cfg := config.NewConfiguration()
	cfg.ParseFlags(startCmd)
	if err := startCmd.Parse(os.Args[2:]); err != nil {
		return nil, fmt.Errorf("failed to parse args: %w", err)
	}
	return cfg, nil
}

func setupConsumerLogger(cfg *config.Configuration) (*zap.Logger, error) {
	if err := cfg.SetupLogger(); err != nil {
		return nil, fmt.Errorf("failed to setup logger: %w", err)
	}
	return cfg.GetLogger(), nil
}

func getConsumerPrivateKey(cfg *config.Configuration, logger *zap.Logger) (crypto.PrivKey, error) {
	keyPath := cfg.KeyPath
	if keyPath == "" {
		defaultPath, err := keys.GetDefaultKeyPath("consumer")
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

func startConsumerServices(c *consumer.Consumer, port int, logger *zap.Logger) {
	c.StartBackgroundDiscovery()
	logger.Info("Background worker discovery started")

	go func() {
		if err := c.StartHTTPServer(port); err != nil {
			logger.Fatal("HTTP server failed", zap.Error(err))
		}
	}()
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
