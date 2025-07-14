package crowdllama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	llamav1 "github.com/crowdllama/crowdllama-pb/llama/v1"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// UnifiedAPIHandler processes a BaseMessage and returns a BaseMessage response
type UnifiedAPIHandler func(ctx context.Context, req *llamav1.BaseMessage) (*llamav1.BaseMessage, error)

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

// WorkerAPIHandler provides a worker implementation that calls Ollama API
func WorkerAPIHandler(ollamaBaseURL string) UnifiedAPIHandler {
	return func(ctx context.Context, req *llamav1.BaseMessage) (*llamav1.BaseMessage, error) {
		// Check if this is a GenerateRequest
		generateReq := req.GetGenerateRequest()
		if generateReq == nil {
			return nil, fmt.Errorf("expected GenerateRequest, got different message type")
		}

		// Get logger from context if available
		logger := getLoggerFromContext(ctx)
		if logger != nil {
			logger.Debug("Worker calling Ollama API",
				zap.String("model", generateReq.Model),
				zap.String("ollama_url", ollamaBaseURL))
		}

		// Call Ollama API
		ollamaResp, err := callOllamaAPI(ctx, generateReq, ollamaBaseURL)
		if err != nil {
			if logger != nil {
				logger.Error("Failed to call Ollama API", zap.Error(err))
			}
			return nil, fmt.Errorf("failed to call Ollama API: %w", err)
		}

		if logger != nil {
			logger.Debug("Worker received Ollama response",
				zap.String("model", ollamaResp.Model),
				zap.Bool("done", ollamaResp.Done))
		}

		// Convert Ollama response to protobuf
		response := &llamav1.GenerateResponse{
			Model:         ollamaResp.Model,
			CreatedAt:     timestamppb.New(ollamaResp.CreatedAt),
			Response:      ollamaResp.Message.Content,
			Done:          ollamaResp.Done,
			DoneReason:    ollamaResp.DoneReason,
			WorkerId:      "worker", // TODO: Get actual worker ID
			TotalDuration: time.Now().UnixNano(),
		}

		// Wrap in BaseMessage
		baseResponse := &llamav1.BaseMessage{
			Message: &llamav1.BaseMessage_GenerateResponse{
				GenerateResponse: response,
			},
		}

		return baseResponse, nil
	}
}

// getLoggerFromContext extracts logger from context if available
func getLoggerFromContext(ctx context.Context) *zap.Logger {
	if logger, ok := ctx.Value("logger").(*zap.Logger); ok {
		return logger
	}
	return nil
}

// callOllamaAPI calls the Ollama API with the given request
func callOllamaAPI(ctx context.Context, req *llamav1.GenerateRequest, baseURL string) (*OllamaResponse, error) {
	ollamaReq := OllamaRequest{
		Model: req.Model,
		Messages: []Message{
			{
				Role:    "user",
				Content: req.Prompt,
			},
		},
		Stream: req.Stream,
	}

	reqBody, err := json.Marshal(ollamaReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal Ollama request: %w", err)
	}

	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}

	fullURL := baseURL + "/api/chat"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", fullURL, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to make HTTP request to Ollama: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read Ollama response: %w", err)
	}

	var ollamaResp OllamaResponse
	if err := json.Unmarshal(respBody, &ollamaResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal Ollama response: %w", err)
	}

	return &ollamaResp, nil
}

// DefaultAPIHandler provides a basic implementation that processes GenerateRequest
func DefaultAPIHandler(ctx context.Context, req *llamav1.BaseMessage) (*llamav1.BaseMessage, error) {
	// Check if this is a GenerateRequest
	generateReq := req.GetGenerateRequest()
	if generateReq == nil {
		return nil, fmt.Errorf("expected GenerateRequest, got different message type")
	}

	// Create a response
	response := &llamav1.GenerateResponse{
		Model:         generateReq.Model,
		CreatedAt:     timestamppb.Now(),
		Response:      fmt.Sprintf("Generated response for model %s with prompt: %s", generateReq.Model, generateReq.Prompt),
		Done:          true,
		DoneReason:    "stop",
		WorkerId:      "default-worker",
		TotalDuration: time.Now().UnixNano(),
	}

	// Wrap in BaseMessage
	baseResponse := &llamav1.BaseMessage{
		Message: &llamav1.BaseMessage_GenerateResponse{
			GenerateResponse: response,
		},
	}

	return baseResponse, nil
}

// CreateGenerateRequest creates a BaseMessage containing a GenerateRequest
func CreateGenerateRequest(model, prompt string, stream bool) *llamav1.BaseMessage {
	req := &llamav1.GenerateRequest{
		Model:  model,
		Prompt: prompt,
		Stream: stream,
	}

	return &llamav1.BaseMessage{
		Message: &llamav1.BaseMessage_GenerateRequest{
			GenerateRequest: req,
		},
	}
}

// ExtractGenerateRequest safely extracts a GenerateRequest from a BaseMessage
func ExtractGenerateRequest(msg *llamav1.BaseMessage) (*llamav1.GenerateRequest, error) {
	req := msg.GetGenerateRequest()
	if req == nil {
		return nil, fmt.Errorf("message does not contain a GenerateRequest")
	}
	return req, nil
}

// ExtractGenerateResponse safely extracts a GenerateResponse from a BaseMessage
func ExtractGenerateResponse(msg *llamav1.BaseMessage) (*llamav1.GenerateResponse, error) {
	resp := msg.GetGenerateResponse()
	if resp == nil {
		return nil, fmt.Errorf("message does not contain a GenerateResponse")
	}
	return resp, nil
}
