package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"void2md/file/pkg/config"
	"void2md/file/pkg/crawler"
)

func testCrawlCfg() config.HTMLCrawlConfig {
	return config.HTMLCrawlConfig{
		Concurrency: 4, MaxPages: 200, HardMaxPages: 2000,
		PageTimeout: 30, TaskTimeout: 300,
		MaxHTMLSizeMB: 10, MaxTaskBytesMB: 200,
		UserAgent: "test", TaskTTLSeconds: 1800, MaxTasks: 20,
	}
}

func newTestStore() *htmlCrawlStore {
	return newHTMLCrawlStore(testCrawlCfg())
}

func TestStore_SubmitAndGet(t *testing.T) {
	s := newTestStore()
	id, deduped, err := s.submit("https://a.com/", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	if deduped {
		t.Error("first submit must not be deduped")
	}
	task, ok := s.get(id)
	if !ok {
		t.Fatal("task not found after submit")
	}
	if task.Status != CrawlPending {
		t.Errorf("Status = %q, want pending", task.Status)
	}
	if task.Pages != 0 || task.Markdown != "" || len(task.Items) != 0 {
		t.Errorf("initial payload must be empty: %+v", task)
	}
	if task.Truncated || task.Error != "" {
		t.Errorf("initial flags must be zero: %+v", task)
	}
}

// TestStore_Dedup 设计 §2.1：同 URL 存活任务直接返回已有 taskId。
func TestStore_Dedup(t *testing.T) {
	s := newTestStore()
	id1, _, _ := s.submit("https://a.com/", crawler.Options{})
	id2, deduped, err := s.submit("https://a.com/", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	if !deduped {
		t.Error("second submit of same url must be deduped")
	}
	if id1 != id2 {
		t.Errorf("dedup returned %q, want %q", id2, id1)
	}

	// 终态后不再去重，可以重新提交
	s.finishSuccess(id1, &crawler.Result{Pages: []crawler.Page{{URL: "https://a.com/", Markdown: "x"}}})
	id3, deduped, _ := s.submit("https://a.com/", crawler.Options{})
	if deduped {
		t.Error("after terminal state, submit must not be deduped")
	}
	if id3 == id1 {
		t.Error("new task must get a new id")
	}
}

// TestStore_MaxTasks 覆盖评审关注点：存活任务数超限 → 429 对应的错误。
func TestStore_MaxTasks(t *testing.T) {
	cfg := testCrawlCfg()
	cfg.MaxTasks = 1
	s := newHTMLCrawlStore(cfg)

	id1, _, err := s.submit("https://a.com/1", crawler.Options{})
	if err != nil {
		t.Fatalf("first submit error: %v", err)
	}
	_, _, err = s.submit("https://a.com/2", crawler.Options{})
	if err == nil {
		t.Fatal("expected error when alive tasks exceed MaxTasks")
	}
	if !isTooManyRequests(err) {
		t.Errorf("error = %v, want a 429 DiagnosisException", err)
	}

	// 占用者进入终态后应放行。
	//
	// **必须真的断言「放行」，不能只调一次 finishFailed 就算完。** 原写法是
	// `s.finishFailed("hpa_", nil)`——"hpa_" 是个不存在的 taskId，而 finishFailed
	// 开头就是 `if !ok || !e.alive { return }`，于是它什么都没做，后面也没有断言。
	// 结果这条语义零覆盖：把存活计数改成统计全部条目（`for range s.tasks { alive++ }`，
	// 不判 e.alive）时，所有任务都终态了仍会被拒 429，直到 TTL(1800s) 扫掉条目为止，
	// 线上表现是「服务空闲却被 429 打满半小时」——而本次提交仍然全绿。
	s.finishFailed(id1, nil)
	if _, _, err := s.submit("https://a.com/3", crawler.Options{}); err != nil {
		t.Errorf("after the occupant reaches a terminal state the slot must be released, got %v", err)
	}
}

// TestStore_CancelBeforeSetCancel 钉住 setCancel 的自愈分支（设计 §2.3）。
//
// 真实时序：Submit 里是 `go s.run(id, opts)`，而 setCancel 在 run 内部才被调用。
// store.submit 返回后任务已对 GET/DELETE 可见，但取消函数还没登记。这段窗口内
// 到达的 DELETE 会走 cancel()——它把状态置为 cancelled 并清空载荷，但彼时
// e.cancel 仍是 nil，于是 **ctx 从不被取消**，抓取 goroutine 继续跑到 TaskTimeout
// （默认 300s），期间持续出网、持续往内存里堆 Markdown 再丢弃；而客户端拿到的是
// 204，完全看不出异常。负载越高（MaxTasks 个任务并存）这个窗口越宽。
//
// 这里手工复现该时序：先 cancel，再补一次迟到的 setCancel，断言 ctx 被立刻取消。
// 若 setCancel 退回成「有则赋值」的写法，本用例必失败。
func TestStore_CancelBeforeSetCancel(t *testing.T) {
	s := newTestStore()
	id, _, err := s.submit("https://a.com/", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	if !s.cancel(id) {
		t.Fatal("cancel on a pending task must report success")
	}

	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()
	s.setCancel(id, cancelFn) // 迟到的登记：此时任务已终态

	select {
	case <-ctx.Done():
	default:
		t.Error("a late setCancel must cancel the ctx immediately; otherwise the crawl keeps " +
			"running until TaskTimeout even though DELETE already returned 204")
	}
}

// TestStore_TTLsweep 设计 §7：终态任务超过 TTL 后被清理。
// 直接把 doneAt 拨到过去，避免 sleep 整个 TTL（TaskTTLSeconds 是整秒，最小 1s）。
func TestStore_TTLsweep(t *testing.T) {
	s := newTestStore()
	id, _, err := s.submit("https://a.com/", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	s.finishSuccess(id, &crawler.Result{Pages: []crawler.Page{{URL: "https://a.com/", Markdown: "x"}}})

	// 已终态但未过 TTL：仍可查询
	if _, ok := s.get(id); !ok {
		t.Fatal("task must survive before TTL expires")
	}

	// 把终态时间拨到 TTL 之外（同包白盒，直接改 doneAt——get 会顺带 sweep）
	s.mu.Lock()
	s.tasks[id].doneAt = time.Now().Add(-time.Duration(s.cfg.TaskTTLSeconds+60) * time.Second)
	s.mu.Unlock()

	if _, ok := s.get(id); ok {
		t.Error("task must be swept after TTL expires")
	}
}

// TestStore_FinishFailed 设计 §2.2 终态判定规则：抓到 0 页 → failed，
// error 携带具体原因；载荷清空；失败后同 URL 可重新提交（不被去重挡住）。
func TestStore_FinishFailed(t *testing.T) {
	s := newTestStore()
	id, _, err := s.submit("https://a.com/", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	s.setRunning(id)
	s.finishFailed(id, errors.New("entry page https://a.com/ failed: http status 404"))

	task, ok := s.get(id)
	if !ok {
		t.Fatal("failed task must still be queryable within TTL")
	}
	if task.Status != CrawlFailed {
		t.Errorf("Status = %q, want failed", task.Status)
	}
	if task.Error == "" || !strings.Contains(task.Error, "http status 404") {
		t.Errorf("Error = %q, want the underlying reason", task.Error)
	}
	if task.Pages != 0 || task.Markdown != "" || len(task.Items) != 0 {
		t.Errorf("failed task payload must be empty: %+v", task)
	}
	if task.Truncated {
		t.Error("failed task must not be marked truncated")
	}

	// 终态后同 URL 可重新提交（finishLocked 已清 byURL）
	if _, deduped, _ := s.submit("https://a.com/", crawler.Options{}); deduped {
		t.Error("after failed, submit must not be deduped")
	}
}
