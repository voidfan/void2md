package service

// This file supplements Task 9's tests (a third file beyond the planned test block).
//
// Reason: the planned Task 9 tests had zero detection power for the semantics below —
// mutating the implementation in these directions left the planned tests all green (see
// the "before supplements" column of the Task 9 report). Each test corresponds to a
// mutation verified to survive; comments name the pinned behavior and the mutation.
//
// Named with the TestStore_ prefix so `go test ./internal/service/ -run TestStore` runs
// them together with the planned tests; each carries static pre-assertions (the fixture
// really took effect) to avoid "empty fixture → vacuously true assertion".

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"void2md/common/diagnosis"
	"void2md/file/pkg/crawler"
)

// TestStore_SuccessPayload pins finishSuccess's output itself:
// items / pages / markdown (with source comments) / truncated propagation.
//
// Mutations m8 (`e.task.Markdown = ""`) and m9 (`finishLocked(..., res.Truncated)` changed
// to `false`) survived the planned tests: the plan only called finishSuccess in
// TestStore_Dedup and TestStore_TTLsweep, asserting only "resubmit after terminal" and
// "sweep after TTL", never the output. This test asserts every field positively: what
// should appear must appear.
func TestStore_SuccessPayload(t *testing.T) {
	s := newTestStore()
	id, _, err := s.submit("https://a.com/", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	s.finishSuccess(id, &crawler.Result{
		Pages: []crawler.Page{
			{URL: "https://a.com/1", Title: "第一页", Markdown: "正文一"},
			{URL: "https://a.com/2", Title: "第二页", Markdown: "正文二"},
		},
		Truncated: true,
	})

	task, ok := s.get(id)
	if !ok {
		t.Fatal("finished task must be queryable within TTL")
	}
	if task.Status != CrawlSuccess {
		t.Errorf("Status = %q, want success", task.Status)
	}
	if task.Pages != 2 || len(task.Items) != 2 {
		t.Fatalf("Pages = %d, len(Items) = %d, want 2/2", task.Pages, len(task.Items))
	}
	if task.Items[0].URL != "https://a.com/1" || task.Items[0].Title != "第一页" || task.Items[0].Markdown != "正文一" {
		t.Errorf("Items[0] = %+v, want the first page verbatim", task.Items[0])
	}
	if task.Items[1].URL != "https://a.com/2" || task.Items[1].Title != "第二页" || task.Items[1].Markdown != "正文二" {
		t.Errorf("Items[1] = %+v, want the second page verbatim", task.Items[1])
	}
	// markdown is the concatenation of items, each page preceded by a source comment
	// (downstream chunkers use it to annotate chunk provenance)
	for _, want := range []string{
		"<!-- source: https://a.com/1 -->", "正文一",
		"<!-- source: https://a.com/2 -->", "正文二",
	} {
		if !strings.Contains(task.Markdown, want) {
			t.Errorf("Markdown must contain %q, got %q", want, task.Markdown)
		}
	}
	// page order: page 1's source comment must appear before page 2's
	if strings.Index(task.Markdown, "https://a.com/1") > strings.Index(task.Markdown, "https://a.com/2") {
		t.Errorf("Markdown must keep crawl order, got %q", task.Markdown)
	}
	if !task.Truncated {
		t.Error("Truncated must be propagated from crawler.Result")
	}
	if task.Error != "" {
		t.Errorf("a success task must not carry an error, got %q", task.Error)
	}
}

// TestStore_CancelTerminalIsIdempotent pins cancel()'s idempotent semantics on terminal
// tasks: returns true (task exists, DELETE should answer 204 not 404) and leaves existing
// results untouched.
//
// Mutation m3 (`return true // already terminal` changed to `return false`) survived the
// planned tests: the plan only called cancel on a **pending** task in
// TestStore_CancelBeforeSetCancel, never on a terminal one.
func TestStore_CancelTerminalIsIdempotent(t *testing.T) {
	s := newTestStore()
	id, _, err := s.submit("https://a.com/", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	s.finishSuccess(id, &crawler.Result{Pages: []crawler.Page{{URL: "https://a.com/", Markdown: "正文"}}})

	if !s.cancel(id) {
		t.Error("cancel on a terminal task must report success (idempotent), so DELETE returns 204")
	}
	task, ok := s.get(id)
	if !ok {
		t.Fatal("task must survive a redundant cancel")
	}
	if task.Status != CrawlSuccess {
		t.Errorf("Status = %q, want success unchanged", task.Status)
	}
	if task.Pages != 1 || !strings.Contains(task.Markdown, "正文") {
		t.Errorf("a redundant cancel must not wipe the result, got %+v", task)
	}
	if s.cancel("hpa_does_not_exist") {
		t.Error("cancel on an unknown id must report false, so DELETE returns 404")
	}
}

// TestStore_FinishCancelledClearsPayload pins finishCancelled's payload clearing
// ("task ended due to ctx cancel" is also design §2.2 cancelled semantics).
//
// Mutation m5 (delete the three payload-clearing lines) survived the planned tests: no
// planned test called finishCancelled. Here we white-box a non-empty payload (simulating
// "ctx cancelled after a few pages were fetched"), confirm via static pre-assertion that
// the fixture took effect, then assert it is cleared.
func TestStore_FinishCancelledClearsPayload(t *testing.T) {
	s := newTestStore()
	id, _, err := s.submit("https://a.com/", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	s.setRunning(id)
	s.setProgress(id, 3) // 3 pages already reported done while running

	// white-box payload: simulates results computed in run but not yet committed
	s.mu.Lock()
	e := s.tasks[id]
	e.task.Markdown = "半截结果"
	e.task.Items = []HTMLCrawlItem{{URL: "https://a.com/1", Markdown: "半截结果"}}
	s.mu.Unlock()

	// pre-assertion: the fixture really took effect (else the assertions below would be
	// vacuously true on an already-empty payload)
	pre, ok := s.get(id)
	if !ok {
		t.Fatal("fixture: task must be queryable")
	}
	if pre.Pages != 3 || pre.Markdown != "半截结果" || len(pre.Items) != 1 {
		t.Fatalf("fixture not applied, got %+v", pre)
	}

	s.finishCancelled(id)

	task, ok := s.get(id)
	if !ok {
		t.Fatal("cancelled task must still be queryable within TTL")
	}
	if task.Status != CrawlCancelled {
		t.Errorf("Status = %q, want cancelled", task.Status)
	}
	if task.Pages != 0 || task.Markdown != "" || len(task.Items) != 0 {
		t.Errorf("cancelled task payload must be dropped, got %+v", task)
	}
	if task.Truncated || task.Error != "" {
		t.Errorf("cancelled task must carry no truncated/error flags, got %+v", task)
	}
	// after terminal state the same URL can be resubmitted
	if _, deduped, _ := s.submit("https://a.com/", crawler.Options{}); deduped {
		t.Error("after cancel, submit of the same url must not be deduped")
	}
}

// TestStore_TerminalReleasesURLIndex pins finishLocked's delete(s.byURL, ...).
//
// Mutation m2 (delete that delete) survived the planned tests: submit has a self-heal
// branch — a byURL hit whose entry is no longer alive is deleted in place and recreated —
// so "resubmit after terminal" still got a new id with deduped=false, invisible to the
// plan. The cost: byURL permanently retains entries pointing at non-alive tasks (memory
// only grows, and it violates design §2.1's "normalized URL → *alive* task ID" index
// semantics). This test asserts the index is released directly.
func TestStore_TerminalReleasesURLIndex(t *testing.T) {
	s := newTestStore()
	const url = "https://a.com/"
	id, _, err := s.submit(url, crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}

	// pre-assertion: really indexed while alive (else "released" could be vacuously true
	// because the index was never built)
	s.mu.Lock()
	_, indexedWhileAlive := s.byURL[url]
	s.mu.Unlock()
	if !indexedWhileAlive {
		t.Fatal("fixture: an alive task must be present in byURL")
	}

	s.finishSuccess(id, &crawler.Result{Pages: []crawler.Page{{URL: url, Markdown: "x"}}})

	s.mu.Lock()
	idx, stillIndexed := s.byURL[url]
	s.mu.Unlock()
	if stillIndexed {
		t.Errorf("a terminal task must be removed from byURL, it still points at %q", idx)
	}

	// positive: resubmission creates a new task and re-occupies the index
	id2, deduped, err := s.submit(url, crawler.Options{})
	if err != nil {
		t.Fatalf("resubmit error: %v", err)
	}
	if deduped || id2 == id {
		t.Errorf("resubmit must create a new task, got id=%q deduped=%v", id2, deduped)
	}
	s.mu.Lock()
	n, indexed2 := s.byURL[url]
	s.mu.Unlock()
	if !indexed2 || n != id2 {
		t.Errorf("byURL[%q] = %q (present=%v), want %q", url, n, indexed2, id2)
	}
}

// TestStore_ItemsNeverNull pins the "items is always an array, never null" response
// contract (design §2.2 response example; implemented by get's defensive copy + Items
// initialization in the constructor).
//
// Mutation m11 (delete both the defensive copy in get and the constructor's Items init)
// survived the planned tests: the plan only checked len(task.Items), and nil vs empty
// slice both equal 0. The consequence: the response body turns from "items": [] into
// "items": null, breaking downstream array parsing.
func TestStore_ItemsNeverNull(t *testing.T) {
	s := newTestStore()

	id, _, err := s.submit("https://a.com/", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	task, ok := s.get(id)
	if !ok {
		t.Fatal("task not found after submit")
	}
	if task.Items == nil {
		t.Error("pending task Items must be a non-nil empty slice, otherwise JSON emits \"items\": null")
	}

	// terminal paths too: failed / cancelled must both be non-nil empty slices
	id2, _, _ := s.submit("https://b.com/", crawler.Options{})
	s.finishFailed(id2, nil)
	t2, ok := s.get(id2)
	if !ok {
		t.Fatal("failed task not found")
	}
	if t2.Items == nil {
		t.Error("failed task Items must be a non-nil empty slice")
	}

	id3, _, _ := s.submit("https://c.com/", crawler.Options{})
	if !s.cancel(id3) {
		t.Fatal("cancel must succeed on a pending task")
	}
	t3, ok := s.get(id3)
	if !ok {
		t.Fatal("cancelled task not found")
	}
	if t3.Items == nil {
		t.Error("cancelled task Items must be a non-nil empty slice")
	}
}

// TestStore_SweepNeverDropsUnfinishedEntries pins sweepLocked's `!e.doneAt.IsZero()`
// guard (per htmlCrawlEntry.doneAt's field comment: zero means not terminal yet).
//
// Mutation m6 (delete the guard) survived the planned tests: TestStore_TTLsweep only
// covers "terminal and past TTL", and finishLocked always sets alive=false and doneAt
// together, so removing the guard changed no real path. The guard matters for "entry
// marked not-alive but doneAt not yet set" — without it such entries are swept instantly
// (under callback/race timing, tasks would vanish into thin air).
//
// Includes a negative assertion (doneAt set into the past must be swept) so "not swept"
// can't be vacuously true from sweepLocked being broken entirely.
func TestStore_SweepNeverDropsUnfinishedEntries(t *testing.T) {
	s := newTestStore()
	id, _, err := s.submit("https://a.com/", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}

	s.mu.Lock()
	s.tasks[id].alive = false // not alive…
	// …but doneAt is still zero: per the field comment, "not terminal yet", not sweepable
	s.sweepLocked()
	_, survived := s.tasks[id]
	s.mu.Unlock()
	if !survived {
		t.Error("sweepLocked must not drop an entry whose doneAt is still zero (not terminal yet)")
	}

	// negative assertion: once doneAt is set and past the TTL, the same entry must be swept
	s.mu.Lock()
	s.tasks[id].doneAt = time.Now().Add(-time.Duration(s.cfg.TaskTTLSeconds+60) * time.Second)
	s.sweepLocked()
	_, survived2 := s.tasks[id]
	s.mu.Unlock()
	if survived2 {
		t.Error("the same entry must be swept once doneAt is older than TTL")
	}
}

// TestStore_LateProgressDoesNotResurrectPayload pins setProgress / setRunning's aliveness
// check ("cancelled → discard fetched pages" is Task 9's core semantic).
//
// The planned tests never covered these methods' `e.alive` check: degrading `ok && e.alive`
// to just `ok` (mutations m12/m13) lets one late callback from the running goroutine give
// a just-cancelled task pages>0 again, or even flip its status back to running — with the
// planned tests all green.
func TestStore_LateProgressDoesNotResurrectPayload(t *testing.T) {
	s := newTestStore()
	id, _, err := s.submit("https://a.com/", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	s.setRunning(id)

	// pre-assertion: setRunning really took effect (proves "status wasn't flipped back"
	// below isn't because it never applied)
	pre, ok := s.get(id)
	if !ok {
		t.Fatal("task not found after submit")
	}
	if pre.Status != CrawlRunning {
		t.Fatalf("fixture: setRunning must move the task to running, got %q", pre.Status)
	}

	if !s.cancel(id) {
		t.Fatal("cancel must succeed on a running task")
	}

	s.setProgress(id, 7) // late progress callback
	s.setRunning(id)     // late status callback

	task, ok := s.get(id)
	if !ok {
		t.Fatal("cancelled task must still be queryable within TTL")
	}
	if task.Status != CrawlCancelled {
		t.Errorf("Status = %q, a late setRunning must not flip it back", task.Status)
	}
	if task.Pages != 0 {
		t.Errorf("Pages = %d, a late setProgress must not resurrect the payload of a cancelled task", task.Pages)
	}
	if task.Markdown != "" || len(task.Items) != 0 {
		t.Errorf("payload must stay empty, got %+v", task)
	}
}

// TestStore_FinishFailedNilErrorUsesDefault pins finishFailed's nil-error fallback: a
// failed task's error field must be non-empty. The API exposes that field directly as
// error; an empty string means "failed without saying why".
//
// The planned tests called finishFailed with nil only once in TestStore_MaxTasks, which
// asserted only "slot released" and never read Error — changing `msg := "unknown error"`
// to `msg := ""` (mutation m14) left the plan all green.
func TestStore_FinishFailedNilErrorUsesDefault(t *testing.T) {
	s := newTestStore()
	id, _, err := s.submit("https://a.com/", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	s.finishFailed(id, nil)

	task, ok := s.get(id)
	if !ok {
		t.Fatal("failed task must be queryable within TTL")
	}
	if task.Status != CrawlFailed {
		t.Errorf("Status = %q, want failed", task.Status)
	}
	if task.Error != "unknown error" {
		t.Errorf("Error = %q, want the non-empty default \"unknown error\"", task.Error)
	}
}

// TestStore_TaskIDFormat pins newTaskID's documented contract ("16-hex-digit task ID with
// hpa_ prefix"): prefix + total length 20 + hex + distinct across two submits.
//
// The planned tests only asserted id1 != id3 (on the dedupe path), checking neither prefix
// nor length — changing `hex.EncodeToString(b[:])` to `b[:4]` (mutation m15) left the plan
// all green.
func TestStore_TaskIDFormat(t *testing.T) {
	s := newTestStore()
	id1, _, err := s.submit("https://a.com/1", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	id2, _, err := s.submit("https://a.com/2", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	for _, id := range []string{id1, id2} {
		if !strings.HasPrefix(id, "hpa_") {
			t.Errorf("task id %q must carry the hpa_ prefix", id)
		}
		if len(id) != len("hpa_")+16 {
			t.Errorf("task id %q has length %d, want 20 (hpa_ + 16 hex chars)", id, len(id))
		}
		if _, err := hex.DecodeString(id[len("hpa_"):]); err != nil {
			t.Errorf("task id %q must be 16 hex chars after the prefix: %v", id, err)
		}
	}
	if id1 == id2 {
		t.Error("two tasks must get distinct ids")
	}
}

// ---- BEGIN audit-response tests ----
// Above is supplement_v1 (first round, 9 tests). The two below respond to gaps reported by
// a read-only audit (2026-09-16) that used supplement_v1: m17 / m16 were measured to
// still survive it.

// TestStore_TerminalFinishWithoutAliveGuardIsIgnored pins the `!e.alive` guards in
// finishSuccess / finishFailed / finishCancelled.
//
// Audit measurement: changing all three `if !ok || !e.alive { return }` to `if !ok { return }`
// (mutation m17) stayed green even with supplement_v1. This is the only defense for "after
// DELETE returns 204, an in-flight crawl must not overwrite the cancelled task": without the
// guard, a late finishSuccess flips cancelled to success and refills markdown/items, directly
// violating design §2.3.
//
// Task 10's CancelDiscardsPages takes the ctx-cancel → finishCancelled path, where cancel()
// already set alive=false first, so the hole is invisible on that path.
//
// The doneAt assertion makes all three subtests discriminating: for finishCancelled the status
// is already cancelled and the payload already empty, so without the guard the only observable
// difference is doneAt being refreshed (the task stays in memory one more TTL).
func TestStore_TerminalFinishWithoutAliveGuardIsIgnored(t *testing.T) {
	res := &crawler.Result{Pages: []crawler.Page{{URL: "https://a.com/1", Markdown: "迟到结果"}}}

	cases := []struct {
		name   string
		finish func(s *htmlCrawlStore, id string)
	}{
		{"finishSuccess", func(s *htmlCrawlStore, id string) { s.finishSuccess(id, res) }},
		{"finishFailed", func(s *htmlCrawlStore, id string) { s.finishFailed(id, errors.New("迟到失败")) }},
		{"finishCancelled", func(s *htmlCrawlStore, id string) { s.finishCancelled(id) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore()
			id, _, err := s.submit("https://a.com/", crawler.Options{})
			if err != nil {
				t.Fatalf("submit error: %v", err)
			}
			s.setRunning(id)
			if !s.cancel(id) {
				t.Fatal("fixture: cancel must succeed on a running task")
			}

			// pre-assertion: really terminal with an empty payload (else the assertions
			// below would be vacuously true because the fixture never applied)
			pre, ok := s.get(id)
			if !ok {
				t.Fatal("fixture: task must be queryable")
			}
			if pre.Status != CrawlCancelled || pre.Pages != 0 || pre.Markdown != "" || len(pre.Items) != 0 {
				t.Fatalf("fixture: task must be cancelled with an empty payload, got %+v", pre)
			}
			s.mu.Lock()
			doneAtBefore := s.tasks[id].doneAt
			s.mu.Unlock()
			if doneAtBefore.IsZero() {
				t.Fatal("fixture: a terminal task must have a non-zero doneAt")
			}

			tc.finish(s, id) // late terminal callback: must be dropped by the guard

			task, ok := s.get(id)
			if !ok {
				t.Fatal("task must still be queryable within TTL")
			}
			if task.Status != CrawlCancelled {
				t.Errorf("Status = %q, a late %s must not overwrite a cancelled task", task.Status, tc.name)
			}
			if task.Pages != 0 || task.Markdown != "" || len(task.Items) != 0 {
				t.Errorf("a late %s must not resurrect the payload, got %+v", tc.name, task)
			}
			if task.Error != "" || task.Truncated {
				t.Errorf("a late %s must not set error/truncated, got %+v", tc.name, task)
			}
			s.mu.Lock()
			doneAtAfter := s.tasks[id].doneAt
			s.mu.Unlock()
			if !doneAtAfter.Equal(doneAtBefore) {
				t.Errorf("a late %s must not refresh doneAt (%v -> %v); it would keep the task in memory for another TTL",
					tc.name, doneAtBefore, doneAtAfter)
			}
		})
	}
}

// TestStore_TooManyRequestsDiagnosisCode bypasses isTooManyRequests and asserts the 429
// diagnosis code independently.
//
// The audit noted: TestStore_MaxTasks asserts via isTooManyRequests(err), which is exactly
// submit's internal predicate — testing itself with its own criterion. Deleting the helper's
// DCode comparison and keeping only StatusCode == 429 (mutation m16) left all tests green,
// while the API response's diagnosisCode would silently change from FF000D to another code;
// downstream routing by diagnosisCode would misread "too many requests" as "invalid param".
// Design §2.1/§5 added FF000D to make this semantic distinguishable.
//
// Asserts DiagnosisException fields directly with the *literal*, not a comparison against
// diagnosis.ErrorTooManyRequestsDiagnosisCode — the latter degrades into a vacuous
// "two declarations agree" assertion (both changed together still passes).
func TestStore_TooManyRequestsDiagnosisCode(t *testing.T) {
	cfg := testCrawlCfg()
	cfg.MaxTasks = 1
	s := newHTMLCrawlStore(cfg)

	if _, _, err := s.submit("https://a.com/1", crawler.Options{}); err != nil {
		t.Fatalf("first submit error: %v", err)
	}
	_, _, err := s.submit("https://a.com/2", crawler.Options{})
	if err == nil {
		t.Fatal("second submit must be rejected with 429 once MaxTasks is exhausted")
	}
	var de *diagnosis.DiagnosisException
	if !errors.As(err, &de) {
		t.Fatalf("error = %T (%v), want *diagnosis.DiagnosisException", err, err)
	}
	if de.StatusCode != 429 {
		t.Errorf("StatusCode = %d, want 429", de.StatusCode)
	}
	if de.DCode != "0x0000000000FF000D" {
		t.Errorf("DCode = %q, want the literal \"0x0000000000FF000D\" (FF000D too many requests)", de.DCode)
	}
}

// TestStore_SourceMarkerCannotBeEscaped pins the *structural integrity* of source comments:
// whatever sequences a URL contains, each page's `<!-- source: <url> -->` line must form
// exactly one complete comment, never closed early by a `-->` inside the URL.
//
// Why it's needed: finishSuccess concatenates the URL straight into the comment, and URL
// queries are preserved verbatim by both normalization (crawler.NormalizeURL only lowercases
// host, strips fragment/default port) and url.URL.String() — a same-origin link shaped like
// `?x=--><svg/onload=alert(1)>` ends the comment at the first `-->`, and the following
// `<svg/onload=...>` is no longer comment text but *live* Markdown/HTML flowing downstream
// (/v1/chunk, knowledge base) with the aggregated markdown. Design §2.2 explicitly relies on
// "comments are invisible when Markdown renders"; that promise fails for such URLs.
//
// Mutation check: removing commentSafeURL (back to `"<!-- source: " + p.URL + " -->"`) fails
// this test while all others stay green — the shape none of the 13 mutants covered at the time.
func TestStore_SourceMarkerCannotBeEscaped(t *testing.T) {
	evil := "https://a.com/next?x=--><svg/onload=alert(1)>"

	s := newTestStore()
	id, _, err := s.submit("https://a.com/", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	s.finishSuccess(id, &crawler.Result{
		Pages: []crawler.Page{
			{URL: "https://a.com/", Title: "首页", Markdown: "正文"},
			{URL: evil, Title: "子页", Markdown: "子页正文"},
		},
	})

	task, ok := s.get(id)
	if !ok {
		t.Fatal("finished task must be queryable within TTL")
	}

	// pre-assertion: the fixture really brought the dangerous sequence in, else every
	// assertion below is vacuously true.
	if !strings.Contains(task.Items[1].URL, "-->") {
		t.Fatalf("fixture broken: items[1].URL = %q contains no -->", task.Items[1].URL)
	}

	// two pages → exactly two comment terminators in the aggregate. Any extra one was left
	// by a URL closing its comment early.
	if n := strings.Count(task.Markdown, "-->"); n != 2 {
		t.Errorf("comment terminators = %d, want 2 (one per page): the URL's --> closed the comment early, markdown = %q",
			n, task.Markdown)
	}
	// the dangerous payload must not appear outside a comment — `--><svg` means it escaped.
	if strings.Contains(task.Markdown, "--><svg") {
		t.Errorf("the URL's --> escaped the comment and <svg> became a live tag, markdown = %q", task.Markdown)
	}
	// items[].URL is still the raw original: escaping serves the comment only and must not
	// rewrite the data, else downstream provenance via items[].url gets a modified URL.
	if task.Items[1].URL != evil {
		t.Errorf("items[1].URL = %q, want the raw URL %q preserved verbatim", task.Items[1].URL, evil)
	}
}

// TestStore_SourceMarkerChunkStillCarriesSource pins that provenance still works after
// escaping: the source URL must remain readable in the comment (downstream chunks rely on
// it for source annotation); escaping must not erase the URL.
//
// Complements the previous test: that one guards against "escaping the comment", this one
// against "over-fixing — deleting the URL entirely or encoding it unreadably".
func TestStore_SourceMarkerChunkStillCarriesSource(t *testing.T) {
	s := newTestStore()
	id, _, err := s.submit("https://a.com/", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	s.finishSuccess(id, &crawler.Result{
		Pages: []crawler.Page{{URL: "https://a.com/docs/intro", Title: "简介", Markdown: "正文"}},
	})
	task, ok := s.get(id)
	if !ok {
		t.Fatal("finished task must be queryable within TTL")
	}
	if !strings.Contains(task.Markdown, "<!-- source: https://a.com/docs/intro -->") {
		t.Errorf("the comment must keep a readable source URL, markdown = %q", task.Markdown)
	}
}
