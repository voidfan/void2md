package ds

import (
	"strings"
	"testing"
)

// TestReadFileSQLParams locks down that both load paths share one set of read
// parameters via readFileSQL (they once drifted, giving the same file different
// column types per entry point).
func TestReadFileSQLParams(t *testing.T) {
	cases := []struct {
		ext  string
		path string
		want string
	}{
		{"csv", "/data/a.csv", "select * from read_csv('/data/a.csv',strict_mode=false)"},
		{"xlsx", "/data/a.xlsx", "select * from read_xlsx('/data/a.xlsx',all_varchar=true)"},
		{"xlsm", "/data/a.xlsm", "select * from read_xlsm('/data/a.xlsm')"},
		{"json", "/data/a.json", "select * from read_json('/data/a.json')"},
		{"parquet", "/data/a.parquet", "select * from read_parquet('/data/a.parquet')"},
	}
	for _, c := range cases {
		if got := readFileSQL(c.path, c.ext); got != c.want {
			t.Errorf("readFileSQL(%q, %q)\n  got  %s\n  want %s", c.path, c.ext, got, c.want)
		}
	}
}

// TestSpreadsheetExtensionSetsCoverFastExcel guarantees every extension in
// fastExcelSupportedExtensions is either readable by DuckDB or explicitly
// rejected via goUnsupportedSpreadsheetExtensions — nothing may slip through to
// a bare "unsupported extension" failure.
func TestSpreadsheetExtensionSetsCoverFastExcel(t *testing.T) {
	if len(goUnsupportedSpreadsheetExtensions) == 0 {
		t.Fatal("goUnsupportedSpreadsheetExtensions must not be empty")
	}
	for ext := range goUnsupportedSpreadsheetExtensions {
		if duckdbRegisterableExtensions[ext] {
			t.Errorf("%s is classified as both \"readable by DuckDB\" and \"unsupported in Go\"; the two sets contradict each other", ext)
		}
		if IngestableExtension(ext) {
			t.Errorf("IngestableExtension(%s) should be false", ext)
		}
		if !fastExcelSupportedExtensions[ext] {
			t.Errorf("%s is in goUnsupportedSpreadsheetExtensions but not in fastExcelSupportedExtensions", ext)
		}
	}
	for ext := range fastExcelSupportedExtensions {
		if !IngestableExtension(ext) && !goUnsupportedSpreadsheetExtensions[ext] {
			t.Errorf("%s in fastExcelSupportedExtensions is neither handled by the DuckDB branch "+
				"nor explicitly rejected, so it would fail with \"unsupported extension\"", ext)
		}
	}
	// Reverse direction: engine-readable formats must be in the readable set.
	for _, ext := range []string{"csv", "xlsx", "xlsm", "json", "parquet"} {
		if !IngestableExtension(ext) {
			t.Errorf("IngestableExtension(%s) should be true", ext)
		}
	}
}

// TestErrSpreadsheetUnsupportedInGoExplainsReason requires the error to name
// the extension and say "no reader implementation" (operators only see the log).
func TestErrSpreadsheetUnsupportedInGoExplainsReason(t *testing.T) {
	for _, ext := range []string{"xls", "xlsb", "ods"} {
		msg := errSpreadsheetUnsupportedInGo(ext).Error()
		if !strings.Contains(msg, ext) {
			t.Errorf("the error text for %s does not mention the extension: %s", ext, msg)
		}
		if !strings.Contains(msg, "no reader implementation") {
			t.Errorf("the error text for %s does not make clear that there is \"no reader implementation\": %s", ext, msg)
		}
	}
}
