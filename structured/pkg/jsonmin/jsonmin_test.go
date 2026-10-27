// Ports the three text2json cases from Python tests/unit/core/utils.py plus fault-tolerance
// edge cases (see jsonmin.go header). Text2JSON keeps dicts only; Loads retains lower-level results.
package jsonmin

import (
	"encoding/json"
	"testing"
)

// TestText2JSONValidJSON corresponds to test_text2json_valid_json.
func TestText2JSONValidJSON(t *testing.T) {
	got := Text2JSON(`{"name": "Alice", "age": 30}`)
	want := map[string]any{"name": "Alice", "age": float64(30)}
	if !equalJSON(t, got, want) {
		t.Fatalf("Text2JSON = %v, want %v", got, want)
	}
}

// TestText2JSONInvalidJSON corresponds to test_text2json_invalid_json.
func TestText2JSONInvalidJSON(t *testing.T) {
	got := Text2JSON("invalid json")
	if len(got) != 0 {
		t.Fatalf("invalid JSON should return an empty map, got %v", got)
	}
}

// TestText2JSONNonDictResult corresponds to test_text2json_non_dict_result.
func TestText2JSONNonDictResult(t *testing.T) {
	got := Text2JSON(`["not", "a", "dict"]`)
	if len(got) != 0 {
		t.Fatalf("array result should return an empty map, got %v", got)
	}
}

// TestText2JSONRepairs: fault-tolerance branches from the jsonmin.go header.
func TestText2JSONRepairs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want map[string]any
	}{
		{"unquoted key", `{a:1}`, map[string]any{"a": float64(1)}},
		{"missing closing brace", `{"a":"b"`, map[string]any{"a": "b"}},
		{"trailing garbage", `{"answer":"x"}垃圾`, map[string]any{"answer": "x"}},
		{"code fence", "```json\n{\"a\":1}\n```", map[string]any{"a": float64(1)}},
		{"empty string", "", map[string]any{}},
		{"null", "null", map[string]any{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Text2JSON(tt.in)
			if !equalJSON(t, got, tt.want) {
				t.Fatalf("Text2JSON(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// equalJSON compares via serialization to avoid map-order and numeric-type differences.
func equalJSON(t *testing.T, a, b map[string]any) bool {
	t.Helper()
	ab, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("failed to serialize left value: %v", err)
	}
	bb, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("failed to serialize right value: %v", err)
	}
	return string(ab) == string(bb)
}
