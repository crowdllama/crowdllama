// Package main provides the DHT command for CrowdLlama.
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

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/matiasinsaurralde/crowdllama/internal/discovery"
	"github.com/matiasinsaurralde/crowdllama/internal/keys"
	"github.com/matiasinsaurralde/crowdllama/pkg/config"
	"github.com/matiasinsaurralde/crowdllama/pkg/crowdllama"
	"github.com/multiformats/go-multiaddr"
	"go.uber.org/zap"
)

// defaultListenAddrs is the default listen addresses for the DHT:
var defaultListenAddrs = []string{
	"/ip4/0.0.0.0/tcp/9000",
	"/ip4/0.0.0.0/udp/9000/quic-v1",
}

// getWorkerNamespaceCID generates the same namespace CID as the worker
func getWorkerNamespaceCID() cid.Cid {
	namespaceCID, err := discovery.GetWorkerNamespaceCID()
	if err != nil {
		panic("Failed to get namespace CID: " + err.Error())
	}
	return namespaceCID
}

// requestWorkerMetadata requests metadata from a worker peer
func requestWorkerMetadata(ctx context.Context, h host.Host, workerPeer peer.ID, logger *zap.Logger) (*crowdllama.Resource, error) {
	metadata, err := discovery.RequestWorkerMetadata(ctx, h, workerPeer, logger)
	if err != nil {
		return nil, fmt.Errorf("request worker metadata: %w", err)
	}
	return metadata, nil
}

func main() {
	// Parse command line flags
	startCmd := flag.NewFlagSet("start", flag.ExitOnError)

	// Initialize configuration
	cfg := config.NewConfiguration()
	cfg.ParseFlags(startCmd)

	if err := startCmd.Parse(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse args: %v\n", err)
		os.Exit(1)
	}

	// Setup logger
	if err := cfg.SetupLogger(); err != nil {
		log.Fatalf("Failed to setup logger: %v", err)
	}
	logger := cfg.GetLogger()
	defer func() {
		if err := logger.Sync(); err != nil {
			fmt.Fprintf(os.Stderr, "failed to sync logger: %v\n", err)
		}
	}()

	if cfg.IsVerbose() {
		logger.Info("Verbose mode enabled")
	}

	logger.Info("Starting DHT server")

	// Determine key path
	keyPath := cfg.KeyPath
	if keyPath == "" {
		defaultPath, err := keys.GetDefaultKeyPath("dht")
		if err != nil {
			logger.Fatal("Failed to get default key path", zap.Error(err))
		}
		keyPath = defaultPath
	}

	// Initialize key manager
	keyManager := keys.NewKeyManager(keyPath, logger)

	// Get or create private key
	privKey, err := keyManager.GetOrCreatePrivateKey()
	if err != nil {
		logger.Fatal("Failed to get or create private key", zap.Error(err))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h, kadDHT, err := newDHTServer(ctx, privKey, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start DHT: %v\n", err)
		os.Exit(1)
	}
	printHostInfo(h, logger)

	// Set up network notifier to detect new connections
	h.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(_ network.Network, conn network.Conn) {
			peerID := conn.RemotePeer().String()
			logger.Info("New peer connected",
				zap.String("peer_id", peerID),
				zap.String("remote_addr", conn.RemoteMultiaddr().String()),
				zap.String("direction", conn.Stat().Direction.String()))
		},
		DisconnectedF: func(_ network.Network, conn network.Conn) {
			logger.Info("Peer disconnected",
				zap.String("peer_id", conn.RemotePeer().String()),
				zap.String("remote_addr", conn.RemoteMultiaddr().String()))
		},
	})

	// Start periodic worker discovery
	go discoverWorkersPeriodically(kadDHT, logger)

	logger.Info("Bootstrapping DHT network")
	if err := kadDHT.Bootstrap(ctx); err != nil {
		logger.Fatal("Failed to bootstrap DHT", zap.Error(err))
	}

	logger.Info("DHT server running. Press Ctrl+C to exit.")
	waitForShutdown(logger)
}

func newDHTServer(ctx context.Context, privKey crypto.PrivKey, logger *zap.Logger) (host.Host, *dht.IpfsDHT, error) {
	logger.Debug("Creating libp2p host with identity",
		zap.Strings("listen_addrs", defaultListenAddrs))

	h, err := libp2p.New(
		libp2p.ListenAddrStrings(defaultListenAddrs...),
		libp2p.Identity(privKey),
	)
	if err != nil {
		logger.Error("Failed to create libp2p host", zap.Error(err))
		return nil, nil, fmt.Errorf("create libp2p host: %w", err)
	}

	logger.Debug("Creating DHT instance", zap.String("mode", "server"))
	kadDHT, err := dht.New(ctx, h, dht.Mode(dht.ModeServer))
	if err != nil {
		logger.Error("Failed to create DHT instance", zap.Error(err))
		return nil, nil, fmt.Errorf("create DHT instance: %w", err)
	}

	logger.Info("DHT server created successfully",
		zap.String("peer_id", h.ID().String()),
		zap.String("dht_mode", "server"))

	return h, kadDHT, nil
}

func printHostInfo(h host.Host, logger *zap.Logger) {
	logger.Info("DHT Server information",
		zap.String("peer_id", h.ID().String()))

	logger.Debug("DHT Server multiaddresses:")
	for _, addr := range h.Addrs() {
		fullAddr := fmt.Sprintf("%s/p2p/%s", addr.String(), h.ID().String())
		logger.Debug("  Listening address", zap.String("address", fullAddr))
	}
}

func waitForShutdown(logger *zap.Logger) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	logger.Info("Shutting down DHT server...")
	time.Sleep(1 * time.Second)
}

func discoverWorkersPeriodically(kadDHT *dht.IpfsDHT, logger *zap.Logger) {
	ticker := time.NewTicker(10 * time.Second) // Run every 10 seconds for testing
	defer ticker.Stop()

	namespaceCID := getWorkerNamespaceCID()
	logger.Info("Starting periodic worker discovery",
		zap.String("namespace_cid", namespaceCID.String()),
		zap.Duration("interval", 10*time.Second))

	for range ticker.C {
		logger.Debug("Searching for workers advertising namespace",
			zap.String("namespace_cid", namespaceCID.String()))

		providers := kadDHT.FindProvidersAsync(context.Background(), namespaceCID, 10)
		workerCount := 0

		for provider := range providers {
			workerCount++
			logger.Info("Found worker",
				zap.String("worker_peer_id", provider.ID.String()),
				zap.Strings("addresses", multiaddrsToStrings(provider.Addrs)))

			// Request metadata from the worker
			metadata, err := requestWorkerMetadata(context.Background(), kadDHT.Host(), provider.ID, logger)
			if err != nil {
				logger.Error("Failed to get metadata from worker",
					zap.String("worker_peer_id", provider.ID.String()),
					zap.Error(err))
			} else {
				logger.Info("Worker metadata retrieved successfully",
					zap.String("worker_peer_id", provider.ID.String()),
					zap.String("gpu_model", metadata.GPUModel),
					zap.Int("vram_gb", metadata.VRAMGB),
					zap.Float64("tokens_throughput", metadata.TokensThroughput),
					zap.Float64("current_load", metadata.Load),
					zap.Strings("supported_models", metadata.SupportedModels),
					zap.Time("last_updated", metadata.LastUpdated))
			}
		}

		if workerCount == 0 {
			logger.Debug("No workers found advertising namespace",
				zap.String("namespace_cid", namespaceCID.String()))
		} else {
			logger.Info("Worker discovery completed",
				zap.String("namespace_cid", namespaceCID.String()),
				zap.Int("total_workers_found", workerCount))
		}
	}
}

// multiaddrsToStrings converts multiaddrs to string slice for logging
func multiaddrsToStrings(addrs []multiaddr.Multiaddr) []string {
	result := make([]string, len(addrs))
	for i, addr := range addrs {
		result[i] = addr.String()
	}
	return result
}
