package metadata

import (
	"bytes"
	"encoding/json"
	"sort"

	"void2md/structured/pkg/sqlutil"
)

// orderedColumnNames returns the rendering order of columns: DBTable.ColumnOrder when it
// matches the current map (Python dicts preserve insertion order; Go maps don't), else
// sorted by name for stable output.
func (t *DBTable) orderedColumnNames() []string {
	if len(t.ColumnOrder) == len(t.Columns) {
		ok := true
		for _, name := range t.ColumnOrder {
			if _, exists := t.Columns[name]; !exists {
				ok = false
				break
			}
		}
		if ok {
			return t.ColumnOrder
		}
	}
	names := make([]string, 0, len(t.Columns))
	for name := range t.Columns {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// MarshalJSON emits Python model_dump_json field order and keeps columns in insertion order.
func (t *DBTable) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')

	writeField := func(key string, v any, first bool) {
		if !first {
			buf.WriteByte(',')
		}
		k, _ := json.Marshal(key)
		buf.Write(k)
		buf.WriteByte(':')
		writeJSON(&buf, v)
	}

	writeField("name", t.Name, true)
	writeField("ns_name", t.NSName, false)
	writeField("comment", t.Comment, false)

	buf.WriteString(`,"columns":{`)
	for i, name := range t.orderedColumnNames() {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, _ := json.Marshal(name)
		buf.Write(k)
		buf.WriteByte(':')
		writeJSON(&buf, t.Columns[name])
	}
	buf.WriteByte('}')

	writeField("primary_keys", nonNilStrings(t.PrimaryKeys), false)
	writeField("foreign_keys", nonNilForeignKeys(t.ForeignKeys), false)
	writeField("enabled", t.Enabled, false)
	writeField("num_rows", t.NumRows, false)

	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func writeJSON(buf *bytes.Buffer, v any) {
	b, err := sqlutil.MarshalJSON(v)
	if err != nil {
		buf.WriteString("null")
		return
	}
	buf.Write(b)
}

// nonNilStrings makes empty collections serialize as [] (Python default_factory=list), never null.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilForeignKeys(f []*TableForeignKey) []*TableForeignKey {
	if f == nil {
		return []*TableForeignKey{}
	}
	return f
}
