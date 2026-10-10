package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"void2md/internal/config"
	"void2md/internal/crawler"
	"void2md/internal/pkg/diagnosis"
)

// HTMLCrawlStatus 抓取任务状态。
type HTMLCrawlStatus string

const (
	CrawlPending   HTMLCrawlStatus = "pending"
	CrawlRunning   HTMLCrawlStatus = "running"
	CrawlSuccess   HTMLCrawlStatus = "success"
	CrawlFailed    HTMLCrawlStatus = "failed"
	CrawlCancelled HTMLCrawlStatus = "cancelled"
)

// HTMLCrawlItem 单页结果。
type HTMLCrawlItem struct {
	URL      string `json:"url"`
	Title    string `json:"title"`
	Markdown string `json:"markdown"`
}

// HTMLCrawlTask 任务对外快照。
type HTMLCrawlTask struct {
	TaskID    string          `json:"taskId"`
	Status    HTMLCrawlStatus `json:"status"`
	Pages     int             `json:"pages"`
	Markdown  string          `json:"markdown"`
	Items     []HTMLCrawlItem `json:"items"`
	Truncated bool            `json:"truncated"`
	Error     string          `json:"error"`
}

// htmlCrawlEntry 任务内部条目，含取消函数与终态时间（用于 TTL 清扫）。
type htmlCrawlEntry struct {
	task   *HTMLCrawlTask
	urlKey string
	cancel context.CancelFunc
	doneAt time.Time // 零值表示未进入终态
	alive  bool
}

// htmlCrawlStore 内存任务表。
//
// 有意不引入后台 goroutine：TTL 清扫在 submit/get 时顺带执行（惰性清扫）。
// 任务表规模上界为 MaxTasks（存活）+ TTL 窗口内已终态的任务，
// 对单机内存态方案足够；代价是长期空闲的服务不会主动释放已终态任务。
type htmlCrawlStore struct {
	mu    sync.Mutex
	cfg   config.HTMLCrawlConfig
	tasks map[string]*htmlCrawlEntry
	byURL map[string]string // 规范化 URL → 存活任务 ID（去重）
}

// newHTMLCrawlStore 构造内存任务表。
//
// cfg 在这里做一次 `Normalize()`，使得 MaxTasks / TaskTTLSeconds **在任务表内部
// 恒为正数**——于是 submit 的限流与 sweepLocked 的清扫都不必再写 `> 0` 守卫，
// 也就不会出现「守卫把 0 解释成关掉保护」那种语义（收尾复审 F6）。
// 归一化放在这里而不只放在 NewHTMLCrawlService，是因为直接构造零值配置
// （测试、脚本）同样必须落进同一条规则。
func newHTMLCrawlStore(cfg config.HTMLCrawlConfig) *htmlCrawlStore {
	return &htmlCrawlStore{
		cfg:   cfg.Normalize(),
		tasks: make(map[string]*htmlCrawlEntry),
		byURL: make(map[string]string),
	}
}

// isTooManyRequests 判定错误是否为「存活任务数超限」（429 + FF000D）。
//
// **必须同时校验诊断码，不能只看 StatusCode**：把 submit 里换成一个
// StatusCode=429 但 DCode=FF0003 的构造函数，接口响应里的 diagnosisCode 就从
// FF000D 静默变成 FF0003，而只校验 429 的断言察觉不到——下游按 diagnosisCode
// 分流时会把「请求过多」误判为「参数错误」。设计 §2.1/§5 新增 FF000D 正是为了
// 让这个语义可被区分。
func isTooManyRequests(err error) bool {
	var de *diagnosis.DiagnosisException
	return errors.As(err, &de) &&
		de.StatusCode == 429 &&
		de.DCode == diagnosis.ErrorTooManyRequestsDiagnosisCode
}

// submit 创建任务。urlKey 为规范化后的 URL（由调用方计算）。
// 返回 (taskID, 是否命中已有存活任务, error)。
func (s *htmlCrawlStore) submit(urlKey string, opts crawler.Options) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sweepLocked()

	// 去重：同 URL 存活任务直接复用（设计 §2.1）
	if id, ok := s.byURL[urlKey]; ok {
		if e, exists := s.tasks[id]; exists && e.alive {
			return id, true, nil
		}
		delete(s.byURL, urlKey)
	}

	// 限流：存活任务数上限（设计 §2.1 → 429 + FF000D）
	//
	// 不再写 `s.cfg.MaxTasks > 0 &&`：cfg 已在构造时 Normalize，该字段恒为正数。
	// 旧写法把 0/负数解释成「不限任务数」，等于把这道内存保护整个关掉（复审 F6）。
	alive := 0
	for _, e := range s.tasks {
		if e.alive {
			alive++
		}
	}
	if alive >= s.cfg.MaxTasks {
		return "", false, diagnosis.NewTooManyRequestsException(
			fmt.Sprintf("too many alive htmlparse tasks (max %d)", s.cfg.MaxTasks))
	}

	id := newTaskID()
	s.tasks[id] = &htmlCrawlEntry{
		task:   &HTMLCrawlTask{TaskID: id, Status: CrawlPending, Items: []HTMLCrawlItem{}},
		urlKey: urlKey,
		alive:  true,
	}
	s.byURL[urlKey] = id
	return id, false, nil
}

// setCancel 记录任务的取消函数（供 DELETE 使用）。
//
// **这里必须自愈，不能只做「有则赋值」。** Submit 里是 `go s.run(id, opts)`，
// 而 setCancel 是在 run 内部才调用的：store.submit 返回后任务已对 GET/DELETE
// 可见，但取消函数要等那个 goroutine 被调度到才登记。在这段窗口内到达的 DELETE
// 会走 cancel()——它把状态置为 cancelled 并清空载荷，但彼时 e.cancel 仍是 nil，
// 于是 **ctx 从不被取消**，抓取 goroutine 继续跑到 TaskTimeout（默认 300s），
// 期间持续出网、持续往内存里堆 Markdown 再丢弃。设计 §2.3 承诺的「pending/running
// → 取消（ctx cancel）」在这个窗口落空，而客户端拿到的是 204，看不出任何异常。
//
// 负载越高（20 个任务并存、P 全忙）这个窗口越宽，所以不是理论问题。
func (s *htmlCrawlStore) setCancel(id string, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.tasks[id]
	if !ok || !e.alive {
		// Cancel 先到（任务已 cancelled / 已终态 / 已不存在）：立刻取消 ctx。
		// 重复 cancel 是幂等的，不会 panic。
		cancel()
		return
	}
	e.cancel = cancel
}

// setRunning 标记任务开始执行。
func (s *htmlCrawlStore) setRunning(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.tasks[id]; ok && e.alive {
		e.task.Status = CrawlRunning
	}
}

// setProgress 更新运行中的已完成页数（设计 §2.2：非终态时 pages 是唯一进度信号）。
func (s *htmlCrawlStore) setProgress(id string, done int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.tasks[id]; ok && e.alive {
		e.task.Pages = done
	}
}

// get 返回任务快照的副本。
func (s *htmlCrawlStore) get(id string) (HTMLCrawlTask, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	e, ok := s.tasks[id]
	if !ok {
		return HTMLCrawlTask{}, false
	}
	snap := *e.task
	// 契约要求 JSON 中 items 始终是数组而非 null（设计 §2.2 响应示例）。
	// append 到非 nil 空切片上，即使零元素也返回非 nil。
	snap.Items = append([]HTMLCrawlItem{}, e.task.Items...)
	return snap, true
}

// cancel 取消存活任务并清空载荷（设计 §2.3：取消意味着调用方不要了）。
// 返回 false 表示任务不存在。
func (s *htmlCrawlStore) cancel(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.tasks[id]
	if !ok {
		return false
	}
	if !e.alive {
		return true // 已终态：幂等，不修改已有结果
	}
	finishLocked(s, e, CrawlCancelled, "", false)
	// 取消语义：丢弃已抓页面
	e.task.Pages = 0
	e.task.Markdown = ""
	e.task.Items = []HTMLCrawlItem{}
	if e.cancel != nil {
		e.cancel()
	}
	return true
}

// commentSafeURL 让 URL 能安全地放进 `<!-- ... -->` 注释里。
//
// HTML 注释在**第一处 `-->`** 结束，而 URL 的 query 在归一化（`crawler.NormalizeURL`
// 只小写 host、去 fragment/默认端口）与 `url.URL.String()` 里都是原样保留的：一条
// `?x=--><svg/onload=alert(1)>` 形状的同域链接会让注释提前闭合，其后的
// `<svg/onload=...>` 变成**活的** Markdown/HTML，随聚合 markdown 流向下游
// （/v1/chunk、知识库），设计 §2.2「注释在 Markdown 渲染时不可见」的承诺也随之失效。
//
// 把 `--` 写成 `-%2D` 后串里不再出现 `--`，`-->` 与 `--!>` 都无法形成，且 `-` 与
// `%2D` 在 URL 里都合法，人仍能读出这是什么地址。只影响注释里的展示：
// `items[].url` 保持原文，下游要溯源就用它。
func commentSafeURL(u string) string {
	return strings.ReplaceAll(u, "--", "-%2D")
}

// finishSuccess 任务成功结束。res.Pages 至少 1 页（由 crawler.Crawl 保证）。
func (s *htmlCrawlStore) finishSuccess(id string, res *crawler.Result) {
	// 聚合在**锁外**完成：store 是全局单例、只有一把锁，而这里的拼接是 O(总字节)。
	// 实测（本机，200MB 载荷 = 200 页 × 1MB，三次）：整个 finishSuccess 0.52 / 0.62 / 0.57s，
	// 其中锁内只剩赋值——也就是说改成锁外之前，这段时间**全部**是持锁时间，会把
	// **所有任务**的 GET/POST/DELETE 一起卡住（POST 在请求路径上，无关调用方会莫名变慢）。
	// 数值随机器与负载浮动，量级是一定的：单次请求可观测地卡住半秒。
	// res 由抓取 goroutine 独占、不与任何其他写者共享，故「锁外算好、进锁赋值」是安全的。
	items := make([]HTMLCrawlItem, 0, len(res.Pages))
	var markdown strings.Builder
	for i, p := range res.Pages {
		items = append(items, HTMLCrawlItem{URL: p.URL, Title: p.Title, Markdown: p.Markdown})
		if i > 0 {
			markdown.WriteString("\n\n\n")
		}
		// 注释在 Markdown 渲染时不可见，但让下游分块器切出的 chunk 携带来源 URL。
		// URL 必须经 commentSafeURL 转义，否则自带 `-->` 的 URL 会提前闭合注释（见该函数）。
		markdown.WriteString("<!-- source: " + commentSafeURL(p.URL) + " -->\n\n")
		markdown.WriteString(p.Markdown)
	}
	aggregated := markdown.String()

	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.tasks[id]
	if !ok || !e.alive {
		// 迟到的完成（任务已取消/已过 TTL）：刚算好的载荷直接丢弃，与改动前一致。
		return
	}
	e.task.Markdown = aggregated
	e.task.Items = items
	e.task.Pages = len(items)
	status := CrawlSuccess
	finishLocked(s, e, status, "", res.Truncated)
}

// finishFailed 任务失败（抓取到 0 页）。err 的文本会作为 error 字段对外暴露。
func (s *htmlCrawlStore) finishFailed(id string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.tasks[id]
	if !ok || !e.alive {
		return
	}
	msg := "unknown error"
	if err != nil {
		msg = err.Error()
	}
	e.task.Pages = 0
	e.task.Markdown = ""
	e.task.Items = []HTMLCrawlItem{}
	finishLocked(s, e, CrawlFailed, msg, false)
}

// finishCancelled 任务因 ctx 取消而结束。
func (s *htmlCrawlStore) finishCancelled(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.tasks[id]
	if !ok || !e.alive {
		return
	}
	e.task.Pages = 0
	e.task.Markdown = ""
	e.task.Items = []HTMLCrawlItem{}
	finishLocked(s, e, CrawlCancelled, "", false)
}

// finishLocked 统一处理终态转移（调用方必须已持锁）。
func finishLocked(s *htmlCrawlStore, e *htmlCrawlEntry, status HTMLCrawlStatus, errMsg string, truncated bool) {
	e.task.Status = status
	e.task.Error = errMsg
	e.task.Truncated = truncated
	e.alive = false
	e.doneAt = time.Now()
	delete(s.byURL, e.urlKey)
}

// sweepLocked 清理超过 TTL 的终态任务（调用方必须已持锁）。
//
// 不写 `if ttl <= 0 { return }`：TaskTTLSeconds 已在构造时 Normalize 成非零，
// 而旧写法把 0 解释成「永不清扫」——已终态任务的整个载荷（每任务最多 200MB
// Markdown）会一直留在内存里，且没有任何日志提示（复审 F6）。
func (s *htmlCrawlStore) sweepLocked() {
	ttl := time.Duration(s.cfg.TaskTTLSeconds) * time.Second
	now := time.Now()
	for id, e := range s.tasks {
		if !e.alive && !e.doneAt.IsZero() && now.Sub(e.doneAt) > ttl {
			delete(s.tasks, id)
		}
	}
}

// newTaskID 生成 16 位十六进制任务 ID，前缀 hpa_。
func newTaskID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 失败极罕见；退化为时间戳，避免任务创建直接失败
		return "hpa_" + strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return "hpa_" + hex.EncodeToString(b[:])
}
