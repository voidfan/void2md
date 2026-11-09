package parse

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func renderForTest(t *testing.T, htmlStr string) string {
	t.Helper()
	md, _, err := ParseHTMLBytesToMarkdown(context.Background(), []byte(htmlStr), "test.html")
	if err != nil {
		t.Fatalf("ParseHTMLBytesToMarkdown error: %v", err)
	}
	return md
}

// TestPruneBoilerplate_Chrome covers the structural rules: semantic tags,
// class/id tokens, and link-density containers.
func TestPruneBoilerplate_Chrome(t *testing.T) {
	md := renderForTest(t, `<html><body>
		<nav><a href="/a">NavLink</a></nav>
		<div class="headwrap"><div class="globalNav">GlobalNav</div></div>
		<div class="ad_context1">AdText</div>
		<div id="sidebar">SideText</div>
		<div class="footer2016">FooterText</div>
		<div class="sharebox">ShareText</div>
		<div class="pinglunbox">CommentText</div>
		<div class="mainright">
			<div class="unknown-widget"><a href="/1">HotOne</a><a href="/2">HotTwo</a></div>
		</div>
		<article><h1>Title</h1><p>CoreText</p></article>
	</body></html>`)

	for _, want := range []string{"Title", "CoreText"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing core content %q:\n%s", want, md)
		}
	}
	for _, absent := range []string{
		"NavLink", "GlobalNav", "AdText", "SideText", "FooterText",
		"ShareText", "CommentText", "HotOne", "HotTwo",
	} {
		if strings.Contains(md, absent) {
			t.Errorf("markdown still contains boilerplate %q:\n%s", absent, md)
		}
	}
}

// TestPruneBoilerplate_Hidden covers hidden/aria-hidden/style-hidden nodes.
func TestPruneBoilerplate_Hidden(t *testing.T) {
	md := renderForTest(t, `<html><body>
		<div style="display: none">HiddenStyle</div>
		<div style="visibility:hidden !important">HiddenVisibility</div>
		<div aria-hidden="true">HiddenAria</div>
		<div hidden>HiddenAttr</div>
		<p>Visible</p>
	</body></html>`)

	if !strings.Contains(md, "Visible") {
		t.Errorf("visible content missing:\n%s", md)
	}
	for _, absent := range []string{"HiddenStyle", "HiddenVisibility", "HiddenAria", "HiddenAttr"} {
		if strings.Contains(md, absent) {
			t.Errorf("hidden content %q leaked into markdown:\n%s", absent, md)
		}
	}
}

// TestPruneBoilerplate_BodyClassProtected pins that a CMS-style body class
// (WordPress puts "comment-open", "sidebar-right", etc. on <body>) cannot empty
// the whole page.
func TestPruneBoilerplate_BodyClassProtected(t *testing.T) {
	md := renderForTest(t, `<html><body class="single-post comment-open sidebar-right">
		<p>CoreBody</p>
		<div class="comments">NotCore</div>
	</body></html>`)

	if !strings.Contains(md, "CoreBody") {
		t.Errorf("body content was dropped by its own class list:\n%s", md)
	}
	if strings.Contains(md, "NotCore") {
		t.Errorf("comment block should still be removed:\n%s", md)
	}
}

// TestPruneBoilerplate_MetadataTail covers short metadata/UI blocks identified
// by their text rather than a class name.
func TestPruneBoilerplate_MetadataTail(t *testing.T) {
	md := renderForTest(t, `<html><body>
		<div class="tail">
			<div>文章来源：某报</div>
			<div>责任编辑：123</div>
			<div>原标题：原始标题</div>
			<div>郑重声明：本站内容仅供参考。</div>
			<p>（文章来源：某报）</p>
		</div>
		<p>CoreParagraph</p>
		<p>他决定向监管部门举报此事，并分享到社区。</p>
		<div>用户可以在应用内分享到朋友圈。</div>
	</body></html>`)

	if !strings.Contains(md, "CoreParagraph") {
		t.Errorf("core paragraph missing:\n%s", md)
	}
	// A sentence merely containing these words is real prose, not metadata.
	if !strings.Contains(md, "他决定向监管部门举报此事") {
		t.Errorf("article sentence containing 举报/分享到 must be preserved:\n%s", md)
	}
	// UI phrases must START the block to count; div-based article paragraphs
	// that merely mention them must survive.
	if !strings.Contains(md, "用户可以在应用内分享到朋友圈") {
		t.Errorf("div-based article paragraph containing 分享到 must be preserved:\n%s", md)
	}
	for _, absent := range []string{"文章来源", "责任编辑", "原标题", "郑重声明"} {
		if strings.Contains(md, absent) {
			t.Errorf("metadata tail %q leaked into markdown:\n%s", absent, md)
		}
	}
}

// TestPruneBoilerplate_InlineLinksPreserved pins that link-density filtering
// only drops link-farm containers, never inline links or a link-heavy sentence
// inside article prose.
func TestPruneBoilerplate_InlineLinksPreserved(t *testing.T) {
	md := renderForTest(t, `<html><body><article>
		<h1>Title</h1>
		<p>See <a href="https://example.com/a">the reference</a> for the full explanation of this topic.</p>
		<div class="refs"><a href="/1">RefOne</a><a href="/2">RefTwo</a></div>
	</article></body></html>`)

	if !strings.Contains(md, "[the reference](https://example.com/a)") {
		t.Errorf("inline article link missing:\n%s", md)
	}
	if !strings.Contains(md, "full explanation") {
		t.Errorf("article prose missing:\n%s", md)
	}
	if strings.Contains(md, "RefOne") || strings.Contains(md, "RefTwo") {
		t.Errorf("link-farm div should be removed:\n%s", md)
	}
}

// TestPruneBoilerplate_ContentContainerClasses protects common article
// container class names from the token rules.
func TestPruneBoilerplate_ContentContainerClasses(t *testing.T) {
	md := renderForTest(t, `<html><body>
		<div class="content article-body"><p>ArticleBody</p></div>
		<div class="editor-content"><p>EditorContent</p></div>
		<div class="post-body"><p>PostBody</p></div>
		<div class="shared-content"><p>SharedContent</p></div>
	</body></html>`)

	for _, want := range []string{"ArticleBody", "EditorContent", "PostBody", "SharedContent"} {
		if !strings.Contains(md, want) {
			t.Errorf("content container %q was removed:\n%s", want, md)
		}
	}
}

// TestRender_SkipsEmptyArtifacts covers empty links/images/emphasis and blank
// list items, which used to leave "[](#)", "****" and "- " noise.
func TestRender_SkipsEmptyArtifacts(t *testing.T) {
	md := renderForTest(t, `<html><body>
		<p>Before <a href="#"></a><a href="javascript:void(0)">Action</a> after.</p>
		<p><img src="" alt=""><img src="x.jpg" alt="ok"></p>
		<p><strong></strong><em></em>Text</p>
		<ul><li><a href="#"></a></li><li>Item</li></ul>
	</body></html>`)

	if strings.Contains(md, "[](") {
		t.Errorf("empty link artifact leaked:\n%s", md)
	}
	if strings.Contains(md, "****") {
		t.Errorf("empty emphasis artifact leaked:\n%s", md)
	}
	if !strings.Contains(md, "Action") {
		t.Errorf("text of a javascript: link should be kept:\n%s", md)
	}
	if !strings.Contains(md, "![ok](x.jpg)") {
		t.Errorf("real image missing:\n%s", md)
	}
	if !strings.Contains(md, "- Item") {
		t.Errorf("real list item missing:\n%s", md)
	}
	for _, line := range strings.Split(md, "\n") {
		if strings.TrimSpace(line) == "-" {
			t.Errorf("blank list item leaked:\n%s", md)
		}
	}
}

// TestRender_LazyImageAndTrackingPixel covers lazy-loaded images and 1x1
// analytics pixels.
func TestRender_LazyImageAndTrackingPixel(t *testing.T) {
	md := renderForTest(t, `<html><body>
		<img src="data:image/gif;base64,R0lGODlhAQABAAAAACw=" data-src="real.jpg" alt="pic">
		<img src="pixel.gif" width="1" height="1">
	</body></html>`)

	if !strings.Contains(md, "![pic](real.jpg)") {
		t.Errorf("lazy-loaded image should use data-src:\n%s", md)
	}
	if strings.Contains(md, "pixel.gif") {
		t.Errorf("tracking pixel leaked:\n%s", md)
	}
}

// TestPruneBoilerplate_RealisticArticle is the eastmoney-shaped integration
// fixture: top nav, breadcrumb, title/meta bar, sidebar hot list, tail
// metadata, and footer around two real paragraphs.
func TestPruneBoilerplate_RealisticArticle(t *testing.T) {
	md := renderForTest(t, `<html><body>
		<div class="headwrap">
			<div class="globalNav">财经 股票 基金</div>
			<div class="levelsnav">首页 &gt; 财经频道 &gt; 正文</div>
		</div>
		<div class="contentwrap">
			<div class="topbox">
				<div class="title">央行设立债市非银大额赎回互换便利</div>
				<div class="tipbox">
					<div class="infos">2026年10月28日 06:01 来源：经济日报</div>
					<div class="aboutctrl">小 中 大 东方财富APP</div>
				</div>
			</div>
			<div class="contentbox">
				<div class="mainleft">
					<div class="zwinfos"><div class="txtinfos">
						<p>核心第一段。</p>
						<p>核心第二段。</p>
					</div></div>
					<div class="zwothers">
						<div class="sourcebox">文章来源：经济日报 责任编辑：156</div>
						<div class="sublab">原标题：央行设立债市非银大额赎回互换便利</div>
						<div class="sm">郑重声明：本内容不构成投资建议。</div>
					</div>
				</div>
				<div class="mainright">
					<div class="rmodel"><a href="/1">热门一</a><a href="/2">热门二</a><a href="/3">热门三</a></div>
					<div class="rdggwmodel"><ul><li><a href="/ad1">一键关注</a></li><li><a href="/ad2">财经大咖</a></li></ul></div>
				</div>
			</div>
		</div>
		<div class="footer2016">东方财富产品 证券交易</div>
	</body></html>`)

	for _, want := range []string{
		"央行设立债市非银大额赎回互换便利", "核心第一段。", "核心第二段。",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("core content %q missing:\n%s", want, md)
		}
	}
	for _, absent := range []string{
		"财经 股票 基金", "首页", "2026年10月28日", "小 中 大", "东方财富APP",
		"文章来源", "责任编辑", "原标题", "郑重声明", "热门一", "一键关注",
		"东方财富产品",
	} {
		if strings.Contains(md, absent) {
			t.Errorf("boilerplate %q leaked into markdown:\n%s", absent, md)
		}
	}
}

// TestPruneBoilerplate_FormWrapperPreserved pins that a page-wide <form>
// (ASP.NET WebForms) is not treated as chrome; search-form chrome is still
// removed by its class token.
func TestPruneBoilerplate_FormWrapperPreserved(t *testing.T) {
	md := renderForTest(t, `<html><body>
		<form id="form1" runat="server"><div><p>WebFormsContent</p></div></form>
		<form class="searchbox"><input name="q"><button>搜索</button></form>
	</body></html>`)

	if !strings.Contains(md, "WebFormsContent") {
		t.Errorf("page-wide form wrapper dropped the article:\n%s", md)
	}
	if strings.Contains(md, "搜索") {
		t.Errorf("search form chrome leaked:\n%s", md)
	}
}

// TestPruneBoilerplate_ArticleHeaderKeepsTitle pins the header guard: the
// article title often lives in <header class="entry-header">, which must not be
// dropped along with site chrome.
func TestPruneBoilerplate_ArticleHeaderKeepsTitle(t *testing.T) {
	md := renderForTest(t, `<html><body>
		<header class="entry-header">
			<h1>ArticleTitle</h1>
			<nav><a href="/x">NavText</a></nav>
		</header>
		<p>BodyText</p>
	</body></html>`)

	for _, want := range []string{"ArticleTitle", "BodyText"} {
		if !strings.Contains(md, want) {
			t.Errorf("core content %q missing:\n%s", want, md)
		}
	}
	if strings.Contains(md, "NavText") {
		t.Errorf("nav inside article header should still be removed:\n%s", md)
	}
}

// TestPruneBoilerplate_SiteHeaderWithManyLinks pins the other side of the
// header guard: a heading-bearing site header packed with links is chrome, not
// an article header.
func TestPruneBoilerplate_SiteHeaderWithManyLinks(t *testing.T) {
	md := renderForTest(t, `<html><body>
		<header class="site-header">
			<h1>SiteName</h1>
			<a href="/1">Menu1</a><a href="/2">Menu2</a><a href="/3">Menu3</a>
			<a href="/4">Menu4</a><a href="/5">Menu5</a>
		</header>
		<p>BodyText</p>
	</body></html>`)

	if !strings.Contains(md, "BodyText") {
		t.Errorf("body missing:\n%s", md)
	}
	for _, absent := range []string{"SiteName", "Menu1", "Menu5"} {
		if strings.Contains(md, absent) {
			t.Errorf("site header chrome %q leaked:\n%s", absent, md)
		}
	}
}

// TestPruneBoilerplate_VueTemplateLeak covers unrendered template expressions
// that some server-rendered SPAs leave in text nodes.
func TestPruneBoilerplate_VueTemplateLeak(t *testing.T) {
	md := renderForTest(t, `<html><body>
		<div>{{sendMatomoQuery("Agent Builder", "安装部署")}}</div>
		<div>{{item.code}}</div>
		<p>Core {{name}} text</p>
	</body></html>`)

	if strings.Contains(md, "sendMatomoQuery") || strings.Contains(md, "{{item.code}}") {
		t.Errorf("template expression leaked:\n%s", md)
	}
	// Only whole-node expressions are dropped; a sentence using one is prose.
	if !strings.Contains(md, "Core {{name}} text") {
		t.Errorf("prose containing a template expression must be preserved:\n%s", md)
	}
}

// TestParseHTML_DecorativeImages covers icons/logos/arrows that carry no
// article meaning while keeping real content images.
func TestParseHTML_DecorativeImages(t *testing.T) {
	md := renderForTest(t, `<html><body>
		<p>Body</p>
		<img src="/static/assets/img/right-tag.svg" alt="">
		<img src="/static/assets/img/logo.svg" alt="">
		<img src="/static/assets/img/arrow.png" alt="">
		<img src="/static/assets/img/pic.png" alt="背景图">
		<img src="/static/assets/img/icon24.png" width="24" height="24" alt="">
		<img src="data:image/png;base64,iVBORw0KGgo=" width="24" alt="">
		<img src="/static/assets/img/screen.png" alt="">
		<img src="/static/assets/img/shot.png" alt="部署截图">
	</body></html>`)

	for _, absent := range []string{"right-tag.svg", "logo.svg", "arrow.png", "pic.png", "icon24.png", "data:image"} {
		if strings.Contains(md, absent) {
			t.Errorf("decorative image %q leaked:\n%s", absent, md)
		}
	}
	if !strings.Contains(md, "![部署截图](/static/assets/img/shot.png)") {
		t.Errorf("content image with alt missing:\n%s", md)
	}
	// Empty-alt image without any icon marker is kept: it may be a content
	// screenshot whose alt the site simply omitted.
	if !strings.Contains(md, "/static/assets/img/screen.png") {
		t.Errorf("content screenshot with omitted alt should be kept:\n%s", md)
	}
}

// TestPruneBoilerplate_QRAttentionBlock covers "关注公众号/小程序" blocks that
// contain a caption <p>, so the ordinary prose-descendant guard would keep them.
func TestPruneBoilerplate_QRAttentionBlock(t *testing.T) {
	md := renderForTest(t, `<html><body>
		<div class="about-panel">
			<h4>关注我们</h4>
			<div class="text-333">
				<div class="new_program_box">
					<img src="/webapp/common/page/image/new_program_gz.png" width="96" alt="">
					<p>关注技术服务公众号</p>
				</div>
				<div class="new_program_box">
					<img src="/webapp/common/page/image/new_program_wx.png" width="96" alt="">
					<p>关注微信小程序</p>
				</div>
			</div>
		</div>
		<p>CoreParagraph</p>
	</body></html>`)

	if !strings.Contains(md, "CoreParagraph") {
		t.Errorf("core paragraph missing:\n%s", md)
	}
	for _, absent := range []string{"关注我们", "关注技术服务公众号", "关注微信小程序", "new_program"} {
		if strings.Contains(md, absent) {
			t.Errorf("QR/attention block %q leaked:\n%s", absent, md)
		}
	}
}

// TestPruneBoilerplate_UpdateTime covers the "更新时间：..." span inside an
// article heading; it is metadata, not part of the title.
func TestPruneBoilerplate_UpdateTime(t *testing.T) {
	md := renderForTest(t, `<html><body>
		<h1 class="file-title">安装部署<div class="msg-pannel"><span class="update-time">更新时间：2026-10-23</span></div></h1>
		<p>正文</p>
	</body></html>`)

	if !strings.Contains(md, "# 安装部署") {
		t.Errorf("title missing:\n%s", md)
	}
	if strings.Contains(md, "更新时间") || strings.Contains(md, "2026-10-23") {
		t.Errorf("update-time metadata leaked into heading:\n%s", md)
	}
}

// TestIsLinkChromeContainer pins the crawl-link priority classification:
// header/footer/sidebar chrome is deferred, while nav (pagination) is primary.
func TestIsLinkChromeContainer(t *testing.T) {
	mk := func(htmlStr string) *html.Node {
		doc, err := html.Parse(strings.NewReader(htmlStr))
		if err != nil {
			t.Fatalf("parse fixture: %v", err)
		}
		var find func(*html.Node) *html.Node
		find = func(n *html.Node) *html.Node {
			if n.Type == html.ElementNode && n.Data != "html" && n.Data != "head" && n.Data != "body" {
				return n
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if got := find(c); got != nil {
					return got
				}
			}
			return nil
		}
		return find(doc)
	}
	tests := []struct {
		name string
		html string
		want bool
	}{
		{"header tag", `<header><a href="/a">a</a><a href="/b">b</a><a href="/c">c</a><a href="/d">d</a><a href="/e">e</a></header>`, true},
		{"article header with heading", `<header class="entry-header"><h1>Title</h1><a href="/a">a</a></header>`, false},
		{"footer tag", `<footer><a href="/a">a</a></footer>`, true},
		{"public header class", `<div class="public-header"><a href="/a">a</a></div>`, true},
		{"sidebar class", `<div class="sidebar"><a href="/a">a</a></div>`, true},
		{"nav tag is primary", `<nav><a href="/next">next</a></nav>`, false},
		{"pagination class is primary", `<div class="pagination"><a href="/next">next</a></div>`, false},
		{"content class", `<div class="content"><a href="/a">a</a></div>`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsLinkChromeContainer(mk(tt.html)); got != tt.want {
				t.Errorf("IsLinkChromeContainer(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestSplitClassTokens(t *testing.T) {
	tests := map[string][]string{
		"globalNav":       {"global", "nav"},
		"ad_context1":     {"ad", "context", "1"},
		"footer2016":      {"footer", "2016"},
		"go-Header--full": {"go", "header", "full"},
		"docs-content":    {"docs", "content"},
		"wxGzbox":         {"wx", "gzbox"},
		"mainright":       {"mainright"},
		"":                nil,
		"   ":             nil,
	}
	for in, want := range tests {
		if got := splitClassTokens(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitClassTokens(%q) = %v, want %v", in, got, want)
		}
	}
}
