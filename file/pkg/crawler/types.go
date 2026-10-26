// Package crawler 提供静态站点同域广度优先抓取，产出每页的 Markdown。
// 本包不依赖 gin 与全局配置单例，所有参数通过 Options 注入，便于测试。
package crawler

import "time"

// Options 单次爬取任务的参数。
type Options struct {
	URL          string        // 入口 URL
	Selector     string        // CSS 选择器，空串按 "body" 处理
	MaxPage      int           // 页数上限，<=0 视为 1
	Concurrency  int           // 任务内并发抓取数，<=0 视为 4
	PageTimeout  time.Duration // 单页超时
	TaskTimeout  time.Duration // 任务总预算，到点停止调度
	MaxHTMLSize  int64         // 单页 HTML 字节上限
	MaxTaskBytes int64         // 累计 Markdown 字节上限，<=0 表示不限
	UserAgent    string        // 请求 UA
}

// Page 一页的抓取与解析结果。
type Page struct {
	URL      string `json:"url"`
	Title    string `json:"title"`
	Markdown string `json:"markdown"`
}

// StopReason 提前停止的原因（设计 §2.2「提前停止」的三种成因）。
type StopReason string

const (
	StopNone     StopReason = ""             // 正常跑完 frontier 为空
	StopMaxPage  StopReason = "maxPage"      // 页数达到上限
	StopTimeout  StopReason = "timeout"      // 任务总预算到点
	StopMaxBytes StopReason = "maxTaskBytes" // 累计 Markdown 字节超限
)

// Result 一次爬取的完整结果。
type Result struct {
	Pages      []Page
	Truncated  bool       // 是否提前停止
	StopReason StopReason // 提前停止的原因；Truncated 为 false 时是 StopNone
}
