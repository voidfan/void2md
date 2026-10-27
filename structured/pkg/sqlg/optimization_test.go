package sqlg

import (
	"strings"
	"testing"
)

// Ports tests/unit/core/sql_inspection/optimization.py (TestSQLOptimization). Python's
// async arun is tested via the synchronous Run / RunExpr counterparts.

// firstSelect returns the first Select in the tree (Python: result.find(exp.Select)).
func firstSelect(t *testing.T, e Expression) *Select {
	t.Helper()
	for _, s := range FindAllOf[*Select](e, true) {
		return s
	}
	t.Fatalf("no Select node in the tree")
	return nil
}

func TestCteToSubquery(t *testing.T) {
	sql := `
              WITH cte1 AS (SELECT id, name FROM users WHERE age > 18)
              SELECT *
              FROM cte1
              WHERE name LIKE 'A%'
              `
	e, err := ParseOne(sql, "")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	result := CteToSubquery(e)
	out := result.SQL(GetDialect(""))

	if strings.Contains(out, "WITH") {
		t.Fatalf("CTE should have been replaced with a subquery, but output still contains WITH: %s", out)
	}
	if !strings.Contains(out, "users") {
		t.Fatalf("result should still contain users: %s", out)
	}
	if !strings.Contains(out, "age > 18") {
		t.Fatalf("result should still contain age > 18: %s", out)
	}
}

func TestOptimizeInLimitSubquery(t *testing.T) {
	sql := `
        SELECT *
        FROM orders
        WHERE user_id IN (SELECT id FROM users LIMIT 10)
        `
	e, err := ParseOne(sql, "")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	result := OptimizeInLimitSubquery(e)

	// Python: subquery = result.find(exp.In).args["query"]
	ins := FindAllOf[*In](result, true)
	if len(ins) == 0 {
		t.Fatalf("no IN clause found in the result")
	}
	if ins[0].Query == nil {
		t.Fatalf("IN clause's query is nil; it should have been rewritten into a subquery")
	}
}

func TestFixMissingGroupByWhenAggFuncCase1(t *testing.T) {
	e, err := ParseOne("SELECT a, b FROM x GROUP BY a", "")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	result := FixMissingGroupByWhenAggFunc(e)

	sel := firstSelect(t, result)
	if sel.Group == nil {
		t.Fatalf("GROUP BY clause should not be nil")
	}
	if got := len(sel.Group.Expressions); got != 2 {
		t.Fatalf("GROUP BY should be completed to 2 items, got %d", got)
	}
}

func TestFixMissingGroupByWhenAggFuncCase2(t *testing.T) {
	e, err := ParseOne("SELECT a, COUNT(b) FROM x", "")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	result := FixMissingGroupByWhenAggFunc(e)

	sel := firstSelect(t, result)
	if sel.Group == nil {
		t.Fatalf("GROUP BY a should have been added automatically, still nil")
	}
}

func TestFixMissingGroupByWhenAggFuncCase3(t *testing.T) {
	e, err := ParseOne("SELECT a FROM x ORDER BY MAX(b)", "")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	result := FixMissingGroupByWhenAggFunc(e)

	sel := firstSelect(t, result)
	if sel.Group == nil {
		t.Fatalf("GROUP BY a should have been added automatically, still nil")
	}
}

func TestArunWithStringSQL(t *testing.T) {
	o := NewSQLOptimization("mysql", 5)
	limit := 100
	got, err := o.Run("SELECT id, name FROM users", nil, &limit)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(got, "LIMIT 100") {
		t.Fatalf("result should contain LIMIT 100: %s", got)
	}
}

func TestArunWithExpression(t *testing.T) {
	o := NewSQLOptimization("mysql", 5)
	e, err := ParseOne("SELECT id, name FROM users", "")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	limit := 50
	got, err := o.RunExpr(e, nil, &limit)
	if err != nil {
		t.Fatalf("RunExpr returned error: %v", err)
	}
	if !strings.Contains(got, "LIMIT 50") {
		t.Fatalf("result should contain LIMIT 50: %s", got)
	}
}

// MySQL 5.x: CTEs must be converted to subqueries.
func TestMySQLCTEHandlingOldVersion(t *testing.T) {
	o := NewSQLOptimization("mysql", 5)
	sql := `
              WITH cte AS (SELECT id FROM users)
              SELECT *
              FROM cte
              `
	got, err := o.Run(sql, nil, nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if strings.Contains(got, "WITH") {
		t.Fatalf("under MySQL 5.x the CTE should be converted to a subquery, but output still contains WITH: %s", got)
	}
}

// MySQL 8.0+: CTEs are preserved.
func TestMySQLCTEHandlingNewVersion(t *testing.T) {
	o := NewSQLOptimization("mysql", 8)
	sql := `
              WITH cte AS (SELECT id FROM users)
              SELECT *
              FROM cte
                       join b
                            on cte.id = b.id
              `
	got, err := o.Run(sql, nil, nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(got, "WITH") {
		t.Fatalf("under MySQL 8.0+ the CTE should be preserved, but it is missing: %s", got)
	}
}

func TestOptimizationWithSchema(t *testing.T) {
	o := NewSQLOptimization("postgres", 13)
	schema := &SchemaInput{Tables: []*SchemaTable{
		{Parts: []string{"users"}, Cols: []SchemaCol{{"id", "INT"}, {"name", "VARCHAR"}}},
		{Parts: []string{"orders"}, Cols: []SchemaCol{{"id", "INT"}, {"user_id", "INT"}}},
	}}
	got, err := o.Run(
		"SELECT u.name, COUNT(o.id) FROM users u JOIN orders o ON u.id = o.user_id",
		schema, nil,
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got == "" {
		t.Fatalf("optimization with a schema should not return an empty string")
	}
}
