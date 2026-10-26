package service

import (
	"testing"
)

func TestChunkMarkdown(t *testing.T) {
	// Test with simple markdown
	markdown := "# Title\n\nThis is a paragraph.\n\n```go\nfunc main() {\n\tprintln(\"hello\")\n}\n```\n\n| Col1 | Col2 |\n|------|------|\n| Val1 | Val2 |"

	result, err := ChunkMarkdown(markdown, nil)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if result.Count == 0 {
		t.Error("Expected at least one chunk, got 0")
	}

	// Verify chunks have proper structure
	for i, chunk := range result.Chunks {
		if chunk.Text == "" {
			t.Errorf("Chunk %d has empty text", i)
		}
		if chunk.Type == "" {
			t.Errorf("Chunk %d has empty type", i)
		}
		if chunk.Index != i {
			t.Errorf("Chunk %d has wrong index, expected %d, got %d", i, i, chunk.Index)
		}
	}
}

func TestChunkMarkdownWithCustomSizes(t *testing.T) {
	markdown := "This is a short paragraph."

	// Test with custom chunk sizes
	customSizes := map[string]int{
		"text": 10,
	}

	result, err := ChunkMarkdown(markdown, customSizes)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if result.Count == 0 {
		t.Error("Expected at least one chunk, got 0")
	}
}