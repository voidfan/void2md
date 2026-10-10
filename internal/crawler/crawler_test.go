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

// site 用 httptest 起一个假站点：/pageN 互链成一条链，另加若干特殊页。
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
		"/":  page("/a", "/b", "/a"), // /a 重复出现
		"/a": page("/", "/b"),        // 回指入口
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

// TestCrawl_CrossDomainNotFollowed 覆盖设计 §3.3 同域判定：跨站链接必须不入队。
//
// **构造难点（不要退回成两个 httptest server）：** httptest 起的 server 都在
// 127.0.0.1，仅端口不同；而本实现的 SameHost 忽略端口、且 IP 字面量退化为
// host 全等 —— 所以「两个 httptest server」按规则是**同域**，那样构造的用例
// 无论加不加过滤都过不了（加了 → 仍被跟随 → 2 页；不加 → 也是 2 页）。
//
// 正确构造：利用 localhost 与 127.0.0.1 的注册域不同（publicsuffix 对
// "localhost" 解析失败，退化为 host 全等比较），但两者指向同一个 server：
//   - 入口用 http://localhost:PORT/
//   - 页面链接指向 http://127.0.0.1:PORT/other（同一 server，但判为跨站）
//
// 鉴别力：加了过滤 → 链接被拒 → 1 页；漏了过滤 → 真能抓到 → 2 页。
func TestCrawl_CrossDomainNotFollowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/" {
			// 用请求自身的 Host 端口拼出对端地址。**不要**改成在测试 goroutine 里
			// 事后写一个 port 变量、再由 handler 读它：那是无同步边的数据竞争，
			// `go test -race` 会报。r.Host 里天然带着端口。
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

	// 可达性探测：localhost 必须能回连同一 server，否则本用例无鉴别力。
	// 探测失败时 Skip 而非放行，避免「静默空过」的假绿。带超时，
	// 否则 localhost 不通时探测会挂住整个测试。
	probeClient := &http.Client{Timeout: 3 * time.Second}
	probe, err := probeClient.Get("http://localhost:" + port + "/other")
	if err != nil {
		t.Skipf("localhost 无法回连测试服务器（%v），本用例在此环境无鉴别力", err)
	}
	probe.Body.Close()

	res, err := Crawl(context.Background(), defaultOpts("http://localhost:"+port+"/"), nil)
	if err != nil {
		t.Fatalf("Crawl error: %v", err)
	}
	if len(res.Pages) != 1 {
		t.Fatalf("pages = %d, want 1（跨站链接不得入队）; urls=%v",
			len(res.Pages), pageURLs(res))
	}
}

// pageURLs 便于失败时打印实际抓到的 URL。
func pageURLs(res *Result) []string {
	out := make([]string, 0, len(res.Pages))
	for _, p := range res.Pages {
		out = append(out, p.URL)
	}
	return out
}

// TestCrawl_SubPageFailuresDoNotAbort 子页 404 / 非 HTML / 空内容都只跳过，
// 不影响入口页成功，也不影响任务终态。
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
	if len(res.Pages) != 2 { // "/" 与 "/ok"
		t.Fatalf("pages = %d, want 2", len(res.Pages))
	}
	if res.Truncated {
		t.Errorf("Truncated = true, want false")
	}
}

// TestCrawl_MaxPageTruncates 覆盖设计 §2.2 提前停止成因 1。
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

// TestCrawl_TaskTimeoutKeepsPages 覆盖设计 §4.3：超时保留已抓页面并置 truncated。
func TestCrawl_TaskTimeoutKeepsPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(page("/slow")))
		case "/slow":
			time.Sleep(2 * time.Second) // 超过任务预算
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(page()))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	opts := defaultOpts(srv.URL + "/")
	// 1s 而非 300ms：这 1s 必须容得下入口页的一次 localhost 往返。
	// 若卡得太紧，CI 负载高时入口页就会因超时被判失败、pages=0，
	// 测试报出误导性的 "entry page failed"，鉴别力却没有提升。
	// /slow 睡 2s，仍远大于预算，鉴别力不变。
	opts.TaskTimeout = 1 * time.Second

	res, err := Crawl(context.Background(), opts, nil)
	if err != nil {
		t.Fatalf("Crawl error: %v", err)
	}
	if len(res.Pages) != 1 {
		t.Fatalf("pages = %d, want 1 (已抓页面必须保留)", len(res.Pages))
	}
	if !res.Truncated {
		t.Errorf("Truncated = false, want true")
	}
}

// TestCrawl_MaxTaskBytesTruncates 内存保护上限。
func TestCrawl_MaxTaskBytesTruncates(t *testing.T) {
	big := "<html><body><p>" + strings.Repeat("字", 5000) + "</p></body></html>"
	srv := site(t, map[string]string{
		"/":  page("/a", "/b"),
		"/a": big,
		"/b": big,
	})
	opts := defaultOpts(srv.URL + "/")
	opts.MaxTaskBytes = 100 // 远小于单页 markdown

	res, err := Crawl(context.Background(), opts, nil)
	if err != nil {
		t.Fatalf("Crawl error: %v", err)
	}
	if !res.Truncated || res.StopReason != StopMaxBytes {
		t.Errorf("Truncated=%v StopReason=%q, want true/StopMaxBytes", res.Truncated, res.StopReason)
	}
}

// TestCrawl_EntryFailures 覆盖设计 §2.2 终态判定：抓到 0 页 → error，原因具体。
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

	// 超时且一页都没抓到 → failed，且 error 必须说明是超时（设计 §2.2）。
	// 这是「超时本身不直接导致 failed，但 0 页时因规则 2 落 failed」的唯一路径。
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

// TestCrawl_ProgressCallback 运行中 pages 计数（设计 §2.2 的进度信号）。
//
// 断言的是**序列**而不只是终值。只校验终值的话，「把回调从 emit 里挪到 Crawl
// 返回前调用一次」这个变异会存活——而设计 §2.2 承诺非终态时 pages 是**唯一**
// 的进度信号：那样一个跑 5 分钟的任务会让调用方始终看到 pages=0，误判为卡死。
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

// TestCrawl_TitleFallback title 三级回退：<title> → 首个 <h1> → URL。
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

// TestCrawl_LinksComeFromWholeDocument 钉死设计 §4.2 第 10 步「链接必须从**整个文档**
// 提取」：导航栏里的分页链接往往在被选中的正文子树**之外**，只从裁剪后的子树取链接
// 就永远发现不了它们。
//
// **为什么必须单独补这一条（计划里的用例对它零鉴别力）**：计划中所有 fixture 都是
// `Selector="body"`，且链接全写在 `<body>` 里 —— 「从 doc 取」与「从重新解析的
// scoped 子树取」产出的链接集合完全相同。实测：把 ExtractLinks 的输入从 doc 换成
// 重新解析的 scoped（变异体 m_links_from_subtree），计划里的全部用例仍全绿。
//
// 本用例把导航链接放在 `main` 之外（真实分页导航的形态），并**正向**断言两侧的
// 链接都被发现，这样「不取链接」与「只取子树链接」都过不去。
func TestCrawl_LinksComeFromWholeDocument(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			// <nav> 在正文子树 <main> 之外；<main> 内另有一条链接。
			_, _ = w.Write([]byte(`<html><body><nav><a href="/nav-next">下一页</a></nav>` +
				`<main><p>正文</p><a href="/in-main">正文内链接</a></main></body></html>`))
		default:
			_, _ = w.Write([]byte(`<html><body><main><p>leaf</p></main></body></html>`))
		}
	}))
	defer srv.Close()

	opts := defaultOpts(srv.URL + "/")
	opts.Selector = "main" // 正文子树：<nav> 不在其中

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
			t.Errorf("page %s missing; 链接必须从整个文档提取（含 selector 之外的导航），子树内的链接同样不能漏; urls=%v",
				u, pageURLs(res))
		}
	}
	if len(res.Pages) != len(want) {
		t.Errorf("pages = %d, want %d; urls=%v", len(res.Pages), len(want), pageURLs(res))
	}
}

// TestCrawl_EmptyContentMessages 钉死空页规则（设计 §4.2 第 7 步）的**两条不同**错误文本：
// "matched nothing"（selector 未命中任何元素）与 "produced no text"（命中但正文为空）。
//
// 为什么需要：`len(scoped) == 0` 那一支整个删掉（变异体 m_empty_scoped_nocheck）后，
// 未命中的页面会落进后面的 TrimSpace 分支、报出另一条文本；而计划里的用例只断言两条
// 文本共有的 "empty content" 前缀与 selector 回显，于是「选择器写错」与「页面正文为空」
// 在错误文本上被抹平，排查时无法区分。这里对两条文本各做一次正向断言。
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
			t.Errorf("error = %q, want the %q wording (selector 未命中任何元素)", err, "matched nothing")
		}
	})

	t.Run("selector matches but no text", func(t *testing.T) {
		srv := site(t, map[string]string{"/": "<html><body>   </body></html>"})
		_, err := Crawl(context.Background(), defaultOpts(srv.URL+"/"), nil)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "produced no text") {
			t.Errorf("error = %q, want the %q wording (命中但正文为空)", err, "produced no text")
		}
		if strings.Contains(err.Error(), "matched nothing") {
			t.Errorf("error = %q: 命中了 body 却报 matched nothing，两条成因被混为一谈", err)
		}
	})
}

// TestCrawl_MarkdownNotDoubleDecoded 钉死「字节已在 Fetch 阶段解码为 UTF-8，因此必须以
// "utf-8" 调用解析器」这条约定（crawler.go 里唯一的编码相关约束）。
//
// 构造：页面自带一个**撒谎的** <meta charset="gbk">（HTTP 头声明 utf-8，字节其实是
// UTF-8）。selector 取 "html" 让 <head> 里的 meta 进入被解析的子树 —— 若把 charset
// hint 传成 ""，解析器会按 <meta> 把已解码的 UTF-8 字节再按 GBK 解一遍，中文变成乱码
// （「中文正文内容」→「涓枃姝ｆ枃鍐呭」），故此处必须**正向**断言原文出现：
// 负向断言（不含乱码）在此没有意义，用错的解码器本来也解不出「中文正文内容」。
func TestCrawl_MarkdownNotDoubleDecoded(t *testing.T) {
	const body = `<html><head><meta charset="gbk"><title>T</title></head>` +
		`<body><p>中文正文内容</p></body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	opts := defaultOpts(srv.URL + "/")
	opts.Selector = "html" // 让 <head> 里的 meta 留在被解析的子树内

	res, err := Crawl(context.Background(), opts, nil)
	if err != nil {
		t.Fatalf("Crawl error: %v", err)
	}
	if len(res.Pages) != 1 {
		t.Fatalf("pages = %d, want 1; urls=%v", len(res.Pages), pageURLs(res))
	}
	if md := res.Pages[0].Markdown; !strings.Contains(md, "中文正文内容") {
		t.Errorf("markdown = %q, want it to contain 中文正文内容 (未被 <meta charset=gbk> 二次解码)", md)
	}
}

// TestCrawl_OptionFallbacks 钉死 Crawl 内部的三条参数兜底（Options 自身不做校验，
// 见 types.go：MaxPage <=0 视为 1、Concurrency <=0 视为 4、TaskTimeout <=0 视为 5 分钟）。
//
// **为什么必须补**：这三条分支此前没有任何用例，把任一条整个删掉（变异体
// m_maxpage_nofallback / m_concurrency_serial / m_tasktimeout_nofallback）全绿。
// 而调用方（service 层）完全可以把 0 当「没配」传下来：
//   - MaxPage 兜底被删 → 0 直接命中「页数已达上限」，调用方拿到 0 页的失败结果；
//   - TaskTimeout 兜底被删 → context.WithTimeout(ctx, 0) 立刻到期，同样 0 页失败；
//   - Concurrency 兜底被删 → 容量 0 的 channel 上谁都拿不到令牌，整批页面全部
//     挂到任务预算耗尽（这条用例把预算压到 3s，失败时 3s 内收敛，不会挂住）。
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
			t.Errorf("pages = %d, want 1 (MaxPage<=0 兜底为 1)", len(res.Pages))
		}
	})

	t.Run("Concurrency 0 falls back to 4", func(t *testing.T) {
		opts := defaultOpts(srv.URL + "/")
		opts.Concurrency = 0
		opts.TaskTimeout = 3 * time.Second // 兜底生效时远用不到，失效时在此收敛
		res, err := Crawl(context.Background(), opts, nil)
		if err != nil {
			t.Fatalf("Crawl error: %v (Concurrency<=0 必须兜底，否则无人能拿到令牌)", err)
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
			t.Fatalf("Crawl error: %v (TaskTimeout<=0 必须兜底，而不是立刻到期)", err)
		}
		if len(res.Pages) != 2 {
			t.Errorf("pages = %d, want 2", len(res.Pages))
		}
		if res.Truncated {
			t.Errorf("Truncated = true, want false (兜底后的预算不该被判定为提前停止)")
		}
	})
}

// TestCrawl_LinksFromDiscardedPagesStillEnqueued 钉住「链接入队与页面是否有效**无关**」。
//
// 为什么需要：`process()` 在「selector 未命中」「正文为空」这些**页面被判空丢弃**的
// 路径上照样返回它已抽出的链接（crawler.go 里那几处 `return Page{}, links, err`），
// 而调用方在 `if r.err != nil { ...; continue }` 里把这些链接一起丢掉了——那个
// `links` 返回值因此是死的。受害场景恰是设计 §4.2 第 10 步存在的理由：**导航页 /
// 聚合页**常常自己渲染不出正文，却链接着整棵子树。判空即止会让整棵子树抓不到，
// 任务还报 success。
func TestCrawl_LinksFromDiscardedPagesStillEnqueued(t *testing.T) {
	// /hub 没有任何 <main>（selector 未命中 → 被丢弃），但它链到 /leaf；
	// /leaf 有 <main>，能渲染出正文。
	srv := site(t, map[string]string{
		"/hub":  `<html><body><nav><a href="/leaf">leaf</a></nav><p>hub</p></body></html>`,
		"/leaf": `<html><body><main><p>leaf 正文</p></main></body></html>`,
	})
	opts := defaultOpts(srv.URL + "/hub")
	opts.Selector = "main"

	res, err := Crawl(context.Background(), opts, nil)
	if err != nil {
		t.Fatalf("Crawl error = %v；/hub 被丢弃但链到了 /leaf，/leaf 必须仍被抓到", err)
	}
	got := map[string]bool{}
	for _, u := range pageURLs(res) {
		got[u] = true
	}
	if !got[srv.URL+"/leaf"] {
		t.Errorf("/leaf 缺失：被丢弃页面上的链接也必须入队（设计 §4.2 第 10 步）；urls=%v", pageURLs(res))
	}
	if len(res.Pages) != 1 {
		t.Errorf("pages = %d, want 1（只有 /leaf 有效）；urls=%v", len(res.Pages), pageURLs(res))
	}
}

// TestCrawl_MaxTaskBytesBeforeAnyPageMessage 钉住「字节预算在第一页就挡下」时的错误成因。
//
// 为什么需要：命中预算时 `stop = StopMaxBytes` 在 `pages = append(...)` **之前** break，
// 于是 pages 保持 0，落进 0 页终态分支；而该分支只特判 `StopTimeout`，其余一律报
// `entry page ... produced no content`。运维看到这句话会去查站点，而真正的原因是
// `PDF2TEXT_HTML_MAX_TASK_BYTES_MB` 配小了——成因词从未出现在 error 或日志里，
// 与设计 §2.2「error 携带具体底层原因」不符。
func TestCrawl_MaxTaskBytesBeforeAnyPageMessage(t *testing.T) {
	srv := site(t, map[string]string{"/": page()})
	opts := defaultOpts(srv.URL + "/")
	opts.MaxTaskBytes = 1 // 比任何一页的 markdown 都小：第一页就被挡下，pages 保持 0

	_, err := Crawl(context.Background(), opts, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "PDF2TEXT_HTML_MAX_TASK_BYTES_MB") {
		t.Errorf("error = %q，必须点名字节预算这条成因（含 PDF2TEXT_HTML_MAX_TASK_BYTES_MB）", err)
	}
}
