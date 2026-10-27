package sqlg

import (
	"errors"
	"strconv"
	"strings"
)

// ErrUnsupportedDialect mirrors Python's `raise ValueError(f"Unsupported dialect by sqlglot: {dialect}")`.
var ErrUnsupportedDialect = errors.New("unsupported dialect by sqlglot")

// AggDistinctValuesSQL mirrors DataSourceBase.agg_distinct_values_sql: per-column
// `SELECT '<col>' AS name, <jsonAggFunc>(<col>) AS examples FROM (SELECT DISTINCT ...
// LIMIT <n>) AS t` chained with UNION ALL.
func AggDistinctValuesSQL(dialect string, tableName string, columns []string, limit int, jsonAggFunc string) (string, error) {
	if !HasDialect(dialect) {
		return "", ErrUnsupportedDialect
	}
	parts := make([]string, 0, len(columns))
	for _, col := range columns {
		parts = append(parts, distinctValuesSelect(dialect, tableName, col, limit, jsonAggFunc))
	}
	return strings.Join(parts, " UNION ALL "), nil
}

// distinctValuesSelect renders the example-value aggregation SELECT for a single column.
func distinctValuesSelect(dialect, tableName, col string, limit int, jsonAggFunc string) string {
	tbl := quoteIdentIfNeeded(dialect, tableName)
	c := quoteIdentIfNeeded(dialect, col)

	inner := "SELECT DISTINCT " + c + " FROM " + tbl + " WHERE NOT " + c + " IS NULL"
	alias := " AS t"
	if usesFetchFirst(dialect) {
		inner += " FETCH FIRST " + strconv.Itoa(limit) + " ROWS ONLY"
		// Oracle renders the subquery alias without AS
		alias = " t"
	} else {
		inner += " LIMIT " + strconv.Itoa(limit)
	}

	return "SELECT '" + col + "' AS name, " + jsonAggFunc + "(" + c + ") AS examples FROM (" + inner + ")" + alias
}

// quoteIdentIfNeeded replicates sqlglot's identifier-quoting policy: quote only for
// reserved words or special characters.
func quoteIdentIfNeeded(dialect, name string) string {
	if !needsQuoting(name) {
		return name
	}
	switch dialect {
	case "mysql", "starrocks", "hive", "spark", "spark2", "doris", "clickhouse":
		return "`" + name + "`"
	case "tsql", "fabric":
		return "[" + name + "]"
	default:
		return `"` + name + `"`
	}
}

// needsQuoting reports whether an identifier needs quoting: not [A-Za-z_][A-Za-z0-9_]* or hits a reserved word.
func needsQuoting(name string) bool {
	if name == "" {
		return true
	}
	for i, r := range name {
		isAlpha := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_'
		isDigit := r >= '0' && r <= '9'
		if i == 0 && !isAlpha {
			return true
		}
		if !isAlpha && !isDigit {
			return true
		}
	}
	return reservedWords[strings.ToUpper(name)]
}

// reservedWords is a minimal subset of sqlglot's reserved-word table.
var reservedWords = map[string]bool{
	"SELECT": true, "FROM": true, "WHERE": true, "GROUP": true, "ORDER": true,
	"BY": true, "HAVING": true, "LIMIT": true, "JOIN": true, "ON": true,
	"AND": true, "OR": true, "NOT": true, "NULL": true, "IN": true,
	"IS": true, "AS": true, "TABLE": true, "INDEX": true, "KEY": true,
	"PRIMARY": true, "FOREIGN": true, "UNION": true, "ALL": true, "DISTINCT": true,
}
