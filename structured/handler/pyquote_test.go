package api

import (
	"net/url"
	"testing"
)

// All expected values were produced by real CPython 3.13 (urllib.parse.unquote /
// bytes.decode("utf-8","replace")); none derived by hand.

// TestPyUnquoteGolden: parity test against urllib.parse.unquote, focused on where the Go
// stdlib differs: "+" (not a space; QueryUnescape decodes it) and invalid escape segments
// (kept as-is per segment; PathUnescape fails whole-string).
func TestPyUnquoteGolden(t *testing.T) {
	cases := []struct{ in, want string }{
		{"report.xlsx", "report.xlsx"},
		{"报表+2024.xlsx", "报表+2024.xlsx"},
		{"a+b.csv", "a+b.csv"},
		{"a%2Bb.xlsx", "a+b.xlsx"},
		{"%E6%8A%A5%E8%A1%A8.xlsx", "报表.xlsx"},
		{"a%zz.xlsx", "a%zz.xlsx"},
		{"100%.xlsx", "100%.xlsx"},
		{"a%b.xlsx", "a%b.xlsx"},
		{"%41%42%43.xlsx", "ABC.xlsx"},
		{"a%2.xlsx", "a%2.xlsx"},
		{"trailing%", "trailing%"},
		{"%25.xlsx", "%.xlsx"},
		{"a+b%20c.xls", "a+b c.xls"},
		{"中文-.csv", "中文-.csv"},
		{"%E4%B8%AD%FF.xlsx", "中�.xlsx"},
		{"%ff%fe.xlsx", "��.xlsx"},
		{"a%20%20b.xlsx", "a  b.xlsx"},
		{"%2e.xlsx", "..xlsx"},
		{"a..xlsx", "a..xlsx"},
		{"", ""},
		{"%", "%"},
	}
	for _, c := range cases {
		if got := pyUnquote(c.in); got != c.want {
			t.Errorf("pyUnquote(%q) = %q, expected %q", c.in, got, c.want)
		}
	}
}

// TestPyUnquoteDiffersFromStdlib pins why the stdlib cannot be used, so pyUnquote is not
// swapped back to url.QueryUnescape / url.PathUnescape.
func TestPyUnquoteDiffersFromStdlib(t *testing.T) {
	// QueryUnescape decodes "+" as a space; Python does not.
	if got, _ := url.QueryUnescape("报表+2024.xlsx"); got == pyUnquote("报表+2024.xlsx") {
		t.Fatal("QueryUnescape and unquote should differ on \"+\"; if this assertion fails, the standard library's behavior has changed")
	}
	// PathUnescape fails whole-string on invalid escapes; Python keeps them per segment.
	if _, err := url.PathUnescape("a%zz.xlsx"); err == nil {
		t.Fatal("PathUnescape should fail on invalid escapes")
	}
	if got := pyUnquote("a%zz.xlsx"); got != "a%zz.xlsx" {
		t.Fatalf("pyUnquote should keep invalid escapes as-is, got %q", got)
	}
}

// TestDecodeUTF8ReplaceGolden: parity test against bytes.decode("utf-8", "replace").
// strings.ToValidUTF8 cannot be used: it collapses consecutive invalid bytes into one U+FFFD.
func TestDecodeUTF8ReplaceGolden(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{"two invalid lead bytes each replaced", []byte{0xff, 0xfe}, "��"},
		{"three-byte sequence truncated", []byte{0xe4, 0xb8}, "�"},
		{"single invalid lead byte", []byte{0xff}, "�"},
		{"valid CJK character followed by an invalid byte", []byte{0xe4, 0xb8, 0xad, 0xff}, "中�"},
		{"four-byte sequence truncated to two bytes", []byte{0xf0, 0x9f}, "�"},
		{"two-byte sequence truncated to one byte", []byte{0xc3}, "�"},
		{"lone continuation byte", []byte{0x80}, "�"},
		{"0xF5 and above invalid", []byte{0xf5}, "�"},
		{"second byte is not a continuation byte", []byte{0xe4, 0x41}, "�A"},
		{"four-byte sequence truncated to three bytes", []byte{0xf0, 0x9f, 0x98}, "�"},
		{"surrogate U+D800 replaced byte by byte", []byte{0xed, 0xa0, 0x80}, "���"},
		{"two-byte sequence truncated to one byte (2)", []byte{0xc2}, "�"},
		{"overlong encoding 0xE0 0x80", []byte{0xe0, 0x80}, "��"},
		{"overlong encoding 0xC0 0x80", []byte{0xc0, 0x80}, "��"},
		{"beyond U+10FFFF replaced byte by byte", []byte{0xf4, 0x90, 0x80, 0x80}, "����"},
		{"invalid third byte consumes only the valid prefix", []byte{0xe4, 0xb8, 0x41}, "�A"},
		// valid sequences must pass through unchanged
		{"valid U+0080", []byte{0xc2, 0x80}, ""},
		{"valid U+FFFF", []byte{0xef, 0xbf, 0xbf}, "￿"},
		{"valid three-byte CJK character", []byte{0xe4, 0xb8, 0xad}, "中"},
		{"pure ASCII", []byte("abc.xlsx"), "abc.xlsx"},
		{"empty input", []byte{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := decodeUTF8Replace(c.in); got != c.want {
				t.Errorf("decodeUTF8Replace(% x) = %q, expected %q", c.in, got, c.want)
			}
		})
	}
}

// TestDecodeUTF8ReplaceVsToValidUTF8 pins the difference from strings.ToValidUTF8:
// consecutive invalid bytes collapse differently.
func TestDecodeUTF8ReplaceVsToValidUTF8(t *testing.T) {
	in := "\xff\xfe"
	if got := decodeUTF8Replace([]byte(in)); got != "��" {
		t.Fatalf("decodeUTF8Replace(% x) = %q, expected two U+FFFD", []byte(in), got)
	}
}
