package sqlg

import "strings"

// SQLCheck mirrors Python's SQLCheck (structured/core/sql_inspection/check.py).
type SQLCheck struct {
	Dialect string
}

// NewSQLCheck mirrors SQLCheck(dialect).
func NewSQLCheck(dialect string) *SQLCheck { return &SQLCheck{Dialect: dialect} }

// IsQuery mirrors SQLCheck.is_query. Known dialects parse (retrying with the default
// dialect on ParseError); unknown dialects just check startswith("SELECT").
// Accepts a string or an Expression.
func (c *SQLCheck) IsQuery(sqlOrExp any) (bool, error) {
	switch v := sqlOrExp.(type) {
	case string:
		if !HasDialect(c.Dialect) {
			return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(v)), "SELECT"), nil
		}
		e, err := ParseOne(v, c.Dialect)
		if err != nil {
			e, err = ParseOne(v, "")
			if err != nil {
				return false, err
			}
		}
		return IsQueryExpr(e), nil
	case Expression:
		return IsQueryExpr(v), nil
	default:
		return false, nil
	}
}

// SyntaxValid mirrors SQLCheck.syntax_valid; no default-dialect fallback.
func (c *SQLCheck) SyntaxValid(sql string) (Expression, error) {
	return ParseOne(sql, c.Dialect)
}
