package config

import (
	commoncfg "void2md/common/config"
)

// ChunkingConfig is the Markdown chunking module configuration.
type ChunkingConfig struct {
	TokenizerPath string
	ChunkSizes    map[string]int
	Separator     string
	MinChunkRatio float64
	MaxChunkRatio float64
}

// GetChunkingConfig returns the chunking configuration with defaults matching file-parse service
func GetChunkingConfig() ChunkingConfig {
	// Match file-parse service default exactly: /app/data/qwen3-embedding-tokenizer/tokenizer.json
	defaultTokenizerPath := "/app/data/qwen3-embedding-tokenizer/tokenizer.json"

	return ChunkingConfig{
		TokenizerPath: commoncfg.GetEnv("VOID2MD_CHUNKING_TOKENIZER_PATH", defaultTokenizerPath),
		ChunkSizes: map[string]int{
			"text":   commoncfg.GetEnvInt("VOID2MD_CHUNKING_TEXT_SIZE", 768),
			"figure": commoncfg.GetEnvInt("VOID2MD_CHUNKING_FIGURE_SIZE", 768),
			"code":   commoncfg.GetEnvInt("VOID2MD_CHUNKING_CODE_SIZE", 1536),
			"table":  commoncfg.GetEnvInt("VOID2MD_CHUNKING_TABLE_SIZE", 1536),
		},
		Separator:     commoncfg.GetEnv("VOID2MD_CHUNKING_SEPARATOR", "\n\n\n\n\n"),
		MinChunkRatio: commoncfg.GetEnvFloat64("VOID2MD_CHUNKING_MIN_RATIO", 0.15),
		MaxChunkRatio: commoncfg.GetEnvFloat64("VOID2MD_CHUNKING_MAX_RATIO", 1.5),
	}
}
