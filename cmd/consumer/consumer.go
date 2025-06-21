package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/matiasinsaurralde/crowdllama/pkg/consumer"
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
		// Add start command flags here if needed
		startCmd.Parse(os.Args[2:])
		if startCmd.NArg() < 1 {
			fmt.Println("Usage: crowdllama start <input>")
			os.Exit(1)
		}
		input := startCmd.Arg(0)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		c, err := consumer.NewConsumer(ctx)
		if err != nil {
			log.Fatalf("Failed to initialize consumer: %v", err)
		}
		// c.ListKnownPeersLoop()

		// Discover available workers
		fmt.Println("Discovering available workers...")
		workers, err := c.DiscoverWorkers(ctx)
		if err != nil {
			log.Fatalf("Failed to discover workers: %v", err)
		}

		if len(workers) == 0 {
			log.Fatalf("No workers found. Make sure a worker is running.")
		}

		// Find the best worker for the task
		fmt.Println("Finding best available worker...")
		bestWorker, err := c.FindBestWorker(ctx, "llama-2-7b") // Default model
		if err != nil {
			log.Fatalf("Failed to find suitable worker: %v", err)
		}

		fmt.Printf("Selected worker: %s (GPU: %s, Throughput: %.2f tokens/sec, Load: %.2f)\n",
			bestWorker.PeerID, bestWorker.GPUModel, bestWorker.TokensThroughput, bestWorker.Load)

		fmt.Printf("Requesting inference from worker %s with input: %s\n", bestWorker.PeerID, input)
		resp, err := c.RequestInference(ctx, bestWorker.PeerID, input)
		if err != nil {
			log.Fatalf("Failed to request inference: %v", err)
		}
		fmt.Printf("Received response from worker: %s\n", resp)
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}
