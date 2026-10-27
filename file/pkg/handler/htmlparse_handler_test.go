package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"void2md/common/diagnosis"
	"void2md/file/pkg/config"
	htmld "void2md/file/pkg/service/crawl"
)

// crawlTestSite stands up a two-page fake site.
func crawlTestSite(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(`<html><body><a href="/a">a</a><p>首页</p></body></html>`))
			return
		}
		_, _ = w.Write([]byte(`<html><body><p>子页</p></body></html>`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newHTMLParseTestRouter replaces the global singleton with a test service (default test config, HardMaxPages=3).
func newHTMLParseTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	return newHTMLParseTestRouterWithCfg(t, config.HTMLCrawlConfig{
		Concurrency: 4, MaxPages: 200, HardMaxPages: 3,
		PageTimeout: 5, TaskTimeout: 5,
		MaxHTMLSizeMB: 10, MaxTaskBytesMB: 200,
		UserAgent: "test", TaskTTLSeconds: 60, MaxTasks: 20,
	})
}

// newHTMLParseTestRouterWithCfg is like newHTMLParseTestRouter but lets a case
// customize the config (e.g. MaxTasks down to 1, triggering 429 in two steps).
func newHTMLParseTestRouterWithCfg(t *testing.T, cfg config.HTMLCrawlConfig) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	// Must construct **one** service outside the closure: NewHTMLCrawlService per
	// request would give each request a fresh store, so a task created by POST
	// would always 404 on GET.
	svc := htmld.NewHTMLCrawlService(cfg)
	getHTMLCrawlService = func() *htmld.HTMLCrawlService { return svc }
	t.Cleanup(func() { getHTMLCrawlService = htmld.GetHTMLCrawlService })

	r := gin.New()
	v1 := r.Group("/v1")
	RegisterHTMLParseRoutes(v1)
	return r
}

func doJSON(t *testing.T, r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestHandleHTMLParse_SubmitAndGet(t *testing.T) {
	site := crawlTestSite(t)
	r := newHTMLParseTestRouter(t)

	w := doJSON(t, r, http.MethodPost, "/v1/htmlparse", `{"url":"`+site.URL+`/"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("submit status = %d, body=%s", w.Code, w.Body.String())
	}
	var sub struct {
		TaskID string `json:"taskId"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &sub); err != nil {
		t.Fatalf("submit response not JSON: %v (%s)", err, w.Body.String())
	}
	if sub.TaskID == "" {
		t.Fatal("missing taskId")
	}
	if sub.Status != "pending" {
		t.Errorf("status = %q, want pending", sub.Status)
	}

	// Poll until terminal
	deadline := time.Now().Add(10 * time.Second)
	var got map[string]any
	for time.Now().Before(deadline) {
		gw := doJSON(t, r, http.MethodGet, "/v1/htmlparse/"+sub.TaskID, "")
		if gw.Code != http.StatusOK {
			t.Fatalf("get status = %d, body=%s", gw.Code, gw.Body.String())
		}
		if err := json.Unmarshal(gw.Body.Bytes(), &got); err != nil {
			t.Fatalf("get response not JSON: %v", err)
		}
		if got["status"] == "success" || got["status"] == "failed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got["status"] != "success" {
		t.Fatalf("final status = %v (error=%v), want success", got["status"], got["error"])
	}
	if got["pages"].(float64) != 2 {
		t.Errorf("pages = %v, want 2", got["pages"])
	}
	// Response fields must be complete (isomorphic to /v1/pdfparse + items/truncated)
	for _, k := range []string{"status", "pages", "markdown", "items", "truncated", "error"} {
		if _, ok := got[k]; !ok {
			t.Errorf("response missing field %q", k)
		}
	}
}

// TestHandleHTMLParse_NoEscape reuses JSONNoEscape: HTML tables in the markdown
// must be output with literal <, else large responses bloat from escaping
// (regression surface of the existing OOM fix).
func TestHandleHTMLParse_NoEscape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><table><tr><td>数据 A&amp;B</td></tr></table></body></html>`))
	}))
	defer srv.Close()

	r := newHTMLParseTestRouter(t)
	w := doJSON(t, r, http.MethodPost, "/v1/htmlparse", `{"url":"`+srv.URL+`/"}`)
	var sub struct {
		TaskID string `json:"taskId"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &sub)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		gw := doJSON(t, r, http.MethodGet, "/v1/htmlparse/"+sub.TaskID, "")
		body := gw.Body.String()
		if strings.Contains(body, `"status":"success"`) {
			// Wording copied from existing repo tests (internal/handler/chunk_handler_test.go:44-45,
			// internal/pkg/response/response_test.go): check **escape sequences**, not literal
			// angle brackets/ampersands, else "assert contains <table>" and "assert no <" contradict.
			if strings.Contains(body, `\u003c`) || strings.Contains(body, `\u0026`) {
				t.Errorf("response HTML-escaped, got: %q", body)
			}
			if !strings.Contains(body, "<table>") {
				t.Errorf("expected literal '<table>' in response, got: %q", body)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("task did not reach success")
}

func TestHandleHTMLParse_ParamErrors(t *testing.T) {
	// Note: the handler does **no** URL / scheme / maxPage validation; these
	// inputs pass through to svc.Submit, which uniformly returns FF0003 (the
	// handler only owns the 400 on JSON binding failure). They still never
	// actually crawl, but the reason is not "Submit isn't reached" — it's that
	// **Submit completes all validation before creating the task and starting
	// the crawl goroutine**.
	//
	// Hence URLs use a local site, not https://example.com: the latter's
	// "no outbound traffic" relies on the single structural assumption of
	// Submit's validation order; if someone moved maxPage validation after
	// store.submit/`go s.run`, these unit tests would silently issue real
	// requests to the public internet. httptest local addresses cost nothing.
	site := crawlTestSite(t)
	r := newHTMLParseTestRouter(t)

	tests := []struct {
		name string
		body string
	}{
		{"missing url", `{}`},
		{"empty url", `{"url":""}`},
		{"non-http scheme", `{"url":"ftp://example.com"}`},
		{"relative url", `{"url":"/docs"}`},
		{"maxPage zero", `{"url":"` + site.URL + `","maxPage":0}`},
		{"maxPage negative", `{"url":"` + site.URL + `","maxPage":-1}`},
		{"maxPage wrong type", `{"url":"` + site.URL + `","maxPage":"abc"}`},
		// The case above and the one below block **different** things; both must stay.
		// When the type error is on maxPage, encoding/json leaves MaxPage as a
		// **non-nil zero-value pointer** (verified: err non-empty, but
		// maxPage=non-nil -> 0), so Submit's own *maxPage<=0 branch returns the
		// same 400+FF0003 — deleting the handler's ShouldBindJSON error branch
		// entirely would go undetected. Only when "URL decodes fine but
		// **another** field has a type error" is MaxPage nil: ignoring err would
		// really Submit a valid URL and return 200 (verified under the mutant:
		// 200 + hpa_ taskId). This is the only input shape that pins the branch.
		{"selector wrong type", `{"url":"` + site.URL + `","selector":123}`},
		{"invalid json", `{not json`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := doJSON(t, r, http.MethodPost, "/v1/htmlparse", tt.body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body=%s", w.Code, w.Body.String())
			}
			var e struct {
				DiagnosisCode string `json:"diagnosisCode"`
				Message       string `json:"message"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
				t.Fatalf("error response not JSON: %v (%s)", err, w.Body.String())
			}
			if e.Message == "" {
				t.Errorf("error message must be non-empty (design §5 requires a specific reason)")
			}
			// Asserting only 400 is not enough: swapping the dcode keeps the
			// status 400 and the assertion passes. Design §5 requires FF0003 for
			// all parameter errors; downstream diagnosisCode routing relies on it.
			if e.DiagnosisCode != diagnosis.ErrorParamCheckDiagnosisCode {
				t.Errorf("diagnosisCode = %q, want %q", e.DiagnosisCode, diagnosis.ErrorParamCheckDiagnosisCode)
			}
		})
	}
}

// TestHandleHTMLParse_TooManyTasks429 covers design §5: live task count over limit → 429 + FF000D.
//
// The value of this case is walking the handler's error-mapping branch **end to
// end**: the service layer's DiagnosisException must be written out verbatim as
// 429 + FF000D via abortWithDiagnosis's errors.As branch. Task 10 only verified
// isTooManyRequests at the store layer; if the handler fell back to 500 for
// this error, store tests stay green while the live response is entirely wrong.
//
// MaxTasks down to 1, triggered in two steps; the two URLs must differ, else dedup merges the POSTs into one task.
func TestHandleHTMLParse_TooManyTasks429(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // stall the entry page so the task stays running → occupies a live slot
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><p>stalled</p></body></html>`))
	}))
	// defers are LIFO: close(release) runs first to unblock the stalled request,
	// then srv.Close() avoids waiting for the httptest timeout.
	defer srv.Close()
	defer close(release)

	cfg := config.HTMLCrawlConfig{
		Concurrency: 4, MaxPages: 200, HardMaxPages: 3,
		PageTimeout: 5, TaskTimeout: 5,
		MaxHTMLSizeMB: 10, MaxTaskBytesMB: 200,
		UserAgent: "test", TaskTTLSeconds: 60, MaxTasks: 1,
	}
	r := newHTMLParseTestRouterWithCfg(t, cfg)

	w1 := doJSON(t, r, http.MethodPost, "/v1/htmlparse", `{"url":"`+srv.URL+`/a"}`)
	if w1.Code != http.StatusOK {
		t.Fatalf("first submit status = %d, want 200; body=%s", w1.Code, w1.Body.String())
	}

	w2 := doJSON(t, r, http.MethodPost, "/v1/htmlparse", `{"url":"`+srv.URL+`/b"}`)
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("second submit status = %d, want 429; body=%s", w2.Code, w2.Body.String())
	}
	var e struct {
		DiagnosisCode string `json:"diagnosisCode"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &e); err != nil {
		t.Fatalf("429 response not JSON: %v (%s)", err, w2.Body.String())
	}
	if e.DiagnosisCode != diagnosis.ErrorTooManyRequestsDiagnosisCode {
		t.Errorf("diagnosisCode = %q, want %q (FF000D)", e.DiagnosisCode, diagnosis.ErrorTooManyRequestsDiagnosisCode)
	}
}

// TestHandleHTMLParse_MaxPageClamped: design §2.1 — over the hard limit is
// clamped, returning 200 not 400, and **the clamp must actually take effect**:
// asserting only 200 is a no-op — deleting the clamp entirely still returns 200.
//
// Must use a local site: this case really goes through Submit and starts a
// crawl goroutine; with URL https://example.com the unit test would issue real
// requests to the public internet (and hang to timeout on CI without egress).
// The site must have more pages than the router's HardMaxPages(=3): otherwise
// clamped or not, the same number of pages is fetched and the assertion proves nothing.
func TestHandleHTMLParse_MaxPageClamped(t *testing.T) {
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

	r := newHTMLParseTestRouter(t) // this router's HardMaxPages = 3
	w := doJSON(t, r, http.MethodPost, "/v1/htmlparse",
		`{"url":"`+srv.URL+`/","maxPage":99999}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (clamp, not error); body=%s", w.Code, w.Body.String())
	}
	var sub struct {
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &sub); err != nil {
		t.Fatalf("submit response not JSON: %v (%s)", err, w.Body.String())
	}

	// Wait for the terminal state, then assert the clamped result (POST only returns taskId; page count needs GET).
	deadline := time.Now().Add(10 * time.Second)
	for {
		gw := doJSON(t, r, http.MethodGet, "/v1/htmlparse/"+sub.TaskID, "")
		var task struct {
			Status    string `json:"status"`
			Pages     int    `json:"pages"`
			Truncated bool   `json:"truncated"`
		}
		if err := json.Unmarshal(gw.Body.Bytes(), &task); err != nil {
			t.Fatalf("GET response not JSON: %v (%s)", err, gw.Body.String())
		}
		switch task.Status {
		case string(htmld.CrawlSuccess):
			if task.Pages != 3 {
				t.Errorf("pages = %d, want 3: maxPage=99999 must be clamped to HardMaxPages=3", task.Pages)
			}
			if !task.Truncated {
				t.Error("truncated = false, want true: a task truncated by the page limit must set truncated")
			}
			return
		case string(htmld.CrawlFailed), string(htmld.CrawlCancelled):
			t.Fatalf("terminal status = %s, want success", task.Status)
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s did not reach a terminal state within 10s", sub.TaskID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHandleHTMLParse_UnknownTask404(t *testing.T) {
	r := newHTMLParseTestRouter(t)

	// Asserting only 404 is not enough: replacing both ErrorNotFoundDiagnosisCode
	// with ErrorUnknownDiagnosisCode (FF0002 → FF0004) keeps the status 404 and
	// the assertion passes (audit verified: all tests green). Design §2.2/§2.3
	// requires FF0002 for unknown taskId; downstream diagnosisCode routing uses
	// it to separate "task not found" from "unknown error" — the caller's
	// handling differs completely (wait for TTL or fix the ID vs report an
	// incident). This file asserts dcode for 400 and 429; 404 is no exception.
	for _, tc := range []struct{ name, method string }{
		{"GET", http.MethodGet},
		{"DELETE", http.MethodDelete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := doJSON(t, r, tc.method, "/v1/htmlparse/hpa_nope", "")
			if w.Code != http.StatusNotFound {
				t.Fatalf("%s unknown status = %d, want 404; body=%s", tc.name, w.Code, w.Body.String())
			}
			var e struct {
				DiagnosisCode string `json:"diagnosisCode"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
				t.Fatalf("404 response not JSON: %v (%s)", err, w.Body.String())
			}
			if e.DiagnosisCode != diagnosis.ErrorNotFoundDiagnosisCode {
				t.Errorf("diagnosisCode = %q, want %q (FF0002)", e.DiagnosisCode, diagnosis.ErrorNotFoundDiagnosisCode)
			}
		})
	}
}

// TestHandleHTMLParse_Cancel204: design §2.3 — DELETE cancels a **running** task and clears its payload.
//
// Must use a **blocking site**. If the site can finish before the DELETE
// (`crawlTestSite` has only two pages and often completes instantly), DELETE
// takes the "terminal idempotent" branch — by contract it **does not modify
// existing results**, so the status is success, not cancelled, and asserting
// cancelled fails randomly. "Cancelling a still-running task" is §2.3's main
// path, and only a blocking site tests it deterministically.
//
// Asserting only 204 is likewise not enough: making `cancel` "do nothing, return 204" would also pass.
func TestHandleHTMLParse_Cancel204(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // stall the entry page so the task stays running
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><p>stalled</p></body></html>`))
	}))
	// defers are LIFO: close(release) runs first to unblock the stalled request,
	// then srv.Close() avoids waiting for the httptest timeout.
	defer srv.Close()
	defer close(release)

	cfg := config.HTMLCrawlConfig{
		Concurrency: 4, MaxPages: 200, HardMaxPages: 3,
		PageTimeout: 5, TaskTimeout: 5,
		MaxHTMLSizeMB: 10, MaxTaskBytesMB: 200,
		UserAgent: "test", TaskTTLSeconds: 60, MaxTasks: 20,
	}
	r := newHTMLParseTestRouterWithCfg(t, cfg)

	w := doJSON(t, r, http.MethodPost, "/v1/htmlparse", `{"url":"`+srv.URL+`/"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("submit status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var sub struct {
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &sub); err != nil {
		t.Fatalf("POST response not JSON: %v (%s)", err, w.Body.String())
	}

	dw := doJSON(t, r, http.MethodDelete, "/v1/htmlparse/"+sub.TaskID, "")
	if dw.Code != http.StatusNoContent {
		t.Errorf("DELETE status = %d, want 204; body=%s", dw.Code, dw.Body.String())
	}

	// After cancel the task is still in the table (TTL not reached), but must be
	// cancelled with its payload cleared — that is §2.3's promise.
	gw := doJSON(t, r, http.MethodGet, "/v1/htmlparse/"+sub.TaskID, "")
	if gw.Code != http.StatusOK {
		t.Fatalf("GET after cancel status = %d, want 200; body=%s", gw.Code, gw.Body.String())
	}
	var got map[string]interface{}
	if err := json.Unmarshal(gw.Body.Bytes(), &got); err != nil {
		t.Fatalf("GET response not JSON: %v (%s)", err, gw.Body.String())
	}
	if got["status"] != string(htmld.CrawlCancelled) {
		t.Errorf("status = %v, want %q (cancelling a running task must land on cancelled)", got["status"], htmld.CrawlCancelled)
	}
	if md, ok := got["markdown"].(string); !ok || md != "" {
		t.Errorf("cancelled task must discard markdown, got %v", got["markdown"])
	}
	// Note the `, ok` assertion form: a direct `.(float64)` / `.([]interface{})`
	// panics on a missing field (interface {} is nil), misreporting "field
	// absent" as a test crash instead of a clean failure.
	if items, ok := got["items"].([]interface{}); !ok || len(items) != 0 {
		t.Errorf("cancelled task must discard items, got %v", got["items"])
	}
	if pages, ok := got["pages"].(float64); !ok || pages != 0 {
		t.Errorf("cancelled task must reset pages, got %v", got["pages"])
	}

	// Second DELETE is idempotent
	dw2 := doJSON(t, r, http.MethodDelete, "/v1/htmlparse/"+sub.TaskID, "")
	if dw2.Code != http.StatusNoContent {
		t.Errorf("second DELETE status = %d, want 204", dw2.Code)
	}
}

// TestHandleHTMLParse_SubmitDedupReturnsSameTaskID: design §2.1 — when a live
// task (pending/running) exists for the same normalized URL, the existing taskId is returned.
//
// Must use a **blocking** site: with an instantly-returning fake site, the
// first crawl may finish between the two POSTs (this test's HardMaxPages is
// small and pages are a few bytes), so the second POST would legitimately
// create a new task with a different taskId and the test fails randomly.
// Stalling the entry page guarantees the task is live across both POSTs.
func TestHandleHTMLParse_SubmitDedupReturnsSameTaskID(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // stall the entry-page request so the task stays running
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><p>stalled</p></body></html>`))
	}))
	// defers are LIFO: close(release) runs first to unblock the stalled request,
	// then srv.Close() avoids waiting for the httptest timeout.
	defer srv.Close()
	defer close(release)

	r := newHTMLParseTestRouter(t)
	body := `{"url":"` + srv.URL + `/"}`
	w1 := doJSON(t, r, http.MethodPost, "/v1/htmlparse", body)
	w2 := doJSON(t, r, http.MethodPost, "/v1/htmlparse", body)

	var a, b struct {
		TaskID string `json:"taskId"`
	}
	_ = json.Unmarshal(w1.Body.Bytes(), &a)
	_ = json.Unmarshal(w2.Body.Bytes(), &b)
	if a.TaskID == "" || b.TaskID == "" {
		t.Fatalf("missing taskId: %q / %q", a.TaskID, b.TaskID)
	}
	if a.TaskID != b.TaskID {
		t.Errorf("dedup returned different ids: %q vs %q", a.TaskID, b.TaskID)
	}
}
