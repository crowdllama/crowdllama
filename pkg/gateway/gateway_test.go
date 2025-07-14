package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"unsafe"

	llamav1 "github.com/crowdllama/crowdllama-pb/llama/v1"
	"github.com/crowdllama/crowdllama/pkg/crowdllama"
	crowdllamapeer "github.com/crowdllama/crowdllama/pkg/peer"
	"github.com/crowdllama/crowdllama/pkg/peermanager"
	dht "github.com/libp2p/go-libp2p-kad-dht"
)

// MockHost mocks the libp2p host with all required methods
type MockHost struct {
	mock.Mock
}

func (m *MockHost) ID() libp2ppeer.ID {
	args := m.Called()
	return args.Get(0).(libp2ppeer.ID)
}

func (m *MockHost) Peerstore() peerstore.Peerstore {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(peerstore.Peerstore)
}

func (m *MockHost) Addrs() []multiaddr.Multiaddr {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).([]multiaddr.Multiaddr)
}

func (m *MockHost) Network() network.Network {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(network.Network)
}

func (m *MockHost) Mux() protocol.Switch {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(protocol.Switch)
}

func (m *MockHost) Connect(ctx context.Context, pi libp2ppeer.AddrInfo) error {
	args := m.Called(ctx, pi)
	return args.Error(0)
}

func (m *MockHost) SetStreamHandler(pid protocol.ID, handler network.StreamHandler) {
	m.Called(pid, handler)
}

func (m *MockHost) SetStreamHandlerMatch(pid protocol.ID, match func(protocol.ID) bool, handler network.StreamHandler) {
	m.Called(pid, match, handler)
}

func (m *MockHost) RemoveStreamHandler(pid protocol.ID) {
	m.Called(pid)
}

func (m *MockHost) NewStream(ctx context.Context, p libp2ppeer.ID, pids ...protocol.ID) (network.Stream, error) {
	args := m.Called(ctx, p, pids)
	return args.Get(0).(network.Stream), args.Error(1)
}

func (m *MockHost) Close() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockHost) ConnManager() connmgr.ConnManager {
	return nil
}

func (m *MockHost) EventBus() event.Bus {
	return nil
}

// MockStream mocks a network stream
type MockStream struct {
	mock.Mock
	readBuffer  *bytes.Buffer
	writeBuffer *bytes.Buffer
}

func (m *MockStream) Read(p []byte) (n int, err error) {
	return m.readBuffer.Read(p)
}

func (m *MockStream) Write(p []byte) (n int, err error) {
	return m.writeBuffer.Write(p)
}

func (m *MockStream) Close() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockStream) CloseWrite() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockStream) CloseRead() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockStream) Reset() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockStream) SetDeadline(t time.Time) error {
	args := m.Called(t)
	return args.Error(0)
}

func (m *MockStream) SetReadDeadline(t time.Time) error {
	args := m.Called(t)
	return args.Error(0)
}

func (m *MockStream) SetWriteDeadline(t time.Time) error {
	args := m.Called(t)
	return args.Error(0)
}

func (m *MockStream) Protocol() protocol.ID {
	args := m.Called()
	return args.Get(0).(protocol.ID)
}

func (m *MockStream) SetProtocol(pid protocol.ID) error {
	args := m.Called(pid)
	return args.Error(0)
}

func (m *MockStream) Conn() network.Conn {
	args := m.Called()
	return args.Get(0).(network.Conn)
}

// MockDHT mocks the DHT
type MockDHT struct {
	mock.Mock
}

func (m *MockDHT) FindPeer(ctx context.Context, p libp2ppeer.ID) (libp2ppeer.AddrInfo, error) {
	args := m.Called(ctx, p)
	return args.Get(0).(libp2ppeer.AddrInfo), args.Error(1)
}

// Minimal stub for dht.IpfsDHT
type StubDHT struct{}

func (s *StubDHT) FindPeer(ctx context.Context, p libp2ppeer.ID) (libp2ppeer.AddrInfo, error) {
	return libp2ppeer.AddrInfo{ID: p}, nil
}

func (s *StubDHT) Provide(ctx context.Context, c interface{}, republish bool) error {
	return nil
}

func (s *StubDHT) Bootstrap(ctx context.Context) error {
	return nil
}

func (s *StubDHT) RoutingTable() interface {
	ListPeers() []libp2ppeer.ID
} {
	return &StubRoutingTable{}
}

type StubRoutingTable struct{}

func (s *StubRoutingTable) ListPeers() []libp2ppeer.ID {
	return []libp2ppeer.ID{}
}

// Minimal mock for PeerManager with just GetHealthyPeers
type MockPeerManager struct {
	healthyPeers map[string]*peermanager.PeerInfo
}

func (m *MockPeerManager) GetHealthyPeers() map[string]*peermanager.PeerInfo {
	return m.healthyPeers
}

func (m *MockPeerManager) GetAllPeers() map[string]*peermanager.PeerInfo {
	return m.healthyPeers
}

func (m *MockPeerManager) GetAvailablePeers() map[string]*crowdllama.Resource {
	result := make(map[string]*crowdllama.Resource)
	for peerID, info := range m.healthyPeers {
		if info.Metadata != nil {
			result[peerID] = info.Metadata
		}
	}
	return result
}

func (m *MockPeerManager) GetAvailableWorkers() map[string]*crowdllama.Resource {
	result := make(map[string]*crowdllama.Resource)
	for peerID, info := range m.healthyPeers {
		if info.Metadata != nil && info.Metadata.WorkerMode {
			result[peerID] = info.Metadata
		}
	}
	return result
}

func (m *MockPeerManager) GetAvailableConsumers() map[string]*crowdllama.Resource {
	result := make(map[string]*crowdllama.Resource)
	for peerID, info := range m.healthyPeers {
		if info.Metadata != nil && !info.Metadata.WorkerMode {
			result[peerID] = info.Metadata
		}
	}
	return result
}

func (m *MockPeerManager) FindBestWorker(requiredModel string) *crowdllama.Resource {
	workers := m.GetAvailableWorkers()
	if len(workers) == 0 {
		return nil
	}
	// Return the first worker that supports the model
	for _, worker := range workers {
		for _, model := range worker.SupportedModels {
			if model == requiredModel {
				// Ensure the worker has all required fields to prevent nil pointer dereferences
				if worker.PeerID == "" {
					worker.PeerID = "QmWorker123"
				}
				if worker.GPUModel == "" {
					worker.GPUModel = "RTX 4090"
				}
				if worker.TokensThroughput == 0 {
					worker.TokensThroughput = 100.0
				}
				if worker.Load == 0 {
					worker.Load = 0.5
				}
				return worker
			}
		}
	}
	return nil
}

func (m *MockPeerManager) AddOrUpdatePeer(peerID string, metadata *crowdllama.Resource) {
	// No-op for tests
}

func (m *MockPeerManager) RemovePeer(peerID string) {
	// No-op for tests
}

func (m *MockPeerManager) IsPeerUnhealthy(peerID string) bool {
	return false
}

func (m *MockPeerManager) MarkPeerAsRecentlyRemoved(peerID string) {
	// No-op for tests
}

func (m *MockPeerManager) Start() {
	// No-op for tests
}

func (m *MockPeerManager) Stop() {
	// No-op for tests
}

func (m *MockPeerManager) GetPeerStatistics() peermanager.PeerStatistics {
	return peermanager.PeerStatistics{}
}

// TestGateway_handleChat_Success tests successful chat request handling
func TestGateway_handleChat_Success(t *testing.T) {
	// Setup
	logger := zap.NewNop()
	mockHost := &MockHost{}

	// Create a mock worker with valid peer ID
	privKey, _, err := crypto.GenerateEd25519Key(rand.Reader)
	assert.NoError(t, err)
	peerID, err := libp2ppeer.IDFromPrivateKey(privKey)
	assert.NoError(t, err)
	workerID := peerID.String()

	// Create mock stream with PB response
	pbResponse := &llamav1.BaseMessage{
		Message: &llamav1.BaseMessage_GenerateResponse{
			GenerateResponse: &llamav1.GenerateResponse{
				Model:      "llama3.2",
				CreatedAt:  timestamppb.Now(),
				Response:   "Hello! This is a test response.",
				Done:       true,
				DoneReason: "stop",
				WorkerId:   workerID,
			},
		},
	}

	// Marshal PB response for mock stream
	pbData, err := proto.Marshal(pbResponse)
	assert.NoError(t, err)

	mockStream := &MockStream{
		readBuffer:  bytes.NewBuffer(pbData),
		writeBuffer: &bytes.Buffer{},
	}
	mockStream.On("Close").Return(nil)

	// Setup mock expectations
	mockHost.On("NewStream", mock.Anything, peerID, crowdllama.InferenceProtocol).Return(mockStream, nil)

	// Create gateway with mock peer
	healthyPeers := map[string]*peermanager.PeerInfo{
		workerID: {
			PeerID: workerID,
			Metadata: &crowdllama.Resource{
				PeerID:          workerID,
				SupportedModels: []string{"llama3.2"},
				WorkerMode:      true,
			},
			IsHealthy: true,
		},
	}
	gw, err := NewGateway(context.Background(), logger, &crowdllamapeer.Peer{
		Host:        mockHost,
		DHT:         (*dht.IpfsDHT)(unsafe.Pointer(&StubDHT{})),
		PeerManager: &MockPeerManager{healthyPeers: healthyPeers},
	})
	assert.NoError(t, err)

	// Create test request
	reqBody := GenerateRequest{
		Model: "llama3.2",
		Messages: []Message{
			{Role: "user", Content: "Hello, how are you?"},
		},
		Stream: false,
	}

	reqJSON, err := json.Marshal(reqBody)
	assert.NoError(t, err)

	// Create HTTP request
	req := httptest.NewRequest("POST", "/api/chat", bytes.NewBuffer(reqJSON))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	// Execute request
	gw.handleChat(w, req)

	// Debug: Print response body if status is not 200
	if w.Code != http.StatusOK {
		t.Logf("Response status: %d", w.Code)
		t.Logf("Response body: %s", w.Body.String())
	}

	// Assertions
	assert.Equal(t, http.StatusOK, w.Code)

	var response GenerateResponse
	err = json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)

	assert.Equal(t, "llama3.2", response.Model)
	assert.Equal(t, "Hello! This is a test response.", response.Message.Content)
	assert.Equal(t, "assistant", response.Message.Role)
	assert.True(t, response.Done)
	assert.Equal(t, "stop", response.DoneReason)

	// Verify mock expectations
	mockHost.AssertExpectations(t)
	mockStream.AssertExpectations(t)
}

// TestGateway_handleChat_NoWorker tests when no suitable worker is found
func TestGateway_handleChat_NoWorker(t *testing.T) {
	// Setup
	logger := zap.NewNop()
	mockHost := &MockHost{}

	// Setup mock to return no workers
	mockHost.On("NewStream", mock.Anything, mock.Anything, mock.Anything).Return(nil, fmt.Errorf("stream error"))

	// Create gateway
	gw, err := NewGateway(context.Background(), logger, &crowdllamapeer.Peer{
		Host:        mockHost,
		DHT:         (*dht.IpfsDHT)(unsafe.Pointer(&StubDHT{})),
		PeerManager: &MockPeerManager{healthyPeers: map[string]*peermanager.PeerInfo{}},
	})
	assert.NoError(t, err)

	// Create test request
	reqBody := GenerateRequest{
		Model: "llama3.2",
		Messages: []Message{
			{Role: "user", Content: "Hello, how are you?"},
		},
		Stream: false,
	}

	reqJSON, err := json.Marshal(reqBody)
	assert.NoError(t, err)

	// Create HTTP request
	req := httptest.NewRequest("POST", "/api/chat", bytes.NewBuffer(reqJSON))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	// Execute request
	gw.handleChat(w, req)

	// Assertions
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	var response map[string]string
	err = json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Equal(t, "No suitable worker found", response["error"])

	// Verify mock expectations
	mockHost.AssertExpectations(t)
}

// TestGateway_handleChat_InvalidRequest tests invalid request handling
func TestGateway_handleChat_InvalidRequest(t *testing.T) {
	// Setup
	logger := zap.NewNop()
	mockHost := &MockHost{}

	// Create gateway
	gw, err := NewGateway(context.Background(), logger, &crowdllamapeer.Peer{
		Host:        mockHost,
		DHT:         (*dht.IpfsDHT)(unsafe.Pointer(&StubDHT{})),
		PeerManager: &MockPeerManager{healthyPeers: map[string]*peermanager.PeerInfo{}},
	})
	assert.NoError(t, err)

	tests := []struct {
		name           string
		requestBody    interface{}
		expectedStatus int
		expectedError  string
	}{
		{
			name:           "Missing model",
			requestBody:    GenerateRequest{Messages: []Message{{Role: "user", Content: "test"}}},
			expectedStatus: http.StatusBadRequest,
			expectedError:  "Model is required",
		},
		{
			name:           "No messages",
			requestBody:    GenerateRequest{Model: "llama3.2", Messages: []Message{}},
			expectedStatus: http.StatusBadRequest,
			expectedError:  "At least one message is required",
		},
		{
			name:           "Invalid JSON",
			requestBody:    "invalid json",
			expectedStatus: http.StatusBadRequest,
			expectedError:  "Invalid JSON",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reqBody []byte
			var err error

			if str, ok := tt.requestBody.(string); ok {
				reqBody = []byte(str)
			} else {
				reqBody, err = json.Marshal(tt.requestBody)
				assert.NoError(t, err)
			}

			req := httptest.NewRequest("POST", "/api/chat", bytes.NewBuffer(reqBody))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			gw.handleChat(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			if tt.expectedError != "" {
				assert.Contains(t, w.Body.String(), tt.expectedError)
			}
		})
	}
}

// TestGateway_handleChat_StreamError tests stream error handling
func TestGateway_handleChat_StreamError(t *testing.T) {
	// Setup
	logger := zap.NewNop()
	mockHost := &MockHost{}

	// Create a mock worker
	workerID := "QmWorker123"

	// Setup mock expectations
	mockHost.On("NewStream", mock.Anything, libp2ppeer.ID(workerID), crowdllama.InferenceProtocol).Return(nil, fmt.Errorf("stream error"))

	// Create gateway
	healthyPeers := map[string]*peermanager.PeerInfo{
		workerID: {
			PeerID: workerID,
			Metadata: &crowdllama.Resource{
				PeerID:          workerID,
				SupportedModels: []string{"llama3.2"},
				WorkerMode:      true,
			},
			IsHealthy: true,
		},
	}
	gw, err := NewGateway(context.Background(), logger, &crowdllamapeer.Peer{
		Host:        mockHost,
		DHT:         (*dht.IpfsDHT)(unsafe.Pointer(&StubDHT{})),
		PeerManager: &MockPeerManager{healthyPeers: healthyPeers},
	})
	assert.NoError(t, err)

	// Create test request
	reqBody := GenerateRequest{
		Model: "llama3.2",
		Messages: []Message{
			{Role: "user", Content: "Hello, how are you?"},
		},
		Stream: false,
	}

	reqJSON, err := json.Marshal(reqBody)
	assert.NoError(t, err)

	// Create HTTP request
	req := httptest.NewRequest("POST", "/api/chat", bytes.NewBuffer(reqJSON))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	// Execute request
	gw.handleChat(w, req)

	// Assertions
	assert.Equal(t, http.StatusInternalServerError, w.Code)

	var response map[string]string
	err = json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Contains(t, response["error"], "stream error")

	// Verify mock expectations
	mockHost.AssertExpectations(t)
}
