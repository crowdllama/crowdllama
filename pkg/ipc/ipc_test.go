package ipc

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	llamav1 "github.com/crowdllama/crowdllama-pb/llama/v1"
	"github.com/crowdllama/crowdllama/pkg/crowdllama"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestIPCServer_PBPrompt(t *testing.T) {
	t.Log("Starting TestIPCServer_PBPrompt")

	socketPath := "/tmp/crowdllama_ipc_test.sock"
	_ = os.Remove(socketPath)
	t.Logf("Using socket path: %s", socketPath)

	// Mock API handler with logging
	mockHandler := func(ctx context.Context, req *llamav1.BaseMessage) (*llamav1.BaseMessage, error) {
		t.Log("Mock API handler called")
		genReq := req.GetGenerateRequest()
		if genReq == nil {
			t.Log("Error: Request does not contain GenerateRequest")
			return nil, assert.AnError
		}

		t.Logf("Mock handler processing request - Model: %s, Prompt: %s", genReq.GetModel(), genReq.GetPrompt())

		resp := &llamav1.GenerateResponse{
			Model:      genReq.GetModel(),
			CreatedAt:  timestamppb.Now(),
			Response:   "PB Hello, " + genReq.GetPrompt(),
			Done:       true,
			DoneReason: "stop",
			WorkerId:   "test-worker",
		}

		baseResp := &llamav1.BaseMessage{
			Message: &llamav1.BaseMessage_GenerateResponse{
				GenerateResponse: resp,
			},
		}

		t.Logf("Mock handler returning response: %s", resp.GetResponse())
		return baseResp, nil
	}

	logger := zap.NewNop()
	server := NewServer(socketPath, logger)
	server.SetAPIHandler(mockHandler)

	t.Log("Starting IPC server")
	// Start server in background
	go func() {
		t.Log("IPC server goroutine started")
		err := server.Start()
		if err != nil {
			t.Logf("IPC server error: %v", err)
		}
	}()
	defer func() {
		t.Log("Stopping IPC server")
		_ = server.Stop()
		_ = os.Remove(socketPath)
	}()

	// Wait for server to start
	t.Log("Waiting for server to start...")
	time.Sleep(200 * time.Millisecond)

	// Connect as client
	t.Log("Connecting to IPC server...")
	conn, err := net.Dial("unix", socketPath)
	assert.NoError(t, err)
	defer func() {
		t.Log("Closing client connection")
		conn.Close()
	}()

	t.Log("Client connected successfully")

	// Create PB request
	pbReq := crowdllama.CreateGenerateRequest("llama3.2", "world!", false)
	t.Logf("Created PB request - Model: %s, Prompt: %s", pbReq.GetGenerateRequest().GetModel(), pbReq.GetGenerateRequest().GetPrompt())

	// Use length-prefixed format
	t.Log("Writing length-prefixed PB request...")
	err = crowdllama.WriteLengthPrefixedPB(conn, pbReq)
	assert.NoError(t, err)
	t.Log("PB request written successfully")

	// Read PB response with timeout
	t.Log("Reading PB response...")
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))

	pbResp, err := crowdllama.ReadLengthPrefixedPB(conn)
	if err != nil {
		t.Logf("Error reading PB response: %v", err)
		assert.NoError(t, err)
		return
	}
	t.Log("PB response read successfully")

	genResp := pbResp.GetGenerateResponse()
	assert.NotNil(t, genResp)
	assert.Equal(t, "llama3.2", genResp.GetModel())
	assert.Equal(t, "PB Hello, world!", genResp.GetResponse())
	assert.True(t, genResp.GetDone())
	assert.Equal(t, "stop", genResp.GetDoneReason())

	t.Log("Test completed successfully")
}
