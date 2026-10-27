// Package metadata mirrors the data-model portion of Python structured/core/data_source/base.py.
// Field order strictly matches pydantic model_dump_json output (Go emits fields in
// declaration order) — do not rearrange fields.
package metadata

// TableColumn corresponds to Python's TableColumn.
type TableColumn struct {
	Name          string `json:"name"`
	Type          string `json:"type"`
	Comment       string `json:"comment"`
	AutoIncrement bool   `json:"auto_increment"`
	Nullable      bool   `json:"nullable"`
	Default       any    `json:"default"`
	Examples      []any  `json:"examples"`
	Enabled       bool   `json:"enabled"`
	ValueIndex    bool   `json:"value_index"`
}

// NewTableColumn returns a column with Python's defaults: comment="", nullable=true, enabled=true.
func NewTableColumn(name, typ string) *TableColumn {
	return &TableColumn{
		Name:     name,
		Type:     typ,
		Nullable: true,
		Examples: []any{},
		Enabled:  true,
	}
}

// ConstraintKey corresponds to Python's ConstraintKey.
type ConstraintKey struct {
	Name   string `json:"name"`
	Column string `json:"column"`
}

// TableForeignKey corresponds to Python's TableForeignKey.
type TableForeignKey struct {
	Name             string `json:"name"`
	Column           string `json:"column"`
	ReferencedSchema string `json:"referenced_schema"`
	ReferencedTable  string `json:"referenced_table"`
	ReferencedColumn string `json:"referenced_column"`
}

// DBTable corresponds to Python's DBTable. Python field ns_name has alias "schema":
// input reads "schema", output writes "ns_name".
type DBTable struct {
	Name        string                  `json:"name"`
	NSName      *string                 `json:"ns_name"`
	Comment     string                  `json:"comment"`
	Columns     map[string]*TableColumn `json:"columns"`
	PrimaryKeys []string                `json:"primary_keys"`
	ForeignKeys []*TableForeignKey      `json:"foreign_keys"`
	Enabled     bool                    `json:"enabled"`
	NumRows     *int64                  `json:"num_rows"`

	// ColumnOrder records column insertion order (Python dict order). Not serialized;
	// MarshalJSON handles output order.
	ColumnOrder []string `json:"-"`
}

// DatabaseMetadata corresponds to Python's DatabaseMetadata.
type DatabaseMetadata struct {
	Name    string     `json:"name"`
	Comment string     `json:"comment"`
	Tables  []*DBTable `json:"tables"`
}

// NewDatabaseMetadata returns Python's defaults (comment="", tables=[]).
func NewDatabaseMetadata(name string) *DatabaseMetadata {
	return &DatabaseMetadata{Name: name, Tables: []*DBTable{}}
}
