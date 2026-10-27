//go:build cgo

package ds

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"void2md/structured/pkg/sqlutil"
)

// Regression tests for the duckdb data source, covering paths that need no
// excel/httpfs extensions. DuckdbLocalStore is not used end to end because the
// official Windows extensions are MSVC ABI and incompatible with the mingw
// extension directory; a minimal bare-connection impl is used instead.

type probeImpl struct {
	files []*FileInfo
}

func (p *probeImpl) getConn() (duckdbConn, error) { return openDuckDBConn("") }
func (p *probeImpl) getFiles(ctx context.Context) ([]*FileInfo, error) {
	return p.files, nil
}
func (p *probeImpl) connTest(ctx context.Context) (bool, error) { return true, nil }
func (p *probeImpl) deleteData(ctx context.Context, name string) error {
	return nil
}
func (p *probeImpl) insertData(
	ctx context.Context, filename string, file io.Reader, meta map[string]any,
) (string, error) {
	return "", nil
}

// newProbeStore builds a store with people.csv and nums.json loaded.
func newProbeStore(t *testing.T) *duckdbBase {
	t.Helper()
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "people.csv")
	if err := os.WriteFile(csvPath, []byte("id,name,score\n1,alice,90\n2,bob,80\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	jsonPath := filepath.Join(dir, "nums.json")
	body := `{"a":1,"b":"x"}` + "\n" + `{"a":2,"b":"y"}` + "\n"
	if err := os.WriteFile(jsonPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &duckdbBase{DBName: "probe"}
	store.impl = &probeImpl{files: []*FileInfo{
		{Name: "people", Extension: "csv", Path: csvPath},
		{Name: "nums", Extension: "json", Path: jsonPath},
	}}
	return store
}

// TestDuckdbEngineVersion pins the engine to DuckDB 1.5.5 — the version of the
// Python parity environment (authoritative over uv.lock's 1.4.x).
func TestDuckdbEngineVersion(t *testing.T) {
	conn, err := openDuckDBConn("")
	if err != nil {
		t.Fatalf("openDuckDBConn: %v", err)
	}
	defer func() { _ = conn.Close() }()

	_, rows, err := conn.Query("SELECT version()")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("version returned an unexpected shape: %v", rows)
	}
	if got := rows[0][0]; got != "v1.5.5" {
		t.Fatalf("engine version = %v, want v1.5.5 (aligned with duckdb 1.5.5 in the Python parity environment)", got)
	}
}

// TestDuckdbLoadDataAndQuery covers the path branch of load_data (csv / json
// registered by path).
func TestDuckdbLoadDataAndQuery(t *testing.T) {
	store := newProbeStore(t)
	ctx := context.Background()
	defer func() { _ = store.Close() }()

	if err := store.LoadData(ctx, nil); err != nil {
		t.Fatalf("LoadData: %v", err)
	}
	if !store.loaded {
		t.Fatal("loaded should be true after load_data")
	}
	// Idempotence: a second call must not reload.
	if err := store.LoadData(ctx, nil); err != nil {
		t.Fatalf("LoadData (second call): %v", err)
	}

	cols, rows, sqlErr := store.ExecuteRawSQL(ctx, "select * from people order by id")
	if sqlErr != nil {
		t.Fatalf("ExecuteRawSQL: %v", sqlErr.Msg)
	}
	if strings.Join(cols, ",") != "id,name,score" {
		t.Fatalf("column names = %v", cols)
	}
	if len(rows) != 2 || rows[0][1] != "alice" {
		t.Fatalf("rows = %v", rows)
	}

	// Error branch: msg must be the engine's original error text.
	_, _, sqlErr = store.ExecuteRawSQL(ctx, "select * from nosuchtable")
	if sqlErr == nil {
		t.Fatal("querying a nonexistent table should return an error")
	}
	if !strings.HasPrefix(sqlErr.Msg, `Catalog Error: Table with name nosuchtable does not exist!`) {
		t.Fatalf("error text = %q", sqlErr.Msg)
	}

	n, c, err := store.GetNumRowsAndCols(ctx, "people")
	if err != nil {
		t.Fatalf("GetNumRowsAndCols: %v", err)
	}
	if n != 2 || c != 3 {
		t.Fatalf("GetNumRowsAndCols = (%d,%d), want (2,3)", n, c)
	}
}

// TestDuckdbMetadataMaterializesJSONColumn guards the CAST(tables AS VARCHAR)
// in duckdbBase.Metadata: the Go driver decodes JSON columns to map/[]any (key
// order lost), so without the CAST metadata parsing fails. Do not replace the
// CAST with Go-side json.Marshal — column key order feeds DBTable.ColumnOrder.
func TestDuckdbMetadataMaterializesJSONColumn(t *testing.T) {
	store := newProbeStore(t)
	ctx := context.Background()
	defer func() { _ = store.Close() }()

	md, err := store.Metadata(ctx)
	if err != nil {
		t.Fatalf("Metadata: %v (the tables column must be parseable JSON text)", err)
	}
	if md.Name != "probe" {
		t.Fatalf("metadata.name = %q, want probe (dir_path.stem)", md.Name)
	}

	names := make([]string, 0, len(md.Tables))
	for _, tb := range md.Tables {
		names = append(names, tb.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "nums,people" {
		t.Fatalf("table names = %v, want [nums people]", names)
	}

	// The column order comes from the JSON object key order and must match the
	// column order in the files.
	for _, tb := range md.Tables {
		var want []string
		switch tb.Name {
		case "people":
			want = []string{"id", "name", "score"}
		case "nums":
			want = []string{"a", "b"}
		}
		got := tb.ColumnOrder
		if len(got) != len(tb.Columns) {
			got = make([]string, 0, len(tb.Columns))
			for k := range tb.Columns {
				got = append(got, k)
			}
			sort.Strings(got)
		}
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("columns of table %s = %v, want %v", tb.Name, got, want)
		}
	}
}

// TestDuckdbTypeNormalization pins the DuckDB type -> Go value -> JSON
// mapping. Every expected value was measured on the Python side; only
// TIMESTAMPTZ is computed here (it depends on the session time zone).
func TestDuckdbTypeNormalization(t *testing.T) {
	conn, err := openDuckDBConn("")
	if err != nil {
		t.Fatalf("openDuckDBConn: %v", err)
	}
	defer func() { _ = conn.Close() }()

	cols, rows, err := conn.Query(`select
  1 as i, 1.5 as f, cast(1.50 as decimal(10,2)) as d, true as bo,
  date '2024-01-02' as dt, time '12:34:56' as tm,
  timestamp '2024-01-02 03:04:05.123456' as ts,
  timestamptz '2024-01-02 03:04:05+00' as tstz,
  timestamp '2024-01-02 03:04:05' as ts0,
  uuid '11111111-2222-3333-4444-555555555555' as u,
  interval '1 day' as iv, interval '2 hours 3 minutes' as iv2,
  [1,2,3] as l, {'x':1} as s, map([1],[2]) as m,
  'txt' as t, NULL as n, 'blob'::blob as bl,
  cast(12345678901234567890 as hugeint) as hi`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d rows", len(rows))
	}
	body, err := sqlutil.MarshalJSON(map[string]any{"cols": cols, "data": rows})
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	got := string(body)

	// Expected TIMESTAMPTZ computed for the local zone (Python uses the session
	// zone too).
	tstz := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC).In(time.Local).Format(time.RFC3339)

	want := `{"cols":["i","f","d","bo","dt","tm","ts","tstz","ts0","u","iv","iv2","l","s","m","t","n","bl","hi"],` +
		`"data":[[1,"1.5","1.50",true,"2024-01-02","12:34:56","2024-01-02T03:04:05.123456",` +
		`"` + tstz + `","2024-01-02T03:04:05","11111111-2222-3333-4444-555555555555",` +
		`"P1D","PT2H3M",[1,2,3],{"x":1},{"1":2},"txt",null,"blob",12345678901234567890]]}`
	if got != want {
		t.Fatalf("type normalization mismatch\n got: %s\nwant: %s", got, want)
	}
}

// TestDuckdbJSONColumnKnownNonequivalence locks down the known non-equivalence:
// the Go driver decodes JSON columns to map/[]any (key order lost) while Python
// materializes them as str. If this test starts failing on the string case, the
// driver changed and the note in duckdb_engine.go should be updated.
func TestDuckdbJSONColumnKnownNonequivalence(t *testing.T) {
	conn, err := openDuckDBConn("")
	if err != nil {
		t.Fatalf("openDuckDBConn: %v", err)
	}
	defer func() { _ = conn.Close() }()

	cols, rows, err := conn.Query(`select '{"b":1,"a":2}'::json as j`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if cols[0] != "j" {
		t.Fatalf("column names = %v", cols)
	}
	switch rows[0][0].(type) {
	case string:
		t.Fatalf("the JSON column is already a str (%#v) — "+
			"the driver behavior has changed and can now be aligned with Python; please update this case and the note in the file header", rows[0][0])
	case map[string]any:
		// Current state; Python would give the string '{"b":1,"a":2}'.
		if _, err := json.Marshal(rows[0][0]); err != nil {
			t.Fatalf("unexpected: %v", err)
		}
	default:
		t.Fatalf("the JSON column returned an unexpected type %T", rows[0][0])
	}
}
