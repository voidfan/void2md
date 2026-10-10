package service

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"void2md/internal/config"
	"void2md/internal/crawler"
)

// 本文件钉住零值配置的语义（收尾复审 F6）：**0 表示未设置，取默认值**，
// 而不是「关掉这项保护」。三处保护都必须仍然生效：
//   - MaxTasks=0        → 默认 20，而不是「不限任务数」
//   - TaskTTLSeconds=0  → 默认 1800s，而不是「永不清扫」
//   - MaxPages=0        → 默认 200，而不是靠 Submit 里硬编码的兜底
//
// 这些用例全部直接构造 `config.HTMLCrawlConfig{}`（全零），走的是
// `newHTMLCrawlStore` / `NewHTMLCrawlService` 自己的归一化，不依赖 env ——
// 因为直接构造结构体是最容易写出零值、也最容易绕过 config 载入的路径。

// TestStore_ZeroConfigStillEnforcesMaxTasks 零值配置必须仍然限流。
//
// 旧语义下 `if s.cfg.MaxTasks > 0 && alive >= s.cfg.MaxTasks` 对 0 恒假，
// 于是存活任务数无上界——每个任务可占 200MB，等于把内存保护整体关掉。
func TestStore_ZeroConfigStillEnforcesMaxTasks(t *testing.T) {
	s := newHTMLCrawlStore(config.HTMLCrawlConfig{})
	limit := config.DefaultHTMLCrawlConfig().MaxTasks

	for i := 0; i < limit; i++ {
		// 每个任务用不同 URL，避开去重分支（去重会让 submit 直接复用而不新建）
		if _, _, err := s.submit(fmt.Sprintf("https://a.com/p%d", i), crawler.Options{}); err != nil {
			t.Fatalf("第 %d 个任务被拒: %v（零值不得表示「不限任务数」）", i+1, err)
		}
	}

	_, _, err := s.submit("https://a.com/overflow", crawler.Options{})
	if err == nil {
		t.Fatalf("超过默认 MaxTasks(%d) 后必须拒绝：零值配置不得等于「不限任务数」", limit)
	}
	if !isTooManyRequests(err) {
		t.Errorf("err = %v, want 429 + FF000D（与 MaxTasks 显式配置时的拒绝方式一致）", err)
	}
}

// TestStore_ZeroConfigStillSweeps 零值配置必须仍然清扫。
//
// 旧语义下 `if ttl <= 0 { return }` 让 TTL=0 变成「永不清扫」：已终态任务的
// 整个载荷（最多 200MB Markdown）会一直留在内存里，且没有任何日志提示。
//
// **两侧都要断言**：只断言「过期的被扫掉」是空转——若零值 TTL 没被补成默认值而是
// 留作 0，`now.Sub(doneAt) > 0` 对**任何**终态任务都成立，过期的照样被扫掉，
// 该断言依然通过。所以要配一条正向断言：**刚进入终态的任务不得被立刻清扫**，
// 这条才真正钉住「0 被补成了 1800s 这个非零默认值」。
func TestStore_ZeroConfigStillSweeps(t *testing.T) {
	s := newHTMLCrawlStore(config.HTMLCrawlConfig{})
	def := config.DefaultHTMLCrawlConfig()

	// 正向：刚终态的任务必须留着。
	freshID, _, err := s.submit("https://a.com/fresh", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	s.finishFailed(freshID, errors.New("boom"))
	if _, ok := s.get(freshID); !ok {
		t.Errorf("刚进入终态的任务不得被立刻清扫：零值 TTL 必须落到默认 %ds，而不是 0",
			def.TaskTTLSeconds)
	}

	// 反向：超过默认 TTL 的必须被扫掉（把终态时间推回到 2h 前）。
	oldID, _, err := s.submit("https://a.com/old", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	s.finishFailed(oldID, errors.New("boom"))
	s.mu.Lock()
	s.tasks[oldID].doneAt = time.Now().Add(-2 * time.Hour)
	s.mu.Unlock()

	if _, ok := s.get(oldID); ok {
		t.Errorf("超过默认 TTL(%ds) 的终态任务必须被清扫，而不是永远留着", def.TaskTTLSeconds)
	}
}

// TestStore_SweepSurvivesLateProgress 钉住清扫不会把**未进入终态**的条目扫掉——
// 与上一条配对，防止「清扫生效」被做成「一律扫掉」。
//
// 这条在归一化之前就成立（doneAt 零值守卫），此处复述是因为上一条用例改动的是
// TTL 的来源；若有人把 sweepLocked 的守卫一起删掉，上一条仍会通过而本条会失败。
func TestStore_SweepSurvivesLateProgress(t *testing.T) {
	s := newHTMLCrawlStore(config.HTMLCrawlConfig{})
	id, _, err := s.submit("https://a.com/", crawler.Options{})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}

	s.mu.Lock()
	s.tasks[id].alive = false // 不存活，但 doneAt 仍为零值（未进入终态）
	s.sweepLocked()
	_, survived := s.tasks[id]
	s.mu.Unlock()
	if !survived {
		t.Error("doneAt 为零值的条目不得被清扫（alive=false 不等于终态已落定）")
	}
}

// TestService_ZeroConfigBuildOptions 钉住「配置 → crawler.Options」这一步：
// 零值配置折出来的参数必须带默认值，且 **MB 必须换算成字节**。
//
// 为什么单独钉这一步：它此前没有任何用例，而这里有两个可删的 `<< 20`。
// 删掉之后配置里的 200/10（MB）会以 200/10 **字节**传下去——单页 HTML 上限 10 字节、
// 单任务字节预算 200 字节，真实站点一个页面都抓不回来；而 HTTP 层的用例喂的都是
// httptest 的小页面，未必暴露得出来（小页面在 200 字节预算下也可能先被别的分支兜住）。
//
// 期望值**写死** 200MB / 10MB，不从 DefaultHTMLCrawlConfig() 推：两边用同一个
// 表达式的话，`<<20` 被一起删掉也测不出来（与 TestHTMLCrawlConfigDefaults 同样的理由）。
func TestService_ZeroConfigBuildOptions(t *testing.T) {
	s := NewHTMLCrawlService(config.HTMLCrawlConfig{})
	def := config.DefaultHTMLCrawlConfig()

	u, err := crawler.NormalizeURL("https://a.com/x")
	if err != nil {
		t.Fatalf("NormalizeURL: %v", err)
	}
	opts := s.buildOptions(u, "body", 7)

	if opts.MaxTaskBytes != 200<<20 {
		t.Errorf("MaxTaskBytes = %d, want 200MB (%d)：单任务字节预算必须由 MB 换算成字节", opts.MaxTaskBytes, 200<<20)
	}
	if opts.MaxHTMLSize != 10<<20 {
		t.Errorf("MaxHTMLSize = %d, want 10MB (%d)：单页 HTML 上限必须由 MB 换算成字节", opts.MaxHTMLSize, 10<<20)
	}
	if opts.Concurrency != def.Concurrency {
		t.Errorf("Concurrency = %d, want 默认值 %d", opts.Concurrency, def.Concurrency)
	}
	if opts.PageTimeout != time.Duration(def.PageTimeout)*time.Second {
		t.Errorf("PageTimeout = %v, want %d 秒", opts.PageTimeout, def.PageTimeout)
	}
	if opts.TaskTimeout != time.Duration(def.TaskTimeout)*time.Second {
		t.Errorf("TaskTimeout = %v, want %d 秒", opts.TaskTimeout, def.TaskTimeout)
	}
	if opts.UserAgent != def.UserAgent {
		t.Errorf("UserAgent = %q, want 默认值 %q", opts.UserAgent, def.UserAgent)
	}
	if opts.MaxPage != 7 || opts.Selector != "body" {
		t.Errorf("入参必须原样透传, got MaxPage=%d Selector=%q", opts.MaxPage, opts.Selector)
	}
}

// TestService_ZeroConfigMaxPages 零值配置下页数解析必须落到默认值，
// 且显式入参的 clamp / 校验语义不变（设计 §2.1）。
func TestService_ZeroConfigMaxPages(t *testing.T) {
	def := config.DefaultHTMLCrawlConfig()
	s := NewHTMLCrawlService(config.HTMLCrawlConfig{})

	got, err := s.resolveMaxPages(nil)
	if err != nil {
		t.Fatalf("maxPage=nil 不得报错: %v", err)
	}
	if got != def.MaxPages {
		t.Errorf("零值 MaxPages 下取默认值，got %d want %d", got, def.MaxPages)
	}

	big := 100000
	got, err = s.resolveMaxPages(&big)
	if err != nil {
		t.Fatalf("超过硬上限应 clamp 而非报错: %v", err)
	}
	if got != def.HardMaxPages {
		t.Errorf("零值 HardMaxPages 下应 clamp 到默认硬上限 %d, got %d", def.HardMaxPages, got)
	}

	three := 3
	if got, err = s.resolveMaxPages(&three); err != nil || got != 3 {
		t.Errorf("合法入参必须原样生效, got %d err %v", got, err)
	}

	zero := 0
	if _, err = s.resolveMaxPages(&zero); err == nil {
		t.Error("maxPage=0 必须报参数错误（入参校验不受归一化影响）")
	}
}
