// Package pyrepr replicates Python's repr() for scenarios requiring character-for-character
// parity with Python output (e.g. SQL result sets embedded into prompts as
// str(list_of_tuples), or JSON bodies embedded in exception messages).
package pyrepr

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Repr returns the Python repr representation of value.
func Repr(value any) string {
	switch v := value.(type) {
	case nil:
		return "None"
	case bool:
		if v {
			return "True"
		}
		return "False"
	case string:
		return reprString(v)
	case []byte:
		return "b" + reprString(string(v))
	case int:
		return strconv.Itoa(v)
	case int8:
		return strconv.FormatInt(int64(v), 10)
	case int16:
		return strconv.FormatInt(int64(v), 10)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case uint:
		return strconv.FormatUint(uint64(v), 10)
	case uint8:
		return strconv.FormatUint(uint64(v), 10)
	case uint16:
		return strconv.FormatUint(uint64(v), 10)
	case uint32:
		return strconv.FormatUint(uint64(v), 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	case float32:
		return reprFloat(float64(v))
	case float64:
		return reprFloat(v)
	case []any:
		return reprList(v)
	case [][]any:
		// Row list degrades to a list of lists; Rows() renders list[tuple] precisely.
		items := make([]string, 0, len(v))
		for _, row := range v {
			items = append(items, reprList(row))
		}
		return "[" + strings.Join(items, ", ") + "]"
	case map[string]any:
		return reprMap(v)
	case fmt.Stringer:
		return reprString(v.String())
	default:
		return reprString(fmt.Sprintf("%v", value))
	}
}

// Rows replicates the Python repr of an SQL result set (list[tuple]).
func Rows(rows [][]any) string {
	items := make([]string, 0, len(rows))
	for _, row := range rows {
		items = append(items, reprTuple(row))
	}
	return "[" + strings.Join(items, ", ") + "]"
}

// reprTuple replicates Python tuple repr (single element keeps the trailing comma).
func reprTuple(items []any) string {
	if len(items) == 1 {
		return "(" + Repr(items[0]) + ",)"
	}
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, Repr(it))
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func reprList(items []any) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, Repr(it))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// reprMap renders a dict; keys are sorted for stable output (Python uses insertion order,
// Go maps are unordered).
func reprMap(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, reprString(k)+": "+Repr(m[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// reprFloat replicates Python float repr (shortest round-trippable).
func reprFloat(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case math.IsNaN(f):
		return "nan"
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e16 {
		return strconv.FormatFloat(f, 'f', -1, 64) + ".0"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// reprString replicates Python str repr: single quotes by default; double quotes when the
// string contains ' but no ".
func reprString(s string) string {
	quote := byte('\'')
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case rune(quote):
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			switch {
			case r < 0x20 || r == 0x7f:
				b.WriteString(fmt.Sprintf(`\x%02x`, r))
			case isPrintable(r):
				b.WriteRune(r)
			case r <= 0xff:
				b.WriteString(fmt.Sprintf(`\x%02x`, r))
			case r <= 0xffff:
				b.WriteString(fmt.Sprintf(`\u%04x`, r))
			default:
				b.WriteString(fmt.Sprintf(`\U%08x`, r))
			}
		}
	}
	b.WriteByte(quote)
	return b.String()
}

// isPrintable approximates str.isprintable(): excludes control, format, surrogate, and
// private-use categories.
func isPrintable(r rune) bool {
	if r == ' ' {
		return true
	}
	switch {
	case r < 0x20, r >= 0x7f && r <= 0xa0:
		return false
	case r >= 0xd800 && r <= 0xdfff:
		return false
	case r >= 0xe000 && r <= 0xf8ff:
		return false
	case r >= 0xfff9 && r <= 0xfffb:
		return false
	}
	return true
}
