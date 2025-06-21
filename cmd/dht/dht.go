package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/matiasinsaurralde/crowdllama/pkg/crowdllama"
	"github.com/multiformats/go-multihash"
)

var (
	// defaultListenAddrs is the default listen addresses for the DHT:
	defaultListenAddrs = []string{
		"/ip4/0.0.0.0/tcp/9000",
		"/ip4/0.0.0.0/udp/9000/quic-v1",
	}
)

// getWorkerNamespaceCID generates the same namespace CID as the worker
func getWorkerNamespaceCID() cid.Cid {
	myNamespace := crowdllama.WorkerNamespace
	mh, err := multihash.Sum([]byte(myNamespace), multihash.IDENTITY, -1)
	if err != nil {
		panic("Failed to create multihash: " + err.Error())
	}
	return cid.NewCidV1(cid.Raw, mh)
}

// requestWorkerMetadata requests metadata from a worker peer
func requestWorkerMetadata(ctx context.Context, h host.Host, workerPeer peer.ID) (*crowdllama.CrowdLlamaResource, error) {
	// Open a stream to the worker
	stream, err := h.NewStream(ctx, workerPeer, crowdllama.MetadataProtocol)
	if err != nil {
		return nil, fmt.Errorf("failed to open stream to worker: %w", err)
	}
	defer stream.Close()

	// Set a read deadline
	stream.SetReadDeadline(time.Now().Add(5 * time.Second))

	// Read the metadata response
	buf := make([]byte, 4096)
	n, err := stream.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata from worker: %w", err)
	}

	// Parse the metadata
	metadata, err := crowdllama.FromJSON(buf[:n])
	if err != nil {
		return nil, fmt.Errorf("failed to parse metadata from worker: %w", err)
	}

	return metadata, nil
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h, kadDHT, err := newDHTServer(ctx)
	if err != nil {
		log.Fatalf("Failed to start DHT server: %v", err)
	}
	printHostInfo(h)

	// Set up network notifier to detect new connections
	h.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(n network.Network, conn network.Conn) {
			peerID := conn.RemotePeer().String()
			fmt.Printf("[DHT Server] New peer connected: %s\n", peerID)
		},
		DisconnectedF: func(n network.Network, conn network.Conn) {
			fmt.Printf("[DHT Server] Peer disconnected: %s\n", conn.RemotePeer().String())
		},
	})

	// Start periodic worker discovery
	go discoverWorkersPeriodically(kadDHT)

	if err := kadDHT.Bootstrap(ctx); err != nil {
		log.Fatalf("Failed to bootstrap DHT: %v", err)
	}
	fmt.Println("DHT server running. Press Ctrl+C to exit.")
	waitForShutdown()
}

func newDHTServer(ctx context.Context) (host.Host, *dht.IpfsDHT, error) {
	h, err := libp2p.New(
		libp2p.ListenAddrStrings(defaultListenAddrs...),
	)
	if err != nil {
		return nil, nil, err
	}
	kadDHT, err := dht.New(ctx, h, dht.Mode(dht.ModeServer))
	if err != nil {
		return nil, nil, err
	}
	return h, kadDHT, nil
}

func printHostInfo(h host.Host) {
	fmt.Printf("DHT Server Peer ID: %s\n", h.ID().String())
	fmt.Println("DHT Server multiaddresses:")
	for _, addr := range h.Addrs() {
		fmt.Printf("  %s/p2p/%s\n", addr.String(), h.ID().String())
	}
}

func waitForShutdown() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	fmt.Println("Shutting down DHT server...")
	time.Sleep(1 * time.Second)
}

func discoverWorkersPeriodically(dht *dht.IpfsDHT) {
	ticker := time.NewTicker(10 * time.Second) // Run every 10 seconds for testing
	defer ticker.Stop()

	namespaceCID := getWorkerNamespaceCID()

	for range ticker.C {
		fmt.Printf("[DHT Server] Searching for workers advertising namespace: %s\n", namespaceCID.String())

		providers := dht.FindProvidersAsync(context.Background(), namespaceCID, 10)
		workerCount := 0

		for provider := range providers {
			workerCount++
			fmt.Printf("[DHT Server] Found worker: %s\n", provider.ID.String())
			fmt.Printf("  └─ Addresses: %v\n", provider.Addrs)

			// Request metadata from the worker
			metadata, err := requestWorkerMetadata(context.Background(), dht.Host(), provider.ID)
			if err != nil {
				fmt.Printf("  └─ Failed to get metadata: %v\n", err)
			} else {
				fmt.Printf("  └─ GPU Model: %s\n", metadata.GPUModel)
				fmt.Printf("  └─ VRAM: %d GB\n", metadata.VRAMGB)
				fmt.Printf("  └─ Throughput: %.2f tokens/sec\n", metadata.TokensThroughput)
				fmt.Printf("  └─ Current Load: %.2f\n", metadata.Load)
				fmt.Printf("  └─ Supported Models: %v\n", metadata.SupportedModels)
				fmt.Printf("  └─ Last Updated: %s\n", metadata.LastUpdated.Format(time.RFC3339))
			}
		}

		if workerCount == 0 {
			fmt.Printf("[DHT Server] No workers found advertising namespace: %s\n", namespaceCID.String())
		} else {
			fmt.Printf("[DHT Server] Total workers found: %d\n", workerCount)
		}
	}
}
