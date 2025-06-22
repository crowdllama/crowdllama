package consumer

import (
	"context"
	"fmt"
	"time"

	"github.com/ipfs/go-cid"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/matiasinsaurralde/crowdllama/internal/discovery"
	"github.com/matiasinsaurralde/crowdllama/pkg/crowdllama"
	"github.com/multiformats/go-multihash"
	"go.uber.org/zap"
)

const InferenceProtocol = "/crowdllama/inference/1.0.0"

type Consumer struct {
	Host   host.Host
	DHT    *dht.IpfsDHT
	logger *zap.Logger
}

func NewConsumer(ctx context.Context, logger *zap.Logger) (*Consumer, error) {
	h, kadDHT, err := discovery.NewHostAndDHT(ctx)
	if err != nil {
		return nil, err
	}
	if err := discovery.BootstrapDHT(ctx, h, kadDHT); err != nil {
		return nil, err
	}
	return &Consumer{
		Host:   h,
		DHT:    kadDHT,
		logger: logger,
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
	c.logger.Debug("Opening stream to worker", zap.String("peer_id", peerInfo.ID.String()))
	stream, err := c.Host.NewStream(ctx, peerInfo.ID, InferenceProtocol)
	if err != nil {
		return "", fmt.Errorf("failed to open stream: %w", err)
	}
	defer stream.Close()

	c.logger.Debug("Writing input to stream", zap.String("input", input))
	_, err = stream.Write([]byte(input))
	if err != nil {
		return "", fmt.Errorf("failed to write to stream: %w", err)
	}

	c.logger.Debug("Waiting for response from worker...")

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
			c.logger.Debug("Read byte from stream",
				zap.String("byte", string(buf[:n])),
				zap.Int("ascii", int(buf[0])))
		}
	}

	c.logger.Info("Received response from worker",
		zap.String("worker_id", workerID),
		zap.Int("response_length", len(response)),
		zap.String("response", response))
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
			c.logger.Info("Known peers in DHT routing table", zap.Int("peer_count", len(peers)))
			for _, p := range peers {
				c.logger.Debug("Known peer", zap.String("peer_id", p.String()))
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

// getMetadataFromPeer retrieves metadata from a specific peer using the metadata protocol
func (c *Consumer) getMetadataFromPeer(ctx context.Context, peerID peer.ID) (*crowdllama.CrowdLlamaResource, error) {
	c.logger.Debug("Attempting to get metadata from peer", zap.String("peer_id", peerID.String()))

	// Try to open a stream to the peer using the metadata protocol
	stream, err := c.Host.NewStream(ctx, peerID, crowdllama.MetadataProtocol)
	if err != nil {
		return nil, fmt.Errorf("failed to open metadata stream: %w", err)
	}
	defer stream.Close()

	c.logger.Debug("Successfully opened metadata stream to peer", zap.String("peer_id", peerID.String()))

	// Set a read deadline
	stream.SetReadDeadline(time.Now().Add(5 * time.Second))

	// Read the metadata response - read all available data until EOF
	var metadataJSON []byte
	buf := make([]byte, 1024)
	totalRead := 0

	for {
		n, err := stream.Read(buf)
		if n > 0 {
			metadataJSON = append(metadataJSON, buf[:n]...)
			totalRead += n
			c.logger.Debug("Read bytes from metadata stream",
				zap.String("peer_id", peerID.String()),
				zap.Int("bytes_read", n),
				zap.Int("total_read", totalRead))
		}
		if err != nil {
			if err.Error() == "EOF" {
				c.logger.Debug("Received EOF from metadata stream",
					zap.String("peer_id", peerID.String()),
					zap.Int("total_bytes_read", totalRead))
				break // EOF reached, we're done reading
			}
			return nil, fmt.Errorf("failed to read metadata from stream: %w", err)
		}
	}

	if len(metadataJSON) == 0 {
		return nil, fmt.Errorf("no metadata received from peer")
	}

	c.logger.Debug("Received metadata from peer",
		zap.String("peer_id", peerID.String()),
		zap.Int("metadata_length", len(metadataJSON)),
		zap.String("metadata", string(metadataJSON)))

	// Parse the metadata JSON
	resource, err := crowdllama.FromJSON(metadataJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to parse metadata JSON: %w", err)
	}

	c.logger.Debug("Successfully parsed metadata from peer", zap.String("peer_id", peerID.String()))
	return resource, nil
}

// DiscoverWorkers searches for available workers in the DHT
func (c *Consumer) DiscoverWorkers(ctx context.Context) ([]*crowdllama.CrowdLlamaResource, error) {
	var workers []*crowdllama.CrowdLlamaResource

	// Use DHT FindProviders to discover workers using the namespace
	namespace := crowdllama.WorkerNamespace
	mh, err := multihash.Sum([]byte(namespace), multihash.IDENTITY, -1)
	if err != nil {
		return nil, fmt.Errorf("failed to create multihash for namespace: %w", err)
	}
	cid := cid.NewCidV1(cid.Raw, mh)

	c.logger.Info("Searching for workers with namespace CID", zap.String("namespace", namespace), zap.String("cid", cid.String()))

	// Find providers for the namespace CID
	providers := c.DHT.FindProvidersAsync(ctx, cid, 10)

	for provider := range providers {
		c.logger.Info("Found worker provider", zap.String("peer_id", provider.ID.String()))

		// Give the worker a moment to set up handlers
		time.Sleep(100 * time.Millisecond)

		// Try to get metadata directly from the worker
		resource, err := c.getMetadataFromPeer(ctx, provider.ID)
		if err != nil {
			c.logger.Warn("Failed to get metadata from worker",
				zap.String("peer_id", provider.ID.String()),
				zap.Error(err))
			continue
		}

		// Verify the metadata is recent (within last 30 seconds)
		if time.Since(resource.LastUpdated) > 1*time.Hour {
			c.logger.Warn("Metadata from worker is too old, skipping",
				zap.String("peer_id", provider.ID.String()),
				zap.Time("last_updated", resource.LastUpdated))
			continue
		}

		workers = append(workers, resource)
		c.logger.Info("Found worker",
			zap.String("peer_id", provider.ID.String()),
			zap.String("gpu_model", resource.GPUModel),
			zap.Strings("supported_models", resource.SupportedModels))
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
