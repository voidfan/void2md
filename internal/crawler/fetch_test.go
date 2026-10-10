package crawler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestFetcher_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "test-agent" {
			t.Errorf("User-Agent = %q, want test-agent", got)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body><p>hi</p></body></html>"))
	}))
	defer srv.Close()

	f := NewFetcher("test-agent", 5*time.Second, 1<<20)
	body, err := f.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch error: %v", err)
	}
	if !strings.Contains(string(body), "<p>hi</p>") {
		t.Errorf("body = %q", body)
	}
	// text/html; charset=utf-8 走的是「原样返回」分支，返回体必须仍是合法 UTF-8。
	// 原文包含性已由上面那条断言覆盖，这里只补编码合法性，不重复断言同一件事。
	if !utf8.Valid(body) {
		t.Errorf("body declared as utf-8 must stay valid UTF-8, got %q", body)
	}
}

// TestFetcher_CharsetHintWins HTTP 头声明 gbk 时必须按 gbk 解码，
// 而不是交给下游再按 <meta> 解一次（那会产出乱码）。
func TestFetcher_CharsetHintWins(t *testing.T) {
	// 页面正文用 GBK 编码的「中文」，且 <meta> 声称 gbk；HTTP 头也声明 gbk。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=gbk")
		enc, _ := simplifiedchinese.GBK.NewEncoder().Bytes(
			[]byte(`<html><head><meta charset="gbk"></head><body><p>中文内容</p></body></html>`))
		_, _ = w.Write(enc)
	}))
	defer srv.Close()

	f := NewFetcher("test-agent", 5*time.Second, 1<<20)
	body, err := f.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch error: %v", err)
	}
	if !utf8.Valid(body) {
		t.Fatalf("decoded body must be valid UTF-8, got %q", body)
	}
	if !strings.Contains(string(body), "中文内容") {
		t.Errorf("gbk content should be decoded to UTF-8, got %q", body)
	}
}

// TestFetcher_HeaderWinsOverLyingMeta 锁定编码回退链的**优先级**：
// HTTP 头声明 gbk（真实且可解），页面 <meta> 撒谎说 big5。必须按头解出中文。
//
// 这条与 TestFetcher_CharsetHintWins 的区别在于两个声明**互相冲突**——
// 前者 header 与 meta 都声明 gbk，谁胜出结果都一样，抓不到优先级错误。
// 而「header 真实、meta 撒谎」正是本功能存在的理由：被 meta 顶掉会导致
// 整页乱码或报错。（Task 3 复审用一个「可解 hint 被 meta 顶掉」的变异实测过：
// 那种实现下全包 96 个 case 全绿，只有冲突用例能抓到。）
func TestFetcher_HeaderWinsOverLyingMeta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=gbk")
		// 正文是 GBK 字节，但 meta 谎称 big5
		enc, _ := simplifiedchinese.GBK.NewEncoder().Bytes(
			[]byte(`<html><head><meta charset="big5"></head><body><p>中文内容</p></body></html>`))
		_, _ = w.Write(enc)
	}))
	defer srv.Close()

	f := NewFetcher("ua", 5*time.Second, 1<<20)
	body, err := f.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch error: %v", err)
	}
	if !strings.Contains(string(body), "中文内容") {
		t.Errorf("HTTP 头声明的 gbk 必须优先于撒谎的 <meta charset=\"big5\">，got %q", body)
	}
}

// TestFetcher_GBKFallback 覆盖设计 §4.2 第 5 步编码回退链的后两步：
// HTTP 头与 <meta> 都没声明编码、且字节不是合法 UTF-8 时，按 GBK 兜底。
func TestFetcher_GBKFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 刻意只写 text/html，不带 charset；页面里也没有 <meta charset>
		w.Header().Set("Content-Type", "text/html")
		enc, _ := simplifiedchinese.GBK.NewEncoder().Bytes(
			[]byte(`<html><body><p>中文内容</p></body></html>`))
		_, _ = w.Write(enc)
	}))
	defer srv.Close()

	f := NewFetcher("ua", 5*time.Second, 1<<20)
	body, err := f.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch error: %v", err)
	}
	if !utf8.Valid(body) {
		t.Fatalf("decoded body must be valid UTF-8, got %q", body)
	}
	if !strings.Contains(string(body), "中文内容") {
		t.Errorf("undeclared gbk content should fall back to gbk, got %q", body)
	}
}

func TestFetcher_NonHTMLErrors(t *testing.T) {
	tests := []struct {
		name string
		hdr  string
		want string
	}{
		{"pdf", "application/pdf", "is not html"},
		{"plain", "text/plain", "is not html"},
		{"json", "application/json", "is not html"},
		// 空串 Content-Type：mime.ParseMediaType 会报错，isHTMLContentType 因此走到
		// 「按 ; 切出 media type」的 fallback 分支。这条钉的就是那个分支被执行到。
		{"empty", "", "is not html"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.hdr == "" {
					// 必须真的发一个空串：Content-Type 为空时 net/http 会自行 sniff
					// （"x" 会被判成 text/plain），那样测的就不是 fallback 分支了。
					w.Header()["Content-Type"] = []string{""}
				} else {
					w.Header().Set("Content-Type", tt.hdr)
				}
				_, _ = w.Write([]byte("x"))
			}))
			defer srv.Close()

			f := NewFetcher("ua", 5*time.Second, 1<<20)
			_, err := f.Fetch(context.Background(), srv.URL)
			if err == nil {
				t.Fatal("expected error for non-html content type")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want substring %q", err, tt.want)
			}
		})
	}
}

// TestFetcher_AcceptsXHTML 钉住 Fetch 对外声明的契约成对成立：
// 请求里发的是 Accept: text/html,application/xhtml+xml，isHTMLContentType 就必须同样放行
// application/xhtml+xml。只认 text/html 的话，XHTML 站点会整站抓取失败。
// 带与不带 charset 两种写法都要能用（裸的 media type 走另一条 charset 分支）。
func TestFetcher_AcceptsXHTML(t *testing.T) {
	const bodyText = `<?xml version="1.0"?><html xmlns="http://www.w3.org/1999/xhtml"><body><p>xhtml body</p></body></html>`

	for _, ct := range []string{"application/xhtml+xml", "application/xhtml+xml; charset=utf-8"} {
		t.Run(ct, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// 我们对外宣称接受 xhtml，实际实现也必须接受它——两者必须一致。
				if accept := r.Header.Get("Accept"); !strings.Contains(accept, "application/xhtml+xml") {
					t.Errorf("Accept = %q, want it to include application/xhtml+xml", accept)
				}
				w.Header().Set("Content-Type", ct)
				_, _ = w.Write([]byte(bodyText))
			}))
			defer srv.Close()

			f := NewFetcher("ua", 5*time.Second, 1<<20)
			body, err := f.Fetch(context.Background(), srv.URL)
			if err != nil {
				t.Fatalf("Content-Type %q must be accepted, error = %v", ct, err)
			}
			if !strings.Contains(string(body), "<p>xhtml body</p>") {
				t.Errorf("body = %q, want it to contain the original markup", body)
			}
		})
	}
}

func TestFetcher_StatusAndSizeErrors(t *testing.T) {
	t.Run("404", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer srv.Close()
		f := NewFetcher("ua", 5*time.Second, 1<<20)
		_, err := f.Fetch(context.Background(), srv.URL)
		if err == nil || !strings.Contains(err.Error(), "http status 404") {
			t.Errorf("error = %v, want http status 404", err)
		}
	})

	t.Run("oversize", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(strings.Repeat("a", 5000)))
		}))
		defer srv.Close()
		f := NewFetcher("ua", 5*time.Second, 1024) // 上限 1KB，响应 5KB
		_, err := f.Fetch(context.Background(), srv.URL)
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Errorf("error = %v, want size-exceeded", err)
		}
	})

	// 边界：响应体**恰好**等于 maxHTMLSize 时必须成功。上面那条 oversize 是 5000 vs 1024，
	// 钉不住边界——实现写成 `len(body) >= f.maxHTMLSize` 时，刚好达标的页面会被误杀，
	// 而 oversize 用例照样通过（删掉 `+1` 的 LimitReader 变异同理）。
	t.Run("exactly at limit", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(strings.Repeat("a", 1024)))
		}))
		defer srv.Close()
		f := NewFetcher("ua", 5*time.Second, 1024) // 上限 1KB，响应恰好 1KB
		body, err := f.Fetch(context.Background(), srv.URL)
		if err != nil {
			t.Fatalf("body exactly at maxHTMLSize must be accepted, error = %v", err)
		}
		if len(body) != 1024 {
			t.Errorf("len(body) = %d, want the full 1024 bytes", len(body))
		}
	})
}

func TestFetcher_TooManyRedirects(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/next", http.StatusFound)
	}))
	defer srv.Close()

	f := NewFetcher("ua", 5*time.Second, 1<<20)
	_, err := f.Fetch(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected redirect error")
	}
	// 断言到常量值本身，而不是只断言「与重定向有关」：http.Client 的默认策略
	// （10 跳）也会返回 "stopped after 10 redirects"，只断言 "redirect" 的话，
	// 把 CheckRedirect 整个删掉这条测试依然通过——等于没测到 maxRedirects。
	if !strings.Contains(err.Error(), "stopped after 5") {
		t.Errorf("error = %q, want the cap in maxRedirects (got the default 10?)", err)
	}
}

// TestFetcher_RedirectCapBoundary 钉住上限的**边界值本身**：恰好 5 跳必须成功。
//
// 为什么需要：`CheckRedirect` 的 `via` **含最初那次请求**，所以 `len(via) >= 5`
// 实际只放行 **4** 跳——与设计 §4.2 第 3 步及常量注释承诺的「最多 5 跳」差一跳。
// 上面那条 TestFetcher_TooManyRedirects 用的是无限重定向站，4 跳与 5 跳在它眼里
// 没有区别，所以这个差一在整个测试集里不可见（差一与「整个策略被删掉」都表现为
// 「会终止」，它只挡得住后者）。
func TestFetcher_RedirectCapBoundary(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n > 0 {
			http.Redirect(w, r, srv.URL+"/?n="+strconv.Itoa(n-1), http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><p>ok</p></body></html>"))
	}))
	defer srv.Close()

	f := NewFetcher("ua", 5*time.Second, 1<<20)

	// 恰好 5 跳：这是设计承诺的上限，必须成功
	if _, err := f.Fetch(context.Background(), srv.URL+"/?n=5"); err != nil {
		t.Errorf("5 跳重定向必须成功（设计 §4.2 第 3 步的上限），error = %v", err)
	}
	// 6 跳：必须被拒
	if _, err := f.Fetch(context.Background(), srv.URL+"/?n=6"); err == nil {
		t.Error("6 跳重定向必须被 maxRedirects 拒绝")
	}
}
