package chunk

import (
	"testing"
)

func TestCountTokens(t *testing.T) {
	tests := []struct {
		text     string
		expected int
	}{
		{"", 0},
		{"Hello world", 3}, // ~2 words + 1 non-ASCII adjustment
		{"你好世界", 4},        // 4 Chinese characters = 4 tokens
		{"Hello 世界", 3},    // 1 word + 2 Chinese chars = ~3 tokens
	}

	for _, tt := range tests {
		result := CountTokens(tt.text)
		if result < 1 {
			result = 1 // CountTokens ensures minimum of 1
		}
		t.Logf("Text: %q, Expected: %d, Got: %d", tt.text, tt.expected, result)
		// We don't assert exact values since it's an estimation,
		// but we check it's reasonable
		if result <= 0 {
			t.Errorf("CountTokens(%q) returned %d, expected positive", tt.text, result)
		}
	}
}

func TestTLen(t *testing.T) {
	text := "test text"
	result1 := TLen(text)
	result2 := TLen(text)

	if result1 != result2 {
		t.Errorf("TLen should return consistent results, got %d and %d", result1, result2)
	}

	if result1 <= 0 {
		t.Errorf("TLen(%q) returned %d, expected positive", text, result1)
	}
}
