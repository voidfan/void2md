package ds

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"void2md/structured/pkg/metadata"
)

// Golden-file tests comparing SQL data sources against the Python oracle
// (tools/oracle/golden_examples.json). duckdb is excluded (needs CGO engine).

// examplesGoldenEntry is one dialect entry in the golden file.
type examplesGoldenEntry struct {
	Dialect     string   `json:"dialect"`
	StringTypes []string `json:"string_types"`
	SQL         string   `json:"sql"`
	Error       string   `json:"error"`
}

// loadExamplesGolden reads the oracle-generated golden; _meta_db_name is
// filtered out.
func loadExamplesGolden(t *testing.T) map[string]examplesGoldenEntry {
	t.Helper()
	path := filepath.Join("..", "..", "tools", "oracle", "golden_examples.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read golden %s: %v", path, err)
	}
	rawMap := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		t.Fatalf("failed to parse golden: %v", err)
	}
	out := map[string]examplesGoldenEntry{}
	for k, v := range rawMap {
		if k == "_meta_db_name" {
			continue
		}
		var e examplesGoldenEntry
		if err := json.Unmarshal(v, &e); err != nil {
			t.Fatalf("failed to parse golden[%s]: %v", k, err)
		}
		out[k] = e
	}
	return out
}

// goldenTable builds the users(id,name,city) table used by the oracle.
func goldenTable() *metadata.DBTable {
	names := []string{"id", "name", "city"}
	cols := map[string]*metadata.TableColumn{}
	for _, n := range names {
		cols[n] = metadata.NewTableColumn(n, "VARCHAR")
	}
	return &metadata.DBTable{Name: "users", Columns: cols, ColumnOrder: names}
}

// goldenDialects stays in sync with make_ds in run_examples_oracle.py; duckdb
// is excluded because it needs the CGO engine.
func goldenDialects() map[string]DataSource {
	return map[string]DataSource{
		"mysql":     NewMysql("h", 3306, "u", "p", "db", false),
		"starrocks": NewStarRocks("h", 9030, "u", "p", "db", false),
		"postgres":  NewPostgres("h", 5432, "u", "p", "db", "public", false),
		"sqlite":    NewSqlite("some/dir/mydb.sqlite"),
		"oracle":    NewOracle("h", 1521, "u", "p", "XEPDB1", ""),
		"mssql":     NewMssql("h", 1433, "u", "p", "db", "dbo", false, 30),
	}
}

// TestGoldenAggDistinctValuesSQL compares agg_distinct_values_sql output
// against the golden (limit=3).
func TestGoldenAggDistinctValuesSQL(t *testing.T) {
	golden := loadExamplesGolden(t)
	table := goldenTable()

	for kind, source := range goldenDialects() {
		t.Run(kind, func(t *testing.T) {
			want, ok := golden[kind]
			if !ok {
				t.Fatalf("golden is missing dialect %s", kind)
			}
			if got := source.Dialect(); got != want.Dialect {
				t.Errorf("dialect: want %q, got %q", want.Dialect, got)
			}
			got, err := source.AggDistinctValuesSQL(table, 3)
			if want.Error != "" {
				if err == nil {
					t.Fatalf("expected error %q, but it succeeded: %s", want.Error, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("agg_distinct_values_sql returned an error: %v", err)
			}
			if got != want.SQL {
				t.Errorf("SQL mismatch\nwant length: %d\nwant:\n%s\ngot:\n%s", len(want.SQL), want.SQL, got)
			}
		})
	}
}

// TestGoldenStringTypes compares string_types against the golden (sorted).
func TestGoldenStringTypes(t *testing.T) {
	golden := loadExamplesGolden(t)

	for kind, source := range goldenDialects() {
		t.Run(kind, func(t *testing.T) {
			want, ok := golden[kind]
			if !ok {
				t.Fatalf("golden is missing dialect %s", kind)
			}
			got := make([]string, 0, len(source.StringTypes()))
			for k := range source.StringTypes() {
				got = append(got, k)
			}
			sort.Strings(got)
			if len(got) != len(want.StringTypes) {
				t.Fatalf("string_types count mismatch\nwant: %v\ngot: %v", want.StringTypes, got)
			}
			for i := range want.StringTypes {
				if got[i] != want.StringTypes[i] {
					t.Fatalf("string_types mismatch\nwant: %v\ngot: %v", want.StringTypes, got)
				}
			}
		})
	}
}

// TestUnsupportedDialectError covers non-sqlglot dialects: mssql has its own
// AggDistinctValuesSQL override and must not fail. (maria reuses Mysql, whose
// dialect stays "mysql", so nothing to assert there.)
func TestUnsupportedDialectError(t *testing.T) {
	table := goldenTable()
	mssql := NewMssql("h", 1433, "u", "p", "db", "dbo", false, 30)
	if _, err := mssql.AggDistinctValuesSQL(table, 3); err != nil {
		t.Fatalf("mssql has its own override and should not fail: %v", err)
	}
}

// TestMetadataWithValueExamplesPropagates confirms value-example parse failures
// propagate upward (Python's JSONDecodeError / KeyError / IndexError -> 400).
func TestMetadataWithValueExamplesPropagates(t *testing.T) {
	table := goldenTable()
	ds := &stubDS{
		metadata: &metadata.DatabaseMetadata{Name: "db", Tables: []*metadata.DBTable{table}},
		aggSQL:   "SELECT 1",
		cols:     []string{"name", "examples"},
		// The first row's col_name matches, but examples is not valid JSON
		rows: [][]any{{"id", "not json"}},
	}
	if _, err := ds.MetadataWithValueExamples(context.Background(), 3, 200); err == nil {
		t.Fatal("expected an error when examples cannot be parsed")
	}

	// col_name is not in the table -> mirrors Python's KeyError
	ds.rows = [][]any{{"nope", `["a"]`}}
	if _, err := ds.MetadataWithValueExamples(context.Background(), 3, 200); err == nil {
		t.Fatal("expected an error for an unknown column name")
	}

	// examples is an empty array -> mirrors Python's str_examples[0] IndexError
	ds.rows = [][]any{{"id", `[]`}}
	if _, err := ds.MetadataWithValueExamples(context.Background(), 3, 200); err == nil {
		t.Fatal("expected an error for empty examples")
	}

	// Happy path: a string type that is not a uuid/number/date/email -> value_index = true
	ds.rows = [][]any{{"name", `["北京","上海"]`}}
	m, err := ds.MetadataWithValueExamples(context.Background(), 3, 200)
	if err != nil {
		t.Fatalf("the happy path should not fail: %v", err)
	}
	col := m.Tables[0].Columns["name"]
	if len(col.Examples) != 2 || col.Examples[0] != "北京" {
		t.Fatalf("examples mismatch: %v", col.Examples)
	}
	if !col.ValueIndex {
		t.Fatal("value_index should be true")
	}
}

// stubDS is the minimal test double for metadataWithValueExamples.
type stubDS struct {
	baseDS
	metadata *metadata.DatabaseMetadata
	aggSQL   string
	cols     []string
	rows     [][]any
}

func (s *stubDS) Dialect() string                        { return "mysql" }
func (s *stubDS) StringTypes() map[string]bool           { return map[string]bool{"VARCHAR": true} }
func (s *stubDS) JSONArrayAggFunc() string               { return "JSON_ARRAYAGG" }
func (s *stubDS) ConnTest(context.Context) (bool, error) { return true, nil }
func (s *stubDS) ServerVersion(context.Context) (*metadata.DBServerVersion, error) {
	return &metadata.DBServerVersion{Major: 8}, nil
}
func (s *stubDS) Metadata(context.Context) (*metadata.DatabaseMetadata, error) {
	return s.metadata, nil
}
func (s *stubDS) ExecuteRawSQL(context.Context, string) ([]string, [][]any, *metadata.SQLException) {
	return s.cols, s.rows, nil
}
func (s *stubDS) AggDistinctValuesSQL(*metadata.DBTable, int) (string, error) { return s.aggSQL, nil }

// MetadataWithValueExamples reuses the baseDS implementation (as every driver does).
func (s *stubDS) MetadataWithValueExamples(
	ctx context.Context, valueNum, valueStrMaxLength int,
) (*metadata.DatabaseMetadata, error) {
	return s.metadataWithValueExamples(ctx, s, "db", 1, valueNum, valueStrMaxLength)
}
