package crawler

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"lowercase host", "https://Doc.Example.COM/A", "https://doc.example.com/A", false},
		{"drop fragment", "https://a.com/p#sec", "https://a.com/p", false},
		{"drop default http port", "http://a.com:80/p", "http://a.com/p", false},
		{"drop default https port", "https://a.com:443/p", "https://a.com/p", false},
		{"keep non-default port", "https://a.com:8443/p", "https://a.com:8443/p", false},
		{"reject ftp", "ftp://a.com/p", "", true},
		{"reject file", "file:///etc/passwd", "", true},
		// 缺 host 必须报错：POST 阶段「非法输入回 400」的契约依据。
		// 注意此处不能靠 raw=="" 那条分支兜住——"http:///a" 能成功 Parse 出来，
		// 只有显式的空 host 校验能拦下它。
		{"reject missing host", "http:///a", "", true},
		{"reject empty", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := NormalizeURL(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NormalizeURL(%q) expected error, got %v", tt.in, u)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeURL(%q) error: %v", tt.in, err)
			}
			if u.String() != tt.want {
				t.Errorf("NormalizeURL(%q) = %q, want %q", tt.in, u.String(), tt.want)
			}
		})
	}
}

// TestSameHost 覆盖设计 §3.3：基准是 eTLD+1，同域含子域；IP 字面量退化为 host 全等。
func TestSameHost(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{"same host", "https://doc.example.com/a", "https://doc.example.com/b", true},
		{"subdomain same etld+1", "https://doc.example.com/", "https://api.example.com/", true},
		{"different etld+1", "https://doc.example.com/", "https://doc.example.org/", false},
		{"different site", "https://doc.example.com/", "https://evil.com/", false},
		{"ip literal equal", "http://127.0.0.1/a", "http://127.0.0.1/b", true},
		{"ip literal different", "http://127.0.0.1/a", "http://127.0.0.2/b", false},
		{"ip vs hostname", "http://127.0.0.1/a", "http://localhost/b", false},
		// IPv6 字面量：stripPort 必须先剥方括号再剥端口，否则 "[::1]" 被切成 "[:"，
		// 不同地址会被判成同域（爬虫会顺着跨站链接爬到别的机器上）。
		{"ipv6 equal", "http://[::1]/a", "http://[::1]/b", true},
		{"ipv6 different", "http://[::1]/a", "http://[::2]/b", false},
		{"ipv6 port ignored", "http://[::1]:8080/a", "http://[::1]/b", true},
		{"port ignored", "https://doc.example.com/", "https://doc.example.com:8443/", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ua, err := NormalizeURL(tt.a)
			if err != nil {
				t.Fatalf("bad fixture %q: %v", tt.a, err)
			}
			ub, err := NormalizeURL(tt.b)
			if err != nil {
				t.Fatalf("bad fixture %q: %v", tt.b, err)
			}
			if got := SameHost(ua, ub); got != tt.want {
				t.Errorf("SameHost(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestIsStaticAsset(t *testing.T) {
	static := []string{"/a.png", "/a.JPG", "/style.css", "/app.js", "/doc.pdf", "/a.zip", "/font.woff2", "/v.mp4"}
	for _, p := range static {
		if !IsStaticAsset(p) {
			t.Errorf("IsStaticAsset(%q) = false, want true", p)
		}
	}
	page := []string{"/", "/docs/intro", "/docs/intro/", "/a.html", "/b.htm", "/noext", "/style.css.html"}
	for _, p := range page {
		if IsStaticAsset(p) {
			t.Errorf("IsStaticAsset(%q) = true, want false", p)
		}
	}
}

// TestExtractLinks 覆盖设计 §4.2 第 10 步的链接提取与过滤。
//
// 带查询串的链接是「静态资源判定必须看 Path 而不是整串」的判别用例：
// 判据若写成 abs.String()，"…/img.png?v=2" 的后缀会变成 ".png?v=2"（不入表 → 误入队），
// 而 "…/download?file=a.png" 的后缀会变成 ".png"（误丢）。
// 两者方向相反，任何一侧写错都会被下面的长度断言抓住。
func TestExtractLinks(t *testing.T) {
	const page = `<html><body>
		<a href="/a">A</a>
		<a href="sub/b.html">B</a>
		<a href="https://doc.example.com/c">C</a>
		<a href="https://other.com/d">D</a>
		<a href="#top">skip fragment</a>
		<a href="mailto:x@y.com">skip mailto</a>
		<a href="/img.png">skip asset</a>
		<a href="/img.png?v=2">skip asset with query</a>
		<a href="/style.css?v=1">skip asset with query</a>
		<a href="/download?file=a.png">keep: ext-less path with a query</a>
		<a href="/a">dup</a>
		<a name="anchor">no href</a>
	</body></html>`

	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	base, _ := NormalizeURL("https://doc.example.com/docs/")

	got := ExtractLinks(doc, base)
	want := map[string]bool{
		"https://doc.example.com/a":                   true,
		"https://doc.example.com/docs/sub/b.html":     true,
		"https://doc.example.com/c":                   true,
		"https://other.com/d":                         true,
		"https://doc.example.com/download?file=a.png": true,
	}
	if len(got) != len(want) {
		t.Fatalf("ExtractLinks = %v (len %d), want %d entries", got, len(got), len(want))
	}
	for _, u := range got {
		if !want[u] {
			t.Errorf("unexpected link %q", u)
		}
	}
}

// TestShouldEnqueue 是设计 §4.2 第 11 步「入队过滤」的核心断言：
// 跨域链接必须被拒绝，同域（含子域）必须被接受，已访问必须被拒绝。
//
// 这条测试用**纯 URL 单元测试**而非 httptest 集成测试，原因见
// TestCrawl_CrossDomainNotFollowed 的说明——httptest 起不出真正的跨域站点。
func TestShouldEnqueue(t *testing.T) {
	entry, err := NormalizeURL("https://doc.example.com/docs/")
	if err != nil {
		t.Fatalf("bad entry fixture: %v", err)
	}
	visited := map[string]bool{"https://doc.example.com/seen": true}

	tests := []struct {
		name string
		link string
		want bool
	}{
		{"same host", "https://doc.example.com/a", true},
		{"subdomain same etld+1", "https://api.example.com/a", true},
		{"cross etld+1", "https://other.com/d", false},
		{"cross etld+1 lookalike", "https://notexample.com/a", false},
		{"cross subdomain of different tld", "https://doc.example.org/a", false},
		{"already visited", "https://doc.example.com/seen", false},
		{"unparseable", "://bad", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldEnqueue(tt.link, entry, visited); got != tt.want {
				t.Errorf("ShouldEnqueue(%q) = %v, want %v", tt.link, got, tt.want)
			}
		})
	}
}
