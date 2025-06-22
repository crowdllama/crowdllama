// Package discovery provides peer and DHT discovery utilities for CrowdLlama.
package discovery

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
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/matiasinsaurralde/crowdllama/pkg/crowdllama"
	"github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-multihash"
	"go.uber.org/zap"
)

// advertiseInterval is the interval at which the model is advertised to the DHT:
var advertiseInterval = 10 * time.Second

var defaultListenAddrs = []string{"/ip4/0.0.0.0/tcp/0"}

const (
	// defaultBootstrapPeerAddr is the default bootstrap peer address for the DHT:
	// TODO: allow CLI to pass custom peers
	// defaultBootstrapPeerAddr = "/ip4/192.168.0.17/tcp/9000/p2p/12D3KooWJzvh2T7Htr1Dr86batqcAf4c5wB8D16zfkM2xJFpoahy"
	defaultBootstrapPeerAddr = "/ip4/192.168.0.15/tcp/9000/p2p/12D3KooWLLUBEZhkEq6NtTLD99RRpEYdcbe8uzx3L56UgF5VK4bw"
	// hardcoded dns bootstrap dht:
	// defaultBootstrapPeerAddr = "/dns4/dht.crowdllama.com/tcp/9000/p2p/12D3KooWJB3rAu12osvuqJDo2ncCN8VqQmVkecwgDxxu1AN7fmeR"
)

// NewHostAndDHT creates a libp2p host with DHT
func NewHostAndDHT(ctx context.Context, privKey crypto.PrivKey) (host.Host, *dht.IpfsDHT, error) {
	h, err := libp2p.New(
		libp2p.ListenAddrStrings(defaultListenAddrs...),
		libp2p.Identity(privKey),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create libp2p host: %w", err)
	}

	kadDHT, err := dht.New(ctx, h, dht.Mode(dht.ModeServer))
	if err != nil {
		return nil, nil, fmt.Errorf("create DHT instance: %w", err)
	}

	return h, kadDHT, nil
}

// BootstrapDHT connects to bootstrap peers. If customPeers is nil, use a local bootstrap address for fast local discovery.
func BootstrapDHT(ctx context.Context, h host.Host, kadDHT *dht.IpfsDHT) error {
	var bootstrapPeers []peer.AddrInfo
	addr, err := multiaddr.NewMultiaddr(defaultBootstrapPeerAddr)
	if err == nil {
		peerInfo, err := peer.AddrInfoFromP2pAddr(addr)
		if err != nil {
			log.Printf("Failed to parse bootstrap peer info: %v", err)
			// fallback to default public bootstrap peers
			bootstrapPeers = dht.GetDefaultBootstrapPeerAddrInfos()
		} else {
			bootstrapPeers = []peer.AddrInfo{*peerInfo}
		}
	} else {
		// fallback to default public bootstrap peers
		bootstrapPeers = dht.GetDefaultBootstrapPeerAddrInfos()
	}
	for _, peerInfo := range bootstrapPeers {
		if err := h.Connect(ctx, peerInfo); err != nil {
			log.Printf("Failed to connect to bootstrap %s: %v", peerInfo.ID, err)
		} else {
			log.Printf("Connected to bootstrap: %s", peerInfo.ID)
		}
	}
	if err := kadDHT.Bootstrap(ctx); err != nil {
		return fmt.Errorf("bootstrap DHT: %w", err)
	}
	return nil
}

// AdvertiseModel periodically announces model availability
func AdvertiseModel(ctx context.Context, kadDHT *dht.IpfsDHT, namespace string) {
	ticker := time.NewTicker(advertiseInterval)
	defer ticker.Stop()

	for {
		fmt.Printf("[DHT] Advertising namespace '%s'\n", namespace)
		select {
		case <-ticker.C:
			c, err := cid.Parse(namespace)
			if err != nil {
				log.Printf("Failed to parse namespace as CID: %v", err)
				continue
			}
			err = kadDHT.Provide(ctx, c, true)
			if err != nil {
				log.Printf("Failed to advertise model: %v", err)
			} else {
				log.Printf("Model advertised successfully")
			}
		case <-ctx.Done():
			return
		}
	}
}

// WaitForShutdown handles termination signals
func WaitForShutdown() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
}

// GetWorkerNamespaceCID generates the CID for worker discovery namespace
func GetWorkerNamespaceCID() (cid.Cid, error) {
	namespace := crowdllama.WorkerNamespace
	mh, err := multihash.Sum([]byte(namespace), multihash.IDENTITY, -1)
	if err != nil {
		return cid.Undef, fmt.Errorf("failed to create multihash for namespace: %w", err)
	}
	return cid.NewCidV1(cid.Raw, mh), nil
}

// RequestWorkerMetadata retrieves metadata from a worker peer using the metadata protocol
func RequestWorkerMetadata(ctx context.Context, h host.Host, workerPeer peer.ID, logger *zap.Logger) (*crowdllama.Resource, error) {
	logger.Debug("Opening stream to worker for metadata request",
		zap.String("worker_peer_id", workerPeer.String()),
		zap.String("protocol", crowdllama.MetadataProtocol))

	// Open a stream to the worker
	stream, err := h.NewStream(ctx, workerPeer, crowdllama.MetadataProtocol)
	if err != nil {
		logger.Error("Failed to open stream to worker",
			zap.String("worker_peer_id", workerPeer.String()),
			zap.Error(err))
		return nil, fmt.Errorf("failed to open stream to worker: %w", err)
	}
	defer func() {
		if closeErr := stream.Close(); closeErr != nil {
			logger.Warn("failed to close stream", zap.Error(closeErr))
		}
	}()

	if setDeadlineErr := stream.SetReadDeadline(time.Now().Add(5 * time.Second)); setDeadlineErr != nil {
		logger.Warn("failed to set read deadline", zap.Error(setDeadlineErr))
	}

	logger.Debug("Reading metadata response from worker",
		zap.String("worker_peer_id", workerPeer.String()))

	// Read the metadata response - read all available data until EOF
	var metadataJSON []byte
	buf := make([]byte, 1024)
	totalRead := 0

	for {
		n, readErr := stream.Read(buf)
		if n > 0 {
			metadataJSON = append(metadataJSON, buf[:n]...)
			totalRead += n
			logger.Debug("Read bytes from metadata stream",
				zap.String("worker_peer_id", workerPeer.String()),
				zap.Int("bytes_read", n),
				zap.Int("total_read", totalRead))
		}
		if readErr != nil {
			if readErr.Error() == "EOF" {
				logger.Debug("Received EOF from metadata stream",
					zap.String("worker_peer_id", workerPeer.String()),
					zap.Int("total_bytes_read", totalRead))
				break // EOF reached, we're done reading
			}
			logger.Error("Failed to read metadata from worker",
				zap.String("worker_peer_id", workerPeer.String()),
				zap.Error(readErr))
			return nil, fmt.Errorf("failed to read metadata from worker: %w", readErr)
		}
	}

	if len(metadataJSON) == 0 {
		return nil, fmt.Errorf("no metadata received from worker")
	}

	logger.Debug("Parsing metadata response",
		zap.String("worker_peer_id", workerPeer.String()),
		zap.Int("metadata_length", len(metadataJSON)))

	// Parse the metadata
	metadata, err := crowdllama.FromJSON(metadataJSON)
	if err != nil {
		logger.Error("Failed to parse metadata from worker",
			zap.String("worker_peer_id", workerPeer.String()),
			zap.Error(err))
		return nil, fmt.Errorf("failed to parse metadata from worker: %w", err)
	}

	logger.Debug("Successfully retrieved metadata from worker",
		zap.String("worker_peer_id", workerPeer.String()),
		zap.String("gpu_model", metadata.GPUModel),
		zap.Int("vram_gb", metadata.VRAMGB),
		zap.Float64("tokens_throughput", metadata.TokensThroughput))

	return metadata, nil
}

// DiscoverWorkers finds workers advertising the namespace and retrieves their metadata
func DiscoverWorkers(ctx context.Context, kadDHT *dht.IpfsDHT, logger *zap.Logger) ([]*crowdllama.Resource, error) {
	workers := make([]*crowdllama.Resource, 0, 10) // Preallocate with capacity 10

	// Get the namespace CID
	namespaceCID, err := GetWorkerNamespaceCID()
	if err != nil {
		return nil, fmt.Errorf("failed to get namespace CID: %w", err)
	}

	logger.Info("Searching for workers with namespace CID",
		zap.String("namespace", crowdllama.WorkerNamespace),
		zap.String("cid", namespaceCID.String()))

	// Find providers for the namespace CID
	providers := kadDHT.FindProvidersAsync(ctx, namespaceCID, 10)

	for provider := range providers {
		logger.Info("Found worker provider", zap.String("peer_id", provider.ID.String()))

		// Give the worker a moment to set up handlers
		time.Sleep(100 * time.Millisecond)

		// Request metadata from the worker
		metadata, err := RequestWorkerMetadata(ctx, kadDHT.Host(), provider.ID, logger)
		if err != nil {
			logger.Warn("Failed to get metadata from worker",
				zap.String("peer_id", provider.ID.String()),
				zap.Error(err))
			continue
		}

		// Verify the metadata is recent (within last hour)
		if time.Since(metadata.LastUpdated) > 1*time.Hour {
			logger.Warn("Metadata from worker is too old, skipping",
				zap.String("peer_id", provider.ID.String()),
				zap.Time("last_updated", metadata.LastUpdated))
			continue
		}

		workers = append(workers, metadata)
		logger.Info("Found worker",
			zap.String("peer_id", provider.ID.String()),
			zap.String("gpu_model", metadata.GPUModel),
			zap.Strings("supported_models", metadata.SupportedModels))
	}

	return workers, nil
}
