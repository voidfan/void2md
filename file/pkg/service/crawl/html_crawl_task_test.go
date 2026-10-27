package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"void2md/file/pkg/config"
	"void2md/file/pkg/crawler"
)

func testCrawlCfg() config.HTMLCrawlConfig {
	return config.HTMLCrawlConfig{
		Concurrency: 4, MaxPages: 200, HardMaxPages: 2000,
		PageTimeout: 30, TaskTimeout: 300,
		MaxHTMLSizeMB: 10, MaxTaskBytesMB: 200,
		UserAgent: "test", TaskTTLSeconds: 1800, MaxTasks: 20,
	}
}

func newTestStore() *htmlCrawlStore {
	return newHTMLCrawlStore(testCrawlCfg())
}

func TestStore_SubmitAndGet(t *testing.T) {
	s := newTestStore()
	id, deduped, err := s.submit("https://a.com/", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	if deduped {
		t.Error("first submit must not be deduped")
	}
	task, ok := s.get(id)
	if !ok {
		t.Fatal("task not found after submit")
	}
	if task.Status != CrawlPending {
		t.Errorf("Status = %q, want pending", task.Status)
	}
	if task.Pages != 0 || task.Markdown != "" || len(task.Items) != 0 {
		t.Errorf("initial payload must be empty: %+v", task)
	}
	if task.Truncated || task.Error != "" {
		t.Errorf("initial flags must be zero: %+v", task)
	}
}

// TestStore_Dedup: design §2.1 — an alive task for the same URL returns the existing taskId.
func TestStore_Dedup(t *testing.T) {
	s := newTestStore()
	id1, _, _ := s.submit("https://a.com/", crawler.Options{})
	id2, deduped, err := s.submit("https://a.com/", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	if !deduped {
		t.Error("second submit of same url must be deduped")
	}
	if id1 != id2 {
		t.Errorf("dedup returned %q, want %q", id2, id1)
	}

	// after a terminal state there is no dedupe; resubmission is allowed
	s.finishSuccess(id1, &crawler.Result{Pages: []crawler.Page{{URL: "https://a.com/", Markdown: "x"}}})
	id3, deduped, _ := s.submit("https://a.com/", crawler.Options{})
	if deduped {
		t.Error("after terminal state, submit must not be deduped")
	}
	if id3 == id1 {
		t.Error("new task must get a new id")
	}
}

// TestStore_MaxTasks covers the review concern: alive-task overflow → the 429 error.
func TestStore_MaxTasks(t *testing.T) {
	cfg := testCrawlCfg()
	cfg.MaxTasks = 1
	s := newHTMLCrawlStore(cfg)

	id1, _, err := s.submit("https://a.com/1", crawler.Options{})
	if err != nil {
		t.Fatalf("first submit error: %v", err)
	}
	_, _, err = s.submit("https://a.com/2", crawler.Options{})
	if err == nil {
		t.Fatal("expected error when alive tasks exceed MaxTasks")
	}
	if !isTooManyRequests(err) {
		t.Errorf("error = %v, want a 429 DiagnosisException", err)
	}

	// The slot must be released once the occupant turns terminal.
	//
	// Must actually assert the release, not just call finishFailed once. The original form
	// was `s.finishFailed("hpa_", nil)` — a nonexistent taskId, and finishFailed starts with
	// `if !ok || !e.alive { return }`, so it did nothing and nothing was asserted. That left
	// this semantic uncovered: counting all entries as alive (`for range s.tasks { alive++ }`,
	// ignoring e.alive) would keep rejecting with 429 even after all tasks turned terminal,
	// until TTL(1800s) swept the entries — "idle service 429s for half an hour" in production,
	// yet all tests green.
	s.finishFailed(id1, nil)
	if _, _, err := s.submit("https://a.com/3", crawler.Options{}); err != nil {
		t.Errorf("after the occupant reaches a terminal state the slot must be released, got %v", err)
	}
}

// TestStore_CancelBeforeSetCancel pins setCancel's self-heal branch (design §2.3).
//
// Real timing: Submit does `go s.run(id, opts)` and setCancel is only called inside run.
// After store.submit returns, the task is visible to GET/DELETE but the cancel func isn't
// registered yet. A DELETE in this window goes through cancel() — status becomes cancelled
// and the payload is cleared, but e.cancel is still nil, so the ctx is never cancelled: the
// crawl goroutine keeps running to TaskTimeout (default 300s), fetching and discarding
// Markdown, while the client sees 204. The window widens under load (MaxTasks concurrent).
//
// This test reproduces that timing manually: cancel first, then a late setCancel, asserting
// the ctx is cancelled immediately. If setCancel regressed to "assign if present", this fails.
func TestStore_CancelBeforeSetCancel(t *testing.T) {
	s := newTestStore()
	id, _, err := s.submit("https://a.com/", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	if !s.cancel(id) {
		t.Fatal("cancel on a pending task must report success")
	}

	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()
	s.setCancel(id, cancelFn) // late registration: task is already terminal

	select {
	case <-ctx.Done():
	default:
		t.Error("a late setCancel must cancel the ctx immediately; otherwise the crawl keeps " +
			"running until TaskTimeout even though DELETE already returned 204")
	}
}

// TestStore_TTLsweep: design §7 — terminal tasks are cleaned up after the TTL.
// Sets doneAt into the past directly instead of sleeping the whole TTL
// (TaskTTLSeconds is whole seconds, minimum 1s).
func TestStore_TTLsweep(t *testing.T) {
	s := newTestStore()
	id, _, err := s.submit("https://a.com/", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	s.finishSuccess(id, &crawler.Result{Pages: []crawler.Page{{URL: "https://a.com/", Markdown: "x"}}})

	// terminal but within TTL: still queryable
	if _, ok := s.get(id); !ok {
		t.Fatal("task must survive before TTL expires")
	}

	// push the finish time past the TTL (white-box, same package: set doneAt directly —
	// get sweeps along the way)
	s.mu.Lock()
	s.tasks[id].doneAt = time.Now().Add(-time.Duration(s.cfg.TaskTTLSeconds+60) * time.Second)
	s.mu.Unlock()

	if _, ok := s.get(id); ok {
		t.Error("task must be swept after TTL expires")
	}
}

// TestStore_FinishFailed: design §2.2 terminal-state rules — 0 pages fetched → failed,
// error carries the reason; payload cleared; the same URL can be resubmitted after
// failure (not blocked by dedupe).
func TestStore_FinishFailed(t *testing.T) {
	s := newTestStore()
	id, _, err := s.submit("https://a.com/", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	s.setRunning(id)
	s.finishFailed(id, errors.New("entry page https://a.com/ failed: http status 404"))

	task, ok := s.get(id)
	if !ok {
		t.Fatal("failed task must still be queryable within TTL")
	}
	if task.Status != CrawlFailed {
		t.Errorf("Status = %q, want failed", task.Status)
	}
	if task.Error == "" || !strings.Contains(task.Error, "http status 404") {
		t.Errorf("Error = %q, want the underlying reason", task.Error)
	}
	if task.Pages != 0 || task.Markdown != "" || len(task.Items) != 0 {
		t.Errorf("failed task payload must be empty: %+v", task)
	}
	if task.Truncated {
		t.Error("failed task must not be marked truncated")
	}

	// after terminal state the same URL can be resubmitted (finishLocked cleared byURL)
	if _, deduped, _ := s.submit("https://a.com/", crawler.Options{}); deduped {
		t.Error("after failed, submit must not be deduped")
	}
}
