package sqleval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestPromptParity: byte-for-byte check of the prompt constant against the frozen Python
// text (golden file), since the prompt is user-observable LLM input.
func TestPromptParity(t *testing.T) {
	path := filepath.Join("..", "..", "tools", "oracle", "golden_prompts.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read golden %s: %v", path, err)
	}
	var g struct {
		SQLEval struct {
			Prompt string `json:"prompt"`
		} `json:"sqleval"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("failed to parse golden %s: %v", path, err)
	}

	want := g.SQLEval.Prompt
	if prompt == want {
		return
	}
	t.Errorf("prompt does not match the original Python text: length got=%d want=%d\n  %s",
		len(prompt), len(want), diffContext(prompt, want))
}

// diffContext shows context around the first difference instead of dumping the whole prompt.
func diffContext(got, want string) string {
	i := 0
	for i < len(got) && i < len(want) && got[i] == want[i] {
		i++
	}
	lo := max(i-40, 0)
	ge, we := min(i+40, len(got)), min(i+40, len(want))
	return fmt.Sprintf("first difference at byte %d\n  got : %q\n  want: %q", i, got[lo:ge], want[lo:we])
}
