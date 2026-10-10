package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"void2md/internal/config"
	"void2md/internal/pkg/diagnosis"
	htmld "void2md/internal/service/crawl"
)

// crawlTestSite 起一个两页假站点。
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

// newHTMLParseTestRouter 用测试服务替换全局单例（HardMaxPages=3 的默认测试配置）。
func newHTMLParseTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	return newHTMLParseTestRouterWithCfg(t, config.HTMLCrawlConfig{
		Concurrency: 4, MaxPages: 200, HardMaxPages: 3,
		PageTimeout: 5, TaskTimeout: 5,
		MaxHTMLSizeMB: 10, MaxTaskBytesMB: 200,
		UserAgent: "test", TaskTTLSeconds: 60, MaxTasks: 20,
	})
}

// newHTMLParseTestRouterWithCfg 同 newHTMLParseTestRouter，但允许用例自定义配置
// （例如把 MaxTasks 压到 1，用两步即可触发 429）。
func newHTMLParseTestRouterWithCfg(t *testing.T, cfg config.HTMLCrawlConfig) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	// 必须在闭包外构造**同一个** service：若在闭包里每次 NewHTMLCrawlService，
	// 每个请求都会拿到新的 store，POST 建的任务在 GET 时必然 404。
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

	// 轮询到终态
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
	// 响应字段必须齐全（与 /v1/pdfparse 同构 + items/truncated）
	for _, k := range []string{"status", "pages", "markdown", "items", "truncated", "error"} {
		if _, ok := got[k]; !ok {
			t.Errorf("response missing field %q", k)
		}
	}
}

// TestHandleHTMLParse_NoEscape 复用 JSONNoEscape：markdown 里的 HTML 表格
// 必须以字面量 < 输出，否则大响应会被转义膨胀（既有 OOM 修复的回归面）。
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
			// 口径照抄仓库既有写法（internal/handler/chunk_handler_test.go:44-45、
			// internal/pkg/response/response_test.go）：查**转义序列**而非字面尖括号/与号，
			// 否则「断言含 <table>」与「断言不含 <」会自相矛盾。
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
	// 注意：handler **不做** URL / scheme / maxPage 校验，这些输入会原样传进
	// svc.Submit，由 Submit 统一返回 FF0003（handler 只负责 JSON 绑定失败时的 400）。
	// 它们仍然不会真正抓取，但原因不是"没走到 Submit"，而是 **Submit 在创建任务与
	// 起抓取 goroutine 之前就完成了全部校验**。
	//
	// 因此 URL 用本地站点而不是 https://example.com：后者的"不出网"只依赖 Submit 的
	// 校验顺序这一条结构性假设，一旦有人把 maxPage 校验挪到 store.submit/`go s.run`
	// 之后，这些单测就会静默向公网发起真实请求。换成 httptest 本地地址，代价为零。
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
		// 上面那条与下面这条堵的是**不同的**东西，两条都必须留。
		// 类型错误发生在 maxPage 上时，encoding/json 会把 MaxPage 留成**非 nil 的零值指针**
		// （实测：err 非空，但 maxPage=non-nil -> 0），于是 Submit 自己的 *maxPage<=0 分支
		// 照样返回同样的 400+FF0003——把 handler 的 ShouldBindJSON 报错分支整个删掉也测不出来。
		// 只有「URL 解码正常、而**另一个**字段类型错误」时 MaxPage 才是 nil：不看 err 就会
		// 拿着一个合法 URL 真的去 Submit 并返回 200（实测变异体下确为 200 + hpa_ taskId）。
		// 这条是唯一能钉住该分支的输入形状。
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
				t.Errorf("error message must be non-empty (设计 §5 要求携带具体原因)")
			}
			// 只断言 400 是不够的：把 dcode 换成别的，状态码仍是 400，断言照样通过。
			// 设计 §5 要求参数错误一律 FF0003，下游按 diagnosisCode 分流时依赖它。
			if e.DiagnosisCode != diagnosis.ErrorParamCheckDiagnosisCode {
				t.Errorf("diagnosisCode = %q, want %q", e.DiagnosisCode, diagnosis.ErrorParamCheckDiagnosisCode)
			}
		})
	}
}

// TestHandleHTMLParse_TooManyTasks429 覆盖设计 §5：存活任务数超限 → 429 + FF000D。
//
// 这条用例的价值在于**端到端**走通 handler 的错误映射分支：服务层返回的
// DiagnosisException 必须经 abortWithDiagnosis 的 errors.As 分支原样写出 429 与 FF000D。
// Task 10 只在 store 层验过 isTooManyRequests；若 handler 把该错误按 500 兜底，
// store 层测试全绿而线上响应整个错。
//
// MaxTasks 压到 1，用两步触发；两个 URL 必须不同，否则去重会把两次 POST 合成一个任务。
func TestHandleHTMLParse_TooManyTasks429(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // 卡住入口页，任务停在 running → 占用存活名额
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><p>stalled</p></body></html>`))
	}))
	// defer 是 LIFO：close(release) 先执行放行被卡住的请求，
	// srv.Close() 后执行才不会干等到 httptest 超时。
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

// TestHandleHTMLParse_MaxPageClamped 设计 §2.1：超硬上限按 clamp 处理，返回 200 而非 400，
// 且**钳制必须真的生效**——只断言 200 是空转的：把 clamp 整段删掉，仍然返回 200。
//
// 必须用本地站点：这个用例会真的走 Submit 起一个爬取 goroutine，
// 若把 URL 写成 https://example.com，单元测试就会向公网发起真实请求
// （CI 无外网时还会拖到超时）。
// 站点必须比 router 的 HardMaxPages(=3) 更多页：否则钳不钳制都抓到同样多页，
// 断言不出区别。
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

	r := newHTMLParseTestRouter(t) // 该 router 的 HardMaxPages = 3
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

	// 等任务落终态，再断言钳制的结果（POST 只返回 taskId，页数要 GET 才看得到）。
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
				t.Errorf("pages = %d, want 3：maxPage=99999 必须被钳到 HardMaxPages=3", task.Pages)
			}
			if !task.Truncated {
				t.Error("truncated = false, want true：被页数上限截断的任务必须置 truncated")
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

	// 只断言 404 是不够的：把两处 ErrorNotFoundDiagnosisCode 换成 ErrorUnknownDiagnosisCode
	// （FF0002 → FF0004），状态码仍是 404，断言照样通过（审计实测：全部测试全绿）。
	// 设计 §2.2/§2.3 要求未知 taskId 一律 FF0002，下游按 diagnosisCode 分流时会据此
	// 把「任务不存在」与「未知错误」分开——两者对调用方的处置完全不同（前者等 TTL 或
	// 改 ID，后者要报障）。本文件对 400 与 429 都断言了 dcode，404 不能例外。
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

// TestHandleHTMLParse_Cancel204 设计 §2.3：DELETE 取消**运行中**的任务并清空载荷。
//
// 必须用**阻塞站点**。若站点能在 DELETE 之前跑完（`crawlTestSite` 只有两页，常常瞬间
// 完成），DELETE 走的是「终态幂等」分支——按契约它**不修改已有结果**，于是状态是
// success 而非 cancelled，断言 cancelled 会随机失败。而「取消一个还在跑的任务」才是
// §2.3 的主路径，也只有阻塞站点能确定性地测到它。
//
// 只断言 204 同样不够：把 `cancel` 改成「什么都不做、直接返回 204」也能通过。
func TestHandleHTMLParse_Cancel204(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // 卡住入口页，任务停在 running
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><p>stalled</p></body></html>`))
	}))
	// defer 是 LIFO：close(release) 先执行放行被卡住的请求，
	// srv.Close() 后执行才不会干等到 httptest 超时。
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

	// 取消后任务仍在表内（TTL 未到），但必须是 cancelled 且载荷已清空——这才是 §2.3 的承诺。
	gw := doJSON(t, r, http.MethodGet, "/v1/htmlparse/"+sub.TaskID, "")
	if gw.Code != http.StatusOK {
		t.Fatalf("GET after cancel status = %d, want 200; body=%s", gw.Code, gw.Body.String())
	}
	var got map[string]interface{}
	if err := json.Unmarshal(gw.Body.Bytes(), &got); err != nil {
		t.Fatalf("GET response not JSON: %v (%s)", err, gw.Body.String())
	}
	if got["status"] != string(htmld.CrawlCancelled) {
		t.Errorf("status = %v, want %q（取消运行中任务必须落 cancelled）", got["status"], htmld.CrawlCancelled)
	}
	if md, ok := got["markdown"].(string); !ok || md != "" {
		t.Errorf("cancelled task must discard markdown, got %v", got["markdown"])
	}
	// 注意用 `, ok` 形式断言：直接 `.(float64)` / `.([]interface{})` 在字段缺失时会 panic
	// （interface {} is nil），把「字段不存在」误报成测试崩溃而不是清晰失败。
	if items, ok := got["items"].([]interface{}); !ok || len(items) != 0 {
		t.Errorf("cancelled task must discard items, got %v", got["items"])
	}
	if pages, ok := got["pages"].(float64); !ok || pages != 0 {
		t.Errorf("cancelled task must reset pages, got %v", got["pages"])
	}

	// 再次 DELETE 幂等
	dw2 := doJSON(t, r, http.MethodDelete, "/v1/htmlparse/"+sub.TaskID, "")
	if dw2.Code != http.StatusNoContent {
		t.Errorf("second DELETE status = %d, want 204", dw2.Code)
	}
}

// TestHandleHTMLParse_SubmitDedupReturnsSameTaskID 设计 §2.1：同一规范化 URL
// 已有存活任务（pending/running）时直接返回已有 taskId。
//
// 必须用**阻塞**的站点：若换成即时返回的假站点，两次 POST 之间第一次爬取可能已经
// 跑完落终态（本测试的配置 HardMaxPages 很小、页面只有几字节），第二次 POST 就会
// 合法地新建任务、拿到不同 taskId，测试随机失败。
// 卡住入口页可以保证任务在两次 POST 期间必然处于存活态。
func TestHandleHTMLParse_SubmitDedupReturnsSameTaskID(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // 卡住入口页请求，任务停在 running
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><p>stalled</p></body></html>`))
	}))
	// defer 是 LIFO：close(release) 先执行放行被卡住的请求，
	// srv.Close() 后执行才不会干等到 httptest 超时。
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
