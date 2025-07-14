// Package gateway provides HTTP API functionality for CrowdLlama.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"

	llamav1 "github.com/crowdllama/crowdllama-pb/llama/v1"
	"github.com/crowdllama/crowdllama/internal/discovery"
	"github.com/crowdllama/crowdllama/pkg/crowdllama"
	peerpkg "github.com/crowdllama/crowdllama/pkg/peer"
	"google.golang.org/protobuf/proto"
)

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
	apiHandler      crowdllama.UnifiedAPIHandler
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
		apiHandler:      crowdllama.DefaultAPIHandler,
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

	// Parse the request (JSON to PB)
	var req GenerateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		g.logger.Error("Failed to decode request", zap.Error(err))
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Model == "" {
		http.Error(w, "Model is required", http.StatusBadRequest)
		return
	}
	if len(req.Messages) == 0 {
		http.Error(w, "At least one message is required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	bestWorker := g.FindBestWorker(req.Model)
	if bestWorker == nil {
		g.logger.Error("Failed to find suitable worker")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "No suitable worker found"})
		return
	}

	// Use PB-based RequestInference
	pbResp, err := g.RequestInference(ctx, bestWorker.PeerID, req.Model, req.Messages[0].Content, req.Stream)
	if err != nil {
		g.logger.Error("Failed to request inference", zap.Error(err))
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Convert PB GenerateResponse to HTTP JSON response
	generateResponse := GenerateResponse{
		Model:     pbResp.GetModel(),
		CreatedAt: pbResp.GetCreatedAt().AsTime(),
		Message: Message{
			Role:    "assistant",
			Content: pbResp.GetResponse(),
		},
		Done:       pbResp.GetDone(),
		DoneReason: pbResp.GetDoneReason(),
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

// RequestInference sends an inference request to a specific worker using PB messages
func (g *Gateway) RequestInference(ctx context.Context, workerID, model, prompt string, stream bool) (*llamav1.GenerateResponse, error) {
	pid, err := peer.Decode(workerID)
	if err != nil {
		return nil, fmt.Errorf("invalid worker peer ID: %w", err)
	}
	peerInfo, err := g.peer.DHT.FindPeer(ctx, pid)
	if err != nil {
		return nil, fmt.Errorf("could not find worker peer: %w", err)
	}
	streamObj, err := g.peer.Host.NewStream(ctx, peerInfo.ID, crowdllama.InferenceProtocol)
	if err != nil {
		return nil, fmt.Errorf("failed to open stream: %w", err)
	}
	defer func() {
		if closeErr := streamObj.Close(); closeErr != nil {
			g.logger.Warn("failed to close stream", zap.Error(closeErr))
		}
	}()

	// Create PB request
	pbReq := crowdllama.CreateGenerateRequest(model, prompt, stream)
	g.logger.Debug("Writing PB request to stream", zap.String("model", model), zap.String("prompt", prompt))
	if err := g.writePBMessage(streamObj, pbReq); err != nil {
		return nil, fmt.Errorf("failed to write PB request: %w", err)
	}

	g.logger.Debug("Waiting for PB response from worker...")
	pbResp, err := g.readPBMessage(streamObj)
	if err != nil {
		return nil, fmt.Errorf("failed to read PB response: %w", err)
	}

	generateResp, err := crowdllama.ExtractGenerateResponse(pbResp)
	if err != nil {
		return nil, fmt.Errorf("failed to extract GenerateResponse: %w", err)
	}

	g.logger.Info("Received PB response from worker",
		zap.String("worker_id", workerID),
		zap.String("model", generateResp.Model),
		zap.String("response", generateResp.Response))
	return generateResp, nil
}

// writePBMessage writes a length-prefixed protobuf message to a network stream
func (g *Gateway) writePBMessage(s network.Stream, msg *llamav1.BaseMessage) error {
	if err := crowdllama.WriteLengthPrefixedPB(s, msg); err != nil {
		return fmt.Errorf("failed to write length-prefixed PB message: %w", err)
	}
	g.logger.Debug("Gateway sent PB request", zap.Int("bytes", proto.Size(msg)))
	return nil
}

// readPBMessage reads a length-prefixed protobuf message from a network stream
func (g *Gateway) readPBMessage(s network.Stream) (*llamav1.BaseMessage, error) {
	msg, err := crowdllama.ReadLengthPrefixedPB(s)
	if err != nil {
		return nil, fmt.Errorf("failed to read length-prefixed PB message: %w", err)
	}
	g.logger.Debug("Gateway received PB response", zap.Int("bytes", proto.Size(msg)))
	return msg, nil
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
	return g.peer.PeerManager.FindBestWorker(requiredModel)
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

// SetAPIHandler sets the unified API handler for processing inference requests
func (g *Gateway) SetAPIHandler(handler crowdllama.UnifiedAPIHandler) {
	g.apiHandler = handler
}
