// Ports Python tests/unit/core/utils.py against sqlutil/utils.go. Not ported: text2json
// (in jsonmin), parse_real_cols (in sqlg), and the thread-pool/async utilities
// (goroutines on the Go side). Assertions follow the Python tests' expected values.
package sqlutil

import (
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestGetBeijingTimeDefaultFormat corresponds to test_get_beijing_time_default_format.
func TestGetBeijingTimeDefaultFormat(t *testing.T) {
	result := GetBeijingTime("")
	if _, err := time.Parse("2006-01-02 15:04:05", result); err != nil {
		t.Fatalf("default format cannot be parsed: %v (result=%q)", err, result)
	}

	// Python: utc_now + 8h, formatted without a time zone.
	want := time.Now().UTC().Add(8 * time.Hour)
	got, err := time.Parse("2006-01-02 15:04:05", result)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if diff := got.Sub(want); diff > 2*time.Second || diff < -2*time.Second {
		t.Fatalf("Beijing time deviates too much: got=%v want~%v", got, want)
	}
}

// TestGetBeijingTimeCustomFormat corresponds to test_get_beijing_time_custom_format.
func TestGetBeijingTimeCustomFormat(t *testing.T) {
	result := GetBeijingTime("%Y/%m/%d")
	if _, err := time.Parse("2006/01/02", result); err != nil {
		t.Fatalf("custom format cannot be parsed: %v (result=%q)", err, result)
	}
}

// TestConvertValue2Str corresponds to test_convert_value2str_*. Python's Decimal("10.5")
// has no Go counterpart (drivers yield string/float64), so float64 covers it.
func TestConvertValue2Str(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{"string", "hello", "hello"},
		{"decimal/float", 10.5, "10.5"},
		{"none", nil, ""},
		{"int", 123, "123"},
		{"float", 45.67, "45.67"},
		{"bool true", true, "True"},
		{"bool false", false, "False"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ConvertValue2Str(tt.value); got != tt.want {
				t.Fatalf("ConvertValue2Str(%#v) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

// TestConvertValue2StrFloatRepr pins the float branch to Python str() character-for-character
// (values measured against the Python implementation). Key point: integral floats gain ".0".
// This output reaches LLM prompts via example values and to_markdown/to_m_schema.
func TestConvertValue2StrFloatRepr(t *testing.T) {
	tests := []struct {
		value float64
		want  string
	}{
		{10.0, "10.0"},
		{0.0, "0.0"},
		{math.Copysign(0, -1), "-0.0"},
		{45.67, "45.67"},
		{1e20, "1e+20"},
		{1e16, "1e+16"},
		{1e-5, "1e-05"},
		{2.5e-10, "2.5e-10"},
		{123456789012345678.0, "1.2345678901234568e+17"},
		{-3.0, "-3.0"},
	}
	for _, tt := range tests {
		if got := ConvertValue2Str(tt.value); got != tt.want {
			t.Fatalf("ConvertValue2Str(%v) = %q, want %q", tt.value, got, tt.want)
		}
		// float32 takes the same branch: promoted to float64 before repr.
		want32 := ConvertValue2Str(float64(float32(tt.value)))
		if got := ConvertValue2Str(float32(tt.value)); got != want32 {
			t.Fatalf("ConvertValue2Str(float32(%v)) = %q, want %q", tt.value, got, want32)
		}
	}
}

// TestIsValidUUID corresponds to test_is_valid_uuid.
func TestIsValidUUID(t *testing.T) {
	if !IsValidUUID("123e4567-e89b-12d3-a456-426614174000") {
		t.Fatal("a valid UUID should return true")
	}
	if IsValidUUID("invalid-uuid") {
		t.Fatal("an invalid UUID should return false")
	}
}

// TestIsNumber corresponds to test_is_number.
func TestIsNumber(t *testing.T) {
	tests := []struct {
		value any
		want  bool
	}{
		{"123", true},
		{"123.45", true},
		{123, true},
		{"not_a_number", false},
		{nil, false},
	}
	for _, tt := range tests {
		if got := IsNumber(tt.value); got != tt.want {
			t.Fatalf("IsNumber(%#v) = %v, want %v", tt.value, got, tt.want)
		}
	}
}

// TestIsDate corresponds to test_is_date.
func TestIsDate(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"2023-01-01", true},
		{"Jan 1, 2023", true},
		{"invalid-date", false},
	}
	for _, tt := range tests {
		if got := IsDate(tt.value); got != tt.want {
			t.Fatalf("IsDate(%q) = %v, want %v", tt.value, got, tt.want)
		}
	}
}

// TestIsEmail corresponds to test_is_email.
func TestIsEmail(t *testing.T) {
	if !IsEmail("test@example.com") {
		t.Fatal("a valid email should return true")
	}
	if IsEmail("invalid-email") {
		t.Fatal("an invalid email should return false")
	}
}

// TestTruncateText corresponds to test_truncate_text.
func TestTruncateText(t *testing.T) {
	text := "This is a long text for testing"

	// max_length=10 -> content[:7].rsplit(" ", 1)[0] + "..." == "This..."
	if got := TruncateText(text, 10, "..."); got != any("This...") {
		t.Fatalf("TruncateText(max_length=10) = %#v, want \"This...\"", got)
	}
	// Returned unchanged when short enough
	if got := TruncateText(text, 100, "..."); got != any(text) {
		t.Fatalf("TruncateText(max_length=100) = %#v, want the original string", got)
	}
	// max_length <= len(suffix) -> content[:max_length]
	if got := TruncateText(text, 3, "..."); got != any("Thi") {
		t.Fatalf("TruncateText(max_length=3) = %#v, want \"Thi\"", got)
	}
	// Non-strings are returned unchanged
	if got := TruncateText(123, 10, "..."); got != any(123) {
		t.Fatalf("TruncateText(123) = %#v, want 123", got)
	}
}

// TestTruncateTextByByte corresponds to test_truncate_text_by_byte; the Python cases only
// check "is str and byte count <= max_length", here concrete results are pinned (measured
// against the Python implementation). E.g. max_length=20 keeps only 5 of the 12 Chinese
// chars because 17/16-byte cuts land mid-character — Python and Go behave identically.
func TestTruncateTextByByte(t *testing.T) {
	text := "这是一个用于测试的长文本" // 12 characters = 36 bytes
	result := TruncateTextByByte(text, 20, "...", "utf-8")
	got, ok := result.(string)
	if !ok {
		t.Fatalf("return value should be a string, got %#v", result)
	}
	if len([]byte(got)) > 20 {
		t.Fatalf("byte length = %d, want <= 20", len([]byte(got)))
	}
	if got != "这是一个用..." {
		t.Fatalf("truncation result = %q, want %q", got, "这是一个用...")
	}

	// Matches Python's available_bytes decrement-and-fallback results verbatim.
	for _, tc := range []struct {
		maxLength int
		want      string
	}{
		{21, "这是一个用于..."},
		{17, "这是一个..."},
		{7, "这..."},
	} {
		if got := TruncateTextByByte(text, tc.maxLength, "...", "utf-8"); got != any(tc.want) {
			t.Fatalf("TruncateTextByByte(max_length=%d) = %#v, want %q", tc.maxLength, got, tc.want)
		}
	}

	englishText := "This is English text"
	result = TruncateTextByByte(englishText, 15, "...", "utf-8")
	got, ok = result.(string)
	if !ok {
		t.Fatalf("return value should be a string, got %#v", result)
	}
	if len([]byte(got)) > 15 {
		t.Fatalf("byte length = %d, want <= 15", len([]byte(got)))
	}
	if got != "This is Engl..." {
		t.Fatalf("truncation result = %q, want %q", got, "This is Engl...")
	}

	// max_length <= len(suffix): Python takes the encoded[:max_length] fallback branch.
	if got := TruncateTextByByte("abcdef", 2, "...", "utf-8"); got != any("ab") {
		t.Fatalf("TruncateTextByByte(max_length=2) = %#v, want \"ab\"", got)
	}
	// Non-strings are returned unchanged
	if got := TruncateTextByByte(123, 5, "...", "utf-8"); got != any(123) {
		t.Fatalf("TruncateTextByByte(123) = %#v, want 123", got)
	}
}

// TestCalculateMetrics corresponds to test_calculate_f1_normal_case and the zero-value cases.
func TestCalculateMetrics(t *testing.T) {
	precision, recall, f1 := CalculateMetrics(10, 5, 3)
	if precision != 10.0/15.0 {
		t.Fatalf("precision = %v, want %v", precision, 10.0/15.0)
	}
	if recall != 10.0/13.0 {
		t.Fatalf("recall = %v, want %v", recall, 10.0/13.0)
	}
	wantF1 := 2 * precision * recall / (precision + recall)
	if f1 != wantF1 {
		t.Fatalf("f1 = %v, want %v", f1, wantF1)
	}

	zeros := []struct {
		name       string
		tp, fp, fn float64
	}{
		{"all zeros", 0, 0, 0},
		{"precision is 0", 0, 5, 5},
		{"recall is 0", 0, 0, 5},
	}
	for _, tt := range zeros {
		t.Run(tt.name, func(t *testing.T) {
			p, r, f := CalculateMetrics(tt.tp, tt.fp, tt.fn)
			if p != 0 || r != 0 || f != 0 {
				t.Fatalf("CalculateMetrics(%v,%v,%v) = (%v,%v,%v), want all zeros", tt.tp, tt.fp, tt.fn, p, r, f)
			}
		})
	}
}

// TestParseLenientDate: lenient parsing behind is_date (Go-side export).
func TestParseLenientDate(t *testing.T) {
	if ParseLenientDate("2023-01-01") == nil {
		t.Fatal("2023-01-01 should parse")
	}
	if ParseLenientDate("") != nil {
		t.Fatal("empty string should not parse (dateutil would raise)")
	}
	if ParseLenientDate("invalid-date") != nil {
		t.Fatal("invalid-date should not parse")
	}
}

// TestStrftime: the strftime subset GetBeijingTime depends on.
func TestStrftime(t *testing.T) {
	ref := time.Date(2023, 1, 2, 15, 4, 5, 0, time.UTC)
	tests := []struct {
		format string
		want   string
	}{
		{"%Y-%m-%d %H:%M:%S", "2023-01-02 15:04:05"},
		{"%Y/%m/%d", "2023/01/02"},
		{"100%%", "100%"},
		{"%Q", "%Q"}, // unrecognized directives emitted verbatim
	}
	for _, tt := range tests {
		if got := Strftime(ref, tt.format); got != tt.want {
			t.Fatalf("Strftime(%q) = %q, want %q", tt.format, got, tt.want)
		}
	}
}

// TestPyTitle: replication of Python str.title.
func TestPyTitle(t *testing.T) {
	tests := []struct{ in, want string }{
		{"userName", "Username"},
		{"hello world", "Hello World"},
		{"HELLO_WORLD", "Hello_World"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := PyTitle(tt.in); got != tt.want {
			t.Fatalf("PyTitle(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestTruncateTextNonPositiveMaxLength: Python's max_length <= 0 branch (unchanged).
func TestTruncateTextNonPositiveMaxLength(t *testing.T) {
	text := "这是一个用于测试的长文本"
	if got := TruncateText(text, 0, "..."); got != any(text) {
		t.Fatalf("max_length=0 should return unchanged, got %#v", got)
	}
	if got := TruncateTextByByte(text, 0, "...", "utf-8"); got != any(text) {
		t.Fatalf("max_length=0 should return unchanged, got %#v", got)
	}
	// Multi-byte characters are never cut in half
	got := TruncateTextByByte(text, 7, "...", "utf-8").(string)
	if !utf8.ValidString(got) {
		t.Fatalf("truncation result is not valid UTF-8: %q", got)
	}
	if !strings.HasPrefix(text, strings.TrimSuffix(got, "...")) {
		t.Fatalf("truncation result is not a prefix of the original: %q", got)
	}
}
