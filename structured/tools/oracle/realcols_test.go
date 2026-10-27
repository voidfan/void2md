package oracle

import (
	"encoding/json"
	"os"
	"sort"
	"testing"

	"void2md/structured/pkg/sqlg"
)

// realColsCase is a single parse_real_cols parity case from golden_realcols.json.
type realColsCase struct {
	Name     string   `json:"name"`
	Dialect  string   `json:"dialect"`
	SQL      string   `json:"sql"`
	ColsTrue []string `json:"cols_True"`
	ColsFals []string `json:"cols_False"`
}

// TestParseRealColsOracle parity-tests parse_real_cols against sqlg.ParseRealCols, covering
// both case_insensitive branches; both sides' results are sorted before comparison to avoid
// set-ordering false divergences. Parse failures print to stdout, as in Python.
func TestParseRealColsOracle(t *testing.T) {
	raw, err := os.ReadFile("golden_realcols.json")
	if err != nil {
		t.Skipf("golden_realcols.json is missing (the oracle scripts were deleted along with the Python implementation; goldens are now read-only): %v", err)
	}
	var cases []realColsCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("failed to parse golden_realcols.json: %v", err)
	}

	var diff []string
	for _, c := range cases {
		for _, ci := range []bool{true, false} {
			want := c.ColsTrue
			if !ci {
				want = c.ColsFals
			}
			got := mapToSorted(sqlg.ParseRealCols(c.SQL, c.Dialect, ci))
			if want == nil {
				want = []string{}
			}
			if !equalStrs(want, got) {
				diff = append(diff, c.Name)
				if len(diff) <= 20 {
					t.Errorf("%s (case_insensitive=%v):\n  py = %v\n  go = %v\n  sql = %s",
						c.Name, ci, want, got, c.SQL)
				}
			}
		}
	}

	total := len(cases) * 2
	t.Logf("parse_real_cols parity: %d/%d matched, %d diverged",
		total-len(diff), total, len(diff))
	if len(diff) > 20 {
		t.Logf("diverging cases (first 20 printed, %d total): %v", len(diff), diff[:20])
	}
}

func mapToSorted(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
