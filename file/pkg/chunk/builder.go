package chunk

import (
	"math"
	"strings"
	"sync"
)

// bufferPool 用于重用 bytes.Buffer，减少内存分配
var bufferPool = sync.Pool{
	New: func() interface{} {
		return &strings.Builder{}
	},
}

// getStringBuilder 获取一个重用的 strings.Builder
func getStringBuilder() *strings.Builder {
	return bufferPool.Get().(*strings.Builder)
}

// putStringBuilder 归还 strings.Builder 到池中
func putStringBuilder(sb *strings.Builder) {
	sb.Reset()
	bufferPool.Put(sb)
}

// -- ChunkSink --

// ChunkSink 收集所有 emit 出的 Chunk。
type ChunkSink struct {
	Docs []Chunk
}

// Push 添加一个 chunk 到收集器。
func (s *ChunkSink) Push(chunk Chunk) {
	if strings.TrimSpace(chunk.Text) == "" {
		return
	}
	chunk.Index = len(s.Docs)
	chunk.Text = strings.TrimSpace(chunk.Text)
	s.Docs = append(s.Docs, chunk)
}

// -- ChunkBuilder --

// ChunkBuilder 构建中的 chunk 状态机。
type ChunkBuilder struct {
	Text              string
	LineStart         int
	LineEnd           int
	Types             []string
	CurrentTitle      *Element
	CurrentTitleLevel int
	currentTokens     int // 跟踪当前token数，避免重复计算
}

// IsEmpty 检查 builder 是否为空。
func (b *ChunkBuilder) IsEmpty() bool {
	return strings.TrimSpace(b.Text) == ""
}

// Tokens 返回当前文本的 token 数。
func (b *ChunkBuilder) Tokens() int {
	if b.currentTokens == 0 && b.Text != "" {
		b.currentTokens = TLen(b.Text)
	}
	return b.currentTokens
}

// Track 合入元素时更新行号窗口和类型集合。
func (b *ChunkBuilder) Track(el *Element) {
	if el == nil {
		return
	}
	if el.StartLine > 0 {
		if b.LineStart == 0 || el.StartLine < b.LineStart {
			b.LineStart = el.StartLine
		}
		if el.EndLine > b.LineEnd {
			b.LineEnd = el.EndLine
		}
	}
	ct, ok := ELEM_TO_CHUNK[el.Type]
	if ok && el.Type != "title" {
		// 去重添加
		found := false
		for _, t := range b.Types {
			if t == ct {
				found = true
				break
			}
		}
		if !found {
			b.Types = append(b.Types, ct)
		}
	}
}

// Append 无条件追加文本。
func (b *ChunkBuilder) Append(text string, el *Element) {
	if text == "" {
		return
	}
	if b.Text != "" {
		sb := getStringBuilder()
		sb.WriteString(b.Text)
		sb.WriteString("\n")
		sb.WriteString(text)
		b.Text = sb.String()
		b.currentTokens = AddTokens(b.Tokens(), "\n"+text)
		putStringBuilder(sb)
	} else {
		b.currentTokens = CountTokens(text)
		b.Text = text
	}
	b.Track(el)
}

// Prepend 在文本前注入前缀。
func (b *ChunkBuilder) Prepend(prefix string) {
	if prefix == "" {
		return
	}
	if b.Text != "" {
		sb := getStringBuilder()
		sb.WriteString(prefix)
		sb.WriteString("\n")
		sb.WriteString(b.Text)
		b.Text = sb.String()
		b.currentTokens = AddTokens(CountTokens(prefix), "\n"+b.Text)
		putStringBuilder(sb)
	} else {
		b.currentTokens = CountTokens(prefix)
		b.Text = prefix
	}
}

// Replace 覆盖式重置内容。
func (b *ChunkBuilder) Replace(text string, el *Element) {
	b.Text = text
	b.currentTokens = 0
	if el != nil {
		b.Track(el)
	}
}

// Emit 将当前缓冲区作为 Chunk 推入 sink，然后 Reset。
func (b *ChunkBuilder) Emit(sink *ChunkSink, el *Element) {
	if b.IsEmpty() {
		b.Reset()
		return
	}
	chunkType := resolveType(b.Types)
	chunk := Chunk{
		Text:      b.Text,
		StartLine: b.LineStart,
		EndLine:   b.LineEnd,
		Type:      chunkType,
	}
	sink.Push(chunk)
	b.Reset()
}

// Reset 清空所有状态。
func (b *ChunkBuilder) Reset() {
	b.Text = ""
	b.currentTokens = 0
	b.LineStart = 0
	b.LineEnd = 0
	b.Types = nil
}

// -- ProcessContext --

// ProcessContext 单次 Chunk() 调用的全部上下文。
type ProcessContext struct {
	Builder         *ChunkBuilder
	Sink            *ChunkSink
	ChunkSizes      map[string]int
	Separator       string
	MaxChunkRatio   float64
	Elements        []Element
	ID2Idx          map[int]int
	AbstractIDs     map[int]bool
	TokenizerInited bool
}

// MergeBudget 合并预算：maxChunkRatio * chunkSize。
func (ctx *ProcessContext) MergeBudget(contentType string) int {
	if ctx.MaxChunkRatio <= 0 {
		return math.MaxInt32
	}
	base, ok := ctx.ChunkSizes[contentType]
	if !ok {
		base = ctx.ChunkSizes["text"]
	}
	return int(math.Round(ctx.MaxChunkRatio * float64(base)))
}

// -- 标题前缀工具 --

// ExtractHeadingPrefix 提取文本开头连续的 # 标题行。
func ExtractHeadingPrefix(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "#") {
			out = append(out, s)
		} else {
			if len(out) > 0 {
				break
			}
			if s == "" {
				continue
			}
			break
		}
	}
	return out
}

// ParentsOf 沿 anchor 的 parent_id 链上溯，返回父级标题文本列表。
func ParentsOf(elements []Element, anchor *Element) []Element {
	if anchor == nil {
		return nil
	}
	var parents []Element
	cur := anchor
	maxDepth := 6
	for maxDepth > 0 {
		maxDepth--
		pid, ok := cur.Metadata["parent_id"].(int)
		if !ok || pid == 0 {
			break
		}
		// 查找 parent element
		found := false
		for i := range elements {
			if elements[i].ID == pid {
				parent := elements[i]
				if parent.Text == "" {
					break
				}
				parents = append(parents, parent)
				cur = &elements[i]
				found = true
				break
			}
		}
		if !found {
			break
		}
	}
	// reverse
	for i, j := 0, len(parents)-1; i < j; i, j = i+1, j-1 {
		parents[i], parents[j] = parents[j], parents[i]
	}
	return parents
}

// BuildPrefix 构造标题前缀。
func BuildPrefix(ctx *ProcessContext, title *Element, parents []Element, contentType string) string {
	budget := ctx.MergeBudget(contentType)
	var parts []string
	if title != nil && title.Text != "" {
		parts = append(parts, title.Text)
	}
	// parents 已反转（从近到远），这里需要从远到近
	for i := len(parents) - 1; i >= 0; i-- {
		t := parents[i].Text
		if t == "" {
			continue
		}
		if TLen(strings.Join(append([]string{t}, parts...), "\n")) < budget {
			parts = append([]string{t}, parts...)
		} else {
			break
		}
	}
	return strings.TrimRight(strings.Join(parts, "\n"), "\n")
}

// DedupePrefixForBuilder 对照 builder.Text 删掉 prefix 中已存在的标题部分。
func DedupePrefixForBuilder(prefix string, builder *ChunkBuilder) string {
	if prefix == "" || builder.IsEmpty() {
		return prefix
	}
	if HeadingsSubsequence(prefix, builder.Text) {
		return ""
	}
	n := TrailingHeadingMatch(builder.Text, prefix)
	if n == 0 {
		return prefix
	}
	pc := strings.Split(prefix, "\n")
	if len(pc) <= n {
		return ""
	}
	return strings.Join(pc[:len(pc)-n], "\n")
}

// HeadingsSubsequence 检查 prefix 中的所有 # 行是否按序出现在 body 中。
func HeadingsSubsequence(prefix, body string) bool {
	var pc []string
	for _, line := range strings.Split(prefix, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			pc = append(pc, line)
		}
	}
	if len(pc) == 0 {
		return false
	}
	cc := strings.Split(body, "\n")
	ci := 0
	for _, ph := range pc {
		for ci < len(cc) && strings.TrimSpace(cc[ci]) != ph {
			ci++
		}
		if ci >= len(cc) {
			return false
		}
		ci++
	}
	return true
}

// TrailingHeadingMatch body 末尾连续标题行与 prefix 末尾标题行匹配的行数。
func TrailingHeadingMatch(body, prefix string) int {
	var cc []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			cc = append(cc, line)
		}
	}
	var pc []string
	for _, line := range strings.Split(prefix, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			pc = append(pc, line)
		}
	}
	if len(cc) == 0 || len(pc) == 0 {
		return 0
	}
	n := 0
	for n < len(cc) && n < len(pc) && cc[len(cc)-1-n] == pc[len(pc)-1-n] {
		n++
	}
	return n
}