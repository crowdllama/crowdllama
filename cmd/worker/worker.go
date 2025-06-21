package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/matiasinsaurralde/crowdllama/pkg/crowdllama"
	"github.com/matiasinsaurralde/crowdllama/pkg/worker"
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
		startCmd.Parse(os.Args[2:])
		fmt.Println("Starting crowdllama...")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		w, err := worker.NewWorker(ctx)
		if err != nil {
			log.Fatalf("Failed to initialize worker: %v", err)
		}
		fmt.Printf("Worker Peer ID: %s\n", w.Host.ID().String())

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
		fmt.Println("Worker running. Press Ctrl+C to exit.")
		for {
			time.Sleep(10 * time.Second)
		}
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}
