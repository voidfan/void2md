package sqlg

import (
	"testing"
)

// Ports tests/unit/core/sql_inspection/check.py (TestSQLCheck).

func TestIsQueryWithSelectStatement(t *testing.T) {
	c := NewSQLCheck("mysql")
	got, err := c.IsQuery("SELECT * FROM users")
	if err != nil {
		t.Fatalf("IsQuery returned error: %v", err)
	}
	if !got {
		t.Fatalf("SELECT should be judged a query, got false")
	}
}

func TestIsQueryWithInsertStatement(t *testing.T) {
	c := NewSQLCheck("mysql")
	got, err := c.IsQuery("INSERT INTO users (name) VALUES ('Alice')")
	if err != nil {
		t.Fatalf("IsQuery returned error: %v", err)
	}
	if got {
		t.Fatalf("INSERT should not be judged a query, got true")
	}
}

// Covers IsQuery's Expression branch with a hand-built AST (no parser).
func TestIsQueryWithExpression(t *testing.T) {
	e := &Select{
		Expressions: []Expression{&Star{}},
		From:        NewTable("users"),
	}
	c := NewSQLCheck("mysql")
	got, err := c.IsQuery(Expression(e))
	if err != nil {
		t.Fatalf("IsQuery returned error: %v", err)
	}
	if !got {
		t.Fatalf("Select expression should be judged a query, got false")
	}
}

func TestSyntaxValidWithCorrectSQL(t *testing.T) {
	c := NewSQLCheck("mysql")
	got, err := c.SyntaxValid("SELECT id, name FROM users WHERE age > 18")
	if err != nil {
		t.Fatalf("SyntaxValid returned error: %v", err)
	}
	if got == nil {
		t.Fatalf("SyntaxValid should return an expression, got nil")
	}
}

func TestSyntaxValidWithIncorrectSQL(t *testing.T) {
	c := NewSQLCheck("mysql")
	if _, err := c.SyntaxValid("SELECT FROM WHERE"); err == nil {
		t.Fatalf("SQL with a syntax error should return an error, got none")
	}
}

func TestMySQLDialect(t *testing.T) {
	c := NewSQLCheck("mysql")
	got, err := c.IsQuery("SELECT * FROM users LIMIT 10")
	if err != nil {
		t.Fatalf("IsQuery returned error: %v", err)
	}
	if !got {
		t.Fatalf("SELECT ... LIMIT 10 should be judged a query, got false")
	}
}

func TestPostgresDialect(t *testing.T) {
	c := NewSQLCheck("postgres")
	got, err := c.IsQuery("SELECT * FROM users LIMIT 10")
	if err != nil {
		t.Fatalf("IsQuery returned error: %v", err)
	}
	if !got {
		t.Fatalf("SELECT ... LIMIT 10 should be judged a query, got false")
	}
}
