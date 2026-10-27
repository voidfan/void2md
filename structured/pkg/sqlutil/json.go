package sqlutil

import (
	"bytes"
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
)

// PyJSONFloat replicates pydantic-core (ryu) float serialization as seen in
// model_dump_json(). Measured rules: integral floats get ".0"; decimal notation for
// exponent in [-5, 16), scientific otherwise (exponent not zero-padded, "+" kept for
// positive); NaN/Inf -> null. Diverges from encoding/json: Go writes "1" not "1.0", full
// decimals for large numbers, and errors on NaN/Inf.
func PyJSONFloat(f float64) string {
	// Non-finite values serialize as null (orjson matches pydantic here).
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}

	// Shortest round-trippable scientific form gives the decimal exponent.
	sci := strconv.FormatFloat(f, 'e', -1, 64) // e.g. "1e+16" / "1.5e-07"
	mant, expPart, _ := strings.Cut(sci, "e")
	exp, err := strconv.Atoi(expPart)
	if err != nil {
		return sci
	}

	// Decimal notation when exponent in [-5, 16); scientific otherwise.
	if exp >= -5 && exp < 16 {
		s := strconv.FormatFloat(f, 'f', -1, 64)
		if !strings.ContainsAny(s, ".") {
			s += ".0"
		}
		return s
	}

	// Scientific: strip exponent zero-padding, keep sign (Python writes 1e-7, 1e+300).
	sign := "+"
	digits := expPart
	switch {
	case strings.HasPrefix(digits, "+"):
		digits = digits[1:]
	case strings.HasPrefix(digits, "-"):
		sign = "-"
		digits = digits[1:]
	}
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		digits = "0"
	}
	return mant + "e" + sign + digits
}

// MarshalPyJSON serializes JSON that may embed floats: floats go through PyJSONFloat,
// containers recurse, other types reuse encoding/json (Go can't override float formatting
// via the Encoder).
func MarshalPyJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := writePyJSON(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writePyJSON(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case float64:
		buf.WriteString(PyJSONFloat(x))
		return nil
	case float32:
		buf.WriteString(PyJSONFloat(float64(x)))
		return nil
	case []any:
		buf.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writePyJSON(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil
	case map[string]any:
		// Go maps are unordered; sort keys for stable output (callers rarely contain objects).
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, err := MarshalJSON(k)
			if err != nil {
				return err
			}
			buf.Write(kb)
			buf.WriteByte(':')
			if err := writePyJSON(buf, x[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
		return nil
	default:
		b, err := MarshalJSON(v)
		if err != nil {
			return err
		}
		buf.Write(b)
		return nil
	}
}

// MarshalJSON serializes to compact JSON matching Python (orjson / model_dump_json):
// HTML escaping disabled and Encoder's trailing newline removed.
func MarshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// MarshalJSONIndent is equivalent to Python's json.dumps(obj, indent=n) /
// model_dump_json(indent=n); mainly for tests and inspection.
func MarshalJSONIndent(v any, indent string) ([]byte, error) {
	compact, err := MarshalJSON(v)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, compact, "", indent); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
