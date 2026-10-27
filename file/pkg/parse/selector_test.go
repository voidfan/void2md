package parse

import (
	"context"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// fixtureBody is selectorFixture's <body> content, extracted for the
// cross-check test (design §1.1): the cross-check must compare two delivery
// paths of **the same subtree**; differing content would fail the assertion.
const fixtureBody = `<nav><a href="/a">A</a></nav>` +
	`<div id="main" class="docs-content" data-kind="guide"><h1>Hello</h1><p>World</p></div>` +
	`<div class="docs-content">second</div>`

const selectorFixture = `<html><head><title>My Docs Title</title></head><body>` +
	fixtureBody + `</body></html>`

// bodyOnlyDoc holds only the same body content, equivalent to "handing this
// subtree to /v1/pdfparse as a .html file". It differs from selectorFixture only in <head><title>.
const bodyOnlyDoc = `<html><body>` + fixtureBody + `</body></html>`

func TestSelectHTML_BasicSelectors(t *testing.T) {
	tests := []struct {
		name     string
		selector string
		want     string // required substring of the serialized result
		absent   string // substring that must **not** appear
	}{
		{"tag", "h1", "<h1>Hello</h1>", "World"},
		{"id", "#main", "World", "second"},
		{"class", ".docs-content", "<h1>Hello</h1>", ""},
		{"descendant", "div p", "World", "second"},
		{"attribute", `[data-kind="guide"]`, "World", "second"},
		// cascadia's MatchFirst returns the first hit in **document order**, not
		// "the first selector's hit". In the fixture <nav> precedes <h1>, so
		// "nav, h1" matches <nav>. The second case **reverses** the selector
		// order ("h1, nav") and still asserts <nav> — that is what proves
		// independence from selector order; a single "nav, h1" case cannot
		// distinguish "document order" from "the first selector happens to come first".
		{"comma multi returns first in document order", "nav, h1", "<nav>", ""},
		{"comma multi ignores selector order", "h1, nav", "<nav>", "Hello"},
		{"default is body", "", "<nav>", ""},
		// A whitespace-only selector also defaults to "body" (design §2.1):
		// implemented via strings.TrimSpace(selector), and {"selector": "   "} is
		// a value clients can send unvalidated. Previously zero coverage —
		// replacing it with a no-op survived the whole package suite.
		{"default is body for whitespace-only", "   \t\n ", "<nav>", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SelectHTML([]byte(selectorFixture), tt.selector)
			if err != nil {
				t.Fatalf("SelectHTML(%q) error: %v", tt.selector, err)
			}
			if !strings.Contains(string(got), tt.want) {
				t.Errorf("SelectHTML(%q) = %q, want substring %q", tt.selector, got, tt.want)
			}
			if tt.absent != "" && strings.Contains(string(got), tt.absent) {
				t.Errorf("SelectHTML(%q) = %q, must not contain %q", tt.selector, got, tt.absent)
			}
		})
	}
}

// TestSelectHTML_NoMatch pins the behavior behind review issue #5: zero matches
// returns empty bytes without error. The layer above then applies the
// "empty page rule" (design §4.2 step 7) to skip the page.
func TestSelectHTML_NoMatch(t *testing.T) {
	got, err := SelectHTML([]byte(selectorFixture), ".does-not-exist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("no-match should return empty bytes, got %q", got)
	}
}

// TestSelectHTML_InvalidSelector, beyond "must error", pins cascadia.Compile's
// **error wrapping**: the error must carry the offending selector
// (fmt.Errorf("invalid selector %q: %w") in selector.go). Asserting only
// err != nil lets a mutation replacing the wrap with `return nil, err` survive,
// leaving the "error names the bad selector" promise unguarded.
func TestSelectHTML_InvalidSelector(t *testing.T) {
	_, err := SelectHTML([]byte(selectorFixture), "div >>>> p")
	if err == nil {
		t.Fatal("invalid selector should return error")
	}
	if !strings.Contains(err.Error(), "invalid selector") {
		t.Errorf("error must wrap the offending selector (with the \"invalid selector\" prefix), got %q", err)
	}
}

// TestSelectHTML_MatchesSubtreeRender — the cross-check of design §1.1.
//
// Asserts: rendering the <body> subtree standalone is byte-identical to
// "scoping body out of the full document, then rendering". I.e. both interfaces
// share one rendering path; the only difference is input scope.
func TestSelectHTML_MatchesSubtreeRender(t *testing.T) {
	ctx := context.Background()

	// Left: render the same body subtree as a standalone document (equivalent to /v1/pdfparse upload)
	want, _, err := ParseHTMLBytesToMarkdown(ctx, []byte(bodyOnlyDoc), "x.html")
	if err != nil {
		t.Fatalf("direct render error: %v", err)
	}

	// Right: scope body out of the full document, then use the same renderer (the /v1/htmlparse path)
	scoped, err := SelectHTML([]byte(selectorFixture), "body")
	if err != nil {
		t.Fatalf("SelectHTML error: %v", err)
	}
	got, _, err := ParseHTMLBytesToMarkdownWithCharset(ctx, scoped, "x.html", "utf-8")
	if err != nil {
		t.Fatalf("scoped render error: %v", err)
	}

	if got != want {
		t.Errorf("subtree render mismatch:\n got = %q\nwant = %q", got, want)
	}
}

// TestSelectHTML_TitleLeakDocumentsTheCorrection pins the mechanism of the §1.1
// correction: rendering the full document treats <title> text as content, body
// scoping does not. If someone later adds a head/title branch to renderNode,
// this test fails, reminding them that the §1.1/§8.3 trade-off needs re-evaluation.
func TestSelectHTML_TitleLeakDocumentsTheCorrection(t *testing.T) {
	ctx := context.Background()

	full, _, err := ParseHTMLBytesToMarkdown(ctx, []byte(selectorFixture), "x.html")
	if err != nil {
		t.Fatalf("full render error: %v", err)
	}
	if !strings.Contains(full, "My Docs Title") {
		t.Fatalf("full-document render should contain the <title> text (the very reason for the §1.1 correction), got %q", full)
	}

	scoped, err := SelectHTML([]byte(selectorFixture), "body")
	if err != nil {
		t.Fatalf("SelectHTML error: %v", err)
	}
	bodyMD, _, err := ParseHTMLBytesToMarkdownWithCharset(ctx, scoped, "x.html", "utf-8")
	if err != nil {
		t.Fatalf("scoped render error: %v", err)
	}
	if strings.Contains(bodyMD, "My Docs Title") {
		t.Errorf("body-scoped render must not contain the <title> text, got %q", bodyMD)
	}
}

// firstElement returns the first element with the given tag in n's subtree (DFS); nil if none.
func firstElement(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := firstElement(c, tag); found != nil {
			return found
		}
	}
	return nil
}

// TestSelectNode_MatchesRootItself pins SelectNode's **load-bearing semantics**:
// the selector applies to the passed node itself.
//
// The implementation uses cascadia.Selector.MatchFirst (**includes** the passed
// node), not cascadia.Query (starts at children, **excludes** self). The two
// diverge only when the input is not the document root, so the existing tests
// that only call SelectHTML (always the document root) cannot catch it —
// swapping MatchFirst for the "cleaner-looking" cascadia.Query keeps the whole
// package suite green.
//
// The consequence is not minor: internal/crawler reuses one DOM for link
// extraction and content scoping; when a subtree root matches itself but gets
// zero-length bytes back, the layer above drops the whole page as
// "selector matched nothing".
func TestSelectNode_MatchesRootItself(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(selectorFixture))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	body := firstElement(doc, "body")
	if body == nil {
		t.Fatal("fixture broken: no <body> found after parsing selectorFixture")
	}

	got, err := SelectNode(body, "body")
	if err != nil {
		t.Fatalf("SelectNode(body, \"body\") error: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("passing the <body> subtree root with selector \"body\": the root matching itself must return itself. " +
			"Returning zero-length bytes is equivalent to cascadia.Query (excludes self), and the layer above would drop the whole page")
	}
	if !strings.Contains(string(got), "World") {
		t.Errorf("SelectNode(body, \"body\") = %q, want the passed subtree's content (World)", got)
	}

	// Asserting non-empty is not enough: a self-excluding implementation would
	// find a sibling or descendant element **elsewhere**, also non-empty. This
	// fixture has two same-tag elements; assert the result comes from the passed
	// subtree (id="a"), not sibling id="b".
	siblingDoc, err := html.Parse(strings.NewReader(
		`<html><body><div id="a">apple</div><div id="b">banana</div></body></html>`))
	if err != nil {
		t.Fatalf("parse sibling fixture: %v", err)
	}
	divA := firstElement(siblingDoc, "div")
	if divA == nil || getAttr(divA, "id") != "a" {
		t.Fatal("fixture broken: first <div> is not id=\"a\"")
	}

	gotA, err := SelectNode(divA, "div")
	if err != nil {
		t.Fatalf("SelectNode(div#a, \"div\") error: %v", err)
	}
	if !strings.Contains(string(gotA), `id="a"`) || !strings.Contains(string(gotA), "apple") {
		t.Errorf("SelectNode(div#a, \"div\") = %q, want the passed subtree itself (id=\"a\" / apple)", gotA)
	}
	if strings.Contains(string(gotA), "banana") {
		t.Errorf("SelectNode(div#a, \"div\") = %q, must not return sibling id=\"b\" content", gotA)
	}
}
