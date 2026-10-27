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

	"void2md/common/logger"

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

// log returns the module logger lazily (see file/pkg/service/crawl).
func log() *slog.Logger { return logger.GetLogger("parse") }

// ParseHTMLBytesToMarkdown converts HTML bytes to a Markdown string.
// Parses the DOM with golang.org/x/net/html and walks it recursively.
// Encoding is decided by <meta charset> probing; callers who already know the
// charset (e.g. from HTTP Content-Type) should use
// ParseHTMLBytesToMarkdownWithCharset instead, to avoid double decoding.
// pageCount is content length / 3000 (at least 1).
func ParseHTMLBytesToMarkdown(ctx context.Context, htmlBytes []byte, filename string) (string, int, error) {
	return ParseHTMLBytesToMarkdownWithCharset(ctx, htmlBytes, filename, "")
}

// ParseHTMLBytesToMarkdownWithCharset behaves like ParseHTMLBytesToMarkdown but
// accepts a caller-provided charsetHint (the HTTP Content-Type charset is more
// authoritative than <meta charset>).
//
// A non-blank charsetHint (after trimming) is treated as authoritative:
//   - "utf-8" (case-insensitive, incl. hyphen-less aliases utf8, unicode-1-1-utf-8)
//     → no decoding; input used as-is;
//   - other decodable values → decoded accordingly (decodable set in
//     getCharsetDecoder: Simplified/Traditional Chinese, Japanese
//     Shift-JIS/EUC-JP/ISO-2022-JP, Korean EUC-KR, Western windows-1252);
//   - undecodable values (e.g. koi8-r) → fall back to <meta charset> probing:
//     1. <meta> declares nothing → keep the hint and report "unsupported"
//     (error text is the hint's name);
//     2. the detected declaration is also undecodable (e.g. meta claims
//     iso-8859-2) → report that declaration's name.
//     Both paths give the caller an error; behavior is equivalent, only the text differs.
//
// An empty/blank charsetHint falls back to <meta charset> probing.
//
// Note: **an empty hint does NOT mean "no decoding"** — <meta charset> still applies.
func ParseHTMLBytesToMarkdownWithCharset(ctx context.Context, htmlBytes []byte, filename, charsetHint string) (string, int, error) {
	if len(bytes.TrimSpace(htmlBytes)) == 0 {
		return "", 0, nil
	}

	decoded, err := DecodeHTMLBytes(htmlBytes, charsetHint)
	if err != nil {
		log().Warn("html encoding decode failed, using raw bytes", "filename", filename, "error", err)
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

// DecodeHTMLBytes decodes HTML bytes to UTF-8.
// A non-blank charsetHint wins (HTTP Content-Type is more authoritative than
// <meta>); an undecodable hint falls back to <meta charset> probing (see
// ParseHTMLBytesToMarkdownWithCharset).
func DecodeHTMLBytes(data []byte, charsetHint string) ([]byte, error) {
	charset := strings.TrimSpace(charsetHint)
	if charset == "" {
		charset = detectHTMLCharset(data)
	} else if !isUTF8Charset(charset) && getCharsetDecoder(charset) == nil {
		// Undecodable hint: don't fail outright, fall back to <meta> probing —
		// the page's own declaration may be decodable (e.g. header says koi8-r,
		// meta says gbk); failing directly would garble the whole page. When
		// <meta> has no usable declaration either, keep the original hint and
		// still report "unsupported" for caller observability.
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

// isUTF8Charset reports whether a charset value means UTF-8.
//
// Besides "utf-8", accepts WHATWG's hyphen-less aliases: charset=utf8 is common
// in HTTP headers, and missing the alias would mark an already-utf-8-declared
// page "undecodable", falling back to <meta> and possibly misdecoding. utf-8
// means "return as-is", so it bypasses getCharsetDecoder and is special-cased here.
func isUTF8Charset(charset string) bool {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "utf-8", "utf8", "unicode-1-1-utf-8":
		return true
	}
	return false
}

// detectHTMLCharset extracts the charset declaration from HTML.
func detectHTMLCharset(data []byte) string {
	head := data
	if len(head) > 4096 {
		head = head[:4096]
	}
	// Strict form: charset must be followed by `=`; the value may be quoted.
	// Do not degrade to adding `'` to the character class (charset[="'\s]+):
	// it would capture "s" from the `'s` in `<p>the charset's value is utf-8</p>`.
	// Trade-off: `charset"gbk"` (no `=`), never a legal form, is no longer supported.
	re := regexp.MustCompile(`(?i)charset\s*=\s*["']?\s*([a-zA-Z0-9_-]+)`)
	matches := re.FindSubmatch(head)
	if len(matches) >= 2 {
		return strings.Trim(string(matches[1]), "\"' ")
	}
	return ""
}

// getCharsetDecoder returns the decoder for a charset; nil when undecodable.
//
// Decodable set (labels are **common** ones collected per the WHATWG Encoding
// Standard, **not exhaustive**; matched after ToLower):
//   - Simplified Chinese: gbk / gb2312 / gb18030; Traditional Chinese: big5;
//   - Japanese: Shift-JIS (shift_jis, cp932, windows-31j…), EUC-JP, ISO-2022-JP;
//   - Korean: EUC-KR;
//   - Western: iso-8859-1 and aliases → always decoded as windows-1252.
//
// Two deliberate choices:
//
//  1. Western always maps to charmap.Windows1252, not charmap.ISO8859_1. Per the
//     WHATWG Encoding Standard (and browser behavior), the iso-8859-1 label in
//     HTTP/HTML should actually be decoded as windows-1252, else characters in
//     0x80–0x9F (curly quotes etc.) become C1 control codes.
//  2. No other single-byte encodings (iso-8859-2…iso-8859-16, koi8-r,
//     windows-1251, etc.). Single-byte encodings decode any bytes; once in the
//     table, the "undecodable" signal disappears — both the <meta>-fallback
//     branch and the "meta also undecodable → error" observability would break.
//
// utf-8 is not listed: it means "return as-is", special-cased via isUTF8Charset in DecodeHTMLBytes.
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

// renderNode recursively renders an HTML node tree to Markdown.
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

// renderChildrenInline renders children as inline text (no block-level newlines).
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

// extractText extracts plain text from a node and its subtree.
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

// extractHTML re-serializes a node and its subtree to an HTML string.
func extractHTML(n *html.Node) string {
	var buf bytes.Buffer
	html.Render(&buf, n)
	return buf.String()
}

// getAttr reads a node attribute value.
func getAttr(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

// cleanMarkdown tidies generated Markdown, collapsing excess blank lines.
var multiNewlineRe = regexp.MustCompile(`\n{3,}`)

func cleanMarkdown(s string) string {
	s = multiNewlineRe.ReplaceAllString(s, "\n\n")
	s = strings.TrimSpace(s)
	return s
}

var _ = encoding.Nop
var _ = io.ReadAll
var _ = fmt.Sprintf

// ParseHTMLXLSBytes parses pseudo-xls files whose content is HTML, extracting
// <table>s verbatim. Matches the Python excel_parser._parse_html_xls_bytes behavior:
//   - encodings tried in order utf-8 → gbk → gb2312 → utf-16 (first success);
//     all failing falls back to utf-8;
//   - every <table> found is serialized verbatim (incl. nested tables, same as
//     BeautifulSoup find_all) and joined with "\n\n"; total rows is the sum of
//     <tr> counts across all tables;
//   - returns (html_text, total rows, error); errors when no <table> is found.
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
		return "", 0, fmt.Errorf("%s is HTML format but contains no <table>", filename)
	}
	log().Info("html format xls parsed", "filename", filename, "tables", len(tables), "rows", totalRows)
	return strings.Join(tables, "\n\n"), totalRows, nil
}

// decodeXLSHTML decodes using the Python _parse_html_xls_bytes try order:
// utf-8 → gbk → gb2312 → utf-16, first success; all failing returns the raw bytes (approximating utf-8+replace).
func decodeXLSHTML(data []byte) (string, error) {
	// 1. utf-8
	if utf8.Valid(data) {
		return string(data), nil
	}
	// 2-3. gbk / gb2312 (GB2312 is a GBK subset; Go has no standalone gb2312 decoder, use GBK)
	if out, err := decodeWith(data, simplifiedchinese.GBK.NewDecoder()); err == nil {
		return out, nil
	}
	// 4. utf-16 (BOM preferred; little-endian without BOM)
	if out, err := decodeWith(data, unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder()); err == nil {
		return out, nil
	}
	// Fallback: keep raw bytes as utf-8 (equivalent to Python decode(errors="replace"))
	return string(data), nil
}

// decodeWith converts bytes to a UTF-8 string with the given decoder.
func decodeWith(data []byte, dec transform.Transformer) (string, error) {
	reader := transform.NewReader(bytes.NewReader(data), dec)
	out, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// countDescendantElements counts elements with the given tag in a node's subtree (incl. itself).
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
