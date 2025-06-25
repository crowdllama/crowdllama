// Package dht provides DHT server functionality for CrowdLlama.
package dht

import (
	"context"
	"fmt"
	"os"
	"strings"
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
	// Set up network notifier to detect new connections and NAT traversal
	s.Host.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(_ network.Network, conn network.Conn) {
			peerID := conn.RemotePeer().String()
			remoteAddr := conn.RemoteMultiaddr().String()
			direction := conn.Stat().Direction.String()

			// Log connection details
			s.logger.Debug("New peer connected",
				zap.String("peer_id", peerID),
				zap.String("remote_addr", remoteAddr),
				zap.String("direction", direction),
				zap.String("transport", conn.RemoteMultiaddr().Protocols()[0].Name),
				zap.Bool("is_relay", isRelayConnection(remoteAddr)),
				zap.Bool("is_hole_punched", isHolePunchedConnection(remoteAddr, direction)))

			// Log NAT traversal information
			if isRelayConnection(remoteAddr) {
				s.logger.Debug("Connection established via relay (NAT traversal)",
					zap.String("peer_id", peerID),
					zap.String("relay_addr", remoteAddr))
			} else if isHolePunchedConnection(remoteAddr, direction) {
				s.logger.Debug("Direct connection established (hole punching successful)",
					zap.String("peer_id", peerID),
					zap.String("direct_addr", remoteAddr))
			} else {
				s.logger.Debug("Direct connection established (no NAT)",
					zap.String("peer_id", peerID),
					zap.String("direct_addr", remoteAddr))
			}
		},
		DisconnectedF: func(_ network.Network, conn network.Conn) {
			s.logger.Info("Peer disconnected",
				zap.String("peer_id", conn.RemotePeer().String()),
				zap.String("remote_addr", conn.RemoteMultiaddr().String()))
		},
		ListenF: func(_ network.Network, addr multiaddr.Multiaddr) {
			s.logger.Debug("Started listening on address",
				zap.String("listen_addr", addr.String()))
		},
		ListenCloseF: func(_ network.Network, addr multiaddr.Multiaddr) {
			s.logger.Debug("Stopped listening on address",
				zap.String("listen_addr", addr.String()))
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

// GetNATStatus returns information about NAT traversal and connection types
func (s *Server) GetNATStatus() map[string]interface{} {
	connections := s.Host.Network().Conns()

	stats := map[string]interface{}{
		"total_connections":        len(connections),
		"direct_connections":       0,
		"relay_connections":        0,
		"hole_punched_connections": 0,
		"local_connections":        0,
		"external_connections":     0,
	}

	for _, conn := range connections {
		addr := conn.RemoteMultiaddr().String()
		direction := conn.Stat().Direction.String()

		if isRelayConnection(addr) {
			stats["relay_connections"] = stats["relay_connections"].(int) + 1
		} else if isHolePunchedConnection(addr, direction) {
			stats["hole_punched_connections"] = stats["hole_punched_connections"].(int) + 1
			stats["external_connections"] = stats["external_connections"].(int) + 1
		} else if isExternalIP(addr) {
			stats["direct_connections"] = stats["direct_connections"].(int) + 1
			stats["external_connections"] = stats["external_connections"].(int) + 1
		} else {
			stats["local_connections"] = stats["local_connections"].(int) + 1
		}
	}

	return stats
}

// LogNATStatus logs current NAT traversal statistics
func (s *Server) LogNATStatus() {
	stats := s.GetNATStatus()
	s.logger.Debug("NAT traversal statistics",
		zap.Int("total_connections", stats["total_connections"].(int)),
		zap.Int("direct_connections", stats["direct_connections"].(int)),
		zap.Int("relay_connections", stats["relay_connections"].(int)),
		zap.Int("hole_punched_connections", stats["hole_punched_connections"].(int)),
		zap.Int("local_connections", stats["local_connections"].(int)),
		zap.Int("external_connections", stats["external_connections"].(int)))
}

// discoverWorkersPeriodically periodically discovers workers advertising the namespace
//
//nolint:funlen // This function handles multiple periodic tasks and is appropriately long
func (s *Server) discoverWorkersPeriodically() {
	// Use shorter interval for testing environments
	discoveryInterval := 10 * time.Second
	if os.Getenv("CROW DLLAMA_TEST_MODE") == "1" {
		discoveryInterval = 2 * time.Second
	}

	ticker := time.NewTicker(discoveryInterval) // Run every 2 seconds for testing
	defer ticker.Stop()

	// Log NAT status every 30 seconds (or 10 seconds in test mode)
	natLogInterval := 30 * time.Second
	if os.Getenv("CROW DLLAMA_TEST_MODE") == "1" {
		natLogInterval = 10 * time.Second
	}
	natTicker := time.NewTicker(natLogInterval)
	defer natTicker.Stop()

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
		case <-natTicker.C:
			// Log NAT traversal statistics periodically
			s.LogNATStatus()
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

// isRelayConnection checks if a connection is going through a relay
func isRelayConnection(addr string) bool {
	return strings.Contains(addr, "/p2p-circuit/")
}

// isHolePunchedConnection checks if a connection is likely hole punched
func isHolePunchedConnection(addr, direction string) bool {
	// Hole punched connections are typically:
	// 1. Inbound connections (other peer initiated)
	// 2. Not relay connections
	// 3. From external IP addresses (not localhost/private ranges)

	if isRelayConnection(addr) {
		return false
	}

	// Check if it's an inbound connection
	if direction != "Inbound" {
		return false
	}

	// Check if it's from an external IP (not localhost or private range)
	return isExternalIP(addr)
}

// isExternalIP checks if an address is from an external IP
func isExternalIP(addr string) bool {
	// Extract IP from multiaddr
	if strings.Contains(addr, "/ip4/127.0.0.1/") ||
		strings.Contains(addr, "/ip4/192.168.") ||
		strings.Contains(addr, "/ip4/10.") ||
		strings.Contains(addr, "/ip4/172.") ||
		strings.Contains(addr, "/ip6/::1/") ||
		strings.Contains(addr, "/ip6/fe80:") {
		return false
	}
	return true
}
