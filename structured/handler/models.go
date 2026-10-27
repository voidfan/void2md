// Package api corresponds to Python's structured/api/ and structured/main.py.
package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Data source configuration (Python data_model.py's DataSourceConfig discriminated union)

// DatabaseConfigBase corresponds to Python's DatabaseConfigBase.
// The default port 3306 lives on the base class, so mssql/oracle/postgres also get it —
// existing Python behavior that must be kept.
type DatabaseConfigBase struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// SqliteConfig corresponds to Python's SqliteConfig.
type SqliteConfig struct {
	Type   string `json:"type"`
	DBPath string `json:"db_path"`
}

// MssqlConfig corresponds to Python's MssqlConfig.
type MssqlConfig struct {
	DatabaseConfigBase
	Type    string `json:"type"`
	DBName  string `json:"db_name"`
	NSName  string `json:"ns_name"` // alias: schema
	Encrypt bool   `json:"encrypt"`
}

// MysqlConfig corresponds to Python's MysqlConfig (type may be mysql / maria / starrocks).
type MysqlConfig struct {
	DatabaseConfigBase
	Type         string `json:"type"`
	DBName       string `json:"db_name"`
	ConnPoolSize int    `json:"conn_pool_size"`
}

// OracleConfig corresponds to Python's OracleConfig.
type OracleConfig struct {
	DatabaseConfigBase
	Type   string  `json:"type"`
	DBName string  `json:"db_name"`
	NSName *string `json:"ns_name"` // alias: schema
}

// PostgresConfig corresponds to Python's PostgresConfig.
type PostgresConfig struct {
	DatabaseConfigBase
	Type         string `json:"type"`
	DBName       string `json:"db_name"`
	NSName       string `json:"ns_name"` // alias: schema
	ConnPoolSize int    `json:"conn_pool_size"`
}

// DuckdbLocalStoreConfig corresponds to Python's DuckdbLocalStoreConfig.
type DuckdbLocalStoreConfig struct {
	Type    string `json:"type"`
	DirPath string `json:"dir_path"`
}

// DuckdbS3StoreConfig corresponds to Python's DuckdbS3StoreConfig.
type DuckdbS3StoreConfig struct {
	Type      string `json:"type"`
	Kid       string `json:"kid"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	Bucket    string `json:"bucket"`
	Region    string `json:"region"`
	UseSSL    bool   `json:"use_ssl"`
}

// DuckdbMongoStoreConfig corresponds to Python's DuckdbMongoStoreConfig.
// uri/bucket env defaults are snapshotted once at process start (Python class-body evaluation);
// later env changes have no effect.
type DuckdbMongoStoreConfig struct {
	Type    string   `json:"type"`
	URI     string   `json:"uri"`
	Bucket  string   `json:"bucket"`
	Kid     string   `json:"kid"`
	FileIDs []string `json:"file_ids"`
}

// DuckdbStoreConfig corresponds to Python's store discriminated union.
type DuckdbStoreConfig struct {
	// Type is one of local / s3 / mongo.
	Type string
	// Store is *DuckdbLocalStoreConfig / *DuckdbS3StoreConfig / *DuckdbMongoStoreConfig per Type.
	Store any
}

// DuckdbConfig corresponds to Python's DuckdbConfig.
type DuckdbConfig struct {
	Type  string `json:"type"`
	Store DuckdbStoreConfig
}

// DataSourceConfig corresponds to Python's `DataSourceConfig = Union[...]` (discriminator="type").
type DataSourceConfig struct {
	// Type is the discriminator: sqlite / mssql / mysql / maria / starrocks / oracle / postgres / duckdb.
	Type string
	// Config is the parsed branch config (*SqliteConfig, *MssqlConfig, …).
	Config any
}

// FastAPI-style 422 validation failure details
// (spec: golden responses of the real Python service, tools/oracle/golden/e2e_service.json)

// pydantic v2 error messages (copied verbatim) and the discriminated-union tag lists.
const (
	msgModelAttrs    = "Input should be a valid dictionary or object to extract fields from"
	msgFieldRequired = "Field required"
	msgStringType    = "Input should be a valid string"
	msgIntType       = "Input should be a valid integer"
	msgIntParsing    = "Input should be a valid integer, unable to parse string as an integer"
	msgIntFromFloat  = "Input should be a valid integer, got a number with a fractional part"
	msgFloatType     = "Input should be a valid number"
	msgFloatParsing  = "Input should be a valid number, unable to parse string as a number"
	msgBoolType      = "Input should be a valid boolean"
	msgBoolParsing   = "Input should be a valid boolean, unable to interpret input"
	// The dict/list messages are **python-mode** wording (FastAPI validates request bodies with
	// model_validate); json mode would say "an object" / "a valid array". We follow FastAPI.
	msgDictType = "Input should be a valid dictionary"
	msgListType = "Input should be a valid list"
)

// dataSourceTags / duckdbStoreTags are the discriminated-union tag orders, matching the Python
// Union[...] written order (union_tag_invalid's expected_tags is joined in this order).
var (
	dataSourceTags  = []string{"sqlite", "mssql", "mysql", "maria", "starrocks", "oracle", "postgres", "duckdb"}
	duckdbStoreTags = []string{"local", "s3", "mongo"}
)

// validationError is one entry of the 422 detail array. Key order: type, loc, msg, input[, ctx].
// Input must stay a json.RawMessage raw fragment (missing -> parent object, type errors -> the
// offending value): map/slice conversion would reorder keys, while Python preserves document
// order; nil serializes as null; final bytes come from pyJSONFragment. Loc is []any because
// json_invalid's last segment is a numeric index.
type validationError struct {
	Type  string          `json:"type"`
	Loc   []any           `json:"loc"`
	Msg   string          `json:"msg"`
	Input json.RawMessage `json:"input"`
	Ctx   map[string]any  `json:"ctx,omitempty"`
}

// pyCtxKeyOrder pins the ctx key order to pydantic's: Go maps would serialize lexicographically
// (e.g. expected_tags before tag), diverging from Python byte-wise.
var pyCtxKeyOrder = []string{"discriminator", "tag", "expected_tags", "expected", "error"}

// MarshalJSON locks the key order: type, loc, msg, input[, ctx], with ctx ordered by
// pyCtxKeyOrder (unlisted keys fall back to lexicographic). input goes through pyJSONFragment
// (RawMessage must not be json.Marshal'ed directly — see pyJSONFragment).
func (e validationError) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	fields := []struct {
		key string
		val any
	}{
		{"type", e.Type},
		{"loc", e.Loc},
		{"msg", e.Msg},
	}
	for i, f := range fields {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := writeJSONPair(&buf, f.key, f.val); err != nil {
			return nil, err
		}
	}
	buf.WriteString(`,"input":`)
	buf.Write(pyJSONFragment(e.Input))
	if len(e.Ctx) > 0 {
		buf.WriteString(`,"ctx":{`)
		for i, k := range pyCtxKeys(e.Ctx) {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeJSONPair(&buf, k, e.Ctx[k]); err != nil {
				return nil, err
			}
		}
		buf.WriteByte('}')
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// pyCtxKeys returns the key order for ctx: first per pyCtxKeyOrder, remaining keys in lexicographic order as a fallback.
func pyCtxKeys(ctx map[string]any) []string {
	keys := make([]string, 0, len(ctx))
	seen := make(map[string]bool, len(ctx))
	for _, k := range pyCtxKeyOrder {
		if _, ok := ctx[k]; ok {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	rest := make([]string, 0, len(ctx))
	for k := range ctx {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(keys, rest...)
}

// writeJSONPair writes a single "key":value pair.
func writeJSONPair(buf *bytes.Buffer, key string, val any) error {
	buf.WriteString(pyStringJSON(key))
	buf.WriteByte(':')
	return pyWriteScalar(buf, val)
}

// pyWriteScalar writes simple values per Python json.dumps. Strings go through
// pyStringJSON: json.Marshal would HTML-escape < > &, unlike Python.
func pyWriteScalar(buf *bytes.Buffer, val any) error {
	switch v := val.(type) {
	case string:
		buf.WriteString(pyStringJSON(v))
	case []any:
		buf.WriteByte('[')
		for i, e := range v {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := pyWriteScalar(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case nil:
		buf.WriteString("null")
	default:
		// Numbers / booleans: Go's rendering matches Python's.
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		buf.Write(b)
	}
	return nil
}

// pyJSONFragment reproduces the final bytes of pydantic's input: FastAPI re-serializes the
// decoded object with ensure_ascii=False and compact separators (escapes decoded, whitespace
// gone, < > & not HTML-escaped), preserving document key order — so no map intermediate, and
// never json.Marshal (it re-compacts with HTML escaping). Known partial coverage: numbers are
// copied verbatim (`1e2` stays "1e2" where Python gives "100.0").
func pyJSONFragment(raw json.RawMessage) []byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return []byte("null")
	}
	var buf bytes.Buffer
	if !pyWriteJSON(&buf, trimmed) {
		return trimmed
	}
	return buf.Bytes()
}

// pyWriteJSON writes a valid JSON fragment into buf per Python's conventions; false on parse failure.
func pyWriteJSON(buf *bytes.Buffer, raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	switch raw[0] {
	case '{', '[':
		dec := json.NewDecoder(bytes.NewReader(raw))
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return false
		}
		closer := byte('}')
		if delim == '[' {
			closer = ']'
		}
		buf.WriteByte(byte(delim))
		first := true
		for dec.More() {
			if !first {
				buf.WriteByte(',')
			}
			first = false
			if delim == '{' {
				keyTok, err := dec.Token()
				if err != nil {
					return false
				}
				key, ok := keyTok.(string)
				if !ok {
					return false
				}
				buf.WriteString(pyStringJSON(key))
				buf.WriteByte(':')
			}
			var elem json.RawMessage
			if err := dec.Decode(&elem); err != nil {
				return false
			}
			if !pyWriteJSON(buf, bytes.TrimSpace(elem)) {
				return false
			}
		}
		if _, err := dec.Token(); err != nil { // closing bracket
			return false
		}
		buf.WriteByte(closer)
		return true
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return false
		}
		buf.WriteString(pyStringJSON(s))
		return true
	}
	// Numbers and true/false/null: the JSON source text is exactly Python's literal form.
	buf.Write(raw)
	return true
}

// pyStringJSON renders a string like Python json.dumps: non-ASCII verbatim, short escapes for
// \n \r \t \b \f, other control chars as \u00XX. json.Marshal cannot be used (it emits \u00XX
// for 0x08/0x0c and escapes < > &).
func pyStringJSON(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// pydantic field validation helpers

// fieldSet is the validation context for one model instance: raw field table, the object's raw
// JSON (parent — the input for missing errors), and its relative loc prefix (union branches
// carry the tag). Errors accumulate: pydantic reports all field errors in one pass, in
// declaration order.
type fieldSet struct {
	raw    map[string]json.RawMessage
	parent json.RawMessage
	loc    []any
}

// path returns the field's loc (relative to the current object).
func (f fieldSet) path(name string) []any {
	loc := make([]any, 0, len(f.loc)+1)
	loc = append(loc, f.loc...)
	return append(loc, name)
}

// missing builds a required-field-missing error; pydantic's input is the **parent object** itself.
func (f fieldSet) missing(name string) validationError {
	return validationError{Type: "missing", Loc: f.path(name), Msg: msgFieldRequired, Input: compactRaw(f.parent)}
}

// valueErr builds a field-level type error; pydantic's input is **the offending value** itself.
func (f fieldSet) valueErr(typ, msg, name string, value json.RawMessage) validationError {
	return validationError{Type: typ, Loc: f.path(name), Msg: msg, Input: compactRaw(value)}
}

// str parses a string field; returns whether dst was assigned. Like pydantic, no lax
// conversion from numbers/booleans; explicit null is legal only when nullable.
func (f fieldSet) str(errs *[]validationError, name string, dst *string, required, nullable bool) bool {
	v, ok := f.raw[name]
	switch {
	case !ok:
		if required {
			*errs = append(*errs, f.missing(name))
		}
		return false
	case isJSONNull(v):
		if nullable {
			return false
		}
		*errs = append(*errs, f.valueErr("string_type", msgStringType, name, v))
		return false
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		*errs = append(*errs, f.valueErr("string_type", msgStringType, name, v))
		return false
	}
	*dst = s
	return true
}

// integer parses an int field with pydantic's lax rules: integral floats, booleans, and
// whole-string integers are accepted; unparseable -> int_parsing, fractional -> int_from_float.
func (f fieldSet) integer(errs *[]validationError, name string, dst *int, required bool) {
	v, ok := f.raw[name]
	if !ok {
		if required {
			*errs = append(*errs, f.missing(name))
		}
		return
	}
	switch {
	case isJSONNull(v):
		*errs = append(*errs, f.valueErr("int_type", msgIntType, name, v))
		return
	case bytes.Equal(bytes.TrimSpace(v), []byte("true")), bytes.Equal(bytes.TrimSpace(v), []byte("false")):
		// In Python, bool is a subclass of int; pydantic's lax mode accepts it.
		*dst = 0
		if bytes.Equal(bytes.TrimSpace(v), []byte("true")) {
			*dst = 1
		}
		return
	}
	var str string
	if err := json.Unmarshal(v, &str); err == nil {
		// Python's int(str) tolerates leading/trailing whitespace and a leading sign.
		if n, err := strconv.Atoi(strings.TrimSpace(str)); err == nil {
			*dst = n
			return
		}
		*errs = append(*errs, f.valueErr("int_parsing", msgIntParsing, name, v))
		return
	}
	var num json.Number
	if err := json.Unmarshal(v, &num); err == nil {
		if n, err := num.Int64(); err == nil {
			*dst = int(n)
			return
		}
		if fl, err := num.Float64(); err == nil {
			if fl == math.Trunc(fl) {
				*dst = int(fl)
				return
			}
			*errs = append(*errs, f.valueErr("int_from_float", msgIntFromFloat, name, v))
			return
		}
	}
	*errs = append(*errs, f.valueErr("int_type", msgIntType, name, v))
}

// number parses a float field (LLMInfo.temperature). Lax mode likewise accepts numeric-form
// strings and booleans (Python's bool is a subclass of int).
func (f fieldSet) number(errs *[]validationError, name string, dst *float64, required bool) {
	v, ok := f.raw[name]
	if !ok {
		if required {
			*errs = append(*errs, f.missing(name))
		}
		return
	}
	if isJSONNull(v) {
		*errs = append(*errs, f.valueErr("float_type", msgFloatType, name, v))
		return
	}
	var str string
	if err := json.Unmarshal(v, &str); err == nil {
		if fl, err := strconv.ParseFloat(strings.TrimSpace(str), 64); err == nil {
			*dst = fl
			return
		}
		*errs = append(*errs, f.valueErr("float_parsing", msgFloatParsing, name, v))
		return
	}
	trimmed := bytes.TrimSpace(v)
	if bytes.Equal(trimmed, []byte("true")) || bytes.Equal(trimmed, []byte("false")) {
		*dst = 0
		if bytes.Equal(trimmed, []byte("true")) {
			*dst = 1
		}
		return
	}
	var num json.Number
	if err := json.Unmarshal(v, &num); err == nil {
		if fl, err := num.Float64(); err == nil {
			*dst = fl
			return
		}
	}
	*errs = append(*errs, f.valueErr("float_type", msgFloatType, name, v))
}

// boolean parses a boolean field. pydantic's lax mode accepts true/false, 1/0 and
// "true"/"false"/"yes"/"no"/"on"/"off" (case-insensitive); anything else produces bool_parsing.
func (f fieldSet) boolean(errs *[]validationError, name string, dst *bool) {
	v, ok := f.raw[name]
	if !ok {
		return
	}
	if isJSONNull(v) {
		*errs = append(*errs, f.valueErr("bool_type", msgBoolType, name, v))
		return
	}
	var str string
	if err := json.Unmarshal(v, &str); err == nil {
		switch strings.ToLower(strings.TrimSpace(str)) {
		case "true", "yes", "on", "1":
			*dst = true
			return
		case "false", "no", "off", "0":
			*dst = false
			return
		}
		*errs = append(*errs, f.valueErr("bool_parsing", msgBoolParsing, name, v))
		return
	}
	trimmed := bytes.TrimSpace(v)
	switch {
	case bytes.Equal(trimmed, []byte("true")):
		*dst = true
		return
	case bytes.Equal(trimmed, []byte("false")):
		*dst = false
		return
	case bytes.Equal(trimmed, []byte("0")):
		*dst = false
		return
	case bytes.Equal(trimmed, []byte("1")):
		*dst = true
		return
	}
	*errs = append(*errs, f.valueErr("bool_parsing", msgBoolParsing, name, v))
}

// literal parses a literal field (provider / schema_format); msg and ctx are built from the
// candidate set, e.g. "Input should be 'markdown' or 'm_schema'".
func (f fieldSet) literal(errs *[]validationError, name string, dst *string, options []string, required bool) bool {
	quoted := make([]string, 0, len(options))
	for _, o := range options {
		quoted = append(quoted, "'"+o+"'")
	}
	expected := strings.Join(quoted, " or ")

	v, ok := f.raw[name]
	if !ok {
		if required {
			*errs = append(*errs, f.missing(name))
		}
		return false
	}
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		for _, o := range options {
			if s == o {
				*dst = s
				return true
			}
		}
	}
	e := f.valueErr("literal_error", "Input should be "+expected, name, v)
	e.Ctx = map[string]any{"expected": expected}
	*errs = append(*errs, e)
	return false
}

// dict parses an object field (LLMInfo.extra_body); nullable corresponds to `dict | None`.
func (f fieldSet) dict(errs *[]validationError, name string, dst *map[string]any, nullable bool) {
	v, ok := f.raw[name]
	if !ok {
		return
	}
	if isJSONNull(v) {
		if nullable {
			return
		}
		*errs = append(*errs, f.valueErr("dict_type", msgDictType, name, v))
		return
	}
	var m map[string]any
	if err := json.Unmarshal(v, &m); err != nil {
		*errs = append(*errs, f.valueErr("dict_type", msgDictType, name, v))
		return
	}
	*dst = m
}

// stringList parses a list[str] field (mongo.file_ids); element-error locations carry the index.
func (f fieldSet) stringList(errs *[]validationError, name string, dst *[]string, nullable bool) {
	v, ok := f.raw[name]
	if !ok {
		return
	}
	if isJSONNull(v) {
		if nullable {
			return
		}
		*errs = append(*errs, f.valueErr("list_type", msgListType, name, v))
		return
	}
	var items []json.RawMessage
	if err := json.Unmarshal(v, &items); err != nil {
		*errs = append(*errs, f.valueErr("list_type", msgListType, name, v))
		return
	}
	out := make([]string, 0, len(items))
	for i, item := range items {
		var s string
		if err := json.Unmarshal(item, &s); err != nil {
			e := f.valueErr("string_type", msgStringType, name, item)
			e.Loc = append(e.Loc, i)
			*errs = append(*errs, e)
			return
		}
		out = append(out, s)
	}
	*dst = out
}

// isJSONNull reports whether the raw fragment is JSON null.
func isJSONNull(v json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(v), []byte("null"))
}

// compactRaw strips insignificant whitespace from a raw fragment: pydantic's input is
// re-serialized (no original indentation); keeping raw bytes preserves key order.
func compactRaw(v json.RawMessage) json.RawMessage {
	if len(v) == 0 {
		return v
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, v); err != nil {
		return v
	}
	return buf.Bytes()
}

// objectFields parses the raw request into a field table. Non-object input yields
// model_attributes_type (not dict_type): FastAPI validates bodies with model_validate (python mode).
func objectFields(data json.RawMessage) (map[string]json.RawMessage, *validationError) {
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, &validationError{
			Type: "model_attributes_type", Loc: []any{}, Msg: msgModelAttrs, Input: compactRaw(data),
		}
	}
	return raw, nil
}

// pyTagString renders a union tag like Python's str(value): strings unquoted, None/True/False,
// numbers verbatim (known partial coverage: `1e2` stays "1e2" where Python's str(100.0) is
// "100.0"), arrays/objects via pyRepr.
func pyTagString(v json.RawMessage) string {
	trimmed := bytes.TrimSpace(v)
	if bytes.Equal(trimmed, []byte("null")) {
		// null must be checked first: json.Unmarshal("null", &s) silently succeeds with the
		// zero value, which would wrongly render the tag as "".
		return "None"
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err == nil {
		return s
	}
	switch {
	case bytes.Equal(trimmed, []byte("true")):
		return "True"
	case bytes.Equal(trimmed, []byte("false")):
		return "False"
	}
	return pyRepr(trimmed)
}

// pyRepr renders a value like Python's repr/str: None/True/False, quoted strings, and
// containers in document order with ", " separators (str({"a": 1}) == "{'a': 1}").
// Top-level strings never reach here (pyTagString returns early).
func pyRepr(v json.RawMessage) string {
	trimmed := bytes.TrimSpace(v)
	if len(trimmed) == 0 {
		return ""
	}
	switch {
	case bytes.Equal(trimmed, []byte("null")):
		return "None"
	case bytes.Equal(trimmed, []byte("true")):
		return "True"
	case bytes.Equal(trimmed, []byte("false")):
		return "False"
	case trimmed[0] == '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return pyStringRepr(s)
		}
	case trimmed[0] == '[' || trimmed[0] == '{':
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		if s, ok := pyCompositeRepr(dec); ok {
			return s
		}
	}
	// Numbers: copied verbatim from the JSON source.
	return string(trimmed)
}

// pyCompositeRepr renders a list/dict into Python repr in JSON document order.
// ok=false on parse failure; the caller falls back to the original text.
func pyCompositeRepr(dec *json.Decoder) (string, bool) {
	tok, err := dec.Token()
	if err != nil {
		return "", false
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return "", false
	}
	switch delim {
	case '[':
		var parts []string
		for dec.More() {
			s, ok := pyReprValue(dec)
			if !ok {
				return "", false
			}
			parts = append(parts, s)
		}
		if _, err := dec.Token(); err != nil { // ']'
			return "", false
		}
		return "[" + strings.Join(parts, ", ") + "]", true
	case '{':
		var parts []string
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return "", false
			}
			key, ok := keyTok.(string)
			if !ok {
				return "", false
			}
			s, ok := pyReprValue(dec)
			if !ok {
				return "", false
			}
			// Object keys always use repr (quoted); values use str/repr.
			parts = append(parts, pyStringRepr(key)+": "+s)
		}
		if _, err := dec.Token(); err != nil { // '}'
			return "", false
		}
		return "{" + strings.Join(parts, ", ") + "}", true
	}
	return "", false
}

// pyReprValue reads the next JSON value and returns its Python repr.
func pyReprValue(dec *json.Decoder) (string, bool) {
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return "", false
	}
	return pyRepr(raw), true
}

// pyStringRepr renders Python's repr(str): single quotes preferred (double when the string
// contains ' but not "), with Python's escape conventions.
func pyStringRepr(s string) string {
	quote := byte('\'')
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				// Python uses \xNN for non-printables.
				fmt.Fprintf(&b, `\x%02x`, r)
				continue
			}
			if rune(quote) == r {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

// resolveUnionTag replicates pydantic's discriminated-union tag extraction. Error locs are
// relative and carry no tag (the tag itself failed): missing discriminator -> union_tag_not_found,
// unknown value -> union_tag_invalid; input is the object's raw fragment.
func resolveUnionTag(data json.RawMessage, raw map[string]json.RawMessage, tags []string, disc string) (string, *validationError) {
	tagRaw, ok := raw[disc]
	if !ok {
		return "", &validationError{
			Type: "union_tag_not_found", Loc: []any{},
			Msg:   "Unable to extract tag using discriminator '" + disc + "'",
			Input: compactRaw(data),
			Ctx:   map[string]any{"discriminator": "'" + disc + "'"},
		}
	}
	tag := pyTagString(tagRaw)
	for _, t := range tags {
		if t == tag {
			return tag, nil
		}
	}
	expected := tagListText(tags)
	return "", &validationError{
		Type: "union_tag_invalid", Loc: []any{},
		Msg: "Input tag '" + tag + "' found using '" + disc + "' does not match any of the expected tags: " +
			expected,
		Input: compactRaw(data),
		Ctx:   map[string]any{"discriminator": "'" + disc + "'", "tag": tag, "expected_tags": expected},
	}
}

// tagListText builds pydantic's expected_tags text: 'a', 'b', 'c'.
func tagListText(tags []string) string {
	quoted := make([]string, 0, len(tags))
	for _, t := range tags {
		quoted = append(quoted, "'"+t+"'")
	}
	return strings.Join(quoted, ", ")
}

// mongoDefaults is the env-var snapshot from Python class-body evaluation.
var mongoDefaults = sync.OnceValues(func() (string, string) {
	return os.Getenv("MONGODB_URI"), os.Getenv("MONGODB_BUCKET")
})

// UnmarshalJSON replicates pydantic's discriminated-union parsing and default filling.
// Error locs are relative to this object; callers add the prefix via relocate, so the same
// type serves both as the source_config field and as the request body itself.
func (c *DataSourceConfig) UnmarshalJSON(data []byte) error {
	raw, objErr := objectFields(data)
	if objErr != nil {
		return &ValidationErrors{Errors: []validationError{*objErr}}
	}

	tag, tagErr := resolveUnionTag(data, raw, dataSourceTags, "type")
	if tagErr != nil {
		return &ValidationErrors{Errors: []validationError{*tagErr}}
	}

	// Branch field errors carry the tag in loc ([<tag>, <field>]); union-level errors above do not.
	fs := fieldSet{raw: raw, parent: data, loc: []any{tag}}
	var errs []validationError

	switch tag {
	case "sqlite":
		cfg := &SqliteConfig{Type: tag}
		fs.str(&errs, "db_path", &cfg.DBPath, true, false)
		if len(errs) > 0 {
			return &ValidationErrors{Errors: errs}
		}
		c.Type, c.Config = tag, cfg

	case "mssql":
		cfg := &MssqlConfig{Type: tag, NSName: "dbo"}
		cfg.Port = 3306
		// Field order matches Python's declaration order (base-class first): error order must match.
		fs.str(&errs, "host", &cfg.Host, true, false)
		fs.integer(&errs, "port", &cfg.Port, false)
		fs.str(&errs, "username", &cfg.Username, true, false)
		fs.str(&errs, "password", &cfg.Password, true, false)
		fs.str(&errs, "db_name", &cfg.DBName, true, false)
		// ns_name has Field(alias="schema"): loc reports the input key name schema.
		fs.str(&errs, "schema", &cfg.NSName, false, false)
		fs.boolean(&errs, "encrypt", &cfg.Encrypt)
		if len(errs) > 0 {
			return &ValidationErrors{Errors: errs}
		}
		c.Type, c.Config = tag, cfg

	case "mysql", "maria", "starrocks":
		cfg := &MysqlConfig{Type: tag, ConnPoolSize: 3}
		cfg.Port = 3306
		fs.str(&errs, "host", &cfg.Host, true, false)
		fs.integer(&errs, "port", &cfg.Port, false)
		fs.str(&errs, "username", &cfg.Username, true, false)
		fs.str(&errs, "password", &cfg.Password, true, false)
		fs.str(&errs, "db_name", &cfg.DBName, true, false)
		fs.integer(&errs, "conn_pool_size", &cfg.ConnPoolSize, false)
		if len(errs) > 0 {
			return &ValidationErrors{Errors: errs}
		}
		c.Type, c.Config = tag, cfg

	case "oracle":
		cfg := &OracleConfig{Type: tag, DBName: "XEPDB1"}
		cfg.Port = 3306
		fs.str(&errs, "host", &cfg.Host, true, false)
		fs.integer(&errs, "port", &cfg.Port, false)
		fs.str(&errs, "username", &cfg.Username, true, false)
		fs.str(&errs, "password", &cfg.Password, true, false)
		fs.str(&errs, "db_name", &cfg.DBName, false, false)
		// ns_name: str | None; an explicit null is legal.
		var schema string
		if fs.str(&errs, "schema", &schema, false, true) {
			cfg.NSName = &schema
		}
		if len(errs) > 0 {
			return &ValidationErrors{Errors: errs}
		}
		c.Type, c.Config = tag, cfg

	case "postgres":
		cfg := &PostgresConfig{Type: tag, NSName: "public", ConnPoolSize: 3}
		cfg.Port = 3306
		fs.str(&errs, "host", &cfg.Host, true, false)
		fs.integer(&errs, "port", &cfg.Port, false)
		fs.str(&errs, "username", &cfg.Username, true, false)
		fs.str(&errs, "password", &cfg.Password, true, false)
		fs.str(&errs, "db_name", &cfg.DBName, true, false)
		fs.str(&errs, "schema", &cfg.NSName, false, false)
		fs.integer(&errs, "conn_pool_size", &cfg.ConnPoolSize, false)
		if len(errs) > 0 {
			return &ValidationErrors{Errors: errs}
		}
		c.Type, c.Config = tag, cfg

	case "duckdb":
		cfg := &DuckdbConfig{Type: tag}
		storeRaw, ok := raw["store"]
		switch {
		case !ok:
			errs = append(errs, fs.missing("store"))
		case !isObject(storeRaw):
			// Explicit null fares like a non-object: model_attributes_type (input is the value).
			errs = append(errs, fs.valueErr("model_attributes_type", msgModelAttrs, "store", storeRaw))
		default:
			storeFields := map[string]json.RawMessage{}
			if err := json.Unmarshal(storeRaw, &storeFields); err != nil {
				return err
			}
			st, err := decodeDuckdbStore(storeRaw, storeFields)
			if err != nil {
				var ve *ValidationErrors
				if !errors.As(err, &ve) {
					return err
				}
				errs = append(errs, relocate(ve.Errors, "duckdb", "store")...)
			} else {
				cfg.Store = st
			}
		}
		if len(errs) > 0 {
			return &ValidationErrors{Errors: errs}
		}
		c.Type, c.Config = tag, cfg
	}
	return nil
}

// isObject reports whether the raw fragment is a JSON object (null, arrays and scalars do not count).
func isObject(v json.RawMessage) bool {
	trimmed := bytes.TrimSpace(v)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

// decodeDuckdbStore replicates decoding of the store discriminated union; error locs are relative to the store object itself.
func decodeDuckdbStore(data json.RawMessage, raw map[string]json.RawMessage) (DuckdbStoreConfig, error) {
	tag, tagErr := resolveUnionTag(data, raw, duckdbStoreTags, "type")
	if tagErr != nil {
		return DuckdbStoreConfig{}, &ValidationErrors{Errors: []validationError{*tagErr}}
	}
	fs := fieldSet{raw: raw, parent: data, loc: []any{tag}}
	var errs []validationError

	switch tag {
	case "local":
		cfg := &DuckdbLocalStoreConfig{Type: tag}
		fs.str(&errs, "dir_path", &cfg.DirPath, true, false)
		if len(errs) > 0 {
			return DuckdbStoreConfig{}, &ValidationErrors{Errors: errs}
		}
		return DuckdbStoreConfig{Type: tag, Store: cfg}, nil

	case "s3":
		cfg := &DuckdbS3StoreConfig{Type: tag, Port: 9000}
		fs.str(&errs, "kid", &cfg.Kid, false, false)
		fs.str(&errs, "host", &cfg.Host, false, false)
		fs.integer(&errs, "port", &cfg.Port, false)
		fs.str(&errs, "access_key", &cfg.AccessKey, false, false)
		fs.str(&errs, "secret_key", &cfg.SecretKey, false, false)
		fs.str(&errs, "bucket", &cfg.Bucket, false, false)
		fs.str(&errs, "region", &cfg.Region, false, false)
		fs.boolean(&errs, "use_ssl", &cfg.UseSSL)
		if len(errs) > 0 {
			return DuckdbStoreConfig{}, &ValidationErrors{Errors: errs}
		}
		return DuckdbStoreConfig{Type: tag, Store: cfg}, nil

	case "mongo":
		uri, bucket := mongoDefaults()
		cfg := &DuckdbMongoStoreConfig{Type: tag, URI: uri, Bucket: bucket}
		fs.str(&errs, "uri", &cfg.URI, false, false)
		fs.str(&errs, "bucket", &cfg.Bucket, false, false)
		fs.str(&errs, "kid", &cfg.Kid, true, false)
		fs.stringList(&errs, "file_ids", &cfg.FileIDs, true)
		if len(errs) > 0 {
			return DuckdbStoreConfig{}, &ValidationErrors{Errors: errs}
		}
		return DuckdbStoreConfig{Type: tag, Store: cfg}, nil
	}

	// Unreachable: resolveUnionTag has already ruled out unknown tags.
	return DuckdbStoreConfig{}, &ValidationErrors{Errors: []validationError{{
		Type: "union_tag_invalid", Loc: []any{},
		Msg: "Input tag '" + tag + "' found using 'type' does not match any of the expected tags: " +
			tagListText(duckdbStoreTags),
		Input: compactRaw(data),
	}}}
}

// relocate prepends a loc prefix to each error: models produce locs relative to themselves,
// and each nesting level (source_config field, request body) adds its own prefix.
func relocate(errs []validationError, prefix ...string) []validationError {
	out := make([]validationError, 0, len(errs))
	for _, e := range errs {
		loc := make([]any, 0, len(prefix)+len(e.Loc))
		for _, p := range prefix {
			loc = append(loc, p)
		}
		e.Loc = append(loc, e.Loc...)
		out = append(out, e)
	}
	return out
}

// ValidationErrors corresponds to FastAPI's RequestValidationError (HTTP 422).
type ValidationErrors struct {
	Errors []validationError
}

func (e *ValidationErrors) Error() string {
	return fmt.Sprintf("validation failed for %d field(s)", len(e.Errors))
}

// LLM configuration

// LLMInfo corresponds to Python's LLMInfo.
type LLMInfo struct {
	Provider    string         `json:"provider"`
	Model       string         `json:"model"`
	APIKey      string         `json:"api_key"`
	BaseURL     string         `json:"base_url"`
	Temperature float64        `json:"temperature"`
	ExtraBody   map[string]any `json:"extra_body"`
}

// UnmarshalJSON lets LLMInfo be handled uniformly as a model field by decodeModelField
// (default filling lives in unmarshalLLMInfo).
func (i *LLMInfo) UnmarshalJSON(data []byte) error {
	info, err := unmarshalLLMInfo(data)
	if err != nil {
		return err
	}
	*i = *info
	return nil
}

// DefaultBaseURL corresponds to data_model.default_base_url.
func DefaultBaseURL() string {
	if ep := os.Getenv("AIPROXY_API_ENDPOINT"); ep != "" {
		return ep + "/v1"
	}
	return "https://api.openai.com/v1"
}

// unmarshalLLMInfo replicates LLMInfo's default-value semantics; error locs are relative to
// this object (callers relocate).
func unmarshalLLMInfo(data []byte) (*LLMInfo, error) {
	raw, objErr := objectFields(data)
	if objErr != nil {
		return nil, &ValidationErrors{Errors: []validationError{*objErr}}
	}
	info := &LLMInfo{
		Provider:    "openai",
		APIKey:      os.Getenv("AIPROXY_API_TOKEN"),
		BaseURL:     DefaultBaseURL(),
		Temperature: 0.7,
	}
	fs := fieldSet{raw: raw, parent: data}
	var errs []validationError
	fs.literal(&errs, "provider", &info.Provider, []string{"openai"}, false)
	fs.str(&errs, "model", &info.Model, true, false)
	// api_key is a SecretStr: same rules as str (explicit null is a string_type error).
	fs.str(&errs, "api_key", &info.APIKey, false, false)
	fs.str(&errs, "base_url", &info.BaseURL, false, false)
	fs.number(&errs, "temperature", &info.Temperature, false)
	fs.dict(&errs, "extra_body", &info.ExtraBody, true)
	if len(errs) > 0 {
		return nil, &ValidationErrors{Errors: errs}
	}
	return info, nil
}
