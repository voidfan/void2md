package parse

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
)

// SelectHTML extracts the **first** element matching a CSS selector from HTML
// bytes, re-serialized to HTML bytes. An empty/blank selector means "body";
// no match returns a zero-length byte slice with nil error (the caller applies
// the "empty page rule" to skip, see design §4.2 step 7).
func SelectHTML(htmlBytes []byte, selector string) ([]byte, error) {
	doc, err := html.Parse(bytes.NewReader(htmlBytes))
	if err != nil {
		return nil, fmt.Errorf("html parse error: %w", err)
	}
	return SelectNode(doc, selector)
}

// SelectNode is like SelectHTML but operates on a parsed node (not limited to
// the document root — any subtree root is valid), letting callers reuse one DOM
// (internal/crawler needs both link extraction and content scoping).
//
// Semantics match cascadia.Selector.MatchFirst: **the selector also matches the
// passed node itself**, not just its descendants (cascadia.Query is
// "exclude self, search descendants only"). So when a subtree root is passed
// and matches the selector, it must return itself; with cascadia.Query this
// case would return zero-length bytes and the caller would drop the whole page
// as "selector matched nothing".
//
// Note: the only current caller (internal/crawler) passes the **document root**,
// which no CSS selector matches, so both APIs behave identically there and the
// difference is invisible today. This self-inclusive semantics is pinned for
// the supported "pass a subtree root" usage — **do not switch to cascadia.Query
// just because it isn't triggered today**; future callers reusing one DOM for
// multi-level scoping would silently lose pages.
func SelectNode(doc *html.Node, selector string) ([]byte, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		selector = "body"
	}
	sel, err := cascadia.Compile(selector)
	if err != nil {
		return nil, fmt.Errorf("invalid selector %q: %w", selector, err)
	}
	node := sel.MatchFirst(doc)
	if node == nil {
		return nil, nil
	}
	var buf bytes.Buffer
	if err := html.Render(&buf, node); err != nil {
		return nil, fmt.Errorf("html render error: %w", err)
	}
	return buf.Bytes(), nil
}
