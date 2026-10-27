// Package crawler provides same-domain BFS crawling of static sites,
// producing per-page Markdown. It has no gin or global-config dependencies;
// all parameters come in via Options, for testability.
package crawler

import "time"

// Options are the parameters of a single crawl task.
type Options struct {
	URL          string        // entry URL
	Selector     string        // CSS selector; empty means "body"
	MaxPage      int           // page limit; <=0 means 1
	Concurrency  int           // concurrent fetches per task; <=0 means 4
	PageTimeout  time.Duration // per-page timeout
	TaskTimeout  time.Duration // total task budget; scheduling stops when reached
	MaxHTMLSize  int64         // per-page HTML byte limit
	MaxTaskBytes int64         // cumulative Markdown byte limit; <=0 means unlimited
	UserAgent    string        // request User-Agent
}

// Page is the fetch+parse result for one page.
type Page struct {
	URL      string `json:"url"`
	Title    string `json:"title"`
	Markdown string `json:"markdown"`
}

// StopReason is why the crawl stopped early (design §2.2, three causes).
type StopReason string

const (
	StopNone     StopReason = ""             // frontier exhausted normally
	StopMaxPage  StopReason = "maxPage"      // page limit reached
	StopTimeout  StopReason = "timeout"      // task budget reached
	StopMaxBytes StopReason = "maxTaskBytes" // cumulative Markdown byte limit exceeded
)

// Result is the full outcome of one crawl.
type Result struct {
	Pages      []Page
	Truncated  bool       // stopped early
	StopReason StopReason // why it stopped early; StopNone when Truncated is false
}
