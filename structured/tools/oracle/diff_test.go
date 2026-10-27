// Package oracle provides byte-for-byte parity tests against the real Python implementation.
// The golden_*.json files are the sole authoritative target (now read-only); this package
// only compares against them and quantifies differences.
package oracle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"void2md/structured/pkg/sqlg"
)

type rtCase struct {
	Name              string `json:"name"`
	Dialect           string `json:"dialect"`
	SQL               string `json:"sql"`
	RoundtripDialect  string `json:"roundtrip_dialect"`
	RoundtripDialectE string `json:"roundtrip_dialect_error"`
	Pretty            string `json:"pretty"`
	PrettyError       string `json:"pretty_error"`
	RoundtripDefault  string `json:"roundtrip_default"`
	RoundtripDefaultE string `json:"roundtrip_default_error"`
}

func loadJSON[T any](t *testing.T, name string) []T {
	t.Helper()
	path := filepath.Join("golden", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read golden %s: %v", path, err)
	}
	var out []T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("failed to parse golden %s: %v", path, err)
	}
	return out
}

// TestRoundtripDialect parity-tests parse_one(sql, dialect).sql(dialect).
func TestRoundtripDialect(t *testing.T) {
	cases := loadJSON[rtCase](t, "golden_roundtrip.json")
	var pass, fail, skip int
	for _, c := range cases {
		if !sqlg.HasDialect(c.Dialect) {
			// dative's sql_format returns this dialect verbatim; no parsing needed
			skip++
			continue
		}
		want := c.RoundtripDialect
		wantErr := c.RoundtripDialectE != ""
		e, err := sqlg.ParseOne(c.SQL, c.Dialect)
		if wantErr {
			if err == nil {
				fail++
				t.Errorf("[%s] expected ParseError, but parsing succeeded -> %s", c.Name, sqlg.FormatExpr(e, c.Dialect))
			} else {
				pass++
			}
			continue
		}
		if err != nil {
			fail++
			t.Errorf("[%s] parse failed: %v\n  input: %s", c.Name, err, c.SQL)
			continue
		}
		got := sqlg.FormatExpr(e, c.Dialect)
		if got != want {
			fail++
			t.Errorf("[%s] compact rendering mismatch\n  input: %s\n  want: %s\n  got:  %s", c.Name, c.SQL, want, got)
			continue
		}
		pass++
	}
	t.Logf("roundtrip: pass=%d fail=%d skip=%d", pass, fail, skip)
}

// TestPretty parity-tests transpile(..., pretty=True, indent=4, pad=4).
func TestPretty(t *testing.T) {
	cases := loadJSON[rtCase](t, "golden_roundtrip.json")
	var pass, fail, skip int
	for _, c := range cases {
		if !sqlg.HasDialect(c.Dialect) {
			skip++
			continue
		}
		if c.PrettyError != "" {
			skip++ // the expected-error branch is covered by TestRoundtripDialect
			continue
		}
		got, ok, err := sqlg.FormatSQL(c.SQL, c.Dialect)
		if err != nil {
			fail++
			t.Errorf("[%s] FormatSQL returned an error: %v", c.Name, err)
			continue
		}
		if !ok {
			fail++
			t.Errorf("[%s] FormatSQL reported the dialect as unsupported: %s", c.Name, c.Dialect)
			continue
		}
		if got != c.Pretty {
			fail++
			t.Errorf("[%s] pretty mismatch\n  input: %s\n  want:\n%s\n  got:\n%s", c.Name, c.SQL, c.Pretty, got)
			continue
		}
		pass++
	}
	t.Logf("pretty: pass=%d fail=%d skip=%d", pass, fail, skip)
}

// TestRoundtripDefault parity-tests parse_one(sql).sql() (default dialect).
func TestRoundtripDefault(t *testing.T) {
	cases := loadJSON[rtCase](t, "golden_roundtrip.json")
	var pass, fail, skip int
	for _, c := range cases {
		if c.RoundtripDefaultE != "" || c.RoundtripDefault == "" {
			skip++
			continue
		}
		e, err := sqlg.ParseOne(c.SQL, "")
		if err != nil {
			fail++
			t.Errorf("[%s] default-dialect parse failed: %v\n  input: %s", c.Name, err, c.SQL)
			continue
		}
		got := sqlg.FormatExpr(e, "")
		if got != c.RoundtripDefault {
			fail++
			t.Errorf("[%s] default-dialect rendering mismatch\n  input: %s\n  want: %s\n  got:  %s", c.Name, c.SQL, c.RoundtripDefault, got)
			continue
		}
		pass++
	}
	t.Logf("default: pass=%d fail=%d skip=%d", pass, fail, skip)
}

// TestIsQuery parity-tests the dialect watershed behavior of dative's SQLCheck.is_query.
func TestIsQuery(t *testing.T) {
	cases := loadJSON[rtCase](t, "golden_roundtrip.json")
	var pass, fail int
	for _, c := range cases {
		if !sqlg.HasDialect(c.Dialect) {
			// non-sqlglot dialects fall back to startswith("SELECT")
			continue
		}
		e, err := sqlg.ParseOneOrDefault(c.SQL, c.Dialect)
		if err != nil {
			continue // dative raises an exception when parsing fails; handled separately
		}
		got := sqlg.IsQueryExpr(e)
		// Expected value for DML/DDL/empty statements: not a query
		want := true
		switch c.Name {
		case "insert_not_query", "update_not_query", "drop_not_query", "bad_syntax", "empty_sql":
			want = false
		}
		if got != want {
			fail++
			t.Errorf("[%s] is_query mismatch: want %v, got %v", c.Name, want, got)
			continue
		}
		pass++
	}
	t.Logf("is_query: pass=%d fail=%d", pass, fail)
}
