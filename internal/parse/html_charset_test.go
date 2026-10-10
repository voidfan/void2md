package parse

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// encodeWith 用指定编码器把 UTF-8 文本编成目标编码字节。
// 解码测试一律用**真实编码字节**而非手写/ASCII 字节：ASCII 在多数编码下是透明的，
// 用 ASCII 构造的 fixture 在「用错解码器」和「完全不解码」两种错误实现下都照样通过。
func encodeWith(t *testing.T, text string, enc transform.Transformer) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := transform.NewWriter(&buf, enc)
	if _, err := w.Write([]byte(text)); err != nil {
		t.Fatalf("encode %q: %v", text, err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close encoder: %v", err)
	}
	return buf.Bytes()
}

// gbkHTML 构造一个 GBK 编码、且 <meta> 声称 gbk 的页面。
func gbkHTML(t *testing.T) []byte {
	t.Helper()
	utf8HTML := `<html><head><meta charset="gbk"></head><body><p>中文内容</p></body></html>`
	var buf bytes.Buffer
	w := transform.NewWriter(&buf, simplifiedchinese.GBK.NewEncoder())
	if _, err := w.Write([]byte(utf8HTML)); err != nil {
		t.Fatalf("encode gbk: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close gbk writer: %v", err)
	}
	return buf.Bytes()
}

// TestParseHTMLBytesToMarkdown_Unchanged 锁定既有入口的行为：仍按 <meta charset> 解码。
func TestParseHTMLBytesToMarkdown_Unchanged(t *testing.T) {
	md, pages, err := ParseHTMLBytesToMarkdown(context.Background(), gbkHTML(t), "x.html")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(md, "中文内容") {
		t.Errorf("meta-declared gbk should decode correctly, got %q", md)
	}
	if pages < 1 {
		t.Errorf("pages = %d, want >= 1", pages)
	}
}

// TestParseHTMLBytesToMarkdownWithCharset_HintWins 锁定 charset hint 优先于 <meta>。
// 页面字节已是 UTF-8（无 meta），调用方传 "utf-8" hint 时必须原样使用，不得二次解码。
func TestParseHTMLBytesToMarkdownWithCharset_HintWins(t *testing.T) {
	utf8HTML := []byte(`<html><body><p>中文内容</p></body></html>`)
	md, _, err := ParseHTMLBytesToMarkdownWithCharset(context.Background(), utf8HTML, "x.html", "utf-8")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(md, "中文内容") {
		t.Errorf("utf-8 hint must keep bytes as-is, got %q", md)
	}
}

// TestDecodeHTMLBytes_HintOverridesMeta 直接锁定「hint 覆盖 meta」这一核心行为：
// 字节是 UTF-8，但 meta 自称 gbk —— 传 utf-8 hint 时不得按 gbk 解，否则乱码。
//
// fixture 必须含**非 ASCII** 字符：x/text 的 GBK 解码器对 ASCII 是透明的，
// 若 fixture 全为 ASCII，「忽略 hint、按 gbk 解」得到的字节与原文完全相同，
// 断言照样通过——这条测试就退化成空转（Task 3 spec 评审实测确认）。
// 末尾的对照断言即是防止有人再把 fixture 换回 ASCII。
func TestDecodeHTMLBytes_HintOverridesMeta(t *testing.T) {
	// 字节是 UTF-8 且含中文，但 meta 自称 gbk：这正是「声明与真实字节冲突」的场景。
	utf8Bytes := []byte(`<meta charset="gbk"><p>中文内容</p>`)

	// 有 utf-8 hint：原样返回，不得按 meta 的 gbk 再解一次
	got, err := DecodeHTMLBytes(utf8Bytes, "utf-8")
	if err != nil {
		t.Fatalf("hint decode error: %v", err)
	}
	if string(got) != string(utf8Bytes) {
		t.Errorf("utf-8 hint must return bytes unchanged, got %q", got)
	}
	if !strings.Contains(string(got), "中文内容") {
		t.Errorf("hint 必须优先于 meta：中文被按 gbk 误解成了 %q", got)
	}

	// 对照：无 hint 时按 meta 的 gbk 解，中文必然乱码。
	// 这条同时证明 fixture 有区分两条路径的能力——若它哪天不再乱码，
	// 说明 fixture 退化了，上面的断言也就重新变成空转。
	noHint, err := DecodeHTMLBytes(utf8Bytes, "")
	if err != nil {
		t.Fatalf("no-hint decode error: %v", err)
	}
	if strings.Contains(string(noHint), "中文内容") {
		t.Errorf("fixture 失效：无 hint 时也解出了正确中文，无法区分 hint 与 meta 两条路径")
	}
}

// TestParseHTMLBytesToMarkdownWithCharset_EmptyInput 与既有入口一致的边界行为。
func TestParseHTMLBytesToMarkdownWithCharset_EmptyInput(t *testing.T) {
	md, pages, err := ParseHTMLBytesToMarkdownWithCharset(context.Background(), []byte("   \n"), "x.html", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if md != "" || pages != 0 {
		t.Errorf("empty input = (%q, %d), want (\"\", 0)", md, pages)
	}
}

// TestDecodeHTMLBytes_HintTriggersDecode 锁定 hint 的**正向**作用：hint 必须真的
// 作为解码来源，而不只是用来抑制 meta 解码。
//
// 缺口是实测出来的：若实现被改成「hint 只抑制 meta、永不用作解码来源」，
// 原先那 5 条测试**全部通过**——因为它们只传 "" 或 "utf-8"，全是抑制分支。
func TestDecodeHTMLBytes_HintTriggersDecode(t *testing.T) {
	// GBK 字节、**无 meta**：探测不到编码，无 hint 时只能原样返回（乱码）。
	gbkNoMeta := append([]byte(`<html><body><p>`), 0xD6, 0xD0, 0xCE, 0xC4)
	gbkNoMeta = append(gbkNoMeta, []byte(`</p></body></html>`)...)

	raw, err := DecodeHTMLBytes(gbkNoMeta, "")
	if err != nil {
		t.Fatalf("no-hint decode error: %v", err)
	}
	if strings.Contains(string(raw), "中文") {
		t.Fatal("fixture 退化：无 hint 也解出了中文，无法区分 hint 是否真的触发了解码")
	}

	got, err := DecodeHTMLBytes(gbkNoMeta, "gbk")
	if err != nil {
		t.Fatalf("hint gbk: %v", err)
	}
	if !strings.Contains(string(got), "中文") {
		t.Errorf("hint \"gbk\" 必须触发 GBK 解码，got %q", got)
	}
}

// TestDecodeHTMLBytes_HintNormalization 覆盖 hint 的大小写与空白归一化。
// 这不是理论情形：mime.ParseMediaType 对 "text/html; charset=UTF-8" 返回的
// "UTF-8" **保留大小写**（实测确认），而 Task 6 的 Fetcher 计划把这个值当作 hint 传进来。
func TestDecodeHTMLBytes_HintNormalization(t *testing.T) {
	// meta 声称 gbk、字节却是 UTF-8：归一化若失效（把 "UTF-8" 当成可解编码去解，
	// 或没 TrimSpace 而整串匹配不上），就会退到 meta 按 gbk 解，中文变乱码。
	utf8WithGbkMeta := []byte(`<meta charset="gbk"><p>中文内容</p>`)

	for _, hint := range []string{"utf-8", "UTF-8", "Utf-8", " utf-8 ", "\tUTF-8\n"} {
		got, err := DecodeHTMLBytes(utf8WithGbkMeta, hint)
		if err != nil {
			t.Fatalf("hint %q: %v", hint, err)
		}
		if !bytes.Equal(got, utf8WithGbkMeta) {
			t.Errorf("hint %q 应视为 utf-8（不做解码），got %q", hint, got)
		}
	}

	// 全空白 hint 等价于空 hint → 回退 meta 探测 → 按 gbk 解，故必然与原字节不同。
	blank, err := DecodeHTMLBytes(utf8WithGbkMeta, "   ")
	if err != nil {
		t.Fatalf("blank hint: %v", err)
	}
	if bytes.Equal(blank, utf8WithGbkMeta) {
		t.Error("全空白 hint 应等价于空 hint 并回退到 meta 探测，不应原样返回")
	}

	// 可解编码的大小写归一化——这才是真正的缺口。上面那个循环里的 "UTF-8" 家族在
	// DecodeHTMLBytes 的 utf-8 特判处（charset == "" || isUTF8Charset(charset)）就被
	// 短路了，根本走不到 getCharsetDecoder，所以「把 getCharsetDecoder 的
	// switch strings.ToLower(charset) 改成 switch charset」这个变异此前无人能抓。
	// fixture 刻意**不带 meta**：少了 ToLower 时 charset 会保持 "GBK" 原样，
	// getCharsetDecoder 返回 nil，直接变成 unsupported charset 报错。
	gbkNoMeta := append([]byte(`<p>`), 0xD6, 0xD0, 0xCE, 0xC4)
	gbkNoMeta = append(gbkNoMeta, []byte(`</p>`)...)
	for _, hint := range []string{"gbk", "GBK", "GbK", " gbk ", "\tGBK\n"} {
		got, err := DecodeHTMLBytes(gbkNoMeta, hint)
		if err != nil {
			t.Fatalf("hint %q: %v", hint, err)
		}
		if !strings.Contains(string(got), "中文") {
			t.Errorf("hint %q 必须归一化后按 GBK 解码，got %q", hint, got)
		}
	}

	// 扩展后的新编码族同样要覆盖大写形式（用真实 EUC-KR 字节，非 ASCII）。
	euckrNoMeta := append([]byte(`<p>`), encodeWith(t, "한국어", korean.EUCKR.NewEncoder())...)
	euckrNoMeta = append(euckrNoMeta, []byte(`</p>`)...)
	for _, hint := range []string{"euc-kr", "EUC-KR", "Euc-KR", " EUC-KR "} {
		got, err := DecodeHTMLBytes(euckrNoMeta, hint)
		if err != nil {
			t.Fatalf("hint %q: %v", hint, err)
		}
		if !strings.Contains(string(got), "한국어") {
			t.Errorf("hint %q 必须归一化后按 EUC-KR 解码，got %q", hint, got)
		}
	}
}

// TestDecodeHTMLBytes_UnsupportedHint 覆盖 hint 不可解时的两条出路（回退 meta / 报错），
// 以及 hint 可解时对 meta 的胜出（meta 可解与不可解各一例）。
func TestDecodeHTMLBytes_UnsupportedHint(t *testing.T) {
	gbkBytes := append([]byte(`<meta charset="gbk"><p>`), 0xD6, 0xD0, 0xCE, 0xC4)
	gbkBytes = append(gbkBytes, []byte(`</p>`)...)

	// 1) hint 不可解 + meta 可解 → 回退到 meta，中文必须解得出来。
	// 「不可解」的范例用 koi8-r：它刻意不在扩展集合里（单字节编码能解任意字节，
	// 一旦可解，「不可解」这个信号就消失了）。
	got, err := DecodeHTMLBytes(gbkBytes, "koi8-r")
	if err != nil {
		t.Fatalf("hint 不可解但 meta 可解时不应报错，got %v", err)
	}
	if !strings.Contains(string(got), "中文") {
		t.Errorf("应回退到 <meta charset=\"gbk\"> 解出中文，got %q", got)
	}

	// 2) hint 不可解 + 无 meta → 报错（保留可观测性，不静默返回乱码）
	noMeta := append([]byte(`<p>`), 0xD6, 0xD0, 0xCE, 0xC4)
	noMeta = append(noMeta, []byte(`</p>`)...)
	if _, err := DecodeHTMLBytes(noMeta, "koi8-r"); err == nil {
		t.Error("hint 不可解且无 meta 时应返回 unsupported charset 错误")
	}

	// 3) 可解的 hint 仍然最权威：字节是 GBK、hint 是真实可解的 gbk，meta 却谎称 big5。
	// 必须按 hint 的 gbk 解出「中文」。
	//
	// meta 谎称的编码必须是本仓库**可解**的（big5 可解；koi8-r 不可解）：只有两者都可解，
	// 断言才在区分「用对了哪个解码器」而不是在测回退分支。断言取**正向**（必须解出中文）
	// 而不是「不得解出中文」——后者在「用错了解码器」和「完全不解码」两种错误实现下
	// 都照样通过，等于空转（这正是被替换掉的那条旧断言）。
	gbkLyingMeta := append([]byte(`<meta charset="big5"><p>`), 0xD6, 0xD0, 0xCE, 0xC4)
	gbkLyingMeta = append(gbkLyingMeta, []byte(`</p>`)...)

	viaHint, err := DecodeHTMLBytes(gbkLyingMeta, "gbk")
	if err != nil {
		t.Fatalf("hint gbk: %v", err)
	}
	if !strings.Contains(string(viaHint), "中文") {
		t.Errorf("可解的 hint 必须最权威：hint=gbk 时不得改用 meta 的 big5 解码，got %q", viaHint)
	}

	// 对照：无 hint 时按 meta 的 big5 解，中文必然解错。这证明该 fixture 有区分
	// 「hint 胜出」与「meta 胜出」两条路径的能力——若它哪天不再解错，上面的断言就退化成空转。
	viaMeta, err := DecodeHTMLBytes(gbkLyingMeta, "")
	if err != nil {
		t.Fatalf("no-hint decode error: %v", err)
	}
	if strings.Contains(string(viaMeta), "中文") {
		t.Error("fixture 失效：无 hint 时也解出了正确中文，无法区分 hint 与 meta 两条路径")
	}

	// 4) meta 声明的是**不可解**编码（koi8-r）时，可解的 hint 同样必须胜出：
	// 既不得报错，也必须由 hint 解出中文。走的是「hint 可解 → 根本不看 meta」这条路，
	// 若有人把可解的 hint 也拿去与 meta 比较，这里的 meta 会立刻把结果变成 unsupported charset 错误。
	gbkUnsolvableMeta := append([]byte(`<meta charset="koi8-r"><p>`), 0xD6, 0xD0, 0xCE, 0xC4)
	gbkUnsolvableMeta = append(gbkUnsolvableMeta, []byte(`</p>`)...)

	viaHint2, err := DecodeHTMLBytes(gbkUnsolvableMeta, "gbk")
	if err != nil {
		t.Fatalf("hint gbk + meta 不可解：hint 可解时不应报错，got %v", err)
	}
	if !strings.Contains(string(viaHint2), "中文") {
		t.Errorf("hint=gbk 必须作为解码来源解出中文，got %q", viaHint2)
	}
}

// TestDecodeHTMLBytes_UnsolvableHintReportsMetaName 锁定**错误文本契约**：
// hint 不可解、回退到的 <meta> 声明也不可解时，错误里报的必须是**该 <meta> 声明的名字**
// （见 ParseHTMLBytesToMarkdownWithCharset 的文档：「按该声明的名字报错」），而不是被
// 回退掉的原始 hint。
//
// 缺口是实测出来的：这条分支此前零覆盖，把 :98 改成
// fmt.Errorf("unsupported charset: %s", charsetHint)（用原始 hint 取代解析后的 charset）
// 全包全绿（X20）。后果是调用方按错误文本排障时被指向一个页面根本没声称过的编码。
func TestDecodeHTMLBytes_UnsolvableHintReportsMetaName(t *testing.T) {
	// hint 与 meta 都刻意选不可解：koi8-r 与 iso-8859-2 都不在扩展集合内。
	data := []byte(`<meta charset="iso-8859-2"><p>hi</p>`)

	_, err := DecodeHTMLBytes(data, "koi8-r")
	if err == nil {
		t.Fatal("hint 与 meta 都不可解时必须报错，不得静默返回（这是唯一的可观测信号）")
	}
	if !strings.Contains(err.Error(), "iso-8859-2") {
		t.Errorf("错误必须按 <meta> 声明的名字报错，got %q", err)
	}
	// 反向断言：原始 hint 已被回退掉，不该出现在错误文本里。
	// 正向断言只能证明「iso-8859-2 在」，把 hint 与 charset 拼在一起同样能通过。
	if strings.Contains(err.Error(), "koi8-r") {
		t.Errorf("错误不应出现已被回退掉的原始 hint，got %q", err)
	}
}

// <meta charset="utf-8"> 是合法声明，即使 getCharsetDecoder("utf-8") == nil
// （utf-8 不走 decoder，由 DecodeHTMLBytes 里的特判原样返回），也必须算「探测到了可用声明」。
//
// 缺口是实测出来的：若把回退条件收紧成「meta 非空**且** getCharsetDecoder(meta) != nil」，
// meta="utf-8" + 不可解 hint 会从「原样返回」变成 unsupported charset 报错，而此前无测试能抓。
// 这类页面并不罕见：header 给了个探测不到的编码、页面自己声明 utf-8。
func TestDecodeHTMLBytes_UnsupportedHintFallsBackToUTF8Meta(t *testing.T) {
	data := []byte(`<meta charset="utf-8"><p>hi</p>`)

	got, err := DecodeHTMLBytes(data, "koi8-r")
	if err != nil {
		t.Fatalf("meta=utf-8 是合法声明，hint 不可解时应原样返回而非报错，got %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("meta=utf-8 → 原样返回，got %q", got)
	}
}

// TestParseHTMLBytesToMarkdownWithCharset_DecodeFailureFallsBackToRaw 锁定解码失败的降级路径：
// DecodeHTMLBytes 报错时**不得**让整页失败，而是记一条 Warn 后按原始字节继续解析。
//
// 这条降级路径在本任务之前就存在，但本任务新增了触发来源（hint 不可解 + 无 meta），
// 更容易被走到；此前零覆盖：把它改成直接 return error 无测试能抓。
func TestParseHTMLBytesToMarkdownWithCharset_DecodeFailureFallsBackToRaw(t *testing.T) {
	// GBK 字节、无 meta、hint 不可解（koi8-r 刻意不可解）→ DecodeHTMLBytes 必然报错。
	// 骨架里放一个 ASCII 锚点 MARKER：下面的断言必须看到它，才算真的锁定了「降级后
	// **原始字节**被继续解析」。
	raw := append([]byte(`<html><body><p>MARKER</p>`), 0xD6, 0xD0, 0xCE, 0xC4)
	raw = append(raw, []byte(`</body></html>`)...)

	md, pages, err := ParseHTMLBytesToMarkdownWithCharset(context.Background(), raw, "x.html", "koi8-r")
	if err != nil {
		t.Fatalf("解码失败应降级为原始字节继续解析，不应向上返回错误，got %v", err)
	}
	if pages != 1 {
		t.Errorf("pages = %d, want 1", pages)
	}
	// 断言 MARKER 出现，而**不是**只断言非空：只断言 len(md) > 0 时，把 :57 的
	// decoded = htmlBytes 换成 decoded = []byte("<p>x</p>")（原始字节被完全丢弃）照样全绿
	// ——「非空」这个断言此前能否杀掉 decoded=nil，只是「md 恰好为空」的副产品，并没有
	// 兑现这条测试注释所声称的契约（评审实测确认，X23）。
	if !strings.Contains(md, "MARKER") {
		t.Errorf("降级路径必须继续用原始字节解析：输出里找不到原始字节中的锚点 MARKER，got %q"+
			"（等价于 decoded=nil，或 decoded 被换成了别的字节）", md)
	}
	// 乱码字节照旧不比对内容：把它们固化成期望值会在无害实现变动（如换页数公式）时误报。
	// 「原始字节确实进了 DOM」由上面的 MARKER 锚点负责，它才是这条测试注释的兑现方式。
	if len(md) == 0 {
		t.Error("降级路径必须继续用原始字节解析出内容，got 空 markdown（等价于 decoded=nil）")
	}
}

// TestGetCharsetDecoder_UnsolvableLabels 锁定**刻意不加**的编码族。
//
// 单字节西欧/西里尔编码能解任意字节，一旦进查表，「不可解」这个信号就消失，
// 回退 <meta> 探测的分支（以及「meta 也不可解时报错」的观测口径）就再不会被走到。
// 用户只拍板了 latin-1/1252 这一组，其余单字节编码必须保持不可解。
//
// "utf-8"/"utf8" 一并列入：utf-8 的语义是「原样返回」，由 DecodeHTMLBytes 特判，
// 不该也不能由 getCharsetDecoder 返回一个 decoder 来表达。
func TestGetCharsetDecoder_UnsolvableLabels(t *testing.T) {
	for _, label := range []string{
		"koi8-r", "koi8-u", "iso-8859-2", "iso-8859-5", "iso-8859-15",
		"windows-1251", "cp1251", "macintosh", "", "utf-8", "utf8",
	} {
		if dec := getCharsetDecoder(label); dec != nil {
			t.Errorf("getCharsetDecoder(%q) 必须为 nil（不在扩展集合内）", label)
		}
	}
}

// TestIsUTF8Charset_RejectsNonUTF8Labels 钉住 isUTF8Charset 的**排除侧**：
// 白名单之外的 label 必须一律返回 false。
//
// 缺口是实测出来的：往 :112 的 case 列表里加 "gb2312"（X26）或 "big5"（X27）后，
// 扩展提交新增的 280 行测试**全绿**——此前没有任何断言检查过「非 UTF-8 的 label 必须
// 返回 false」，测试对那一行的存在理由零检测力。
// 后果不轻：这两族页面会从「按 gbk/big5 正确解码」静默变成「原样返回乱码字节、
// 不报错、无告警」（isUTF8Charset 为 true 直接短路返回原字节）。
//
// 白名单**内侧**（utf-8 及 utf8 / unicode-1-1-utf-8 别名）由 TestDecodeHTMLBytes_UTF8Aliases 覆盖。
func TestIsUTF8Charset_RejectsNonUTF8Labels(t *testing.T) {
	for _, l := range []string{"gbk", "gb2312", "gb18030", "big5", "shift_jis", "euc-jp",
		"iso-2022-jp", "euc-kr", "iso-8859-1", "koi8-r", ""} {
		if isUTF8Charset(l) {
			t.Errorf("isUTF8Charset(%q) 必须为 false", l)
		}
	}
}

// TestDecodeHTMLBytes_ExtendedCharsetsDecode 覆盖扩展后每个 label 的解码**去向**：
// 用真实编码字节构造 fixture，断言解出原文。wire 错了编码族（例如 shift_jis 接到 EUCJP）
// 必然解不出原文，断言会失败。
func TestDecodeHTMLBytes_ExtendedCharsetsDecode(t *testing.T) {
	const ja = "日本語のテスト"
	const ko = "한국어 테스트"
	const west = "café “résumé”"

	cases := []struct {
		name  string
		label string
		text  string
		enc   transform.Transformer
	}{
		{"shift_jis", "shift_jis", ja, japanese.ShiftJIS.NewEncoder()},
		{"shift-jis", "shift-jis", ja, japanese.ShiftJIS.NewEncoder()},
		{"sjis", "sjis", ja, japanese.ShiftJIS.NewEncoder()},
		{"x-sjis", "x-sjis", ja, japanese.ShiftJIS.NewEncoder()},
		{"ms_kanji", "ms_kanji", ja, japanese.ShiftJIS.NewEncoder()},
		{"windows-31j", "windows-31j", ja, japanese.ShiftJIS.NewEncoder()},
		{"cp932", "cp932", ja, japanese.ShiftJIS.NewEncoder()},
		{"euc-jp", "euc-jp", ja, japanese.EUCJP.NewEncoder()},
		{"eucjp", "eucjp", ja, japanese.EUCJP.NewEncoder()},
		{"x-euc-jp", "x-euc-jp", ja, japanese.EUCJP.NewEncoder()},
		{"iso-2022-jp", "iso-2022-jp", ja, japanese.ISO2022JP.NewEncoder()},
		{"iso2022jp", "iso2022jp", ja, japanese.ISO2022JP.NewEncoder()},
		{"euc-kr", "euc-kr", ko, korean.EUCKR.NewEncoder()},
		{"euckr", "euckr", ko, korean.EUCKR.NewEncoder()},
		{"ks_c_5601-1987", "ks_c_5601-1987", ko, korean.EUCKR.NewEncoder()},
		{"ksc5601", "ksc5601", ko, korean.EUCKR.NewEncoder()},
		{"iso-8859-1", "iso-8859-1", west, charmap.Windows1252.NewEncoder()},
		{"iso8859-1", "iso8859-1", west, charmap.Windows1252.NewEncoder()},
		{"iso_8859-1", "iso_8859-1", west, charmap.Windows1252.NewEncoder()},
		{"latin1", "latin1", west, charmap.Windows1252.NewEncoder()},
		{"latin-1", "latin-1", west, charmap.Windows1252.NewEncoder()},
		{"l1", "l1", west, charmap.Windows1252.NewEncoder()},
		{"cp819", "cp819", west, charmap.Windows1252.NewEncoder()},
		{"windows-1252", "windows-1252", west, charmap.Windows1252.NewEncoder()},
		{"cp1252", "cp1252", west, charmap.Windows1252.NewEncoder()},
		{"x-cp1252", "x-cp1252", west, charmap.Windows1252.NewEncoder()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := append([]byte(`<html><body><p>`), encodeWith(t, c.text, c.enc)...)
			body = append(body, []byte(`</p></body></html>`)...)

			got, err := DecodeHTMLBytes(body, c.label)
			if err != nil {
				t.Fatalf("hint %q: %v", c.label, err)
			}
			if !strings.Contains(string(got), c.text) {
				t.Errorf("hint %q 必须解出 %q，got %q", c.label, c.text, got)
			}
		})
	}
}

// TestDecodeHTMLBytes_WesternLatin1IsWindows1252 锁定「西欧一律映射到 windows-1252」这一决策。
//
// 按 WHATWG 编码标准（也是浏览器的行为），HTTP/HTML 里的 iso-8859-1 label 实际就该按
// windows-1252 解：否则 0x80–0x9F 区间的弯引号会变成 C1 控制符（U+0093）而不是 “ (U+201C)。
// 若把它错接到 charmap.ISO8859_1，0xE9 (é) 之类的字节仍然正确，**只有** 0x80–0x9F 能区分，
// 所以 fixture 必须含弯引号——这条断言正是用来区分这两个解码器的。
func TestDecodeHTMLBytes_WesternLatin1IsWindows1252(t *testing.T) {
	// windows-1252 字节：caf<0xE9> <0x93>r<0xE9>sum<0xE9><0x94> = café “résumé”
	raw := append([]byte(`<p>caf`), 0xE9, 0x20, 0x93, 'r', 0xE9, 's', 'u', 'm', 0xE9, 0x94)
	raw = append(raw, []byte(`</p>`)...)
	const want = "café “résumé”"

	for _, hint := range []string{
		"iso-8859-1", "iso8859-1", "iso_8859-1", "latin1", "latin-1", "l1", "cp819",
		"windows-1252", "cp1252", "x-cp1252",
	} {
		got, err := DecodeHTMLBytes(raw, hint)
		if err != nil {
			t.Fatalf("hint %q: %v", hint, err)
		}
		if !strings.Contains(string(got), want) {
			t.Errorf("hint %q 必须按 windows-1252 解出 %q，got %q", hint, want, got)
		}
		if strings.ContainsRune(string(got), '\u0093') {
			t.Errorf("hint %q 不能按 ISO-8859-1 解（0x93 变成 C1 控制符 U+0093），got %q", hint, got)
		}
	}
}

// TestDecodeHTMLBytes_WesternLabelOnUTF8BytesIsMojibake 显式钉住一个**已知代价**，不是 bug。
//
// 把 iso-8859-1/windows-1252 加进查表，堵死了 /v1/pdfparse 唯一的恢复路径
// 「解码报错 → 用原始字节继续解析」：单字节编码能解任意字节，不再报错。
// 于是「<meta> 声明旧西欧编码、字节实为 UTF-8」的页面，从此前侥幸解出的正确文本
// 变成静默乱码（无 error、无 warn）。用户已权衡后选择保留（收益是真实旧西欧编码
// 页面能被解出）。
//
// 若有人让这条测试失败，那不是发现 bug，而是**有意改变该权衡**——请先回读设计 §8.11。
func TestDecodeHTMLBytes_WesternLabelOnUTF8BytesIsMojibake(t *testing.T) {
	// 字节是 UTF-8 且含中文，meta 却声明 iso-8859-1。
	utf8Bytes := []byte(`<meta charset="iso-8859-1"><p>中文内容</p>`)

	// 三个 hint 都必须覆盖："" 走 <meta> 探测，两个显式西欧 label 走 hint 分支——
	// 是两条不同的代码路径，都得钉住。
	for _, hint := range []string{"", "iso-8859-1", "windows-1252"} {
		got, err := DecodeHTMLBytes(utf8Bytes, hint)
		if err != nil {
			t.Fatalf("hint %q: 单字节编码不应报错，got %v", hint, err)
		}
		// 负向断言在这里是**恰当**的：单字节解码必然成功且必然产出非中文，
		// 不存在「用错解码器也恰好通过」的空转风险。
		if strings.Contains(string(got), "中文内容") {
			t.Errorf("hint %q: 该 fixture 应当解成乱码（已拍板的代价）；"+
				"若这条失败，说明权衡被有意改变了，请先回读设计 §8.11", hint)
		}
	}
}

// TestDecodeHTMLBytes_UTF8Aliases 覆盖 UTF-8 的无短横线别名。
//
// 缺口是实测出来的：DecodeHTMLBytes 只特判 "utf-8"，而 HTTP 头写 charset=utf8 很常见。
// 没有别名时 "utf8" 会被判为「不可解」并回退到 <meta>，一个声明 utf-8 的页面会被按
// meta 里的另一种编码误解——本条 fixture 让 meta 谎称 gbk，正好把这个回退变成乱码。
func TestDecodeHTMLBytes_UTF8Aliases(t *testing.T) {
	// 字节是 UTF-8 且含中文，meta 谎称 gbk。
	data := []byte(`<meta charset="gbk"><p>中文内容</p>`)

	for _, hint := range []string{
		"utf8", "UTF8", "Utf8", " utf8 ", "unicode-1-1-utf-8", "UNICODE-1-1-UTF-8",
	} {
		got, err := DecodeHTMLBytes(data, hint)
		if err != nil {
			t.Fatalf("hint %q: %v", hint, err)
		}
		if !bytes.Equal(got, data) {
			t.Errorf("hint %q 应视为 utf-8（原样返回、不看 meta），got %q", hint, got)
		}
	}

	// 对照：没有别名语义时上面会退到 meta 的 gbk 解出乱码——证明该 fixture 能区分
	// 「utf-8 别名」与「不可解 → 回退 meta」两条路径。
	viaMeta, err := DecodeHTMLBytes(data, "")
	if err != nil {
		t.Fatalf("no-hint decode error: %v", err)
	}
	if bytes.Equal(viaMeta, data) {
		t.Error("fixture 失效：无 hint 时也原样返回，无法区分 utf-8 别名与 meta 回退")
	}
}

// TestParseHTMLBytesToMarkdownWithCharset_HintIsUsedAsDecoder 锁定**包装入口转发 hint**。
//
// 缺口是实测出来的：ParseHTMLBytesToMarkdownWithCharset 是 Fetcher 要调用的生产入口，
// 但既有测试只喂过 "utf-8"、""、不可解 hint 三种——恰好都不能区分「转发 hint」与
// 「丢弃 hint（把 :49 的 charsetHint 换成 ""）」，该变异此前全包全绿。
//
// fixture 刻意让 <meta> 谎称 big5：只有真的把可解的 hint 当解码来源用，才能解出中文。
func TestParseHTMLBytesToMarkdownWithCharset_HintIsUsedAsDecoder(t *testing.T) {
	body := append([]byte(`<html><head><meta charset="big5"></head><body><p>`),
		encodeWith(t, "中文内容", simplifiedchinese.GBK.NewEncoder())...)
	body = append(body, []byte(`</p></body></html>`)...)

	md, _, err := ParseHTMLBytesToMarkdownWithCharset(context.Background(), body, "x.html", "gbk")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(md, "中文内容") {
		t.Errorf("包装入口必须把可解的 hint 转发给解码器（hint=gbk），got %q", md)
	}

	// 对照：丢弃 hint 时按 meta 的 big5 解，中文必然解不出。
	mdNoHint, _, err := ParseHTMLBytesToMarkdownWithCharset(context.Background(), body, "x.html", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(mdNoHint, "中文内容") {
		t.Error("fixture 失效：丢弃 hint 也解出了中文，无法区分 hint 是否被转发")
	}
}

// TestDetectHTMLCharset_UppercaseMeta 覆盖正则里的 (?i)：属性名与取值大小写混排也必须认出来。
// 既有 fixture 全是小写，(?i) 此前零覆盖。
// 返回值**不做**大小写归一化（由 getCharsetDecoder 统一 ToLower），所以这里是原样断言。
func TestDetectHTMLCharset_UppercaseMeta(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`<meta CHARSET="gbk">`, "gbk"},
		{`<META CHARSET="GBK">`, "GBK"},
		{`<head><meta CharSet="Shift_JIS"></head>`, "Shift_JIS"},
		{`<head><META Charset="EUC-KR"></head>`, "EUC-KR"},
	}
	for _, c := range cases {
		if got := detectHTMLCharset([]byte(c.in)); got != c.want {
			t.Errorf("detectHTMLCharset(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestDetectHTMLCharset_MultipleDeclarations 覆盖页面里有多个 <meta> 声明的情形：
// detectHTMLCharset 用 FindSubmatch 取**首个**，与 HTML 规范一致（第一个声明生效）。
// 此前零覆盖——改成取最后一个同样全绿。
func TestDetectHTMLCharset_MultipleDeclarations(t *testing.T) {
	utf8First := `<html><head><meta charset="utf-8"><meta charset="gbk"></head></html>`
	if got := detectHTMLCharset([]byte(utf8First)); !strings.EqualFold(got, "utf-8") {
		t.Errorf("多个声明必须取首个（utf-8），got %q", got)
	}

	gbkFirst := `<html><head><meta charset="gbk"><meta charset="utf-8"></head></html>`
	if got := detectHTMLCharset([]byte(gbkFirst)); !strings.EqualFold(got, "gbk") {
		t.Errorf("多个声明必须取首个（gbk），got %q", got)
	}
}

// TestDetectHTMLCharset_AttributeForms 覆盖 charset 声明的各种合法书写形式，
// 重点是 HTML5 推荐的短式**单引号**写法 <meta charset='gbk'>。
//
// 缺口是实测出来的（与父提交一致）：旧正则 (?i)charset[="\s]+([a-zA-Z0-9_-]+) 里
// `charset='gbk'` 的取值前那个 `'` 不在字符类内，匹配不到 → detectHTMLCharset 返回 ""
// → 整页 GBK 字节**静默**原样进 markdown（无 error、无 warn）。而 /v1/pdfparse 这条
// 路径没有下游编码兜底（internal/service/parser.go、internal/parse/mhtml_parser.go），
// 后果就是静默乱码。
func TestDetectHTMLCharset_AttributeForms(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"双引号", `<meta charset="gbk">`, "gbk"},
		{"单引号", `<meta charset='gbk'>`, "gbk"},
		{"无引号", `<meta charset=gbk>`, "gbk"},
		{"等号两侧空白", `<meta charset = "gbk">`, "gbk"},
		{"等号左侧换行", "<meta charset\n=\t'gbk'>", "gbk"},
		{"http-equiv 双引号长式", `<meta http-equiv="Content-Type" content="text/html; charset=gbk">`, "gbk"},
		{"http-equiv 单引号长式", `<meta http-equiv='Content-Type' content='text/html; charset=gbk'>`, "gbk"},
		{"无声明", `<html><body><p>no charset here</p></body></html>`, ""},
		// 反向：非属性位置的 charset 不得被误捕获。
		// 这条正是为了挡住「往字符类里加 ' 」那种改法——它会把 `charset's` 的 `'s`
		// 捕获成取值 "s"，然后在下游触发一次 unsupported charset 报错（或按 "s" 解）。
		{"正文里的 charset's 不得误匹配", `<p>the charset's value is utf-8</p>`, ""},
		// 正文散文里的「charset + 一个**可解**的编码名」不得被当成声明——这是本次修复
		// 危害最大的一类：探测出 "gb2312" 会被真的拿去解码整页，静默乱码。
		// 上面那条 `charset's` 只挡住了 label 不含任何合法编码名的弱变体。
		{"正文里的 charset gb2312 不得误匹配", `<p>charset gb2312 is good</p>`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := detectHTMLCharset([]byte(c.in)); !strings.EqualFold(got, c.want) {
				t.Errorf("detectHTMLCharset(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestDetectHTMLCharset_WindowClamp 锁定 detectHTMLCharset 只扫**前 4096 字节**这个 clamp。
//
// clamp 挡住的是「正文深处出现的 charset=… 被误读成声明」——比如一篇引用了
// `charset=gb2312` 的长文，若扫描整篇就会把散文当成声明，进而按错误的编码解码整页。
// 把 `len(head) > 4096` 放大到 `> 99999999`（等于去掉 clamp）此前全绿：
// 既有的 fixture 里没有一条声明的落点在窗口之外。
//
// 填充用 strings.Repeat 生成，不把 4KB 字面量写进源码。
func TestDetectHTMLCharset_WindowClamp(t *testing.T) {
	const pad = "<!-- pad -->" // 12 字节

	// 声明落在 4096 字节之外 → 探测不到。
	outside := `<html><head>` + strings.Repeat(pad, 400) +
		`<meta charset="gbk"></head><body></body></html>`
	if len(outside) <= 4096 {
		t.Fatalf("fixture 失效：整段只有 %d 字节，声明没有落在 4096 窗口之外", len(outside))
	}
	if got := detectHTMLCharset([]byte(outside)); got != "" {
		t.Errorf("窗口之外的 <meta charset> 不得被读到，got %q", got)
	}

	// 对照：同一份声明落在窗口**之内**必须被读到。少了这条，上面那句在「fixture 本身
	// 损坏 / 正则根本不匹配」的实现下也会通过，等于空转。
	inside := `<html><head>` + strings.Repeat(pad, 10) +
		`<meta charset="gbk"></head><body></body></html>`
	if got := detectHTMLCharset([]byte(inside)); !strings.EqualFold(got, "gbk") {
		t.Errorf("窗口之内的同一份声明必须被读到（对照），got %q", got)
	}
}

// TestDecodeHTMLBytes_SingleQuotedMetaCharset 端到端锁定单引号 <meta charset='gbk'>：
// 真实 GBK 字节必须被解出中文，而不是静默原样（乱码）返回。
//
// fixture 刻意用**真实 GBK 字节**而非 ASCII：ASCII 在 GBK 下是透明的，「解错」与
// 「完全没解」两种实现下 ASCII fixture 都照样通过，断言会退化成空转。
func TestDecodeHTMLBytes_SingleQuotedMetaCharset(t *testing.T) {
	body := append([]byte(`<html><head><meta charset='gbk'></head><body><p>`),
		encodeWith(t, "中文内容", simplifiedchinese.GBK.NewEncoder())...)
	body = append(body, []byte(`</p></body></html>`)...)

	got, err := DecodeHTMLBytes(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(got), "中文内容") {
		t.Errorf("单引号 <meta charset='gbk'> 必须被探测到并按 GBK 解码，got %q", got)
	}
}
