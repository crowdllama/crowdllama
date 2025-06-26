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

	"github.com/matiasinsaurralde/crowdllama/internal/discovery"
	"github.com/matiasinsaurralde/crowdllama/pkg/consumer"
	"github.com/matiasinsaurralde/crowdllama/pkg/crowdllama"
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
	Host      host.Host
	DHT       *dht.IpfsDHT
	Metadata  *crowdllama.Resource
	OllamaURL string // Configurable Ollama URL for testing

	// Metadata update management
	metadataCtx    context.Context
	metadataCancel context.CancelFunc
}

// handleInferenceRequest processes an inference request from a consumer
func handleInferenceRequest(ctx context.Context, s network.Stream, ollamaURL string) {
	defer func() {
		if err := s.Close(); err != nil {
			log.Printf("failed to close stream: %v", err)
		}
	}()

	fmt.Println("StreamHandler is called")

	input, err := readInferenceInput(s)
	if err != nil {
		log.Printf("Failed to read inference input: %v", err)
		return
	}

	output, err := callOllamaAPI(ctx, input, ollamaURL)
	if err != nil {
		log.Printf("Failed to call Ollama API: %v", err)
		return
	}

	if err := writeInferenceResponse(s, output); err != nil {
		log.Printf("Failed to write inference response: %v", err)
		return
	}

	log.Printf("Worker sent response: %s", output)
	fmt.Println("StreamHandler completed")
}

func readInferenceInput(s network.Stream) (string, error) {
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

func callOllamaAPI(ctx context.Context, input, ollamaURL string) (string, error) {
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

	if ollamaURL == "" {
		ollamaURL = "http://localhost:11434/api/chat"
	}

	req, err := http.NewRequestWithContext(ctx, "POST", ollamaURL, bytes.NewBuffer(reqBody))
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

func writeInferenceResponse(s network.Stream, output string) error {
	responseBytes := []byte(output)
	log.Printf("Worker writing %d bytes: %s", len(responseBytes), output)

	_, err := s.Write(responseBytes)
	if err != nil {
		return fmt.Errorf("failed to write response: %w", err)
	}

	return nil
}

// NewWorker creates a new worker instance
func NewWorker(ctx context.Context, privKey crypto.PrivKey) (*Worker, error) {
	return NewWorkerWithBootstrapPeers(ctx, privKey, nil)
}

// NewWorkerWithBootstrapPeers creates a new worker instance with custom bootstrap peers
func NewWorkerWithBootstrapPeers(ctx context.Context, privKey crypto.PrivKey, bootstrapPeers []string) (*Worker, error) {
	return NewWorkerWithBootstrapPeersAndOllamaURL(ctx, privKey, bootstrapPeers, "")
}

// NewWorkerWithBootstrapPeersAndOllamaURL creates a new worker instance with custom bootstrap peers and Ollama URL
func NewWorkerWithBootstrapPeersAndOllamaURL(
	ctx context.Context,
	privKey crypto.PrivKey,
	bootstrapPeers []string,
	ollamaURL string,
) (*Worker, error) {
	h, kadDHT, err := discovery.NewHostAndDHT(ctx, privKey)
	if err != nil {
		return nil, fmt.Errorf("new host and DHT: %w", err)
	}

	// Bootstrap with custom peers if provided, otherwise use defaults
	if len(bootstrapPeers) > 0 {
		if err := discovery.BootstrapDHTWithPeers(ctx, h, kadDHT, bootstrapPeers); err != nil {
			return nil, fmt.Errorf("bootstrap DHT with custom peers: %w", err)
		}
	} else {
		if err := discovery.BootstrapDHT(ctx, h, kadDHT); err != nil {
			return nil, fmt.Errorf("bootstrap DHT: %w", err)
		}
	}

	fmt.Println("BootstrapDHT ok")
	h.SetStreamHandler(consumer.InferenceProtocol, func(s network.Stream) {
		handleInferenceRequest(ctx, s, ollamaURL)
	})

	// Initialize metadata
	metadata := crowdllama.NewCrowdLlamaResource(h.ID().String())

	// Create metadata context for managing metadata updates
	metadataCtx, metadataCancel := context.WithCancel(ctx)

	return &Worker{
		Host:           h,
		DHT:            kadDHT,
		Metadata:       metadata,
		OllamaURL:      ollamaURL,
		metadataCtx:    metadataCtx,
		metadataCancel: metadataCancel,
	}, nil
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

	log.Printf("Updated worker metadata - Models: %v, Throughput: %.1f tokens/sec, VRAM: %dGB, Load: %.1f, GPU: %s",
		models, tokensThroughput, vramGB, load, gpuModel)

	return nil
}

// StartMetadataUpdates starts periodic metadata updates
func (w *Worker) StartMetadataUpdates() {
	go func() {
		// Use shorter interval for testing environments
		updateInterval := MetadataUpdateInterval
		if os.Getenv("CROW DLLAMA_TEST_MODE") == "1" {
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
}

// PublishMetadata publishes the worker's metadata to the DHT
func (w *Worker) PublishMetadata(ctx context.Context) error {
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
func (w *Worker) AdvertiseModel(ctx context.Context, _ string) {
	go func() {
		// Use shorter interval for testing environments
		advertiseInterval := 1 * time.Second
		if os.Getenv("CROW DLLAMA_TEST_MODE") == "1" {
			advertiseInterval = 500 * time.Millisecond
		}

		ticker := time.NewTicker(advertiseInterval)
		defer ticker.Stop()

		// Get the namespace CID using the unified function
		namespaceCID, err := discovery.GetWorkerNamespaceCID()
		if err != nil {
			panic("Failed to get namespace CID: " + err.Error())
		}

		for {
			select {
			case <-ticker.C:
				// Advertise the worker using Provide
				err := w.DHT.Provide(ctx, namespaceCID, true)
				if err != nil {
					log.Printf("Failed to advertise worker: %v", err)
				} else {
					log.Printf("Worker advertised with CID: %s", namespaceCID.String())
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

// BootstrapWithPeer bootstraps the worker's DHT with a peer
func BootstrapWithPeer(ctx context.Context, w *Worker) error {
	if err := discovery.BootstrapDHT(ctx, w.Host, w.DHT); err != nil {
		return fmt.Errorf("bootstrap DHT: %w", err)
	}
	return nil
}
