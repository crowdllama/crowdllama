package crowdllama

import (
	"encoding/binary"
	"fmt"
	"io"

	"google.golang.org/protobuf/proto"

	llamav1 "github.com/crowdllama/crowdllama-pb/llama/v1"
)

// WriteLengthPrefixedPB writes a protobuf message with a 4-byte length prefix
func WriteLengthPrefixedPB(w io.Writer, msg *llamav1.BaseMessage) error {
	data, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal protobuf message: %w", err)
	}

	// Write 4-byte length prefix (big-endian)
	const maxMessageSize = 0xffffffff
	msgLen := len(data)
	if msgLen > maxMessageSize {
		return fmt.Errorf("message too large: %d bytes", msgLen)
	}
	length := uint32(msgLen)
	lengthBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(lengthBytes, length)

	// Write length prefix
	if _, err := w.Write(lengthBytes); err != nil {
		return fmt.Errorf("failed to write length prefix: %w", err)
	}

	// Write protobuf data
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("failed to write protobuf data: %w", err)
	}

	return nil
}

// ReadLengthPrefixedPB reads a length-prefixed protobuf message
func ReadLengthPrefixedPB(r io.Reader) (*llamav1.BaseMessage, error) {
	// Read 4-byte length prefix
	lengthBytes := make([]byte, 4)
	if _, err := io.ReadFull(r, lengthBytes); err != nil {
		return nil, fmt.Errorf("failed to read length prefix: %w", err)
	}

	// Parse length (big-endian)
	length := binary.BigEndian.Uint32(lengthBytes)
	if length > 10*1024*1024 { // 10MB limit
		return nil, fmt.Errorf("message too large: %d bytes", length)
	}

	// Read protobuf data
	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("failed to read protobuf data: %w", err)
	}

	// Unmarshal protobuf message
	var msg llamav1.BaseMessage
	if err := proto.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal protobuf message: %w", err)
	}

	return &msg, nil
}
