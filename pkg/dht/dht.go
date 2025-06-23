// Package dht provides DHT server functionality for CrowdLlama.
package dht

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"go.uber.org/zap"

	"github.com/matiasinsaurralde/crowdllama/internal/discovery"
	"github.com/matiasinsaurralde/crowdllama/pkg/crowdllama"
)

// DefaultListenAddrs is the default listen addresses for the DHT server
var DefaultListenAddrs = []string{
	"/ip4/0.0.0.0/tcp/9000",
	"/ip4/0.0.0.0/udp/9000/quic-v1",
}

// Server represents a DHT server instance
type Server struct {
	Host      host.Host
	DHT       *dht.IpfsDHT
	logger    *zap.Logger
	ctx       context.Context
	cancel    context.CancelFunc
	peerAddrs []string
}

// NewDHTServer creates a new DHT server instance
func NewDHTServer(ctx context.Context, privKey crypto.PrivKey, logger *zap.Logger) (*Server, error) {
	return NewDHTServerWithAddrs(ctx, privKey, logger, DefaultListenAddrs)
}

// NewDHTServerWithAddrs creates a new DHT server instance with custom listen addresses
func NewDHTServerWithAddrs(ctx context.Context, privKey crypto.PrivKey, logger *zap.Logger, listenAddrs []string) (*Server, error) {
	// If no listen addresses provided, fallback to defaults
	if len(listenAddrs) == 0 {
		listenAddrs = DefaultListenAddrs
		logger.Debug("No listen addresses provided, using defaults", zap.Strings("default_addrs", DefaultListenAddrs))
	}

	logger.Debug("Creating libp2p host with identity",
		zap.Strings("listen_addrs", listenAddrs))

	h, err := libp2p.New(
		libp2p.ListenAddrStrings(listenAddrs...),
		libp2p.Identity(privKey),
	)
	if err != nil {
		logger.Error("Failed to create libp2p host", zap.Error(err))
		return nil, fmt.Errorf("create libp2p host: %w", err)
	}

	logger.Debug("Creating DHT instance", zap.String("mode", "server"))
	kadDHT, err := dht.New(ctx, h, dht.Mode(dht.ModeServer))
	if err != nil {
		logger.Error("Failed to create DHT instance", zap.Error(err))
		return nil, fmt.Errorf("create DHT instance: %w", err)
	}

	// Generate peer addresses in the required format
	peerAddrs := make([]string, 0, len(h.Addrs()))
	for _, addr := range h.Addrs() {
		fullAddr := fmt.Sprintf("%s/p2p/%s", addr.String(), h.ID().String())
		peerAddrs = append(peerAddrs, fullAddr)
	}

	// Ensure we have at least one peer address
	if len(peerAddrs) == 0 {
		logger.Warn("No peer addresses generated, this may indicate a configuration issue")
	}

	serverCtx, cancel := context.WithCancel(ctx)

	server := &Server{
		Host:      h,
		DHT:       kadDHT,
		logger:    logger,
		ctx:       serverCtx,
		cancel:    cancel,
		peerAddrs: peerAddrs,
	}

	logger.Info("DHT server created successfully",
		zap.String("peer_id", h.ID().String()),
		zap.String("dht_mode", "server"),
		zap.Strings("peer_addrs", peerAddrs))

	return server, nil
}

// Start starts the DHT server and returns the primary peer address
func (s *Server) Start() (string, error) {
	// Set up network notifier to detect new connections
	s.Host.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(_ network.Network, conn network.Conn) {
			peerID := conn.RemotePeer().String()
			s.logger.Info("New peer connected",
				zap.String("peer_id", peerID),
				zap.String("remote_addr", conn.RemoteMultiaddr().String()),
				zap.String("direction", conn.Stat().Direction.String()))
		},
		DisconnectedF: func(_ network.Network, conn network.Conn) {
			s.logger.Info("Peer disconnected",
				zap.String("peer_id", conn.RemotePeer().String()),
				zap.String("remote_addr", conn.RemoteMultiaddr().String()))
		},
	})

	// Start periodic worker discovery
	go s.discoverWorkersPeriodically()

	s.logger.Info("Bootstrapping DHT network")
	if err := s.DHT.Bootstrap(s.ctx); err != nil {
		return "", fmt.Errorf("failed to bootstrap DHT: %w", err)
	}

	// Return the first peer address (usually the most accessible one)
	if len(s.peerAddrs) > 0 {
		return s.peerAddrs[0], nil
	}
	return "", fmt.Errorf("no peer addresses available")
}

// Stop stops the DHT server
func (s *Server) Stop() {
	s.logger.Info("Stopping DHT server...")
	s.cancel()
	if err := s.Host.Close(); err != nil {
		s.logger.Error("Failed to close host", zap.Error(err))
	}
}

// GetPeerID returns the DHT server's peer ID
func (s *Server) GetPeerID() string {
	return s.Host.ID().String()
}

// GetPeerAddrs returns all peer addresses in the required format
func (s *Server) GetPeerAddrs() []string {
	return s.peerAddrs
}

// GetPrimaryPeerAddr returns the primary peer address (first in the list)
func (s *Server) GetPrimaryPeerAddr() string {
	if len(s.peerAddrs) > 0 {
		return s.peerAddrs[0]
	}
	return ""
}

// GetPeers returns all connected peer IDs
func (s *Server) GetPeers() []string {
	peers := s.Host.Network().Peers()
	peerIDs := make([]string, 0, len(peers))
	for _, p := range peers {
		peerIDs = append(peerIDs, p.String())
	}
	return peerIDs
}

// HasPeer checks if a specific peer ID is connected
func (s *Server) HasPeer(peerID string) bool {
	peers := s.Host.Network().Peers()
	for _, p := range peers {
		if p.String() == peerID {
			return true
		}
	}
	return false
}

// GetConnectedPeersCount returns the number of connected peers
func (s *Server) GetConnectedPeersCount() int {
	return len(s.Host.Network().Peers())
}

// discoverWorkersPeriodically periodically discovers workers advertising the namespace
func (s *Server) discoverWorkersPeriodically() {
	// Use shorter interval for testing environments
	discoveryInterval := 10 * time.Second
	if os.Getenv("CROW DLLAMA_TEST_MODE") == "1" {
		discoveryInterval = 2 * time.Second
	}

	ticker := time.NewTicker(discoveryInterval) // Run every 2 seconds for testing
	defer ticker.Stop()

	namespaceCID := s.getWorkerNamespaceCID()
	s.logger.Info("Starting periodic worker discovery",
		zap.String("namespace_cid", namespaceCID.String()),
		zap.Duration("interval", discoveryInterval))

	for {
		select {
		case <-ticker.C:
			s.logger.Debug("Searching for workers advertising namespace",
				zap.String("namespace_cid", namespaceCID.String()))

			providers := s.DHT.FindProvidersAsync(context.Background(), namespaceCID, 10)
			workerCount := 0

			for provider := range providers {
				workerCount++
				s.logger.Info("Found worker",
					zap.String("worker_peer_id", provider.ID.String()),
					zap.Strings("addresses", s.multiaddrsToStrings(provider.Addrs)))

				// Request metadata from the worker
				metadata, err := s.requestWorkerMetadata(context.Background(), provider.ID)
				if err != nil {
					s.logger.Error("Failed to get metadata from worker",
						zap.String("worker_peer_id", provider.ID.String()),
						zap.Error(err))
				} else {
					s.logger.Info("Worker metadata retrieved successfully",
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
				s.logger.Debug("No workers found advertising namespace",
					zap.String("namespace_cid", namespaceCID.String()))
			} else {
				s.logger.Info("Worker discovery completed",
					zap.String("namespace_cid", namespaceCID.String()),
					zap.Int("total_workers_found", workerCount))
			}
		case <-s.ctx.Done():
			return
		}
	}
}

// getWorkerNamespaceCID generates the same namespace CID as the worker
func (s *Server) getWorkerNamespaceCID() cid.Cid {
	namespaceCID, err := discovery.GetWorkerNamespaceCID()
	if err != nil {
		panic("Failed to get namespace CID: " + err.Error())
	}
	return namespaceCID
}

// requestWorkerMetadata requests metadata from a worker peer
func (s *Server) requestWorkerMetadata(ctx context.Context, workerPeer peer.ID) (*crowdllama.Resource, error) {
	metadata, err := discovery.RequestWorkerMetadata(ctx, s.Host, workerPeer, s.logger)
	if err != nil {
		return nil, fmt.Errorf("request worker metadata: %w", err)
	}
	return metadata, nil
}

// multiaddrsToStrings converts multiaddrs to string slice for logging
func (s *Server) multiaddrsToStrings(addrs []multiaddr.Multiaddr) []string {
	result := make([]string, len(addrs))
	for i, addr := range addrs {
		result[i] = addr.String()
	}
	return result
}
