// Package gateway provides HTTP API functionality for CrowdLlama.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"

	"github.com/crowdllama/crowdllama/internal/discovery"
	"github.com/crowdllama/crowdllama/pkg/crowdllama"
	peerpkg "github.com/crowdllama/crowdllama/pkg/peer"
)

// InferenceProtocol is the protocol identifier for inference requests
const InferenceProtocol = "/crowdllama/inference/1.0.0"

// DefaultHTTPPort is the default HTTP port for the gateway
const DefaultHTTPPort = 9001

// DiscoveryInterval is the interval for worker discovery
const DiscoveryInterval = 10 * time.Second

// GenerateRequest represents the JSON request structure for the /api/chat endpoint
type GenerateRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

// Message represents a message sent between gateway and worker
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// GenerateResponse represents the JSON response structure for the /api/chat endpoint
type GenerateResponse struct {
	Model      string    `json:"model"`
	CreatedAt  time.Time `json:"created_at"`
	Message    Message   `json:"message"`
	Stream     bool      `json:"stream"`
	DoneReason string    `json:"done_reason"`
	Done       bool      `json:"done"`
}

// Gateway handles HTTP API requests and forwards them to workers in the P2P network
type Gateway struct {
	peer            *peerpkg.Peer
	server          *http.Server
	logger          *zap.Logger
	discoveryCtx    context.Context
	discoveryCancel context.CancelFunc
}

// NewGateway creates a new gateway instance using an existing Peer
func NewGateway(
	ctx context.Context,
	logger *zap.Logger,
	p *peerpkg.Peer,
) (*Gateway, error) {
	discoveryCtx, discoveryCancel := context.WithCancel(ctx)
	g := &Gateway{
		peer:            p,
		logger:          logger,
		discoveryCtx:    discoveryCtx,
		discoveryCancel: discoveryCancel,
	}
	return g, nil
}

// StartHTTPServer starts the HTTP server on the specified port
func (g *Gateway) StartHTTPServer(port int) error {
	if port == 0 {
		port = DefaultHTTPPort
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat", g.handleChat)
	mux.HandleFunc("/api/health", g.handleHealth)

	// Wrap the mux with logging middleware
	loggedMux := g.loggingMiddleware(mux)

	g.server = &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           loggedMux,
		ReadHeaderTimeout: 30 * time.Second,
	}

	g.logger.Info("Starting HTTP server", zap.Int("port", port))
	if err := g.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("listen and serve: %w", err)
	}
	return nil
}

// loggingMiddleware wraps the HTTP handler to log all requests
func (g *Gateway) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Create a response writer wrapper to capture status code
		wrappedWriter := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		// Log the incoming request
		g.logger.Info("HTTP request received",
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
		g.logger.Info("HTTP request completed",
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
func (g *Gateway) StopHTTPServer(ctx context.Context) error {
	if g.server != nil {
		g.logger.Info("Stopping HTTP server")
		if err := g.server.Shutdown(ctx); err != nil {
			return fmt.Errorf("shutdown server: %w", err)
		}
	}
	return nil
}

// handleChat handles the /api/chat endpoint
func (g *Gateway) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse the request
	var req GenerateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		g.logger.Error("Failed to decode request", zap.Error(err))
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Validate the request
	if req.Model == "" {
		http.Error(w, "Model is required", http.StatusBadRequest)
		return
	}

	g.logger.Info("Processing chat request",
		zap.String("model", req.Model),
		zap.Any("messages", req.Messages),
		zap.Bool("stream", req.Stream))

	// Find the best worker for the model
	ctx := r.Context()
	bestWorker := g.FindBestWorker(req.Model)
	if bestWorker == nil {
		g.logger.Error("Failed to find suitable worker")
		response := GenerateResponse{
			Model: req.Model,
		}
		g.sendJSONResponse(w, response, http.StatusServiceUnavailable)
		return
	}

	// Request inference from the worker
	response, err := g.RequestInference(ctx, bestWorker.PeerID, req.Messages[0].Content)
	if err != nil {
		g.logger.Error("Failed to request inference", zap.Error(err))
		response := GenerateResponse{
			Model: req.Model,
		}
		g.sendJSONResponse(w, response, http.StatusInternalServerError)
		return
	}

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

	g.sendJSONResponse(w, generateResponse, http.StatusOK)
}

// sendJSONResponse sends a JSON response with the specified status code
func (g *Gateway) sendJSONResponse(w http.ResponseWriter, response interface{}, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		g.logger.Error("Failed to encode JSON response", zap.Error(err))
	}
}

// RequestInference sends a string task to a worker and waits for a response
func (g *Gateway) RequestInference(ctx context.Context, workerID, input string) (string, error) {
	pid, err := peer.Decode(workerID)
	if err != nil {
		return "", fmt.Errorf("invalid worker peer ID: %w", err)
	}
	peerInfo, err := g.peer.DHT.FindPeer(ctx, pid)
	if err != nil {
		return "", fmt.Errorf("could not find worker peer: %w", err)
	}
	g.logger.Debug("Opening stream to worker", zap.String("peer_id", peerInfo.ID.String()))
	stream, err := g.peer.Host.NewStream(ctx, peerInfo.ID, InferenceProtocol)
	if err != nil {
		return "", fmt.Errorf("failed to open stream: %w", err)
	}
	defer func() {
		if closeErr := stream.Close(); closeErr != nil {
			g.logger.Warn("failed to close stream", zap.Error(closeErr))
		}
	}()

	g.logger.Debug("Writing input to stream", zap.String("input", input))
	_, err = stream.Write([]byte(input))
	if err != nil {
		return "", fmt.Errorf("failed to write to stream: %w", err)
	}

	g.logger.Debug("Waiting for response from worker...")

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
		}
	}

	g.logger.Info("Received response from worker",
		zap.String("worker_id", workerID),
		zap.Int("response_length", len(response)),
		zap.String("response", response))
	return response, nil
}

// DiscoverPeers searches for available peers in the DHT
func (g *Gateway) DiscoverPeers(ctx context.Context) ([]*crowdllama.Resource, error) {
	peers, err := discovery.DiscoverPeers(ctx, g.peer.DHT, g.logger, g.peer.PeerManager)
	if err != nil {
		return nil, fmt.Errorf("discover peers: %w", err)
	}
	return peers, nil
}

// GetAvailablePeers returns all available peers with their details
func (g *Gateway) GetAvailablePeers() map[string]*crowdllama.Resource {
	result := make(map[string]*crowdllama.Resource)
	for peerID, info := range g.peer.PeerManager.GetHealthyPeers() {
		if info.Metadata != nil {
			result[peerID] = info.Metadata
		}
	}
	return result
}

// GetAvailableWorkers returns only worker peers
func (g *Gateway) GetAvailableWorkers() map[string]*crowdllama.Resource {
	result := make(map[string]*crowdllama.Resource)
	for peerID, info := range g.peer.PeerManager.GetHealthyPeers() {
		if info.Metadata != nil && info.Metadata.WorkerMode {
			result[peerID] = info.Metadata
		}
	}
	return result
}

// FindBestWorker finds the best available worker for a specific model
func (g *Gateway) FindBestWorker(requiredModel string) *crowdllama.Resource {
	workers := g.GetAvailableWorkers()
	if len(workers) == 0 {
		return nil
	}

	// Filter workers that support the required model
	suitableWorkers := make([]*crowdllama.Resource, 0)
	for _, worker := range workers {
		// Check if the worker supports the required model
		supportsModel := false
		for _, model := range worker.SupportedModels {
			if model == requiredModel {
				supportsModel = true
				break
			}
		}

		if supportsModel {
			suitableWorkers = append(suitableWorkers, worker)
		}
	}

	if len(suitableWorkers) == 0 {
		return nil
	}

	// Select the best worker based on criteria (lowest load, highest throughput)
	var selectedWorker *crowdllama.Resource
	bestScore := float64(0)

	for _, worker := range suitableWorkers {
		// Simple scoring: tokens_throughput / (1 + current_load)
		// This favors workers with high throughput and low current load
		score := worker.TokensThroughput / (1 + worker.Load)
		if score > bestScore {
			bestScore = score
			selectedWorker = worker
		}
	}

	g.logger.Info("Selected best worker",
		zap.String("worker_id", selectedWorker.PeerID),
		zap.String("gpu_model", selectedWorker.GPUModel),
		zap.Float64("tokens_throughput", selectedWorker.TokensThroughput),
		zap.Float64("current_load", selectedWorker.Load),
		zap.Int("total_suitable_workers", len(suitableWorkers)))

	return selectedWorker
}

// StartBackgroundDiscovery starts the background worker discovery process
func (g *Gateway) StartBackgroundDiscovery() {
	g.logger.Info("Starting background worker discovery")

	// Start the peer manager
	g.peer.PeerManager.Start()

	go func() {
		// Use shorter interval for testing environments
		discoveryInterval := DiscoveryInterval
		if os.Getenv("CROWDLLAMA_TEST_MODE") == "1" {
			discoveryInterval = 2 * time.Second
		}

		ticker := time.NewTicker(discoveryInterval)
		defer ticker.Stop()

		// Run initial discovery immediately
		g.runDiscovery()

		for {
			select {
			case <-ticker.C:
				g.runDiscovery()
			case <-g.discoveryCtx.Done():
				g.logger.Info("Background discovery stopped")
				return
			}
		}
	}()
}

// runDiscovery performs a single discovery run and updates the worker map
func (g *Gateway) runDiscovery() {
	ctx, cancel := context.WithTimeout(g.discoveryCtx, 10*time.Second)
	defer cancel()

	peers, err := g.DiscoverPeers(ctx)
	if err != nil {
		g.logger.Warn("Background discovery failed", zap.Error(err))
		return
	}

	updatedCount := 0
	skippedCount := 0
	for _, peer := range peers {
		// Check if this peer is already marked as unhealthy or recently removed
		if g.peer.PeerManager.IsPeerUnhealthy(peer.PeerID) {
			g.logger.Debug("Skipping unhealthy peer",
				zap.String("peer_id", peer.PeerID))
			skippedCount++
			continue
		}

		// Additional check: skip peers with old metadata
		if time.Since(peer.LastUpdated) > 1*time.Minute {
			g.logger.Debug("Skipping peer with old metadata",
				zap.String("peer_id", peer.PeerID),
				zap.Time("last_updated", peer.LastUpdated))
			skippedCount++
			continue
		}

		g.peer.PeerManager.AddOrUpdatePeer(peer.PeerID, peer)
		updatedCount++
	}

	if updatedCount > 0 || skippedCount > 0 {
		g.logger.Info("Background discovery completed",
			zap.Int("updated_count", updatedCount),
			zap.Int("skipped_count", skippedCount),
			zap.Int("total_workers", len(g.peer.PeerManager.GetAllPeers())))
	}
}

// GetWorkerHealthStatus returns detailed health information about all workers
func (g *Gateway) GetWorkerHealthStatus() map[string]map[string]interface{} {
	result := make(map[string]map[string]interface{})
	for peerID, info := range g.peer.PeerManager.GetAllPeers() {
		result[peerID] = map[string]interface{}{
			"is_healthy":        info.IsHealthy,
			"last_seen":         info.LastSeen,
			"last_health_check": info.LastHealthCheck,
			"failed_attempts":   info.FailedAttempts,
			"last_failure":      info.LastFailure,
		}
		if info.Metadata != nil {
			result[peerID]["gpu_model"] = info.Metadata.GPUModel
			result[peerID]["supported_models"] = info.Metadata.SupportedModels
		}
	}
	return result
}

// StopBackgroundDiscovery stops the background discovery process
func (g *Gateway) StopBackgroundDiscovery() {
	if g.discoveryCancel != nil {
		g.discoveryCancel()
	}
	g.peer.PeerManager.Stop()
}

// handleHealth handles the /api/health endpoint
func (g *Gateway) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	healthStatus := g.GetWorkerHealthStatus()
	g.sendJSONResponse(w, healthStatus, http.StatusOK)
}
