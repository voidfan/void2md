// Package chunk 提供版面感知的 Markdown 分块功能。
package chunk

// Element 是解析后的版面块。
type Element struct {
	ID        int            // 唯一标识符
	Type      string         // title | table | code | figure | text
	Text      string         // 原始文本
	Metadata  map[string]any // level / parent_id / lang / format
	StartLine int            // 1-based 起始行号
	EndLine   int            // 1-based 结束行号
}

// Chunk 是分块最终输出。
type Chunk struct {
	Index     int    `json:"index"`
	Text      string `json:"text"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Type      string `json:"type"`
}

// TYPE_PRIORITY 定义 chunk 类型的优先级（序号越小越优先）。
// 用于合并时从类型集合中选出主导类型。
var TYPE_PRIORITY = map[string]int{
	"title":  0,
	"figure": 1,
	"table":  2,
	"code":   3,
	"text":   4,
}

// ELEM_TO_CHUNK 将 Element 类型映射到 Chunk 类型。
var ELEM_TO_CHUNK = map[string]string{
	"title":    "title",
	"text":     "text",
	"figure":   "figure",
	"table":    "table",
	"code":     "code",
	"abstract": "text",
}

// resolveType 从类型集合中按优先级选主导类型，空集回落 text。
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