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

// staticAssetExts are path extensions skipped (not page content).
var staticAssetExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true,
	".webp": true, ".bmp": true, ".ico": true, ".css": true, ".js": true,
	".mjs": true, ".map": true, ".pdf": true, ".zip": true, ".tar": true,
	".gz": true, ".rar": true, ".7z": true, ".mp4": true, ".mp3": true,
	".avi": true, ".mov": true, ".wmv": true, ".woff": true, ".woff2": true,
	".ttf": true, ".otf": true, ".eot": true, ".xml": true, ".rss": true,
	".atom": true, ".json": true, ".dmg": true, ".exe": true, ".apk": true,
}

// NormalizeURL normalizes a URL: http/https only, lowercase host,
// strip default port and fragment. Invalid input returns an error
// (used by the POST stage to reply 400).
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

// SameHost reports whether two URLs are on the same site, keyed on eTLD+1
// (design §3.3): doc.example.com and api.example.com are the same site.
// IP literals or unresolvable hosts fall back to full host equality;
// ports are ignored.
func SameHost(a, b *url.URL) bool {
	ra, rb := registrableDomain(a.Host), registrableDomain(b.Host)
	return ra != "" && ra == rb
}

// registrableDomain returns the eTLD+1 of host; falls back to the
// port-stripped host when it cannot be resolved.
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

// stripPort removes the port from host, handling IPv6 literals ([::1]:8080).
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

// IsStaticAsset reports whether the path points at a static asset.
func IsStaticAsset(p string) bool {
	ext := strings.ToLower(path.Ext(p))
	if ext == "" {
		return false
	}
	return staticAssetExts[ext]
}

// ExtractLinks extracts normalized absolute links from the **whole document**
// (design §4.2 step 10: must be the whole document, otherwise pagination
// links in nav bars are never discovered). Skips fragment-only / mailto /
// non-http(s) / static assets / duplicates.
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

// resolveLink resolves href against base into a normalized absolute URL;
// returns false when unusable.
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
		return "", false // mailto: / javascript: / tel: etc.
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

// ShouldEnqueue decides whether a normalized absolute link enters the crawl
// frontier. This is the single implementation point of design §4.2 step 11
// (enqueue filter): not visited + parseable + same domain. **Must be called
// from Crawl's BFS**, otherwise the crawler follows every external link.
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

// getAttr reads a node attribute value.
func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
