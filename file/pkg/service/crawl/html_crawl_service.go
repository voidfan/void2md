package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"void2md/common/diagnosis"
	"void2md/common/logger"
	"void2md/file/pkg/config"
	"void2md/file/pkg/crawler"
)

// log returns the module logger lazily so it picks up the handler
// configured by logger.InitLogger even though package init runs before main.
func log() *slog.Logger { return logger.GetLogger("crawl") }

// HTMLCrawlService is the public API for site knowledge crawling.
type HTMLCrawlService struct {
	store *htmlCrawlStore
	cfg   config.HTMLCrawlConfig
}

// NewHTMLCrawlService builds a service with the given config (used by the singleton and tests).
//
// cfg is Normalize()d here: zero values always become defaults — 0 never means "off"
// (see config.HTMLCrawlConfig.Normalize). Normalizing at this entry point, instead of at
// scattered read sites, keeps the service, task store, and crawler.Options seeing one
// normalized config; their fallback sources differ (crawler.Crawl and NewFetcher have
// their own), so handling them separately would eventually drift.
func NewHTMLCrawlService(cfg config.HTMLCrawlConfig) *HTMLCrawlService {
	cfg = cfg.Normalize()
	return &HTMLCrawlService{store: newHTMLCrawlStore(cfg), cfg: cfg}
}

var (
	htmlCrawlOnce sync.Once
	htmlCrawlSvc  *HTMLCrawlService
)

// GetHTMLCrawlService returns the global singleton.
func GetHTMLCrawlService() *HTMLCrawlService {
	htmlCrawlOnce.Do(func() {
		htmlCrawlSvc = NewHTMLCrawlService(config.LoadConfig().HTMLCrawlConfig)
	})
	return htmlCrawlSvc
}

// buildOptions folds service config into per-crawl options (MB → bytes conversion happens here).
//
// Factored out for testability: if the two `<< 20` were dropped, the configured 200/10 (MB)
// would be passed as 200/10 *bytes* — every fetch would fail immediately, and HTTP-level
// tests feeding small httptest pages might not expose it.
//
// Config is already Normalized at construction (zero → default), so all values read here are positive.
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

// resolveMaxPages resolves the page limit for a request: the param wins, nil takes the
// server default; over HardMaxPages is clamped with a warn (design §2.1, not an error);
// param <= 0 returns 400.
//
// No second hardcoded default here (e.g. `if pages <= 0 { pages = 200 }`): the default
// comes only from s.cfg.MaxPages, normalized to non-zero at construction (see
// NewHTMLCrawlService) — two independently evolving literals are how "config says 200,
// code uses something else" drift starts.
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
		log().Warn("html crawl maxPage clamped to hard limit",
			"requested", pages, "hardMax", s.cfg.HardMaxPages)
		pages = s.cfg.HardMaxPages
	}
	return pages, nil
}

// Submit validates the URL and creates a crawl task.
// maxPage nil means server default; non-nil <=0 returns 400; over HardMaxPages is
// clamped with a warn (design §2.1, not an error).
// Returns (taskID, whether an existing live task was hit, error).
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

// Get returns a task snapshot.
func (s *HTMLCrawlService) Get(taskID string) (HTMLCrawlTask, bool) {
	return s.store.get(taskID)
}

// Cancel cancels a task; false means the task does not exist.
func (s *HTMLCrawlService) Cancel(taskID string) bool {
	return s.store.cancel(taskID)
}

// run executes one crawl and writes the result back to the task store.
func (s *HTMLCrawlService) run(taskID string, opts crawler.Options) {
	// task-level ctx is independent of the request ctx (submit already returned); Cancel triggers it
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.store.setCancel(taskID, cancel)
	s.store.setRunning(taskID)

	res, err := crawler.Crawl(ctx, opts, func(done int) {
		s.store.setProgress(taskID, done)
	})

	if err != nil {
		// Cancellation takes precedence over failure. NOTE: this branch is unreachable
		// today — the only cancel source is store.cancel(), which clears alive first, so
		// both finishCancelled and finishFailed return at the `!e.alive` guard; deleting
		// this block changes nothing observable (verified by mutation: tests stay green).
		//
		// Kept because this is where the semantic defense belongs: if cancel() ever changes
		// to "cancel ctx only, don't write a terminal state", this becomes the only guard
		// against marking a cancelled task as failed. At that point it must get a test that
		// drives it directly — the ctx is created inside run via
		// context.WithCancel(context.Background()) and cannot be injected from outside,
		// so it is untestable today.
		if ctx.Err() == context.Canceled {
			s.store.finishCancelled(taskID)
			return
		}
		log().Warn("html crawl task failed", "taskId", taskID, "url", opts.URL, "error", err)
		s.store.finishFailed(taskID, err)
		return
	}

	s.store.finishSuccess(taskID, res)
	log().Info("html crawl task completed", "taskId", taskID, "url", opts.URL,
		"pages", len(res.Pages), "truncated", res.Truncated, "stopReason", string(res.StopReason))
}
