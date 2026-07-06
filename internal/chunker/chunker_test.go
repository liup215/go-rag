package chunker

import (
	"testing"
)

func TestNewChunker(t *testing.T) {
	c := NewChunker()
	if c.MaxTokens != 512 {
		t.Errorf("Expected MaxTokens to be 512, got %d", c.MaxTokens)
	}
	if c.Overlap != 100 {
		t.Errorf("Expected Overlap to be 100, got %d", c.Overlap)
	}
}

func TestChunkerSplit(t *testing.T) {
	c := NewChunker()

	// Test empty string
	chunks := c.Split("")
	if chunks != nil {
		t.Error("Expected nil for empty string")
	}

	// Test simple text
	text := "This is paragraph one.\n\nThis is paragraph two.\n\nThis is paragraph three."
	chunks = c.Split(text)
	if len(chunks) == 0 {
		t.Error("Expected non-empty chunks")
	}

	// Test that chunks contain the text
	for i, chunk := range chunks {
		if chunk == "" {
			t.Errorf("Chunk %d is empty", i)
		}
	}
}

func TestChunkerTokenCount(t *testing.T) {
	c := NewChunker()

	// Test token estimation
	text := "Hello world"
	tokens := c.TokenCount(text)
	expected := len(text) / 4
	if tokens != expected {
		t.Errorf("Expected %d tokens, got %d", expected, tokens)
	}
}

func TestNewChunkerWithSize(t *testing.T) {
	c := NewChunkerWithSize(1024, 200)
	if c.MaxTokens != 1024 {
		t.Errorf("Expected MaxTokens to be 1024, got %d", c.MaxTokens)
	}
	if c.Overlap != 200 {
		t.Errorf("Expected Overlap to be 200, got %d", c.Overlap)
	}
}
