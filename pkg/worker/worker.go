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
}

// handleInferenceRequest processes an inference request from a consumer
func handleInferenceRequest(ctx context.Context, s network.Stream) {
	defer func() {
		if err := s.Close(); err != nil {
			log.Printf("failed to close stream: %v", err)
		}
	}()

	fmt.Println("StreamHandler is called")

	if err := s.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		log.Printf("failed to set read deadline: %v", err)
	}

	// Read the input data with a fixed buffer
	buf := make([]byte, 1024)
	n, err := s.Read(buf)
	if err != nil {
		log.Printf("Failed to read from stream: %v", err)
		return
	}

	input := string(buf[:n])
	log.Printf("Worker received inference request (%d bytes): %s", n, input)

	// Prepare Ollama API request
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

	// Serialize request to JSON
	reqBody, err := json.Marshal(ollamaReq)
	if err != nil {
		log.Printf("Failed to marshal Ollama request: %v", err)
		return
	}

	// Make HTTP POST request to Ollama API
	req, err := http.NewRequestWithContext(ctx, "POST", "http://localhost:11434/api/chat", bytes.NewBuffer(reqBody))
	if err != nil {
		log.Printf("Failed to create HTTP request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("Failed to make HTTP request to Ollama: %v", err)
		return
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("failed to close response body: %v", closeErr)
		}
	}()

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Failed to read Ollama response: %v", err)
		return
	}

	// Parse Ollama response
	var ollamaResp OllamaResponse
	if unmarshalErr := json.Unmarshal(respBody, &ollamaResp); unmarshalErr != nil {
		log.Printf("Failed to unmarshal Ollama response: %v", unmarshalErr)
		return
	}

	// Extract the response content
	output := ollamaResp.Message.Content
	if output == "" {
		output = "No response content received from Ollama"
	}

	// Write the response
	responseBytes := []byte(output)
	log.Printf("Worker writing %d bytes: %s", len(responseBytes), output)
	for i, b := range responseBytes {
		log.Printf("Writing byte %d: '%s' (ASCII: %d)", i, string(b), b)
	}

	_, err = s.Write(responseBytes)
	if err != nil {
		log.Printf("Failed to write response: %v", err)
		return
	}

	log.Printf("Worker sent response: %s", output)
	fmt.Println("StreamHandler completed")
}

// NewWorker creates a new worker instance
func NewWorker(ctx context.Context, privKey crypto.PrivKey) (*Worker, error) {
	h, kadDHT, err := discovery.NewHostAndDHT(ctx, privKey)
	if err != nil {
		return nil, fmt.Errorf("new host and DHT: %w", err)
	}
	if err := discovery.BootstrapDHT(ctx, h, kadDHT); err != nil {
		return nil, fmt.Errorf("bootstrap DHT: %w", err)
	}

	fmt.Println("BootstrapDHT ok")
	h.SetStreamHandler(consumer.InferenceProtocol, func(s network.Stream) {
		handleInferenceRequest(ctx, s)
	})

	// Initialize metadata
	metadata := crowdllama.NewCrowdLlamaResource(h.ID().String())

	return &Worker{
		Host:     h,
		DHT:      kadDHT,
		Metadata: metadata,
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

// UpdateMetadata updates the worker's metadata
func (w *Worker) UpdateMetadata(models []string, tokensThroughput float64, vramGB int, load float64, gpuModel string) {
	w.Metadata.SupportedModels = models
	w.Metadata.TokensThroughput = tokensThroughput
	w.Metadata.VRAMGB = vramGB
	w.Metadata.Load = load
	w.Metadata.GPUModel = gpuModel
	w.Metadata.LastUpdated = time.Now()
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
		ticker := time.NewTicker(3 * time.Second)
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
