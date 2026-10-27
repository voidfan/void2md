package sqlg

import "strings"

// FormatSQL mirrors the sqlglot transpile behind `POST /data_source/sql_format`
// (pretty, indent=4, pad=4, joined with "\n\n"). ok=false means the dialect is
// unrecognized (caller returns input unchanged); parse errors must bubble up as 500.
func FormatSQL(sql, dialect string) (string, bool, error) {
	if !HasDialect(dialect) {
		return sql, false, nil
	}
	exprs, err := ParseStatements(sql, dialect)
	if err != nil {
		return "", true, err
	}
	parts := make([]string, 0, len(exprs))
	d := GetDialect(dialect)
	for _, e := range exprs {
		parts = append(parts, d.Pretty(e))
	}
	return strings.Join(parts, "\n\n"), true, nil
}

// FormatExpr mirrors `expression.sql(dialect)` (compact rendering).
func FormatExpr(e Expression, dialect string) string {
	return e.SQL(GetDialect(dialect))
}

// ParseOneOrDefault mirrors structured's "parse with the dialect first, fall back to the default dialect on ParseError" pattern.
func ParseOneOrDefault(sql, dialect string) (Expression, error) {
	if HasDialect(dialect) {
		if e, err := ParseOne(sql, dialect); err == nil {
			return e, nil
		}
	}
	return ParseOne(sql, "")
}

// IsQueryExpr mirrors `isinstance(expression, exp.Query)`; Values and Paren do not count.
func IsQueryExpr(e Expression) bool {
	switch e.(type) {
	case *Select, *Union, *Subquery, *unionTail:
		return true
	}
	return false
}
