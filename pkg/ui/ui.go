// Package ui provides the web UI functionality for CrowdLlama.
package ui

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"fmt"
	"html/template"
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
	}

	// Start background worker discovery
	go server.startBackgroundDiscovery()

	logger.Info("UI server created",
		zap.String("peer_id", h.ID().String()),
		zap.String("listen_addrs", h.Addrs()[0].String()))

	return server, nil
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
	u.workers = workers
	u.workersMutex.Unlock()

	u.logger.Info("Discovered workers", zap.Int("count", len(workers)))
	for _, worker := range workers {
		u.logger.Debug("Worker found",
			zap.String("peer_id", worker.PeerID),
			zap.Strings("supported_models", worker.SupportedModels))
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

	// API endpoints
	mux.HandleFunc("/api/inference", u.handleInference)
	mux.HandleFunc("/api/workers", u.handleWorkers)

	// In StartHTTPServer, add a handler for /image.png
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
