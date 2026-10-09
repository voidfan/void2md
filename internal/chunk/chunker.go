package chunk

import (
	"log/slog"
	"strings"
)

// DEFAULT_CHUNK_SIZES 默认 chunk size 预算。
var DEFAULT_CHUNK_SIZES = map[string]int{
	"text":   768,
	"figure": 768,
	"code":   1536,
	"table":  1536,
}

// LayoutChunker Markdown 版面规则分块器。
type LayoutChunker struct {
	TokenizerPath string
	ChunkSizes    map[string]int
	Separator     string
	MinChunkRatio float64
	MaxChunkRatio float64
}

// NewLayoutChunker 创建新的 LayoutChunker。
func NewLayoutChunker(tokenizerPath string, chunkSizes map[string]int, separator string, minRatio, maxRatio float64) *LayoutChunker {
	merged := make(map[string]int)
	for k, v := range DEFAULT_CHUNK_SIZES {
		merged[k] = v
	}
	for k, v := range chunkSizes {
		merged[k] = v
	}
	return &LayoutChunker{
		TokenizerPath: tokenizerPath,
		ChunkSizes:    merged,
		Separator:     separator,
		MinChunkRatio: minRatio,
		MaxChunkRatio: maxRatio,
	}
}

// Chunk 执行完整的版面感知分块流程。
func (lc *LayoutChunker) Chunk(text string) []Chunk {
	if strings.TrimSpace(text) == "" {
		return nil
	}

	// 1. 初始化 tokenizer（纯 Go fallback 为 no-op）
	if err := InitTokenizer(lc.TokenizerPath); err != nil {
		slog.Warn("tokenizer init failed, using fallback estimation", "path", lc.TokenizerPath, "error", err)
	}

	// 2. Parse → Elements
	elements := Parse(text, lc.Separator)
	if len(elements) == 0 {
		return nil
	}

	// 3. 构建 id2idx 映射
	id2idx := make(map[int]int)
	for i, el := range elements {
		id2idx[el.ID] = i
	}

	// 4. 创建上下文
	ctx := &ProcessContext{
		Builder:       &ChunkBuilder{},
		Sink:          &ChunkSink{},
		ChunkSizes:    lc.ChunkSizes,
		Separator:     lc.Separator,
		MaxChunkRatio: lc.MaxChunkRatio,
		Elements:      elements,
		ID2Idx:        id2idx,
		AbstractIDs:   make(map[int]bool),
	}

	// 5. 提取摘要
	ExtractAbstract(ctx)

	// 6. 主循环：dispatch 路由
	for i := 0; i < len(elements); i++ {
		if ctx.AbstractIDs[elements[i].ID] {
			continue
		}
		skip := Dispatch(ctx, i)
		i += skip
	}

	// 7. 收尾
	ctx.Builder.Emit(ctx.Sink, nil)

	// 8. 后处理
	chunks := PostProcess(ctx.Sink.Docs, lc.ChunkSizes, lc.MinChunkRatio, lc.MaxChunkRatio)

	// 9. HTML block 空行规整（对齐 Python chunker._ensure_block_blank_lines）：
	// table/figure/text 类型的 chunk 可能含结构性 HTML 表格标签或图片（前端会转 <img>），
	// 与相邻 Markdown 之间缺少空行会导致 CommonMark 把后续内容吞进 HTML block 或加粗
	// 渲染失败。code 类型跳过：围栏代码块内的 ![](url) 是字面量，不应改动。
	for i := range chunks {
		if chunks[i].Type != "code" {
			chunks[i].Text = ensureBlockBlankLines(chunks[i].Text)
		}
	}
	return chunks
}