package crawler

// 本文件是 Task 7 的补充覆盖。单独成文件有两个原因：
//  1. 计划里的测试块（crawler_test.go 第 1-399 行）要保持与计划逐字一致，而下面的
//     用例需要 sync/atomic 这类计划 import 块里没有的测试专用包；
//  2. 这条用例钉的是调度层语义（批同步扇出），与 crawler_test.go 里按「入口/终态/
//     截断」组织的用例关注点不同。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestCrawl_BatchFanoutIsConcurrent 钉死设计 §4.3 的「批同步扇出」：同一批的页面必须
// 真并发地抓（容量 Concurrency 的信号量），而不是一条条串行。
//
// 构造刻意不靠 sleep 赌调度：叶子页的 handler 会一直挂到**第二个并发到访者**出现为止
// （或 2s 兜底），于是
//   - 真并发的实现（信号量容量 4）：首批 4 个请求几乎同时到达，收齐第 2 个即放行，
//     整个爬取毫秒级正常结束（5 页）；
//   - 把信号量容量改成 1（变异体 m_concurrency_serial）：同一时刻只有 1 个请求在飞，
//     每个叶子页都要等满 2s，且 maxInFlight 恒为 1 → 下面的断言失败。
//
// 断言的是**观测到的最大在飞请求数**而不是耗时：机器越慢、请求越容易重叠，只会更容易
// 满足 >= 2，因此不会出现「负载高就偶发假红」的坏味道（这与计划里那条 1s 预算的用例
// 刻意留出余量是同一个考虑）。
func TestCrawl_BatchFanoutIsConcurrent(t *testing.T) {
	var inFlight, maxInFlight, released int32
	release := make(chan struct{})

	enter := func() {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			cur := atomic.LoadInt32(&maxInFlight)
			if n <= cur || atomic.CompareAndSwapInt32(&maxInFlight, cur, n) {
				break
			}
		}
		// 第二个并发到访者负责放行；CAS 保证 close 只发生一次。
		if n >= 2 && atomic.CompareAndSwapInt32(&released, 0, 1) {
			close(release)
		}
		select {
		case <-release:
		case <-time.After(2 * time.Second): // 串行的实现会走到这里
		}
		atomic.AddInt32(&inFlight, -1)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(page("/a", "/b", "/c", "/d")))
			return
		}
		enter()
		_, _ = w.Write([]byte(page()))
	}))
	defer srv.Close()

	opts := defaultOpts(srv.URL + "/")
	opts.Concurrency = 4
	opts.MaxPage = 8
	opts.TaskTimeout = 30 * time.Second

	res, err := Crawl(context.Background(), opts, nil)
	if err != nil {
		t.Fatalf("Crawl error: %v", err)
	}
	if len(res.Pages) != 5 { // 入口 + /a /b /c /d
		t.Fatalf("pages = %d, want 5; urls=%v", len(res.Pages), pageURLs(res))
	}
	if n := atomic.LoadInt32(&maxInFlight); n < 2 {
		t.Errorf("max concurrent in-flight requests = %d, want >= 2; 同一批页面必须并发抓取（信号量容量 Concurrency），不是串行", n)
	}
}
