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

// newTestCrawlService 构造测试用抓取服务。
//
// 原计划签名是 newTestCrawlService(t, srv)，但 srv 参数**完全未被使用**
// （t 也只用于 t.Helper()）：留着它会让读代码的人以为「服务端不可达」这类
// 失败被这个参数管住了，实际测试根本不受它影响。已删掉该参数并同步改所有调用点。
func newTestCrawlService(t *testing.T) *HTMLCrawlService {
	t.Helper()
	cfg := testCrawlCfg()
	cfg.TaskTimeout = 5
	cfg.PageTimeout = 5
	return NewHTMLCrawlService(cfg)
}

// waitTerminal 轮询直到任务进入终态。
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
	// 设计 §2.1 承诺的 ID 形态：hpa_ 前缀。
	// 少了这条，把 newTaskID 的前缀改成 zzz_ 是全绿的。
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
	// markdown 必须带来源注释，供下游溯源
	if !strings.Contains(task.Markdown, "<!-- source: "+srv.URL+"/ -->") {
		t.Errorf("markdown missing source comment, got %q", task.Markdown)
	}
}

// TestService_InvalidURL 覆盖 URL 层面的坏输入。
//
// 除了「必须报错」，还断言错误的形状：必须是携带 FF0003 的 DiagnosisException
// （设计 §5：缺 url / scheme 非 http(s) → 400 + FF0003）。只判 err != nil 的话，
// 把诊断码换成任意别的码（甚至换成一个裸 error）仍然是绿的。
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

// TestService_MaxPageNonPositive 覆盖设计 §2.1「maxPage ≤ 0 → 400 + FF0003」。
//
// TestService_InvalidURL 只喂 URL 层面的坏输入，从不传 maxPage，于是服务层
// `if *maxPage <= 0 { return … }` 这一整段是零覆盖的：把整段删掉，pages=0 会被
// crawler.Crawl 兜底成 1（见 internal/crawler/crawler.go 的「参数兜底」），
// 任务正常成功，接口契约（非正数必须 400）被破坏而测试全绿。
//
// URL 用 127.0.0.1:1（保留端口，连接必然被拒）而非真实域名：万一实现没拦住、
// 落到 `go s.run`，只会本地快速失败，不会真的去出网。
func TestService_MaxPageNonPositive(t *testing.T) {
	svc := NewHTMLCrawlService(testCrawlCfg())
	for _, bad := range []int{0, -1, -100} {
		_, deduped, err := svc.Submit("http://127.0.0.1:1/", "", &bad)
		if err == nil {
			t.Errorf("Submit(maxPage=%d) expected error, got nil（非正 maxPage 必须 400）", bad)
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
			t.Errorf("Submit(maxPage=%d) deduped = true, want false（被拒的提交不得命中任务表）", bad)
		}
	}
}

// TestService_DedupAliveTask 覆盖设计 §2.1「同一规范化 url 已有存活任务（pending /
// running）→ 直接返回已有 taskId」。
//
// 站点必须**阻塞**：站点若即时返回，第一个任务可能在第二次 Submit 之前就跑完并翻终态，
// 那时 byURL 已清空、去重本就不该触发，用例会变成随机失败。
//
// 三重断言，缺一不可：
//   - deduped == true：直接删掉 `if deduped { return id, true, nil }` 时，
//     store 仍返回同一个 id、错误仍为 nil，只有这个返回值会变；
//   - id 相同：去重必须复用已有任务；
//   - 站点**没有出现第二个爬取请求**：这是「去重真的省掉了重复出网/重复内存」的
//     正向证据——没有它，一个「返回 true 但照样再起一个 goroutine」的实现是绿的。
func TestService_DedupAliveTask(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	doRelease := func() { releaseOnce.Do(func() { close(release) }) }

	var hits int32
	entered := make(chan struct{}, 8)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		entered <- struct{}{}
		<-release // 卡住请求，保证首个任务在第二次 Submit 时仍然存活
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><p>正文</p></body></html>`))
	}))
	// defer 后进先出：兜底放行必须早于 srv.Close() 执行，否则用例中途 Fatal 时
	// handler 还阻塞在 release 上，srv.Close() 会一直等它。
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

	// 等入口请求真的到达站点，此时首个任务必然存活（阻塞在 handler 里）
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
		t.Error("deduped = false, want true：同一 URL 已有存活任务时必须复用")
	}
	if id2 != id1 {
		t.Errorf("taskId = %q, want %q（去重必须复用已有任务）", id2, id1)
	}

	// 去重必须真的省掉第二次爬取：站点上不应再出现请求。
	// （若实现只改了返回值而照样 `go s.run`，第二个 goroutine 会立刻发出请求。）
	select {
	case <-entered:
		t.Error("second crawl issued a request: 去重后仍起了新的爬取 goroutine")
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
		t.Errorf("server hits = %d, want 1（去重后只应有一次入口请求）", got)
	}
}

// TestService_MaxPageClamped 覆盖设计 §2.1：超过硬上限按 clamp 处理，不报错，
// 且**钳制必须真的生效**。
//
// 只断言「不报错」是空转的：把 clamp 整段删掉，Submit 照样不报错，断言照样通过。
// 要观测钳制，站点必须比硬上限**更多页**——单页站点无论钳不钳制都只抓到 1 页，
// 断言不出任何区别。这里 5 页（入口 + 4 个子页）对 HardMaxPages=3。
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
	// Crawl 在批处理前把本批截断到剩余预算，故恰好产出 MaxPage 页。
	if task.Pages != cfg.HardMaxPages {
		t.Errorf("pages = %d, want %d：maxPage=10000 必须被钳到硬上限（若站点只有 %d 页，本用例观测不到钳制）",
			task.Pages, cfg.HardMaxPages, cfg.HardMaxPages)
	}
	if !task.Truncated {
		t.Error("truncated = false, want true：被页数上限截断的任务必须置 truncated")
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
		<-release // 卡住子页，制造可取消窗口
		_, _ = w.Write([]byte(`<html><body><p>慢页</p></body></html>`))
	}))
	defer srv.Close()
	defer close(release)

	svc := newTestCrawlService(t)
	id, _, err := svc.Submit(srv.URL+"/", "", nil)
	if err != nil {
		t.Fatalf("Submit error: %v", err)
	}

	// 等入口页抓完（pages 出现进度）再取消。两个要点：
	//
	//  1. 观测条件必须是「pages >= 1 **且状态仍是 running**」。只判 pages >= 1
	//     不够：终态转移（finishSuccess）也会写 pages，于是 setProgress 完全没接线时，
	//     该条件会在 TaskTimeout(5s) 到点、任务翻 success 的瞬间被满足，用例照样往下走，
	//     「进度回路坏了」只能靠后面 Status=success 偶然撞到。
	//  2. deadline 必须**明显大于** TaskTimeout。原计划两者都是 5s，Cancel 恰好落在
	//     任务翻终态的瞬间：某些时序下 Cancel 打在已终态任务上（幂等 no-op），
	//     于是「取消语义」这条用例本身变成了计时赛跑。
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
			break // 已翻终态，却从未在 running 期间见过进度信号
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
	// 取消语义：已抓页面必须被丢弃
	if task.Pages != 0 || task.Markdown != "" || len(task.Items) != 0 {
		t.Errorf("cancelled task must discard payload: %+v", task)
	}
}

// TestService_CancelAbortsInFlightRequest 设计 §2.3：pending/running 任务的 DELETE
// 必须真的取消任务级 ctx。
//
// 只断言「状态变 cancelled、载荷被清空」是空转的：把 run 里的
// `s.store.setCancel(taskID, cancel)` 删掉后，store.cancel() 照样把状态置 cancelled、
// 把载荷清空（它是同步完成的），而 ctx 从不被取消——抓取 goroutine 继续跑到
// TaskTimeout（默认 300s），期间持续出网、持续往内存里堆 Markdown 再丢弃，
// 客户端拿到的仍是 204，看不出任何异常。
//
// 这条用例观测的唯一东西就是「在飞请求被真的掐断」：爬取方的 HTTP 请求在 ctx 取消后
// 会断开连接，服务端据此看到 r.Context().Done()。TaskTimeout/PageTimeout 都放到 30s，
// 所以这里若没断，5s 的窗口内绝无可能由超时自然产生。
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
			aborted <- struct{}{} // 爬取方真的断开了连接
		}
	}))
	// defer 后进先出：兜底放行必须早于 srv.Close() 执行，否则用例中途 Fatal 时
	// handler 还阻塞在 release 上，srv.Close() 会一直等它。
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

	// 必须在子页请求真的到达之后再取消，否则可能取消在出网之前，观测不到在飞请求。
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
		t.Fatal("in-flight request not aborted after Cancel: ctx cancel 未生效" +
			"（TaskTimeout=30s，5s 内不会因超时自然结束）")
	}
}

// TestService_CancelTerminalIsIdempotent 设计 §2.3：已终态任务取消是幂等 no-op，
// 不得修改已有结果。
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

	// 正向对照：已知 taskId 必须能被 Get 取回，且 TaskID 原样回填。
	// 少了它，「未知返回 false」在一个「Get 永远返回 false」的实现下也是绿的。
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
	waitTerminal(t, svc2, id) // 让抓取 goroutine 收尾
}

// TestService_TimeoutKeepsPartial 设计 §2.2：任务超时保留部分结果并置 truncated。
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
	cfg.TaskTimeout = 1 // 秒
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
		t.Errorf("Pages = %d, want 1 (已抓页面必须保留)", task.Pages)
	}
}
