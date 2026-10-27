package sqlutil

import (
	"fmt"
	"strings"
)

// PyFormat replicates the str.format subset structured uses: {{, }}, {name}. Missing keys
// error (Python KeyError); extra keys ignored. Positional/conversion/spec forms are
// unsupported and error rather than silently producing wrong text.
func PyFormat(template string, data map[string]string) (string, error) {
	var b strings.Builder
	b.Grow(len(template))

	for i := 0; i < len(template); {
		c := template[i]
		if c == '{' {
			if i+1 < len(template) && template[i+1] == '{' {
				b.WriteByte('{')
				i += 2
				continue
			}
			end := strings.IndexByte(template[i:], '}')
			if end < 0 {
				return "", fmt.Errorf("Single '{' encountered in format string")
			}
			key := template[i+1 : i+end]
			if key == "" || strings.ContainsAny(key, "!:{}") {
				return "", fmt.Errorf("unsupported format field %q", key)
			}
			val, ok := data[key]
			if !ok {
				return "", fmt.Errorf("'%s'", key)
			}
			b.WriteString(val)
			i += end + 1
			continue
		}
		if c == '}' {
			if i+1 < len(template) && template[i+1] == '}' {
				b.WriteByte('}')
				i += 2
				continue
			}
			return "", fmt.Errorf("Single '}' encountered in format string")
		}
		b.WriteByte(c)
		i++
	}
	return b.String(), nil
}

// PyTruthyString replicates Python's `value or ""` plus str() conversion: None / "" / 0 /
// 0.0 / False / empty containers yield "", everything else is str()'d.
func PyTruthyString(value any, exists bool) string {
	if !exists {
		return ""
	}
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		if v {
			return "True"
		}
		return ""
	case float64:
		if v == 0 {
			return ""
		}
		return ConvertValue2Str(v)
	case int:
		if v == 0 {
			return ""
		}
		return ConvertValue2Str(v)
	case int64:
		if v == 0 {
			return ""
		}
		return ConvertValue2Str(v)
	case []any:
		if len(v) == 0 {
			return ""
		}
		return ConvertValue2Str(v)
	case map[string]any:
		if len(v) == 0 {
			return ""
		}
		return ConvertValue2Str(v)
	default:
		return ConvertValue2Str(v)
	}
}
