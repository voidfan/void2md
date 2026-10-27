package service

import (
	"sync"

	"void2md/file/pkg/chunk"
	"void2md/file/pkg/config"
)

// ChunkResponse is the chunking response.
type ChunkResponse struct {
	Chunks []chunk.Chunk `json:"chunks"`
	Count  int           `json:"count"`
}

var (
	chunkerOnce sync.Once
	chunkerInst *chunk.LayoutChunker
)

// GetChunker returns the singleton LayoutChunker.
func GetChunker() *chunk.LayoutChunker {
	chunkerOnce.Do(func() {
		cfg := config.GetChunkingConfig()
		chunkerInst = chunk.NewLayoutChunker(
			cfg.TokenizerPath,
			cfg.ChunkSizes,
			cfg.Separator,
			cfg.MinChunkRatio,
			cfg.MaxChunkRatio,
		)
	})
	return chunkerInst
}

// ChunkMarkdown chunks markdown text.
func ChunkMarkdown(markdown string, chunkSizes map[string]int) (*ChunkResponse, error) {
	chunker := GetChunker()
	// chunkSizes override: merge on a copy of the singleton; never mutate shared fields.
	// (Temporarily rewriting chunker.ChunkSizes with a deferred restore raced between
	// concurrent requests, leaking one request's config into another.)
	if chunkSizes != nil && len(chunkSizes) > 0 {
		merged := make(map[string]int, len(chunker.ChunkSizes)+len(chunkSizes))
		for k, v := range chunker.ChunkSizes {
			merged[k] = v
		}
		for k, v := range chunkSizes {
			merged[k] = v
		}
		c := *chunker
		c.ChunkSizes = merged
		chunker = &c
	}
	chunks := chunker.Chunk(markdown)
	return &ChunkResponse{Chunks: chunks, Count: len(chunks)}, nil
}
