package service

// 本文件是 Task 9 的**补充测试**（计划 Task 9 的测试块之外的第三个文件）。
//
// 存在理由：计划 Task 9 的测试块对下列语义零检测力——把实现按这些方向变异后，
// 计划测试块仍然全绿（见 Task 9 报告的「补测前」列）。每条用例都对应一个
// 已实测存活过的变异，注释里点名它钉住的实现行为与变异，便于复核。
//
// 命名沿用 TestStore_ 前缀，因此 `go test ./internal/service/ -run TestStore`
// 会与计划用例一并执行；每条用例都自带静态前置断言（fixture 真的生效），
// 避免「夹具为空 → 断言恒真」这一类空转。

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"void2md/common/diagnosis"
	"void2md/file/pkg/crawler"
)

// TestStore_SuccessPayload 钉住 finishSuccess 的产出本身：
// items / pages / markdown（含来源注释）/ truncated 传播。
//
// 变异 m8（`e.task.Markdown = ""`）与 m9（`finishLocked(..., res.Truncated)`
// 改成 `false`）在计划测试块下均存活：计划只在 TestStore_Dedup 与
// TestStore_TTLsweep 里调用 finishSuccess，且只断言「终态后可重新提交」
// 「TTL 后可被清扫」，从不断言产出。本用例对每个字段做正向断言：
// 该出现的东西必须出现。
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
	// markdown 是 items 的拼接，且每页前带来源注释（下游分块器据此标注 chunk 来源）
	for _, want := range []string{
		"<!-- source: https://a.com/1 -->", "正文一",
		"<!-- source: https://a.com/2 -->", "正文二",
	} {
		if !strings.Contains(task.Markdown, want) {
			t.Errorf("Markdown must contain %q, got %q", want, task.Markdown)
		}
	}
	// 页序：第一页的来源注释必须出现在第二页之前
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

// TestStore_CancelTerminalIsIdempotent 钉住 cancel() 对已终态任务的幂等语义：
// 返回 true（任务存在，DELETE 应回 204 而非 404），且不改动已有结果。
//
// 变异 m3（`return true // 已终态` 改成 `return false`）在计划测试块下存活：
// 计划只在 TestStore_CancelBeforeSetCancel 里对 **pending** 任务调 cancel，
// 从未对终态任务调过。
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

// TestStore_FinishCancelledClearsPayload 钉住 finishCancelled 的清载荷行为
// （「任务因 ctx 取消而结束」也属设计 §2.2 的 cancelled 语义）。
//
// 变异 m5（删掉三行清载荷）在计划测试块下存活：计划里没有任何用例调用
// finishCancelled。这里白盒塞入非空载荷（模拟「已抓了几页后 ctx 被取消」），
// 并以静态前置断言确认夹具真的生效，再断言被清空。
func TestStore_FinishCancelledClearsPayload(t *testing.T) {
	s := newTestStore()
	id, _, err := s.submit("https://a.com/", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	s.setRunning(id)
	s.setProgress(id, 3) // 运行中已报告完成 3 页

	// 白盒塞入载荷：模拟 run 里已完成但尚未提交的结果
	s.mu.Lock()
	e := s.tasks[id]
	e.task.Markdown = "半截结果"
	e.task.Items = []HTMLCrawlItem{{URL: "https://a.com/1", Markdown: "半截结果"}}
	s.mu.Unlock()

	// 前置断言：夹具确实生效（否则下面的断言会因载荷本就是空的而恒真）
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
	// 终态后同 URL 可重新提交
	if _, deduped, _ := s.submit("https://a.com/", crawler.Options{}); deduped {
		t.Error("after cancel, submit of the same url must not be deduped")
	}
}

// TestStore_TerminalReleasesURLIndex 钉住 finishLocked 的 delete(s.byURL, ...)。
//
// 变异 m2（删掉该 delete）在计划测试块下存活：submit 里有自愈分支——命中
// byURL 但条目已不 alive 时就地 delete 再新建——所以「终态后重新提交」依旧
// 拿到新 id、deduped=false，计划用例完全察觉不到。代价是 byURL 会永久残留
// 指向非存活任务的条目（内存只增不减，且与设计 §2.1「规范化 URL → **存活**
// 任务 ID」的索引语义不符）。本用例直接断言索引被释放。
func TestStore_TerminalReleasesURLIndex(t *testing.T) {
	s := newTestStore()
	const url = "https://a.com/"
	id, _, err := s.submit(url, crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}

	// 前置断言：存活时确实在索引里（否则「已释放」的断言可能因索引从未建立而恒真）
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

	// 正向：重新提交拿到新任务，并重新占用索引
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

// TestStore_ItemsNeverNull 钉住「items 永远是数组而非 null」的响应契约
// （设计 §2.2 响应示例；实现里 get 的防御性拷贝 + 构造函数的 Items 初始化）。
//
// 变异 m11（同时删掉 get 里的防御性拷贝与构造函数的 Items 初始化）在计划
// 测试块下存活：计划只用 len(task.Items) 判断，nil 与空切片都等于 0。
// 后果是响应体从 "items": [] 变成 "items": null，下游按数组解析会出错。
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

	// 终态路径同样：failed / cancelled 都必须是非 nil 空切片
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

// TestStore_SweepNeverDropsUnfinishedEntries 钉住 sweepLocked 的
// `!e.doneAt.IsZero()` 守卫（htmlCrawlEntry.doneAt 的字段注释：
// **零值表示未进入终态**）。
//
// 变异 m6（删掉该守卫）在计划测试块下存活：计划的 TestStore_TTLsweep 只覆盖
// 「终态且已过 TTL」，而 finishLocked 总是同时置 alive=false 与 doneAt，所以
// 删掉守卫后所有真实路径的行为都不变。守卫真正生效的场景是「条目已标记为
// 不存活、但 doneAt 尚未落定」——没有守卫时这种条目会被立刻清扫（在回调/
// 竞态时序下等于任务凭空消失）。
//
// 用例内含反向断言（把 doneAt 落实为过去时间后**必须**被扫掉），
// 以确保「没被扫掉」不是因为 sweepLocked 整体失效而恒真。
func TestStore_SweepNeverDropsUnfinishedEntries(t *testing.T) {
	s := newTestStore()
	id, _, err := s.submit("https://a.com/", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}

	s.mu.Lock()
	s.tasks[id].alive = false // 不存活……
	// ……但 doneAt 仍是零值：按字段注释它「未进入终态」，不属可清扫对象
	s.sweepLocked()
	_, survived := s.tasks[id]
	s.mu.Unlock()
	if !survived {
		t.Error("sweepLocked must not drop an entry whose doneAt is still zero (未进入终态)")
	}

	// 反向断言：doneAt 落实并超过 TTL 后，同一条目必须被扫掉
	s.mu.Lock()
	s.tasks[id].doneAt = time.Now().Add(-time.Duration(s.cfg.TaskTTLSeconds+60) * time.Second)
	s.sweepLocked()
	_, survived2 := s.tasks[id]
	s.mu.Unlock()
	if survived2 {
		t.Error("the same entry must be swept once doneAt is older than TTL")
	}
}

// TestStore_LateProgressDoesNotResurrectPayload 钉住 setProgress / setRunning
// 的存活判断（「cancelled → 丢弃已抓页面」是 Task 9 的核心语义）。
//
// 计划用例从未覆盖这两个方法的 `e.alive` 判断：把 `ok && e.alive` 退化成只看
// `ok`（变异 m12/m13）后，运行中 goroutine 的一次迟到回调就能让刚被取消的
// 任务重新出现 pages>0、甚至状态倒退回 running，而计划测试块全绿。
func TestStore_LateProgressDoesNotResurrectPayload(t *testing.T) {
	s := newTestStore()
	id, _, err := s.submit("https://a.com/", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	s.setRunning(id)

	// 前置断言：setRunning 确实生效（证明下面「状态没被改回去」不是因为根本没生效过）
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

	s.setProgress(id, 7) // 迟到的进度回调
	s.setRunning(id)     // 迟到的状态回调

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

// TestStore_FinishFailedNilErrorUsesDefault 钉住 finishFailed 对 nil error 的兜底：
// 失败任务的 error 字段必须非空。接口把该字段直接作为 error 暴露给调用方，
// 空串等于「失败了但不说原因」。
//
// 计划测试块只在 TestStore_MaxTasks 里用 nil 调过一次 finishFailed，而那条用例
// 只断言「名额被释放」，从不读 Error——把 `msg := "unknown error"` 改成 `msg := ""`
// （变异 m14）后计划用例全绿。
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

// TestStore_TaskIDFormat 钉住 newTaskID 的文档化契约（「生成 16 位十六进制任务
// ID，前缀 hpa_」）：前缀 + 总长 20 + 十六进制 + 两次提交互不相同。
//
// 计划用例只断言 id1 != id3（且那是去重路径上的断言），既不校验前缀也不校验
// 长度——把 `hex.EncodeToString(b[:])` 改成 `b[:4]`（变异 m15）后计划用例全绿。
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
// 以上是 supplement_v1（首轮补测，共 9 条）。以下两条是对一轮只读审计
// （2026-09-16）报出的缺口的回应，审计当时用的正是 supplement_v1：
// 实测 m17 / m16 在 supplement_v1 下仍然存活。

// TestStore_TerminalFinishWithoutAliveGuardIsIgnored 钉住 finishSuccess /
// finishFailed / finishCancelled 三处的 `!e.alive` 守卫。
//
// 审计实测：把这三处 `if !ok || !e.alive { return }` 同时改成 `if !ok { return }`
// （变异 m17），**带着 supplement_v1 也全绿**。这是「DELETE 返回 204 之后，
// 仍在飞的抓取不得覆写已取消任务」的唯一防线：守卫没了，迟到的 finishSuccess
// 会把任务从 cancelled 翻成 success 并回填 markdown/items，直接违背设计 §2.3。
//
// Task 10 的 CancelDiscardsPages 走的是 ctx 取消 → finishCancelled 路径，而
// cancel() 已经先把 alive 置 false，所以那条路径上看不见这个洞。
//
// doneAt 的断言是为了让三个子用例都有区分力：对 finishCancelled 而言状态本就
// 是 cancelled、载荷本就为空，去掉守卫唯一的可观测差异就是 doneAt 被刷新
// （任务在内存里多留一个 TTL）。
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

			// 前置断言：确实已是终态且载荷已清空（否则下面的断言会因夹具未生效而恒真）
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

			tc.finish(s, id) // 迟到的终态回调：必须被守卫丢掉

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

// TestStore_TooManyRequestsDiagnosisCode 绕开 isTooManyRequests，独立断言 429 的
// 诊断码。
//
// 审计指出：TestStore_MaxTasks 用 isTooManyRequests(err) 断言，而那正是 submit
// 内部使用的判据——属于「用自己的判据测自己」。把 helper 里的 DCode 比较删掉、
// 只留 StatusCode == 429（变异 m16），所有用例照旧全绿，而接口响应里的
// diagnosisCode 会从 FF000D 静默变成别的码，下游按 diagnosisCode 分流时
// 把「请求过多」误判为「参数错误」。设计 §2.1/§5 新增 FF000D 正是为了让这个
// 语义可被区分。
//
// 这里直接断言 DiagnosisException 的字段，并写出**字面量**而不是与
// diagnosis.ErrorTooManyRequestsDiagnosisCode 比较——后者会退化成「两处声明
// 一致」型的空转断言（两个声明同时被改仍然全绿）。
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
		t.Errorf("DCode = %q, want the literal \"0x0000000000FF000D\" (FF000D 请求过多)", de.DCode)
	}
}

// TestStore_SourceMarkerCannotBeEscaped 钉住来源注释的**结构完整性**：无论 URL 里
// 出现什么序列，每页那行 `<!-- source: <url> -->` 都必须恰好构成**一个**完整注释，
// 不得被 URL 自带的 `-->` 提前闭合。
//
// 为什么需要：`finishSuccess` 把 URL 直接拼进注释，而 URL 的 query 在归一化
// （`crawler.NormalizeURL` 只小写 host、去 fragment/默认端口）与 `url.URL.String()`
// 里都是**原样保留**的 —— 一条 `?x=--><svg/onload=alert(1)>` 形状的同域链接会让注释
// 在第一处 `-->` 结束，其后的 `<svg/onload=...>` 不再是注释文本，而是**活的**
// Markdown/HTML，随聚合 markdown 一起流向下游（/v1/chunk、知识库）。设计 §2.2 明确
// 依赖「注释在 Markdown 渲染时不可见」，该承诺对这类 URL 不成立。
//
// 变异判定：把 `commentSafeURL` 去掉（还原成 `"<!-- source: " + p.URL + " -->"`），
// 本用例失败、其余用例全绿 —— 这正是当时 13 个变异体都没覆盖到的形状。
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

	// 前置断言：夹具必须真的把危险序列带进来了，否则下面的断言全部恒真。
	if !strings.Contains(task.Items[1].URL, "-->") {
		t.Fatalf("fixture broken: items[1].URL = %q 里没有 -->", task.Items[1].URL)
	}

	// 两页 → 聚合结果里恰好两个注释终结符。多出来的那个就是 URL 提前闭合注释留下的。
	if n := strings.Count(task.Markdown, "-->"); n != 2 {
		t.Errorf("注释终结符 %d 个，want 2（每页一个）：URL 里的 --> 提前闭合了注释，markdown = %q",
			n, task.Markdown)
	}
	// 危险载荷不得出现在注释之外 —— 出现 `--><svg` 就说明它逃出来了。
	if strings.Contains(task.Markdown, "--><svg") {
		t.Errorf("URL 里的 --> 逃出了注释，<svg> 成了活标签，markdown = %q", task.Markdown)
	}
	// items[].URL 仍是**未加工**的原文：转义只服务于注释，不得改写数据本身，
	// 否则下游按 items[].url 溯源会拿到一个被改过的 URL。
	if task.Items[1].URL != evil {
		t.Errorf("items[1].URL = %q, want the raw URL %q 原样保留", task.Items[1].URL, evil)
	}
}

// TestStore_SourceMarkerChunkStillCarriesSource 钉住转义之后**溯源能力仍在**：
// 注释里必须还能读到来源 URL（下游 chunk 靠它标注来源），转义不得把 URL 抹掉。
//
// 与上一条互补：上一条防「逃出注释」，这条防「修过头 —— 把 URL 整段删掉/编码成不可读」。
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
		t.Errorf("注释里必须保留可读的来源 URL，markdown = %q", task.Markdown)
	}
}
