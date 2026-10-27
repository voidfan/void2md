package parse

import (
	"context"
	"strings"
	"testing"
)

func TestParseHTMLBytesToMarkdown_Empty(t *testing.T) {
	ctx := context.Background()
	result, pages, err := ParseHTMLBytesToMarkdown(ctx, []byte(""), "test.html")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result, got %q", result)
	}
	if pages != 0 {
		t.Errorf("expected 0 pages, got %d", pages)
	}
}

func TestParseHTMLBytesToMarkdown_WhitespaceOnly(t *testing.T) {
	ctx := context.Background()
	result, pages, err := ParseHTMLBytesToMarkdown(ctx, []byte("  \n  \t  "), "test.html")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result, got %q", result)
	}
	if pages != 0 {
		t.Errorf("expected 0 pages, got %d", pages)
	}
}

func TestParseHTMLBytesToMarkdown_SimpleParagraph(t *testing.T) {
	ctx := context.Background()
	htmlData := []byte("<html><body><p>Hello</p></body></html>")
	result, pages, err := ParseHTMLBytesToMarkdown(ctx, htmlData, "test.html")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Hello") {
		t.Errorf("expected 'Hello' in output, got: %s", result)
	}
	if pages < 1 {
		t.Error("expected at least 1 page")
	}
}

func TestParseHTMLBytesToMarkdown_H1(t *testing.T) {
	ctx := context.Background()
	htmlData := []byte("<html><body><h1>Title</h1></body></html>")
	result, _, err := ParseHTMLBytesToMarkdown(ctx, htmlData, "test.html")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "# Title") {
		t.Errorf("expected '# Title' in output, got: %s", result)
	}
}

func TestParseHTMLBytesToMarkdown_H2(t *testing.T) {
	ctx := context.Background()
	htmlData := []byte("<html><body><h2>Subtitle</h2></body></html>")
	result, _, err := ParseHTMLBytesToMarkdown(ctx, htmlData, "test.html")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "## Subtitle") {
		t.Errorf("expected '## Subtitle' in output, got: %s", result)
	}
}

func TestParseHTMLBytesToMarkdown_Bold(t *testing.T) {
	ctx := context.Background()
	htmlData := []byte("<html><body><b>bold</b></body></html>")
	result, _, err := ParseHTMLBytesToMarkdown(ctx, htmlData, "test.html")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "**bold**") {
		t.Errorf("expected '**bold**' in output, got: %s", result)
	}
}

func TestParseHTMLBytesToMarkdown_Strong(t *testing.T) {
	ctx := context.Background()
	htmlData := []byte("<html><body><strong>strong</strong></body></html>")
	result, _, err := ParseHTMLBytesToMarkdown(ctx, htmlData, "test.html")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "**strong**") {
		t.Errorf("expected '**strong**' in output, got: %s", result)
	}
}

func TestParseHTMLBytesToMarkdown_Italic(t *testing.T) {
	ctx := context.Background()
	htmlData := []byte("<html><body><i>italic</i></body></html>")
	result, _, err := ParseHTMLBytesToMarkdown(ctx, htmlData, "test.html")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "*italic*") {
		t.Errorf("expected '*italic*' in output, got: %s", result)
	}
}

func TestParseHTMLBytesToMarkdown_Em(t *testing.T) {
	ctx := context.Background()
	htmlData := []byte("<html><body><em>emphasis</em></body></html>")
	result, _, err := ParseHTMLBytesToMarkdown(ctx, htmlData, "test.html")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "*emphasis*") {
		t.Errorf("expected '*emphasis*' in output, got: %s", result)
	}
}

func TestParseHTMLBytesToMarkdown_Link(t *testing.T) {
	ctx := context.Background()
	htmlData := []byte(`<html><body><a href="http://example.com">text</a></body></html>`)
	result, _, err := ParseHTMLBytesToMarkdown(ctx, htmlData, "test.html")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "[text](http://example.com)") {
		t.Errorf("expected '[text](http://example.com)' in output, got: %s", result)
	}
}

func TestParseHTMLBytesToMarkdown_Image(t *testing.T) {
	ctx := context.Background()
	htmlData := []byte(`<html><body><img src="x.jpg" alt="alt"></body></html>`)
	result, _, err := ParseHTMLBytesToMarkdown(ctx, htmlData, "test.html")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "![alt](x.jpg)") {
		t.Errorf("expected '![alt](x.jpg)' in output, got: %s", result)
	}
}

func TestParseHTMLBytesToMarkdown_Table(t *testing.T) {
	ctx := context.Background()
	htmlData := []byte(`<html><body><table><tr><td>cell</td></tr></table></body></html>`)
	result, _, err := ParseHTMLBytesToMarkdown(ctx, htmlData, "test.html")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "<table>") {
		t.Errorf("expected '<table>' preserved in output, got: %s", result)
	}
}

func TestParseHTMLBytesToMarkdown_ScriptAndStyleSkipped(t *testing.T) {
	ctx := context.Background()
	htmlData := []byte(`<html><body><script>alert(1)</script><p>visible</p><style>body{}</style></body></html>`)
	result, _, err := ParseHTMLBytesToMarkdown(ctx, htmlData, "test.html")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if strings.Contains(result, "alert") {
		t.Error("script content should not appear in output")
	}
	if !strings.Contains(result, "visible") {
		t.Error("expected 'visible' in output")
	}
}

func TestParseHTMLBytesToMarkdown_CodeBlock(t *testing.T) {
	ctx := context.Background()
	htmlData := []byte("<html><body><pre><code>func main() {}</code></pre></body></html>")
	result, _, err := ParseHTMLBytesToMarkdown(ctx, htmlData, "test.html")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "```") {
		t.Errorf("expected code fence in output, got: %s", result)
	}
	if !strings.Contains(result, "func main() {}") {
		t.Errorf("expected 'func main() {}' in output, got: %s", result)
	}
}

func TestParseHTMLBytesToMarkdown_ListItem(t *testing.T) {
	ctx := context.Background()
	htmlData := []byte("<html><body><ul><li>Item 1</li><li>Item 2</li></ul></body></html>")
	result, _, err := ParseHTMLBytesToMarkdown(ctx, htmlData, "test.html")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "- Item 1") {
		t.Errorf("expected '- Item 1' in output, got: %s", result)
	}
}

func TestDetectHTMLCharset_GBK(t *testing.T) {
	html := []byte(`<html><head><meta charset="gbk"></head><body></body></html>`)
	charset := detectHTMLCharset(html)
	if strings.ToLower(charset) != "gbk" {
		t.Errorf("expected 'gbk', got '%s'", charset)
	}
}

func TestDetectHTMLCharset_UTF8(t *testing.T) {
	html := []byte(`<html><head><meta charset="utf-8"></head><body></body></html>`)
	charset := detectHTMLCharset(html)
	if strings.ToLower(charset) != "utf-8" {
		t.Errorf("expected 'utf-8', got '%s'", charset)
	}
}

func TestDetectHTMLCharset_NoCharset(t *testing.T) {
	result := detectHTMLCharset([]byte("<html><body></body></html>"))
	if result != "" {
		t.Errorf("expected empty string, got '%s'", result)
	}
}

func TestGetCharsetDecoder_GBK(t *testing.T) {
	dec := getCharsetDecoder("gbk")
	if dec == nil {
		t.Error("expected non-nil decoder for gbk")
	}
}

func TestGetCharsetDecoder_GB2312(t *testing.T) {
	dec := getCharsetDecoder("gb2312")
	if dec == nil {
		t.Error("expected non-nil decoder for gb2312")
	}
}

func TestGetCharsetDecoder_GB18030(t *testing.T) {
	dec := getCharsetDecoder("gb18030")
	if dec == nil {
		t.Error("expected non-nil decoder for gb18030")
	}
}

func TestGetCharsetDecoder_Big5(t *testing.T) {
	dec := getCharsetDecoder("big5")
	if dec == nil {
		t.Error("expected non-nil decoder for big5")
	}
}

func TestGetCharsetDecoder_Unknown(t *testing.T) {
	// Note: the sample used koi8-r. iso-8859-1 became **decodable** after the
	// extension (windows-1252 per WHATWG), so it no longer represents an
	// "undecodable charset"; koi8-r is deliberately outside the decodable set.
	dec := getCharsetDecoder("koi8-r")
	if dec != nil {
		t.Error("expected nil decoder for unsupported charset")
	}
}

func TestCleanMarkdown_MultipleBlankLines(t *testing.T) {
	input := "line1\n\n\n\nline2\n\n\nline3"
	expected := "line1\n\nline2\n\nline3"
	result := cleanMarkdown(input)
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestCleanMarkdown_TrimSpace(t *testing.T) {
	input := "\n\n  hello  \n\n"
	expected := "hello"
	result := cleanMarkdown(input)
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestCleanMarkdown_NoChange(t *testing.T) {
	input := "hello\n\nworld"
	result := cleanMarkdown(input)
	if result != input {
		t.Errorf("expected unchanged %q, got %q", input, result)
	}
}
