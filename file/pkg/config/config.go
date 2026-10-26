// Package config 提供 file 模块的模块级配置（HTML 站点抓取、Markdown 分块）。
// 项目级配置（Host/Port/日志）见 common/config。
package config

import (
	"sync"

	commoncfg "void2md/common/config"
)

// Config file 模块配置
type Config struct {
	HTMLCrawlConfig HTMLCrawlConfig `json:"htmlCrawlConfig"`
}

var (
	cfg  *Config
	once sync.Once
)

// LoadConfig 加载 file 模块配置，使用 sync.Once 保证单例模式
func LoadConfig() *Config {
	once.Do(func() {
		cfg = &Config{}
		cfg.loadFromEnv()
	})
	return cfg
}

func (c *Config) loadFromEnv() {
	c.HTMLCrawlConfig = HTMLCrawlConfig{
		Concurrency:    commoncfg.GetEnvInt("VOID2MD_HTML_CRAWL_CONCURRENCY", htmlDef.Concurrency),
		MaxPages:       commoncfg.GetEnvInt("VOID2MD_HTML_MAX_PAGES", htmlDef.MaxPages),
		HardMaxPages:   commoncfg.GetEnvInt("VOID2MD_HTML_HARD_MAX_PAGES", htmlDef.HardMaxPages),
		PageTimeout:    commoncfg.GetEnvInt("VOID2MD_HTML_PAGE_TIMEOUT", htmlDef.PageTimeout),
		TaskTimeout:    commoncfg.GetEnvInt("VOID2MD_HTML_TASK_TIMEOUT", htmlDef.TaskTimeout),
		MaxHTMLSizeMB:  commoncfg.GetEnvInt("VOID2MD_HTML_MAX_HTML_SIZE_MB", htmlDef.MaxHTMLSizeMB),
		MaxTaskBytesMB: commoncfg.GetEnvInt("VOID2MD_HTML_MAX_TASK_BYTES_MB", htmlDef.MaxTaskBytesMB),
		UserAgent:      commoncfg.GetEnv("VOID2MD_HTML_USER_AGENT", htmlDef.UserAgent),
		TaskTTLSeconds: commoncfg.GetEnvInt("VOID2MD_HTML_TASK_TTL_SECONDS", htmlDef.TaskTTLSeconds),
		MaxTasks:       commoncfg.GetEnvInt("VOID2MD_HTML_MAX_TASKS", htmlDef.MaxTasks),
	}.Normalize()
}
