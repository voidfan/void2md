package crawler

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// site stands up a fake site with httptest: /pageN interlinked as a chain, plus special pages.
func site(t *testing.T, pages map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func page(links ...string) string {
	var b strings.Builder
	b.WriteString("<html><head><title>T</title></head><body>")
	for _, l := range links {
		fmt.Fprintf(&b, `<a href="%s">link</a>`, l)
	}
	b.WriteString("<p>正文</p></body></html>")
	return b.String()
}

func defaultOpts(entry string) Options {
	return Options{
		URL:         entry,
		Selector:    "body",
		MaxPage:     200,
		Concurrency: 4,
		PageTimeout: 5 * time.Second,
		TaskTimeout: 30 * time.Second,
		MaxHTMLSize: 10 << 20,
		UserAgent:   "test",
	}
}

func TestCrawl_ThreePagesDedup(t *testing.T) {
	srv := site(t, map[string]string{
		"/":  page("/a", "/b", "/a"), // /a appears twice
		"/a": page("/", "/b"),        // links back to entry
		"/b": page("/"),
	})
	res, err := Crawl(context.Background(), defaultOpts(srv.URL+"/"), nil)
	if err != nil {
		t.Fatalf("Crawl error: %v", err)
	}
	if len(res.Pages) != 3 {
		t.Fatalf("pages = %d, want 3", len(res.Pages))
	}
	if res.Truncated {
		t.Errorf("Truncated = true, want false")
	}
	seen := map[string]bool{}
	for _, p := range res.Pages {
		if seen[p.URL] {
			t.Errorf("duplicate page %q", p.URL)
		}
		seen[p.URL] = true
	}
}

// TestCrawl_CrossDomainNotFollowed covers design §3.3 same-site rule: cross-site links must not be enqueued.
//
// **Construction pitfall (do not fall back to two httptest servers):** httptest
// servers all live on 127.0.0.1 with different ports, while SameHost ignores
// ports and IP literals degrade to host equality — so "two httptest servers"
// are **same-domain** by the rules, and such a test fails with or without the
// filter (with → still followed → 2 pages; without → also 2 pages).
//
// Correct construction: exploit that localhost and 127.0.0.1 have different
// registrable domains (publicsuffix fails to parse "localhost", degrading to
// host equality), while both point at the same server:
//   - entry: http://localhost:PORT/
//   - page links to http://127.0.0.1:PORT/other (same server, but cross-site)
//
// Discriminating power: filter present → link rejected → 1 page; filter missing → actually fetched → 2 pages.
func TestCrawl_CrossDomainNotFollowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/" {
			// Build the peer address from the request's own Host port. **Do not**
			// switch to a port variable written by the test goroutine and read by
			// the handler: that is a data race with no synchronization edge and
			// `go test -race` will flag it. r.Host naturally carries the port.
			_, port, err := net.SplitHostPort(r.Host)
			if err != nil {
				http.Error(w, "bad host", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(page("http://127.0.0.1:" + port + "/other")))
			return
		}
		_, _ = w.Write([]byte(page()))
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse srv.URL: %v", err)
	}
	port := u.Port()

	// Reachability probe: localhost must connect back to the same server, else
	// this case has no discriminating power. Skip (not pass) on probe failure to
	// avoid a silent false green. With timeout, else an unreachable localhost
	// hangs the whole test.
	probeClient := &http.Client{Timeout: 3 * time.Second}
	probe, err := probeClient.Get("http://localhost:" + port + "/other")
	if err != nil {
		t.Skipf("localhost cannot reach the test server (%v); this case has no discriminating power here", err)
	}
	probe.Body.Close()

	res, err := Crawl(context.Background(), defaultOpts("http://localhost:"+port+"/"), nil)
	if err != nil {
		t.Fatalf("Crawl error: %v", err)
	}
	if len(res.Pages) != 1 {
		t.Fatalf("pages = %d, want 1 (cross-site links must not be enqueued); urls=%v",
			len(res.Pages), pageURLs(res))
	}
}

// pageURLs makes failure output show the actually fetched URLs.
func pageURLs(res *Result) []string {
	out := make([]string, 0, len(res.Pages))
	for _, p := range res.Pages {
		out = append(out, p.URL)
	}
	return out
}

// TestCrawl_SubPageFailuresDoNotAbort: child-page 404 / non-HTML / empty content are
// skipped only; the entry page success and task terminal state are unaffected.
func TestCrawl_SubPageFailuresDoNotAbort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(page("/missing", "/pdf", "/empty", "/ok")))
		case "/pdf":
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte("%PDF-1.4"))
		case "/empty":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html><body>   </body></html>"))
		case "/ok":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(page()))
		default:
			http.NotFound(w, r) // /missing
		}
	}))
	defer srv.Close()

	res, err := Crawl(context.Background(), defaultOpts(srv.URL+"/"), nil)
	if err != nil {
		t.Fatalf("Crawl error: %v", err)
	}
	if len(res.Pages) != 2 { // "/" and "/ok"
		t.Fatalf("pages = %d, want 2", len(res.Pages))
	}
	if res.Truncated {
		t.Errorf("Truncated = true, want false")
	}
}

// TestCrawl_MaxPageTruncates covers design §2.2 early-stop cause 1.
func TestCrawl_MaxPageTruncates(t *testing.T) {
	srv := site(t, map[string]string{
		"/":  page("/a", "/b", "/c"),
		"/a": page(),
		"/b": page(),
		"/c": page(),
	})
	opts := defaultOpts(srv.URL + "/")
	opts.MaxPage = 2

	res, err := Crawl(context.Background(), opts, nil)
	if err != nil {
		t.Fatalf("Crawl error: %v", err)
	}
	if len(res.Pages) != 2 {
		t.Fatalf("pages = %d, want exactly 2", len(res.Pages))
	}
	if !res.Truncated || res.StopReason != StopMaxPage {
		t.Errorf("Truncated=%v StopReason=%q, want true/StopMaxPage", res.Truncated, res.StopReason)
	}
}

// TestCrawl_TaskTimeoutKeepsPages covers design §4.3: timeout keeps fetched pages and sets truncated.
func TestCrawl_TaskTimeoutKeepsPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(page("/slow")))
		case "/slow":
			time.Sleep(2 * time.Second) // exceeds the task budget
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(page()))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	opts := defaultOpts(srv.URL + "/")
	// 1s rather than 300ms: the 1s must fit one localhost round-trip for the
	// entry page. Too tight, and under heavy CI load the entry page fails by
	// timeout with pages=0, producing a misleading "entry page failed" with no
	// gain in discriminating power. /slow sleeps 2s, still far over budget.
	opts.TaskTimeout = 1 * time.Second

	res, err := Crawl(context.Background(), opts, nil)
	if err != nil {
		t.Fatalf("Crawl error: %v", err)
	}
	if len(res.Pages) != 1 {
		t.Fatalf("pages = %d, want 1 (already-fetched pages must be kept)", len(res.Pages))
	}
	if !res.Truncated {
		t.Errorf("Truncated = false, want true")
	}
}

// TestCrawl_MaxTaskBytesTruncates: memory-protection byte limit.
func TestCrawl_MaxTaskBytesTruncates(t *testing.T) {
	big := "<html><body><p>" + strings.Repeat("字", 5000) + "</p></body></html>"
	srv := site(t, map[string]string{
		"/":  page("/a", "/b"),
		"/a": big,
		"/b": big,
	})
	opts := defaultOpts(srv.URL + "/")
	opts.MaxTaskBytes = 100 // far below one page's markdown

	res, err := Crawl(context.Background(), opts, nil)
	if err != nil {
		t.Fatalf("Crawl error: %v", err)
	}
	if !res.Truncated || res.StopReason != StopMaxBytes {
		t.Errorf("Truncated=%v StopReason=%q, want true/StopMaxBytes", res.Truncated, res.StopReason)
	}
}

// TestCrawl_EntryFailures covers design §2.2 terminal state: 0 pages → error with a specific reason.
func TestCrawl_EntryFailures(t *testing.T) {
	t.Run("entry 404", func(t *testing.T) {
		srv := site(t, map[string]string{})
		_, err := Crawl(context.Background(), defaultOpts(srv.URL+"/x"), nil)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "http status 404") {
			t.Errorf("error = %q, want mention of http status 404", err)
		}
	})

	t.Run("entry selector no match", func(t *testing.T) {
		srv := site(t, map[string]string{"/": page()})
		opts := defaultOpts(srv.URL + "/")
		opts.Selector = ".does-not-exist"
		_, err := Crawl(context.Background(), opts, nil)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "empty content") {
			t.Errorf("error = %q, want mention of empty content", err)
		}
		if !strings.Contains(err.Error(), ".does-not-exist") {
			t.Errorf("error = %q, want the selector echoed", err)
		}
	})

	t.Run("entry non-html", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("hello"))
		}))
		defer srv.Close()
		_, err := Crawl(context.Background(), defaultOpts(srv.URL), nil)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "is not html") {
			t.Errorf("error = %q, want mention of non-html", err)
		}
	})

	// Timeout with zero pages → failed, and the error must say it was a timeout (design §2.2).
	// This is the only path where "timeout alone doesn't cause failed, but 0 pages makes it failed via rule 2".
	t.Run("timeout with zero pages reports timeout", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(2 * time.Second)
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(page()))
		}))
		defer srv.Close()

		opts := defaultOpts(srv.URL + "/")
		opts.TaskTimeout = 200 * time.Millisecond
		_, err := Crawl(context.Background(), opts, nil)
		if err == nil {
			t.Fatal("expected error when timeout leaves 0 pages")
		}
		if !strings.Contains(err.Error(), "timed out") {
			t.Errorf("error = %q, want mention of timeout", err)
		}
	})
}

func TestCrawl_InvalidEntryURL(t *testing.T) {
	_, err := Crawl(context.Background(), defaultOpts("ftp://example.com"), nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported scheme") {
		t.Errorf("error = %v, want unsupported scheme", err)
	}
}

// TestCrawl_ProgressCallback: running pages count (the design §2.2 progress signal).
//
// Asserts the **sequence**, not just the final value. Checking only the final
// value lets the mutation "move the callback out of emit into one call before
// Crawl returns" survive — and design §2.2 promises pages is the **only**
// progress signal for non-terminal states: a 5-minute task would keep showing
// pages=0 and be mistaken for hung.
func TestCrawl_ProgressCallback(t *testing.T) {
	srv := site(t, map[string]string{
		"/":  page("/a", "/b"),
		"/a": page(),
		"/b": page(),
	})
	var seq []int
	res, err := Crawl(context.Background(), defaultOpts(srv.URL+"/"), func(done int) { seq = append(seq, done) })
	if err != nil {
		t.Fatalf("Crawl error: %v", err)
	}
	if len(seq) < 2 {
		t.Fatalf("progress callback fired %d time(s) (%v); must report intermediate values, not only the final one", len(seq), seq)
	}
	for i := 1; i < len(seq); i++ {
		if seq[i] < seq[i-1] {
			t.Fatalf("progress must not decrease, got %v", seq)
		}
	}
	if seq[len(seq)-1] != len(res.Pages) {
		t.Errorf("final progress = %d, want %d", seq[len(seq)-1], len(res.Pages))
	}
	if seq[0] >= seq[len(seq)-1] {
		t.Errorf("no intermediate progress observed, got %v; the callback must fire during the run, not only at the end", seq)
	}
}

// TestCrawl_TitleFallback: three-level title fallback: <title> → first <h1> → URL.
func TestCrawl_TitleFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`<html><body><a href="/a">a</a><a href="/b">b</a><a href="/c">c</a><p>x</p></body></html>`))
		case "/a":
			_, _ = w.Write([]byte(`<html><head><title>FromTitle</title></head><body><h1>FromH1</h1><p>x</p></body></html>`))
		case "/b":
			_, _ = w.Write([]byte(`<html><body><h1>FromH1</h1><p>x</p></body></html>`))
		default:
			_, _ = w.Write([]byte(`<html><body><p>no title no h1</p></body></html>`))
		}
	}))
	defer srv.Close()

	res, err := Crawl(context.Background(), defaultOpts(srv.URL+"/"), nil)
	if err != nil {
		t.Fatalf("Crawl error: %v", err)
	}
	titles := map[string]string{}
	for _, p := range res.Pages {
		titles[p.URL] = p.Title
	}
	if got := titles[srv.URL+"/a"]; got != "FromTitle" {
		t.Errorf("/a title = %q, want FromTitle", got)
	}
	if got := titles[srv.URL+"/b"]; got != "FromH1" {
		t.Errorf("/b title = %q, want FromH1", got)
	}
	if got := titles[srv.URL+"/c"]; got != srv.URL+"/c" {
		t.Errorf("/c title = %q, want the URL itself", got)
	}
}

// TestCrawl_LinksComeFromWholeDocument pins design §4.2 step 10 "links must be
// extracted from the **whole document**": nav-bar pagination links often live
// **outside** the selected content subtree, so extracting only from the scoped
// subtree never discovers them.
//
// **Why this case had to be added (planned cases have zero discriminating
// power for it)**: every planned fixture uses `Selector="body"` with links
// inside `<body>` — "from doc" and "from the re-parsed scoped subtree" yield
// identical link sets. Verified: switching ExtractLinks's input from doc to a
// re-parsed scoped (mutation m_links_from_subtree) keeps all planned cases green.
//
// This case puts a nav link outside `main` (the shape of real pagination navs)
// and **positively** asserts both sides' links are found, so both "no links"
// and "subtree-only links" fail.
func TestCrawl_LinksComeFromWholeDocument(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			// <nav> is outside the content subtree <main>; <main> has its own link.
			_, _ = w.Write([]byte(`<html><body><nav><a href="/nav-next">下一页</a></nav>` +
				`<main><p>正文</p><a href="/in-main">正文内链接</a></main></body></html>`))
		default:
			_, _ = w.Write([]byte(`<html><body><main><p>leaf</p></main></body></html>`))
		}
	}))
	defer srv.Close()

	opts := defaultOpts(srv.URL + "/")
	opts.Selector = "main" // content subtree: <nav> is not in it

	res, err := Crawl(context.Background(), opts, nil)
	if err != nil {
		t.Fatalf("Crawl error: %v", err)
	}
	got := map[string]bool{}
	for _, u := range pageURLs(res) {
		got[u] = true
	}
	want := []string{srv.URL + "/", srv.URL + "/nav-next", srv.URL + "/in-main"}
	for _, u := range want {
		if !got[u] {
			t.Errorf("page %s missing; links must come from the whole document (incl. navs outside the selector), and subtree links must not be missed either; urls=%v",
				u, pageURLs(res))
		}
	}
	if len(res.Pages) != len(want) {
		t.Errorf("pages = %d, want %d; urls=%v", len(res.Pages), len(want), pageURLs(res))
	}
}

// TestCrawl_EmptyContentMessages pins the **two distinct** error texts of the
// empty-page rule (design §4.2 step 7): "matched nothing" (selector hit no
// element) vs "produced no text" (matched but content empty).
//
// Why needed: deleting the whole `len(scoped) == 0` branch (mutation
// m_empty_scoped_nocheck) makes no-match pages fall into the later TrimSpace
// branch and report the other text; planned cases only assert the shared
// "empty content" prefix and the echoed selector, so "wrong selector" and
// "empty page body" become indistinguishable when debugging. This asserts each text positively.
func TestCrawl_EmptyContentMessages(t *testing.T) {
	t.Run("selector matches nothing", func(t *testing.T) {
		srv := site(t, map[string]string{"/": page()})
		opts := defaultOpts(srv.URL + "/")
		opts.Selector = ".does-not-exist"
		_, err := Crawl(context.Background(), opts, nil)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "matched nothing") {
			t.Errorf("error = %q, want the %q wording (selector matched no element)", err, "matched nothing")
		}
	})

	t.Run("selector matches but no text", func(t *testing.T) {
		srv := site(t, map[string]string{"/": "<html><body>   </body></html>"})
		_, err := Crawl(context.Background(), defaultOpts(srv.URL+"/"), nil)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "produced no text") {
			t.Errorf("error = %q, want the %q wording (matched but content empty)", err, "produced no text")
		}
		if strings.Contains(err.Error(), "matched nothing") {
			t.Errorf("error = %q: body was matched yet reports matched nothing; the two causes are conflated", err)
		}
	})
}

// TestCrawl_MarkdownNotDoubleDecoded pins the convention "bytes are already
// decoded to UTF-8 in Fetch, so the parser must be called with \"utf-8\""
// (the only encoding-related constraint in crawler.go).
//
// Construction: the page carries a **lying** <meta charset="gbk"> (HTTP header
// says utf-8 and the bytes really are UTF-8). Selector "html" keeps the <head>
// meta inside the parsed subtree — if the charset hint were passed as "", the
// parser would re-decode the already-UTF-8 bytes as GBK per <meta>, garbling
// the Chinese ("中文正文内容" → "涓枃姝ｆ枃鍐呭"). Hence the **positive**
// assertion that the original text appears: a negative assertion (no mojibake)
// is meaningless here since a wrong decoder also wouldn't produce the original.
func TestCrawl_MarkdownNotDoubleDecoded(t *testing.T) {
	const body = `<html><head><meta charset="gbk"><title>T</title></head>` +
		`<body><p>中文正文内容</p></body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	opts := defaultOpts(srv.URL + "/")
	opts.Selector = "html" // keep the <head> meta inside the parsed subtree

	res, err := Crawl(context.Background(), opts, nil)
	if err != nil {
		t.Fatalf("Crawl error: %v", err)
	}
	if len(res.Pages) != 1 {
		t.Fatalf("pages = %d, want 1; urls=%v", len(res.Pages), pageURLs(res))
	}
	if md := res.Pages[0].Markdown; !strings.Contains(md, "中文正文内容") {
		t.Errorf("markdown = %q, want it to contain 中文正文内容 (not double-decoded per <meta charset=gbk>)", md)
	}
}

// TestCrawl_OptionFallbacks pins Crawl's three internal parameter fallbacks
// (Options itself does no validation, see types.go: MaxPage <=0 → 1,
// Concurrency <=0 → 4, TaskTimeout <=0 → 5 minutes).
//
// **Why needed**: these three branches had no cases; deleting any of them
// (mutations m_maxpage_nofallback / m_concurrency_serial /
// m_tasktimeout_nofallback) stayed green. Callers (the service layer) may well
// pass 0 meaning "not configured":
//   - MaxPage fallback removed → 0 immediately hits "page limit reached", caller gets a 0-page failure;
//   - TaskTimeout fallback removed → context.WithTimeout(ctx, 0) expires instantly, same 0-page failure;
//   - Concurrency fallback removed → nobody gets a token on a capacity-0 channel; the whole batch
//     hangs until the task budget is exhausted (this case sets the budget to 3s, so it converges within 3s).
func TestCrawl_OptionFallbacks(t *testing.T) {
	srv := site(t, map[string]string{
		"/":  page("/a"),
		"/a": page(),
	})

	t.Run("MaxPage 0 falls back to 1", func(t *testing.T) {
		opts := defaultOpts(srv.URL + "/")
		opts.MaxPage = 0
		res, err := Crawl(context.Background(), opts, nil)
		if err != nil {
			t.Fatalf("Crawl error: %v", err)
		}
		if len(res.Pages) != 1 {
			t.Errorf("pages = %d, want 1 (MaxPage<=0 falls back to 1)", len(res.Pages))
		}
	})

	t.Run("Concurrency 0 falls back to 4", func(t *testing.T) {
		opts := defaultOpts(srv.URL + "/")
		opts.Concurrency = 0
		opts.TaskTimeout = 3 * time.Second // far from needed when the fallback works; bounds failure otherwise
		res, err := Crawl(context.Background(), opts, nil)
		if err != nil {
			t.Fatalf("Crawl error: %v (Concurrency<=0 must fall back, else nobody gets a token)", err)
		}
		if len(res.Pages) != 2 {
			t.Errorf("pages = %d, want 2", len(res.Pages))
		}
	})

	t.Run("TaskTimeout 0 falls back to the 5m default", func(t *testing.T) {
		opts := defaultOpts(srv.URL + "/")
		opts.TaskTimeout = 0
		res, err := Crawl(context.Background(), opts, nil)
		if err != nil {
			t.Fatalf("Crawl error: %v (TaskTimeout<=0 must fall back, not expire instantly)", err)
		}
		if len(res.Pages) != 2 {
			t.Errorf("pages = %d, want 2", len(res.Pages))
		}
		if res.Truncated {
			t.Errorf("Truncated = true, want false (the fallback budget should not count as early stop)")
		}
	})
}

// TestCrawl_LinksFromDiscardedPagesStillEnqueued pins "link enqueueing is
// **independent of page validity**".
//
// Why needed: on the "selector missed" / "empty content" paths where the page
// is discarded as empty, `process()` still returns the links it extracted
// (the `return Page{}, links, err` sites in crawler.go), but the caller used
// to drop them in `if r.err != nil { ...; continue }` — making that `links`
// return dead. The victim scenario is exactly why design §4.2 step 10 exists:
// **nav/aggregator pages** often render no content themselves yet link a whole
// subtree. Discarding on empty loses the entire subtree while the task still
// reports success.
func TestCrawl_LinksFromDiscardedPagesStillEnqueued(t *testing.T) {
	// /hub has no <main> (selector misses → discarded) but links to /leaf;
	// /leaf has <main> and renders content.
	srv := site(t, map[string]string{
		"/hub":  `<html><body><nav><a href="/leaf">leaf</a></nav><p>hub</p></body></html>`,
		"/leaf": `<html><body><main><p>leaf 正文</p></main></body></html>`,
	})
	opts := defaultOpts(srv.URL + "/hub")
	opts.Selector = "main"

	res, err := Crawl(context.Background(), opts, nil)
	if err != nil {
		t.Fatalf("Crawl error = %v; /hub is discarded but links to /leaf, so /leaf must still be fetched", err)
	}
	got := map[string]bool{}
	for _, u := range pageURLs(res) {
		got[u] = true
	}
	if !got[srv.URL+"/leaf"] {
		t.Errorf("/leaf missing: links on discarded pages must still be enqueued (design §4.2 step 10); urls=%v", pageURLs(res))
	}
	if len(res.Pages) != 1 {
		t.Errorf("pages = %d, want 1 (only /leaf is valid); urls=%v", len(res.Pages), pageURLs(res))
	}
}

// TestCrawl_MaxTaskBytesBeforeAnyPageMessage pins the error cause when "the byte
// budget blocks at the very first page".
//
// Why needed: when the budget is hit, `stop = StopMaxBytes` breaks **before**
// `pages = append(...)`, so pages stays 0 and falls into the 0-page terminal
// branch; that branch only special-cases `StopTimeout` and otherwise reports
// `entry page ... produced no content`. Ops seeing that would go check the
// site, while the real cause is `PDF2TEXT_HTML_MAX_TASK_BYTES_MB` set too low —
// the cause word never appeared in the error or logs, contradicting design
// §2.2's "error carries the specific underlying cause".
func TestCrawl_MaxTaskBytesBeforeAnyPageMessage(t *testing.T) {
	srv := site(t, map[string]string{"/": page()})
	opts := defaultOpts(srv.URL + "/")
	opts.MaxTaskBytes = 1 // smaller than any page's markdown: blocked at page 1, pages stays 0

	_, err := Crawl(context.Background(), opts, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "PDF2TEXT_HTML_MAX_TASK_BYTES_MB") {
		t.Errorf("error = %q, must name the byte-budget cause (incl. PDF2TEXT_HTML_MAX_TASK_BYTES_MB)", err)
	}
}
