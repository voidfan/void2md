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

// encodeWith encodes UTF-8 text to target-encoding bytes with the given encoder.
// Decode tests always use **real encoded bytes**, not hand-written/ASCII: ASCII
// is transparent under most encodings, so ASCII fixtures pass under both
// "wrong decoder" and "no decoding at all" broken implementations.
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

// gbkHTML builds a GBK-encoded page whose <meta> claims gbk.
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

// TestParseHTMLBytesToMarkdown_Unchanged pins the existing entry's behavior: still decodes per <meta charset>.
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

// TestParseHTMLBytesToMarkdownWithCharset_HintWins pins that the charset hint wins over <meta>.
// The bytes are already UTF-8 (no meta); with a "utf-8" hint they must be used as-is, not re-decoded.
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

// TestDecodeHTMLBytes_HintOverridesMeta directly pins "hint overrides meta":
// bytes are UTF-8 but meta claims gbk — with a utf-8 hint they must not be
// decoded as gbk, else mojibake.
//
// The fixture must contain **non-ASCII** characters: x/text's GBK decoder is
// transparent for ASCII, so an all-ASCII fixture makes "ignore hint, decode as
// gbk" produce bytes identical to the original and the assertion still passes —
// the test degenerates into a no-op (verified in the Task 3 spec review).
// The control assertion at the end prevents anyone from switching the fixture back to ASCII.
func TestDecodeHTMLBytes_HintOverridesMeta(t *testing.T) {
	// Bytes are UTF-8 Chinese, but meta claims gbk: the "declaration conflicts with real bytes" scenario.
	utf8Bytes := []byte(`<meta charset="gbk"><p>中文内容</p>`)

	// With a utf-8 hint: return as-is, must not re-decode per meta's gbk
	got, err := DecodeHTMLBytes(utf8Bytes, "utf-8")
	if err != nil {
		t.Fatalf("hint decode error: %v", err)
	}
	if string(got) != string(utf8Bytes) {
		t.Errorf("utf-8 hint must return bytes unchanged, got %q", got)
	}
	if !strings.Contains(string(got), "中文内容") {
		t.Errorf("hint must win over meta: the Chinese was misdecoded as gbk into %q", got)
	}

	// Control: without a hint, decoding per meta's gbk necessarily garbles the
	// Chinese. This also proves the fixture can distinguish the two paths — if
	// it ever stops garbling, the fixture has degraded and the assertion above
	// becomes a no-op again.
	noHint, err := DecodeHTMLBytes(utf8Bytes, "")
	if err != nil {
		t.Fatalf("no-hint decode error: %v", err)
	}
	if strings.Contains(string(noHint), "中文内容") {
		t.Errorf("fixture broken: correct Chinese decoded even without a hint; cannot distinguish the hint vs meta paths")
	}
}

// TestParseHTMLBytesToMarkdownWithCharset_EmptyInput: boundary behavior matching the existing entry.
func TestParseHTMLBytesToMarkdownWithCharset_EmptyInput(t *testing.T) {
	md, pages, err := ParseHTMLBytesToMarkdownWithCharset(context.Background(), []byte("   \n"), "x.html", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if md != "" || pages != 0 {
		t.Errorf("empty input = (%q, %d), want (\"\", 0)", md, pages)
	}
}

// TestDecodeHTMLBytes_HintTriggersDecode pins the hint's **positive** role: it must
// actually serve as the decode source, not merely suppress meta decoding.
//
// The gap was measured: if the implementation became "hint only suppresses
// meta, never used as decode source", the original 5 tests **all passed** —
// they only pass "" or "utf-8", all suppression branches.
func TestDecodeHTMLBytes_HintTriggersDecode(t *testing.T) {
	// GBK bytes, **no meta**: nothing to detect, so without a hint only as-is return (mojibake) is possible.
	gbkNoMeta := append([]byte(`<html><body><p>`), 0xD6, 0xD0, 0xCE, 0xC4)
	gbkNoMeta = append(gbkNoMeta, []byte(`</p></body></html>`)...)

	raw, err := DecodeHTMLBytes(gbkNoMeta, "")
	if err != nil {
		t.Fatalf("no-hint decode error: %v", err)
	}
	if strings.Contains(string(raw), "中文") {
		t.Fatal("fixture degraded: Chinese decoded even without a hint; cannot tell whether the hint really triggered decoding")
	}

	got, err := DecodeHTMLBytes(gbkNoMeta, "gbk")
	if err != nil {
		t.Fatalf("hint gbk: %v", err)
	}
	if !strings.Contains(string(got), "中文") {
		t.Errorf("hint \"gbk\" must trigger GBK decoding, got %q", got)
	}
}

// TestDecodeHTMLBytes_HintNormalization covers hint case and whitespace normalization.
// Not theoretical: mime.ParseMediaType returns "UTF-8" for
// "text/html; charset=UTF-8" **with case preserved** (verified), and the Task 6
// Fetcher passes that value in as the hint.
func TestDecodeHTMLBytes_HintNormalization(t *testing.T) {
	// meta claims gbk but bytes are UTF-8: if normalization fails (treating "UTF-8"
	// as a decodable charset, or no TrimSpace so the full string doesn't match),
	// it falls back to meta's gbk and the Chinese garbles.
	utf8WithGbkMeta := []byte(`<meta charset="gbk"><p>中文内容</p>`)

	for _, hint := range []string{"utf-8", "UTF-8", "Utf-8", " utf-8 ", "\tUTF-8\n"} {
		got, err := DecodeHTMLBytes(utf8WithGbkMeta, hint)
		if err != nil {
			t.Fatalf("hint %q: %v", hint, err)
		}
		if !bytes.Equal(got, utf8WithGbkMeta) {
			t.Errorf("hint %q should be treated as utf-8 (no decoding), got %q", hint, got)
		}
	}

	// An all-blank hint equals an empty hint → fall back to meta probing → decode as gbk, so it must differ from the original bytes.
	blank, err := DecodeHTMLBytes(utf8WithGbkMeta, "   ")
	if err != nil {
		t.Fatalf("blank hint: %v", err)
	}
	if bytes.Equal(blank, utf8WithGbkMeta) {
		t.Error("an all-blank hint should equal an empty hint and fall back to meta probing, not return as-is")
	}

	// Case normalization of decodable charsets — this is the real gap. The
	// "UTF-8" family in the loop above is short-circuited at DecodeHTMLBytes's
	// utf-8 special case (charset == "" || isUTF8Charset(charset)) and never
	// reaches getCharsetDecoder, so the mutation "change getCharsetDecoder's
	// switch strings.ToLower(charset) to switch charset" was previously
	// uncatchable. The fixture deliberately has **no meta**: without ToLower,
	// charset stays "GBK", getCharsetDecoder returns nil, and it errors as unsupported charset.
	gbkNoMeta := append([]byte(`<p>`), 0xD6, 0xD0, 0xCE, 0xC4)
	gbkNoMeta = append(gbkNoMeta, []byte(`</p>`)...)
	for _, hint := range []string{"gbk", "GBK", "GbK", " gbk ", "\tGBK\n"} {
		got, err := DecodeHTMLBytes(gbkNoMeta, hint)
		if err != nil {
			t.Fatalf("hint %q: %v", hint, err)
		}
		if !strings.Contains(string(got), "中文") {
			t.Errorf("hint %q must be normalized then decoded as GBK, got %q", hint, got)
		}
	}

	// The newly added charset families must also cover uppercase forms (real EUC-KR bytes, non-ASCII).
	euckrNoMeta := append([]byte(`<p>`), encodeWith(t, "한국어", korean.EUCKR.NewEncoder())...)
	euckrNoMeta = append(euckrNoMeta, []byte(`</p>`)...)
	for _, hint := range []string{"euc-kr", "EUC-KR", "Euc-KR", " EUC-KR "} {
		got, err := DecodeHTMLBytes(euckrNoMeta, hint)
		if err != nil {
			t.Fatalf("hint %q: %v", hint, err)
		}
		if !strings.Contains(string(got), "한국어") {
			t.Errorf("hint %q must be normalized then decoded as EUC-KR, got %q", hint, got)
		}
	}
}

// TestDecodeHTMLBytes_UnsupportedHint covers the two exits when the hint is
// undecodable (fall back to meta / error), plus the decodable hint winning over
// meta (one case with a decodable meta, one with an undecodable meta).
func TestDecodeHTMLBytes_UnsupportedHint(t *testing.T) {
	gbkBytes := append([]byte(`<meta charset="gbk"><p>`), 0xD6, 0xD0, 0xCE, 0xC4)
	gbkBytes = append(gbkBytes, []byte(`</p>`)...)

	// 1) Undecodable hint + decodable meta → fall back to meta; the Chinese must decode.
	// The "undecodable" example uses koi8-r: deliberately outside the extended set
	// (single-byte encodings decode any bytes; once decodable, the "undecodable" signal disappears).
	got, err := DecodeHTMLBytes(gbkBytes, "koi8-r")
	if err != nil {
		t.Fatalf("undecodable hint with decodable meta must not error, got %v", err)
	}
	if !strings.Contains(string(got), "中文") {
		t.Errorf("should fall back to <meta charset=\"gbk\"> and decode the Chinese, got %q", got)
	}

	// 2) Undecodable hint + no meta → error (keep observability; don't silently return mojibake)
	noMeta := append([]byte(`<p>`), 0xD6, 0xD0, 0xCE, 0xC4)
	noMeta = append(noMeta, []byte(`</p>`)...)
	if _, err := DecodeHTMLBytes(noMeta, "koi8-r"); err == nil {
		t.Error("undecodable hint with no meta should return an unsupported charset error")
	}

	// 3) A decodable hint is still most authoritative: bytes are GBK, hint is the
	// genuinely decodable gbk, but meta lies with big5. The Chinese must decode per the hint's gbk.
	//
	// The meta's lying charset must itself be **decodable** here (big5 is; koi8-r
	// isn't): only when both are decodable does the assertion distinguish "which
	// decoder was used" rather than testing the fallback branch. The assertion is
	// **positive** (must decode the Chinese) rather than "must not decode the
	// Chinese" — the latter passes under both "wrong decoder" and "no decoding
	// at all" broken implementations, i.e. a no-op (that was the replaced old assertion).
	gbkLyingMeta := append([]byte(`<meta charset="big5"><p>`), 0xD6, 0xD0, 0xCE, 0xC4)
	gbkLyingMeta = append(gbkLyingMeta, []byte(`</p>`)...)

	viaHint, err := DecodeHTMLBytes(gbkLyingMeta, "gbk")
	if err != nil {
		t.Fatalf("hint gbk: %v", err)
	}
	if !strings.Contains(string(viaHint), "中文") {
		t.Errorf("a decodable hint must be most authoritative: with hint=gbk it must not decode via meta's big5, got %q", viaHint)
	}

	// Control: without a hint, decoding per meta's big5 necessarily gets the
	// Chinese wrong. This proves the fixture can distinguish "hint wins" from
	// "meta wins" — if it ever stops decoding wrong, the assertion above degenerates into a no-op.
	viaMeta, err := DecodeHTMLBytes(gbkLyingMeta, "")
	if err != nil {
		t.Fatalf("no-hint decode error: %v", err)
	}
	if strings.Contains(string(viaMeta), "中文") {
		t.Error("fixture broken: correct Chinese decoded even without a hint; cannot distinguish the hint vs meta paths")
	}

	// 4) When meta declares an **undecodable** charset (koi8-r), the decodable
	// hint must equally win: no error, and the Chinese decoded via the hint.
	// This takes the "hint decodable → never look at meta" path; if someone made
	// decodable hints compare against meta too, this meta would immediately turn
	// the result into an unsupported charset error.
	gbkUnsolvableMeta := append([]byte(`<meta charset="koi8-r"><p>`), 0xD6, 0xD0, 0xCE, 0xC4)
	gbkUnsolvableMeta = append(gbkUnsolvableMeta, []byte(`</p>`)...)

	viaHint2, err := DecodeHTMLBytes(gbkUnsolvableMeta, "gbk")
	if err != nil {
		t.Fatalf("hint gbk + undecodable meta: a decodable hint must not error, got %v", err)
	}
	if !strings.Contains(string(viaHint2), "中文") {
		t.Errorf("hint=gbk must serve as the decode source and produce the Chinese, got %q", viaHint2)
	}
}

// TestDecodeHTMLBytes_UnsolvableHintReportsMetaName pins the **error text contract**:
// when the hint is undecodable and the <meta> declaration fallen back to is also
// undecodable, the error must name **that <meta> declaration** (see
// ParseHTMLBytesToMarkdownWithCharset's doc: "report that declaration's name"),
// not the original hint that was fallen back from.
//
// The gap was measured: this branch had zero coverage; changing :98 to
// fmt.Errorf("unsupported charset: %s", charsetHint) (original hint instead of
// the resolved charset) kept the whole package green (X20). The consequence is
// callers debugging by error text get pointed at a charset the page never claimed.
func TestDecodeHTMLBytes_UnsolvableHintReportsMetaName(t *testing.T) {
	// Hint and meta both deliberately undecodable: neither koi8-r nor iso-8859-2 is in the extended set.
	data := []byte(`<meta charset="iso-8859-2"><p>hi</p>`)

	_, err := DecodeHTMLBytes(data, "koi8-r")
	if err == nil {
		t.Fatal("hint and meta both undecodable must error, not return silently (this is the only observable signal)")
	}
	if !strings.Contains(err.Error(), "iso-8859-2") {
		t.Errorf("error must name the <meta> declaration, got %q", err)
	}
	// Negative assertion: the original hint was fallen back from and must not appear in the error text.
	// The positive assertion alone only proves "iso-8859-2 is present"; concatenating hint and charset would also pass.
	if strings.Contains(err.Error(), "koi8-r") {
		t.Errorf("error must not contain the original hint that was fallen back from, got %q", err)
	}
}

// <meta charset="utf-8"> is a valid declaration: even though
// getCharsetDecoder("utf-8") == nil (utf-8 bypasses decoders, returned as-is by
// DecodeHTMLBytes's special case), it must count as "a usable declaration was detected".
//
// The gap was measured: tightening the fallback condition to "meta non-empty
// **and** getCharsetDecoder(meta) != nil" would turn meta="utf-8" + undecodable
// hint from "return as-is" into an unsupported charset error, with no test to
// catch it. Such pages are not rare: header gives an undetectable charset, page declares utf-8.
func TestDecodeHTMLBytes_UnsupportedHintFallsBackToUTF8Meta(t *testing.T) {
	data := []byte(`<meta charset="utf-8"><p>hi</p>`)

	got, err := DecodeHTMLBytes(data, "koi8-r")
	if err != nil {
		t.Fatalf("meta=utf-8 is a valid declaration; an undecodable hint should return as-is, not error, got %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("meta=utf-8 → return as-is, got %q", got)
	}
}

// TestParseHTMLBytesToMarkdownWithCharset_DecodeFailureFallsBackToRaw pins the
// decode-failure degradation path: when DecodeHTMLBytes errors, the page must
// **not** fail; log a Warn and continue parsing with the raw bytes.
//
// This path predated this task, but the task added a new trigger (undecodable
// hint + no meta), making it easier to reach; it had zero coverage — changing
// it to return the error directly was uncatchable.
func TestParseHTMLBytesToMarkdownWithCharset_DecodeFailureFallsBackToRaw(t *testing.T) {
	// GBK bytes, no meta, undecodable hint (koi8-r, deliberately undecodable) → DecodeHTMLBytes must error.
	// The skeleton carries an ASCII anchor MARKER: the assertions below must see
	// it, which is what actually pins "after degradation the **raw bytes** continue to be parsed".
	raw := append([]byte(`<html><body><p>MARKER</p>`), 0xD6, 0xD0, 0xCE, 0xC4)
	raw = append(raw, []byte(`</body></html>`)...)

	md, pages, err := ParseHTMLBytesToMarkdownWithCharset(context.Background(), raw, "x.html", "koi8-r")
	if err != nil {
		t.Fatalf("decode failure should degrade to parsing the raw bytes, not return an error, got %v", err)
	}
	if pages != 1 {
		t.Errorf("pages = %d, want 1", pages)
	}
	// Assert MARKER appears, **not** merely non-empty: with only len(md) > 0,
	// replacing :57's decoded = htmlBytes with decoded = []byte("<p>x</p>")
	// (raw bytes fully discarded) still passes — whether "non-empty" could kill
	// decoded=nil was only a side effect of "md happens to be empty", not the
	// contract this test's comment claims (verified in review, X23).
	if !strings.Contains(md, "MARKER") {
		t.Errorf("the degradation path must keep parsing the raw bytes: the raw-byte anchor MARKER is missing from the output, got %q"+
			" (equivalent to decoded=nil, or decoded replaced with other bytes)", md)
	}
	// Mojibake bytes are still not content-compared: freezing them as expected
	// values would false-alarm on harmless implementation changes (e.g. a new
	// page-count formula). "The raw bytes really entered the DOM" is the MARKER
	// anchor's job; that is how this test's comment is honored.
	if len(md) == 0 {
		t.Error("the degradation path must keep producing content from the raw bytes, got empty markdown (equivalent to decoded=nil)")
	}
}

// TestGetCharsetDecoder_UnsolvableLabels pins the charset families **deliberately not added**.
//
// Single-byte Western/Cyrillic encodings decode any bytes; once in the table,
// the "undecodable" signal disappears and the <meta>-fallback branch (and the
// "meta also undecodable → error" observability) would never be reached again.
// The user only signed off on the latin-1/1252 group; all other single-byte
// encodings must stay undecodable.
//
// "utf-8"/"utf8" are included: utf-8 means "return as-is", special-cased in
// DecodeHTMLBytes; it should not and cannot be expressed by getCharsetDecoder returning a decoder.
func TestGetCharsetDecoder_UnsolvableLabels(t *testing.T) {
	for _, label := range []string{
		"koi8-r", "koi8-u", "iso-8859-2", "iso-8859-5", "iso-8859-15",
		"windows-1251", "cp1251", "macintosh", "", "utf-8", "utf8",
	} {
		if dec := getCharsetDecoder(label); dec != nil {
			t.Errorf("getCharsetDecoder(%q) must be nil (not in the extended set)", label)
		}
	}
}

// TestIsUTF8Charset_RejectsNonUTF8Labels pins isUTF8Charset's **exclusion side**:
// labels outside the whitelist must all return false.
//
// The gap was measured: adding "gb2312" (X26) or "big5" (X27) to the case list
// at :112 kept the extension commit's 280 new test lines **all green** — no
// assertion ever checked "non-UTF-8 labels must return false", so the tests had
// zero detection power for that line's reason to exist.
// The consequence is serious: pages of those two families would silently go
// from "correctly decoded as gbk/big5" to "raw mojibake bytes returned, no
// error, no warning" (isUTF8Charset true short-circuits to the original bytes).
//
// The whitelist **inside** (utf-8 plus the utf8 / unicode-1-1-utf-8 aliases) is covered by TestDecodeHTMLBytes_UTF8Aliases.
func TestIsUTF8Charset_RejectsNonUTF8Labels(t *testing.T) {
	for _, l := range []string{"gbk", "gb2312", "gb18030", "big5", "shift_jis", "euc-jp",
		"iso-2022-jp", "euc-kr", "iso-8859-1", "koi8-r", ""} {
		if isUTF8Charset(l) {
			t.Errorf("isUTF8Charset(%q) must be false", l)
		}
	}
}

// TestDecodeHTMLBytes_ExtendedCharsetsDecode covers the decode **target** of every
// extended label: fixtures use real encoded bytes, asserting the original text
// decodes. Wiring the wrong family (e.g. shift_jis to EUCJP) necessarily fails
// to produce the original text, and the assertion fails.
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
				t.Errorf("hint %q must decode to %q, got %q", c.label, c.text, got)
			}
		})
	}
}

// TestDecodeHTMLBytes_WesternLatin1IsWindows1252 pins the "Western always maps to windows-1252" decision.
//
// Per the WHATWG Encoding Standard (and browser behavior), the iso-8859-1 label
// in HTTP/HTML should actually decode as windows-1252: otherwise curly quotes in
// 0x80–0x9F become C1 control codes (U+0093) instead of “ (U+201C).
// Miswiring it to charmap.ISO8859_1 keeps bytes like 0xE9 (é) correct — **only**
// 0x80–0x9F distinguishes — so the fixture must contain curly quotes; that is
// exactly what this assertion separates the two decoders by.
func TestDecodeHTMLBytes_WesternLatin1IsWindows1252(t *testing.T) {
	// windows-1252 bytes: caf<0xE9> <0x93>r<0xE9>sum<0xE9><0x94> = café “résumé”
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
			t.Errorf("hint %q must decode as windows-1252 into %q, got %q", hint, want, got)
		}
		if strings.ContainsRune(string(got), '\u0093') {
			t.Errorf("hint %q must not decode as ISO-8859-1 (0x93 becomes C1 control U+0093), got %q", hint, got)
		}
	}
}

// TestDecodeHTMLBytes_WesternLabelOnUTF8BytesIsMojibake explicitly pins a
// **known cost**, not a bug.
//
// Adding iso-8859-1/windows-1252 to the table closed /v1/pdfparse's only
// recovery path "decode error → continue with raw bytes": single-byte encodings
// decode any bytes, so they no longer error. Pages with "<meta> declaring an old
// Western charset but bytes actually UTF-8" thus go from previously lucky
// correct text to silent mojibake (no error, no warn). The user weighed this
// and chose to keep it (the gain is real old-Western-charset pages decoding).
//
// If someone makes this test fail, that is not finding a bug but **deliberately
// changing the trade-off** — re-read design §8.11 first.
func TestDecodeHTMLBytes_WesternLabelOnUTF8BytesIsMojibake(t *testing.T) {
	// Bytes are UTF-8 Chinese, but meta declares iso-8859-1.
	utf8Bytes := []byte(`<meta charset="iso-8859-1"><p>中文内容</p>`)

	// All three hints must be covered: "" takes the <meta> probing path, the two
	// explicit Western labels take the hint path — two different code paths, both must be pinned.
	for _, hint := range []string{"", "iso-8859-1", "windows-1252"} {
		got, err := DecodeHTMLBytes(utf8Bytes, hint)
		if err != nil {
			t.Fatalf("hint %q: single-byte encodings must not error, got %v", hint, err)
		}
		// A negative assertion is **appropriate** here: single-byte decoding
		// always succeeds and always yields non-Chinese, so there is no
		// "wrong decoder also passes" no-op risk.
		if strings.Contains(string(got), "中文内容") {
			t.Errorf("hint %q: this fixture should decode to mojibake (the signed-off cost); "+
				"if this fails, the trade-off was deliberately changed — re-read design §8.11 first", hint)
		}
	}
}

// TestDecodeHTMLBytes_UTF8Aliases covers UTF-8's hyphen-less aliases.
//
// The gap was measured: DecodeHTMLBytes only special-cased "utf-8", while
// charset=utf8 is common in HTTP headers. Without the aliases, "utf8" would be
// judged "undecodable" and fall back to <meta>, so a page declaring utf-8 gets
// misdecoded per meta's other charset — this fixture has meta lying with gbk,
// turning exactly that fallback into mojibake.
func TestDecodeHTMLBytes_UTF8Aliases(t *testing.T) {
	// Bytes are UTF-8 Chinese, meta lies with gbk.
	data := []byte(`<meta charset="gbk"><p>中文内容</p>`)

	for _, hint := range []string{
		"utf8", "UTF8", "Utf8", " utf8 ", "unicode-1-1-utf-8", "UNICODE-1-1-UTF-8",
	} {
		got, err := DecodeHTMLBytes(data, hint)
		if err != nil {
			t.Fatalf("hint %q: %v", hint, err)
		}
		if !bytes.Equal(got, data) {
			t.Errorf("hint %q should be treated as utf-8 (returned as-is, meta ignored), got %q", hint, got)
		}
	}

	// Control: without alias semantics the loop above would fall back to meta's
	// gbk and produce mojibake — proving the fixture distinguishes "utf-8 alias"
	// from "undecodable → fall back to meta".
	viaMeta, err := DecodeHTMLBytes(data, "")
	if err != nil {
		t.Fatalf("no-hint decode error: %v", err)
	}
	if bytes.Equal(viaMeta, data) {
		t.Error("fixture broken: returned as-is even without a hint; cannot distinguish utf-8 aliases from the meta fallback")
	}
}

// TestParseHTMLBytesToMarkdownWithCharset_HintIsUsedAsDecoder pins that the
// **wrapper entry forwards the hint**.
//
// The gap was measured: ParseHTMLBytesToMarkdownWithCharset is the production
// entry the Fetcher calls, but existing tests only fed "utf-8", "", and an
// undecodable hint — none of which distinguishes "forward the hint" from
// "discard the hint (replace charsetHint with \"\" at :49)"; that mutation was
// previously all-green across the package.
//
// The fixture deliberately has <meta> lie with big5: only genuinely using the
// decodable hint as the decode source produces the Chinese.
func TestParseHTMLBytesToMarkdownWithCharset_HintIsUsedAsDecoder(t *testing.T) {
	body := append([]byte(`<html><head><meta charset="big5"></head><body><p>`),
		encodeWith(t, "中文内容", simplifiedchinese.GBK.NewEncoder())...)
	body = append(body, []byte(`</p></body></html>`)...)

	md, _, err := ParseHTMLBytesToMarkdownWithCharset(context.Background(), body, "x.html", "gbk")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(md, "中文内容") {
		t.Errorf("the wrapper entry must forward the decodable hint to the decoder (hint=gbk), got %q", md)
	}

	// Control: discarding the hint decodes per meta's big5, so the Chinese necessarily fails to decode.
	mdNoHint, _, err := ParseHTMLBytesToMarkdownWithCharset(context.Background(), body, "x.html", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(mdNoHint, "中文内容") {
		t.Error("fixture broken: Chinese decoded even with the hint discarded; cannot tell whether the hint was forwarded")
	}
}

// TestDetectHTMLCharset_UppercaseMeta covers the regex's (?i): mixed-case
// attribute names and values must be recognized. Existing fixtures were all
// lowercase, so (?i) had zero coverage. The return value is **not**
// case-normalized (getCharsetDecoder does the ToLower), so assert as-is.
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

// TestDetectHTMLCharset_MultipleDeclarations covers pages with multiple <meta>
// declarations: detectHTMLCharset takes the **first** via FindSubmatch, matching
// the HTML spec (first declaration wins). Previously zero coverage — taking the
// last one was equally green.
func TestDetectHTMLCharset_MultipleDeclarations(t *testing.T) {
	utf8First := `<html><head><meta charset="utf-8"><meta charset="gbk"></head></html>`
	if got := detectHTMLCharset([]byte(utf8First)); !strings.EqualFold(got, "utf-8") {
		t.Errorf("multiple declarations must take the first (utf-8), got %q", got)
	}

	gbkFirst := `<html><head><meta charset="gbk"><meta charset="utf-8"></head></html>`
	if got := detectHTMLCharset([]byte(gbkFirst)); !strings.EqualFold(got, "gbk") {
		t.Errorf("multiple declarations must take the first (gbk), got %q", got)
	}
}

// TestDetectHTMLCharset_AttributeForms covers the various legal spellings of the
// charset declaration, especially the HTML5-recommended short form with
// **single quotes** <meta charset='gbk'>.
//
// The gap was measured (consistent with the parent commit): in the old regex
// (?i)charset[="\s]+([a-zA-Z0-9_-]+) the `'` before `charset='gbk'`'s value was
// not in the character class, so it didn't match → detectHTMLCharset returned ""
// → the whole page's GBK bytes went into markdown **silently** as-is (no error,
// no warn). And the /v1/pdfparse path has no downstream encoding fallback
// (internal/service/parser.go, internal/parse/mhtml_parser.go), so the result
// was silent mojibake.
func TestDetectHTMLCharset_AttributeForms(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"double quotes", `<meta charset="gbk">`, "gbk"},
		{"single quotes", `<meta charset='gbk'>`, "gbk"},
		{"no quotes", `<meta charset=gbk>`, "gbk"},
		{"whitespace around =", `<meta charset = "gbk">`, "gbk"},
		{"newline before =", "<meta charset\n=\t'gbk'>", "gbk"},
		{"http-equiv long form, double quotes", `<meta http-equiv="Content-Type" content="text/html; charset=gbk">`, "gbk"},
		{"http-equiv long form, single quotes", `<meta http-equiv='Content-Type' content='text/html; charset=gbk'>`, "gbk"},
		{"no declaration", `<html><body><p>no charset here</p></body></html>`, ""},
		// Negative: charset in non-attribute position must not be mis-captured.
		// This case exists to block the "add ' to the character class" fix — it
		// would capture `charset's`'s `'s` as the value "s", then trigger an
		// unsupported charset error downstream (or decode as "s").
		{"charset's in prose must not match", `<p>the charset's value is utf-8</p>`, ""},
		// "charset + a **decodable** encoding name" in prose must not count as a
		// declaration — the most damaging class for this fix: detecting "gb2312"
		// would really decode the whole page with it, silent mojibake. The
		// `charset's` case above only blocks the weak variant whose label
		// contains no legal encoding name.
		{"charset gb2312 in prose must not match", `<p>charset gb2312 is good</p>`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := detectHTMLCharset([]byte(c.in)); !strings.EqualFold(got, c.want) {
				t.Errorf("detectHTMLCharset(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestDetectHTMLCharset_WindowClamp pins detectHTMLCharset's clamp to scanning only the **first 4096 bytes**.
//
// The clamp blocks "charset=… deep in body prose being misread as a
// declaration" — e.g. a long article quoting `charset=gb2312`; scanning the
// whole page would treat prose as a declaration and decode the entire page with
// the wrong charset. Enlarging `len(head) > 4096` to `> 99999999` (removing the
// clamp) was previously all-green: no existing fixture has a declaration
// landing outside the window.
//
// Padding is generated with strings.Repeat, not a 4KB literal in source.
func TestDetectHTMLCharset_WindowClamp(t *testing.T) {
	const pad = "<!-- pad -->" // 12 bytes

	// Declaration lands beyond 4096 bytes → must not be detected.
	outside := `<html><head>` + strings.Repeat(pad, 400) +
		`<meta charset="gbk"></head><body></body></html>`
	if len(outside) <= 4096 {
		t.Fatalf("fixture broken: only %d bytes total; the declaration did not land outside the 4096 window", len(outside))
	}
	if got := detectHTMLCharset([]byte(outside)); got != "" {
		t.Errorf("<meta charset> outside the window must not be read, got %q", got)
	}

	// Control: the same declaration **inside** the window must be read. Without
	// this, the check above would also pass under "fixture itself broken / regex
	// matches nothing" implementations — a no-op.
	inside := `<html><head>` + strings.Repeat(pad, 10) +
		`<meta charset="gbk"></head><body></body></html>`
	if got := detectHTMLCharset([]byte(inside)); !strings.EqualFold(got, "gbk") {
		t.Errorf("the same declaration inside the window must be read (control), got %q", got)
	}
}

// TestDecodeHTMLBytes_SingleQuotedMetaCharset pins single-quoted <meta charset='gbk'>
// end to end: real GBK bytes must decode to Chinese, not silently return as-is (mojibake).
//
// The fixture deliberately uses **real GBK bytes**, not ASCII: ASCII is
// transparent under GBK, so under both "wrong decode" and "no decode at all"
// implementations an ASCII fixture passes and the assertion degenerates into a no-op.
func TestDecodeHTMLBytes_SingleQuotedMetaCharset(t *testing.T) {
	body := append([]byte(`<html><head><meta charset='gbk'></head><body><p>`),
		encodeWith(t, "中文内容", simplifiedchinese.GBK.NewEncoder())...)
	body = append(body, []byte(`</p></body></html>`)...)

	got, err := DecodeHTMLBytes(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(got), "中文内容") {
		t.Errorf("single-quoted <meta charset='gbk'> must be detected and decoded as GBK, got %q", got)
	}
}
