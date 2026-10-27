package sqlg

import (
	"strings"
)

// Replicates the parts of sqlglot.schema used by structured (MappingSchema).
// Column order must be preserved (Python dict ordering drives expand_stars SELECT order),
// so tables/columns use slices, not maps.

// SchemaError mirrors sqlglot.errors.SchemaError.
type SchemaError struct{ Msg string }

// Error carries the "SchemaError: " class-name prefix verbatim: oracle fixtures persist
// `{type(e).__name__}: {e}`, so the prefix is parity-critical.
func (e *SchemaError) Error() string { return "SchemaError: " + e.Msg }

// OptimizeError mirrors sqlglot.errors.OptimizeError. SQL/Line/Col are kept but not
// emitted (oracle's normErr truncates everything after ` Line: ...`).
type OptimizeError struct {
	Msg  string
	SQL  string
	Line int
	Col  int
}

func (e *OptimizeError) Error() string { return "OptimizeError: " + e.Msg }

// PythonError carries exceptions Python would raise but the port has no type for
// (e.g. AttributeError on None inside a rule). They must bubble up like Python's 500,
// not silently produce SQL.
type PythonError struct {
	// Kind is the Python exception class name (e.g. "AttributeError").
	Kind string
	// Msg is the verbatim str(exc).
	Msg string
}

func (e *PythonError) Error() string { return e.Kind + ": " + e.Msg }

// SchemaCol is the definition of one column.
type SchemaCol struct {
	Name string
	Type string
}

// SchemaTable is the definition of one table; Parts holds name levels, Cols preserves order.
type SchemaTable struct {
	Parts []string
	Cols  []SchemaCol
}

// SchemaInput is the input for constructing a MappingSchema: an ordered list of tables.
type SchemaInput struct {
	Tables []*SchemaTable
}

// MappingSchema mirrors sqlglot.schema.MappingSchema.
type MappingSchema struct {
	dialect *Dialect
	// tables is the normalized, order-preserving list of tables.
	tables []*SchemaTable
	// index maps the normalized table name (levels joined with ".") → table definition.
	index map[string]*SchemaTable
	depth int
}

// NewMappingSchema mirrors MappingSchema(schema, dialect=dialect) (normalize=True).
func NewMappingSchema(in *SchemaInput, dialect string) (*MappingSchema, error) {
	s := &MappingSchema{
		dialect: GetDialect(dialect),
		index:   map[string]*SchemaTable{},
	}
	if in == nil {
		s.tables = nil
		s.depth = 0
		return s, nil
	}
	var depth int
	for _, t := range in.Tables {
		norm := &SchemaTable{Parts: make([]string, 0, len(t.Parts))}
		for _, p := range t.Parts {
			norm.Parts = append(norm.Parts, s.dialect.NormalizeIdentifier(p, false))
		}
		if len(t.Cols) == 0 {
			return nil, &SchemaError{Msg: "Table " + strings.Join(norm.Parts, ".") +
				" must have at least one column"}
		}
		// Python dict assignment: columns normalizing to the same name overwrite the
		// value but keep the first insertion position.
		seen := make(map[string]int, len(t.Cols))
		for _, c := range t.Cols {
			name := s.dialect.NormalizeIdentifier(c.Name, false)
			if i, ok := seen[name]; ok {
				norm.Cols[i].Type = c.Type
				continue
			}
			seen[name] = len(norm.Cols)
			norm.Cols = append(norm.Cols, SchemaCol{Name: name, Type: c.Type})
		}
		d := len(norm.Parts) + 1
		if d < 1 || d > 3 {
			return nil, &SchemaError{Msg: "Invalid mapping shape. Depth: " + itoa(d)}
		}
		if depth == 0 {
			depth = d
		} else if depth != d {
			// First table's nesting level is the validation baseline (sqlglot _normalize).
			return nil, &SchemaError{Msg: "Table " + strings.Join(norm.Parts, ".") +
				" must match the schema's nesting level: " + itoa(depth) + "."}
		}
		s.tables = append(s.tables, norm)
		s.index[strings.Join(norm.Parts, ".")] = norm
	}
	s.depth = depth
	return s, nil
}

// Empty mirrors AbstractMappingSchema.empty.
func (s *MappingSchema) Empty() bool { return len(s.tables) == 0 }

// Depth mirrors MappingSchema.depth: dict_depth(mapping) - 1.
func (s *MappingSchema) Depth() int { return s.depth }

// SupportedTableArgs mirrors AbstractMappingSchema.supported_table_args.
func (s *MappingSchema) SupportedTableArgs() []string {
	if s.depth < 1 || s.depth > 3 {
		return nil
	}
	return tableParts[:s.depth]
}

// tableParts mirrors exp.TABLE_PARTS = ('this', 'db', 'catalog').
var tableParts = []string{"this", "db", "catalog"}

// TableParts mirrors AbstractMappingSchema.table_parts: reversed(t.Parts) names,
// innermost to outermost.
func TableParts(t *Table) []string {
	out := make([]string, 0, len(t.Parts))
	for i := len(t.Parts) - 1; i >= 0; i-- {
		out = append(out, t.Parts[i].Name)
	}
	return out
}

// Find mirrors AbstractMappingSchema.find: returns the table's definition, or nil if not found.
func (s *MappingSchema) Find(t *Table) *SchemaTable {
	if s.Empty() {
		return nil
	}
	parts := TableParts(t)
	if n := len(s.SupportedTableArgs()); len(parts) > n {
		parts = parts[:n]
	}
	// nested_get fetches level by level following reversed(parts) — equivalent to
	// joining the table name outermost-to-innermost.
	keyParts := make([]string, 0, len(parts))
	for i := len(parts) - 1; i >= 0; i-- {
		keyParts = append(keyParts, parts[i])
	}
	// Prefix matching replaces sqlglot's trie for partially-qualified names:
	// exact match first, then unique-prefix check.
	if t := s.index[strings.Join(keyParts, ".")]; t != nil {
		return t
	}
	var prefixMatches []*SchemaTable
	prefix := strings.Join(keyParts, ".")
	for _, t := range s.tables {
		full := strings.Join(t.Parts, ".")
		if full == prefix || strings.HasPrefix(full, prefix+".") || strings.HasPrefix(prefix, full+".") {
			prefixMatches = append(prefixMatches, t)
		}
	}
	if len(prefixMatches) == 1 {
		return prefixMatches[0]
	}
	return nil
}

// ColumnNames mirrors MappingSchema.column_names (only_visible is always false).
func (s *MappingSchema) ColumnNames(t *Table) []string {
	st := s.Find(t)
	if st == nil {
		return nil
	}
	out := make([]string, 0, len(st.Cols))
	for _, c := range st.Cols {
		out = append(out, c.Name)
	}
	return out
}

// GetColumnType mirrors MappingSchema.get_column_type. Divergence: sqlglot converts to
// exp.DataType; we keep the original text (scalar types don't affect qualification).
func (s *MappingSchema) GetColumnType(t *Table, columnName string) string {
	st := s.Find(t)
	if st == nil {
		return "UNKNOWN"
	}
	for _, c := range st.Cols {
		if c.Name == columnName {
			return c.Type
		}
	}
	return "UNKNOWN"
}

// HasColumn mirrors MappingSchema.has_column.
func (s *MappingSchema) HasColumn(t *Table, columnName string) bool {
	st := s.Find(t)
	if st == nil {
		return false
	}
	for _, c := range st.Cols {
		if c.Name == columnName {
			return true
		}
	}
	return false
}

// itoa avoids importing strconv in this file.
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
