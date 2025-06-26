// Package ui provides the web UI functionality for CrowdLlama.
package ui

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"fmt"
	"html/template"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/libp2p/go-libp2p/p2p/transport/websocket"
	"go.uber.org/zap"

	"github.com/matiasinsaurralde/crowdllama/internal/discovery"
	"github.com/matiasinsaurralde/crowdllama/pkg/consumer"
	"github.com/matiasinsaurralde/crowdllama/pkg/crowdllama"
)

// DefaultHTTPPort is the default HTTP port for the UI server
const DefaultHTTPPort = 9002

// InferenceTask represents an inference task for metrics
type InferenceTask struct {
	ID           int       `json:"id"`
	ConsumerPeer string    `json:"consumer_peer"`
	WorkerPeer   string    `json:"worker_peer"`
	Model        string    `json:"model"`
	Prompt       string    `json:"prompt"`
	Response     string    `json:"response"`
	ElapsedMs    int64     `json:"elapsed_ms"`
	Timestamp    time.Time `json:"timestamp"`
}

// MetricsEvent represents a real-time metrics event
type MetricsEvent struct {
	Type      string      `json:"type"`
	Timestamp time.Time   `json:"timestamp"`
	Data      interface{} `json:"data"`
}

// Server handles the web UI and manages DHT connection and worker discovery
type Server struct {
	server *http.Server
	logger *zap.Logger
	host   host.Host
	dht    *dht.IpfsDHT
	ctx    context.Context
	cancel context.CancelFunc

	// Worker management
	workers         []*crowdllama.Resource
	workersMutex    sync.RWMutex
	discoveryCtx    context.Context
	discoveryCancel context.CancelFunc

	// Metrics management
	inferenceTasks []*InferenceTask
	inferenceMutex sync.RWMutex
	inferenceCount int

	// SSE clients
	sseClients      map[chan MetricsEvent]bool
	sseClientsMutex sync.RWMutex
}

// NewUIServer creates a new UI server instance
func NewUIServer(ctx context.Context, logger *zap.Logger) (*Server, error) {
	// Generate a random private key for the UI server
	privKey, _, err := crypto.GenerateKeyPairWithReader(crypto.Ed25519, 2048, cryptorand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate UI server key: %w", err)
	}

	// Create libp2p host for the UI server
	h, err := libp2p.New(
		libp2p.ListenAddrStrings(
			"/ip4/127.0.0.1/tcp/0", // Random TCP port for DHT
		),
		libp2p.Identity(privKey),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Transport(websocket.New),
	)
	if err != nil {
		return nil, fmt.Errorf("create UI server host: %w", err)
	}

	// Create DHT for the UI server
	kadDHT, err := dht.New(ctx, h, dht.Mode(dht.ModeClient))
	if err != nil {
		if closeErr := h.Close(); closeErr != nil {
			logger.Error("Failed to close host after DHT creation error", zap.Error(closeErr))
		}
		return nil, fmt.Errorf("create UI server DHT: %w", err)
	}

	// Bootstrap the DHT
	if err := discovery.BootstrapDHT(ctx, h, kadDHT); err != nil {
		logger.Warn("Failed to bootstrap UI server DHT", zap.Error(err))
	}

	// Create context for background discovery
	discoveryCtx, discoveryCancel := context.WithCancel(ctx)

	server := &Server{
		logger:          logger,
		host:            h,
		dht:             kadDHT,
		ctx:             ctx,
		cancel:          discoveryCancel,
		discoveryCtx:    discoveryCtx,
		discoveryCancel: discoveryCancel,
		workers:         make([]*crowdllama.Resource, 0),
		inferenceTasks:  make([]*InferenceTask, 0),
		sseClients:      make(map[chan MetricsEvent]bool),
	}

	// Start background worker discovery
	go server.startBackgroundDiscovery()

	logger.Info("UI server created",
		zap.String("peer_id", h.ID().String()),
		zap.String("listen_addrs", h.Addrs()[0].String()))

	return server, nil
}

// broadcastEvent sends an event to all connected SSE clients
func (u *Server) broadcastEvent(eventType string, data interface{}) {
	event := MetricsEvent{
		Type:      eventType,
		Timestamp: time.Now(),
		Data:      data,
	}

	u.sseClientsMutex.RLock()
	defer u.sseClientsMutex.RUnlock()

	for clientChan := range u.sseClients {
		select {
		case clientChan <- event:
			// Event sent successfully
		default:
			// Client buffer is full, remove it
			delete(u.sseClients, clientChan)
			close(clientChan)
		}
	}
}

// startBackgroundDiscovery continuously discovers workers in the background
func (u *Server) startBackgroundDiscovery() {
	ticker := time.NewTicker(10 * time.Second) // Discover workers every 10 seconds
	defer ticker.Stop()

	// Initial discovery
	u.discoverWorkers()

	for {
		select {
		case <-ticker.C:
			u.discoverWorkers()
		case <-u.discoveryCtx.Done():
			u.logger.Info("Background discovery stopped")
			return
		}
	}
}

// discoverWorkers discovers available workers in the DHT
func (u *Server) discoverWorkers() {
	workers, err := discovery.DiscoverWorkers(u.discoveryCtx, u.dht, u.logger)
	if err != nil {
		u.logger.Error("Failed to discover workers", zap.Error(err))
		return
	}

	u.workersMutex.Lock()
	oldWorkers := u.workers
	u.workers = workers
	u.workersMutex.Unlock()

	u.logger.Info("Discovered workers", zap.Int("count", len(workers)))

	// Check for new workers and broadcast them
	oldWorkerIDs := make(map[string]bool)
	for _, worker := range oldWorkers {
		oldWorkerIDs[worker.PeerID] = true
	}

	for _, worker := range workers {
		if !oldWorkerIDs[worker.PeerID] {
			u.logger.Debug("New worker found",
				zap.String("peer_id", worker.PeerID),
				zap.Strings("supported_models", worker.SupportedModels))

			// Broadcast new peer event
			u.broadcastEvent("peer_joined", map[string]interface{}{
				"peer_id": worker.PeerID,
				"models":  worker.SupportedModels,
				"x":       rand.Intn(700) + 100, // Random position
				"y":       rand.Intn(500) + 100,
			})
		}
	}
}

// getRandomWorker returns a random worker from the discovered workers
func (u *Server) getRandomWorker() (*crowdllama.Resource, error) {
	u.workersMutex.RLock()
	defer u.workersMutex.RUnlock()

	if len(u.workers) == 0 {
		return nil, fmt.Errorf("no workers available")
	}

	// Use crypto/rand for secure random selection
	randomBytes := make([]byte, 8)
	if _, err := cryptorand.Read(randomBytes); err != nil {
		return nil, fmt.Errorf("generate random bytes: %w", err)
	}
	randomIndex := int(randomBytes[0]) % len(u.workers)
	return u.workers[randomIndex], nil
}

// StartHTTPServer starts the HTTP server
func (u *Server) StartHTTPServer(port int) error {
	if port == 0 {
		port = DefaultHTTPPort
	}

	mux := http.NewServeMux()

	// Serve static HTML
	mux.HandleFunc("/", u.handleIndex)
	mux.HandleFunc("/metrics", u.handleMetrics)

	// API endpoints
	mux.HandleFunc("/api/inference", u.handleInference)
	mux.HandleFunc("/api/workers", u.handleWorkers)
	mux.HandleFunc("/api/worker_info", u.handleWorkerInfo)
	mux.HandleFunc("/api/metrics/events", u.handleMetricsEvents)
	mux.HandleFunc("/api/metrics/inferences", u.handleMetricsInferences)

	// Serve static files
	mux.Handle("/image.png", http.FileServer(http.Dir(".")))

	u.server = &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           mux,
		ReadHeaderTimeout: 30 * time.Second,
	}

	u.logger.Info("Starting UI HTTP server", zap.Int("port", port))
	if err := u.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("listen and serve: %w", err)
	}
	return nil
}

// StopHTTPServer gracefully stops the HTTP server
func (u *Server) StopHTTPServer(ctx context.Context) error {
	// Stop background discovery
	u.discoveryCancel()

	// Close all SSE clients
	u.sseClientsMutex.Lock()
	for clientChan := range u.sseClients {
		close(clientChan)
	}
	u.sseClients = make(map[chan MetricsEvent]bool)
	u.sseClientsMutex.Unlock()

	// Stop HTTP server
	if u.server != nil {
		u.logger.Info("Stopping HTTP server")
		if err := u.server.Shutdown(ctx); err != nil {
			return fmt.Errorf("shutdown server: %w", err)
		}
	}

	// Close libp2p host
	if u.host != nil {
		if err := u.host.Close(); err != nil {
			u.logger.Error("Failed to close host", zap.Error(err))
		}
	}

	return nil
}

// handleIndex serves the main HTML page
func (u *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	tmpl := template.Must(template.New("index").Parse(indexHTML))
	if err := tmpl.Execute(w, nil); err != nil {
		u.logger.Error("Failed to execute template", zap.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleMetrics serves the metrics visualization page
func (u *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	tmpl := template.Must(template.New("metrics").Parse(metricsHTML))
	if err := tmpl.Execute(w, nil); err != nil {
		u.logger.Error("Failed to execute metrics template", zap.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleWorkerInfo returns detailed information about a specific worker
func (u *Server) handleWorkerInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	peerID := r.URL.Query().Get("peer_id")
	if peerID == "" {
		http.Error(w, "peer_id parameter is required", http.StatusBadRequest)
		return
	}

	u.workersMutex.RLock()
	defer u.workersMutex.RUnlock()

	for _, worker := range u.workers {
		if worker.PeerID == peerID {
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(worker); err != nil {
				u.logger.Error("Failed to encode worker info", zap.Error(err))
				http.Error(w, "Internal server error", http.StatusInternalServerError)
			}
			return
		}
	}

	http.Error(w, "Worker not found", http.StatusNotFound)
}

// handleMetricsEvents handles Server-Sent Events for real-time metrics
func (u *Server) handleMetricsEvents(w http.ResponseWriter, r *http.Request) {
	// Set headers for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Cache-Control")

	// Create a channel for this client
	clientChan := make(chan MetricsEvent, 10)

	// Register the client
	u.sseClientsMutex.Lock()
	u.sseClients[clientChan] = true
	u.sseClientsMutex.Unlock()

	// Send initial state
	u.workersMutex.RLock()
	workers := make([]map[string]interface{}, len(u.workers))
	for i, worker := range u.workers {
		workers[i] = map[string]interface{}{
			"peer_id": worker.PeerID,
			"models":  worker.SupportedModels,
			"x":       rand.Intn(700) + 100,
			"y":       rand.Intn(500) + 100,
		}
	}
	u.workersMutex.RUnlock()

	initialEvent := MetricsEvent{
		Type:      "initial_state",
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"workers": workers,
		},
	}

	// Send initial state
	eventData, _ := json.Marshal(initialEvent)
	fmt.Fprintf(w, "data: %s\n\n", eventData)
	w.(http.Flusher).Flush()

	// Listen for events
	for {
		select {
		case event := <-clientChan:
			eventData, err := json.Marshal(event)
			if err != nil {
				u.logger.Error("Failed to marshal event", zap.Error(err))
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", eventData)
			w.(http.Flusher).Flush()
		case <-r.Context().Done():
			// Client disconnected
			u.sseClientsMutex.Lock()
			delete(u.sseClients, clientChan)
			u.sseClientsMutex.Unlock()
			close(clientChan)
			return
		}
	}
}

// handleMetricsInferences returns the list of inference tasks
func (u *Server) handleMetricsInferences(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	u.inferenceMutex.RLock()
	inferences := make([]*InferenceTask, len(u.inferenceTasks))
	copy(inferences, u.inferenceTasks)
	u.inferenceMutex.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"inferences": inferences,
		"count":      len(inferences),
	}); err != nil {
		u.logger.Error("Failed to encode inferences", zap.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleWorkers returns the list of available workers
func (u *Server) handleWorkers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	u.workersMutex.RLock()
	workers := make([]map[string]interface{}, len(u.workers))
	for i, worker := range u.workers {
		workers[i] = map[string]interface{}{
			"peer_id":          worker.PeerID,
			"supported_models": worker.SupportedModels,
		}
	}
	u.workersMutex.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"workers": workers,
		"count":   len(workers),
	}); err != nil {
		u.logger.Error("Failed to encode workers response", zap.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleInference handles inference requests
func (u *Server) handleInference(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Prompt string `json:"prompt"`
		Model  string `json:"model"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Prompt == "" {
		http.Error(w, "Prompt is required", http.StatusBadRequest)
		return
	}

	// Send inference request
	response, workerPeerID, elapsed, err := u.sendInferenceRequest(req.Model, req.Prompt)
	if err != nil {
		u.logger.Error("Inference request failed", zap.Error(err))
		http.Error(w, fmt.Sprintf("Inference failed: %v", err), http.StatusInternalServerError)
		return
	}

	// Add inference task to metrics
	u.addInferenceTask(req.Model, req.Prompt, response, workerPeerID, elapsed)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"response":       response,
		"worker_peer_id": workerPeerID,
		"elapsed_ms":     elapsed.Milliseconds(),
	}); err != nil {
		u.logger.Error("Failed to encode inference response", zap.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// addInferenceTask adds an inference task to the metrics
func (u *Server) addInferenceTask(model, prompt, response, workerPeerID string, elapsed time.Duration) {
	u.inferenceMutex.Lock()
	defer u.inferenceMutex.Unlock()

	u.inferenceCount++
	task := &InferenceTask{
		ID:           u.inferenceCount,
		ConsumerPeer: u.host.ID().String(),
		WorkerPeer:   workerPeerID,
		Model:        model,
		Prompt:       prompt,
		Response:     response,
		ElapsedMs:    elapsed.Milliseconds(),
		Timestamp:    time.Now(),
	}

	u.inferenceTasks = append([]*InferenceTask{task}, u.inferenceTasks...)

	// Keep only last 10 tasks
	if len(u.inferenceTasks) > 10 {
		u.inferenceTasks = u.inferenceTasks[:10]
	}

	// Broadcast inference event
	u.broadcastEvent("inference_completed", task)
}

// sendInferenceRequest sends an inference request to a random worker
func (u *Server) sendInferenceRequest(model, prompt string) (response string, workerPeerID string, elapsed time.Duration, err error) {
	start := time.Now()

	// Get a random worker
	worker, err := u.getRandomWorker()
	if err != nil {
		return "", "", 0, fmt.Errorf("get random worker: %w", err)
	}

	u.logger.Info("Sending inference request",
		zap.String("worker_peer_id", worker.PeerID),
		zap.String("model", model),
		zap.String("prompt", prompt))

	// Parse the worker peer ID
	parsedWorkerPeerID, err := peer.Decode(worker.PeerID)
	if err != nil {
		return "", "", 0, fmt.Errorf("decode worker peer ID: %w", err)
	}

	// Open stream to worker
	stream, err := u.host.NewStream(u.ctx, parsedWorkerPeerID, consumer.InferenceProtocol)
	if err != nil {
		return "", "", 0, fmt.Errorf("open stream to worker: %w", err)
	}
	defer func() {
		if closeErr := stream.Close(); closeErr != nil {
			u.logger.Error("Failed to close stream", zap.Error(closeErr))
		}
	}()

	// Send the request as a simple string (model + prompt)
	requestStr := fmt.Sprintf("Model: %s\nPrompt: %s", model, prompt)
	if _, err := stream.Write([]byte(requestStr)); err != nil {
		return "", "", 0, fmt.Errorf("write inference request: %w", err)
	}

	// Close write side to signal end of request
	if err := stream.CloseWrite(); err != nil {
		return "", "", 0, fmt.Errorf("close write stream: %w", err)
	}

	// Read the response
	responseData, err := u.readStream(stream, parsedWorkerPeerID)
	if err != nil {
		return "", "", 0, fmt.Errorf("read inference response: %w", err)
	}

	elapsed = time.Since(start)

	u.logger.Info("Received inference response",
		zap.String("worker_peer_id", worker.PeerID),
		zap.Duration("elapsed", elapsed),
		zap.String("response", string(responseData)))

	return string(responseData), worker.PeerID, elapsed, nil
}

// readStream reads all data from the stream until EOF
func (u *Server) readStream(stream network.Stream, workerPeer peer.ID) ([]byte, error) {
	var responseData []byte
	buf := make([]byte, 1024)
	totalRead := 0

	for {
		n, readErr := stream.Read(buf)
		if n > 0 {
			responseData = append(responseData, buf[:n]...)
			totalRead += n
			u.logger.Debug("Read bytes from inference stream",
				zap.String("worker_peer_id", workerPeer.String()),
				zap.Int("bytes_read", n),
				zap.Int("total_read", totalRead))
		}
		if readErr != nil {
			if readErr.Error() == "EOF" {
				u.logger.Debug("Received EOF from inference stream",
					zap.String("worker_peer_id", workerPeer.String()),
					zap.Int("total_bytes_read", totalRead))
				break // EOF reached, we're done reading
			}
			u.logger.Error("Failed to read inference response from worker",
				zap.String("worker_peer_id", workerPeer.String()),
				zap.Error(readErr))
			return nil, fmt.Errorf("failed to read inference response from worker: %w", readErr)
		}
	}

	if len(responseData) == 0 {
		return nil, fmt.Errorf("no response received from worker")
	}

	return responseData, nil
}

// indexHTML is the main HTML template for the web UI
const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>CrowdLlama - P2P AI Inference</title>
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
            max-width: 900px;
            margin: 0 auto;
            padding: 20px;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            min-height: 100vh;
            color: #333;
        }
        .container {
            background: white;
            border-radius: 12px;
            padding: 30px;
            box-shadow: 0 10px 30px rgba(0,0,0,0.2);
        }
        h1 {
            text-align: center;
            color: #4a5568;
            margin-bottom: 30px;
            font-size: 2.5em;
        }
        .form-group {
            margin-bottom: 20px;
        }
        label {
            display: block;
            margin-bottom: 8px;
            font-weight: 600;
            color: #4a5568;
        }
        select, textarea, input[type="text"] {
            width: 100%;
            padding: 12px;
            border: 2px solid #e2e8f0;
            border-radius: 6px;
            font-size: 16px;
            transition: border-color 0.2s;
        }
        select:focus, textarea:focus, input[type="text"]:focus {
            outline: none;
            border-color: #667eea;
        }
        textarea {
            resize: vertical;
            min-height: 120px;
            font-family: inherit;
        }
        button {
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white;
            border: none;
            padding: 12px 24px;
            border-radius: 6px;
            font-size: 16px;
            font-weight: 600;
            cursor: pointer;
            transition: transform 0.2s;
            width: 100%;
        }
        button:hover {
            transform: translateY(-2px);
        }
        button:disabled {
            opacity: 0.6;
            cursor: not-allowed;
            transform: none;
        }
        .response {
            margin-top: 20px;
            padding: 20px;
            background: #f7fafc;
            border-radius: 6px;
            border-left: 4px solid #667eea;
            white-space: pre-wrap;
            min-height: 100px;
        }
        .response.empty {
            color: #a0aec0;
            font-style: italic;
        }
        .loading {
            display: none;
            text-align: center;
            margin: 20px 0;
        }
        .spinner {
            border: 3px solid #f3f3f3;
            border-top: 3px solid #667eea;
            border-radius: 50%;
            width: 30px;
            height: 30px;
            animation: spin 1s linear infinite;
            margin: 0 auto 10px;
        }
        @keyframes spin {
            0% { transform: rotate(0deg); }
            100% { transform: rotate(360deg); }
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>crowdLlama p2p inference</h1>
        <img src="/image.png" alt="CrowdLlama" style="display:block; margin:0 auto 24px auto; max-width:300px; width:100%; height:auto;" />
        
        <!-- Navigation -->
        <div style="text-align: center; margin-bottom: 20px;">
            <a href="/" style="color: #667eea; text-decoration: none; font-weight: 500; padding: 8px 16px; border-radius: 6px; background: rgba(102, 126, 234, 0.1); margin-right: 10px;">Inference</a>
            <a href="/metrics" style="color: rgba(102, 126, 234, 0.6); text-decoration: none; font-weight: 500; padding: 8px 16px; border-radius: 6px;">Metrics</a>
        </div>
        
        <form id="inferenceForm">
            <div class="form-group">
                <label for="model">Model:</label>
                <select id="model" required>
                    <option value="">Select a model...</option>
                    <option value="tinyllama">TinyLlama</option>
                </select>
            </div>
            <div class="form-group">
                <label for="prompt">Prompt:</label>
                <textarea id="prompt" placeholder="Enter your prompt here..." required></textarea>
            </div>
            <button type="submit" id="submitBtn">Send to P2P Network</button>
        </form>
        <div class="loading" id="loading">
            <div class="spinner"></div>
            <div>Processing through P2P network...</div>
        </div>
        <div class="response empty" id="response">
            Response will appear here...
        </div>
        <div id="elapsed" style="margin-top: 10px; color: #4a5568; text-align: center;"></div>
    </div>
    <script>
        function showLoading() {
            document.getElementById('loading').style.display = 'block';
            document.getElementById('submitBtn').disabled = true;
        }
        function hideLoading() {
            document.getElementById('loading').style.display = 'none';
            document.getElementById('submitBtn').disabled = false;
        }
        function clearResponse() {
            const responseDiv = document.getElementById('response');
            responseDiv.className = 'response empty';
            responseDiv.textContent = 'Response will appear here...';
            document.getElementById('elapsed').textContent = '';
        }
        function displayResponse(response, elapsed, workerPeerId) {
            const responseDiv = document.getElementById('response');
            responseDiv.className = 'response';
            responseDiv.innerHTML = response;
            const elapsedDiv = document.getElementById('elapsed');
            elapsedDiv.innerHTML = 
                '<div style="margin-top: 10px; color: #4a5568; text-align: center;">' +
                '<div>⏱️ Time taken: ' + elapsed.toFixed(2) + 's</div>' +
                '<div>🤖 Worker: ' + workerPeerId + '</div>' +
                '</div>';
        }
        function displayError(error) {
            const responseDiv = document.getElementById('response');
            responseDiv.className = 'response';
            responseDiv.innerHTML = '<span style="color: #e53e3e;">Error: ' + error + '</span>';
            document.getElementById('elapsed').textContent = '';
        }
        document.getElementById('inferenceForm').addEventListener('submit', async function(e) {
            e.preventDefault();
            const model = document.getElementById('model').value;
            const prompt = document.getElementById('prompt').value;
            if (!model || !prompt.trim()) {
                displayError('Please select a model and enter a prompt');
                return;
            }
            clearResponse();
            showLoading();
            try {
                const response = await fetch('/api/inference', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ model, prompt })
                });
                if (!response.ok) {
                    const errorText = await response.text();
                    throw new Error('Inference failed: ' + errorText);
                }
                const data = await response.json();
                displayResponse(data.response, data.elapsed_ms / 1000, data.worker_peer_id);
            } catch (error) {
                displayError(error.message);
            } finally {
                hideLoading();
            }
        });
    </script>
</body>
</html>`

// metricsHTML is the metrics visualization template
const metricsHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>CrowdLlama P2P Network - Metrics</title>
    <style>
        @import url('https://fonts.googleapis.com/css2?family=Inter:wght@300;400;500;600;700&display=swap');
        
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }
        
        body {
            background: #000000;
            font-family: 'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
            color: #ffffff;
            overflow: hidden;
            height: 100vh;
            position: relative;
        }
        
        /* Subtle gradient background */
        body::before {
            content: '';
            position: absolute;
            top: 0;
            left: 0;
            width: 100%;
            height: 100%;
            background: radial-gradient(circle at 50% 50%, rgba(255, 255, 255, 0.02) 0%, transparent 50%);
            z-index: 1;
        }
        
        /* Main container */
        .container {
            position: relative;
            width: 100%;
            height: 100vh;
            z-index: 2;
        }
        
        /* Info panel */
        .info-panel {
            position: fixed;
            top: 24px;
            right: 24px;
            width: 320px;
            background: rgba(255, 255, 255, 0.05);
            border: 1px solid rgba(255, 255, 255, 0.1);
            border-radius: 12px;
            padding: 24px;
            backdrop-filter: blur(20px);
            z-index: 10;
            transition: all 0.3s ease;
        }
        
        .info-panel:hover {
            background: rgba(255, 255, 255, 0.08);
            border-color: rgba(255, 255, 255, 0.15);
        }
        
        .info-panel h2 {
            color: #ffffff;
            margin-bottom: 16px;
            font-size: 14px;
            font-weight: 600;
            text-transform: uppercase;
            letter-spacing: 0.5px;
            opacity: 0.7;
        }
        
        .info-content {
            color: #ffffff;
            font-size: 14px;
            line-height: 1.5;
        }
        
        .peer-id {
            font-weight: 500;
            color: #ffffff;
            word-break: break-all;
            margin-bottom: 12px;
            font-family: 'SF Mono', Monaco, 'Cascadia Code', 'Roboto Mono', Consolas, 'Courier New', monospace;
            font-size: 12px;
            background: rgba(255, 255, 255, 0.05);
            padding: 8px 12px;
            border-radius: 6px;
            border: 1px solid rgba(255, 255, 255, 0.1);
        }
        
        .models-list {
            list-style: none;
            margin-top: 12px;
        }
        
        .models-list li {
            background: rgba(255, 255, 255, 0.03);
            padding: 6px 12px;
            margin: 4px 0;
            border-radius: 6px;
            font-size: 13px;
            border: 1px solid rgba(255, 255, 255, 0.05);
            transition: all 0.2s ease;
        }
        
        .models-list li:hover {
            background: rgba(255, 255, 255, 0.08);
            border-color: rgba(255, 255, 255, 0.1);
        }
        
        /* Central llama */
        .central-llama {
            position: absolute;
            top: 50%;
            left: 50%;
            transform: translate(-50%, -50%);
            width: 80px;
            height: 80px;
            z-index: 5;
            transition: all 0.3s ease;
        }
        
        .central-llama:hover {
            transform: translate(-50%, -50%) scale(1.1);
        }
        
        /* Minimal llama */
        .llama-minimal {
            width: 100%;
            height: 100%;
            background: linear-gradient(135deg, #f5f5f5 0%, #e0e0e0 100%);
            border-radius: 50%;
            position: relative;
            display: flex;
            align-items: center;
            justify-content: center;
            font-size: 40px;
            box-shadow: 0 8px 32px rgba(0, 0, 0, 0.3);
            border: 2px solid rgba(255, 255, 255, 0.1);
        }
        
        /* Peer nodes */
        .peer {
            position: absolute;
            width: 12px;
            height: 12px;
            background: #ffffff;
            border-radius: 50%;
            cursor: pointer;
            z-index: 3;
            transition: all 0.3s ease;
            box-shadow: 0 2px 8px rgba(255, 255, 255, 0.2);
        }
        
        .peer:hover {
            transform: scale(1.5);
            box-shadow: 0 4px 16px rgba(255, 255, 255, 0.4);
        }
        
        .peer.selected {
            background: #0070f3;
            box-shadow: 0 4px 16px rgba(0, 112, 243, 0.4);
        }
        
        /* Connection lines */
        .connection {
            position: absolute;
            height: 1px;
            background: linear-gradient(90deg, transparent, rgba(255, 255, 255, 0.3), transparent);
            z-index: 2;
            transition: all 0.3s ease;
        }
        
        /* Inference connection */
        .inference-connection {
            position: absolute;
            height: 2px;
            background: repeating-linear-gradient(
                90deg,
                #0070f3 0px,
                #0070f3 8px,
                transparent 8px,
                transparent 16px
            );
            z-index: 4;
            animation: dataTransfer 1s linear infinite;
            box-shadow: 0 0 10px rgba(0, 112, 243, 0.5);
        }
        
        @keyframes dataTransfer {
            0% { 
                background-position: 0 0;
                opacity: 0.7;
                box-shadow: 0 0 10px rgba(0, 112, 243, 0.5);
            }
            50% { 
                opacity: 1;
                box-shadow: 0 0 20px rgba(0, 112, 243, 0.8);
            }
            100% { 
                background-position: -16px 0;
                opacity: 0.7;
                box-shadow: 0 0 10px rgba(0, 112, 243, 0.5);
            }
        }
        
        /* Title */
        .title {
            position: fixed;
            top: 24px;
            left: 24px;
            z-index: 10;
        }
        
        .title h1 {
            font-size: 24px;
            font-weight: 600;
            margin-bottom: 4px;
            color: #ffffff;
        }
        
        .title p {
            font-size: 14px;
            color: rgba(255, 255, 255, 0.6);
            font-weight: 400;
        }
        
        /* Peer join animation */
        .peer-join {
            animation: peerJoin 0.6s ease-out;
        }
        
        @keyframes peerJoin {
            0% {
                transform: scale(0);
                opacity: 0;
            }
            50% {
                transform: scale(1.2);
                opacity: 0.8;
            }
            100% {
                transform: scale(1);
                opacity: 1;
            }
        }
        
        /* Subtle grid overlay */
        .grid-overlay {
            position: absolute;
            top: 0;
            left: 0;
            width: 100%;
            height: 100%;
            background-image: 
                linear-gradient(rgba(255, 255, 255, 0.02) 1px, transparent 1px),
                linear-gradient(90deg, rgba(255, 255, 255, 0.02) 1px, transparent 1px);
            background-size: 100px 100px;
            z-index: 1;
            opacity: 0.3;
        }
        
        /* Inference log panel */
        .inference-log {
            position: fixed;
            bottom: 80px;
            left: 24px;
            right: 24px;
            height: 120px;
            background: rgba(255, 255, 255, 0.05);
            border: 1px solid rgba(255, 255, 255, 0.1);
            border-radius: 12px;
            backdrop-filter: blur(20px);
            z-index: 10;
            overflow: hidden;
            transition: all 0.3s ease;
        }
        
        .inference-log:hover {
            background: rgba(255, 255, 255, 0.08);
            border-color: rgba(255, 255, 255, 0.15);
        }
        
        .inference-log-header {
            padding: 12px 16px;
            border-bottom: 1px solid rgba(255, 255, 255, 0.1);
            display: flex;
            justify-content: space-between;
            align-items: center;
        }
        
        .inference-log-title {
            font-size: 12px;
            font-weight: 600;
            text-transform: uppercase;
            letter-spacing: 0.5px;
            opacity: 0.7;
            color: #ffffff;
        }
        
        .inference-count {
            font-size: 11px;
            color: rgba(255, 255, 255, 0.5);
            font-family: 'SF Mono', Monaco, 'Cascadia Code', 'Roboto Mono', Consolas, 'Courier New', monospace;
        }
        
        .inference-list {
            padding: 8px 16px;
            height: calc(100% - 40px);
            overflow-y: auto;
        }
        
        .inference-item {
            background: rgba(0, 112, 243, 0.1);
            border: 1px solid rgba(0, 112, 243, 0.2);
            border-radius: 8px;
            padding: 12px;
            margin-bottom: 8px;
            font-size: 12px;
            animation: inferenceItemSlide 0.4s ease-out;
            transition: all 0.2s ease;
        }
        
        .inference-item:hover {
            background: rgba(0, 112, 243, 0.15);
            border-color: rgba(0, 112, 243, 0.3);
        }
        
        @keyframes inferenceItemSlide {
            0% {
                opacity: 0;
                transform: translateY(-20px);
            }
            100% {
                opacity: 1;
                transform: translateY(0);
            }
        }
        
        .inference-item-header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 6px;
        }
        
        .inference-status {
            color: #00ff88;
            font-weight: 500;
            font-size: 11px;
            text-transform: uppercase;
        }
        
        .inference-time {
            color: rgba(255, 255, 255, 0.5);
            font-size: 11px;
            font-family: 'SF Mono', Monaco, 'Cascadia Code', 'Roboto Mono', Consolas, 'Courier New', monospace;
        }
        
        .inference-details {
            display: grid;
            grid-template-columns: 1fr 1fr 1fr;
            gap: 12px;
            font-size: 11px;
        }
        
        .inference-detail {
            display: flex;
            flex-direction: column;
        }
        
        .inference-detail-label {
            color: rgba(255, 255, 255, 0.6);
            margin-bottom: 2px;
            font-size: 10px;
            text-transform: uppercase;
            letter-spacing: 0.5px;
        }
        
        .inference-detail-value {
            color: #ffffff;
            font-weight: 500;
            font-family: 'SF Mono', Monaco, 'Cascadia Code', 'Roboto Mono', Consolas, 'Courier New', monospace;
            word-break: break-all;
        }
        
        .inference-detail-value.peer-id {
            font-size: 10px;
        }
        
        /* Navigation */
        .nav-links {
            position: fixed;
            top: 24px;
            left: 50%;
            transform: translateX(-50%);
            z-index: 10;
            display: flex;
            gap: 16px;
        }
        
        .nav-link {
            color: rgba(255, 255, 255, 0.6);
            text-decoration: none;
            font-size: 14px;
            font-weight: 500;
            padding: 8px 16px;
            border-radius: 6px;
            transition: all 0.2s ease;
        }
        
        .nav-link:hover {
            color: #ffffff;
            background: rgba(255, 255, 255, 0.1);
        }
        
        .nav-link.active {
            color: #0070f3;
            background: rgba(0, 112, 243, 0.1);
        }
    </style>
</head>
<body>
    <div class="container">
        <!-- Grid overlay -->
        <div class="grid-overlay"></div>
        
        <!-- Navigation -->
        <div class="nav-links">
            <a href="/" class="nav-link">Inference</a>
            <a href="/metrics" class="nav-link active">Metrics</a>
        </div>
        
        <!-- Title -->
        <div class="title">
            <h1>CrowdLlama</h1>
            <p>P2P AI Network - Live Metrics</p>
        </div>
        
        <!-- Info Panel -->
        <div class="info-panel">
            <h2>Network Info</h2>
            <div class="info-content">
                <div class="peer-id">Select a peer to view details</div>
                <div class="models-list"></div>
            </div>
        </div>
        
        <!-- Central Llama -->
        <div class="central-llama">
            <div class="llama-minimal">🦙</div>
        </div>
        
        <!-- Inference Log Panel -->
        <div class="inference-log">
            <div class="inference-log-header">
                <div class="inference-log-title">Inference Log</div>
                <div class="inference-count">0 tasks</div>
            </div>
            <div class="inference-list" id="inferenceList">
                <!-- Inference items will be added here dynamically -->
            </div>
        </div>
    </div>

    <script>
        class CrowdLlamaMetrics {
            constructor() {
                this.peers = [];
                this.connections = [];
                this.selectedPeer = null;
                this.container = document.querySelector('.container');
                this.infoPanel = document.querySelector('.info-panel');
                this.peerIdElement = this.infoPanel.querySelector('.peer-id');
                this.modelsListElement = this.infoPanel.querySelector('.models-list');
                this.inferenceList = document.getElementById('inferenceList');
                this.inferenceCount = 0;
                this.inferenceTasks = [];
                this.eventSource = null;
                
                this.init();
            }
            
            init() {
                this.connectSSE();
                this.updateConnections();
            }
            
            connectSSE() {
                this.eventSource = new EventSource('/api/metrics/events');
                
                this.eventSource.onmessage = (event) => {
                    const data = JSON.parse(event.data);
                    this.handleEvent(data);
                };
                
                this.eventSource.onerror = (error) => {
                    console.error('SSE connection error:', error);
                    // Reconnect after 5 seconds
                    setTimeout(() => {
                        this.connectSSE();
                    }, 5000);
                };
            }
            
            handleEvent(event) {
                switch (event.type) {
                    case 'initial_state':
                        this.handleInitialState(event.data);
                        break;
                    case 'peer_joined':
                        this.handlePeerJoined(event.data);
                        break;
                    case 'inference_completed':
                        this.handleInferenceCompleted(event.data);
                        break;
                }
            }
            
            handleInitialState(data) {
                // Clear existing peers
                this.peers.forEach(peer => peer.element.remove());
                this.peers = [];
                
                // Add workers from initial state
                if (data.workers) {
                    data.workers.forEach(worker => {
                        this.addPeer({
                            id: worker.peer_id,
                            models: worker.models,
                            x: worker.x,
                            y: worker.y
                        });
                    });
                }
                
                this.updateConnections();
            }
            
            handlePeerJoined(data) {
                this.addPeer({
                    id: data.peer_id,
                    models: data.models,
                    x: data.x,
                    y: data.y
                });
            }
            
            handleInferenceCompleted(data) {
                this.addInferenceTask(data);
            }
            
            addPeer(peerData) {
                const peer = document.createElement('div');
                peer.className = 'peer peer-join';
                peer.style.left = peerData.x + 'px';
                peer.style.top = peerData.y + 'px';
                
                peer.addEventListener('click', () => this.selectPeer(peerData));
                
                this.container.appendChild(peer);
                this.peers.push({ element: peer, data: peerData });
                
                // Remove join animation class after animation completes
                setTimeout(() => {
                    peer.classList.remove('peer-join');
                }, 600);
                
                this.updateConnections();
            }
            
            selectPeer(peerData) {
                // Remove previous selection
                if (this.selectedPeer) {
                    this.selectedPeer.classList.remove('selected');
                }
                
                // Find and select new peer
                const peerElement = this.peers.find(p => p.data.id === peerData.id)?.element;
                if (peerElement) {
                    peerElement.classList.add('selected');
                    this.selectedPeer = peerElement;
                }
                
                // Update info panel with latest data
                this.updatePeerInfo(peerData.id);
            }
            
            async updatePeerInfo(peerId) {
                try {
                    const response = await fetch('/api/worker_info?peer_id=' + peerId);
                    if (response.ok) {
                        const workerData = await response.json();
                        
                        this.peerIdElement.textContent = workerData.peer_id;
                        this.modelsListElement.innerHTML = '';
                        
                        workerData.supported_models.forEach(model => {
                            const li = document.createElement('li');
                            li.textContent = model;
                            this.modelsListElement.appendChild(li);
                        });
                    }
                } catch (error) {
                    console.error('Failed to fetch worker info:', error);
                }
            }
            
            updateConnections() {
                // Remove existing connections
                this.connections.forEach(conn => conn.remove());
                this.connections = [];
                
                // Create connections from each peer to the center
                const centerX = window.innerWidth / 2;
                const centerY = window.innerHeight / 2;
                
                this.peers.forEach(peer => {
                    const peerRect = peer.element.getBoundingClientRect();
                    const peerX = peerRect.left + peerRect.width / 2;
                    const peerY = peerRect.top + peerRect.height / 2;
                    
                    const connection = document.createElement('div');
                    connection.className = 'connection';
                    
                    // Calculate connection properties
                    const length = Math.sqrt(Math.pow(centerX - peerX, 2) + Math.pow(centerY - peerY, 2));
                    const angle = Math.atan2(centerY - peerY, centerX - peerX) * 180 / Math.PI;
                    
                    connection.style.width = length + 'px';
                    connection.style.left = peerX + 'px';
                    connection.style.top = peerY + 'px';
                    connection.style.transform = 'rotate(' + angle + 'deg)';
                    connection.style.transformOrigin = '0 50%';
                    
                    this.container.appendChild(connection);
                    this.connections.push(connection);
                });
            }
            
            addInferenceTask(task) {
                this.inferenceCount++;
                
                const taskData = {
                    id: task.id,
                    consumerPeerId: task.consumer_peer.substring(0, 12) + '...',
                    workerPeerId: task.worker_peer.substring(0, 12) + '...',
                    model: task.model,
                    elapsedMs: task.elapsed_ms,
                    timestamp: new Date(task.timestamp).toLocaleTimeString()
                };
                
                this.inferenceTasks.unshift(taskData);
                
                // Keep only last 4 tasks
                if (this.inferenceTasks.length > 4) {
                    this.inferenceTasks.pop();
                }
                
                this.updateInferenceLog();
                this.createInferenceConnection(taskData);
            }
            
            createInferenceConnection(taskData) {
                // Find the consumer and worker peers
                const consumerPeer = this.peers.find(p => p.data.id === taskData.consumerPeerId.replace('...', ''));
                const workerPeer = this.peers.find(p => p.data.id === taskData.workerPeerId.replace('...', ''));
                
                if (consumerPeer && workerPeer) {
                    const consumerRect = consumerPeer.element.getBoundingClientRect();
                    const workerRect = workerPeer.element.getBoundingClientRect();
                    
                    const consumerX = consumerRect.left + consumerRect.width / 2;
                    const consumerY = consumerRect.top + consumerRect.height / 2;
                    const workerX = workerRect.left + workerRect.width / 2;
                    const workerY = workerRect.top + workerRect.height / 2;
                    
                    const connection = document.createElement('div');
                    connection.className = 'inference-connection';
                    
                    // Calculate connection properties
                    const length = Math.sqrt(Math.pow(workerX - consumerX, 2) + Math.pow(workerY - consumerY, 2));
                    const angle = Math.atan2(workerY - consumerY, workerX - consumerX) * 180 / Math.PI;
                    
                    connection.style.width = length + 'px';
                    connection.style.left = consumerX + 'px';
                    connection.style.top = consumerY + 'px';
                    connection.style.transform = 'rotate(' + angle + 'deg)';
                    connection.style.transformOrigin = '0 50%';
                    
                    this.container.appendChild(connection);
                    
                    // Remove the connection after 5 seconds
                    setTimeout(() => {
                        if (connection.parentNode) {
                            connection.parentNode.removeChild(connection);
                        }
                    }, 5000);
                    
                    // Highlight the connected peers temporarily
                    consumerPeer.element.style.background = '#0070f3';
                    workerPeer.element.style.background = '#0070f3';
                    
                    setTimeout(() => {
                        consumerPeer.element.style.background = '#ffffff';
                        workerPeer.element.style.background = '#ffffff';
                    }, 5000);
                }
            }
            
            updateInferenceLog() {
                // Update count
                const countElement = document.querySelector('.inference-count');
                countElement.textContent = this.inferenceTasks.length + ' tasks';
                
                // Clear and rebuild list
                this.inferenceList.innerHTML = '';
                
                this.inferenceTasks.forEach(task => {
                    const item = document.createElement('div');
                    item.className = 'inference-item';
                    item.innerHTML = 
                        '<div class="inference-item-header">' +
                            '<div class="inference-status">Completed</div>' +
                            '<div class="inference-time">' + task.timestamp + '</div>' +
                        '</div>' +
                        '<div class="inference-details">' +
                            '<div class="inference-detail">' +
                                '<div class="inference-detail-label">From Peer</div>' +
                                '<div class="inference-detail-value peer-id">' + task.consumerPeerId + '</div>' +
                            '</div>' +
                            '<div class="inference-detail">' +
                                '<div class="inference-detail-label">To Peer</div>' +
                                '<div class="inference-detail-value peer-id">' + task.workerPeerId + '</div>' +
                            '</div>' +
                            '<div class="inference-detail">' +
                                '<div class="inference-detail-label">Model</div>' +
                                '<div class="inference-detail-value">' + task.model + '</div>' +
                            '</div>' +
                            '<div class="inference-detail">' +
                                '<div class="inference-detail-label">Time</div>' +
                                '<div class="inference-detail-value">' + task.elapsedMs + 'ms</div>' +
                            '</div>' +
                        '</div>';
                    this.inferenceList.appendChild(item);
                });
            }
        }
        
        // Initialize the metrics
        const metrics = new CrowdLlamaMetrics();
        
        // Handle window resize
        window.addEventListener('resize', () => {
            metrics.updateConnections();
        });
        
        // Add some floating animation variation
        setInterval(() => {
            metrics.peers.forEach(peer => {
                const x = parseFloat(peer.element.style.left);
                const y = parseFloat(peer.element.style.top);
                
                // Generate random movement direction and speed
                const speed = 1.5; // Faster movement
                const angle = Math.random() * 2 * Math.PI; // Random direction
                
                const newX = x + Math.cos(angle) * speed;
                const newY = y + Math.sin(angle) * speed;
                
                // Keep peers within bounds
                const maxX = window.innerWidth - 50;
                const maxY = window.innerHeight - 50;
                
                // Bounce off edges by reversing direction
                let finalX = newX;
                let finalY = newY;
                
                if (newX <= 50 || newX >= maxX) {
                    finalX = x - Math.cos(angle) * speed; // Reverse X direction
                }
                if (newY <= 50 || newY >= maxY) {
                    finalY = y - Math.sin(angle) * speed; // Reverse Y direction
                }
                
                peer.element.style.left = Math.max(50, Math.min(maxX, finalX)) + 'px';
                peer.element.style.top = Math.max(50, Math.min(maxY, finalY)) + 'px';
            });
            
            metrics.updateConnections();
        }, 100); // Update every 100ms for smooth movement
    </script>
</body>
</html>`
