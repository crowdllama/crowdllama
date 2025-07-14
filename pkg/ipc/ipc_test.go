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
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestIPCServer_PBPrompt(t *testing.T) {
	socketPath := "/tmp/crowdllama_ipc_test.sock"
	_ = os.Remove(socketPath)

	// Mock API handler
	mockHandler := func(ctx context.Context, req *llamav1.BaseMessage) (*llamav1.BaseMessage, error) {
		genReq := req.GetGenerateRequest()
		resp := &llamav1.GenerateResponse{
			Model:      genReq.GetModel(),
			CreatedAt:  timestamppb.Now(),
			Response:   "PB Hello, " + genReq.GetPrompt(),
			Done:       true,
			DoneReason: "stop",
			WorkerId:   "test-worker",
		}
		return &llamav1.BaseMessage{
			Message: &llamav1.BaseMessage_GenerateResponse{
				GenerateResponse: resp,
			},
		}, nil
	}

	logger := zap.NewNop()
	server := NewServer(socketPath, logger)
	server.SetAPIHandler(mockHandler)

	// Start server in background
	go func() {
		_ = server.Start()
	}()
	defer func() {
		_ = server.Stop()
		_ = os.Remove(socketPath)
	}()

	// Wait for server to start
	time.Sleep(200 * time.Millisecond)

	// Connect as client
	conn, err := net.Dial("unix", socketPath)
	assert.NoError(t, err)
	defer conn.Close()

	// Create PB request
	pbReq := crowdllama.CreateGenerateRequest("llama3.2", "world!", false)
	data, err := proto.Marshal(pbReq)
	assert.NoError(t, err)

	// Send PB request
	_, err = conn.Write(data)
	assert.NoError(t, err)

	// Read PB response
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	assert.NoError(t, err)

	var pbResp llamav1.BaseMessage
	err = proto.Unmarshal(buf[:n], &pbResp)
	assert.NoError(t, err)

	genResp := pbResp.GetGenerateResponse()
	assert.NotNil(t, genResp)
	assert.Equal(t, "llama3.2", genResp.GetModel())
	assert.Equal(t, "PB Hello, world!", genResp.GetResponse())
	assert.True(t, genResp.GetDone())
	assert.Equal(t, "stop", genResp.GetDoneReason())
}
