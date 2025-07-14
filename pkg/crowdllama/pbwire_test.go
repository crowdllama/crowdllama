package crowdllama

import (
	"bytes"
	"testing"

	llamav1 "github.com/crowdllama/crowdllama-pb/llama/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestWriteReadLengthPrefixedPB(t *testing.T) {
	// Create a test message
	originalMsg := &llamav1.BaseMessage{
		Message: &llamav1.BaseMessage_GenerateRequest{
			GenerateRequest: &llamav1.GenerateRequest{
				Model:  "test-model",
				Prompt: "test prompt",
				Stream: false,
			},
		},
	}

	// Write to buffer
	var buf bytes.Buffer
	err := WriteLengthPrefixedPB(&buf, originalMsg)
	if err != nil {
		t.Fatalf("Failed to write length-prefixed PB: %v", err)
	}

	// Read from buffer
	readMsg, err := ReadLengthPrefixedPB(&buf)
	if err != nil {
		t.Fatalf("Failed to read length-prefixed PB: %v", err)
	}

	// Verify the message is the same
	if readMsg.GetGenerateRequest().Model != originalMsg.GetGenerateRequest().Model {
		t.Errorf("Model mismatch: got %s, want %s",
			readMsg.GetGenerateRequest().Model,
			originalMsg.GetGenerateRequest().Model)
	}

	if readMsg.GetGenerateRequest().Prompt != originalMsg.GetGenerateRequest().Prompt {
		t.Errorf("Prompt mismatch: got %s, want %s",
			readMsg.GetGenerateRequest().Prompt,
			originalMsg.GetGenerateRequest().Prompt)
	}
}

func TestWriteReadLengthPrefixedPBResponse(t *testing.T) {
	// Create a test response message
	originalMsg := &llamav1.BaseMessage{
		Message: &llamav1.BaseMessage_GenerateResponse{
			GenerateResponse: &llamav1.GenerateResponse{
				Model:         "test-model",
				CreatedAt:     timestamppb.Now(),
				Response:      "test response",
				Done:          true,
				DoneReason:    "stop",
				WorkerId:      "test-worker",
				TotalDuration: 123456789,
			},
		},
	}

	// Write to buffer
	var buf bytes.Buffer
	err := WriteLengthPrefixedPB(&buf, originalMsg)
	if err != nil {
		t.Fatalf("Failed to write length-prefixed PB: %v", err)
	}

	// Read from buffer
	readMsg, err := ReadLengthPrefixedPB(&buf)
	if err != nil {
		t.Fatalf("Failed to read length-prefixed PB: %v", err)
	}

	// Verify the message is the same
	if readMsg.GetGenerateResponse().Model != originalMsg.GetGenerateResponse().Model {
		t.Errorf("Model mismatch: got %s, want %s",
			readMsg.GetGenerateResponse().Model,
			originalMsg.GetGenerateResponse().Model)
	}

	if readMsg.GetGenerateResponse().Response != originalMsg.GetGenerateResponse().Response {
		t.Errorf("Response mismatch: got %s, want %s",
			readMsg.GetGenerateResponse().Response,
			originalMsg.GetGenerateResponse().Response)
	}
}
