package crawler

import (
	"fmt"
	"net"
	"net/url"
	"path"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/publicsuffix"
)

// staticAssetExts 静态资源后缀，命中则不入队（内容不是网页正文）。
var staticAssetExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true,
	".webp": true, ".bmp": true, ".ico": true, ".css": true, ".js": true,
	".mjs": true, ".map": true, ".pdf": true, ".zip": true, ".tar": true,
	".gz": true, ".rar": true, ".7z": true, ".mp4": true, ".mp3": true,
	".avi": true, ".mov": true, ".wmv": true, ".woff": true, ".woff2": true,
	".ttf": true, ".otf": true, ".eot": true, ".xml": true, ".rss": true,
	".atom": true, ".json": true, ".dmg": true, ".exe": true, ".apk": true,
}

// NormalizeURL 规范化 URL：scheme 仅允许 http/https、host 转小写、
// 去掉默认端口与 fragment。非法输入返回 error（POST 阶段据此回 400）。
func NormalizeURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty url")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid url %q: %w", raw, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme %q in url %q", u.Scheme, raw)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("missing host in url %q", raw)
	}
	u.Scheme = scheme
	u.Host = strings.ToLower(u.Host)
	if (scheme == "http" && strings.HasSuffix(u.Host, ":80")) ||
		(scheme == "https" && strings.HasSuffix(u.Host, ":443")) {
		u.Host = u.Host[:strings.LastIndex(u.Host, ":")]
	}
	u.Fragment = ""
	u.RawFragment = ""
	return u, nil
}

// SameHost 判断两个 URL 是否同站：基准为 eTLD+1（设计 §3.3），
// doc.example.com 与 api.example.com 判为同站；IP 字面量或无法解析的 host
// 退化为 host 全等比较；端口不参与比较。
func SameHost(a, b *url.URL) bool {
	ra, rb := registrableDomain(a.Host), registrableDomain(b.Host)
	return ra != "" && ra == rb
}

// registrableDomain 返回 host 的 eTLD+1；无法解析时退化为去端口的 host 本身。
func registrableDomain(host string) string {
	h := stripPort(strings.ToLower(host))
	if h == "" || net.ParseIP(h) != nil {
		return h
	}
	d, err := publicsuffix.EffectiveTLDPlusOne(h)
	if err != nil {
		return h
	}
	return d
}

// stripPort 去掉 host 中的端口，正确处理 IPv6 字面量（[::1]:8080）。
func stripPort(host string) string {
	if strings.HasPrefix(host, "[") {
		if i := strings.Index(host, "]"); i >= 0 {
			return host[1:i]
		}
		return host
	}
	if i := strings.LastIndex(host, ":"); i >= 0 {
		return host[:i]
	}
	return host
}

// IsStaticAsset 判断路径是否指向静态资源。
func IsStaticAsset(p string) bool {
	ext := strings.ToLower(path.Ext(p))
	if ext == "" {
		return false
	}
	return staticAssetExts[ext]
}

// ExtractLinks 从**整个文档**提取规范化后的绝对链接（设计 §4.2 第 10 步：
// 必须从整个文档取，否则导航栏中的分页链接无法被发现）。
// 跳过 fragment-only / mailto / 非 http(s) / 静态资源 / 重复项。
func ExtractLinks(doc *html.Node, base *url.URL) []string {
	var out []string
	seen := make(map[string]bool)

	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			href := getAttr(n, "href")
			if abs, ok := resolveLink(base, href); ok && !seen[abs] {
				seen[abs] = true
				out = append(out, abs)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

// resolveLink 把 href 解析为相对 base 的规范化绝对 URL；不可用则返回 false。
func resolveLink(base *url.URL, href string) (string, bool) {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return "", false
	}
	ref, err := url.Parse(href)
	if err != nil {
		return "", false
	}
	if ref.Scheme != "" && ref.Scheme != "http" && ref.Scheme != "https" {
		return "", false // mailto: / javascript: / tel: 等
	}
	abs := base.ResolveReference(ref)
	if IsStaticAsset(abs.Path) {
		return "", false
	}
	u, err := NormalizeURL(abs.String())
	if err != nil {
		return "", false
	}
	return u.String(), true
}

// ShouldEnqueue 判断一个已规范化的绝对链接是否应进入抓取 frontier。
// 这是设计 §4.2 第 11 步「入队过滤」的唯一实现点：未 visited + 可解析 + 同域。
// **必须由 Crawl 的 BFS 调用**，否则爬虫会跟随页面上所有外链（等于爬全网）。
func ShouldEnqueue(link string, entry *url.URL, visited map[string]bool) bool {
	if visited[link] {
		return false
	}
	u, err := url.Parse(link)
	if err != nil {
		return false
	}
	return SameHost(entry, u)
}

// getAttr 读取节点属性值。
func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
