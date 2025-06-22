package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

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
		os.Exit(1)
	}

	switch os.Args[1] {
	case "version":
		fmt.Println("crowdllama version", version)
	case "start":
		startCmd := flag.NewFlagSet("start", flag.ExitOnError)
		port := startCmd.Int("port", consumer.DefaultHTTPPort, "HTTP server port")

		// Initialize configuration
		cfg := config.NewConfiguration()
		cfg.ParseFlags(startCmd)

		startCmd.Parse(os.Args[2:])

		// Setup logger
		if err := cfg.SetupLogger(); err != nil {
			log.Fatalf("Failed to setup logger: %v", err)
		}
		logger := cfg.GetLogger()
		defer logger.Sync()

		if cfg.IsVerbose() {
			logger.Info("Verbose mode enabled")
		}

		logger.Info("Starting CrowdLlama consumer")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		c, err := consumer.NewConsumer(ctx, logger)
		if err != nil {
			logger.Fatal("Failed to initialize consumer", zap.Error(err))
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
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}
