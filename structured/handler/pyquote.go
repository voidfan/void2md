package api

import (
	"fmt"
	"strings"
)

// Replicates Python's urllib.parse.unquote/quote (excel_upload filenames, 307 Location headers).
// url.QueryUnescape (decodes "+") and url.PathUnescape (fails whole-string on invalid escapes)
// cannot be used. Expected values verified against real CPython 3.13; see pyquote_test.go.

// pyUnquote is urllib.parse.unquote(s) (encoding="utf-8", errors="replace", both defaults).
func pyUnquote(s string) string {
	if s == "" {
		return ""
	}
	// CPython: split on '%'; a single segment returns the original string.
	parts := strings.Split(s, "%")
	if len(parts) == 1 {
		return s
	}
	buf := make([]byte, 0, len(s))
	buf = append(buf, parts[0]...)
	for _, item := range parts[1:] {
		// Unescape only when the first two chars are valid hex; else keep '%' + segment as-is.
		if len(item) >= 2 {
			if b, ok := hexPairToByte(item[:2]); ok {
				buf = append(buf, b)
				buf = append(buf, item[2:]...)
				continue
			}
		}
		buf = append(buf, '%')
		buf = append(buf, item...)
	}
	return decodeUTF8Replace(buf)
}

// hexPairToByte is CPython's _hextobyte lookup: both chars must be hex digits (either case).
func hexPairToByte(s string) (byte, bool) {
	hi, ok := hexVal(s[0])
	if !ok {
		return 0, false
	}
	lo, ok := hexVal(s[1])
	if !ok {
		return 0, false
	}
	return hi<<4 | lo, true
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// replacementRune is the U+FFFD used by Python's errors="replace".
const replacementRune = "�"

// alwaysSafeBytes is CPython's _ALWAYS_SAFE: never percent-encoded.
const alwaysSafeBytes = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_.-~"

// pyQuote is urllib.parse.quote(s, safe=safe). Used for 307 Location headers, where starlette
// uses safe=":/%#?=@[]!$&'()*+,;=". Go's url.URL.String() encodes by different rules, so bytes
// are processed per CPython.
func pyQuote(s, safe string) string {
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(alwaysSafeBytes, c) >= 0 || strings.IndexByte(safe, c) >= 0 {
			sb.WriteByte(c)
			continue
		}
		fmt.Fprintf(&sb, "%%%02X", c)
	}
	return sb.String()
}

// decodeUTF8Replace is bytes.decode("utf-8", "replace") per CPython's "longest valid subpart"
// rule. strings.ToValidUTF8 cannot be used: it collapses consecutive invalid bytes into one
// U+FFFD (b"\xff\xfe" -> 2 U+FFFD in Python). Invalid lead bytes consume 1 byte; a valid lead
// with a broken sequence consumes the matched prefix; each emits one U+FFFD. Overlong forms,
// surrogates and code points beyond U+10FFFF are rejected via the second-byte bounds below.
func decodeUTF8Replace(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	replaced := false
	i := 0
	for i < len(b) {
		c := b[i]
		if c < 0x80 {
			sb.WriteByte(c)
			i++
			continue
		}
		var need int
		lo, hi := byte(0x80), byte(0xBF)
		switch {
		case c < 0xC2:
			// 0x80–0xBF are continuation bytes; 0xC0/0xC1 are overlong leads — both invalid.
			sb.WriteString(replacementRune)
			i++
			replaced = true
			continue
		case c < 0xE0:
			need = 2
		case c < 0xF0:
			need = 3
			if c == 0xE0 {
				lo = 0xA0 // exclude overlong encodings
			}
			if c == 0xED {
				hi = 0x9F // exclude U+D800–U+DFFF surrogates
			}
		case c < 0xF5:
			need = 4
			if c == 0xF0 {
				lo = 0x90 // exclude overlong encodings
			}
			if c == 0xF4 {
				hi = 0x8F // exclude code points beyond U+10FFFF
			}
		default:
			sb.WriteString(replacementRune)
			i++
			replaced = true
			continue
		}
		j := i + 1
		if j >= len(b) || b[j] < lo || b[j] > hi {
			sb.WriteString(replacementRune)
			i++
			replaced = true
			continue
		}
		j++
		for k := 2; k < need; k++ {
			if j >= len(b) || b[j] < 0x80 || b[j] > 0xBF {
				break
			}
			j++
		}
		if j == i+need {
			sb.Write(b[i:j])
			i = j
			continue
		}
		sb.WriteString(replacementRune)
		i = j
		replaced = true
	}
	if !replaced {
		return string(b)
	}
	return sb.String()
}
