package sqlutil

import (
	"math"
	"testing"
)

// TestPyJSONFloat pins PyJSONFloat to pydantic-core's float serialization (values measured
// against pydantic). Covers divergences from encoding/json: "1.0" vs "1", scientific for
// large numbers, "null" for NaN/Inf.
func TestPyJSONFloat(t *testing.T) {
	tests := []struct {
		value float64
		want  string
	}{
		{1.0, "1.0"},
		{10.0, "10.0"},
		{100.0, "100.0"},
		// Go constants fold -0.0 to 0; build negative zero with Copysign.
		{math.Copysign(0, -1), "-0.0"},
		{45.67, "45.67"},
		{1.5, "1.5"},
		{0.1, "0.1"},
		{12345.6789, "12345.6789"},
		{1.0 / 3.0, "0.3333333333333333"},
		{123456789.0, "123456789.0"},
		// Decimal up to exponent 15; scientific from 16.
		{1e15, "1000000000000000.0"},
		{1e16, "1e+16"},
		{1e17, "1e+17"},
		{1e20, "1e+20"},
		{1e21, "1e+21"},
		{1e22, "1e+22"},
		{1e300, "1e+300"},
		// Decimal down to exponent -5; scientific from -6 (no zero padding, no + sign).
		{1e-5, "0.00001"},
		{1e-6, "1e-6"},
		{1e-7, "1e-7"},
		{0.0001, "0.0001"},
		{2.5e-10, "2.5e-10"},
		{5e-324, "5e-324"},
		{math.MaxFloat64, "1.7976931348623157e+308"},
		// Non-finite -> null (encoding/json would error).
		{math.NaN(), "null"},
		{math.Inf(1), "null"},
		{math.Inf(-1), "null"},
	}
	for _, tt := range tests {
		if got := PyJSONFloat(tt.value); got != tt.want {
			t.Errorf("PyJSONFloat(%v) = %q, want %q", tt.value, got, tt.want)
		}
	}
}

// TestMarshalPyJSON: float replacement in nested containers; non-floats via encoding/json.
func TestMarshalPyJSON(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"top-level float", 10.0, "10.0"},
		{"float array", []any{1, 2.5, 10.0, nil, "a"}, `[1,2.5,10.0,null,"a"]`},
		{"nested array", []any{[]any{1.0}, 2}, "[[1.0],2]"},
		{"integers keep integer literals", []any{int64(1), int64(10)}, "[1,10]"},
		{"non-finite values", []any{math.NaN(), math.Inf(1)}, "[null,null]"},
		{"nil", nil, "null"},
		{"strings do not escape HTML", "a<b>&c", `"a<b>&c"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MarshalPyJSON(tt.in)
			if err != nil {
				t.Fatalf("MarshalPyJSON(%#v) errored: %v", tt.in, err)
			}
			if string(got) != tt.want {
				t.Fatalf("MarshalPyJSON(%#v) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}
