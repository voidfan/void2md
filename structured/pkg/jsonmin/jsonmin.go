// Package jsonmin replicates Python json_repair's fault-tolerant parsing (structured-side
// text2json behavior): repairs fences, unquoted keys, missing closing brackets, trailing
// garbage; returns "" (not an error) when wholly unparseable.
package jsonmin

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Loads corresponds to json_repair.loads; returns "" when unparseable (failure signal).
func Loads(text string) any {
	p := &parser{src: stripFences(text)}
	p.skipSpace()
	if p.eof() {
		return ""
	}
	v, ok := p.parseValue()
	if !ok {
		return ""
	}
	return v
}

// Text2JSON corresponds to Python's text2json: objects only, else empty map.
func Text2JSON(text string) map[string]any {
	v := Loads(text)
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// stripFences removes markdown code fences; json_repair does this step first.
func stripFences(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "```") {
		return t
	}
	// Remove opening ``` plus optional language tag
	rest := t[3:]
	if idx := strings.IndexByte(rest, '\n'); idx >= 0 {
		line := strings.TrimSpace(rest[:idx])
		if line == "" || isLangTag(line) {
			rest = rest[idx+1:]
		}
	}
	// Remove the closing fence
	if idx := strings.LastIndex(rest, "```"); idx >= 0 {
		rest = rest[:idx]
	}
	return strings.TrimSpace(rest)
}

func isLangTag(s string) bool {
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return len(s) > 0
}

type parser struct {
	src string
	pos int
}

func (p *parser) eof() bool { return p.pos >= len(p.src) }

func (p *parser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.src[p.pos]
}

// skipSpace skips whitespace and // , /* */ comments (json_repair strips comments).
func (p *parser) skipSpace() {
	for !p.eof() {
		c := p.src[p.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			p.pos++
		case c == '/' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '/':
			for !p.eof() && p.src[p.pos] != '\n' {
				p.pos++
			}
		case c == '/' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '*':
			p.pos += 2
			for p.pos+1 < len(p.src) && !(p.src[p.pos] == '*' && p.src[p.pos+1] == '/') {
				p.pos++
			}
			if p.pos+1 < len(p.src) {
				p.pos += 2
			} else {
				p.pos = len(p.src)
			}
		default:
			return
		}
	}
}

// parseValue parses one value; only the first top-level value is taken, trailing content
// is ignored (json_repair behavior).
func (p *parser) parseValue() (any, bool) {
	p.skipSpace()
	if p.eof() {
		return nil, false
	}
	switch c := p.peek(); {
	case c == '{':
		return p.parseObject()
	case c == '[':
		return p.parseArray()
	case c == '"' || c == '\'':
		s, ok := p.parseString()
		if !ok {
			return nil, false
		}
		return s, true
	case c == '-' || c == '+' || (c >= '0' && c <= '9') || c == '.':
		return p.parseNumber()
	default:
		return p.parseLiteral()
	}
}

func (p *parser) parseObject() (any, bool) {
	p.pos++ // '{'
	out := map[string]any{}
	for {
		p.skipSpace()
		if p.eof() {
			// Missing closing brace: json_repair completes it
			return out, true
		}
		if p.peek() == '}' {
			p.pos++
			return out, true
		}
		if p.peek() == ',' {
			p.pos++
			continue
		}
		key, ok := p.parseKey()
		if !ok {
			return nil, false
		}
		p.skipSpace()
		if p.peek() != ':' {
			// No colon: key-value treated as missing; skip the key
			return nil, false
		}
		p.pos++ // ':'
		val, ok := p.parseValue()
		if !ok {
			return nil, false
		}
		out[key] = val
	}
}

func (p *parser) parseArray() (any, bool) {
	p.pos++ // '['
	out := []any{}
	for {
		p.skipSpace()
		if p.eof() {
			return out, true
		}
		if p.peek() == ']' {
			p.pos++
			return out, true
		}
		if p.peek() == ',' {
			p.pos++
			continue
		}
		val, ok := p.parseValue()
		if !ok {
			return nil, false
		}
		out = append(out, val)
	}
}

// parseKey parses an object key: quoted or unquoted.
func (p *parser) parseKey() (string, bool) {
	p.skipSpace()
	if p.eof() {
		return "", false
	}
	if c := p.peek(); c == '"' || c == '\'' {
		return p.parseString()
	}
	start := p.pos
	for !p.eof() {
		c := p.src[p.pos]
		if c == ':' || c == ',' || c == '}' || c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			break
		}
		p.pos++
	}
	if p.pos == start {
		return "", false
	}
	return p.src[start:p.pos], true
}

func (p *parser) parseString() (string, bool) {
	quote := p.src[p.pos]
	p.pos++
	var b strings.Builder
	for !p.eof() {
		c := p.src[p.pos]
		if c == '\\' {
			p.pos++
			if p.eof() {
				break
			}
			esc := p.src[p.pos]
			p.pos++
			switch esc {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case '/':
				b.WriteByte('/')
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			case '\'':
				b.WriteByte('\'')
			case 'u':
				if p.pos+4 <= len(p.src) {
					if n, err := strconv.ParseUint(p.src[p.pos:p.pos+4], 16, 32); err == nil {
						b.WriteRune(rune(n))
						p.pos += 4
					}
				}
			default:
				// Unrecognized escapes keep the original character (json_repair)
				b.WriteByte(esc)
			}
			continue
		}
		if c == quote {
			p.pos++
			return b.String(), true
		}
		// Unclosed string: json_repair adds the closing quote when hitting a newline or EOF
		if c == '\n' {
			return b.String(), true
		}
		b.WriteByte(c)
		p.pos++
	}
	// EOF: add the closing quote
	return b.String(), true
}

func (p *parser) parseNumber() (any, bool) {
	start := p.pos
	if c := p.peek(); c == '-' || c == '+' {
		p.pos++
	}
	seenDigit := false
	for !p.eof() {
		c := p.src[p.pos]
		if (c >= '0' && c <= '9') || c == '.' || c == 'e' || c == 'E' ||
			((c == '-' || c == '+') && p.pos > start && (p.src[p.pos-1] == 'e' || p.src[p.pos-1] == 'E')) {
			if c >= '0' && c <= '9' {
				seenDigit = true
			}
			p.pos++
			continue
		}
		break
	}
	if !seenDigit {
		return nil, false
	}
	raw := p.src[start:p.pos]
	if !strings.ContainsAny(raw, ".eE") {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return n, true
		}
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil, false
	}
	return f, true
}

// parseLiteral parses true/false/null (plus Python None/NaN/Infinity variants).
func (p *parser) parseLiteral() (any, bool) {
	start := p.pos
	if !isValueStart(rune(p.peek())) {
		return nil, false
	}
	for !p.eof() {
		r, size := utf8.DecodeRuneInString(p.src[p.pos:])
		if !isValueRune(r) {
			break
		}
		p.pos += size
	}
	word := p.src[start:p.pos]
	switch strings.ToLower(word) {
	case "true":
		return true, true
	case "false":
		return false, true
	case "null", "none":
		return nil, true
	case "nan":
		return nil, true
	case "infinity", "inf":
		return nil, true
	}
	return nil, false
}

func isValueStart(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_'
}

func isValueRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_'
}
