package config

// HTMLCrawlConfig is the site-knowledge crawl (/v1/htmlparse) configuration.
type HTMLCrawlConfig struct {
	Concurrency    int    `json:"concurrency"`    // concurrent page fetches within one task
	MaxPages       int    `json:"maxPages"`       // default page limit
	HardMaxPages   int    `json:"hardMaxPages"`   // hard cap for the maxPage request param (clamped to this)
	PageTimeout    int    `json:"pageTimeout"`    // per-page timeout (seconds)
	TaskTimeout    int    `json:"taskTimeout"`    // total task budget (seconds)
	MaxHTMLSizeMB  int    `json:"maxHtmlSizeMB"`  // per-page HTML size limit
	MaxTaskBytesMB int    `json:"maxTaskBytesMB"` // cumulative per-task Markdown limit (memory guard)
	UserAgent      string `json:"userAgent"`      // request UA
	TaskTTLSeconds int    `json:"taskTtlSeconds"` // task result retention duration
	MaxTasks       int    `json:"maxTasks"`       // max concurrently live tasks
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

// Normalize fills unset fields with defaults (numeric fields <= 0, empty UserAgent).
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
