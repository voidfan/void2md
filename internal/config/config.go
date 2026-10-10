package config

import (
	"os"
	"strconv"
	"strings"
	"sync"
)

// Config 全局应用配置
type Config struct {
	Host      string `json:"host"`
	Port      int    `json:"port"`
	LogLevel  string `json:"logLevel"`
	LogFormat string `json:"logFormat"`

	HTMLCrawlConfig HTMLCrawlConfig `json:"htmlCrawlConfig"`
}

var (
	cfg  *Config
	once sync.Once
)

// LoadConfig 加载配置，使用 sync.Once 保证单例模式
func LoadConfig() *Config {
	once.Do(func() {
		cfg = &Config{}
		cfg.loadFromEnv()
	})
	return cfg
}

func (c *Config) loadFromEnv() {
	c.Host = getEnv("VOID2MD_HOST", "0.0.0.0")
	c.Port = getEnvInt("VOID2MD_PORT", 3457)
	c.LogLevel = getEnv("VOID2MD_LOG_LEVEL", "INFO")
	c.LogFormat = getEnv("VOID2MD_LOG_FORMAT", "plain")

	c.HTMLCrawlConfig = HTMLCrawlConfig{
		Concurrency:    getEnvInt("VOID2MD_HTML_CRAWL_CONCURRENCY", htmlDef.Concurrency),
		MaxPages:       getEnvInt("VOID2MD_HTML_MAX_PAGES", htmlDef.MaxPages),
		HardMaxPages:   getEnvInt("VOID2MD_HTML_HARD_MAX_PAGES", htmlDef.HardMaxPages),
		PageTimeout:    getEnvInt("VOID2MD_HTML_PAGE_TIMEOUT", htmlDef.PageTimeout),
		TaskTimeout:    getEnvInt("VOID2MD_HTML_TASK_TIMEOUT", htmlDef.TaskTimeout),
		MaxHTMLSizeMB:  getEnvInt("VOID2MD_HTML_MAX_HTML_SIZE_MB", htmlDef.MaxHTMLSizeMB),
		MaxTaskBytesMB: getEnvInt("VOID2MD_HTML_MAX_TASK_BYTES_MB", htmlDef.MaxTaskBytesMB),
		UserAgent:      getEnv("VOID2MD_HTML_USER_AGENT", htmlDef.UserAgent),
		TaskTTLSeconds: getEnvInt("VOID2MD_HTML_TASK_TTL_SECONDS", htmlDef.TaskTTLSeconds),
		MaxTasks:       getEnvInt("VOID2MD_HTML_MAX_TASKS", htmlDef.MaxTasks),
	}.Normalize()
}

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
