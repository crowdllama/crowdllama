// Package main provides the CrowdLlama UI server command.
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

	"github.com/matiasinsaurralde/crowdllama/pkg/ui"
)

func main() {
	var (
		port    = flag.Int("port", 0, "HTTP server port (default: 9002)")
		verbose = flag.Bool("verbose", false, "Enable verbose logging")
	)
	flag.Parse()

	// Setup logging
	var logger *zap.Logger
	var err error
	if *verbose {
		logger, err = zap.NewDevelopment()
	} else {
		logger, err = zap.NewProduction()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if syncErr := logger.Sync(); syncErr != nil {
			fmt.Fprintf(os.Stderr, "Failed to sync logger: %v\n", syncErr)
		}
	}()

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create UI server
	uiServer, err := ui.NewUIServer(ctx, logger)
	if err != nil {
		logger.Fatal("Failed to create UI server", zap.Error(err))
	}

	// Start HTTP server in a goroutine
	go func() {
		if err := uiServer.StartHTTPServer(*port); err != nil {
			logger.Fatal("Failed to start HTTP server", zap.Error(err))
		}
	}()

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	logger.Info("Shutting down UI server...")

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := uiServer.StopHTTPServer(shutdownCtx); err != nil {
		logger.Error("Error during HTTP server shutdown", zap.Error(err))
	}

	logger.Info("UI server stopped")
}
