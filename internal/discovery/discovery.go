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
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

var (
	// advertiseInterval is the interval at which the model is advertised to the DHT:
	advertiseInterval = 10 * time.Second
)

const (
	// defaultBootstrapPeerAddr is the default bootstrap peer address for the DHT:
	// TODO: allow CLI to pass custom peers
	defaultBootstrapPeerAddr = "/ip4/192.168.0.15/tcp/9000/p2p/12D3KooWJzvh2T7Htr1Dr86batqcAf4c5wB8D16zfkM2xJFpoahy"
	// hardcoded dns bootstrap dht:
	// defaultBootstrapPeerAddr = "/dns4/dht.crowdllama.com/tcp/9000/p2p/12D3KooWJB3rAu12osvuqJDo2ncCN8VqQmVkecwgDxxu1AN7fmeR"
)

// NewHostAndDHT creates a libp2p host with DHT
func NewHostAndDHT(ctx context.Context) (host.Host, *dht.IpfsDHT, error) {
	h, err := libp2p.New()
	if err != nil {
		return nil, nil, err
	}

	kadDHT, err := dht.New(ctx, h, dht.Mode(dht.ModeServer))
	if err != nil {
		return nil, nil, err
	}

	return h, kadDHT, nil
}

// BootstrapDHT connects to bootstrap peers. If customPeers is nil, use a local bootstrap address for fast local discovery.
func BootstrapDHT(ctx context.Context, h host.Host, kadDHT *dht.IpfsDHT) error {
	fmt.Println("BootstrapDHT is called")
	var bootstrapPeers []peer.AddrInfo
	fmt.Println("Use harcoded default bootstrap peer addr")
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
		fmt.Printf("%+v\n", peerInfo)
		if err := h.Connect(ctx, peerInfo); err != nil {
			log.Printf("Failed to connect to bootstrap %s: %v", peerInfo.ID, err)
		} else {
			log.Printf("Connected to bootstrap: %s", peerInfo.ID)
		}
	}
	return kadDHT.Bootstrap(ctx)
}

// AdvertiseModel periodically announces model availability
func AdvertiseModel(ctx context.Context, dht *dht.IpfsDHT, namespace string) {
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
			err = dht.Provide(ctx, c, true)
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
