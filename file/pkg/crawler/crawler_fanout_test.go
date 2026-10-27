package crawler

// This file is supplementary coverage for Task 7. Kept separate for two reasons:
//  1. the planned test block (crawler_test.go lines 1-399) must stay verbatim
//     with the plan, while the cases below need test-only packages like
//     sync/atomic that are not in the planned import block;
//  2. this case pins scheduler-layer semantics (batch-synchronous fanout),
//     unlike crawler_test.go's cases organized by entry/terminal-state/truncation.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestCrawl_BatchFanoutIsConcurrent pins design §4.3's "batch-synchronous fanout":
// pages in one batch must be fetched truly concurrently (semaphore of capacity
// Concurrency), not one by one serially.
//
// The construction deliberately avoids sleep-based scheduling gambles: the leaf
// handler blocks until a **second concurrent visitor** arrives (or 2s fallback), so
//   - a truly concurrent implementation (semaphore capacity 4): the first batch's
//     4 requests arrive nearly together; the 2nd releases the gate and the whole
//     crawl finishes in milliseconds (5 pages);
//   - semaphore capacity changed to 1 (mutation m_concurrency_serial): only 1
//     request in flight at a time, every leaf waits the full 2s, and maxInFlight
//     stays 1 → the assertion below fails.
//
// Asserts the **observed max in-flight requests**, not elapsed time: slower
// machines overlap requests more easily, only making >= 2 easier to satisfy, so
// there is no "occasional false red under load" smell (same consideration as
// the planned 1s-budget case deliberately leaving headroom).
func TestCrawl_BatchFanoutIsConcurrent(t *testing.T) {
	var inFlight, maxInFlight, released int32
	release := make(chan struct{})

	enter := func() {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			cur := atomic.LoadInt32(&maxInFlight)
			if n <= cur || atomic.CompareAndSwapInt32(&maxInFlight, cur, n) {
				break
			}
		}
		// The second concurrent visitor releases the gate; CAS ensures close happens once.
		if n >= 2 && atomic.CompareAndSwapInt32(&released, 0, 1) {
			close(release)
		}
		select {
		case <-release:
		case <-time.After(2 * time.Second): // a serial implementation lands here
		}
		atomic.AddInt32(&inFlight, -1)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(page("/a", "/b", "/c", "/d")))
			return
		}
		enter()
		_, _ = w.Write([]byte(page()))
	}))
	defer srv.Close()

	opts := defaultOpts(srv.URL + "/")
	opts.Concurrency = 4
	opts.MaxPage = 8
	opts.TaskTimeout = 30 * time.Second

	res, err := Crawl(context.Background(), opts, nil)
	if err != nil {
		t.Fatalf("Crawl error: %v", err)
	}
	if len(res.Pages) != 5 { // entry + /a /b /c /d
		t.Fatalf("pages = %d, want 5; urls=%v", len(res.Pages), pageURLs(res))
	}
	if n := atomic.LoadInt32(&maxInFlight); n < 2 {
		t.Errorf("max concurrent in-flight requests = %d, want >= 2; pages in one batch must be fetched concurrently (semaphore capacity Concurrency), not serially", n)
	}
}
