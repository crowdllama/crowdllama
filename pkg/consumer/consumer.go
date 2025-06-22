package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

const (
	InferenceProtocol = "/crowdllama/inference/1.0.0"
	DefaultHTTPPort   = 9001
)

// GenerateRequest represents the JSON request structure for the /api/generate endpoint
type GenerateRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

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

type Consumer struct {
	Host   host.Host
	DHT    *dht.IpfsDHT
	logger *zap.Logger
	server *http.Server
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
		Addr:    fmt.Sprintf(":%d", port),
		Handler: loggedMux,
	}

	c.logger.Info("Starting HTTP server", zap.Int("port", port))
	return c.server.ListenAndServe()
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
	return rw.ResponseWriter.Write(b)
}

// StopHTTPServer gracefully stops the HTTP server
func (c *Consumer) StopHTTPServer(ctx context.Context) error {
	if c.server != nil {
		c.logger.Info("Stopping HTTP server")
		return c.server.Shutdown(ctx)
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

// handleGenerate handles the /api/generate endpoint
func (c *Consumer) handleGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse the request
	var req GenerateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.logger.Error("Failed to decode request", zap.Error(err))
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Validate the request
	if req.Model == "" {
		http.Error(w, "Model is required", http.StatusBadRequest)
		return
	}
	if len(req.Messages) == 0 {
		http.Error(w, "No messages provided", http.StatusBadRequest)
		return
	}

	c.logger.Info("Processing generate request",
		zap.String("model", req.Model),
		zap.Any("mesagess", req.Messages),
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
	_, err = c.RequestInference(ctx, bestWorker.PeerID, req.Messages[0].Content)
	if err != nil {
		c.logger.Error("Failed to request inference", zap.Error(err))
		response := GenerateResponse{
			Model: req.Model,
		}
		c.sendJSONResponse(w, response, http.StatusInternalServerError)
		return
	}

	// Send successful response
	generateResponse := GenerateResponse{
		Model: req.Model,
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

// DiscoverWorkers searches for available workers in the DHT
func (c *Consumer) DiscoverWorkers(ctx context.Context) ([]*crowdllama.CrowdLlamaResource, error) {
	return discovery.DiscoverWorkers(ctx, c.DHT, c.logger)
}

// getMetadataFromPeer retrieves metadata from a specific peer using the metadata protocol
func (c *Consumer) getMetadataFromPeer(ctx context.Context, peerID peer.ID) (*crowdllama.CrowdLlamaResource, error) {
	return discovery.RequestWorkerMetadata(ctx, c.Host, peerID, c.logger)
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
