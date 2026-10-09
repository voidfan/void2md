package config

import (
	"os"
	"strconv"
	"strings"
)

// ChunkingConfig Markdown 分块模块配置
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
		TokenizerPath: getEnv("VOID2MD_CHUNKING_TOKENIZER_PATH", defaultTokenizerPath),
		ChunkSizes: map[string]int{
			"text":   getEnvInt("VOID2MD_CHUNKING_TEXT_SIZE", 768),
			"figure": getEnvInt("VOID2MD_CHUNKING_FIGURE_SIZE", 768),
			"code":   getEnvInt("VOID2MD_CHUNKING_CODE_SIZE", 1536),
			"table":  getEnvInt("VOID2MD_CHUNKING_TABLE_SIZE", 1536),
		},
		Separator:     getEnv("VOID2MD_CHUNKING_SEPARATOR", "\n\n\n\n\n"),
		MinChunkRatio: getEnvFloat64("VOID2MD_CHUNKING_MIN_RATIO", 0.15),
		MaxChunkRatio: getEnvFloat64("VOID2MD_CHUNKING_MAX_RATIO", 1.5),
	}
}

// Helper functions for environment variable parsing

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return defaultVal
	}
	return n
}

func getEnvFloat64(key string, defaultVal float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return defaultVal
	}
	return n
}

func getEnvStringSlice(key string, defaultVal []string) []string {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	parts := strings.Split(v, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	if len(result) == 0 {
		return defaultVal
	}
	return result
}