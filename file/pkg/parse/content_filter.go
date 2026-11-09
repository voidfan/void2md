package parse

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// This file removes page chrome before rendering. A crawled page is a whole
// document, so rendering <body> verbatim also renders navigation, sidebars,
// footers, ads, share widgets and related-article lists; on a typical news
// page that is more text than the article itself.
//
// Two passes, both independent of any one site:
//
//  1. Structural/attribute rules: known chrome tags, ARIA landmark roles,
//     hidden elements, and class/id tokens such as nav/sidebar/footer/share.
//  2. Link density: containers whose text is mostly anchor text and that carry
//     little non-link prose are link farms (menus, related lists). This catches
//     widgets whose class names are unknown or obfuscated.
//
// A conservative text rule also drops short metadata tails (责任编辑/原标题/
// 文章来源/郑重声明/...) that have no distinguishing class.
//
// Explicit user selectors still win: pruning never drops <html>/<head>/<body>,
// so a page whose body class is "comment-page" cannot be emptied, and the
// selected subtree itself is what gets pruned.

// boilerplateTags are elements that never carry article prose.
//
// Deliberately absent: "form". ASP.NET WebForms and some CMS templates wrap
// the entire page body in <form>, so dropping every form by tag would drop the
// article. Search/login forms are still removed via their role or class/id
// tokens, and their <input>/<button> children are dropped below.
var boilerplateTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true,
	"iframe": true, "input": true, "button": true,
	"select": true, "option": true, "textarea": true,
	"svg": true, "canvas": true, "object": true, "embed": true,
	"nav": true, "footer": true, "aside": true, "menu": true,
	"video": true, "audio": true,
}

// boilerplateRoles are ARIA landmarks that mark page chrome (not article prose).
var boilerplateRoles = map[string]bool{
	"navigation": true, "banner": true, "contentinfo": true,
	"complementary": true, "search": true, "menu": true,
	"menubar": true, "toolbar": true,
}

// boilerplateExactTokens match a whole class/id token (after splitting on
// non-alphanumerics, camelCase and letter/digit boundaries):
// "globalNav" -> "global","nav"; "ad_context1" -> "ad","context","1".
//
// Deliberately absent: "content", "article", "main", "post", "entry", "body",
// and "editor" — all are common content-container classes.
var boilerplateExactTokens = map[string]bool{
	// Navigation and page chrome.
	"nav": true, "navbar": true, "navigation": true, "navbox": true,
	"navitem": true, "globalnav": true, "levelsnav": true, "headwrap": true,
	"menu": true, "menubar": true, "submenu": true,
	"breadcrumb": true, "breadcrumbs": true, "crumb": true, "crumbs": true,
	"header": true, "footer": true, "foot": true, "topbar": true,
	"sidebar": true, "side": true, "aside": true,
	// Ads and promotion.
	"ad": true, "ads": true, "adv": true, "adbox": true, "adslot": true,
	"adunit": true, "advert": true, "adverts": true, "advertisement": true,
	"advertising": true, "adsbygoogle": true, "sponsor": true, "sponsored": true,
	"banner": true, "promo": true, "promotion": true,
	// Sharing and social.
	"share": true, "sharing": true, "sharebox": true, "sharewrap": true,
	"sharebar": true, "sharebtn": true, "sharebutton": true, "sharebuttons": true,
	"sharetools": true, "social": true, "socialshare": true, "follow": true,
	"subscribe": true, "subscription": true, "wxshare": true, "wxgzbox": true,
	"bdshare": true,
	// Comments.
	"comment": true, "comments": true, "commentbox": true, "commentlist": true,
	"commentitem": true, "commentarea": true, "commentsection": true,
	"pinglun": true, "pinglunbox": true,
	// Related and hot lists.
	"related": true, "relatedposts": true, "relatedbox": true,
	"recommend": true, "recommendation": true, "recommended": true,
	"hot": true, "hotbox": true, "hotnews": true, "hotsuggest": true,
	"hotlist": true, "hotrank": true, "rank": true, "ranking": true, "ranklist": true,
	// QR codes and article controls.
	"qrcode": true, "qr": true, "qrimg": true, "qrl": true,
	"toolbar": true, "ctrl": true, "ctrls": true, "controls": true,
	"print": true, "fontsize": true, "textsize": true, "zoom": true,
	// Metadata and other UI.
	"tags": true, "taglist": true, "tagcloud": true, "keywords": true,
	"infos": true, "tip": true, "tips": true, "tipbox": true,
	"copyright": true, "disclaimer": true,
	"search": true, "searchbox": true, "searchbar": true, "searchform": true,
	"login": true, "register": true, "signin": true, "signup": true,
	"popup": true, "modal": true, "overlay": true, "mask": true,
	"cookie": true, "consent": true,
	"pagination": true, "pager": true, "paging": true, "pagebar": true,
	"sourcebox": true, "jubao": true, "hidden": true, "invisible": true,
}

// boilerplateTokenPrefixes match compound class names written without a
// separator (e.g. "footer2016", "hotsuggest", "sidebarBox").
var boilerplateTokenPrefixes = []string{
	"ads", "advert", "sponsor", "promo", "banner",
	"breadcrumb", "pagination", "sidebar", "related", "recommend",
	"qr", "search", "login", "register", "popup", "modal", "overlay",
	"copyright", "disclaimer", "toolbar", "sourcebox",
}

// boilerplateTokenSubstrings cover chrome words embedded anywhere in a token:
// "globalnav", "levelsnav", "footer2016", "aboutctrl", "zwctrls".
// "nav" also matches "navy", but a content class named navy is rare enough
// that the gain on real navs outweighs it.
var boilerplateTokenSubstrings = []string{"nav", "footer", "ctrl"}

// metadataPhrases mark short article-tail metadata blocks. Only a block whose
// text STARTS with one of these (after leading punctuation) is dropped, so an
// article sentence merely containing the word is safe.
var metadataPhrases = []string{
	"责任编辑", "原标题", "文章来源", "郑重声明", "免责声明", "版权所有", "未经授权",
	"更新时间", "更新日期",
}

// uiPhrases mark short UI instructions. They only apply to div/section (never
// to <p>) and must also START the text, so an article paragraph that merely
// mentions e.g. 分享到 is safe.
var uiPhrases = []string{
	"扫一扫", "扫码", "打开微信", "分享到", "关注我们", "下载app", "点击底部",
}

// densityContainerTags are the only elements link density may drop. Paragraphs,
// headings and articles are excluded so a link-heavy sentence/list inside real
// prose is never mistaken for navigation.
var densityContainerTags = map[string]bool{
	"div": true, "section": true, "table": true,
	"ul": true, "ol": true, "dl": true,
}

// pruneBoilerplate removes page chrome from a parsed document in place.
func pruneBoilerplate(root *html.Node) {
	pruneObvious(root)
	pruneLinkDense(root)
}

// pruneObvious removes elements that are chrome by tag, role, visibility,
// class/id token, or short metadata text. Parent-first: a removed subtree is
// not walked again.
func pruneObvious(n *html.Node) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling // RemoveChild invalidates c.NextSibling
		if c.Type == html.ElementNode {
			if shouldRemoveElement(c) {
				n.RemoveChild(c)
			} else {
				pruneObvious(c)
			}
		}
		c = next
	}
}

// shouldRemoveElement applies the structural/attribute/text rules to one node.
func shouldRemoveElement(n *html.Node) bool {
	// Never drop the document skeleton: body classes are a favorite place for
	// CMS themes to put "comment-open", "sidebar-right", etc.
	switch n.Data {
	case "html", "head", "body":
		return false
	}
	if boilerplateTags[n.Data] {
		return true
	}
	if boilerplateRoles[strings.ToLower(strings.TrimSpace(getAttr(n, "role")))] {
		return true
	}
	if isHiddenElement(n) {
		return true
	}
	for _, tok := range classIDTokens(n) {
		// Many templates put the article title inside <header class="entry-header">.
		// Dropping every "header" token would silently lose the title, so keep a
		// header-like element that still contains a heading — unless it is packed
		// with links (a site/app header with a breadcrumb and menus). Its nav/share
		// children are pruned individually on the recursive walk.
		if tok == "header" && containsHeading(n) && anchorCountAtMost(n, 4) {
			continue
		}
		if isBoilerplateToken(tok) {
			return true
		}
	}
	return isBoilerplateByText(n)
}

// linkChrome* classify page chrome for crawl-link prioritization. Unlike the
// content rules above, <nav>/<menu>/pagination are deliberately NOT chrome:
// a "next page" link in a nav bar is valuable, so its links must stay primary.
var linkChromeTags = map[string]bool{
	"header": true, "footer": true, "aside": true,
}

var linkChromeRoles = map[string]bool{
	"banner": true, "contentinfo": true, "complementary": true, "search": true,
}

var linkChromeExactTokens = map[string]bool{
	"header": true, "footer": true, "foot": true, "topbar": true,
	"sidebar": true, "side": true, "aside": true,
	"share": true, "sharing": true, "social": true, "socialshare": true,
	"comment": true, "comments": true, "commentbox": true, "pinglun": true, "pinglunbox": true,
	"related": true, "relatedposts": true, "relatedbox": true,
	"recommend": true, "recommendation": true, "recommended": true,
	"breadcrumb": true, "breadcrumbs": true, "crumb": true, "crumbs": true,
	"login": true, "register": true, "signin": true, "signup": true, "logout": true,
	"popup": true, "modal": true, "overlay": true, "cookie": true, "consent": true,
	"copyright": true, "disclaimer": true, "toolbar": true, "controls": true,
	"jubao": true,
}

var linkChromeTokenPrefixes = []string{
	"sidebar", "breadcrumb", "related", "recommend",
	"copyright", "disclaimer", "toolbar", "sourcebox",
}

var linkChromeTokenSubstrings = []string{"footer", "ctrl"}

// IsLinkChromeContainer reports whether a subtree is page chrome whose links
// should be crawled only after primary (content/nav) links are exhausted.
// The crawler uses this to keep maxPage from being spent on headers, footers,
// share bars and comment lists before the actual content links.
func IsLinkChromeContainer(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	switch n.Data {
	case "html", "head", "body", "main", "article":
		return false
	}
	if linkChromeTags[n.Data] {
		if n.Data == "header" && containsHeading(n) && anchorCountAtMost(n, 4) {
			return false
		}
		return true
	}
	if linkChromeRoles[strings.ToLower(strings.TrimSpace(getAttr(n, "role")))] {
		return true
	}
	if isHiddenElement(n) {
		return true
	}
	for _, tok := range classIDTokens(n) {
		if tok == "header" && containsHeading(n) && anchorCountAtMost(n, 4) {
			continue
		}
		if isLinkChromeToken(tok) {
			return true
		}
	}
	return false
}

func isLinkChromeToken(tok string) bool {
	if tok == "" {
		return false
	}
	if linkChromeExactTokens[tok] {
		return true
	}
	for _, p := range linkChromeTokenPrefixes {
		if strings.HasPrefix(tok, p) {
			return true
		}
	}
	for _, s := range linkChromeTokenSubstrings {
		if strings.Contains(tok, s) {
			return true
		}
	}
	return false
}

// containsHeading reports whether n has an h1–h6 descendant.
func containsHeading(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		switch c.Data {
		case "h1", "h2", "h3", "h4", "h5", "h6":
			return true
		}
		if containsHeading(c) {
			return true
		}
	}
	return false
}

// anchorCountAtMost reports whether n contains at most max <a> elements,
// stopping as soon as the limit is exceeded.
func anchorCountAtMost(n *html.Node, max int) bool {
	count := 0
	var walk func(*html.Node) bool
	walk = func(m *html.Node) bool {
		if m.Type == html.ElementNode && m.Data == "a" {
			count++
			if count > max {
				return false
			}
		}
		for c := m.FirstChild; c != nil; c = c.NextSibling {
			if !walk(c) {
				return false
			}
		}
		return true
	}
	return walk(n)
}

// isBoilerplateToken reports whether one class/id token names page chrome.
func isBoilerplateToken(tok string) bool {
	if tok == "" {
		return false
	}
	if boilerplateExactTokens[tok] {
		return true
	}
	for _, p := range boilerplateTokenPrefixes {
		if strings.HasPrefix(tok, p) {
			return true
		}
	}
	for _, s := range boilerplateTokenSubstrings {
		if strings.Contains(tok, s) {
			return true
		}
	}
	return false
}

// classIDTokens returns lowercased tokens from every class and id attribute.
func classIDTokens(n *html.Node) []string {
	var out []string
	for _, a := range n.Attr {
		if a.Key == "class" || a.Key == "id" {
			out = append(out, splitClassTokens(a.Val)...)
		}
	}
	return out
}

// splitClassTokens splits on non-alphanumerics and camelCase/letter-digit
// boundaries, lowercasing each token: "globalNav" -> global, nav;
// "ad_context1" -> ad, context, 1; "footer2016" -> footer, 2016.
func splitClassTokens(s string) []string {
	var (
		out  []string
		cur  []rune
		prev rune
	)
	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
			cur = cur[:0]
		}
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			prev = 0
			continue
		}
		if len(cur) > 0 {
			switch {
			case unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsDigit(prev)):
				flush()
			case unicode.IsDigit(r) && unicode.IsLetter(prev):
				flush()
			case unicode.IsLetter(r) && unicode.IsDigit(prev):
				flush()
			}
		}
		cur = append(cur, unicode.ToLower(r))
		prev = r
	}
	flush()
	return out
}

// isHiddenElement reports elements hidden by HTML, ARIA, or inline CSS.
func isHiddenElement(n *html.Node) bool {
	for _, a := range n.Attr {
		switch a.Key {
		case "hidden":
			return true
		case "aria-hidden":
			v := strings.ToLower(strings.TrimSpace(a.Val))
			if v == "true" || v == "1" {
				return true
			}
		case "style":
			s := strings.Map(func(r rune) rune {
				if unicode.IsSpace(r) {
					return -1
				}
				return unicode.ToLower(r)
			}, a.Val)
			if strings.Contains(s, "display:none") || strings.Contains(s, "visibility:hidden") {
				return true
			}
		}
	}
	return false
}

// isBoilerplateByText drops short metadata/UI blocks that carry no class
// hint. It refuses any element containing real prose descendants (p, headings,
// pre/code, table, article, main), so article containers are never matched.
func isBoilerplateByText(n *html.Node) bool {
	switch n.Data {
	case "div", "section", "p", "li", "span":
	default:
		return false
	}
	// Long text is never a metadata/UI block, so check the cheap length bound
	// first; prose descendants matter only for short blocks.
	text, ok := shortPlainText(n, 200)
	if !ok {
		return false
	}
	if isAttentionBlock(n, text) {
		return true
	}
	if hasProseDescendant(n) {
		return false
	}
	text = strings.TrimLeftFunc(text, isLeadingMarker)
	for _, p := range metadataPhrases {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	if n.Data == "div" || n.Data == "section" {
		if utf8.RuneCountInString(text) <= 120 {
			lower := strings.ToLower(text)
			for _, p := range uiPhrases {
				if strings.HasPrefix(lower, p) {
					return true
				}
			}
		}
	}
	return false
}

// isAttentionBlock drops QR/follow blocks such as
// "关注技术服务公众号" + QR image + "关注微信小程序". These often contain a
// <p> caption, so they must be recognized before the prose-descendant guard;
// the strong phrase set keeps ordinary article text safe.
func isAttentionBlock(n *html.Node, text string) bool {
	if n.Data != "div" && n.Data != "section" {
		return false
	}
	t := strings.TrimLeftFunc(text, isLeadingMarker)
	switch {
	case strings.HasPrefix(t, "关注"), strings.HasPrefix(t, "扫码"),
		strings.HasPrefix(t, "扫一扫"), strings.HasPrefix(t, "扫描"):
	default:
		return false
	}
	for _, kw := range []string{"公众号", "小程序", "二维码", "微信", "微博"} {
		if strings.Contains(t, kw) {
			return true
		}
	}
	return false
}

// isLeadingMarker reports punctuation commonly wrapping a metadata label,
// e.g. the "（" in "（文章来源：经济日报）".
func isLeadingMarker(r rune) bool {
	if unicode.IsSpace(r) {
		return true
	}
	switch r {
	case '（', '(', '【', '[', '「', '『', '《', '<', '"', '\'', '“', '”', '‘', '’':
		return true
	}
	return false
}

// hasProseDescendant reports whether n contains article-like content; such an
// element must never be dropped by the text rule.
func hasProseDescendant(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		switch c.Data {
		case "p", "h1", "h2", "h3", "h4", "h5", "h6", "pre", "code", "table", "article", "main":
			return true
		}
		if hasProseDescendant(c) {
			return true
		}
	}
	return false
}

// shortPlainText returns visible text (TextNode only, no comments/scripts) when
// it is at most limit runes; ok is false as soon as the limit is exceeded, so
// large article containers fail this check cheaply.
func shortPlainText(n *html.Node, limit int) (string, bool) {
	var b strings.Builder
	runes := 0
	within := true
	var walk func(*html.Node)
	walk = func(m *html.Node) {
		if !within {
			return
		}
		if m.Type == html.TextNode {
			runes += utf8.RuneCountInString(m.Data)
			if runes > limit {
				within = false
				return
			}
			b.WriteString(m.Data)
			return
		}
		if m.Type == html.ElementNode {
			switch m.Data {
			case "script", "style", "noscript", "template":
				return
			}
		}
		for c := m.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	if !within {
		return "", false
	}
	text := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(text) > limit {
		return "", false
	}
	return text, true
}

// densityMetrics summarizes a subtree's visible text vs anchor text.
type densityMetrics struct {
	text      int // visible runes
	linkText  int // visible runes inside <a>
	linkCount int // number of <a> elements
}

// pruneLinkDense walks bottom-up, removing link-farm containers and returning
// the metrics of what remains under n.
func pruneLinkDense(n *html.Node) densityMetrics {
	var m densityMetrics
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling // RemoveChild invalidates c.NextSibling
		switch c.Type {
		case html.TextNode:
			m.text += utf8.RuneCountInString(strings.TrimSpace(c.Data))
		case html.ElementNode:
			cm := pruneLinkDense(c)
			m.text += cm.text
			m.linkText += cm.linkText
			m.linkCount += cm.linkCount
			if shouldDropLinkDense(c, cm) {
				n.RemoveChild(c)
				m.text -= cm.text
				m.linkText -= cm.linkText
				m.linkCount -= cm.linkCount
			}
		}
		c = next
	}
	if n.Type == html.ElementNode && n.Data == "a" {
		m.linkText = m.text
		m.linkCount++
	}
	return m
}

// shouldDropLinkDense reports a container whose text is essentially a list of
// links. Requiring little non-link prose keeps article bodies with inline
// links and link-heavy sentences in <p> safe.
func shouldDropLinkDense(n *html.Node, m densityMetrics) bool {
	if !densityContainerTags[n.Data] {
		return false
	}
	if m.text < 6 || m.linkCount < 2 {
		return false
	}
	if m.linkText*100 < m.text*50 {
		return false
	}
	return m.text-m.linkText <= 60
}
