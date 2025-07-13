// Package worker provides the worker functionality for CrowdLlama.
package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/ipfs/go-cid"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/multiformats/go-multihash"

	"github.com/crowdllama/crowdllama/internal/discovery"
	"github.com/crowdllama/crowdllama/pkg/config"
	"github.com/crowdllama/crowdllama/pkg/consumer"
	"github.com/crowdllama/crowdllama/pkg/crowdllama"
	"github.com/crowdllama/crowdllama/pkg/version"
)

// MetadataUpdateInterval is the interval at which worker metadata is updated
var MetadataUpdateInterval = 30 * time.Second

// SetMetadataUpdateInterval allows programmatically setting the metadata update interval
func SetMetadataUpdateInterval(interval time.Duration) {
	MetadataUpdateInterval = interval
}

// GetMetadataUpdateInterval returns the current metadata update interval
func GetMetadataUpdateInterval() time.Duration {
	return MetadataUpdateInterval
}

// OllamaRequest represents the request structure for Ollama API
type OllamaRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

// Message represents a message in the Ollama API
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// OllamaResponse represents the response structure from Ollama API
type OllamaResponse struct {
	Model      string    `json:"model"`
	CreatedAt  time.Time `json:"created_at"`
	Message    Message   `json:"message"`
	Stream     bool      `json:"stream"`
	DoneReason string    `json:"done_reason"`
	Done       bool      `json:"done"`
}

// Worker represents a CrowdLlama worker node
type Worker struct {
	Host     host.Host
	DHT      *dht.IpfsDHT
	Metadata *crowdllama.Resource
	Config   *config.Configuration // Configuration for the worker

	// Metadata update management
	metadataCtx    context.Context
	metadataCancel context.CancelFunc

	// Advertising management
	advertisingCtx    context.Context
	advertisingCancel context.CancelFunc

	// Bootstrap peers for reconnection
	bootstrapPeers []string
}

// handleInferenceRequest processes an inference request from a consumer
func (w *Worker) handleInferenceRequest(ctx context.Context, s network.Stream) {
	defer func() {
		if err := s.Close(); err != nil {
			log.Printf("failed to close stream: %v", err)
		}
	}()

	input, err := w.readInferenceInput(s)
	if err != nil {
		log.Printf("Failed to read inference input: %v", err)
		return
	}

	output, err := w.callOllamaAPI(ctx, input, "/api/chat")
	if err != nil {
		log.Printf("Failed to call Ollama API: %v", err)
		return
	}

	if err := w.writeInferenceResponse(s, output); err != nil {
		log.Printf("Failed to write inference response: %v", err)
		return
	}

	log.Printf("Worker sent response: %s", output)
}

func (w *Worker) readInferenceInput(s network.Stream) (string, error) {
	if err := s.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return "", fmt.Errorf("failed to set read deadline: %w", err)
	}

	buf := make([]byte, 1024)
	n, err := s.Read(buf)
	if err != nil {
		return "", fmt.Errorf("failed to read from stream: %w", err)
	}

	input := string(buf[:n])
	log.Printf("Worker received inference request (%d bytes): %s", n, input)
	return input, nil
}

func (w *Worker) callOllamaAPI(ctx context.Context, input, apiPath string) (string, error) {
	ollamaReq := OllamaRequest{
		Model: "tinyllama",
		Messages: []Message{
			{
				Role:    "user",
				Content: input,
			},
		},
		Stream: false,
	}

	reqBody, err := json.Marshal(ollamaReq)
	if err != nil {
		return "", fmt.Errorf("failed to marshal Ollama request: %w", err)
	}

	baseURL := w.Config.GetOllamaBaseURL()
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}

	fullURL := baseURL + apiPath
	req, err := http.NewRequestWithContext(ctx, "POST", fullURL, bytes.NewBuffer(reqBody))
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to make HTTP request to Ollama: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("failed to close response body: %v", closeErr)
		}
	}()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read Ollama response: %w", err)
	}

	var ollamaResp OllamaResponse
	if err := json.Unmarshal(respBody, &ollamaResp); err != nil {
		return "", fmt.Errorf("failed to unmarshal Ollama response: %w", err)
	}

	output := ollamaResp.Message.Content
	if output == "" {
		output = "No response content received from Ollama"
	}

	return output, nil
}

func (w *Worker) writeInferenceResponse(s network.Stream, output string) error {
	responseBytes := []byte(output)
	log.Printf("Worker writing %d bytes: %s", len(responseBytes), output)

	_, err := s.Write(responseBytes)
	if err != nil {
		return fmt.Errorf("failed to write response: %w", err)
	}

	return nil
}

// NewWorkerWithConfig creates a new worker instance using the provided configuration
func NewWorkerWithConfig(
	ctx context.Context,
	privKey crypto.PrivKey,
	cfg *config.Configuration,
) (*Worker, error) {
	h, kadDHT, err := discovery.NewHostAndDHT(ctx, privKey)
	if err != nil {
		return nil, fmt.Errorf("new host and DHT: %w", err)
	}

	// Bootstrap with custom peers if provided, otherwise use defaults
	if len(cfg.BootstrapPeers) > 0 {
		if err := discovery.BootstrapDHTWithPeers(ctx, h, kadDHT, cfg.BootstrapPeers); err != nil {
			return nil, fmt.Errorf("bootstrap DHT with custom peers: %w", err)
		}
	} else {
		if err := discovery.BootstrapDHT(ctx, h, kadDHT); err != nil {
			return nil, fmt.Errorf("bootstrap DHT: %w", err)
		}
	}

	// Initialize metadata
	metadata := crowdllama.NewCrowdLlamaResource(h.ID().String())

	// Create metadata context for managing metadata updates
	metadataCtx, metadataCancel := context.WithCancel(ctx)

	// Create advertising context for managing advertising
	advertisingCtx, advertisingCancel := context.WithCancel(ctx)

	worker := &Worker{
		Host:              h,
		DHT:               kadDHT,
		Metadata:          metadata,
		Config:            cfg,
		metadataCtx:       metadataCtx,
		metadataCancel:    metadataCancel,
		advertisingCtx:    advertisingCtx,
		advertisingCancel: advertisingCancel,
		bootstrapPeers:    cfg.BootstrapPeers,
	}

	// Set up stream handler with the worker instance
	h.SetStreamHandler(consumer.InferenceProtocol, func(s network.Stream) {
		worker.handleInferenceRequest(ctx, s)
	})

	return worker, nil
}

// NewWorker creates a new worker instance
func NewWorker(ctx context.Context, privKey crypto.PrivKey, cfg *config.Configuration) (*Worker, error) {
	return NewWorkerWithConfig(ctx, privKey, cfg)
}

// SetupMetadataHandler sets up the metadata request handler
func (w *Worker) SetupMetadataHandler() {
	log.Printf("Setting up metadata handler for protocol: %s", crowdllama.MetadataProtocol)

	w.Host.SetStreamHandler(crowdllama.MetadataProtocol, func(s network.Stream) {
		defer func() {
			if err := s.Close(); err != nil {
				log.Printf("failed to close stream: %v", err)
			}
		}()
		log.Printf("Worker received metadata request from %s", s.Conn().RemotePeer().String())

		// Serialize metadata to JSON
		metadataJSON, err := w.Metadata.ToJSON()
		if err != nil {
			log.Printf("Failed to serialize metadata: %v", err)
			return
		}

		log.Printf("Worker sending metadata (%d bytes): %s", len(metadataJSON), string(metadataJSON))

		// Send metadata response
		_, err = s.Write(metadataJSON)
		if err != nil {
			log.Printf("Failed to send metadata: %v", err)
			return
		}

		// Close the stream after writing to signal EOF
		log.Printf("Worker sent metadata successfully, closing stream")
	})

	log.Printf("Metadata handler setup complete")
}

// UpdateMetadata queries Ollama API and updates the worker's internal metadata
func (w *Worker) UpdateMetadata() error {
	// For now, use hardcoded values as requested
	// TODO: In the future, this could query Ollama API for actual model information

	// Hardcoded metadata values
	models := []string{"llama-2-7b", "llama-2-13b", "mistral-7b", "tinyllama"}
	tokensThroughput := 150.0 // tokens/sec
	vramGB := 24              // VRAM GB
	load := 0.3               // current load (0.0 to 1.0)
	gpuModel := "RTX 4090"

	// Update the metadata
	w.Metadata.SupportedModels = models
	w.Metadata.TokensThroughput = tokensThroughput
	w.Metadata.VRAMGB = vramGB
	w.Metadata.Load = load
	w.Metadata.GPUModel = gpuModel
	w.Metadata.LastUpdated = time.Now()
	w.Metadata.Version = version.CommitHash // Set the CrowdLlama version

	log.Printf("Updated worker metadata - Models: %v, Throughput: %.1f tokens/sec, VRAM: %dGB, Load: %.1f, GPU: %s, Version: %s",
		models, tokensThroughput, vramGB, load, gpuModel, w.Metadata.Version)

	return nil
}

// StartMetadataUpdates starts periodic metadata updates
func (w *Worker) StartMetadataUpdates() {
	go func() {
		// Use shorter interval for testing environments
		updateInterval := MetadataUpdateInterval
		if os.Getenv("CROWDLLAMA_TEST_MODE") == "1" {
			updateInterval = 5 * time.Second
		}

		ticker := time.NewTicker(updateInterval)
		defer ticker.Stop()

		// Run initial update immediately
		if err := w.UpdateMetadata(); err != nil {
			log.Printf("Failed to perform initial metadata update: %v", err)
		}

		for {
			select {
			case <-ticker.C:
				if err := w.UpdateMetadata(); err != nil {
					log.Printf("Failed to update metadata: %v", err)
				}
			case <-w.metadataCtx.Done():
				log.Printf("Metadata update loop stopped")
				return
			}
		}
	}()
}

// StopMetadataUpdates stops the periodic metadata updates
func (w *Worker) StopMetadataUpdates() {
	if w.metadataCancel != nil {
		w.metadataCancel()
	}
	// Stop advertising to the DHT
	w.stopAdvertising()
	// Remove metadata handler
	w.removeMetadataHandler()
}

// removeMetadataHandler removes the metadata protocol handler
func (w *Worker) removeMetadataHandler() {
	w.Host.RemoveStreamHandler(crowdllama.MetadataProtocol)
	log.Printf("Removed metadata handler for protocol: %s", crowdllama.MetadataProtocol)
}

// PublishMetadata publishes the worker's metadata to the DHT
func (w *Worker) PublishMetadata(ctx context.Context) error {
	// Check if DHT is connected before attempting to publish
	if !w.IsDHTConnected() {
		log.Printf("DHT is disconnected, attempting to reconnect to bootstrap peers...")
		if err := w.AttemptBootstrapReconnection(ctx); err != nil {
			return fmt.Errorf("failed to reconnect to bootstrap peers: %w", err)
		}

		// Give the DHT a moment to establish connections
		time.Sleep(2 * time.Second)

		// Check again if reconnection was successful
		if !w.IsDHTConnected() {
			return fmt.Errorf("DHT is still disconnected after reconnection attempt")
		}
	}

	data, err := w.Metadata.ToJSON()
	if err != nil {
		return fmt.Errorf("failed to serialize metadata: %w", err)
	}

	// Create a CID from the metadata
	mh, err := multihash.Sum(data, multihash.SHA2_256, -1)
	if err != nil {
		return fmt.Errorf("failed to create multihash: %w", err)
	}

	c := cid.NewCidV1(cid.Raw, mh)

	// Use Provide instead of PutValue
	err = w.DHT.Provide(ctx, c, true)
	if err != nil {
		return fmt.Errorf("failed to provide metadata to DHT: %w", err)
	}

	log.Printf("Published metadata to DHT with CID: %s", c.String())
	return nil
}

// AdvertiseModel periodically announces model availability and advertises the worker using Provide
func (w *Worker) AdvertiseModel(_ context.Context, namespace string) {
	go func() {
		// Use shorter interval for testing environments
		advertiseInterval := 1 * time.Second
		if os.Getenv("CROWDLLAMA_TEST_MODE") == "1" {
			advertiseInterval = 500 * time.Millisecond
		}

		ticker := time.NewTicker(advertiseInterval)
		defer ticker.Stop()

		// Get the namespace CID using the provided namespace parameter
		mh, err := multihash.Sum([]byte(namespace), multihash.IDENTITY, -1)
		if err != nil {
			panic("Failed to create multihash for namespace: " + err.Error())
		}
		namespaceCID := cid.NewCidV1(cid.Raw, mh)

		log.Printf("Worker advertising with namespace: %s, CID: %s", namespace, namespaceCID.String())

		for {
			select {
			case <-ticker.C:
				// Check if DHT is connected before attempting to advertise
				if !w.IsDHTConnected() {
					log.Printf("DHT is disconnected, attempting to reconnect to bootstrap peers...")
					if err := w.AttemptBootstrapReconnection(w.advertisingCtx); err != nil {
						log.Printf("Failed to reconnect to bootstrap peers: %v", err)
						continue // Skip this advertising cycle and try again next time
					}

					// Give the DHT a moment to establish connections
					time.Sleep(2 * time.Second)

					// Check again if reconnection was successful
					if !w.IsDHTConnected() {
						log.Printf("DHT is still disconnected after reconnection attempt, skipping advertisement")
						continue // Skip this advertising cycle and try again next time
					}
				}

				// Advertise the worker using Provide
				err := w.DHT.Provide(w.advertisingCtx, namespaceCID, true)
				if err != nil {
					log.Printf("Failed to advertise worker: %v", err)
				} else {
					log.Printf("Worker advertised with CID: %s", namespaceCID.String())
				}
			case <-w.advertisingCtx.Done():
				log.Printf("Worker advertising stopped")
				return
			}
		}
	}()
}

// stopAdvertising stops the advertising process
func (w *Worker) stopAdvertising() {
	if w.advertisingCancel != nil {
		w.advertisingCancel()
	}
}

// BootstrapWithPeer bootstraps the worker's DHT with a peer
func BootstrapWithPeer(ctx context.Context, w *Worker) error {
	if err := discovery.BootstrapDHT(ctx, w.Host, w.DHT); err != nil {
		return fmt.Errorf("bootstrap DHT: %w", err)
	}
	return nil
}

// IsDHTConnected checks if the DHT has any peers in its routing table
func (w *Worker) IsDHTConnected() bool {
	// Check if we have any peers in the DHT routing table
	peers := w.DHT.RoutingTable().ListPeers()
	return len(peers) > 0
}

// AttemptBootstrapReconnection attempts to reconnect to bootstrap peers
func (w *Worker) AttemptBootstrapReconnection(ctx context.Context) error {
	log.Printf("Attempting to reconnect to bootstrap peers...")

	// Use custom bootstrap peers if configured, otherwise use defaults
	if len(w.bootstrapPeers) > 0 {
		if err := discovery.BootstrapDHTWithPeers(ctx, w.Host, w.DHT, w.bootstrapPeers); err != nil {
			return fmt.Errorf("failed to reconnect to custom bootstrap peers: %w", err)
		}
	} else {
		if err := discovery.BootstrapDHT(ctx, w.Host, w.DHT); err != nil {
			return fmt.Errorf("failed to reconnect to default bootstrap peers: %w", err)
		}
	}

	log.Printf("Successfully reconnected to bootstrap peers")
	return nil
}
