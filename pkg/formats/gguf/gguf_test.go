package gguf

import (
	"bytes"
	"io"
	"testing"
)

func TestChunkReader(t *testing.T) {
	data := make([]byte, 3*firstChunk+123)
	for i := range data {
		data[i] = byte(i % 251)
	}
	got, err := io.ReadAll(newChunkReader(bytes.NewReader(data), int64(len(data))))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("len=%d err=%v", len(got), err)
	}
}
