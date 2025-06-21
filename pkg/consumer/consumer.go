package consumer

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ipfs/go-cid"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/matiasinsaurralde/crowdllama/internal/discovery"
	"github.com/matiasinsaurralde/crowdllama/pkg/crowdllama"
	"github.com/multiformats/go-multihash"
)

const InferenceProtocol = "/crowdllama/inference/1.0.0"

type Consumer struct {
	Host host.Host
	DHT  *dht.IpfsDHT
}

func NewConsumer(ctx context.Context) (*Consumer, error) {
	h, kadDHT, err := discovery.NewHostAndDHT(ctx)
	if err != nil {
		return nil, err
	}
	if err := discovery.BootstrapDHT(ctx, h, kadDHT); err != nil {
		return nil, err
	}
	return &Consumer{
		Host: h,
		DHT:  kadDHT,
	}, nil
}

// RequestInference sends a string task to a worker and waits for a response
func (c *Consumer) RequestInference(ctx context.Context, workerID string, input string) (string, error) {
	pid, err := peer.Decode(workerID)
	if err != nil {
		return "", fmt.Errorf("invalid worker peer ID: %w", err)
	}
	peerInfo, err := c.DHT.FindPeer(ctx, pid)
	if err != nil {
		return "", fmt.Errorf("could not find worker peer: %w", err)
	}
	fmt.Printf("Opening stream to worker %s\n", peerInfo.ID.String())
	stream, err := c.Host.NewStream(ctx, peerInfo.ID, InferenceProtocol)
	if err != nil {
		return "", fmt.Errorf("failed to open stream: %w", err)
	}
	defer stream.Close()

	fmt.Printf("Writing input to stream: %s\n", input)
	_, err = stream.Write([]byte(input))
	if err != nil {
		return "", fmt.Errorf("failed to write to stream: %w", err)
	}

	fmt.Println("Waiting for response from worker...")

	// Read response byte by byte until EOF
	var response string
	buf := make([]byte, 1)
	for {
		n, err := stream.Read(buf)
		if err != nil {
			if err.Error() == "EOF" {
				break // EOF reached, we're done reading
			}
			return "", fmt.Errorf("failed to read from stream: %w", err)
		}
		if n > 0 {
			response += string(buf[:n])
			fmt.Printf("Read byte: '%s' (ASCII: %d)\n", string(buf[:n]), buf[0])
		}
	}

	fmt.Printf("Received response (%d bytes): %s\n", len(response), response)
	return response, nil
}

// ListenForResponses is a placeholder for future expansion if needed
func (c *Consumer) ListenForResponses() {
	// Not needed for this simple request/response model
}

func (c *Consumer) ListKnownPeersLoop() {
	go func() {
		for {
			peers := c.DHT.RoutingTable().ListPeers()
			fmt.Printf("[DHT] Known peers (%d):\n", len(peers))
			for _, p := range peers {
				fmt.Println("  ", p.String())
			}
			time.Sleep(1 * time.Minute)
		}
	}()
}

// getWorkerMetadataKey generates the same DHT key as the worker
func getWorkerMetadataKey(peerID string) string {
	// Use a simple string key - the DHT might accept this format
	return "crowdllama-worker-" + peerID
}

// DiscoverWorkers searches for available workers in the DHT
func (c *Consumer) DiscoverWorkers(ctx context.Context) ([]*crowdllama.CrowdLlamaResource, error) {
	var workers []*crowdllama.CrowdLlamaResource

	// For now, we'll use a simple approach to find workers
	// In a real implementation, you might want to use a more sophisticated discovery mechanism

	// Get known peers and try to find their metadata
	peers := c.DHT.RoutingTable().ListPeers()
	for _, peerID := range peers {
		// Try to find providers for this peer's metadata
		// We'll use a simple approach for now
		log.Printf("Checking peer: %s", peerID.String())

		// For now, we'll skip this approach and just return empty
		// In a real implementation, you'd need to implement a proper discovery mechanism
	}

	return workers, nil
}

// FindBestWorker finds the best available worker based on criteria
func (c *Consumer) FindBestWorker(ctx context.Context, requiredModel string) (*crowdllama.CrowdLlamaResource, error) {
	workers, err := c.DiscoverWorkers(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to discover workers: %w", err)
	}

	if len(workers) == 0 {
		return nil, fmt.Errorf("no workers found")
	}

	// Find the best worker based on criteria
	var bestWorker *crowdllama.CrowdLlamaResource
	var bestScore float64

	for _, worker := range workers {
		// Check if worker supports the required model
		supportsModel := false
		for _, model := range worker.SupportedModels {
			if model == requiredModel {
				supportsModel = true
				break
			}
		}

		if !supportsModel {
			continue
		}

		// Calculate score based on throughput and load
		score := worker.TokensThroughput * (1.0 - worker.Load)

		if bestWorker == nil || score > bestScore {
			bestWorker = worker
			bestScore = score
		}
	}

	if bestWorker == nil {
		return nil, fmt.Errorf("no worker found supporting model: %s", requiredModel)
	}

	return bestWorker, nil
}

// DiscoverWorkersViaProviders discovers workers using FindProviders and a namespace-derived CID
func (c *Consumer) DiscoverWorkersViaProviders(ctx context.Context, namespace string) ([]peer.ID, error) {
	// Generate the same CID as the worker
	mh, err := multihash.Sum([]byte(namespace), multihash.IDENTITY, -1)
	if err != nil {
		return nil, err
	}
	cid := cid.NewCidV1(cid.Raw, mh)

	providers := c.DHT.FindProvidersAsync(ctx, cid, 10)
	var peers []peer.ID
	for p := range providers {
		peers = append(peers, p.ID)
	}
	return peers, nil
}
