package metadata

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"void2md/structured/pkg/sqlutil"
)

type metadataGolden struct {
	// Raw is the Python-side input (exported by run_metadata_oracle.py); tests use it
	// directly to avoid fixture drift.
	Raw string `json:"raw"`

	JSON            string              `json:"json"`
	JSONIndent      string              `json:"json_indent"`
	Markdown        string              `json:"markdown"`
	MSchema         string              `json:"m_schema"`
	TableMarkdown   string              `json:"table_markdown"`
	TableMarkdownDB string              `json:"table_markdown_dbname"`
	TableMSchema    string              `json:"table_m_schema"`
	TableMSchemaDB  string              `json:"table_m_schema_dbname"`
	ColumnType      []map[string]string `json:"column_type"`
	ServerVersion   []string            `json:"server_version"`
}

func loadMetadataGolden(t *testing.T) metadataGolden {
	t.Helper()
	path := filepath.Join("..", "..", "tools", "oracle", "golden_metadata.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read golden %s: %v", path, err)
	}
	var g metadataGolden
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("failed to parse golden: %v", err)
	}
	return g
}

func mustDecode(t *testing.T) *DatabaseMetadata {
	t.Helper()
	return decodeGoldenRaw(t, loadMetadataGolden(t))
}

// decodeGoldenRaw builds the metadata model from the golden's raw input.
func decodeGoldenRaw(t *testing.T, g metadataGolden) *DatabaseMetadata {
	t.Helper()
	var m DatabaseMetadata
	if err := json.Unmarshal([]byte(g.Raw), &m); err != nil {
		t.Fatalf("failed to parse golden.raw: %v", err)
	}
	return &m
}

// TestGoldenModelJSON: parity with model_dump_json() (compact and indent=2).
func TestGoldenModelJSON(t *testing.T) {
	g := loadMetadataGolden(t)
	m := mustDecode(t)

	got, err := sqlutil.MarshalJSON(m)
	if err != nil {
		t.Fatalf("serialization failed: %v", err)
	}
	if string(got) != g.JSON {
		t.Errorf("compact JSON mismatch\nwant: %s\ngot:  %s", g.JSON, got)
	}

	gotIndent, err := sqlutil.MarshalJSONIndent(m, "  ")
	if err != nil {
		t.Fatalf("indented serialization failed: %v", err)
	}
	if string(gotIndent) != g.JSONIndent {
		t.Errorf("indented JSON mismatch\nwant:\n%s\ngot:\n%s", g.JSONIndent, gotIndent)
	}
}

// TestGoldenRender: parity on to_markdown / to_m_schema combinations.
func TestGoldenRender(t *testing.T) {
	g := loadMetadataGolden(t)
	m := mustDecode(t)
	table := m.Tables[0]

	if got := m.ToMarkdown(); got != g.Markdown {
		t.Errorf("DatabaseMetadata.to_markdown mismatch\nwant:\n%s\ngot:\n%s", g.Markdown, got)
	}
	if got := m.ToMSchema(); got != g.MSchema {
		t.Errorf("DatabaseMetadata.to_m_schema mismatch\nwant:\n%s\ngot:\n%s", g.MSchema, got)
	}
	if got := table.ToMarkdown(""); got != g.TableMarkdown {
		t.Errorf("DBTable.to_markdown mismatch\nwant:\n%s\ngot:\n%s", g.TableMarkdown, got)
	}
	if got := table.ToMarkdown("shop"); got != g.TableMarkdownDB {
		t.Errorf("DBTable.to_markdown(db_name) mismatch\nwant:\n%s\ngot:\n%s", g.TableMarkdownDB, got)
	}
	if got := table.ToMSchema(""); got != g.TableMSchema {
		t.Errorf("DBTable.to_m_schema mismatch\nwant:\n%s\ngot:\n%s", g.TableMSchema, got)
	}
	if got := table.ToMSchema("shop"); got != g.TableMSchemaDB {
		t.Errorf("DBTable.to_m_schema(db_name) mismatch\nwant:\n%s\ngot:\n%s", g.TableMSchemaDB, got)
	}
}

// TestGoldenColumnType: parity on both case modes of column_type.
func TestGoldenColumnType(t *testing.T) {
	g := loadMetadataGolden(t)
	table := mustDecode(t).Tables[0]

	got := table.ColumnType(true)
	if len(got) != len(g.ColumnType[0]) {
		t.Fatalf("column_type(true) key count mismatch: want %d got %d\ngot: %v",
			len(g.ColumnType[0]), len(got), got)
	}
	for k, want := range g.ColumnType[0] {
		if got[k] != want {
			t.Errorf("column_type(true)[%q] want %q got %q", k, want, got[k])
		}
	}

	gotCI := table.ColumnType(false)
	if len(gotCI) != len(g.ColumnType[1]) {
		t.Fatalf("column_type(false) key count mismatch: want %d got %d", len(g.ColumnType[1]), len(gotCI))
	}
	for k, want := range g.ColumnType[1] {
		if gotCI[k] != want {
			t.Errorf("column_type(false)[%q] want %q got %q", k, want, gotCI[k])
		}
	}
}

// TestGoldenServerVersion: parity on DBServerVersion __str__ and model_dump_json.
func TestGoldenServerVersion(t *testing.T) {
	g := loadMetadataGolden(t)

	patch := 33
	withPatch := DBServerVersion{Major: 8, Minor: 0, Patch: &patch}
	noPatch := DBServerVersion{Major: 8, Minor: 0}

	if got := withPatch.String(); got != g.ServerVersion[0] {
		t.Errorf("str(8.0.33) want %q got %q", g.ServerVersion[0], got)
	}
	if got := noPatch.String(); got != g.ServerVersion[1] {
		t.Errorf("str(8.0) want %q got %q", g.ServerVersion[1], got)
	}
	raw, err := sqlutil.MarshalJSON(withPatch)
	if err != nil {
		t.Fatalf("serialization failed: %v", err)
	}
	if string(raw) != g.ServerVersion[2] {
		t.Errorf("model_dump_json want %s got %s", g.ServerVersion[2], raw)
	}
}

// TestColumnOrderPreserved: columns keep insertion order (Python dict semantics).
func TestColumnOrderPreserved(t *testing.T) {
	m := mustDecode(t)
	order := m.Tables[0].orderedColumnNames()
	want := []string{"id", "name", "city"}
	if len(order) != len(want) {
		t.Fatalf("column count mismatch: want %v got %v", want, order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("column order mismatch: want %v got %v", want, order)
		}
	}
}
