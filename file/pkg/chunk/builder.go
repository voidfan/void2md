package chunk

import (
	"math"
	"strings"
	"sync"
)

// bufferPool reuses strings.Builders to reduce allocations.
var bufferPool = sync.Pool{
	New: func() interface{} {
		return &strings.Builder{}
	},
}

// getStringBuilder returns a pooled strings.Builder.
func getStringBuilder() *strings.Builder {
	return bufferPool.Get().(*strings.Builder)
}

// putStringBuilder resets and returns a strings.Builder to the pool.
func putStringBuilder(sb *strings.Builder) {
	sb.Reset()
	bufferPool.Put(sb)
}

// -- ChunkSink --

// ChunkSink collects emitted chunks.
type ChunkSink struct {
	Docs []Chunk
}

// Push appends a chunk to the sink.
func (s *ChunkSink) Push(chunk Chunk) {
	if strings.TrimSpace(chunk.Text) == "" {
		return
	}
	chunk.Index = len(s.Docs)
	chunk.Text = strings.TrimSpace(chunk.Text)
	s.Docs = append(s.Docs, chunk)
}

// -- ChunkBuilder --

// ChunkBuilder is the in-progress chunk state.
type ChunkBuilder struct {
	Text              string
	LineStart         int
	LineEnd           int
	Types             []string
	CurrentTitle      *Element
	CurrentTitleLevel int
	currentTokens     int // cached token count, avoids recomputation
}

// IsEmpty reports whether the builder has no content.
func (b *ChunkBuilder) IsEmpty() bool {
	return strings.TrimSpace(b.Text) == ""
}

// Tokens returns the token count of the current text.
func (b *ChunkBuilder) Tokens() int {
	if b.currentTokens == 0 && b.Text != "" {
		b.currentTokens = TLen(b.Text)
	}
	return b.currentTokens
}

// Track updates the line window and type set when merging an element.
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
		// append if not already present
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

// Append unconditionally appends text.
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

// Prepend injects a prefix before the text.
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

// Replace resets the content.
func (b *ChunkBuilder) Replace(text string, el *Element) {
	b.Text = text
	b.currentTokens = 0
	if el != nil {
		b.Track(el)
	}
}

// Emit pushes the buffered content to the sink as a Chunk, then resets.
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

// Reset clears all state.
func (b *ChunkBuilder) Reset() {
	b.Text = ""
	b.currentTokens = 0
	b.LineStart = 0
	b.LineEnd = 0
	b.Types = nil
}

// -- ProcessContext --

// ProcessContext holds all state for one Chunk() call.
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

// MergeBudget returns maxChunkRatio * chunkSize for the content type.
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

// -- Heading prefix helpers --

// ExtractHeadingPrefix extracts the leading consecutive # heading lines.
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

// ParentsOf walks the anchor's parent_id chain and returns ancestor title elements.
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
		// find parent element
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

// BuildPrefix builds the heading prefix.
func BuildPrefix(ctx *ProcessContext, title *Element, parents []Element, contentType string) string {
	budget := ctx.MergeBudget(contentType)
	var parts []string
	if title != nil && title.Text != "" {
		parts = append(parts, title.Text)
	}
	// parents are near-to-far; prefix needs far-to-near
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

// DedupePrefixForBuilder drops heading lines from prefix already present in builder.Text.
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

// HeadingsSubsequence reports whether all # lines in prefix appear in body in order.
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

// TrailingHeadingMatch returns how many trailing heading lines of body match prefix's.
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
