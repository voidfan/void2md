package sqlg

import (
	"strings"
)

// Expression is the common interface for all SQL AST nodes, shaped to match sqlglot's
// exp.Expression: a parent-pointer tree that rules may rewrite in place.
type Expression interface {
	// SQL renders the node as compact SQL text for the given dialect.
	SQL(d *Dialect) string
	// Copy deep-copies the node (excluding parent).
	Copy() Expression
	// Children returns child nodes in rendering order.
	Children() []Expression
	// SetChildren replaces the existing children (safest when the length matches).
	SetChildren([]Expression)
	Parent() Expression
	SetParent(Expression)
	// Type returns the annotate_types annotation (nil when unannotated).
	Type() *DataType
	SetType(*DataType)
}

// base provides the shared parent-pointer and type implementation.
type base struct {
	parent Expression
	// typ mirrors Expression._type; sqlglot's __deepcopy__ copies it, so every Copy()
	// must carry it over or simplify's is_type(...) decisions change.
	typ *DataType
}

func (b *base) Parent() Expression     { return b.parent }
func (b *base) SetParent(p Expression) { b.parent = p }
func (b *base) Type() *DataType        { return b.typ }
func (b *base) SetType(t *DataType)    { b.typ = t }

// IsTypeOf mirrors Expression.is_type(*types). sqlglot compares DataType enums; sqlg
// compares normalized base type names. Empty types returns false.
func IsTypeOf(e Expression, types ...string) bool {
	if isNilExpr(e) || len(types) == 0 {
		return false
	}
	t := e.Type()
	if t == nil {
		return false
	}
	for _, want := range types {
		if t.Name == want {
			return true
		}
	}
	return false
}

// retag rebuilds parent pointers after children were replaced. Must use isNilExpr: a
// typed nil pointer in an interface is not == nil and would crash SetParent.
func retag(parent Expression, children ...Expression) {
	for _, c := range children {
		if !isNilExpr(c) {
			c.SetParent(parent)
		}
	}
}

// Ident is an identifier (table/column name); Quoted records original quoting.
type Ident struct {
	base
	Name   string
	Quoted bool
	// QuoteChar: original quote char (backtick/double/bracket), for dialect-consistent rendering.
	QuoteChar byte
}

func NewIdent(name string) *Ident { return &Ident{Name: name} }

// IdentFromName mirrors `exp.to_identifier(name)` with quoted=None: unsafe names are
// auto-quoted (MySQL alias for SELECT 1 must be `1` in backticks). Not interchangeable
// with NewIdent, which always leaves Quoted false.
func IdentFromName(name string) *Ident {
	return &Ident{Name: name, Quoted: !safeIdentifierRe(name)}
}

func (i *Ident) copyBase() *Ident {
	c := &Ident{Name: i.Name, Quoted: i.Quoted, QuoteChar: i.QuoteChar}
	return c
}

// Table is a table reference; the db.table form is represented by Parts (left to right).
type Table struct {
	base
	Parts []*Ident
	Alias *TableAlias
}

func NewTable(parts ...string) *Table {
	t := &Table{}
	for _, p := range parts {
		t.Parts = append(t.Parts, NewIdent(p))
	}
	return t
}

// Name returns the last part (the table name), mirroring sqlglot Table.name.
func (t *Table) Name() string {
	if len(t.Parts) == 0 {
		return ""
	}
	return t.Parts[len(t.Parts)-1].Name
}

// AliasOrName returns the alias if present, otherwise the table name.
func (t *Table) AliasOrName() string {
	if t.Alias != nil && t.Alias.Name != nil {
		return t.Alias.Name.Name
	}
	return t.Name()
}

// TableAlias is a table alias with an optional list of column aliases.
type TableAlias struct {
	base
	Name    *Ident
	Columns []*Ident
}

// Column is a column reference: Table is optional, Name is required.
type Column struct {
	base
	Table *Ident
	Name  *Ident
}

func NewColumn(name string) *Column { return &Column{Name: NewIdent(name)} }

// Star is the wildcard: * or t.*.
type Star struct {
	base
	Table *Ident
}

// Literal is a literal value: numbers, strings; NULL is represented by Null.
type Literal struct {
	base
	Value    string
	IsString bool
}

func Num(v string) *Literal { return &Literal{Value: v} }
func Str(v string) *Literal { return &Literal{Value: v, IsString: true} }

// Null represents NULL.
type Null struct{ base }

// Boolean represents TRUE/FALSE.
type Boolean struct {
	base
	Value bool
}

// Alias is an expression alias: `expr AS alias`.
type Alias struct {
	base
	This  Expression
	Alias *Ident
}

// Paren represents a parenthesized expression.
type Paren struct {
	base
	This Expression
}

// Unary is a unary operation: NOT / - / + .
type Unary struct {
	base
	Op   string
	This Expression
}

// Binary is a binary operation: = <> < <= > >= + - * / % AND OR LIKE ILIKE IS IN etc.
type Binary struct {
	base
	Op    string
	Left  Expression
	Right Expression
}

// Is represents `expr IS [NOT] NULL` / `IS TRUE` etc.
type Is struct {
	base
	This Expression
	Not  bool
	Expr Expression
}

// In represents `expr [NOT] IN (...)`; when Query is non-nil the right side is a subquery.
type In struct {
	base
	This  Expression
	Not   bool
	List  []Expression
	Query *Subquery
}

// Exists represents `[NOT] EXISTS (subquery)`.
type Exists struct {
	base
	This    *Subquery
	Not     bool
	Negated bool
}

// Between represents `expr [NOT] BETWEEN low AND high`.
type Between struct {
	base
	This Expression
	Low  Expression
	High Expression
	Not  bool
}

// Case/When represent CASE WHEN ... THEN ... ELSE ... END.
type Case struct {
	base
	Operand Expression
	Ifs     []*When
	Default Expression
}

// When represents the WHEN ... THEN ... branch inside a CASE.
type When struct {
	base
	Cond Expression
	Then Expression
}

// Cast represents CAST(expr AS type) or `::type`.
type Cast struct {
	base
	This Expression
	To   *DataType
	Try  bool // TRY_CAST
}

// DataType is a data type node.
type DataType struct {
	base
	Name string
}

// Func is a named function call (including aggregate functions).
type Func struct {
	base
	Name     string
	Args     []Expression
	Distinct bool
	Filter   Expression // FILTER(WHERE ...)
	Star     bool       // COUNT(*)
	Over     string     // window definition source text (preserved verbatim)
	Suffix   []string   // fragments such as ORDER BY inside arguments (preserved verbatim)
}

// Subquery is a derived table/subquery: `(SELECT ...) [AS alias]`.
type Subquery struct {
	base
	This  Expression // usually *Select / *Union / *With
	Alias *TableAlias
}

// CTE is a common table expression: `name AS (query)`.
type CTE struct {
	base
	Alias *TableAlias
	This  *Subquery
}

// With is the statement-level WITH clause: it only carries the CTE list; the owning
// query renders the prefix (avoids a this ↔ owner parent cycle).
type With struct {
	base
	Recursive bool
	CTEs      []*CTE
}

// Select is the query body.
type Select struct {
	base
	With        *With
	Distinct    bool
	Top         string // T-SQL TOP n (numeric text preserved verbatim)
	Expressions []Expression
	From        Expression
	Joins       []*Join
	Where       Expression
	Group       *Group
	Having      Expression
	Order       *Order
	Limit       *Limit
	Offset      *Offset
}

// Group represents GROUP BY.
type Group struct {
	base
	Expressions []Expression
}

// Order represents ORDER BY.
type Order struct {
	base
	Expressions []*Ordered
}

// Ordered represents a single ordering item.
type Ordered struct {
	base
	This Expression
	Desc bool
	// Explicit: ASC/DESC was written in the input (sqlglot only emits it then).
	Explicit   bool
	NullsFirst *bool
}

// Limit represents LIMIT n.
type Limit struct {
	base
	Expression Expression
}

// Offset represents OFFSET n.
type Offset struct {
	base
	Expression Expression
}

// Join represents the various kinds of JOIN.
type Join struct {
	base
	Side  string // "", "LEFT", "RIGHT", "FULL", "NATURAL"
	Kind  string // "", "INNER", "CROSS", "OUTER"
	Comma bool   // comma-separated table in FROM (sqlglot renders it as `, t`)
	This  Expression
	On    Expression
	Using []*Ident
}

// Union represents UNION [ALL] / INTERSECT / EXCEPT.
type Union struct {
	base
	With     *With
	Distinct bool
	Op       string // "UNION", "INTERSECT", "EXCEPT"
	Left     Expression
	Right    Expression
}

// Unknown represents an unrecognized statement; the original text is written back verbatim.
type Unknown struct {
	base
	Kind string
	Text string
}

// ---- Children / Copy implementations ----

func (n *Ident) Children() []Expression     { return nil }
func (n *Ident) SetChildren(_ []Expression) {}
func (n *Ident) Copy() Expression           { c := n.copyBase(); c.typ = n.typ; return c }
func (n *Ident) SQL(d *Dialect) string      { return d.renderIdent(n) }

func (n *Table) Children() []Expression {
	out := []Expression{}
	for _, p := range n.Parts {
		out = append(out, p)
	}
	if n.Alias != nil {
		out = append(out, n.Alias)
	}
	return out
}
func (n *Table) SetChildren(ch []Expression) {
	p := n.Parts[:0]
	n.Parts = nil
	n.Alias = nil
	for _, c := range ch {
		switch v := c.(type) {
		case *Ident:
			n.Parts = append(p, v)
		case *TableAlias:
			n.Alias = v
		}
	}
	retag(n, n.Children()...)
}
func (n *Table) Copy() Expression {
	c := &Table{}
	for _, p := range n.Parts {
		c.Parts = append(c.Parts, p.Copy().(*Ident))
	}
	if n.Alias != nil {
		c.Alias = n.Alias.Copy().(*TableAlias)
	}
	c.typ = n.typ
	return c
}
func (n *Table) SQL(d *Dialect) string { return d.renderTable(n) }

func (n *TableAlias) Children() []Expression {
	out := []Expression{}
	if n.Name != nil {
		out = append(out, n.Name)
	}
	for _, c := range n.Columns {
		out = append(out, c)
	}
	return out
}
func (n *TableAlias) SetChildren(ch []Expression) {
	n.Name = nil
	n.Columns = nil
	for _, c := range ch {
		if id, ok := c.(*Ident); ok {
			if n.Name == nil {
				n.Name = id
			} else {
				n.Columns = append(n.Columns, id)
			}
		}
	}
	retag(n, n.Children()...)
}
func (n *TableAlias) Copy() Expression {
	c := &TableAlias{}
	if n.Name != nil {
		c.Name = n.Name.Copy().(*Ident)
	}
	for _, x := range n.Columns {
		c.Columns = append(c.Columns, x.Copy().(*Ident))
	}
	c.typ = n.typ
	return c
}
func (n *TableAlias) SQL(d *Dialect) string { return d.renderTableAlias(n) }

func (n *Column) Children() []Expression {
	out := []Expression{}
	if n.Table != nil {
		out = append(out, n.Table)
	}
	out = append(out, n.Name)
	return out
}
func (n *Column) SetChildren(ch []Expression) {
	n.Table, n.Name = nil, nil
	ids := []*Ident{}
	for _, c := range ch {
		if id, ok := c.(*Ident); ok {
			ids = append(ids, id)
		}
	}
	if len(ids) >= 2 {
		n.Table, n.Name = ids[len(ids)-2], ids[len(ids)-1]
	} else if len(ids) == 1 {
		n.Name = ids[0]
	}
	retag(n, n.Children()...)
}
func (n *Column) Copy() Expression {
	c := &Column{}
	if n.Table != nil {
		c.Table = n.Table.Copy().(*Ident)
	}
	if n.Name != nil {
		c.Name = n.Name.Copy().(*Ident)
	}
	c.typ = n.typ
	return c
}
func (n *Column) SQL(d *Dialect) string { return d.renderColumn(n) }

func (n *Star) Children() []Expression {
	if n.Table != nil {
		return []Expression{n.Table}
	}
	return nil
}
func (n *Star) SetChildren(ch []Expression) {
	n.Table = nil
	if len(ch) > 0 {
		if id, ok := ch[0].(*Ident); ok {
			n.Table = id
		}
	}
}
func (n *Star) Copy() Expression {
	c := &Star{}
	if n.Table != nil {
		c.Table = n.Table.Copy().(*Ident)
	}
	c.typ = n.typ
	return c
}
func (n *Star) SQL(d *Dialect) string { return d.renderStar(n) }

func (n *Literal) Children() []Expression     { return nil }
func (n *Literal) SetChildren(_ []Expression) {}
func (n *Literal) Copy() Expression           { return &Literal{Value: n.Value, IsString: n.IsString} }
func (n *Literal) SQL(d *Dialect) string      { return d.renderLiteral(n) }

func (n *Null) Children() []Expression     { return nil }
func (n *Null) SetChildren(_ []Expression) {}
func (n *Null) Copy() Expression           { return &Null{} }
func (n *Null) SQL(_ *Dialect) string      { return "NULL" }

func (n *Boolean) Children() []Expression     { return nil }
func (n *Boolean) SetChildren(_ []Expression) {}
func (n *Boolean) Copy() Expression           { return &Boolean{Value: n.Value} }
func (n *Boolean) SQL(_ *Dialect) string {
	if n.Value {
		return "TRUE"
	}
	return "FALSE"
}

func (n *Alias) Children() []Expression { return []Expression{n.This, n.Alias} }
func (n *Alias) SetChildren(ch []Expression) {
	if len(ch) >= 1 {
		n.This = ch[0]
	}
	if len(ch) >= 2 {
		if id, ok := ch[1].(*Ident); ok {
			n.Alias = id
		}
	} else {
		n.Alias = nil
	}
	retag(n, n.Children()...)
}
func (n *Alias) Copy() Expression {
	c := &Alias{}
	if n.This != nil {
		c.This = n.This.Copy()
	}
	if n.Alias != nil {
		c.Alias = n.Alias.Copy().(*Ident)
	}
	c.typ = n.typ
	return c
}
func (n *Alias) SQL(d *Dialect) string { return d.renderAlias(n) }

func (n *Paren) Children() []Expression { return []Expression{n.This} }
func (n *Paren) SetChildren(ch []Expression) {
	if len(ch) > 0 {
		n.This = ch[0]
	}
	retag(n, n.Children()...)
}
func (n *Paren) Copy() Expression {
	c := &Paren{}
	if n.This != nil {
		c.This = n.This.Copy()
	}
	c.typ = n.typ
	return c
}
func (n *Paren) SQL(d *Dialect) string { return "(" + n.This.SQL(d) + ")" }

func (n *Unary) Children() []Expression { return []Expression{n.This} }
func (n *Unary) SetChildren(ch []Expression) {
	if len(ch) > 0 {
		n.This = ch[0]
	}
	retag(n, n.Children()...)
}
func (n *Unary) Copy() Expression {
	c := &Unary{Op: n.Op}
	if n.This != nil {
		c.This = n.This.Copy()
	}
	c.typ = n.typ
	return c
}
func (n *Unary) SQL(d *Dialect) string { return d.renderUnary(n) }

func (n *Binary) Children() []Expression { return []Expression{n.Left, n.Right} }
func (n *Binary) SetChildren(ch []Expression) {
	if len(ch) > 0 {
		n.Left = ch[0]
	}
	if len(ch) > 1 {
		n.Right = ch[1]
	}
	retag(n, n.Children()...)
}
func (n *Binary) Copy() Expression {
	c := &Binary{Op: n.Op}
	if n.Left != nil {
		c.Left = n.Left.Copy()
	}
	if n.Right != nil {
		c.Right = n.Right.Copy()
	}
	c.typ = n.typ
	return c
}
func (n *Binary) SQL(d *Dialect) string { return d.renderBinary(n) }

func (n *Is) Children() []Expression {
	out := []Expression{n.This}
	if n.Expr != nil {
		out = append(out, n.Expr)
	}
	return out
}
func (n *Is) SetChildren(ch []Expression) {
	if len(ch) > 0 {
		n.This = ch[0]
	}
	if len(ch) > 1 {
		n.Expr = ch[1]
	} else {
		n.Expr = nil
	}
	retag(n, n.Children()...)
}
func (n *Is) Copy() Expression {
	c := &Is{Not: n.Not}
	if n.This != nil {
		c.This = n.This.Copy()
	}
	if n.Expr != nil {
		c.Expr = n.Expr.Copy()
	}
	c.typ = n.typ
	return c
}
func (n *Is) SQL(d *Dialect) string { return d.renderIs(n) }

func (n *In) Children() []Expression {
	// The order must match walk.go's setChildAt and sqlglot's arg_types:
	// this, expressions(List)..., query
	out := []Expression{n.This}
	out = append(out, n.List...)
	if n.Query != nil {
		out = append(out, n.Query)
	}
	return out
}
func (n *In) SetChildren(ch []Expression) {
	n.List, n.Query = nil, nil
	for i, c := range ch {
		if i == 0 {
			n.This = c
			continue
		}
		if sq, ok := c.(*Subquery); ok {
			n.Query = sq
		} else {
			n.List = append(n.List, c)
		}
	}
	retag(n, n.Children()...)
}
func (n *In) Copy() Expression {
	c := &In{Not: n.Not}
	if n.This != nil {
		c.This = n.This.Copy()
	}
	for _, x := range n.List {
		c.List = append(c.List, x.Copy())
	}
	if n.Query != nil {
		c.Query = n.Query.Copy().(*Subquery)
	}
	c.typ = n.typ
	return c
}
func (n *In) SQL(d *Dialect) string { return d.renderIn(n) }

func (n *Exists) Children() []Expression {
	if n.This != nil {
		return []Expression{n.This}
	}
	return nil
}
func (n *Exists) SetChildren(ch []Expression) {
	if len(ch) > 0 {
		if sq, ok := ch[0].(*Subquery); ok {
			n.This = sq
		}
	}
	retag(n, n.Children()...)
}
func (n *Exists) Copy() Expression {
	c := &Exists{Not: n.Not}
	if n.This != nil {
		c.This = n.This.Copy().(*Subquery)
	}
	c.typ = n.typ
	return c
}
func (n *Exists) SQL(d *Dialect) string { return d.renderExists(n) }

func (n *Between) Children() []Expression { return []Expression{n.This, n.Low, n.High} }
func (n *Between) SetChildren(ch []Expression) {
	if len(ch) > 0 {
		n.This = ch[0]
	}
	if len(ch) > 1 {
		n.Low = ch[1]
	}
	if len(ch) > 2 {
		n.High = ch[2]
	}
	retag(n, n.Children()...)
}
func (n *Between) Copy() Expression {
	c := &Between{Not: n.Not}
	if n.This != nil {
		c.This = n.This.Copy()
	}
	if n.Low != nil {
		c.Low = n.Low.Copy()
	}
	if n.High != nil {
		c.High = n.High.Copy()
	}
	c.typ = n.typ
	return c
}
func (n *Between) SQL(d *Dialect) string { return d.renderBetween(n) }

func (n *Case) Children() []Expression {
	out := []Expression{}
	if n.Operand != nil {
		out = append(out, n.Operand)
	}
	for _, w := range n.Ifs {
		out = append(out, w)
	}
	if n.Default != nil {
		out = append(out, n.Default)
	}
	return out
}
func (n *Case) SetChildren(ch []Expression) {
	n.Operand, n.Ifs, n.Default = nil, nil, nil
	for _, c := range ch {
		switch v := c.(type) {
		case *When:
			n.Ifs = append(n.Ifs, v)
		default:
			if n.Ifs == nil && n.Operand == nil && len(ch) > 1 {
				n.Operand = v
			} else {
				n.Default = v
			}
		}
	}
	retag(n, n.Children()...)
}
func (n *Case) Copy() Expression {
	c := &Case{}
	if n.Operand != nil {
		c.Operand = n.Operand.Copy()
	}
	for _, w := range n.Ifs {
		c.Ifs = append(c.Ifs, w.Copy().(*When))
	}
	if n.Default != nil {
		c.Default = n.Default.Copy()
	}
	c.typ = n.typ
	return c
}
func (n *Case) SQL(d *Dialect) string { return d.renderCase(n) }

func (n *When) Children() []Expression { return []Expression{n.Cond, n.Then} }
func (n *When) SetChildren(ch []Expression) {
	if len(ch) > 0 {
		n.Cond = ch[0]
	}
	if len(ch) > 1 {
		n.Then = ch[1]
	}
	retag(n, n.Children()...)
}
func (n *When) Copy() Expression {
	c := &When{}
	if n.Cond != nil {
		c.Cond = n.Cond.Copy()
	}
	if n.Then != nil {
		c.Then = n.Then.Copy()
	}
	c.typ = n.typ
	return c
}
func (n *When) SQL(d *Dialect) string {
	return "WHEN " + n.Cond.SQL(d) + " THEN " + n.Then.SQL(d)
}

func (n *Cast) Children() []Expression { return []Expression{n.This, n.To} }
func (n *Cast) SetChildren(ch []Expression) {
	if len(ch) > 0 {
		n.This = ch[0]
	}
	if len(ch) > 1 {
		if dt, ok := ch[1].(*DataType); ok {
			n.To = dt
		}
	}
	retag(n, n.Children()...)
}
func (n *Cast) Copy() Expression {
	c := &Cast{Try: n.Try}
	if n.This != nil {
		c.This = n.This.Copy()
	}
	if n.To != nil {
		c.To = n.To.Copy().(*DataType)
	}
	c.typ = n.typ
	return c
}
func (n *Cast) SQL(d *Dialect) string {
	to := ""
	if n.To != nil {
		to = n.To.Name
	}
	name := "CAST"
	if n.Try {
		name = "TRY_CAST"
	}
	return name + "(" + n.This.SQL(d) + " AS " + d.renderCastTypeName(to) + ")"
}

func (n *DataType) Children() []Expression     { return nil }
func (n *DataType) SetChildren(_ []Expression) {}
func (n *DataType) Copy() Expression           { return &DataType{Name: n.Name} }
func (n *DataType) SQL(d *Dialect) string      { return d.renderTypeName(n.Name) }

func (n *Func) Children() []Expression {
	out := []Expression{}
	out = append(out, n.Args...)
	if n.Filter != nil {
		out = append(out, n.Filter)
	}
	return out
}
func (n *Func) SetChildren(ch []Expression) {
	n.Args, n.Filter = nil, nil
	for _, c := range ch {
		if f, ok := c.(*WhereClause); ok {
			n.Filter = f
		} else {
			n.Args = append(n.Args, c)
		}
	}
	retag(n, n.Children()...)
}
func (n *Func) Copy() Expression {
	c := &Func{Name: n.Name, Distinct: n.Distinct, Star: n.Star, Over: n.Over}
	c.Suffix = append(c.Suffix, n.Suffix...)
	for _, a := range n.Args {
		c.Args = append(c.Args, a.Copy())
	}
	if n.Filter != nil {
		c.Filter = n.Filter.Copy()
	}
	c.typ = n.typ
	return c
}
func (n *Func) SQL(d *Dialect) string { return d.renderFunc(n) }

// WhereClause carries the condition inside FILTER(WHERE ...).
type WhereClause struct {
	base
	This Expression
}

func (n *WhereClause) Children() []Expression { return []Expression{n.This} }
func (n *WhereClause) SetChildren(ch []Expression) {
	if len(ch) > 0 {
		n.This = ch[0]
	}
	retag(n, n.Children()...)
}
func (n *WhereClause) Copy() Expression {
	c := &WhereClause{}
	if n.This != nil {
		c.This = n.This.Copy()
	}
	c.typ = n.typ
	return c
}
func (n *WhereClause) SQL(d *Dialect) string { return "WHERE " + n.This.SQL(d) }

func (n *Subquery) Children() []Expression {
	out := []Expression{}
	if n.This != nil {
		out = append(out, n.This)
	}
	if n.Alias != nil {
		out = append(out, n.Alias)
	}
	return out
}
func (n *Subquery) SetChildren(ch []Expression) {
	n.This, n.Alias = nil, nil
	for _, c := range ch {
		if a, ok := c.(*TableAlias); ok {
			n.Alias = a
		} else {
			n.This = c
		}
	}
	retag(n, n.Children()...)
}
func (n *Subquery) Copy() Expression {
	c := &Subquery{}
	if n.This != nil {
		c.This = n.This.Copy()
	}
	if n.Alias != nil {
		c.Alias = n.Alias.Copy().(*TableAlias)
	}
	c.typ = n.typ
	return c
}
func (n *Subquery) SQL(d *Dialect) string { return d.renderSubquery(n) }

func (n *CTE) Children() []Expression { return []Expression{n.Alias, n.This} }
func (n *CTE) SetChildren(ch []Expression) {
	if len(ch) > 0 {
		if a, ok := ch[0].(*TableAlias); ok {
			n.Alias = a
		}
	}
	if len(ch) > 1 {
		if s, ok := ch[1].(*Subquery); ok {
			n.This = s
		}
	}
	retag(n, n.Children()...)
}
func (n *CTE) Copy() Expression {
	c := &CTE{}
	if n.Alias != nil {
		c.Alias = n.Alias.Copy().(*TableAlias)
	}
	if n.This != nil {
		c.This = n.This.Copy().(*Subquery)
	}
	c.typ = n.typ
	return c
}
func (n *CTE) SQL(d *Dialect) string { return d.renderCTE(n) }

// AliasName returns the CTE name, mirroring sqlglot CTE.alias.
func (n *CTE) AliasName() string {
	if n.Alias != nil && n.Alias.Name != nil {
		return n.Alias.Name.Name
	}
	return ""
}

func (n *With) Children() []Expression {
	out := []Expression{}
	for _, c := range n.CTEs {
		out = append(out, c)
	}
	return out
}
func (n *With) SetChildren(ch []Expression) {
	n.CTEs = nil
	for _, c := range ch {
		if cte, ok := c.(*CTE); ok {
			n.CTEs = append(n.CTEs, cte)
		}
	}
	retag(n, n.Children()...)
}
func (n *With) Copy() Expression {
	c := &With{Recursive: n.Recursive}
	for _, x := range n.CTEs {
		c.CTEs = append(c.CTEs, x.Copy().(*CTE))
	}
	c.typ = n.typ
	return c
}
func (n *With) SQL(d *Dialect) string { return d.renderWithPrefix(n) }

func (n *Select) Children() []Expression {
	out := []Expression{}
	if n.With != nil {
		out = append(out, n.With)
	}
	out = append(out, n.Expressions...)
	if n.From != nil {
		out = append(out, n.From)
	}
	for _, j := range n.Joins {
		out = append(out, j)
	}
	if n.Where != nil {
		out = append(out, n.Where)
	}
	if n.Group != nil {
		out = append(out, n.Group)
	}
	if n.Having != nil {
		out = append(out, n.Having)
	}
	if n.Order != nil {
		out = append(out, n.Order)
	}
	if n.Limit != nil {
		out = append(out, n.Limit)
	}
	if n.Offset != nil {
		out = append(out, n.Offset)
	}
	return out
}
func (n *Select) SetChildren(ch []Expression) {
	n.With, n.Expressions, n.From, n.Joins = nil, nil, nil, nil
	n.Where, n.Group, n.Having, n.Order, n.Limit, n.Offset = nil, nil, nil, nil, nil, nil
	for _, c := range ch {
		switch v := c.(type) {
		case *With:
			n.With = v
		case *Join:
			n.Joins = append(n.Joins, v)
		case *Group:
			n.Group = v
		case *Order:
			n.Order = v
		case *Limit:
			n.Limit = v
		case *Offset:
			n.Offset = v
		case *WhereClause:
			n.Where = v.This
		case *Table, *Subquery, *Paren:
			n.From = v
		default:
			n.Expressions = append(n.Expressions, v)
		}
	}
	retag(n, n.Children()...)
}
func (n *Select) Copy() Expression {
	c := &Select{Distinct: n.Distinct, Top: n.Top}
	if n.With != nil {
		c.With = n.With.Copy().(*With)
	}
	for _, e := range n.Expressions {
		c.Expressions = append(c.Expressions, e.Copy())
	}
	if n.From != nil {
		c.From = n.From.Copy()
	}
	for _, j := range n.Joins {
		c.Joins = append(c.Joins, j.Copy().(*Join))
	}
	if n.Where != nil {
		c.Where = n.Where.Copy()
	}
	if n.Group != nil {
		c.Group = n.Group.Copy().(*Group)
	}
	if n.Having != nil {
		c.Having = n.Having.Copy()
	}
	if n.Order != nil {
		c.Order = n.Order.Copy().(*Order)
	}
	if n.Limit != nil {
		c.Limit = n.Limit.Copy().(*Limit)
	}
	if n.Offset != nil {
		c.Offset = n.Offset.Copy().(*Offset)
	}
	c.typ = n.typ
	return c
}
func (n *Select) SQL(d *Dialect) string { return d.renderSelect(n) }

func (n *Group) Children() []Expression { return n.Expressions }
func (n *Group) SetChildren(ch []Expression) {
	n.Expressions = ch
	retag(n, n.Children()...)
}
func (n *Group) Copy() Expression {
	c := &Group{}
	for _, e := range n.Expressions {
		c.Expressions = append(c.Expressions, e.Copy())
	}
	c.typ = n.typ
	return c
}
func (n *Group) SQL(d *Dialect) string { return "GROUP BY " + joinSQL(d, n.Expressions, ", ") }

func (n *Order) Children() []Expression {
	out := []Expression{}
	for _, e := range n.Expressions {
		out = append(out, e)
	}
	return out
}
func (n *Order) SetChildren(ch []Expression) {
	n.Expressions = nil
	for _, c := range ch {
		if o, ok := c.(*Ordered); ok {
			n.Expressions = append(n.Expressions, o)
		}
	}
	retag(n, n.Children()...)
}
func (n *Order) Copy() Expression {
	c := &Order{}
	for _, e := range n.Expressions {
		c.Expressions = append(c.Expressions, e.Copy().(*Ordered))
	}
	c.typ = n.typ
	return c
}
func (n *Order) SQL(d *Dialect) string {
	parts := make([]string, 0, len(n.Expressions))
	for _, o := range n.Expressions {
		parts = append(parts, o.SQL(d))
	}
	return "ORDER BY " + strings.Join(parts, ", ")
}

func (n *Ordered) Children() []Expression { return []Expression{n.This} }
func (n *Ordered) SetChildren(ch []Expression) {
	if len(ch) > 0 {
		n.This = ch[0]
	}
	retag(n, n.Children()...)
}
func (n *Ordered) Copy() Expression {
	c := &Ordered{Desc: n.Desc, Explicit: n.Explicit, NullsFirst: n.NullsFirst}
	if n.This != nil {
		c.This = n.This.Copy()
	}
	c.typ = n.typ
	return c
}
func (n *Ordered) SQL(d *Dialect) string {
	s := n.This.SQL(d)
	if n.Explicit {
		if n.Desc {
			s += " DESC"
		} else {
			s += " ASC"
		}
	}
	if n.NullsFirst != nil {
		if *n.NullsFirst {
			s += " NULLS FIRST"
		} else {
			s += " NULLS LAST"
		}
	}
	return s
}

func (n *Limit) Children() []Expression { return []Expression{n.Expression} }
func (n *Limit) SetChildren(ch []Expression) {
	if len(ch) > 0 {
		n.Expression = ch[0]
	}
	retag(n, n.Children()...)
}
func (n *Limit) Copy() Expression {
	c := &Limit{}
	if n.Expression != nil {
		c.Expression = n.Expression.Copy()
	}
	c.typ = n.typ
	return c
}
func (n *Limit) SQL(d *Dialect) string {
	if usesFetchFirst(d.Name) {
		return "FETCH FIRST " + n.Expression.SQL(d) + " ROWS ONLY"
	}
	return "LIMIT " + n.Expression.SQL(d)
}

func (n *Offset) Children() []Expression { return []Expression{n.Expression} }
func (n *Offset) SetChildren(ch []Expression) {
	if len(ch) > 0 {
		n.Expression = ch[0]
	}
	retag(n, n.Children()...)
}
func (n *Offset) Copy() Expression {
	c := &Offset{}
	if n.Expression != nil {
		c.Expression = n.Expression.Copy()
	}
	c.typ = n.typ
	return c
}

// SQL: Oracle's offset_sql appends " ROWS" (`OFFSET 5 ROWS`); others keep `OFFSET 5`.
func (n *Offset) SQL(d *Dialect) string {
	s := "OFFSET " + n.Expression.SQL(d)
	if d.FetchFirst {
		s += " ROWS"
	}
	return s
}

func (n *Join) Children() []Expression {
	out := []Expression{n.This}
	if n.On != nil {
		out = append(out, n.On)
	}
	for _, u := range n.Using {
		out = append(out, u)
	}
	return out
}
func (n *Join) SetChildren(ch []Expression) {
	n.This, n.On, n.Using = nil, nil, nil
	for i, c := range ch {
		switch v := c.(type) {
		case *Ident:
			n.Using = append(n.Using, v)
		default:
			if i == 0 {
				n.This = c
			} else {
				n.On = c
			}
		}
	}
	retag(n, n.Children()...)
}
func (n *Join) Copy() Expression {
	c := &Join{Side: n.Side, Kind: n.Kind, Comma: n.Comma}
	if n.This != nil {
		c.This = n.This.Copy()
	}
	if n.On != nil {
		c.On = n.On.Copy()
	}
	for _, u := range n.Using {
		c.Using = append(c.Using, u.Copy().(*Ident))
	}
	c.typ = n.typ
	return c
}
func (n *Join) SQL(d *Dialect) string { return d.renderJoin(n) }

func (n *Union) Children() []Expression {
	out := []Expression{}
	if n.With != nil {
		out = append(out, n.With)
	}
	return append(out, n.Left, n.Right)
}
func (n *Union) SetChildren(ch []Expression) {
	n.With = nil
	var rest []Expression
	for _, c := range ch {
		if w, ok := c.(*With); ok {
			n.With = w
			continue
		}
		rest = append(rest, c)
	}
	if len(rest) > 0 {
		n.Left = rest[0]
	}
	if len(rest) > 1 {
		n.Right = rest[1]
	}
	retag(n, n.Children()...)
}
func (n *Union) Copy() Expression {
	c := &Union{Distinct: n.Distinct, Op: n.Op}
	if n.With != nil {
		c.With = n.With.Copy().(*With)
	}
	if n.Left != nil {
		c.Left = n.Left.Copy()
	}
	if n.Right != nil {
		c.Right = n.Right.Copy()
	}
	c.typ = n.typ
	return c
}
func (n *Union) SQL(d *Dialect) string {
	op := n.Op
	if op == "" {
		op = "UNION"
	}
	s := n.Left.SQL(d) + " " + op
	if !n.Distinct {
		s += " ALL"
	}
	s = s + " " + n.Right.SQL(d)
	if n.With != nil {
		return n.With.SQL(d) + s
	}
	return s
}

func (n *Unknown) Children() []Expression     { return nil }
func (n *Unknown) SetChildren(_ []Expression) {}
func (n *Unknown) Copy() Expression           { return &Unknown{Kind: n.Kind, Text: n.Text} }
func (n *Unknown) SQL(_ *Dialect) string      { return n.Text }

// joinSQL renders a list of expressions.
func joinSQL(d *Dialect, exprs []Expression, sep string) string {
	parts := make([]string, 0, len(exprs))
	for _, e := range exprs {
		if e == nil {
			continue
		}
		parts = append(parts, e.SQL(d))
	}
	return strings.Join(parts, sep)
}
