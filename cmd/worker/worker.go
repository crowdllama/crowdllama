package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

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

		logger.Info("Starting crowdllama worker")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		w, err := worker.NewWorker(ctx)
		if err != nil {
			logger.Fatal("Failed to initialize worker", zap.Error(err))
		}
		logger.Info("Worker initialized", zap.String("peer_id", w.Host.ID().String()))

		// Set up metadata handler
		w.SetupMetadataHandler()

		// Set sample metadata
		w.UpdateMetadata(
			[]string{"llama-2-7b", "llama-2-13b", "mistral-7b"},
			150.0, // tokens/sec
			24,    // VRAM GB
			0.3,   // current load
			"RTX 4090",
		)

		// Generate a valid CID from a string namespace
		myNamespace := crowdllama.WorkerNamespace
		w.AdvertiseModel(ctx, myNamespace)

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
