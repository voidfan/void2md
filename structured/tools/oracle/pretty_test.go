package oracle

// Full-corpus parity test for the /data_source/sql_format endpoint (852 cases).
// golden_pretty.json covers two branches: known dialects transpile with
// pretty=True/indent=4/pad=4; unknown types (maria/mssql) return the input verbatim.

import (
	"testing"

	"void2md/structured/pkg/sqlg"
)

type prettyCase struct {
	Name        string `json:"name"`
	Dialect     string `json:"dialect"`
	SQL         string `json:"sql"`
	InSet       bool   `json:"in_set"`
	Pretty      string `json:"pretty"`
	PrettyError string `json:"pretty_error"`
}

func TestPrettyCorpus(t *testing.T) {
	cases := loadJSON[prettyCase](t, "golden_pretty.json")
	var pass, fail, bothErr, passthrough int

	for _, c := range cases {
		if !c.InSet {
			// Unrecognized dialect: the endpoint returns the input verbatim.
			got, ok, err := sqlg.FormatSQL(c.SQL, c.Dialect)
			if err != nil {
				fail++
				t.Errorf("%s[%s]: the unrecognized-dialect branch must not error, got %v", c.Name, c.Dialect, err)
			} else if ok {
				fail++
				t.Errorf("%s[%s]: expected \"unrecognized dialect → return verbatim\", but the SQL was formatted for the dialect", c.Name, c.Dialect)
			} else if got != c.SQL {
				fail++
				t.Errorf("%s[%s]: verbatim-return mismatch\n  want %q\n  got  %q", c.Name, c.Dialect, c.SQL, got)
			} else {
				passthrough++
			}
			continue
		}

		_, parseErr := sqlg.ParseStatements(c.SQL, c.Dialect)
		if c.PrettyError != "" {
			if parseErr == nil {
				fail++
				t.Errorf("%s[%s]: expected error %s, but parsing succeeded", c.Name, c.Dialect, normErr(c.PrettyError))
			} else {
				bothErr++
			}
			// FormatSQL must also error: Python's transpile raises up to FastAPI (500);
			// swallowing it would degrade the endpoint to 200 + empty SQL.
			if _, _, err := sqlg.FormatSQL(c.SQL, c.Dialect); err == nil {
				fail++
				t.Errorf("%s[%s]: FormatSQL did not bubble %s up to the endpoint caller", c.Name, c.Dialect, normErr(c.PrettyError))
			}
			continue
		}
		if parseErr != nil {
			fail++
			t.Errorf("%s[%s]: expected success, got parse failure %v", c.Name, c.Dialect, parseErr)
			continue
		}
		got, _, err := sqlg.FormatSQL(c.SQL, c.Dialect)
		if err != nil {
			fail++
			t.Errorf("%s[%s]: FormatSQL returned an error %v", c.Name, c.Dialect, err)
			continue
		}
		if got == c.Pretty {
			pass++
			continue
		}
		fail++
		t.Errorf("%s[%s]:\n  sql  %s\n  want %q\n  got  %q", c.Name, c.Dialect, c.SQL, c.Pretty, got)
	}

	t.Logf("sql_format: %d matched, %d both-errored, %d returned verbatim, %d diverged", pass, bothErr, passthrough, fail)
}
