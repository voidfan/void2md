package parse

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// ParseHTMLBytesToMarkdown 将 HTML 字节数据转换为 Markdown 字符串。
// 使用 golang.org/x/net/html 解析 DOM 树，递归遍历提取内容。
// 编码由 <meta charset> 探测决定；调用方若已知 charset（如 HTTP Content-Type），
// 应改用 ParseHTMLBytesToMarkdownWithCharset 显式传入，避免二次解码。
// pageCount 为内容长度 / 3000（至少为1）。
func ParseHTMLBytesToMarkdown(ctx context.Context, htmlBytes []byte, filename string) (string, int, error) {
	return ParseHTMLBytesToMarkdownWithCharset(ctx, htmlBytes, filename, "")
}

// ParseHTMLBytesToMarkdownWithCharset 与 ParseHTMLBytesToMarkdown 行为一致，
// 但接受调用方提供的 charsetHint（HTTP Content-Type 的 charset 比 <meta charset> 更权威）。
//
// charsetHint 去空白后非空即视为权威来源：
//   - "utf-8"（不分大小写，含无短横线别名 utf8、unicode-1-1-utf-8）→ 不做任何解码，
//     原样使用输入；
//   - 其它可解的取值 → 按其解码（可解集合见 getCharsetDecoder：简繁中文、日文
//     Shift-JIS/EUC-JP/ISO-2022-JP、韩文 EUC-KR、西欧 windows-1252）；
//   - 不可解的取值（如 koi8-r）→ 回退到 <meta charset> 探测：
//     1. <meta> 也探测不到声明 → 保留该 hint 并按「不可解」报错（错误文本是 hint 的名字）；
//     2. 探测到的声明仍不可解（如 meta 声称 iso-8859-2）→ 按该声明的名字报错。
//     两条路调用方都拿到 error，行为等价，仅错误文本不同。
//
// charsetHint 为空或全为空白时回退到 <meta charset> 探测。
//
// 注意：**空 hint 并不等于「不解码」**——此时仍会按 <meta charset> 解码。
func ParseHTMLBytesToMarkdownWithCharset(ctx context.Context, htmlBytes []byte, filename, charsetHint string) (string, int, error) {
	if len(bytes.TrimSpace(htmlBytes)) == 0 {
		return "", 0, nil
	}

	decoded, err := DecodeHTMLBytes(htmlBytes, charsetHint)
	if err != nil {
		slog.Warn("html encoding decode failed, using raw bytes", "filename", filename, "error", err)
		decoded = htmlBytes
	}

	doc, err := html.Parse(bytes.NewReader(decoded))
	if err != nil {
		return "", 0, fmt.Errorf("html parse error: %w", err)
	}

	var buf bytes.Buffer
	renderNode(&buf, doc)
	result := cleanMarkdown(buf.String())

	pageCount := len(result) / 3000
	if pageCount < 1 {
		pageCount = 1
	}

	return result, pageCount, nil
}

// DecodeHTMLBytes 将 HTML 字节解码为 UTF-8。
// charsetHint 去空白后非空时优先使用（HTTP Content-Type 比 <meta> 更权威）；
// hint 不可解时回退到 <meta charset> 探测（见 ParseHTMLBytesToMarkdownWithCharset 的说明）。
func DecodeHTMLBytes(data []byte, charsetHint string) ([]byte, error) {
	charset := strings.TrimSpace(charsetHint)
	if charset == "" {
		charset = detectHTMLCharset(data)
	} else if !isUTF8Charset(charset) && getCharsetDecoder(charset) == nil {
		// hint 不可解：不要直接失败，先退到 <meta> 探测——页面自带的声明可能反而
		// 可解（例如 header 说 koi8-r、meta 说 gbk），直接失败会让整页以乱码收场。
		// <meta> 也没有可用声明时保留原 hint，仍按「不可解」报错，便于调用方观测。
		if meta := detectHTMLCharset(data); meta != "" {
			charset = meta
		}
	}
	if charset == "" || isUTF8Charset(charset) {
		return data, nil
	}

	dec := getCharsetDecoder(charset)
	if dec == nil {
		return nil, fmt.Errorf("unsupported charset: %s", charset)
	}

	reader := transform.NewReader(bytes.NewReader(data), dec)
	return io.ReadAll(reader)
}

// isUTF8Charset 判断 charset 取值是否表示 UTF-8。
//
// 除 "utf-8" 外还接受 WHATWG 的无短横线别名：HTTP 头写 charset=utf8 很常见，
// 漏掉别名会让本已声明 utf-8 的页面被判为「不可解」，进而回退到 <meta> 被按另一种
// 编码误解。utf-8 的语义是「原样返回」，所以它不走 getCharsetDecoder，只在这里特判。
func isUTF8Charset(charset string) bool {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "utf-8", "utf8", "unicode-1-1-utf-8":
		return true
	}
	return false
}

// detectHTMLCharset 从 HTML 中提取 charset 声明。
func detectHTMLCharset(data []byte) string {
	head := data
	if len(head) > 4096 {
		head = head[:4096]
	}
	// 严格写法：charset 后必须跟 `=`，取值前允许被引号包裹。
	// 不要退化成往字符类里加 `'` 的写法（charset[="'\s]+）：它会把
	// `<p>the charset's value is utf-8</p>` 里的 `'s` 当成取值捕获到 "s"。
	// 代价是不再支持 `charset"gbk"`（无 `=`）这种从不合法的形式。
	re := regexp.MustCompile(`(?i)charset\s*=\s*["']?\s*([a-zA-Z0-9_-]+)`)
	matches := re.FindSubmatch(head)
	if len(matches) >= 2 {
		return strings.Trim(string(matches[1]), "\"' ")
	}
	return ""
}

// getCharsetDecoder 返回 charset 对应的解码器；取值不可解时返回 nil。
//
// 可解集合（label 按 WHATWG 编码标准收集的**常用** label，**非全集**；统一 ToLower 后匹配）：
//   - 简体中文：gbk / gb2312 / gb18030；繁体中文：big5；
//   - 日文：Shift-JIS（shift_jis、cp932、windows-31j…）、EUC-JP、ISO-2022-JP；
//   - 韩文：EUC-KR；
//   - 西欧：iso-8859-1 及其别名 → 一律按 windows-1252 解。
//
// 两个刻意的取舍：
//
//  1. 西欧一律映射到 charmap.Windows1252，而不是 charmap.ISO8859_1。按 WHATWG 编码标准
//     （也是浏览器的行为），HTTP/HTML 里的 iso-8859-1 label 实际就该按 windows-1252 解，
//     否则 0x80–0x9F 区间的弯引号等字符会变成 C1 控制符。
//  2. 不加其它单字节编码（iso-8859-2…iso-8859-16、koi8-r、windows-1251 等）。单字节编码
//     能解任意字节，一旦进查表，「不可解」这个信号就消失——回退 <meta> 探测的分支和
//     「meta 也不可解时报错」的观测口径都会失效。
//
// utf-8 不在此列：它的语义是「原样返回」，由 DecodeHTMLBytes 的 isUTF8Charset 特判。
func getCharsetDecoder(charset string) transform.Transformer {
	switch strings.ToLower(charset) {
	case "gbk", "gb2312":
		return simplifiedchinese.GBK.NewDecoder()
	case "gb18030":
		return simplifiedchinese.GB18030.NewDecoder()
	case "big5":
		return traditionalchinese.Big5.NewDecoder()
	case "shift_jis", "shift-jis", "sjis", "x-sjis", "ms_kanji", "windows-31j", "cp932":
		return japanese.ShiftJIS.NewDecoder()
	case "euc-jp", "eucjp", "x-euc-jp":
		return japanese.EUCJP.NewDecoder()
	case "iso-2022-jp", "iso2022jp":
		return japanese.ISO2022JP.NewDecoder()
	case "euc-kr", "euckr", "ks_c_5601-1987", "ksc5601":
		return korean.EUCKR.NewDecoder()
	case "iso-8859-1", "iso8859-1", "iso_8859-1", "latin1", "latin-1", "l1", "cp819",
		"windows-1252", "cp1252", "x-cp1252":
		return charmap.Windows1252.NewDecoder()
	}
	return nil
}

// renderNode 递归渲染 HTML 节点树为 Markdown。
func renderNode(buf *bytes.Buffer, n *html.Node) {
	if n.Type == html.TextNode {
		text := strings.TrimSpace(n.Data)
		if text != "" {
			buf.WriteString(text)
		}
		return
	}

	if n.Type != html.ElementNode {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			renderNode(buf, c)
		}
		return
	}

	switch n.Data {
	case "script", "style", "noscript":
		return

	case "h1", "h2", "h3", "h4", "h5", "h6":
		level := int(n.Data[1] - '0')
		buf.WriteString("\n\n")
		buf.WriteString(strings.Repeat("#", level))
		buf.WriteString(" ")
		renderChildrenInline(buf, n)
		buf.WriteString("\n\n")

	case "p":
		buf.WriteString("\n\n")
		renderChildrenInline(buf, n)
		buf.WriteString("\n\n")

	case "br":
		buf.WriteString("\n")

	case "li":
		buf.WriteString("\n- ")
		renderChildrenInline(buf, n)

	case "table":
		buf.WriteString("\n\n")
		buf.WriteString(extractHTML(n))
		buf.WriteString("\n\n")

	case "img":
		alt := getAttr(n, "alt")
		src := getAttr(n, "src")
		buf.WriteString(fmt.Sprintf("![%s](%s)", alt, src))

	case "a":
		href := getAttr(n, "href")
		buf.WriteString("[")
		renderChildrenInline(buf, n)
		buf.WriteString("](")
		buf.WriteString(href)
		buf.WriteString(")")

	case "b", "strong":
		buf.WriteString("**")
		renderChildrenInline(buf, n)
		buf.WriteString("**")

	case "i", "em":
		buf.WriteString("*")
		renderChildrenInline(buf, n)
		buf.WriteString("*")

	case "pre", "code":
		buf.WriteString("\n```\n")
		text := extractText(n)
		buf.WriteString(strings.TrimSpace(text))
		buf.WriteString("\n```\n")

	case "ul", "ol", "div", "span", "section", "article", "header", "footer", "main", "nav":
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			renderNode(buf, c)
		}

	default:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			renderNode(buf, c)
		}
	}
}

// renderChildrenInline 渲染子节点为内联文本（不添加块级换行）。
func renderChildrenInline(buf *bytes.Buffer, n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			text := strings.TrimSpace(c.Data)
			buf.WriteString(text)
		} else if c.Type == html.ElementNode {
			switch c.Data {
			case "br":
				buf.WriteString("\n")
			case "b", "strong":
				buf.WriteString("**")
				renderChildrenInline(buf, c)
				buf.WriteString("**")
			case "i", "em":
				buf.WriteString("*")
				renderChildrenInline(buf, c)
				buf.WriteString("*")
			case "a":
				href := getAttr(c, "href")
				buf.WriteString("[")
				renderChildrenInline(buf, c)
				buf.WriteString("](")
				buf.WriteString(href)
				buf.WriteString(")")
			case "img":
				alt := getAttr(c, "alt")
				src := getAttr(c, "src")
				buf.WriteString(fmt.Sprintf("![%s](%s)", alt, src))
			case "code":
				buf.WriteString("`")
				renderChildrenInline(buf, c)
				buf.WriteString("`")
			default:
				renderChildrenInline(buf, c)
			}
		}
	}
}

// extractText 从节点及其子树提取纯文本。
func extractText(n *html.Node) string {
	var buf bytes.Buffer
	extractTextRecursive(&buf, n)
	return buf.String()
}

func extractTextRecursive(buf *bytes.Buffer, n *html.Node) {
	if n.Type == html.TextNode {
		buf.WriteString(n.Data)
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		extractTextRecursive(buf, c)
	}
}

// extractHTML 从节点及其子树重新序列化为 HTML 字符串。
func extractHTML(n *html.Node) string {
	var buf bytes.Buffer
	html.Render(&buf, n)
	return buf.String()
}

// getAttr 获取节点的属性值。
func getAttr(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

// cleanMarkdown 清理生成的 Markdown 文本，合并多余空行。
var multiNewlineRe = regexp.MustCompile(`\n{3,}`)

func cleanMarkdown(s string) string {
	s = multiNewlineRe.ReplaceAllString(s, "\n\n")
	s = strings.TrimSpace(s)
	return s
}

var _ = encoding.Nop
var _ = io.ReadAll
var _ = fmt.Sprintf

// ParseHTMLXLSBytes 解析内容为 HTML 的伪 xls 文件，提取其中的 <table> 原样返回。
// 对齐 Python excel_parser._parse_html_xls_bytes：
//   - 编码按 utf-8 → gbk → gb2312 → utf-16 依次尝试（首个成功者），全部失败以 utf-8 兜底；
//   - 找到的所有 <table> 原样序列化（含嵌套表格，与 BeautifulSoup find_all 行为一致），
//     以 "\n\n" 拼接；总行数为所有 table 内 <tr> 数量之和；
//   - 返回 (html_text, 总行数, error)；未找到 <table> 时报错。
func ParseHTMLXLSBytes(ctx context.Context, fileBytes []byte, filename string) (string, int, error) {
	if len(bytes.TrimSpace(fileBytes)) == 0 {
		return "", 0, nil
	}

	text, err := decodeXLSHTML(fileBytes)
	if err != nil {
		return "", 0, fmt.Errorf("xls html decode failed: %w", err)
	}

	doc, err := html.Parse(strings.NewReader(text))
	if err != nil {
		return "", 0, fmt.Errorf("xls html parse error: %w", err)
	}

	var tables []string
	totalRows := 0
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "table" {
			tables = append(tables, extractHTML(n))
			totalRows += countDescendantElements(n, "tr")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	if len(tables) == 0 {
		return "", 0, fmt.Errorf("%s 是 HTML 格式但未找到 <table>", filename)
	}
	slog.Info("html format xls parsed", "filename", filename, "tables", len(tables), "rows", totalRows)
	return strings.Join(tables, "\n\n"), totalRows, nil
}

// decodeXLSHTML 按 Python _parse_html_xls_bytes 的编码尝试顺序解码：
// utf-8 → gbk → gb2312 → utf-16，首个成功者；全部失败返回原始字节（近似 utf-8+replace）。
func decodeXLSHTML(data []byte) (string, error) {
	// 1. utf-8
	if utf8.Valid(data) {
		return string(data), nil
	}
	// 2-3. gbk / gb2312（GB2312 是 GBK 子集，Go 无独立 gb2312 解码器，用 GBK）
	if out, err := decodeWith(data, simplifiedchinese.GBK.NewDecoder()); err == nil {
		return out, nil
	}
	// 4. utf-16（优先 BOM，无 BOM 时按 little-endian）
	if out, err := decodeWith(data, unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder()); err == nil {
		return out, nil
	}
	// 兜底：按 utf-8 保留原始字节（等价 Python decode(errors="replace")）
	return string(data), nil
}

// decodeWith 用指定解码器将字节转为 UTF-8 字符串。
func decodeWith(data []byte, dec transform.Transformer) (string, error) {
	reader := transform.NewReader(bytes.NewReader(data), dec)
	out, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// countDescendantElements 统计节点子树中指定标签的元素数量（含自身）。
func countDescendantElements(n *html.Node, tag string) int {
	count := 0
	var visit func(m *html.Node)
	visit = func(m *html.Node) {
		if m.Type == html.ElementNode && m.Data == tag {
			count++
		}
		for c := m.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(n)
	return count
}
