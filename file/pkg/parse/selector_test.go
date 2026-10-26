package parse

import (
	"context"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// fixtureBody 是 selectorFixture 的 <body> 内容，单独抽出供对拍测试使用
// （设计 §1.1）：对拍必须比较**同一份子树**的两种投递方式，
// 若两侧内容不同，断言必然失败。
const fixtureBody = `<nav><a href="/a">A</a></nav>` +
	`<div id="main" class="docs-content" data-kind="guide"><h1>Hello</h1><p>World</p></div>` +
	`<div class="docs-content">second</div>`

const selectorFixture = `<html><head><title>My Docs Title</title></head><body>` +
	fixtureBody + `</body></html>`

// bodyOnlyDoc 只含同一份 body 内容，等价于「把这棵子树当成一个 .html 文件
// 交给 /v1/pdfparse」。它与 selectorFixture 的差异只在 <head><title>。
const bodyOnlyDoc = `<html><body>` + fixtureBody + `</body></html>`

func TestSelectHTML_BasicSelectors(t *testing.T) {
	tests := []struct {
		name     string
		selector string
		want     string // 期望序列化结果中的必需子串
		absent   string // 期望**不**出现的子串
	}{
		{"tag", "h1", "<h1>Hello</h1>", "World"},
		{"id", "#main", "World", "second"},
		{"class", ".docs-content", "<h1>Hello</h1>", ""},
		{"descendant", "div p", "World", "second"},
		{"attribute", `[data-kind="guide"]`, "World", "second"},
		// cascadia 的 MatchFirst 返回**文档序**首个命中节点，而非「第一个选择器的命中」。
		// fixture 中 <nav> 在 <h1> 之前，故 "nav, h1" 命中 <nav>。
		// 下面第二条把选择器顺序**反过来**（"h1, nav"）仍断言 <nav>——
		// 这才真正证明了「与选择器书写顺序无关」，而单条 "nav, h1" 无法区分
		// 「文档序」与「恰好第一个选择器也先出现」两种解释。
		{"comma multi returns first in document order", "nav, h1", "<nav>", ""},
		{"comma multi ignores selector order", "h1, nav", "<nav>", "Hello"},
		{"default is body", "", "<nav>", ""},
		// selector 只含空白时同样走默认值 "body"（设计 §2.1）：实现靠
		// strings.TrimSpace(selector) 兑现，而 {"selector": "   "} 是客户端可以不经验证
		// 直接送进来的取值。此前零覆盖——把它换成 no-op 能活过整个包套件。
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

// TestSelectHTML_NoMatch 锁定评审问题 #5 的底层行为：零匹配返回空字节且不报错。
// 上层据此走「空页规则」（设计 §4.2 第 7 步）跳过该页。
func TestSelectHTML_NoMatch(t *testing.T) {
	got, err := SelectHTML([]byte(selectorFixture), ".does-not-exist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("no-match should return empty bytes, got %q", got)
	}
}

// TestSelectHTML_InvalidSelector 除了「必须报错」外，还锁定 cascadia.Compile 的**错误包装**：
// 错误里必须带上出错的 selector（selector.go 的 fmt.Errorf("invalid selector %q: %w")）。
// 只断言 err != nil 时，把包装整个换成 `return nil, err` 的变异能存活，
// 「错误信息里带上出错的 selector」这个承诺就无人守。
func TestSelectHTML_InvalidSelector(t *testing.T) {
	_, err := SelectHTML([]byte(selectorFixture), "div >>>> p")
	if err == nil {
		t.Fatal("invalid selector should return error")
	}
	if !strings.Contains(err.Error(), "invalid selector") {
		t.Errorf("错误必须包装出错的 selector（含 \"invalid selector\" 前缀），got %q", err)
	}
}

// TestSelectHTML_MatchesSubtreeRender —— 设计 §1.1 的对拍口径。
//
// 断言：把 <body> 子树单独交给渲染器，与「整文档裁剪出 body 再交给渲染器」，
// 输出逐字节相同。即两个接口的渲染逻辑是同一个，差异只来自输入范围。
func TestSelectHTML_MatchesSubtreeRender(t *testing.T) {
	ctx := context.Background()

	// 左：把同一份 body 子树当独立文档渲染（等价 /v1/pdfparse 上传该子树）
	want, _, err := ParseHTMLBytesToMarkdown(ctx, []byte(bodyOnlyDoc), "x.html")
	if err != nil {
		t.Fatalf("direct render error: %v", err)
	}

	// 右：从整文档裁剪出 body，再走同一个渲染器（/v1/htmlparse 的路径）
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

// TestSelectHTML_TitleLeakDocumentsTheCorrection 钉住 §1.1 修正的机制：
// 整文档直调会把 <title> 文本当正文，body 裁剪则不会。
// 若将来有人给 renderNode 补上 head/title 分支，这条测试会失败，
// 提醒他设计文档 §1.1 与 §8.3 的取舍需要重新评估。
func TestSelectHTML_TitleLeakDocumentsTheCorrection(t *testing.T) {
	ctx := context.Background()

	full, _, err := ParseHTMLBytesToMarkdown(ctx, []byte(selectorFixture), "x.html")
	if err != nil {
		t.Fatalf("full render error: %v", err)
	}
	if !strings.Contains(full, "My Docs Title") {
		t.Fatalf("整文档渲染应包含 <title> 文本（这正是 §1.1 修正的原因），got %q", full)
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
		t.Errorf("body 裁剪渲染不得包含 <title> 文本, got %q", bodyMD)
	}
}

// firstElement 深度优先返回 n 子树中首个指定标签的元素节点；找不到返回 nil。
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

// TestSelectNode_MatchesRootItself 锁定 SelectNode 的**承重语义**：选择器作用于传入节点自身。
//
// 实现用的是 cascadia.Selector.MatchFirst（**含**传入节点自身），而不是 cascadia.Query
// （从子节点起搜、**排除**自身）。两者只在「传入的不是文档根」时才分道扬镳，
// 所以既有那批只调 SelectHTML（永远传文档根）的测试全都抓不到这个差异——
// 把 MatchFirst 换成「看起来更干净的」cascadia.Query，整个包套件照样全绿。
//
// 后果不是小事：internal/crawler 复用同一个 DOM 做链接提取与正文裁剪，
// 子树根自己命中却拿到长度为 0 的字节切片时，上层会按
// 「selector matched nothing」把整页丢掉。
func TestSelectNode_MatchesRootItself(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(selectorFixture))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	body := firstElement(doc, "body")
	if body == nil {
		t.Fatal("fixture 失效：解析 selectorFixture 后找不到 <body>")
	}

	got, err := SelectNode(body, "body")
	if err != nil {
		t.Fatalf("SelectNode(body, \"body\") error: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("传入 <body> 子树根、selector 为 \"body\"：子树根自身命中必须返回它自己。" +
			"返回长度为 0 的字节切片等价于 cascadia.Query（排除自身），上层会据此丢弃整页")
	}
	if !strings.Contains(string(got), "World") {
		t.Errorf("SelectNode(body, \"body\") = %q, want 含传入子树的内容（World）", got)
	}

	// 只断言非空还不够：排除自身的实现会在**别处**找到同级或后代元素，结果同样非空。
	// 这棵 fixture 里有两个同名元素，断言结果必须来自传入的那棵子树（id="a"），
	// 而不是兄弟节点 id="b" 的内容。
	siblingDoc, err := html.Parse(strings.NewReader(
		`<html><body><div id="a">apple</div><div id="b">banana</div></body></html>`))
	if err != nil {
		t.Fatalf("parse sibling fixture: %v", err)
	}
	divA := firstElement(siblingDoc, "div")
	if divA == nil || getAttr(divA, "id") != "a" {
		t.Fatal("fixture 失效：首个 <div> 不是 id=\"a\"")
	}

	gotA, err := SelectNode(divA, "div")
	if err != nil {
		t.Fatalf("SelectNode(div#a, \"div\") error: %v", err)
	}
	if !strings.Contains(string(gotA), `id="a"`) || !strings.Contains(string(gotA), "apple") {
		t.Errorf("SelectNode(div#a, \"div\") = %q, want 传入子树自身（id=\"a\" / apple）", gotA)
	}
	if strings.Contains(string(gotA), "banana") {
		t.Errorf("SelectNode(div#a, \"div\") = %q, 不得返回兄弟节点 id=\"b\" 的内容", gotA)
	}
}
