package service

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"void2md/file/pkg/config"
	"void2md/file/pkg/crawler"
)

// This file pins zero-value config semantics (final review F6): 0 means unset and
// takes the default, never "protection off". All three protections must still hold:
//   - MaxTasks=0        → default 20, not "unlimited tasks"
//   - TaskTTLSeconds=0  → default 1800s, not "never sweep"
//   - MaxPages=0        → default 200, not a hardcoded fallback inside Submit
//
// All tests construct `config.HTMLCrawlConfig{}` (all zeros) directly, exercising
// newHTMLCrawlStore / NewHTMLCrawlService normalization without env — direct struct
// construction is the path most likely to produce zero values and bypass config loading.

// TestStore_ZeroConfigStillEnforcesMaxTasks: zero config must still rate-limit.
//
// Under the old semantics `if s.cfg.MaxTasks > 0 && alive >= s.cfg.MaxTasks` was always
// false for 0, so alive tasks had no upper bound — each can hold 200MB, disabling the
// memory protection entirely.
func TestStore_ZeroConfigStillEnforcesMaxTasks(t *testing.T) {
	s := newHTMLCrawlStore(config.HTMLCrawlConfig{})
	limit := config.DefaultHTMLCrawlConfig().MaxTasks

	for i := 0; i < limit; i++ {
		// distinct URL per task to avoid the dedupe branch (dedupe reuses instead of creating)
		if _, _, err := s.submit(fmt.Sprintf("https://a.com/p%d", i), crawler.Options{}); err != nil {
			t.Fatalf("task #%d rejected: %v (zero must not mean \"unlimited tasks\")", i+1, err)
		}
	}

	_, _, err := s.submit("https://a.com/overflow", crawler.Options{})
	if err == nil {
		t.Fatalf("must reject beyond default MaxTasks(%d): zero config must not mean \"unlimited tasks\"", limit)
	}
	if !isTooManyRequests(err) {
		t.Errorf("err = %v, want 429 + FF000D (same rejection as with explicit MaxTasks)", err)
	}
}

// TestStore_ZeroConfigStillSweeps: zero config must still sweep.
//
// Under the old semantics `if ttl <= 0 { return }` made TTL=0 mean "never sweep": terminal
// tasks' full payloads (up to 200MB Markdown each) would stay in memory forever with no log hint.
//
// Both sides must be asserted: "expired gets swept" alone is vacuous — if the zero TTL stayed
// 0 instead of being defaulted, `now.Sub(doneAt) > 0` holds for *every* terminal task, expired
// ones still get swept, and the assertion still passes. So pair it with a positive assertion:
// a just-terminal task must not be swept immediately — that's what actually pins
// "0 was defaulted to 1800s".
func TestStore_ZeroConfigStillSweeps(t *testing.T) {
	s := newHTMLCrawlStore(config.HTMLCrawlConfig{})
	def := config.DefaultHTMLCrawlConfig()

	// positive: a just-terminal task must be kept
	freshID, _, err := s.submit("https://a.com/fresh", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	s.finishFailed(freshID, errors.New("boom"))
	if _, ok := s.get(freshID); !ok {
		t.Errorf("a just-terminal task must not be swept immediately: zero TTL must default to %ds, not 0",
			def.TaskTTLSeconds)
	}

	// negative: past the default TTL it must be swept (push the finish time back 2h)
	oldID, _, err := s.submit("https://a.com/old", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	s.finishFailed(oldID, errors.New("boom"))
	s.mu.Lock()
	s.tasks[oldID].doneAt = time.Now().Add(-2 * time.Hour)
	s.mu.Unlock()

	if _, ok := s.get(oldID); ok {
		t.Errorf("terminal tasks past the default TTL(%ds) must be swept, not kept forever", def.TaskTTLSeconds)
	}
}

// TestStore_SweepSurvivesLateProgress pins that sweeping never drops entries that haven't
// reached a terminal state — paired with the previous test to keep "sweeping works" from
// becoming "sweep everything".
//
// This held before normalization (the doneAt zero guard); restated here because the previous
// test changed the TTL source — if someone also deleted sweepLocked's guard, the previous
// test would still pass while this one fails.
func TestStore_SweepSurvivesLateProgress(t *testing.T) {
	s := newHTMLCrawlStore(config.HTMLCrawlConfig{})
	id, _, err := s.submit("https://a.com/", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}

	s.mu.Lock()
	s.tasks[id].alive = false // not alive, but doneAt is still zero (not terminal)
	s.sweepLocked()
	_, survived := s.tasks[id]
	s.mu.Unlock()
	if !survived {
		t.Error("an entry with zero doneAt must not be swept (alive=false doesn't mean terminal is settled)")
	}
}

// TestService_ZeroConfigBuildOptions pins the "config → crawler.Options" step: zero-config
// options must carry defaults, and MB must be converted to bytes.
//
// Why pin this step separately: it previously had no tests, and there are two deletable
// `<< 20` here. Without them, the configured 200/10 (MB) would be passed as 200/10 *bytes* —
// per-page HTML limit 10 bytes, per-task budget 200 bytes — so no real page could ever be
// fetched; HTTP-level tests feed small httptest pages that might not expose it (a small page
// might be caught by another branch first under a 200-byte budget).
//
// Expected values are hardcoded 200MB / 10MB, not derived from DefaultHTMLCrawlConfig():
// sharing the same expression on both sides would hide a jointly-deleted `<<20` (same
// reasoning as TestHTMLCrawlConfigDefaults).
func TestService_ZeroConfigBuildOptions(t *testing.T) {
	s := NewHTMLCrawlService(config.HTMLCrawlConfig{})
	def := config.DefaultHTMLCrawlConfig()

	u, err := crawler.NormalizeURL("https://a.com/x")
	if err != nil {
		t.Fatalf("NormalizeURL: %v", err)
	}
	opts := s.buildOptions(u, "body", 7)

	if opts.MaxTaskBytes != 200<<20 {
		t.Errorf("MaxTaskBytes = %d, want 200MB (%d): per-task byte budget must be converted from MB to bytes", opts.MaxTaskBytes, 200<<20)
	}
	if opts.MaxHTMLSize != 10<<20 {
		t.Errorf("MaxHTMLSize = %d, want 10MB (%d): per-page HTML limit must be converted from MB to bytes", opts.MaxHTMLSize, 10<<20)
	}
	if opts.Concurrency != def.Concurrency {
		t.Errorf("Concurrency = %d, want default %d", opts.Concurrency, def.Concurrency)
	}
	if opts.PageTimeout != time.Duration(def.PageTimeout)*time.Second {
		t.Errorf("PageTimeout = %v, want %d seconds", opts.PageTimeout, def.PageTimeout)
	}
	if opts.TaskTimeout != time.Duration(def.TaskTimeout)*time.Second {
		t.Errorf("TaskTimeout = %v, want %d seconds", opts.TaskTimeout, def.TaskTimeout)
	}
	if opts.UserAgent != def.UserAgent {
		t.Errorf("UserAgent = %q, want default %q", opts.UserAgent, def.UserAgent)
	}
	if opts.MaxPage != 7 || opts.Selector != "body" {
		t.Errorf("params must pass through unchanged, got MaxPage=%d Selector=%q", opts.MaxPage, opts.Selector)
	}
}

// TestService_ZeroConfigMaxPages: under zero config, page resolution must land on defaults,
// and explicit params keep their clamp/validation semantics (design §2.1).
func TestService_ZeroConfigMaxPages(t *testing.T) {
	def := config.DefaultHTMLCrawlConfig()
	s := NewHTMLCrawlService(config.HTMLCrawlConfig{})

	got, err := s.resolveMaxPages(nil)
	if err != nil {
		t.Fatalf("maxPage=nil must not error: %v", err)
	}
	if got != def.MaxPages {
		t.Errorf("zero MaxPages must take the default, got %d want %d", got, def.MaxPages)
	}

	big := 100000
	got, err = s.resolveMaxPages(&big)
	if err != nil {
		t.Fatalf("above the hard limit must clamp, not error: %v", err)
	}
	if got != def.HardMaxPages {
		t.Errorf("zero HardMaxPages must clamp to the default hard limit %d, got %d", def.HardMaxPages, got)
	}

	three := 3
	if got, err = s.resolveMaxPages(&three); err != nil || got != 3 {
		t.Errorf("a valid param must take effect as-is, got %d err %v", got, err)
	}

	zero := 0
	if _, err = s.resolveMaxPages(&zero); err == nil {
		t.Error("maxPage=0 must return a param error (param validation is unaffected by normalization)")
	}
}
