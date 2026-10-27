//go:build cgo

package ds

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/duckdb/duckdb-go/v2"
)

// CGO implementation of duckdbConn on top of duckdb-go/v2 (DuckDB 1.5.5,
// matching the Python parity environment).

// duckdbSQLConn carries a duckdb connection on top of database/sql.
type duckdbSQLConn struct {
	db *sql.DB
}

// openDuckDBConn opens a duckdb connection; empty dsn / ":memory:" means an
// in-memory database.
func openDuckDBConn(dsn string) (duckdbConn, error) {
	connector, err := duckdb.NewConnector(dsn, nil)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(connector)
	// Pool limited to 1: views registered in load_data must be visible to later
	// queries, and Python uses a single serial connection (no concurrency).
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return &duckdbSQLConn{db: db}, nil
}

// Query runs SQL and returns (columns, rows). Values are normalized by column
// type (normalizeDuckDBColumnValue), because the native Go type alone cannot
// distinguish e.g. UUID ([]byte) from BLOB.
func (c *duckdbSQLConn) Query(query string) ([]string, [][]any, error) {
	rows, err := c.db.Query(query)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	typeNames := make([]string, len(cols))
	if colTypes, err := rows.ColumnTypes(); err == nil {
		for i, ct := range colTypes {
			if ct != nil {
				typeNames[i] = ct.DatabaseTypeName()
			}
		}
	}

	out := [][]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		for i, v := range vals {
			vals[i] = normalizeDuckDBColumnValue(typeNames[i], v)
		}
		out = append(out, vals)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return cols, out, nil
}

// Exec runs a statement without returning rows.
func (c *duckdbSQLConn) Exec(query string) error {
	_, err := c.db.Exec(query)
	return err
}

func (c *duckdbSQLConn) Close() error {
	return c.db.Close()
}

// normalizeDuckDBValue is the fallback normalization by native Go type, used
// when the column type is unknown or does not need special handling.
func normalizeDuckDBValue(v any) any {
	switch x := v.(type) {
	case duckdb.Decimal:
		return formatDecimal(x)
	case duckdb.Interval:
		return formatInterval(x)
	case duckdb.UUID:
		return formatUUID(x[:])
	case *big.Int:
		if x == nil {
			return nil
		}
		return json.Number(x.String())
	default:
		return normalizeValue(v)
	}
}

// normalizeDuckDBColumnValue converts a driver value to what the Python duckdb
// driver + pydantic would emit, keyed by DuckDB column type. Divergent or
// non-obvious cases: DECIMAL keeps its scale ("1.50"), DATE/TIME/TIMESTAMP use
// Python isoformat rules, INTERVAL serializes as ISO8601 timedelta ("PT2H3M"),
// MAP keys are stringified, HUGEINT uses json.Number (unquoted in JSON).
// Known non-equivalences (see end of file): JSON columns lose key order, and
// FLOAT 1.0 serializes as 1.
func normalizeDuckDBColumnValue(typeName string, v any) any {
	if v == nil {
		return nil
	}
	switch {
	case typeName == "DATE":
		if t, ok := v.(time.Time); ok {
			return t.Format("2006-01-02")
		}
	case typeName == "TIME":
		if t, ok := v.(time.Time); ok {
			return formatTimeOfDay(t)
		}
	case typeName == "TIMESTAMP" || typeName == "TIMESTAMP_S" ||
		typeName == "TIMESTAMP_MS" || typeName == "TIMESTAMP_NS":
		if t, ok := v.(time.Time); ok {
			return formatNaiveTimestamp(t)
		}
	case typeName == "TIMESTAMP WITH TIME ZONE" || typeName == "TIMESTAMPTZ":
		if t, ok := v.(time.Time); ok {
			// Python emits TIMESTAMPTZ in the session time zone; the Go driver
			// returns UTC, so convert back to local.
			return formatAwareTimestamp(t.In(time.Local))
		}
	case typeName == "TIME WITH TIME ZONE" || typeName == "TIMETZ":
		if t, ok := v.(time.Time); ok {
			return formatTimeOfDay(t) + t.Format("-07:00")
		}
	case typeName == "UUID":
		if b, ok := v.([]byte); ok && len(b) == 16 {
			return formatUUID(b)
		}
	case typeName == "HUGEINT" || typeName == "UHUGEINT":
		// json.Number so encoding/json writes it as an unquoted number, like
		// Python's arbitrary-precision int.
		if b, ok := v.(*big.Int); ok && b != nil {
			return json.Number(b.String())
		}
	case strings.HasPrefix(typeName, "MAP("):
		if m, ok := v.(duckdb.Map); ok {
			// pydantic stringifies dict keys: {1: 2} -> {"1": 2}
			out := make(map[string]any, len(m))
			for k, val := range m {
				out[pythonStr(k)] = val
			}
			return out
		}
	}
	return normalizeDuckDBValue(v)
}

// pythonStr mirrors Python's str(x) (for stringified MAP keys).
func pythonStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case nil:
		return "None"
	case float32:
		return strconv.FormatFloat(float64(x), 'g', -1, 32)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// formatNaiveTimestamp reproduces Python's datetime.isoformat(): microseconds
// padded to 6 digits when nonzero, omitted when zero.
func formatNaiveTimestamp(t time.Time) string {
	base := t.Format("2006-01-02T15:04:05")
	if t.Nanosecond() != 0 {
		return fmt.Sprintf("%s.%06d", base, t.Nanosecond()/1000)
	}
	return base
}

// formatTimeOfDay reproduces Python's datetime.time.isoformat().
func formatTimeOfDay(t time.Time) string {
	base := t.Format("15:04:05")
	if t.Nanosecond() != 0 {
		return fmt.Sprintf("%s.%06d", base, t.Nanosecond()/1000)
	}
	return base
}

// formatAwareTimestamp serializes a timezone-aware datetime in RFC3339 style,
// matching pydantic's output.
func formatAwareTimestamp(t time.Time) string {
	base := formatNaiveTimestamp(t)
	_, offset := t.Zone()
	if offset == 0 {
		return base + "Z"
	}
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	return fmt.Sprintf("%s%s%02d:%02d", base, sign, offset/3600, (offset%3600)/60)
}

// formatDecimal reproduces str(decimal.Decimal): scale digits are always kept
// (duckdb-go's own Decimal.String() would render Decimal("0.00") as "0").
func formatDecimal(d duckdb.Decimal) string {
	if d.Value == nil {
		return "0"
	}
	neg := d.Value.Sign() < 0
	digits := new(big.Int).Abs(d.Value).String()
	scale := int(d.Scale)
	var out string
	if scale <= 0 {
		out = digits + strings.Repeat("0", -scale)
	} else {
		if len(digits) <= scale {
			digits = strings.Repeat("0", scale-len(digits)+1) + digits
		}
		out = digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
	}
	if neg {
		out = "-" + out
	}
	return out
}

// formatInterval reproduces Python's ISO8601 timedelta serialization. INTERVAL
// months are converted at 30 days per month (as duckdb itself does for
// timedelta, which cannot express months).
func formatInterval(i duckdb.Interval) string {
	totalDays := int64(i.Days) + int64(i.Months)*30
	micros := i.Micros
	neg := totalDays < 0 || micros < 0
	if totalDays < 0 {
		totalDays = -totalDays
	}
	if micros < 0 {
		micros = -micros
	}
	hours := micros / 3_600_000_000
	micros -= hours * 3_600_000_000
	minutes := micros / 60_000_000
	micros -= minutes * 60_000_000
	seconds := micros / 1_000_000
	micros -= seconds * 1_000_000

	var b strings.Builder
	if neg {
		b.WriteString("-")
	}
	b.WriteString("P")
	if totalDays != 0 {
		fmt.Fprintf(&b, "%dD", totalDays)
	}
	if hours != 0 || minutes != 0 || seconds != 0 || micros != 0 {
		b.WriteString("T")
		if hours != 0 {
			fmt.Fprintf(&b, "%dH", hours)
		}
		if minutes != 0 {
			fmt.Fprintf(&b, "%dM", minutes)
		}
		if seconds != 0 || micros != 0 {
			if micros != 0 {
				frac := strings.TrimRight(fmt.Sprintf("%06d", micros), "0")
				fmt.Fprintf(&b, "%d.%sS", seconds, frac)
			} else {
				fmt.Fprintf(&b, "%dS", seconds)
			}
		}
	}
	if b.String() == "P" || b.String() == "-P" {
		return "P0D"
	}
	return b.String()
}

// formatUUID renders a 16-byte UUID in canonical string form.
func formatUUID(b []byte) string {
	if len(b) != 16 {
		return string(b)
	}
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// duckdbLoadExtension runs LOAD; duckdb auto-installs missing extensions (same
// as Python's default).
func duckdbLoadExtension(conn duckdbConn, name string) error {
	return conn.Exec("LOAD " + name)
}

// Note: duckdb.Error's text needs no unwrapping — the Go driver returns the
// engine message directly, like Python's str(duckdb.Error).
//
// Known non-equivalences with Python (driver-level, not configurable):
//  1. JSON columns: Python materializes them as str (key order preserved); the
//     Go driver decodes to map/[]any and key order is lost. Affects direct
//     SELECTs of JSON columns (the metadata path sidesteps this via CAST).
//  2. FLOAT columns: Python JSON gives 1.0; Go's encoding/json emits 1. Fix
//     belongs in the API serializer, not here.
