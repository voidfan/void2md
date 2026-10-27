package oracle

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"void2md/structured/pkg/sqlg"
)

// optCase is a single optimizer parity case from golden_opt.json.
type optCase struct {
	Name    string          `json:"name"`
	Dialect string          `json:"dialect"`
	Major   int             `json:"major"`
	SQL     string          `json:"sql"`
	Limit   int             `json:"limit"`
	Schema  json.RawMessage `json:"schema"`

	IsQuery       *bool  `json:"is_query"`
	IsQueryError  string `json:"is_query_error"`
	SyntaxValid   *bool  `json:"syntax_valid"`
	SyntaxError   string `json:"syntax_error"`
	Optimized     string `json:"optimized"`
	OptimizeError string `json:"optimize_error"`
}

// parseOrderedSchema parses {table: {col: type}} preserving key order: column order
// determines the SELECT list order after expand_stars, and a map would lose it.
func parseOrderedSchema(raw json.RawMessage) (*sqlg.SchemaInput, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("schema top level is not an object: %s", tok)
	}
	in := &sqlg.SchemaInput{}
	for dec.More() {
		tableTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		table := tableTok.(string)
		st := &sqlg.SchemaTable{Parts: []string{table}}

		colTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if d, ok := colTok.(json.Delim); !ok || d != '{' {
			return nil, fmt.Errorf("column definitions for table %s are not an object", table)
		}
		for dec.More() {
			nameTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			typeTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			st.Cols = append(st.Cols, sqlg.SchemaCol{Name: nameTok.(string), Type: typeTok.(string)})
		}
		if _, err := dec.Token(); err != nil { // '}'
			return nil, err
		}
		in.Tables = append(in.Tables, st)
	}
	return in, nil
}

func loadOptCases(t *testing.T) []optCase {
	t.Helper()
	raw, err := os.ReadFile("golden_opt.json")
	if err != nil {
		t.Skipf("golden_opt.json is missing (the oracle scripts were deleted along with the Python implementation; goldens are now read-only): %v", err)
	}
	var cases []optCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("failed to parse golden_opt.json: %v", err)
	}
	return cases
}

// normErr truncates a Python exception string before the first " Line: ": sqlglot errors
// carry line/column metadata that sqlg lacks, so only the message body is compared.
func normErr(s string) string {
	if i := strings.Index(s, " Line: "); i >= 0 {
		return s[:i]
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// classifyErr normalizes an error string into "class name" + "message body".
func classifyErr(s string) (string, string) {
	if s == "" {
		return "", ""
	}
	kind := ""
	body := s
	if i := strings.Index(s, ": "); i >= 0 {
		kind = s[:i]
		body = s[i+2:]
	}
	kind = strings.TrimPrefix(kind, "<class '")
	kind = strings.TrimSuffix(kind, "'>")
	return kind, normErr(body)
}

type optStat struct {
	total    int
	match    int
	bothErr  int
	errKind  int
	diverged []string
}

// TestOptimizeSummary prints optimizer divergence statistics (a metrics dashboard).
// It does not fail by default; ORACLE_STRICT=1 requires zero divergence.
func TestOptimizeSummary(t *testing.T) {
	cases := loadOptCases(t)
	byDialect := map[string]*optStat{}
	byName := map[string]int{}

	for _, c := range cases {
		schema, err := parseOrderedSchema(c.Schema)
		if err != nil {
			t.Fatalf("failed to parse schema of case %s: %v", c.Name, err)
		}
		limit := c.Limit
		got, gotErr := sqlg.NewSQLOptimization(c.Dialect, c.Major).Run(c.SQL, schema, &limit)

		st := byDialect[c.Dialect]
		if st == nil {
			st = &optStat{}
			byDialect[c.Dialect] = st
		}
		st.total++

		if c.OptimizeError != "" {
			if gotErr == nil {
				st.diverged = append(st.diverged,
					fmt.Sprintf("%s[%s]: expected error %s, got %q", c.Name, c.Dialect,
						normErr(c.OptimizeError), got))
				byName[c.Name]++
				continue
			}
			wantKind, wantBody := classifyErr(c.OptimizeError)
			gotKind, gotBody := classifyErr(gotErr.Error())
			if wantKind == gotKind {
				st.errKind++
			}
			if wantBody == gotBody {
				st.bothErr++
				// Identical normalized error message counts as a full match; error cases
				// never reach the st.match++ branch above.
				st.match++
			} else {
				st.diverged = append(st.diverged,
					fmt.Sprintf("%s[%s]: error messages differ\n  want %s: %s\n  got  %s: %s",
						c.Name, c.Dialect, wantKind, wantBody, gotKind, gotBody))
				byName[c.Name]++
			}
			continue
		}

		if gotErr != nil {
			st.diverged = append(st.diverged,
				fmt.Sprintf("%s[%s]: expected success, got error %v", c.Name, c.Dialect, gotErr))
			byName[c.Name]++
			continue
		}
		if got == c.Optimized {
			st.match++
			continue
		}
		st.diverged = append(st.diverged,
			fmt.Sprintf("%s[%s]:\n  want %s\n  got  %s", c.Name, c.Dialect, c.Optimized, got))
		byName[c.Name]++
	}

	dialects := make([]string, 0, len(byDialect))
	for d := range byDialect {
		dialects = append(dialects, d)
	}
	sort.Strings(dialects)

	totalOK, total := 0, 0
	t.Logf("%-12s %7s %7s %7s %7s", "dialect", "cases", "exact", "err-kind", "err-text")
	for _, d := range dialects {
		st := byDialect[d]
		totalOK += st.match
		total += st.total
		t.Logf("%-12s %7d %7d %7d %7d", d, st.total, st.match, st.errKind, st.bothErr)
	}
	t.Logf("total %d/%d exact matches (%.1f%%), %d divergences",
		totalOK, total, 100*float64(totalOK)/float64(total), total-totalOK)

	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return byName[names[i]] > byName[names[j]] })
	if len(names) > 25 {
		names = names[:25]
	}
	t.Logf("SQL shapes with the most divergences:")
	for _, n := range names {
		t.Logf("  %-40s %d", n, byName[n])
	}

	if os.Getenv("ORACLE_STRICT") != "" && total-totalOK > 0 {
		shown := 0
		for _, d := range dialects {
			for _, msg := range byDialect[d].diverged {
				if shown >= 20 {
					break
				}
				t.Errorf("%s", msg)
				shown++
			}
		}
		t.Fatalf("optimizer still has %d divergences", total-totalOK)
	}
}

// TestIsQueryFromOracle parity-tests is_query using real oracle data.
func TestIsQueryFromOracle(t *testing.T) {
	cases := loadOptCases(t)
	checked := 0
	for _, c := range cases {
		if c.IsQuery == nil && c.IsQueryError == "" {
			continue
		}
		checked++
		got, err := sqlg.NewSQLCheck(c.Dialect).IsQuery(c.SQL)
		if c.IsQueryError != "" {
			if err == nil {
				t.Errorf("%s[%s]: expected is_query to error, got %v", c.Name, c.Dialect, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s[%s]: is_query errored unexpectedly: %v", c.Name, c.Dialect, err)
			continue
		}
		if got != *c.IsQuery {
			t.Errorf("%s[%s]: is_query want %v, got %v (sql=%q)", c.Name, c.Dialect, *c.IsQuery, got, c.SQL)
		}
	}
	t.Logf("is_query parity coverage: %d cases", checked)
}
