package chunk

import (
	"log/slog"
	"strings"

	"void2md/common/logger"
)

// log returns the module logger lazily (see file/pkg/service/crawl).
func log() *slog.Logger { return logger.GetLogger("chunk") }

// DEFAULT_CHUNK_SIZES are the default per-type chunk size budgets.
var DEFAULT_CHUNK_SIZES = map[string]int{
	"text":   768,
	"figure": 768,
	"code":   1536,
	"table":  1536,
}

// LayoutChunker chunks Markdown by layout rules.
type LayoutChunker struct {
	TokenizerPath string
	ChunkSizes    map[string]int
	Separator     string
	MinChunkRatio float64
	MaxChunkRatio float64
}

// NewLayoutChunker creates a LayoutChunker.
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

// Chunk runs the full layout-aware chunking pipeline.
func (lc *LayoutChunker) Chunk(text string) []Chunk {
	if strings.TrimSpace(text) == "" {
		return nil
	}

	// 1. Init tokenizer (pure-Go fallback is a no-op)
	if err := InitTokenizer(lc.TokenizerPath); err != nil {
		log().Warn("tokenizer init failed, using fallback estimation", "path", lc.TokenizerPath, "error", err)
	}

	// 2. Parse → Elements
	elements := Parse(text, lc.Separator)
	if len(elements) == 0 {
		return nil
	}

	// 3. Build id2idx map
	id2idx := make(map[int]int)
	for i, el := range elements {
		id2idx[el.ID] = i
	}

	// 4. Create context
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

	// 5. Extract abstract
	ExtractAbstract(ctx)

	// 6. Main loop: dispatch routing
	for i := 0; i < len(elements); i++ {
		if ctx.AbstractIDs[elements[i].ID] {
			continue
		}
		skip := Dispatch(ctx, i)
		i += skip
	}

	// 7. Flush
	ctx.Builder.Emit(ctx.Sink, nil)

	// 8. Post-process
	chunks := PostProcess(ctx.Sink.Docs, lc.ChunkSizes, lc.MinChunkRatio, lc.MaxChunkRatio)

	// 9. HTML block blank-line fixup (matches Python chunker._ensure_block_blank_lines):
	// table/figure/text chunks may contain structural HTML table tags or images (the
	// frontend converts them to <img>); a missing blank line next to Markdown makes
	// CommonMark swallow following content into the HTML block. Skipped for code chunks:
	// ![](url) inside fenced code is literal.
	for i := range chunks {
		if chunks[i].Type != "code" {
			chunks[i].Text = ensureBlockBlankLines(chunks[i].Text)
		}
	}
	return chunks
}
