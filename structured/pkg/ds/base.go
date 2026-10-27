// Package ds is the data-source layer: driver-agnostic metadata, value-example
// and raw-SQL logic shared by every SQL/DuckDB data source (Go port of the
// Python structured/core/data_source modules).
package ds

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"void2md/structured/pkg/metadata"
	"void2md/structured/pkg/sqlg"
	"void2md/structured/pkg/sqlutil"
)

// DataSource is the interface every data source implements.
type DataSource interface {
	// Dialect returns the sqlglot dialect name.
	Dialect() string
	// StringTypes returns the set of string types (case sensitive).
	StringTypes() map[string]bool
	// JSONArrayAggFunc returns the JSON array aggregate function name.
	JSONArrayAggFunc() string
	// ConnTest reports reachability; the error carries what Python would raise.
	ConnTest(ctx context.Context) (bool, error)
	ServerVersion(ctx context.Context) (*metadata.DBServerVersion, error)
	Metadata(ctx context.Context) (*metadata.DatabaseMetadata, error)
	MetadataWithValueExamples(ctx context.Context, valueNum, valueStrMaxLength int) (*metadata.DatabaseMetadata, error)
	// ExecuteRawSQL returns (cols, rows, err).
	ExecuteRawSQL(ctx context.Context, sql string) ([]string, [][]any, *metadata.SQLException)
	AggDistinctValuesSQL(t *metadata.DBTable, limit int) (string, error)
}

// baseDS holds the metadata / value-example logic shared by every driver.
//
// Python dicts preserve column insertion order; Go maps do not, so the order is
// recorded separately here while metadata is parsed.
type baseDS struct {
	orderMu     sync.Mutex
	columnOrder map[string][]string
}

func (b *baseDS) recordColumnOrder(table string, order []string) {
	b.orderMu.Lock()
	defer b.orderMu.Unlock()
	if b.columnOrder == nil {
		b.columnOrder = map[string][]string{}
	}
	b.columnOrder[table] = order
}

// orderedColumnsFallback iterates the column map when the order is unknown
// (nondeterministic; last resort only).
func (b *baseDS) orderedColumnsFallback(t *metadata.DBTable) []string {
	out := make([]string, 0, len(t.Columns))
	for name := range t.Columns {
		out = append(out, name)
	}
	return out
}

// connTestViaSelect1 runs "SELECT 1" and returns false on error.
func (b *baseDS) connTestViaSelect1(ctx context.Context, ds DataSource, sql string) (bool, error) {
	_, _, err := ds.ExecuteRawSQL(ctx, sql)
	if err != nil {
		return false, nil
	}
	return true, nil
}

// --- Version parsing ---

// versionPattern is the base version regex. The Postgres override lives in
// postgres.go.
var versionPattern = regexp.MustCompile(`^(?:[vV])?(\d+)\.(\d+)(?:\.(\d+))?`)

// parseServerVersion parses a version string; a nil pattern means versionPattern.
func parseServerVersion(versionStr string, pattern *regexp.Regexp) (*metadata.DBServerVersion, error) {
	if pattern == nil {
		pattern = versionPattern
	}
	m := pattern.FindStringSubmatch(versionStr)
	if m == nil {
		return nil, fmt.Errorf("Invalid version string format: %s", versionStr)
	}
	// Missing minor is an error (Python raises TypeError here).
	if m[1] == "" {
		return nil, fmt.Errorf("Invalid version string format: %s", versionStr)
	}
	major, err := strconv.Atoi(m[1])
	if err != nil {
		return nil, fmt.Errorf("Invalid version string format: %s", versionStr)
	}
	if m[2] == "" {
		return nil, fmt.Errorf("Invalid version string format: %s", versionStr)
	}
	minor, err := strconv.Atoi(m[2])
	if err != nil {
		return nil, fmt.Errorf("Invalid version string format: %s", versionStr)
	}
	v := &metadata.DBServerVersion{Major: major, Minor: minor}
	if m[3] != "" {
		patch, err := strconv.Atoi(m[3])
		if err != nil {
			return nil, fmt.Errorf("Invalid version string format: %s", versionStr)
		}
		v.Patch = &patch
	}
	return v, nil
}

// --- Metadata parsing ---

// buildMetadataFromRows unmarshals the tables JSON from rows[0][idx] (idx is 1
// for most drivers, 0 for sqlite) and records column order.
func (b *baseDS) buildMetadataFromRows(dbName string, rows [][]any, idx int) (*metadata.DatabaseMetadata, error) {
	if len(rows) == 0 {
		return metadata.NewDatabaseMetadata(dbName), nil
	}
	if idx >= len(rows[0]) {
		return metadata.NewDatabaseMetadata(dbName), nil
	}
	rawVal := rows[0][idx]
	rawStr, ok := asString(rawVal)
	if !ok || rawStr == "" {
		return metadata.NewDatabaseMetadata(dbName), nil
	}
	tables := []*metadata.DBTable{}
	if err := json.Unmarshal([]byte(rawStr), &tables); err != nil {
		return nil, err
	}
	for _, t := range tables {
		b.recordColumnOrder(t.Name, t.ColumnOrder)
	}
	return &metadata.DatabaseMetadata{Name: dbName, Comment: "", Tables: tables}, nil
}

// orderedColumns returns the table's insertion column order.
func (b *baseDS) orderedColumns(t *metadata.DBTable) []string {
	if len(t.ColumnOrder) == len(t.Columns) && len(t.ColumnOrder) > 0 {
		return t.ColumnOrder
	}
	b.orderMu.Lock()
	order, ok := b.columnOrder[t.Name]
	b.orderMu.Unlock()
	if ok && len(order) == len(t.Columns) {
		return order
	}
	return b.orderedColumnsFallback(t)
}

// asString converts a raw driver value to a string.
func asString(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", false
	case string:
		return x, true
	case []byte:
		return string(x), true
	default:
		return sqlutil.ConvertValue2Str(v), true
	}
}

// --- Value examples ---

// metadataWithValueExamples is the shared MetadataWithValueExamples
// implementation. Parse failures propagate as errors (matching Python's
// JSONDecodeError / KeyError / IndexError bubbling out as a 400).
func (b *baseDS) metadataWithValueExamples(
	ctx context.Context, ds DataSource, dbName string, rowsIdx int, valueNum, valueStrMaxLength int,
) (*metadata.DatabaseMetadata, error) {
	dbMetadata, err := ds.Metadata(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range dbMetadata.Tables {
		sql, err := ds.AggDistinctValuesSQL(t, valueNum)
		if err != nil {
			return nil, err
		}
		_, rows, sqlErr := ds.ExecuteRawSQL(ctx, sql)
		if sqlErr != nil {
			// Python ignores the error here; rows is empty and the loop is skipped.
			rows = nil
		}
		for _, row := range rows {
			if len(row) < 2 {
				continue
			}
			colName := sqlutil.ConvertValue2Str(row[0])
			if row[1] == nil {
				continue
			}
			exRaw := sqlutil.ConvertValue2Str(row[1])
			if exRaw == "" {
				continue
			}
			var examples []any
			if err := json.Unmarshal([]byte(exRaw), &examples); err != nil {
				return nil, fmt.Errorf("JSONDecodeError: %v", err)
			}
			strExamples := make([]any, 0, len(examples))
			for _, v := range examples {
				strExamples = append(strExamples, sqlutil.TruncateText(
					sqlutil.ConvertValue2Str(v), valueStrMaxLength, "...",
				))
			}
			col, exists := t.Columns[colName]
			if !exists {
				return nil, fmt.Errorf("KeyError: %q", colName)
			}
			col.Examples = strExamples
			// Empty list is an error (Python raises IndexError on [0]).
			if len(strExamples) == 0 {
				return nil, fmt.Errorf("IndexError: list index out of range")
			}
			first, _ := strExamples[0].(string)
			if ds.StringTypes()[col.Type] &&
				!sqlutil.IsValidUUID(first) &&
				!sqlutil.IsNumber(first) &&
				!sqlutil.IsDate(first) &&
				!sqlutil.IsEmail(first) {
				col.ValueIndex = true
			}
		}
	}
	return dbMetadata, nil
}

// aggDistinctValuesSQLBase is the default AggDistinctValuesSQL implementation.
func (b *baseDS) aggDistinctValuesSQLBase(ds DataSource, t *metadata.DBTable, limit int) (string, error) {
	cols := b.orderedColumns(t)
	return sqlg.AggDistinctValuesSQL(ds.Dialect(), t.Name, cols, limit, ds.JSONArrayAggFunc())
}

// --- Metadata SQL template substitution ---

// renderTemplate substitutes literal placeholders in metadata SQL templates
// (Jinja-style {{db_name}}/{{schema}} and str.format-style {schema}/{db_name}).
func renderTemplate(tpl string, pairs ...string) string {
	out := tpl
	for i := 0; i+1 < len(pairs); i += 2 {
		out = strings.ReplaceAll(out, pairs[i], pairs[i+1])
	}
	return out
}
