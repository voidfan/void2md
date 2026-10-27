// Package sqlutil mirrors the Python-side structured/core/utils.py (the parts unrelated
// to SQL engines).
package sqlutil

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// GetBeijingTime corresponds to Python's get_beijing_time: UTC + 8h; default format
// "%Y-%m-%d %H:%M:%S".
func GetBeijingTime(format string) string {
	if format == "" {
		format = "%Y-%m-%d %H:%M:%S"
	}
	now := time.Now().UTC().Add(8 * time.Hour)
	return Strftime(now, format)
}

// Strftime implements the subset of Python strftime directives needed here.
func Strftime(t time.Time, format string) string {
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] != '%' || i+1 >= len(format) {
			b.WriteByte(format[i])
			continue
		}
		i++
		switch format[i] {
		case 'Y':
			b.WriteString(fmt.Sprintf("%04d", t.Year()))
		case 'y':
			b.WriteString(fmt.Sprintf("%02d", t.Year()%100))
		case 'm':
			b.WriteString(fmt.Sprintf("%02d", int(t.Month())))
		case 'd':
			b.WriteString(fmt.Sprintf("%02d", t.Day()))
		case 'H':
			b.WriteString(fmt.Sprintf("%02d", t.Hour()))
		case 'I':
			h := t.Hour() % 12
			if h == 0 {
				h = 12
			}
			b.WriteString(fmt.Sprintf("%02d", h))
		case 'M':
			b.WriteString(fmt.Sprintf("%02d", t.Minute()))
		case 'S':
			b.WriteString(fmt.Sprintf("%02d", t.Second()))
		case 'f':
			b.WriteString(fmt.Sprintf("%06d", t.Nanosecond()/1000))
		case 'j':
			b.WriteString(fmt.Sprintf("%03d", t.YearDay()))
		case 'p':
			if t.Hour() < 12 {
				b.WriteString("AM")
			} else {
				b.WriteString("PM")
			}
		case 'a':
			b.WriteString(t.Format("Mon"))
		case 'A':
			b.WriteString(t.Weekday().String())
		case 'b':
			b.WriteString(t.Format("Jan"))
		case 'B':
			b.WriteString(t.Month().String())
		case 'Z':
			b.WriteString(t.Format("MST"))
		case 'z':
			b.WriteString(t.Format("-0700"))
		case '%':
			b.WriteByte('%')
		default:
			b.WriteByte('%')
			b.WriteByte(format[i])
		}
	}
	return b.String()
}

// ConvertValue2Str corresponds to Python's convert_value2str (branch order str -> Decimal
// -> None -> str()).
func ConvertValue2Str(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		// Python str(True) == "True"
		if v {
			return "True"
		}
		return "False"
	case []byte:
		return string(v)
	case float64:
		return pyFloatRepr(v)
	case float32:
		return pyFloatRepr(float64(v))
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case time.Time:
		return pyDatetimeRepr(v)
	default:
		if s, ok := value.(fmt.Stringer); ok {
			return s.String()
		}
		return fmt.Sprintf("%v", value)
	}
}

// pyFloatRepr matches Python float repr (shortest round-trippable). Integral floats must
// gain ".0" (str(10.0) == "10.0"); 1e16 is where Python switches to exponential, so ".0"
// only applies when |f| < 1e16. Must stay consistent with pyrepr.reprFloat.
func pyFloatRepr(f float64) string {
	if math.IsInf(f, 1) {
		return "inf"
	}
	if math.IsInf(f, -1) {
		return "-inf"
	}
	if math.IsNaN(f) {
		return "nan"
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e16 {
		return strconv.FormatFloat(f, 'f', -1, 64) + ".0"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// pyDatetimeRepr matches the output of Python's str(datetime): "2023-01-01 10:00:00".
func pyDatetimeRepr(t time.Time) string {
	base := t.Format("2006-01-02 15:04:05")
	if t.Nanosecond() == 0 {
		return base
	}
	frac := strings.TrimRight(fmt.Sprintf("%06d", t.Nanosecond()/1000), "0")
	return base + "." + frac
}

var emailRe = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`)

// IsEmail corresponds to Python's is_email (re.match: anchored at start only).
func IsEmail(value string) bool {
	return emailRe.MatchString(value)
}

// IsNumber corresponds to Python's is_number: whether float(value) succeeds.
func IsNumber(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return true
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	case float32, float64:
		return true
	case string:
		return isPyFloatString(v)
	default:
		return false
	}
}

// isPyFloatString replicates Python float(str)'s accepted set (whitespace, signs,
// inf/nan, underscores).
func isPyFloatString(s string) bool {
	t := strings.TrimSpace(s)
	t = strings.ReplaceAll(t, "_", "")
	if t == "" {
		return false
	}
	lower := strings.ToLower(t)
	switch lower {
	case "inf", "+inf", "-inf", "infinity", "+infinity", "-infinity", "nan", "+nan", "-nan":
		return true
	}
	_, err := strconv.ParseFloat(t, 64)
	return err == nil
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsValidUUID corresponds to Python's is_valid_uuid (uuid.UUID(s) succeeds): also accepts
// the urn:uuid: prefix, brace wrapping, and 32 hyphen-less hex digits.
func IsValidUUID(s string) bool {
	t := s
	if strings.HasPrefix(strings.ToLower(t), "urn:uuid:") {
		t = t[len("urn:uuid:"):]
	}
	t = strings.TrimPrefix(t, "{")
	t = strings.TrimSuffix(t, "}")
	t = strings.ReplaceAll(t, "-", "")
	if len(t) != 32 {
		return false
	}
	for _, r := range t {
		if !isHexDigit(r) {
			return false
		}
	}
	return true
}

func isHexDigit(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

// IsDate corresponds to Python's is_date (true when dateutil.parser.parse succeeds).
func IsDate(value any) bool {
	s, ok := value.(string)
	if !ok {
		return false
	}
	return ParseLenientDate(s) != nil
}

// dateLayouts covers the shapes dateutil parses in practice.
var dateLayouts = []string{
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04",
	"2006-01-02T15:04",
	"2006-01-02",
	"2006/01/02 15:04:05",
	"2006/01/02",
	"01/02/2006 15:04:05",
	"01/02/2006",
	"02/01/2006",
	"20060102",
	"20060102150405",
	"Jan 2, 2006",
	"Jan 2 2006",
	"2 Jan 2006",
	"January 2, 2006",
	"2006-01-02 15:04:05 -0700",
	"2006-01-02T15:04:05Z07:00",
	"15:04:05",
	"2006-01",
	"2006",
}

// ParseLenientDate tries dateLayouts in order; nil when unparseable (dateutil would raise).
func ParseLenientDate(s string) *time.Time {
	t := strings.TrimSpace(s)
	if t == "" {
		return nil
	}
	for _, layout := range dateLayouts {
		if parsed, err := time.Parse(layout, t); err == nil {
			return &parsed
		}
	}
	return nil
}

// TruncateText corresponds to Python's truncate_text; non-strings or maxLength<=0 are
// returned unchanged (hence the any return).
func TruncateText(content any, maxLength int, suffix string) any {
	s, ok := content.(string)
	if !ok || maxLength <= 0 {
		return content
	}
	if utf8.RuneCountInString(s) <= maxLength {
		return content
	}
	if maxLength <= len([]rune(suffix)) {
		return string([]rune(s)[:maxLength])
	}
	head := string([]rune(s)[:maxLength-len([]rune(suffix))])
	// Mirrors Python's str.rsplit(" ", 1)[0].
	if idx := strings.LastIndex(head, " "); idx >= 0 {
		head = head[:idx]
	}
	return head + suffix
}

// TruncateTextByByte corresponds to Python's truncate_text_by_byte (UTF-8-safe truncation).
func TruncateTextByByte(content any, maxLength int, suffix string, encoding string) any {
	s, ok := content.(string)
	if !ok || maxLength <= 0 {
		return content
	}
	encoded := []byte(s)
	if len(encoded) <= maxLength {
		return content
	}
	suffixBytes := []byte(suffix)
	available := maxLength - len(suffixBytes)
	if available <= 0 {
		for i := maxLength; i > 0; i-- {
			if utf8.Valid(encoded[:i]) {
				return string(encoded[:i])
			}
		}
		return ""
	}
	for i := available; i > 0; i-- {
		if utf8.Valid(encoded[:i]) {
			return string(encoded[:i]) + suffix
		}
	}
	head := suffixBytes
	if len(head) > maxLength {
		head = head[:maxLength]
	}
	return string(head)
}

// CalculateMetrics corresponds to Python's calculate_metrics.
func CalculateMetrics(tp, fp, fn float64) (float64, float64, float64) {
	precision := 0.0
	if tp+fp > 0 {
		precision = tp / (tp + fp)
	}
	recall := 0.0
	if tp+fn > 0 {
		recall = tp / (tp + fn)
	}
	f1 := 0.0
	if precision+recall > 0 {
		f1 = 2 * precision * recall / (precision + recall)
	}
	return precision, recall, f1
}

// PyTitle replicates Python str.title(): first cased char after each non-cased char is
// uppercased, rest lowercased ("userName".title() == "Username"; differs from Go's
// deprecated strings.Title).
func PyTitle(s string) string {
	var b strings.Builder
	prevCased := false
	for _, r := range s {
		if isCased(r) {
			if prevCased {
				b.WriteRune(unicode.ToLower(r))
			} else {
				b.WriteRune(unicode.ToUpper(r))
			}
			prevCased = true
		} else {
			b.WriteRune(r)
			prevCased = false
		}
	}
	return b.String()
}

// isCased reports whether a rune is case-bearing (Python's "cased").
func isCased(r rune) bool {
	return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r)
}
