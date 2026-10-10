package config

// HTMLCrawlConfig 站点知识获取（/v1/htmlparse）配置
type HTMLCrawlConfig struct {
	Concurrency    int    `json:"concurrency"`    // 单任务内并发抓取数
	MaxPages       int    `json:"maxPages"`       // 默认页数上限
	HardMaxPages   int    `json:"hardMaxPages"`   // 入参 maxPage 的硬上限（clamp 到此值）
	PageTimeout    int    `json:"pageTimeout"`    // 单页超时（秒）
	TaskTimeout    int    `json:"taskTimeout"`    // 任务总预算（秒）
	MaxHTMLSizeMB  int    `json:"maxHtmlSizeMB"`  // 单页 HTML 上限
	MaxTaskBytesMB int    `json:"maxTaskBytesMB"` // 单任务累计 Markdown 上限（内存保护）
	UserAgent      string `json:"userAgent"`      // 请求 UA
	TaskTTLSeconds int    `json:"taskTtlSeconds"` // 任务结果保留时长
	MaxTasks       int    `json:"maxTasks"`       // 同时存活任务数上限
}

func DefaultHTMLCrawlConfig() HTMLCrawlConfig {
	return HTMLCrawlConfig{
		Concurrency:    4,
		MaxPages:       200,
		HardMaxPages:   2000,
		PageTimeout:    30,
		TaskTimeout:    300,
		MaxHTMLSizeMB:  10,
		MaxTaskBytesMB: 200,
		UserAgent:      "void2md/1.0",
		TaskTTLSeconds: 1800,
		MaxTasks:       20,
	}
}

// Normalize 把「未设置」的字段补成默认值：**数值字段 <= 0、UserAgent 为空，
func (c HTMLCrawlConfig) Normalize() HTMLCrawlConfig {
	def := DefaultHTMLCrawlConfig()
	if c.Concurrency <= 0 {
		c.Concurrency = def.Concurrency
	}
	if c.MaxPages <= 0 {
		c.MaxPages = def.MaxPages
	}
	if c.HardMaxPages <= 0 {
		c.HardMaxPages = def.HardMaxPages
	}
	if c.PageTimeout <= 0 {
		c.PageTimeout = def.PageTimeout
	}
	if c.TaskTimeout <= 0 {
		c.TaskTimeout = def.TaskTimeout
	}
	if c.MaxHTMLSizeMB <= 0 {
		c.MaxHTMLSizeMB = def.MaxHTMLSizeMB
	}
	if c.MaxTaskBytesMB <= 0 {
		c.MaxTaskBytesMB = def.MaxTaskBytesMB
	}
	if c.TaskTTLSeconds <= 0 {
		c.TaskTTLSeconds = def.TaskTTLSeconds
	}
	if c.MaxTasks <= 0 {
		c.MaxTasks = def.MaxTasks
	}
	if c.UserAgent == "" {
		c.UserAgent = def.UserAgent
	}
	return c
}

var htmlDef = DefaultHTMLCrawlConfig()
