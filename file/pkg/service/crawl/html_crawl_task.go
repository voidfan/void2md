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

	"void2md/common/diagnosis"
	"void2md/file/pkg/config"
	"void2md/file/pkg/crawler"
)

// HTMLCrawlStatus is the crawl task status.
type HTMLCrawlStatus string

const (
	CrawlPending   HTMLCrawlStatus = "pending"
	CrawlRunning   HTMLCrawlStatus = "running"
	CrawlSuccess   HTMLCrawlStatus = "success"
	CrawlFailed    HTMLCrawlStatus = "failed"
	CrawlCancelled HTMLCrawlStatus = "cancelled"
)

// HTMLCrawlItem is a single page result.
type HTMLCrawlItem struct {
	URL      string `json:"url"`
	Title    string `json:"title"`
	Markdown string `json:"markdown"`
}

// HTMLCrawlTask is the task's external snapshot.
type HTMLCrawlTask struct {
	TaskID    string          `json:"taskId"`
	Status    HTMLCrawlStatus `json:"status"`
	Pages     int             `json:"pages"`
	Markdown  string          `json:"markdown"`
	Items     []HTMLCrawlItem `json:"items"`
	Truncated bool            `json:"truncated"`
	Error     string          `json:"error"`
}

// htmlCrawlEntry is the internal task entry, holding the cancel func and
// finish time (for TTL sweeping).
type htmlCrawlEntry struct {
	task   *HTMLCrawlTask
	urlKey string
	cancel context.CancelFunc
	doneAt time.Time // zero means not in a terminal state
	alive  bool
}

// htmlCrawlStore is the in-memory task store.
//
// Deliberately no background goroutine: TTL sweeping piggybacks on submit/get (lazy).
// Size is bounded by MaxTasks (alive) + terminal tasks within the TTL window — enough
// for a single-node in-memory design; the tradeoff is an idle service won't release
// terminal tasks proactively.
type htmlCrawlStore struct {
	mu    sync.Mutex
	cfg   config.HTMLCrawlConfig
	tasks map[string]*htmlCrawlEntry
	byURL map[string]string // normalized URL → alive task ID (dedupe)
}

// newHTMLCrawlStore builds the in-memory task store.
//
// cfg is Normalize()d here so MaxTasks / TaskTTLSeconds are always positive inside the
// store — submit's rate limit and sweepLocked need no `> 0` guards, avoiding the
// "guard reads 0 as protection-off" semantics (review F6). Normalizing here, not only in
// NewHTMLCrawlService, because directly-constructed zero configs (tests, scripts) must
// follow the same rule.
func newHTMLCrawlStore(cfg config.HTMLCrawlConfig) *htmlCrawlStore {
	return &htmlCrawlStore{
		cfg:   cfg.Normalize(),
		tasks: make(map[string]*htmlCrawlEntry),
		byURL: make(map[string]string),
	}
}

// isTooManyRequests reports whether err is "too many alive tasks" (429 + FF000D).
//
// Must check the diagnosis code, not just StatusCode: a constructor returning
// StatusCode=429 with DCode=FF0003 would silently change the response diagnosisCode,
// and 429-only assertions wouldn't notice — downstream routing by diagnosisCode would
// misread "too many requests" as "invalid param". Design §2.1/§5 added FF000D to make
// this semantic distinguishable.
func isTooManyRequests(err error) bool {
	var de *diagnosis.DiagnosisException
	return errors.As(err, &de) &&
		de.StatusCode == 429 &&
		de.DCode == diagnosis.ErrorTooManyRequestsDiagnosisCode
}

// submit creates a task. urlKey is the normalized URL (computed by the caller).
// Returns (taskID, whether an existing alive task was hit, error).
func (s *htmlCrawlStore) submit(urlKey string, opts crawler.Options) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sweepLocked()

	// dedupe: reuse an alive task for the same URL (design §2.1)
	if id, ok := s.byURL[urlKey]; ok {
		if e, exists := s.tasks[id]; exists && e.alive {
			return id, true, nil
		}
		delete(s.byURL, urlKey)
	}

	// rate limit: alive task cap (design §2.1 → 429 + FF000D)
	//
	// No `s.cfg.MaxTasks > 0 &&` guard: cfg is Normalized at construction, so the field
	// is always positive. The old form read 0/negative as "unlimited", disabling this
	// memory protection entirely (review F6).
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

// setCancel registers the task's cancel func (used by DELETE).
//
// Must self-heal, not just "assign if present". Submit does `go s.run(id, opts)` and
// setCancel is called inside run: after store.submit returns, the task is already visible
// to GET/DELETE, but the cancel func is only registered once that goroutine is scheduled.
// A DELETE arriving in this window goes through cancel() — it sets status cancelled and
// clears the payload, but e.cancel is still nil, so the ctx is never cancelled: the crawl
// goroutine keeps running until TaskTimeout (default 300s), fetching and discarding
// Markdown the whole time. Design §2.3's "pending/running → cancel (ctx cancel)" promise
// silently fails in this window while the client sees 204.
//
// The window widens under load (20 concurrent tasks, all Ps busy) — not a theoretical issue.
func (s *htmlCrawlStore) setCancel(id string, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.tasks[id]
	if !ok || !e.alive {
		// Cancel arrived first (task already cancelled / terminal / gone): cancel ctx now.
		// Repeated cancel is idempotent, no panic.
		cancel()
		return
	}
	e.cancel = cancel
}

// setRunning marks the task as running.
func (s *htmlCrawlStore) setRunning(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.tasks[id]; ok && e.alive {
		e.task.Status = CrawlRunning
	}
}

// setProgress updates the completed page count while running (design §2.2: pages is
// the only progress signal in non-terminal states).
func (s *htmlCrawlStore) setProgress(id string, done int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.tasks[id]; ok && e.alive {
		e.task.Pages = done
	}
}

// get returns a copy of the task snapshot.
func (s *htmlCrawlStore) get(id string) (HTMLCrawlTask, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	e, ok := s.tasks[id]
	if !ok {
		return HTMLCrawlTask{}, false
	}
	snap := *e.task
	// Contract: items is always a JSON array, never null (design §2.2 response example).
	// Appending onto a non-nil empty slice yields non-nil even with zero elements.
	snap.Items = append([]HTMLCrawlItem{}, e.task.Items...)
	return snap, true
}

// cancel cancels an alive task and clears its payload (design §2.3: cancel means the
// caller no longer wants it). Returns false if the task does not exist.
func (s *htmlCrawlStore) cancel(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.tasks[id]
	if !ok {
		return false
	}
	if !e.alive {
		return true // already terminal: idempotent, don't modify existing results
	}
	finishLocked(s, e, CrawlCancelled, "", false)
	// cancel semantics: discard fetched pages
	e.task.Pages = 0
	e.task.Markdown = ""
	e.task.Items = []HTMLCrawlItem{}
	if e.cancel != nil {
		e.cancel()
	}
	return true
}

// commentSafeURL makes a URL safe to embed in an `<!-- ... -->` comment.
//
// HTML comments end at the *first* `-->`, and URL queries are preserved verbatim by both
// crawler.NormalizeURL (lowercases host, strips fragment/default port) and url.URL.String().
// A same-origin link shaped like `?x=--><svg/onload=alert(1)>` would close the comment
// early, turning the following `<svg/onload=...>` into *live* Markdown/HTML that flows
// downstream (/v1/chunk, knowledge base), breaking design §2.2's "comments are invisible
// when Markdown renders" promise.
//
// Rewriting `--` as `-%2D` leaves no `--` in the string, so neither `-->` nor `--!>` can
// form; both `-` and `%2D` are legal in URLs and the address stays human-readable.
// Affects comment display only: items[].url keeps the original for downstream provenance.
func commentSafeURL(u string) string {
	return strings.ReplaceAll(u, "--", "-%2D")
}

// finishSuccess completes a task successfully. res.Pages has at least 1 page (guaranteed
// by crawler.Crawl).
func (s *htmlCrawlStore) finishSuccess(id string, res *crawler.Result) {
	// Aggregation happens *outside* the lock: the store is a global singleton with one
	// mutex, and this concatenation is O(total bytes). Measured locally (200MB payload =
	// 200 pages × 1MB, three runs): finishSuccess took 0.52/0.62/0.57s total with only
	// assignments left inside the lock — before the change, all of that was lock-held time,
	// stalling GET/POST/DELETE for *every* task (POST is on the request path, so unrelated
	// callers would slow down inexplicably). Numbers vary by machine; the order of
	// magnitude doesn't: a single request could observably stall for half a second.
	// res is owned exclusively by the crawl goroutine, so "compute outside, assign inside"
	// is safe.
	items := make([]HTMLCrawlItem, 0, len(res.Pages))
	var markdown strings.Builder
	for i, p := range res.Pages {
		items = append(items, HTMLCrawlItem{URL: p.URL, Title: p.Title, Markdown: p.Markdown})
		if i > 0 {
			markdown.WriteString("\n\n\n")
		}
		// Invisible in rendered Markdown, but lets downstream chunker chunks carry the
		// source URL. The URL must go through commentSafeURL, or a URL containing `-->`
		// would close the comment early (see that function).
		markdown.WriteString("<!-- source: " + commentSafeURL(p.URL) + " -->\n\n")
		markdown.WriteString(p.Markdown)
	}
	aggregated := markdown.String()

	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.tasks[id]
	if !ok || !e.alive {
		// Late completion (task cancelled / TTL expired): discard the just-built payload.
		return
	}
	e.task.Markdown = aggregated
	e.task.Items = items
	e.task.Pages = len(items)
	status := CrawlSuccess
	finishLocked(s, e, status, "", res.Truncated)
}

// finishFailed marks a task failed (0 pages fetched). err's text is exposed as the error field.
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

// finishCancelled ends a task due to ctx cancellation.
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

// finishLocked handles terminal state transitions (caller must hold the lock).
func finishLocked(s *htmlCrawlStore, e *htmlCrawlEntry, status HTMLCrawlStatus, errMsg string, truncated bool) {
	e.task.Status = status
	e.task.Error = errMsg
	e.task.Truncated = truncated
	e.alive = false
	e.doneAt = time.Now()
	delete(s.byURL, e.urlKey)
}

// sweepLocked removes terminal tasks older than the TTL (caller must hold the lock).
//
// No `if ttl <= 0 { return }`: TaskTTLSeconds is Normalized to non-zero at construction.
// The old form read 0 as "never sweep" — terminal tasks' full payloads (up to 200MB
// Markdown each) would stay in memory forever with no log hint (review F6).
func (s *htmlCrawlStore) sweepLocked() {
	ttl := time.Duration(s.cfg.TaskTTLSeconds) * time.Second
	now := time.Now()
	for id, e := range s.tasks {
		if !e.alive && !e.doneAt.IsZero() && now.Sub(e.doneAt) > ttl {
			delete(s.tasks, id)
		}
	}
}

// newTaskID generates a 16-hex-digit task ID with the hpa_ prefix.
func newTaskID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is extremely rare; fall back to a timestamp so task
		// creation doesn't fail outright
		return "hpa_" + strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return "hpa_" + hex.EncodeToString(b[:])
}
