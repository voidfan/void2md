package metadata

import (
	"bytes"
	"encoding/json"
	"math/big"
	"strings"
)

// decodePyJSON decodes with Python json / orjson semantics: UseNumber keeps number
// literals for pyJSONValue to classify.
func decodePyJSON(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(v)
}

// pyJSONValue restores json.Number to Python's types: integer literal -> int64 (big.Int on
// overflow), else float64. Go would make everything float64; the difference is observable
// in schema text rendered into LLM prompts ("1" vs "1.0").
func pyJSONValue(v any) any {
	switch x := v.(type) {
	case json.Number:
		s := x.String()
		if !strings.ContainsAny(s, ".eE") {
			if i, err := x.Int64(); err == nil {
				return i
			}
			// Python ints are arbitrary precision; fall back to big.Int, preserving the literal.
			if bi, ok := new(big.Int).SetString(s, 10); ok {
				return *bi
			}
		}
		if f, err := x.Float64(); err == nil {
			return f
		}
		return s
	case []any:
		for i := range x {
			x[i] = pyJSONValue(x[i])
		}
		return x
	case map[string]any:
		for k := range x {
			x[k] = pyJSONValue(x[k])
		}
		return x
	default:
		return v
	}
}

// rawColumnShape is the decoding shadow struct for TableColumn. Pointer fields distinguish
// "missing" from explicit false/"" so pydantic defaults (nullable=true, enabled=true, ...) apply.
type rawColumnShape struct {
	Name          string          `json:"name"`
	Type          string          `json:"type"`
	Comment       *string         `json:"comment"`
	AutoIncrement *bool           `json:"auto_increment"`
	Nullable      *bool           `json:"nullable"`
	Default       json.RawMessage `json:"default"`
	Examples      []any           `json:"examples"`
	Enabled       *bool           `json:"enabled"`
	ValueIndex    *bool           `json:"value_index"`
}

func (rc *rawColumnShape) toTableColumn() *TableColumn {
	col := &TableColumn{
		Name:     rc.Name,
		Type:     rc.Type,
		Nullable: true,
		Examples: []any{},
		Enabled:  true,
	}
	if rc.Comment != nil {
		col.Comment = *rc.Comment
	}
	if rc.AutoIncrement != nil {
		col.AutoIncrement = *rc.AutoIncrement
	}
	if rc.Nullable != nil {
		col.Nullable = *rc.Nullable
	}
	if len(rc.Default) > 0 {
		var v any
		if err := decodePyJSON(rc.Default, &v); err == nil {
			col.Default = v
		}
	}
	if rc.Examples != nil {
		col.Examples = rc.Examples
	}
	if rc.Enabled != nil {
		col.Enabled = *rc.Enabled
	}
	if rc.ValueIndex != nil {
		col.ValueIndex = *rc.ValueIndex
	}
	return col
}

// UnmarshalJSON replicates pydantic DBTable deserialization: ns_name's input alias is
// "schema" (writing "ns_name" is ignored), missing fields get defaults, and column key
// order is preserved (Python dict order).
func (t *DBTable) UnmarshalJSON(data []byte) error {
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	*t = DBTable{
		Columns:     map[string]*TableColumn{},
		PrimaryKeys: []string{},
		ForeignKeys: []*TableForeignKey{},
		Enabled:     true,
	}

	if v, ok := raw["name"]; ok {
		if err := json.Unmarshal(v, &t.Name); err != nil {
			return err
		}
	}
	// Only the alias "schema" is recognized.
	if v, ok := raw["schema"]; ok && string(v) != "null" {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return err
		}
		t.NSName = &s
	}
	if v, ok := raw["comment"]; ok && string(v) != "null" {
		if err := json.Unmarshal(v, &t.Comment); err != nil {
			return err
		}
	}
	if v, ok := raw["primary_keys"]; ok && string(v) != "null" {
		if err := json.Unmarshal(v, &t.PrimaryKeys); err != nil {
			return err
		}
	}
	if v, ok := raw["foreign_keys"]; ok && string(v) != "null" {
		if err := json.Unmarshal(v, &t.ForeignKeys); err != nil {
			return err
		}
	}
	if v, ok := raw["enabled"]; ok && string(v) != "null" {
		if err := json.Unmarshal(v, &t.Enabled); err != nil {
			return err
		}
	}
	if v, ok := raw["num_rows"]; ok && string(v) != "null" {
		var n int64
		if err := json.Unmarshal(v, &n); err != nil {
			return err
		}
		t.NumRows = &n
	}
	if v, ok := raw["columns"]; ok && string(v) != "null" {
		cols, order, err := decodeColumnsOrdered(v)
		if err != nil {
			return err
		}
		t.Columns = cols
		t.ColumnOrder = order
	}
	return nil
}

// decodeColumnsOrdered decodes the columns object preserving key appearance order.
func decodeColumnsOrdered(raw json.RawMessage) (map[string]*TableColumn, []string, error) {
	cols := map[string]*TableColumn{}
	order := []string{}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	tok, err := dec.Token()
	if err != nil {
		return cols, order, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return cols, order, nil
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return cols, order, err
		}
		key, _ := keyTok.(string)
		// Capture raw JSON, decode with UseNumber, then normalize so `1` yields int64(1),
		// not float64(1).
		var rawShape json.RawMessage
		if err := dec.Decode(&rawShape); err != nil {
			return cols, order, err
		}
		var rc rawColumnShape
		if err := decodePyJSON(rawShape, &rc); err != nil {
			return cols, order, err
		}
		col := rc.toTableColumn()
		for i, v := range col.Examples {
			col.Examples[i] = pyJSONValue(v)
		}
		col.Default = pyJSONValue(col.Default)
		cols[key] = col
		order = append(order, key)
	}
	if _, err := dec.Token(); err != nil {
		return cols, order, err
	}
	return cols, order, nil
}

// UnmarshalJSON replicates pydantic DatabaseMetadata deserialization (defaults comment="", tables=[]).
func (m *DatabaseMetadata) UnmarshalJSON(data []byte) error {
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*m = DatabaseMetadata{Tables: []*DBTable{}}
	if v, ok := raw["name"]; ok {
		if err := json.Unmarshal(v, &m.Name); err != nil {
			return err
		}
	}
	if v, ok := raw["comment"]; ok && string(v) != "null" {
		if err := json.Unmarshal(v, &m.Comment); err != nil {
			return err
		}
	}
	if v, ok := raw["tables"]; ok && string(v) != "null" {
		if err := json.Unmarshal(v, &m.Tables); err != nil {
			return err
		}
	}
	return nil
}
