package sqlg

// Supplementary AST nodes needed by the parser.

// Distinct represents the DISTINCT wrapper inside function arguments (COUNT(DISTINCT a)).
type Distinct struct {
	base
	This Expression
}

func (n *Distinct) Children() []Expression { return []Expression{n.This} }
func (n *Distinct) SetChildren(ch []Expression) {
	if len(ch) > 0 {
		n.This = ch[0]
	}
	retag(n, n.Children()...)
}
func (n *Distinct) Copy() Expression {
	c := &Distinct{}
	if n.This != nil {
		c.This = n.This.Copy()
	}
	c.typ = n.typ
	return c
}
func (n *Distinct) SQL(d *Dialect) string { return "DISTINCT " + n.This.SQL(d) }

// Interval represents INTERVAL '1' DAY (sqlglot always quotes the number when rendering).
type Interval struct {
	base
	Value Expression
	Unit  string
}

func (n *Interval) Children() []Expression {
	if n.Value != nil {
		return []Expression{n.Value}
	}
	return nil
}
func (n *Interval) SetChildren(ch []Expression) {
	if len(ch) > 0 {
		n.Value = ch[0]
	}
	retag(n, n.Children()...)
}
func (n *Interval) Copy() Expression {
	c := &Interval{Unit: n.Unit}
	if n.Value != nil {
		c.Value = n.Value.Copy()
	}
	c.typ = n.typ
	return c
}
func (n *Interval) SQL(d *Dialect) string {
	v := ""
	if lit, ok := n.Value.(*Literal); ok {
		v = "'" + lit.Value + "'"
	} else if n.Value != nil {
		v = n.Value.SQL(d)
	}
	return "INTERVAL " + v + " " + n.Unit
}

// HexString represents the x'1F' form.
type HexString struct {
	base
	Value string
}

func (n *HexString) Children() []Expression     { return nil }
func (n *HexString) SetChildren(_ []Expression) {}
func (n *HexString) Copy() Expression           { return &HexString{Value: n.Value} }
func (n *HexString) SQL(_ *Dialect) string      { return "x'" + n.Value + "'" }

// TypedLiteral represents DATE '2024-01-01' / TIMESTAMP '...'. The field is Kind, not
// Type: a Type field would shadow the promoted Type() method and break the interface.
type TypedLiteral struct {
	base
	Kind  string
	Value string
}

func (n *TypedLiteral) Children() []Expression     { return nil }
func (n *TypedLiteral) SetChildren(_ []Expression) {}
func (n *TypedLiteral) Copy() Expression {
	c := &TypedLiteral{Kind: n.Kind, Value: n.Value}
	c.typ = n.typ
	return c
}
func (n *TypedLiteral) SQL(_ *Dialect) string {
	return n.Kind + " '" + n.Value + "'"
}

// ---- DML/DDL nodes (canonicalized output only, no semantic analysis) ----

// Insert represents INSERT INTO ... .
type Insert struct {
	base
	Table   Expression
	Columns []*Ident
	Values  [][]Expression
	Query   Expression
}

func (n *Insert) Children() []Expression {
	out := []Expression{n.Table}
	if n.Query != nil {
		out = append(out, n.Query)
	}
	for _, row := range n.Values {
		out = append(out, row...)
	}
	return out
}
func (n *Insert) SetChildren(ch []Expression) {
	n.Table, n.Query, n.Values = nil, nil, nil
	for i, c := range ch {
		if i == 0 {
			n.Table = c
		} else {
			n.Query = c
		}
	}
	retag(n, n.Children()...)
}
func (n *Insert) Copy() Expression {
	c := &Insert{}
	if n.Table != nil {
		c.Table = n.Table.Copy()
	}
	for _, id := range n.Columns {
		c.Columns = append(c.Columns, id.Copy().(*Ident))
	}
	for _, row := range n.Values {
		var r []Expression
		for _, e := range row {
			r = append(r, e.Copy())
		}
		c.Values = append(c.Values, r)
	}
	if n.Query != nil {
		c.Query = n.Query.Copy()
	}
	c.typ = n.typ
	return c
}
func (n *Insert) SQL(d *Dialect) string {
	s := "INSERT INTO " + n.Table.SQL(d)
	if len(n.Columns) > 0 {
		parts := make([]string, 0, len(n.Columns))
		for _, c := range n.Columns {
			parts = append(parts, c.SQL(d))
		}
		s += " (" + joinStr(parts, ", ") + ")"
	}
	if n.Query != nil {
		return s + " " + n.Query.SQL(d)
	}
	if len(n.Values) > 0 {
		rows := make([]string, 0, len(n.Values))
		for _, row := range n.Values {
			vals := make([]string, 0, len(row))
			for _, v := range row {
				vals = append(vals, v.SQL(d))
			}
			rows = append(rows, "("+joinStr(vals, ", ")+")")
		}
		return s + " VALUES " + joinStr(rows, ", ")
	}
	return s
}

// Update represents UPDATE ... SET ... [WHERE ...].
type Update struct {
	base
	Table Expression
	Set   [][2]Expression
	Where Expression
	Limit *Limit
}

func (n *Update) Children() []Expression {
	out := []Expression{n.Table}
	for _, kv := range n.Set {
		out = append(out, kv[0], kv[1])
	}
	if n.Where != nil {
		out = append(out, n.Where)
	}
	if n.Limit != nil {
		out = append(out, n.Limit)
	}
	return out
}
func (n *Update) SetChildren(ch []Expression) {
	if len(ch) > 0 {
		n.Table = ch[0]
	}
	// Tail may be Where/Limit: extract by type, group the rest into SET pairs.
	var core []Expression
	for _, c := range ch[1:] {
		switch v := c.(type) {
		case *Limit:
			n.Limit = v
			continue
		case *WhereClause:
			n.Where = v.This
			continue
		}
		core = append(core, c)
	}
	// Where stores the bare condition, so distinguish by position: an odd trailing
	// node after the SET pairs is the Where.
	if len(core)%2 == 1 {
		n.Where = core[len(core)-1]
		core = core[:len(core)-1]
	}
	n.Set = n.Set[:0]
	for i := 0; i+1 < len(core); i += 2 {
		n.Set = append(n.Set, [2]Expression{core[i], core[i+1]})
	}
	retag(n, n.Children()...)
}
func (n *Update) Copy() Expression {
	c := &Update{}
	if n.Table != nil {
		c.Table = n.Table.Copy()
	}
	for _, kv := range n.Set {
		c.Set = append(c.Set, [2]Expression{kv[0].Copy(), kv[1].Copy()})
	}
	if n.Where != nil {
		c.Where = n.Where.Copy()
	}
	if n.Limit != nil {
		c.Limit = n.Limit.Copy().(*Limit)
	}
	c.typ = n.typ
	return c
}
func (n *Update) setSQL(d *Dialect) string {
	parts := make([]string, 0, len(n.Set))
	for _, kv := range n.Set {
		parts = append(parts, kv[0].SQL(d)+" = "+kv[1].SQL(d))
	}
	return joinStr(parts, ", ")
}
func (n *Update) SQL(d *Dialect) string {
	s := "UPDATE " + n.Table.SQL(d) + " SET " + n.setSQL(d)
	if n.Where != nil {
		s += " WHERE " + n.Where.SQL(d)
	}
	s += limitSQLPlain(d, n.Limit)
	return s
}

// limitSQLPlain mirrors the base Generator.limit_sql (leading space included). Oracle's
// FETCH FIRST rewrite applies only to SELECT query_modifiers; UPDATE/DELETE take the
// "limit" arg through this base path, so `UPDATE ... LIMIT 100` stays literal.
func limitSQLPlain(d *Dialect, n *Limit) string {
	if n == nil || n.Expression == nil {
		return ""
	}
	return " LIMIT " + n.Expression.SQL(d)
}

// Delete represents DELETE FROM ... [WHERE ...].
type Delete struct {
	base
	Table Expression
	Where Expression
	Limit *Limit
}

func (n *Delete) Children() []Expression {
	out := []Expression{n.Table}
	if n.Where != nil {
		out = append(out, n.Where)
	}
	if n.Limit != nil {
		out = append(out, n.Limit)
	}
	return out
}
func (n *Delete) SetChildren(ch []Expression) {
	if len(ch) > 0 {
		n.Table = ch[0]
	}
	n.Where, n.Limit = nil, nil
	for _, c := range ch[1:] {
		switch v := c.(type) {
		case *Limit:
			n.Limit = v
		case *WhereClause:
			n.Where = v.This
		default:
			n.Where = c
		}
	}
	retag(n, n.Children()...)
}
func (n *Delete) Copy() Expression {
	c := &Delete{}
	if n.Table != nil {
		c.Table = n.Table.Copy()
	}
	if n.Where != nil {
		c.Where = n.Where.Copy()
	}
	if n.Limit != nil {
		c.Limit = n.Limit.Copy().(*Limit)
	}
	c.typ = n.typ
	return c
}
func (n *Delete) SQL(d *Dialect) string {
	s := "DELETE FROM " + n.Table.SQL(d)
	if n.Where != nil {
		s += " WHERE " + n.Where.SQL(d)
	}
	s += limitSQLPlain(d, n.Limit)
	return s
}
func (n *Delete) pretty(indent int) string { return n.SQL(GetDialect("")) }

// Drop represents `DROP <kind> <table>`. Real Table/Ident nodes are required: plain text
// would not participate in normalize_identifiers, so Oracle's `DROP TABLE users` would
// never upper-case to USERS.
type Drop struct {
	base
	Kind string     // TABLE / VIEW / INDEX / DATABASE / SCHEMA / FUNCTION
	This Expression // *Table
}

func (n *Drop) Children() []Expression {
	if n.This == nil {
		return nil
	}
	return []Expression{n.This}
}
func (n *Drop) SetChildren(ch []Expression) {
	n.This = nil
	if len(ch) > 0 {
		n.This = ch[0]
	}
	retag(n, ch...)
}
func (n *Drop) Copy() Expression {
	c := &Drop{Kind: n.Kind}
	if n.This != nil {
		c.This = n.This.Copy()
	}
	c.typ = n.typ
	return c
}

// SQL replicates Generator.drop_sql (exists/temporary/cascade are always empty here).
func (n *Drop) SQL(d *Dialect) string {
	this := ""
	if n.This != nil {
		this = n.This.SQL(d)
	}
	return "DROP " + n.Kind + " " + this
}

// Create represents `CREATE <kind> <this>`.
type Create struct {
	base
	Kind string     // TABLE
	This Expression // *Schema / *Table
}

func (n *Create) Children() []Expression {
	if n.This == nil {
		return nil
	}
	return []Expression{n.This}
}
func (n *Create) SetChildren(ch []Expression) {
	n.This = nil
	if len(ch) > 0 {
		n.This = ch[0]
	}
	retag(n, ch...)
}
func (n *Create) Copy() Expression {
	c := &Create{Kind: n.Kind}
	if n.This != nil {
		c.This = n.This.Copy()
	}
	c.typ = n.typ
	return c
}

// SQL replicates Generator.create_sql; the Schema renders `t2 (id INT)` as a whole.
func (n *Create) SQL(d *Dialect) string {
	this := ""
	if n.This != nil {
		this = n.This.SQL(d)
	}
	return "CREATE " + n.Kind + " " + this
}

// Describe represents `DESCRIBE <table>`.
type Describe struct {
	base
	This Expression // *Table
}

func (n *Describe) Children() []Expression {
	if n.This == nil {
		return nil
	}
	return []Expression{n.This}
}
func (n *Describe) SetChildren(ch []Expression) {
	n.This = nil
	if len(ch) > 0 {
		n.This = ch[0]
	}
	retag(n, ch...)
}
func (n *Describe) Copy() Expression {
	c := &Describe{}
	if n.This != nil {
		c.This = n.This.Copy()
	}
	c.typ = n.typ
	return c
}

// SQL replicates Generator.describe_sql (style/format/partition are always empty).
func (n *Describe) SQL(d *Dialect) string {
	this := ""
	if n.This != nil {
		this = n.This.SQL(d)
	}
	return "DESCRIBE " + this
}

// Show represents the MySQL/StarRocks `SHOW ...`. Other dialects pass SHOW through as a
// Command (see Parser.parseStatement).
type Show struct {
	base
	This  string // e.g. TABLES
	Limit *Limit
}

func (n *Show) Children() []Expression {
	if n.Limit == nil {
		return nil
	}
	return []Expression{n.Limit}
}
func (n *Show) SetChildren(ch []Expression) {
	n.Limit = nil
	for _, c := range ch {
		if v, ok := c.(*Limit); ok {
			n.Limit = v
		}
	}
	retag(n, n.Children()...)
}
func (n *Show) Copy() Expression {
	c := &Show{This: n.This}
	if n.Limit != nil {
		c.Limit = n.Limit.Copy().(*Limit)
	}
	c.typ = n.typ
	return c
}

// SQL replicates mysql show_sql's degenerate form. Quirk: `_oldstyle_limit_sql` prepends
// " LIMIT " and base limit_sql has its own leading space, so the output is
// `SHOW TABLES LIMIT  LIMIT 100` — the double space is sqlglot behavior, not a typo.
func (n *Show) SQL(d *Dialect) string {
	s := "SHOW " + n.This
	if n.Limit != nil {
		s += " LIMIT " + limitSQLPlain(d, n.Limit)
	}
	return s
}

// Command represents a statement passed through verbatim when the dialect does not
// recognize it (e.g. postgres SHOW TABLES).
type Command struct {
	base
	Text string
}

func (n *Command) Children() []Expression     { return nil }
func (n *Command) SetChildren(_ []Expression) {}
func (n *Command) Copy() Expression           { return &Command{Text: n.Text} }
func (n *Command) SQL(_ *Dialect) string      { return n.Text }

// Schema represents `name (a INT, b TEXT)`, carrying CREATE TABLE column definitions.
type Schema struct {
	base
	This        Expression   // *Table
	Expressions []Expression // *ColumnDef
}

func (n *Schema) Children() []Expression {
	out := make([]Expression, 0, len(n.Expressions)+1)
	if n.This != nil {
		out = append(out, n.This)
	}
	out = append(out, n.Expressions...)
	return out
}
func (n *Schema) SetChildren(ch []Expression) {
	n.This, n.Expressions = nil, nil
	for i, c := range ch {
		if i == 0 {
			n.This = c
			continue
		}
		n.Expressions = append(n.Expressions, c)
	}
	retag(n, n.Children()...)
}
func (n *Schema) Copy() Expression {
	c := &Schema{}
	if n.This != nil {
		c.This = n.This.Copy()
	}
	for _, e := range n.Expressions {
		c.Expressions = append(c.Expressions, e.Copy())
	}
	c.typ = n.typ
	return c
}

// SQL replicates Generator.schema_sql: `this`, or `this (a, b)` when non-empty.
func (n *Schema) SQL(d *Dialect) string {
	this := ""
	if n.This != nil {
		this = n.This.SQL(d)
	}
	if len(n.Expressions) == 0 {
		return this
	}
	parts := make([]string, 0, len(n.Expressions))
	for _, e := range n.Expressions {
		parts = append(parts, e.SQL(d))
	}
	return this + " (" + joinStr(parts, ", ") + ")"
}

// ColumnDef represents a column definition `id INT`.
type ColumnDef struct {
	base
	This *Ident
	Kind *DataType
}

func (n *ColumnDef) Children() []Expression {
	out := []Expression{}
	if n.This != nil {
		out = append(out, n.This)
	}
	if n.Kind != nil {
		out = append(out, n.Kind)
	}
	return out
}
func (n *ColumnDef) SetChildren(ch []Expression) {
	n.This, n.Kind = nil, nil
	for _, c := range ch {
		switch v := c.(type) {
		case *Ident:
			n.This = v
		case *DataType:
			n.Kind = v
		}
	}
	retag(n, n.Children()...)
}
func (n *ColumnDef) Copy() Expression {
	c := &ColumnDef{}
	if n.This != nil {
		c.This = n.This.Copy().(*Ident)
	}
	if n.Kind != nil {
		c.Kind = n.Kind.Copy().(*DataType)
	}
	c.typ = n.typ
	return c
}

// SQL replicates Generator.columndef_sql: `name` + ` kind` (sep is a single space).
func (n *ColumnDef) SQL(d *Dialect) string {
	name := ""
	if n.This != nil {
		name = n.This.SQL(d)
	}
	if n.Kind == nil {
		return name
	}
	return name + " " + n.Kind.SQL(d)
}

func joinStr(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

func indentStr(n int) string {
	s := ""
	for i := 0; i < n; i++ {
		s += " "
	}
	return s
}
