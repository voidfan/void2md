// Package chunk provides layout-aware Markdown chunking.
package chunk

// Element is a parsed layout block.
type Element struct {
	ID        int            // unique id
	Type      string         // title | table | code | figure | text
	Text      string         // raw text
	Metadata  map[string]any // level / parent_id / lang / format
	StartLine int            // 1-based start line
	EndLine   int            // 1-based end line
}

// Chunk is the final chunking output.
type Chunk struct {
	Index     int    `json:"index"`
	Text      string `json:"text"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Type      string `json:"type"`
}

// TYPE_PRIORITY ranks chunk types (lower wins); used to pick the dominant
// type when merging.
var TYPE_PRIORITY = map[string]int{
	"title":  0,
	"figure": 1,
	"table":  2,
	"code":   3,
	"text":   4,
}

// ELEM_TO_CHUNK maps Element types to Chunk types.
var ELEM_TO_CHUNK = map[string]string{
	"title":    "title",
	"text":     "text",
	"figure":   "figure",
	"table":    "table",
	"code":     "code",
	"abstract": "text",
}

// resolveType picks the dominant type by priority; empty set falls back to text.
func resolveType(types []string) string {
	best := ""
	bestPri := 999
	for _, t := range types {
		p, ok := TYPE_PRIORITY[t]
		if ok && p < bestPri {
			bestPri = p
			best = t
		}
	}
	if best == "" {
		return "text"
	}
	return best
}
