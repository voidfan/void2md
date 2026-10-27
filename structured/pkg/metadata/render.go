package metadata

import (
	"strings"

	"void2md/structured/pkg/sqlutil"
)

// ColumnTypePair is an order-preserving "column name → type" entry: column order
// determines star-expansion order in the SQL optimizer, and Go maps are unordered.
type ColumnTypePair struct {
	Name string
	Type string
}

// ColumnTypeOrdered is the order-preserving form of Python's DBTable.column_type.
// Like dict assignment: same-named keys override the value but keep the first-seen position.
func (t *DBTable) ColumnTypeOrdered(caseInsensitive bool) []ColumnTypePair {
	out := make([]ColumnTypePair, 0, len(t.Columns)*4)
	seen := make(map[string]int, len(t.Columns)*4)
	put := func(name, typ string) {
		if i, ok := seen[name]; ok {
			out[i].Type = typ
			return
		}
		seen[name] = len(out)
		out = append(out, ColumnTypePair{Name: name, Type: typ})
	}
	for _, name := range t.orderedColumnNames() {
		col := t.Columns[name]
		put(col.Name, col.Type)
		if caseInsensitive {
			put(sqlutil.PyTitle(col.Name), col.Type)
			put(strings.ToLower(col.Name), col.Type)
			put(strings.ToUpper(col.Name), col.Type)
		}
	}
	return out
}

// ColumnType corresponds to Python's DBTable.column_type; case_insensitive=true also
// writes the title/lower/upper variants.
func (t *DBTable) ColumnType(caseInsensitive bool) map[string]string {
	out := make(map[string]string, len(t.Columns)*4)
	for _, name := range t.orderedColumnNames() {
		col := t.Columns[name]
		out[col.Name] = col.Type
		if caseInsensitive {
			out[sqlutil.PyTitle(col.Name)] = col.Type
			out[strings.ToLower(col.Name)] = col.Type
			out[strings.ToUpper(col.Name)] = col.Type
		}
	}
	return out
}

// ToMarkdown corresponds to Python's DBTable.to_markdown.
func (t *DBTable) ToMarkdown(dbName string) string {
	var tableName string
	if dbName != "" {
		tableName = "### Table name: " + dbName + "." + t.Name
	} else {
		tableName = "### Table name: " + t.Name
	}
	s := "\n" + tableName + "\n" + t.Comment + "\n#### Columns:\n|Name|Description|Type|Examples|\n|---|---|---|---|\n"
	for _, name := range t.orderedColumnNames() {
		col := t.Columns[name]
		if !col.Enabled {
			continue
		}
		parts := make([]string, 0, len(col.Examples))
		for _, v := range col.Examples {
			parts = append(parts, sqlutil.ConvertValue2Str(v))
		}
		s += "|" + col.Name + "|" + col.Comment + "|" + col.Type + "|" + strings.Join(parts, "<br>") + "|\n"
	}
	if len(t.PrimaryKeys) > 0 {
		s += "#### Primary Keys: " + pyTupleRepr(t.PrimaryKeys) + "\n"
	}
	if len(t.ForeignKeys) > 0 {
		fkStr := "#### Foreign Keys: \n"
		for _, fk := range t.ForeignKeys {
			// Only show foreign keys within the same schema
			if t.NSName == nil || *t.NSName == fk.ReferencedSchema {
				fkStr += " - " + fk.Column + " -> " + fk.ReferencedTable + "." + fk.ReferencedColumn + "\n"
			}
		}
		s += fkStr
	}
	return s
}

// ToMSchema corresponds to Python's DBTable.to_m_schema (XiYanSQL M-Schema format).
func (t *DBTable) ToMSchema(dbName string) string {
	output := make([]string, 0, 3)
	var tableComment string
	if dbName != "" {
		tableComment = "# Table: " + dbName + "." + t.Name
	} else {
		tableComment = "# Table: " + t.Name
	}
	if t.Comment != "" {
		tableComment += ", " + t.Comment
	}
	output = append(output, tableComment)

	fieldLines := make([]string, 0, len(t.Columns))
	for _, name := range t.orderedColumnNames() {
		col := t.Columns[name]
		if !col.Enabled {
			continue
		}
		fieldLine := "(" + col.Name + ": " + strings.ToUpper(col.Type)
		if contains(t.PrimaryKeys, col.Name) {
			fieldLine += ", Primary Key"
		}
		if col.Comment != "" {
			fieldLine += ", " + strings.TrimSpace(col.Comment)
		}
		if len(col.Examples) > 0 {
			parts := make([]string, 0, len(col.Examples))
			for _, v := range col.Examples {
				parts = append(parts, sqlutil.ConvertValue2Str(v))
			}
			fieldLine += ", Examples: [" + strings.Join(parts, ", ") + "]"
		}
		fieldLine += ")"
		fieldLines = append(fieldLines, fieldLine)
	}

	output = append(output, "[")
	output = append(output, strings.Join(fieldLines, ",\n"))
	output = append(output, "]")
	return strings.Join(output, "\n")
}

// ToMarkdown corresponds to Python's DatabaseMetadata.to_markdown.
func (m *DatabaseMetadata) ToMarkdown() string {
	s := "\n# Database name: " + m.Name + "\n" + m.Comment + "\n## Tables:\n"
	for _, t := range m.Tables {
		if t.Enabled {
			s += t.ToMarkdown("") + "\n"
		}
	}
	return s
}

// ToMSchema corresponds to Python's DatabaseMetadata.to_m_schema.
func (m *DatabaseMetadata) ToMSchema() string {
	output := []string{"【DB_ID】 " + m.Name}
	if m.Comment != "" {
		output = append(output, m.Comment)
	}
	output = append(output, "【Schema】")
	output = append(output, "schema format: (column name: data type, is primary key, comment, examples)")
	fks := []string{}
	for _, t := range m.Tables {
		if !t.Enabled {
			continue
		}
		output = append(output, t.ToMSchema(""))
		for _, fk := range t.ForeignKeys {
			// Only show foreign keys within the same schema
			if t.NSName == nil || *t.NSName == fk.ReferencedSchema {
				fks = append(fks, t.Name+"."+fk.Column+"="+fk.ReferencedTable+"."+fk.ReferencedColumn)
			}
		}
	}
	if len(fks) > 0 {
		output = append(output, "【Foreign Keys】")
		output = append(output, fks...)
	}
	output = append(output, "\n")
	return strings.Join(output, "\n")
}

// pyTupleRepr replicates Python's repr of a tuple, e.g. ["id"] -> "('id',)", ["a","b"] -> "('a', 'b')".
func pyTupleRepr(items []string) string {
	if len(items) == 1 {
		return "('" + items[0] + "',)"
	}
	quoted := make([]string, 0, len(items))
	for _, it := range items {
		quoted = append(quoted, "'"+it+"'")
	}
	return "(" + strings.Join(quoted, ", ") + ")"
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
