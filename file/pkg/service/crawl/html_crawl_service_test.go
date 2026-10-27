package service

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"void2md/common/diagnosis"
)

// newTestCrawlService builds a crawl service for tests.
//
// The originally planned srv parameter was completely unused (t only fed t.Helper()),
// misleadingly implying it governed "server unreachable" failures; it was removed
// along with all call sites.
func newTestCrawlService(t *testing.T) *HTMLCrawlService {
	t.Helper()
	cfg := testCrawlCfg()
	cfg.TaskTimeout = 5
	cfg.PageTimeout = 5
	return NewHTMLCrawlService(cfg)
}

// waitTerminal polls until the task reaches a terminal state.
func waitTerminal(t *testing.T, svc *HTMLCrawlService, id string) HTMLCrawlTask {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		task, ok := svc.Get(id)
		if !ok {
			t.Fatalf("task %s disappeared", id)
		}
		switch task.Status {
		case CrawlSuccess, CrawlFailed, CrawlCancelled:
			return task
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task %s did not reach terminal state", id)
	return HTMLCrawlTask{}
}

func TestService_SubmitCrawlSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`<html><body><a href="/a">a</a><p>首页正文</p></body></html>`))
		default:
			_, _ = w.Write([]byte(`<html><body><p>子页正文</p></body></html>`))
		}
	}))
	defer srv.Close()

	svc := newTestCrawlService(t)
	id, deduped, err := svc.Submit(srv.URL+"/", "", nil)
	if err != nil {
		t.Fatalf("Submit error: %v", err)
	}
	if deduped {
		t.Error("first submit must not be deduped")
	}
	// ID shape promised by design §2.1: hpa_ prefix.
	// Without this, changing newTaskID's prefix to zzz_ stays green.
	if !strings.HasPrefix(id, "hpa_") {
		t.Errorf("taskId = %q, want hpa_ prefix", id)
	}

	task := waitTerminal(t, svc, id)
	if task.Status != CrawlSuccess {
		t.Fatalf("Status = %q (error=%q), want success", task.Status, task.Error)
	}
	if task.Pages != 2 {
		t.Errorf("Pages = %d, want 2", task.Pages)
	}
	if len(task.Items) != 2 {
		t.Errorf("Items = %d, want 2", len(task.Items))
	}
	if task.Error != "" || task.Truncated {
		t.Errorf("Error=%q Truncated=%v, want empty/false", task.Error, task.Truncated)
	}
	// markdown must carry the source comment for downstream provenance
	if !strings.Contains(task.Markdown, "<!-- source: "+srv.URL+"/ -->") {
		t.Errorf("markdown missing source comment, got %q", task.Markdown)
	}
}

// TestService_InvalidURL covers bad URL-level input.
//
// Beyond "must error", asserts the error shape: a DiagnosisException carrying FF0003
// (design §5: missing url / non-http(s) scheme → 400 + FF0003). An err != nil check alone
// would stay green if the diagnosis code were swapped for another (or a bare error).
func TestService_InvalidURL(t *testing.T) {
	svc := NewHTMLCrawlService(testCrawlCfg())
	for _, bad := range []string{"", "ftp://a.com", "not a url at all", "/relative/path"} {
		_, _, err := svc.Submit(bad, "", nil)
		if err == nil {
			t.Errorf("Submit(%q) expected error", bad)
			continue
		}
		var de *diagnosis.DiagnosisException
		if !errors.As(err, &de) {
			t.Errorf("Submit(%q) error = %v (%T), want *DiagnosisException", bad, err, err)
			continue
		}
		if de.DCode != diagnosis.ErrorParamCheckDiagnosisCode {
			t.Errorf("Submit(%q) dcode = %q, want %q", bad, de.DCode, diagnosis.ErrorParamCheckDiagnosisCode)
		}
		if de.StatusCode != 400 {
			t.Errorf("Submit(%q) status = %d, want 400", bad, de.StatusCode)
		}
	}
}

// TestService_MaxPageNonPositive covers design §2.1: maxPage ≤ 0 → 400 + FF0003.
//
// TestService_InvalidURL never passes maxPage, so the service-level
// `if *maxPage <= 0 { return … }` block had zero coverage: deleting it would let pages=0
// fall back to 1 inside crawler.Crawl (see its param fallback), the task would succeed,
// and the API contract (non-positive must 400) would break with all tests green.
//
// Uses 127.0.0.1:1 (reserved port, connection always refused) instead of a real domain:
// if the implementation failed to block and reached `go s.run`, it would only fail fast
// locally without real network traffic.
func TestService_MaxPageNonPositive(t *testing.T) {
	svc := NewHTMLCrawlService(testCrawlCfg())
	for _, bad := range []int{0, -1, -100} {
		_, deduped, err := svc.Submit("http://127.0.0.1:1/", "", &bad)
		if err == nil {
			t.Errorf("Submit(maxPage=%d) expected error, got nil (non-positive maxPage must 400)", bad)
			continue
		}
		var de *diagnosis.DiagnosisException
		if !errors.As(err, &de) {
			t.Errorf("Submit(maxPage=%d) error = %v (%T), want *DiagnosisException", bad, err, err)
			continue
		}
		if de.DCode != diagnosis.ErrorParamCheckDiagnosisCode {
			t.Errorf("Submit(maxPage=%d) dcode = %q, want %q",
				bad, de.DCode, diagnosis.ErrorParamCheckDiagnosisCode)
		}
		if de.StatusCode != 400 {
			t.Errorf("Submit(maxPage=%d) status = %d, want 400", bad, de.StatusCode)
		}
		if deduped {
			t.Errorf("Submit(maxPage=%d) deduped = true, want false (rejected submits must not hit the task store)", bad)
		}
	}
}

// TestService_DedupAliveTask covers design §2.1: an alive task (pending/running) for the
// same normalized URL → return the existing taskId directly.
//
// The site must block: if it responded instantly, the first task could finish before the
// second Submit, byURL would already be cleared, dedupe legitimately wouldn't trigger,
// and the test would flake.
//
// Three assertions, none optional:
//   - deduped == true: deleting `if deduped { return id, true, nil }` leaves the store
//     returning the same id and nil error — only this return value changes;
//   - same id: dedupe must reuse the existing task;
//   - the site sees no second crawl request: positive evidence that dedupe actually saves
//     duplicate network/memory — without it, an implementation that "returns true but still
//     spawns a goroutine" would pass.
func TestService_DedupAliveTask(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	doRelease := func() { releaseOnce.Do(func() { close(release) }) }

	var hits int32
	entered := make(chan struct{}, 8)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		entered <- struct{}{}
		<-release // hold the request so the first task is still alive at the second Submit
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><p>正文</p></body></html>`))
	}))
	// defers run LIFO: the fallback release must run before srv.Close(), else a mid-test
	// Fatal leaves the handler blocked on release and srv.Close() waits forever.
	defer srv.Close()
	defer doRelease()

	cfg := testCrawlCfg()
	cfg.TaskTimeout = 30
	cfg.PageTimeout = 30
	svc := NewHTMLCrawlService(cfg)

	id1, deduped1, err := svc.Submit(srv.URL+"/", "", nil)
	if err != nil {
		t.Fatalf("first Submit error: %v", err)
	}
	if deduped1 {
		t.Error("first submit must not be deduped")
	}

	// wait until the entry request actually reaches the site; the first task is then
	// necessarily alive (blocked in the handler)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("entry page request never reached the server")
	}

	id2, deduped2, err := svc.Submit(srv.URL+"/", "", nil)
	if err != nil {
		t.Fatalf("second Submit error: %v", err)
	}
	if !deduped2 {
		t.Error("deduped = false, want true: must reuse the alive task for the same URL")
	}
	if id2 != id1 {
		t.Errorf("taskId = %q, want %q (dedupe must reuse the existing task)", id2, id1)
	}

	// Dedupe must actually skip the second crawl: no further request may hit the site.
	// (An implementation that only changed the return value but still ran `go s.run`
	// would issue the request immediately.)
	select {
	case <-entered:
		t.Error("second crawl issued a request: a new crawl goroutine started despite dedupe")
	case <-time.After(2 * time.Second):
	}

	doRelease()

	task := waitTerminal(t, svc, id1)
	if task.Status != CrawlSuccess {
		t.Fatalf("Status = %q (error=%q), want success", task.Status, task.Error)
	}
	if task.Pages != 1 {
		t.Errorf("Pages = %d, want 1", task.Pages)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("server hits = %d, want 1 (only one entry request after dedupe)", got)
	}
}

// TestService_MaxPageClamped covers design §2.1: above the hard limit, clamp without
// erroring — and the clamp must actually take effect.
//
// Asserting only "no error" is vacuous: deleting the whole clamp still passes. To observe
// clamping, the site must have more pages than the hard limit — a single-page site yields
// 1 page either way. Here: 5 pages (entry + 4 subpages) vs HardMaxPages=3.
func TestService_MaxPageClamped(t *testing.T) {
	cfg := testCrawlCfg()
	cfg.HardMaxPages = 3
	svc := NewHTMLCrawlService(cfg)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(`<html><body><p>首页</p>` +
				`<a href="/1">1</a><a href="/2">2</a><a href="/3">3</a><a href="/4">4</a>` +
				`</body></html>`))
			return
		}
		_, _ = w.Write([]byte(`<html><body><p>子页</p></body></html>`))
	}))
	defer srv.Close()

	big := 10000
	id, _, err := svc.Submit(srv.URL+"/", "", &big)
	if err != nil {
		t.Fatalf("Submit with oversized maxPage must not error, got %v", err)
	}

	task := waitTerminal(t, svc, id)
	if task.Status != CrawlSuccess {
		t.Fatalf("status = %v, want success (error=%q)", task.Status, task.Error)
	}
	// Crawl truncates the batch to the remaining budget before processing, so exactly
	// MaxPage pages come out.
	if task.Pages != cfg.HardMaxPages {
		t.Errorf("pages = %d, want %d: maxPage=10000 must be clamped to the hard limit (a site with only %d pages couldn't observe clamping)",
			task.Pages, cfg.HardMaxPages, cfg.HardMaxPages)
	}
	if !task.Truncated {
		t.Error("truncated = false, want true: a task cut off by the page limit must set truncated")
	}
}

func TestService_CancelDiscardsPages(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(`<html><body><a href="/slow">s</a><p>首页</p></body></html>`))
			return
		}
		<-release // hold the subpage to create a cancellable window
		_, _ = w.Write([]byte(`<html><body><p>慢页</p></body></html>`))
	}))
	defer srv.Close()
	defer close(release)

	svc := newTestCrawlService(t)
	id, _, err := svc.Submit(srv.URL+"/", "", nil)
	if err != nil {
		t.Fatalf("Submit error: %v", err)
	}

	// Wait for the entry page to be fetched (progress appears) before cancelling. Two points:
	//
	//  1. The observed condition must be "pages >= 1 AND status still running". pages >= 1
	//     alone isn't enough: the terminal transition (finishSuccess) also writes pages, so
	//     with setProgress entirely unwired, the condition would be met the moment
	//     TaskTimeout(5s) flips the task to success, the test would proceed, and a broken
	//     progress loop would only be caught accidentally by the later Status check.
	//  2. The deadline must be clearly larger than TaskTimeout. Both were originally 5s, so
	//     Cancel could land exactly as the task turned terminal: in some timings Cancel hits
	//     an already-terminal task (idempotent no-op), turning this cancel-semantics test
	//     into a timing race.
	deadline := time.Now().Add(60 * time.Second)
	var lastPages int
	var lastStatus HTMLCrawlStatus
	progressed := false
	for time.Now().Before(deadline) {
		task, ok := svc.Get(id)
		if !ok {
			t.Fatalf("task %s disappeared", id)
		}
		lastPages, lastStatus = task.Pages, task.Status
		if task.Pages >= 1 && task.Status == CrawlRunning {
			progressed = true
			break
		}
		if task.Status != CrawlPending && task.Status != CrawlRunning {
			break // turned terminal without ever showing a progress signal while running
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !progressed {
		t.Fatalf("progress signal never advanced: pages stayed %d (status=%q)", lastPages, lastStatus)
	}

	if !svc.Cancel(id) {
		t.Fatal("Cancel returned false for an existing task")
	}

	task := waitTerminal(t, svc, id)
	if task.Status != CrawlCancelled {
		t.Fatalf("Status = %q, want cancelled", task.Status)
	}
	// cancel semantics: fetched pages must be discarded
	if task.Pages != 0 || task.Markdown != "" || len(task.Items) != 0 {
		t.Errorf("cancelled task must discard payload: %+v", task)
	}
}

// TestService_CancelAbortsInFlightRequest: design §2.3 — DELETE on a pending/running task
// must really cancel the task-level ctx.
//
// Asserting only "status becomes cancelled, payload cleared" is vacuous: with run's
// `s.store.setCancel(taskID, cancel)` deleted, store.cancel() still sets cancelled and
// clears the payload synchronously, while the ctx is never cancelled — the crawl goroutine
// keeps running to TaskTimeout (default 300s), fetching and discarding Markdown, and the
// client still sees 204.
//
// The only thing this test observes is the in-flight request actually being cut: after ctx
// cancel the crawler's HTTP request disconnects, and the server sees r.Context().Done().
// TaskTimeout/PageTimeout are both 30s, so a disconnect within the 5s window cannot come
// from a natural timeout.
func TestService_CancelAbortsInFlightRequest(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	doRelease := func() { releaseOnce.Do(func() { close(release) }) }

	slowStarted := make(chan struct{}, 1)
	aborted := make(chan struct{}, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(`<html><body><a href="/slow">s</a><p>首页</p></body></html>`))
			return
		}
		slowStarted <- struct{}{}
		select {
		case <-release:
			_, _ = w.Write([]byte(`<html><body><p>慢页</p></body></html>`))
		case <-r.Context().Done():
			aborted <- struct{}{} // the crawler really disconnected
		}
	}))
	// defers run LIFO: the fallback release must run before srv.Close(), else a mid-test
	// Fatal leaves the handler blocked on release and srv.Close() waits forever.
	defer srv.Close()
	defer doRelease()

	cfg := testCrawlCfg()
	cfg.TaskTimeout = 30
	cfg.PageTimeout = 30
	svc := NewHTMLCrawlService(cfg)

	id, _, err := svc.Submit(srv.URL+"/", "", nil)
	if err != nil {
		t.Fatalf("Submit error: %v", err)
	}

	// Cancel only after the subpage request actually arrives, else the cancel may precede
	// the network call and no in-flight request can be observed.
	select {
	case <-slowStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("slow page request never reached the server")
	}

	if !svc.Cancel(id) {
		t.Fatal("Cancel returned false for an existing task")
	}

	select {
	case <-aborted:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request not aborted after Cancel: ctx cancel had no effect " +
			"(TaskTimeout=30s, cannot end naturally by timeout within 5s)")
	}
}

// TestService_CancelTerminalIsIdempotent: design §2.3 — cancelling a terminal task is an
// idempotent no-op and must not modify existing results.
func TestService_CancelTerminalIsIdempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><p>正文</p></body></html>`))
	}))
	defer srv.Close()

	svc := newTestCrawlService(t)
	id, _, _ := svc.Submit(srv.URL+"/", "", nil)
	before := waitTerminal(t, svc, id)
	if before.Status != CrawlSuccess {
		t.Fatalf("setup failed: status = %q", before.Status)
	}

	if !svc.Cancel(id) {
		t.Error("Cancel on terminal task should return true (idempotent)")
	}
	after, _ := svc.Get(id)
	if after.Markdown != before.Markdown || after.Pages != before.Pages || after.Status != before.Status {
		t.Errorf("terminal task mutated by cancel: before=%+v after=%+v", before, after)
	}
}

func TestService_GetUnknown(t *testing.T) {
	svc := NewHTMLCrawlService(testCrawlCfg())
	if _, ok := svc.Get("hpa_nope"); ok {
		t.Error("Get(unknown) should return false")
	}
	if svc.Cancel("hpa_nope") {
		t.Error("Cancel(unknown) should return false")
	}

	// Positive control: a known taskId must be retrievable via Get with TaskID echoed back.
	// Without it, "unknown returns false" also passes for an implementation where Get
	// always returns false.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><p>正文</p></body></html>`))
	}))
	defer srv.Close()

	svc2 := newTestCrawlService(t)
	id, _, err := svc2.Submit(srv.URL+"/", "", nil)
	if err != nil {
		t.Fatalf("Submit error: %v", err)
	}
	task, ok := svc2.Get(id)
	if !ok {
		t.Fatal("Get(known taskId) returned false")
	}
	if task.TaskID != id {
		t.Errorf("TaskID = %q, want %q", task.TaskID, id)
	}
	waitTerminal(t, svc2, id) // let the crawl goroutine finish
}

// TestService_TimeoutKeepsPartial: design §2.2 — on task timeout, keep partial results
// and set truncated.
func TestService_TimeoutKeepsPartial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(`<html><body><a href="/slow">s</a><p>首页</p></body></html>`))
			return
		}
		time.Sleep(3 * time.Second)
		_, _ = w.Write([]byte(`<html><body><p>慢页</p></body></html>`))
	}))
	defer srv.Close()

	cfg := testCrawlCfg()
	cfg.TaskTimeout = 1 // seconds
	svc := NewHTMLCrawlService(cfg)

	id, _, err := svc.Submit(srv.URL+"/", "", nil)
	if err != nil {
		t.Fatalf("Submit error: %v", err)
	}
	task := waitTerminal(t, svc, id)
	if task.Status != CrawlSuccess {
		t.Fatalf("Status = %q (error=%q), want success with partial result", task.Status, task.Error)
	}
	if !task.Truncated {
		t.Error("Truncated = false, want true")
	}
	if task.Pages != 1 {
		t.Errorf("Pages = %d, want 1 (fetched pages must be kept)", task.Pages)
	}
}
