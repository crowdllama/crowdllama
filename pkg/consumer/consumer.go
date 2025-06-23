// Package consumer provides the consumer functionality for CrowdLlama.
package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/ipfs/go-cid"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multihash"
	"go.uber.org/zap"

	"github.com/matiasinsaurralde/crowdllama/internal/discovery"
	"github.com/matiasinsaurralde/crowdllama/pkg/crowdllama"
)

// InferenceProtocol is the protocol identifier for inference requests
const InferenceProtocol = "/crowdllama/inference/1.0.0"

// DefaultHTTPPort is the default HTTP port for the consumer
const DefaultHTTPPort = 9001

// DiscoveryInterval is the interval for worker discovery
const DiscoveryInterval = 10 * time.Second

// WorkerMapTimeout is how long to keep workers in the map
const WorkerMapTimeout = 30 * time.Second

// GenerateRequest represents the JSON request structure for the /api/generate endpoint
type GenerateRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

// Message represents a message sent between consumer and worker
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// GenerateResponse represents the JSON response structure for the /api/generate endpoint
type GenerateResponse struct {
	Model      string    `json:"model"`
	CreatedAt  time.Time `json:"created_at"`
	Message    Message   `json:"message"`
	Stream     bool      `json:"stream"`
	DoneReason string    `json:"done_reason"`
	Done       bool      `json:"done"`
	// WorkerID  string    `json:"worker_id,omitempty"`
	// Timestamp time.Time `json:"timestamp"`
	// Error     string    `json:"error,omitempty"`
}

// Consumer handles inference requests and worker discovery
type Consumer struct {
	Host            host.Host
	DHT             *dht.IpfsDHT
	server          *http.Server
	logger          *zap.Logger
	Resource        *crowdllama.Resource
	workers         map[string]*workerInfo
	workersMutex    sync.RWMutex
	discoveryCtx    context.Context
	discoveryCancel context.CancelFunc
}

// workerInfo holds worker metadata with timestamp for cleanup
type workerInfo struct {
	Resource *crowdllama.Resource
	LastSeen time.Time
}

// NewConsumer creates a new consumer instance
func NewConsumer(ctx context.Context, logger *zap.Logger, privKey crypto.PrivKey) (*Consumer, error) {
	h, kadDHT, err := discovery.NewHostAndDHT(ctx, privKey)
	if err != nil {
		return nil, fmt.Errorf("new host and DHT: %w", err)
	}
	if err := discovery.BootstrapDHT(ctx, h, kadDHT); err != nil {
		return nil, fmt.Errorf("bootstrap DHT: %w", err)
	}

	discoveryCtx, discoveryCancel := context.WithCancel(ctx)

	return &Consumer{
		Host:            h,
		DHT:             kadDHT,
		logger:          logger,
		workers:         make(map[string]*workerInfo),
		discoveryCtx:    discoveryCtx,
		discoveryCancel: discoveryCancel,
	}, nil
}

// StartHTTPServer starts the HTTP server on the specified port
func (c *Consumer) StartHTTPServer(port int) error {
	if port == 0 {
		port = DefaultHTTPPort
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat", c.handleChat)

	// Wrap the mux with logging middleware
	loggedMux := c.loggingMiddleware(mux)

	c.server = &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           loggedMux,
		ReadHeaderTimeout: 30 * time.Second,
	}

	c.logger.Info("Starting HTTP server", zap.Int("port", port))
	if err := c.server.ListenAndServe(); err != nil {
		return fmt.Errorf("listen and serve: %w", err)
	}
	return nil
}

// loggingMiddleware wraps the HTTP handler to log all requests
func (c *Consumer) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Create a response writer wrapper to capture status code
		wrappedWriter := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		// Log the incoming request
		c.logger.Info("HTTP request received",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.String("remote_addr", r.RemoteAddr),
			zap.String("user_agent", r.UserAgent()),
			zap.String("content_type", r.Header.Get("Content-Type")),
			zap.Int64("content_length", r.ContentLength))

		// Call the next handler
		next.ServeHTTP(wrappedWriter, r)

		// Log the response
		duration := time.Since(start)
		c.logger.Info("HTTP request completed",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.String("remote_addr", r.RemoteAddr),
			zap.Int("status_code", wrappedWriter.statusCode),
			zap.Duration("duration", duration))
	})
}

// responseWriter wraps http.ResponseWriter to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	if err != nil {
		return n, fmt.Errorf("write response: %w", err)
	}
	return n, nil
}

// StopHTTPServer gracefully stops the HTTP server
func (c *Consumer) StopHTTPServer(ctx context.Context) error {
	if c.server != nil {
		c.logger.Info("Stopping HTTP server")
		if err := c.server.Shutdown(ctx); err != nil {
			return fmt.Errorf("shutdown server: %w", err)
		}
	}
	return nil
}

// handleChat handles the /api/generate endpoint
func (c *Consumer) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		fmt.Println("Method not allowed")
		return
	}

	// Parse the request
	var req GenerateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.logger.Error("Failed to decode request", zap.Error(err))
		fmt.Println("Failed to decode request")
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Validate the request
	if req.Model == "" {
		http.Error(w, "Model is required", http.StatusBadRequest)
		return
	}

	c.logger.Info("Processing generate request",
		zap.String("model", req.Model),
		zap.Any("messages", req.Messages),
		zap.Bool("stream", req.Stream))

	// Find the best worker for the model
	ctx := r.Context()
	bestWorker, err := c.FindBestWorker(ctx, req.Model)
	if err != nil {
		c.logger.Error("Failed to find suitable worker", zap.Error(err))
		response := GenerateResponse{
			Model: req.Model,
		}
		c.sendJSONResponse(w, response, http.StatusServiceUnavailable)
		return
	}

	// Request inference from the worker
	response, err := c.RequestInference(ctx, bestWorker.PeerID, req.Messages[0].Content)
	if err != nil {
		c.logger.Error("Failed to request inference", zap.Error(err))
		response := GenerateResponse{
			Model: req.Model,
		}
		c.sendJSONResponse(w, response, http.StatusInternalServerError)
		return
	}

	fmt.Printf("RequestInference response: %+v, err = %+v\n", response, err)

	// Send successful response
	generateResponse := GenerateResponse{
		Model:     req.Model,
		CreatedAt: time.Now(),
		Message: Message{
			Role:    "assistant",
			Content: response,
		},
		Stream:     false,
		Done:       true,
		DoneReason: "done",
	}

	c.sendJSONResponse(w, generateResponse, http.StatusOK)
}

// sendJSONResponse sends a JSON response with the specified status code
func (c *Consumer) sendJSONResponse(w http.ResponseWriter, response interface{}, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		c.logger.Error("Failed to encode JSON response", zap.Error(err))
	}
}

// RequestInference sends a string task to a worker and waits for a response
func (c *Consumer) RequestInference(ctx context.Context, workerID, input string) (string, error) {
	fmt.Println("*** RequestInference is called")
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
	defer func() {
		if closeErr := stream.Close(); closeErr != nil {
			c.logger.Warn("failed to close stream", zap.Error(closeErr))
		}
	}()

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

// ListKnownPeersLoop continuously lists known peers for debugging
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

// DiscoverWorkers searches for available workers in the DHT
func (c *Consumer) DiscoverWorkers(ctx context.Context) ([]*crowdllama.Resource, error) {
	workers, err := discovery.DiscoverWorkers(ctx, c.DHT, c.logger)
	if err != nil {
		return nil, fmt.Errorf("discover workers: %w", err)
	}
	return workers, nil
}

// FindBestWorker finds the best available worker based on criteria
func (c *Consumer) FindBestWorker(ctx context.Context, requiredModel string) (*crowdllama.Resource, error) {
	// Try to find a worker from the cached map first
	bestWorker := c.findBestWorkerFromCache(requiredModel)
	if bestWorker != nil {
		return bestWorker, nil
	}

	// If no worker found in cache, wait for discovery with timeout
	c.logger.Info("No suitable worker in cache, waiting for discovery", zap.String("model", requiredModel))

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	timeout := time.After(30 * time.Second) // Wait up to 30 seconds for a worker

	for {
		select {
		case <-ticker.C:
			bestWorker = c.findBestWorkerFromCache(requiredModel)
			if bestWorker != nil {
				c.logger.Info("Found suitable worker after waiting",
					zap.String("worker_id", bestWorker.PeerID),
					zap.String("model", requiredModel))
				return bestWorker, nil
			}
		case <-timeout:
			return nil, fmt.Errorf("timeout waiting for worker supporting model: %s", requiredModel)
		case <-ctx.Done():
			return nil, fmt.Errorf("context canceled while waiting for worker: %w", ctx.Err())
		}
	}
}

// findBestWorkerFromCache finds the best worker from the cached worker map
func (c *Consumer) findBestWorkerFromCache(requiredModel string) *crowdllama.Resource {
	c.workersMutex.RLock()
	defer c.workersMutex.RUnlock()

	if len(c.workers) == 0 {
		return nil
	}

	// Find the best worker based on criteria
	var bestWorker *crowdllama.Resource
	var bestScore float64

	for _, info := range c.workers {
		worker := info.Resource

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

	return bestWorker
}

// DiscoverWorkersViaProviders discovers workers using FindProviders and a namespace-derived CID
func (c *Consumer) DiscoverWorkersViaProviders(ctx context.Context, namespace string) ([]peer.ID, error) {
	// Generate the same CID as the worker
	mh, err := multihash.Sum([]byte(namespace), multihash.IDENTITY, -1)
	if err != nil {
		return nil, fmt.Errorf("create multihash: %w", err)
	}
	contentID := cid.NewCidV1(cid.Raw, mh)

	providers := c.DHT.FindProvidersAsync(ctx, contentID, 10)
	peers := make([]peer.ID, 0, 10) // Preallocate with capacity 10
	for p := range providers {
		peers = append(peers, p.ID)
	}
	return peers, nil
}

// StartBackgroundDiscovery starts the background worker discovery process
func (c *Consumer) StartBackgroundDiscovery() {
	c.logger.Info("Starting background worker discovery")

	go func() {
		ticker := time.NewTicker(DiscoveryInterval)
		defer ticker.Stop()

		// Run initial discovery immediately
		c.runDiscovery()

		for {
			select {
			case <-ticker.C:
				c.runDiscovery()
			case <-c.discoveryCtx.Done():
				c.logger.Info("Background discovery stopped")
				return
			}
		}
	}()

	// Start cleanup goroutine
	go c.cleanupStaleWorkers()
}

// runDiscovery performs a single discovery run and updates the worker map
func (c *Consumer) runDiscovery() {
	ctx, cancel := context.WithTimeout(c.discoveryCtx, 10*time.Second)
	defer cancel()

	workers, err := c.DiscoverWorkers(ctx)
	if err != nil {
		c.logger.Warn("Background discovery failed", zap.Error(err))
		return
	}

	c.workersMutex.Lock()
	defer c.workersMutex.Unlock()

	now := time.Now()
	updatedCount := 0

	for _, worker := range workers {
		workerID := worker.PeerID
		c.workers[workerID] = &workerInfo{
			Resource: worker,
			LastSeen: now,
		}
		updatedCount++
	}

	if updatedCount > 0 {
		c.logger.Info("Background discovery updated workers",
			zap.Int("updated_count", updatedCount),
			zap.Int("total_workers", len(c.workers)))
	}
}

// cleanupStaleWorkers removes workers that haven't been seen recently
func (c *Consumer) cleanupStaleWorkers() {
	ticker := time.NewTicker(WorkerMapTimeout)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.workersMutex.Lock()
			now := time.Now()
			removedCount := 0

			for workerID, info := range c.workers {
				if now.Sub(info.LastSeen) > WorkerMapTimeout {
					delete(c.workers, workerID)
					removedCount++
				}
			}

			if removedCount > 0 {
				c.logger.Info("Cleaned up stale workers",
					zap.Int("removed_count", removedCount),
					zap.Int("remaining_workers", len(c.workers)))
			}
			c.workersMutex.Unlock()

		case <-c.discoveryCtx.Done():
			return
		}
	}
}

// GetAvailableWorkers returns a copy of the current worker map
func (c *Consumer) GetAvailableWorkers() map[string]*crowdllama.Resource {
	c.workersMutex.RLock()
	defer c.workersMutex.RUnlock()

	result := make(map[string]*crowdllama.Resource)
	for workerID, info := range c.workers {
		result[workerID] = info.Resource
	}
	return result
}

// StopBackgroundDiscovery stops the background discovery process
func (c *Consumer) StopBackgroundDiscovery() {
	if c.discoveryCancel != nil {
		c.discoveryCancel()
	}
}
