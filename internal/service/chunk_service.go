package service

import (
	"sync"

	"void2md-api/internal/chunk"
	"void2md-api/internal/config"
)

// ChunkResponse chunk 响应
type ChunkResponse struct {
	Chunks []chunk.Chunk `json:"chunks"`
	Count  int           `json:"count"`
}

var (
	chunkerOnce sync.Once
	chunkerInst *chunk.LayoutChunker
)

// GetChunker 返回单例 LayoutChunker
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

// ChunkMarkdown 对 markdown 文本执行分块
func ChunkMarkdown(markdown string, chunkSizes map[string]int) (*ChunkResponse, error) {
	chunker := GetChunker()
	// chunkSizes 覆盖：在单例副本上合并，绝不改动共享单例的字段。
	// 此前直接临时改写 chunker.ChunkSizes 再 defer 还原，并发请求下彼此
	// 覆盖/恢复，形成数据竞争，还会读到别的请求的配置。
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