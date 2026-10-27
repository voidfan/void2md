package metadata

import (
	"bytes"
	"encoding/json"

	"void2md/structured/pkg/sqlutil"
)

// MarshalJSON emits fields in Python `TableColumn.model_dump_json()` order
// (name, type, comment, auto_increment, nullable, default, examples, enabled, value_index).
// Hand-written because Go's float JSON formatting differs from pydantic — see sqlutil.PyJSONFloat.
func (c *TableColumn) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')

	writeField := func(key string, v any, pyJSON bool, first bool) error {
		if !first {
			buf.WriteByte(',')
		}
		k, err := json.Marshal(key)
		if err != nil {
			return err
		}
		buf.Write(k)
		buf.WriteByte(':')
		if pyJSON {
			return appendPyJSON(&buf, v)
		}
		return appendPlainJSON(&buf, v)
	}

	if err := writeField("name", c.Name, false, true); err != nil {
		return nil, err
	}
	if err := writeField("type", c.Type, false, false); err != nil {
		return nil, err
	}
	if err := writeField("comment", c.Comment, false, false); err != nil {
		return nil, err
	}
	if err := writeField("auto_increment", c.AutoIncrement, false, false); err != nil {
		return nil, err
	}
	if err := writeField("nullable", c.Nullable, false, false); err != nil {
		return nil, err
	}
	if err := writeField("default", c.Default, true, false); err != nil {
		return nil, err
	}
	if err := writeField("examples", nonNilAny(c.Examples), true, false); err != nil {
		return nil, err
	}
	if err := writeField("enabled", c.Enabled, false, false); err != nil {
		return nil, err
	}
	if err := writeField("value_index", c.ValueIndex, false, false); err != nil {
		return nil, err
	}

	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func appendPlainJSON(buf *bytes.Buffer, v any) error {
	b, err := sqlutil.MarshalJSON(v)
	if err != nil {
		return err
	}
	buf.Write(b)
	return nil
}

func appendPyJSON(buf *bytes.Buffer, v any) error {
	b, err := sqlutil.MarshalPyJSON(v)
	if err != nil {
		return err
	}
	buf.Write(b)
	return nil
}

// nonNilAny makes empty collections serialize as [] (Python default_factory=list), never null.
func nonNilAny(a []any) []any {
	if a == nil {
		return []any{}
	}
	return a
}
