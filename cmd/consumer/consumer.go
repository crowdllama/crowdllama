package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

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

		if startCmd.NArg() < 1 {
			fmt.Println("Usage: crowdllama start <input>")
			os.Exit(1)
		}
		input := startCmd.Arg(0)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		c, err := consumer.NewConsumer(ctx)
		if err != nil {
			logger.Fatal("Failed to initialize consumer", zap.Error(err))
		}
		// c.ListKnownPeersLoop()

		// Discover available workers
		logger.Info("Discovering available workers")
		workers, err := c.DiscoverWorkers(ctx)
		if err != nil {
			logger.Fatal("Failed to discover workers", zap.Error(err))
		}

		if len(workers) == 0 {
			logger.Fatal("No workers found. Make sure a worker is running.")
		}

		// Find the best worker for the task
		logger.Info("Finding best available worker")
		bestWorker, err := c.FindBestWorker(ctx, "llama-2-7b") // Default model
		if err != nil {
			logger.Fatal("Failed to find suitable worker", zap.Error(err))
		}

		logger.Info("Selected worker",
			zap.String("peer_id", bestWorker.PeerID),
			zap.String("gpu_model", bestWorker.GPUModel),
			zap.Float64("tokens_throughput", bestWorker.TokensThroughput),
			zap.Float64("load", bestWorker.Load))

		logger.Info("Requesting inference",
			zap.String("worker_peer_id", bestWorker.PeerID),
			zap.String("input", input))
		resp, err := c.RequestInference(ctx, bestWorker.PeerID, input)
		if err != nil {
			logger.Fatal("Failed to request inference", zap.Error(err))
		}
		logger.Info("Received response from worker", zap.String("response", resp))
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}
