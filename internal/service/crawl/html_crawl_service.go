package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"void2md/internal/config"
	"void2md/internal/crawler"
	"void2md/internal/pkg/diagnosis"
)

// HTMLCrawlService 站点知识获取对外 API。
type HTMLCrawlService struct {
	store *htmlCrawlStore
	cfg   config.HTMLCrawlConfig
}

// NewHTMLCrawlService 按给定配置构造服务（供单例与测试使用）。
//
// 这里对 cfg 做一次 `Normalize()`：**零值一律取默认值，0 不表示「关掉」**
// （见 config.HTMLCrawlConfig.Normalize 的说明）。归一化放在这个入口、而不是散在各个
// 读取点，是为了让服务、任务表、`crawler.Options` 三处看到同一份已归一化的配置——
// 它们各自的兜底来源不同（crawler.Crawl 与 NewFetcher 内部还各有一套），
// 分开处理迟早漂移成「配置里写的默认」与「实际生效的默认」不一致。
func NewHTMLCrawlService(cfg config.HTMLCrawlConfig) *HTMLCrawlService {
	cfg = cfg.Normalize()
	return &HTMLCrawlService{store: newHTMLCrawlStore(cfg), cfg: cfg}
}

var (
	htmlCrawlOnce sync.Once
	htmlCrawlSvc  *HTMLCrawlService
)

// GetHTMLCrawlService 返回全局单例。
func GetHTMLCrawlService() *HTMLCrawlService {
	htmlCrawlOnce.Do(func() {
		htmlCrawlSvc = NewHTMLCrawlService(config.LoadConfig().HTMLCrawlConfig)
	})
	return htmlCrawlSvc
}

// buildOptions 把服务端配置折成单次抓取参数（MB → 字节的换算在这里完成）。
//
// 单独抽出来是为了可测：两个 `<< 20` 若被删掉，配置里写的 200/10（单位 MB）就会
// 以 **200/10 字节**传下去——单页 HTML 上限 10 字节、单任务字节预算 200 字节，
// 抓取会立刻全线失败，而 HTTP 层用例喂的都是 httptest 的小页面，未必暴露得出来。
//
// 配置在构造时已 Normalize（零值 → 默认值），故这里读到的都是正数。
func (s *HTMLCrawlService) buildOptions(u *url.URL, selector string, pages int) crawler.Options {
	return crawler.Options{
		URL:          u.String(),
		Selector:     selector,
		MaxPage:      pages,
		Concurrency:  s.cfg.Concurrency,
		PageTimeout:  time.Duration(s.cfg.PageTimeout) * time.Second,
		TaskTimeout:  time.Duration(s.cfg.TaskTimeout) * time.Second,
		MaxHTMLSize:  int64(s.cfg.MaxHTMLSizeMB) << 20,
		MaxTaskBytes: int64(s.cfg.MaxTaskBytesMB) << 20,
		UserAgent:    s.cfg.UserAgent,
	}
}

// resolveMaxPages 解析本次请求的页数上限：入参优先，`nil` 取服务端默认；
// 超过 HardMaxPages 按 clamp 处理并 warn（设计 §2.1，不报错）；入参 <=0 报 400。
//
// **这里不再有 `if pages <= 0 { pages = 200 }` 那样的第二份硬编码默认值。**
// 默认值只来自 `s.cfg.MaxPages`，而该字段在构造时已被 Normalize 成非零
// （见 NewHTMLCrawlService）——两份各自演化的字面量正是「配置写 200、实际用别的」
// 这类漂移的来源。
func (s *HTMLCrawlService) resolveMaxPages(maxPage *int) (int, error) {
	pages := s.cfg.MaxPages
	if maxPage != nil {
		if *maxPage <= 0 {
			return 0, diagnosis.NewClientException(
				diagnosis.ClientInvalidArgument,
				diagnosis.ErrorParamCheckDiagnosisCode,
				fmt.Sprintf("maxPage must be positive, got %d", *maxPage))
		}
		pages = *maxPage
	}
	if pages > s.cfg.HardMaxPages {
		slog.Warn("html crawl maxPage clamped to hard limit",
			"requested", pages, "hardMax", s.cfg.HardMaxPages)
		pages = s.cfg.HardMaxPages
	}
	return pages, nil
}

// Submit 校验 URL 并创建抓取任务。
// maxPage 为 nil 表示使用服务端默认值；非 nil 且 <=0 返回 400；
// 超过 HardMaxPages 按 clamp 处理并 warn（设计 §2.1，不报错）。
// 返回 (taskID, 是否命中已有存活任务, error)。
func (s *HTMLCrawlService) Submit(rawURL, selector string, maxPage *int) (string, bool, error) {
	u, err := crawler.NormalizeURL(rawURL)
	if err != nil {
		return "", false, diagnosis.NewClientException(
			diagnosis.ClientInvalidArgument,
			diagnosis.ErrorParamCheckDiagnosisCode,
			fmt.Sprintf("invalid url: %s", err.Error()))
	}

	pages, err := s.resolveMaxPages(maxPage)
	if err != nil {
		return "", false, err
	}

	opts := s.buildOptions(u, selector, pages)

	id, deduped, err := s.store.submit(u.String(), opts)
	if err != nil {
		return "", false, err
	}
	if deduped {
		return id, true, nil
	}

	go s.run(id, opts)
	return id, false, nil
}

// Get 返回任务快照。
func (s *HTMLCrawlService) Get(taskID string) (HTMLCrawlTask, bool) {
	return s.store.get(taskID)
}

// Cancel 取消任务。返回 false 表示任务不存在。
func (s *HTMLCrawlService) Cancel(taskID string) bool {
	return s.store.cancel(taskID)
}

// run 执行一次抓取并把结果写回任务表。
func (s *HTMLCrawlService) run(taskID string, opts crawler.Options) {
	// 任务级 ctx 独立于请求 ctx（提交请求已经返回），由 Cancel 触发取消
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.store.setCancel(taskID, cancel)
	s.store.setRunning(taskID)

	res, err := crawler.Crawl(ctx, opts, func(done int) {
		s.store.setProgress(taskID, done)
	})

	if err != nil {
		// 取消优先于失败。**注意：这条分支今天不可达**——cancel 的唯一来源是
		// store.cancel()，它已先把 alive 置 false，于是 finishCancelled 与
		// finishFailed 都会在 `!e.alive` 守卫处直接返回；把整段删掉不改变任何
		// 可观测结果（只读审计用实跑变异确认：删掉后测试全绿）。
		//
		// 保留它是因为语义上的防线位置确实在这里：一旦将来把 cancel() 改成
		// 「只 cancel ctx、不立即落终态」，这段就成了「已取消的任务不得被写成
		// failed」的唯一防线。**那时它必须配一条直接驱动它的用例**——当前 ctx
		// 由 run 内部用 context.WithCancel(context.Background()) 创建、无法从外部
		// 注入，所以今天测不到它。
		if ctx.Err() == context.Canceled {
			s.store.finishCancelled(taskID)
			return
		}
		slog.Warn("html crawl task failed", "taskId", taskID, "url", opts.URL, "error", err)
		s.store.finishFailed(taskID, err)
		return
	}

	s.store.finishSuccess(taskID, res)
	slog.Info("html crawl task completed", "taskId", taskID, "url", opts.URL,
		"pages", len(res.Pages), "truncated", res.Truncated, "stopReason", string(res.StopReason))
}
