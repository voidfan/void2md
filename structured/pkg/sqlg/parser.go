package sqlg

import (
	"fmt"
	"strings"
)

// ParseError mirrors sqlglot.errors.ParseError.
type ParseError struct {
	Msg  string
	SQL  string
	Line int
	Col  int
}

// Error carries the "ParseError: " prefix verbatim (oracle persists `{type(e).__name__}: {e}`).
// Msg is assembled exactly as parser.raise_error does: message + ". Line {line}, Col: {col}."
// + newline + two spaces + highlighted SQL. The format is " Line 1" (no colon), so the
// oracle's truncation at " Line: " does not fire — the position must be exact.
func (e *ParseError) Error() string { return "ParseError: " + e.Msg }

// Parser is a recursive-descent SQL parser.
type Parser struct {
	toks []Token
	pos  int
	sql  string
	name string // dialect name
}

// ParseOne mirrors sqlglot.parse_one(sql, dialect=...).
func ParseOne(sql string, dialect string) (Expression, error) {
	toks, err := LexDialect(sql, dialect)
	if err != nil {
		return nil, &ParseError{Msg: err.Error(), SQL: sql, Line: 1, Col: 1}
	}
	p := &Parser{toks: toks, sql: sql, name: dialect}
	if p.cur().Type == TokEOF {
		return nil, &ParseError{
			Msg:  fmt.Sprintf("No expression was parsed from '%s'", sql),
			SQL:  sql,
			Line: 1,
			Col:  1,
		}
	}
	e, err := p.parseStatement()
	if err != nil {
		return nil, err
	}
	// parser._parse: leftover tokens after a statement are themselves a parse failure,
	// reported at the token where parsing stopped. ';' separates blocks, so stopping at
	// ';' is fine (`SELECT 1;` is valid); content after the first block is not checked.
	if t := p.cur(); t.Type != TokEOF && !(t.Type == TokPunct && t.Text == ";") {
		return nil, p.errUnexpected()
	}
	// Rebuild the whole-tree parent invariant (parse branches only retag shallowly).
	return RefreshParents(e), nil
}

// ParseStatements parses multiple `;`-separated statements (mirrors sqlglot.transpile input).
func ParseStatements(sql string, dialect string) ([]Expression, error) {
	toks, err := LexDialect(sql, dialect)
	if err != nil {
		return nil, &ParseError{Msg: err.Error(), SQL: sql, Line: 1, Col: 1}
	}
	p := &Parser{toks: toks, sql: sql, name: dialect}
	out := []Expression{}
	for {
		for p.cur().Type == TokPunct && p.cur().Text == ";" {
			p.pos++
		}
		if p.cur().Type == TokEOF {
			break
		}
		e, err := p.parseStatement()
		if err != nil {
			return nil, err
		}
		out = append(out, e)
		if p.cur().Type == TokPunct && p.cur().Text == ";" {
			p.pos++
			continue
		}
		if p.cur().Type != TokEOF {
			return nil, p.errUnexpected()
		}
	}
	return out, nil
}

// ---- token helpers ----

func (p *Parser) cur() Token  { return p.toks[p.pos] }
func (p *Parser) prev() Token { return p.toks[p.pos-1] }

func (p *Parser) peek(n int) Token {
	i := p.pos + n
	if i >= len(p.toks) {
		return p.toks[len(p.toks)-1]
	}
	return p.toks[i]
}

func (p *Parser) advance() Token {
	t := p.toks[p.pos]
	if t.Type != TokEOF {
		p.pos++
	}
	return t
}

func (p *Parser) matchKw(kws ...string) bool {
	t := p.cur()
	if t.Type != TokWord {
		return false
	}
	for _, kw := range kws {
		if strings.EqualFold(t.Text, kw) {
			p.pos++
			return true
		}
	}
	return false
}

// matchSeq tries to match multiple keywords in order; on failure it consumes no tokens.
func (p *Parser) matchSeq(kws ...string) bool {
	save := p.pos
	for _, kw := range kws {
		if !p.matchKw(kw) {
			p.pos = save
			return false
		}
	}
	return true
}

func (p *Parser) matchPunct(s string) bool {
	t := p.cur()
	if (t.Type == TokPunct || t.Type == TokOp) && t.Text == s {
		p.pos++
		return true
	}
	return false
}

func (p *Parser) matchOp(ops ...string) (string, bool) {
	t := p.cur()
	if t.Type != TokOp && t.Type != TokPunct {
		return "", false
	}
	for _, op := range ops {
		if t.Text == op {
			p.pos++
			return op, true
		}
	}
	return "", false
}

func (p *Parser) errUnexpected() error {
	t := p.cur()
	return &ParseError{
		Msg:  fmt.Sprintf("Invalid expression / Unexpected token. Line 1, Col: %d.\n  %s", t.Pos+1, p.sql),
		SQL:  p.sql,
		Line: 1,
		Col:  t.Pos + 1,
	}
}

func (p *Parser) errAt(msg string) error {
	t := p.cur()
	return &ParseError{
		Msg:  fmt.Sprintf("%s. Line 1, Col: %d.\n  %s", msg, t.Pos+1, p.sql),
		SQL:  p.sql,
		Line: 1,
		Col:  t.Pos + 1,
	}
}

// ---- statement level ----

func (p *Parser) parseStatement() (Expression, error) {
	switch {
	case p.cur().isKeyword("SELECT"):
		return p.parseQuery()
	case p.cur().isKeyword("WITH"):
		return p.parseQuery()
	case p.cur().isKeyword("VALUES"):
		return p.parseValues()
	case p.cur().Type == TokPunct && p.cur().Text == "(":
		return p.parseSetOperand()
	case p.cur().isKeyword("INSERT"):
		return p.parseInsert()
	case p.cur().isKeyword("UPDATE"):
		return p.parseUpdate()
	case p.cur().isKeyword("DELETE"):
		return p.parseDelete()
	case p.cur().isKeyword("DROP"):
		return p.parseDrop()
	case p.cur().isKeyword("CREATE"):
		return p.parseCreate()
	case p.cur().isKeyword("DESCRIBE") || p.cur().isKeyword("DESC"):
		return p.parseDescribe()
	case p.cur().isKeyword("SHOW"):
		// Only mysql/starrocks parse SHOW into a statement node; other dialects pass
		// the whole statement through as a Command.
		if p.name == "mysql" || p.name == "starrocks" {
			return p.parseShow()
		}
		return p.parseCommandPassthrough()
	}
	// _parse_statement fallback: a bare expression is a valid statement (`1+2`, `a IS
	// NULL`, `this is not sql`); error only if expression parsing fails too. No EOF
	// check — leftover text becomes the next statement, as in sqlglot.
	if e, err := p.parseExpression(); err == nil && e != nil {
		// _parse_expression = _parse_alias(_parse_assignment()): aliases are consumed
		// at statement level too, so `x y` parses as `x AS y`.
		return p.parseExprAlias(e)
	}
	return nil, p.errUnexpected()
}

// parseQuery parses [WITH ...] SELECT ... [UNION ...]; WITH attaches to the query node
// itself (Select/Union), not a wrapper.
func (p *Parser) parseQuery() (Expression, error) {
	var with *With
	if p.cur().isKeyword("WITH") {
		w, err := p.parseWith()
		if err != nil {
			return nil, err
		}
		with = w
	}
	left, err := p.parseSetOperand()
	if err != nil {
		return nil, err
	}
	left, err = p.parseSetOperations(left)
	if err != nil {
		return nil, err
	}
	if with != nil {
		switch v := left.(type) {
		case *Select:
			v.With = with
		case *Union:
			v.With = with
		case *unionTail:
			v.Union.With = with
		default:
			return nil, p.errAt("Expected SELECT after WITH")
		}
		retag(left, left.Children()...)
	}
	return left, nil
}

// parseWith parses WITH [RECURSIVE] name AS (query), ....
func (p *Parser) parseWith() (*With, error) {
	w := &With{}
	if !p.matchKw("WITH") {
		return nil, p.errUnexpected()
	}
	if p.matchKw("RECURSIVE") {
		w.Recursive = true
	}
	for {
		nameTok := p.cur()
		if nameTok.Type != TokWord && nameTok.Type != TokIdent {
			return nil, p.errAt("Expected CTE name")
		}
		p.pos++
		alias := &TableAlias{Name: &Ident{Name: nameTok.Text, Quoted: nameTok.Type == TokIdent, QuoteChar: nameTok.QuoteChar}}
		if p.matchPunct("(") {
			for {
				t := p.cur()
				if t.Type != TokWord && t.Type != TokIdent {
					return nil, p.errUnexpected()
				}
				p.pos++
				alias.Columns = append(alias.Columns, &Ident{Name: t.Text, Quoted: t.Type == TokIdent, QuoteChar: t.QuoteChar})
				if !p.matchPunct(",") {
					break
				}
			}
			if !p.matchPunct(")") {
				return nil, p.errUnexpected()
			}
		}
		if !p.matchKw("AS") {
			return nil, p.errAt("Expected AS after CTE name")
		}
		if !p.matchPunct("(") {
			return nil, p.errUnexpected()
		}
		q, err := p.parseQuery()
		if err != nil {
			return nil, err
		}
		if !p.matchPunct(")") {
			return nil, p.errUnexpected()
		}
		sub := &Subquery{This: q}
		cte := &CTE{Alias: alias, This: sub}
		retag(cte, alias, sub)
		w.CTEs = append(w.CTEs, cte)
		if !p.matchPunct(",") {
			break
		}
	}
	retag(w, w.Children()...)
	return w, nil
}

// parseSetOperand parses one set-operation operand (SELECT or a parenthesized query).
func (p *Parser) parseSetOperand() (Expression, error) {
	if p.cur().Type == TokPunct && p.cur().Text == "(" {
		p.pos++
		if p.cur().isKeyword("SELECT") || p.cur().isKeyword("WITH") {
			q, err := p.parseQuery()
			if err != nil {
				return nil, err
			}
			if !p.matchPunct(")") {
				return nil, p.errUnexpected()
			}
			sub := &Subquery{This: q}
			retag(sub, sub.Children()...)
			return sub, nil
		}
		return nil, p.errUnexpected()
	}
	if p.cur().isKeyword("SELECT") {
		return p.parseSelect()
	}
	if p.cur().isKeyword("VALUES") {
		return p.parseValues()
	}
	return nil, p.errUnexpected()
}

// parseSetOperations parses a UNION/INTERSECT/EXCEPT chain. Left-associative.
func (p *Parser) parseSetOperations(left Expression) (Expression, error) {
	for {
		op := ""
		switch {
		case p.cur().isKeyword("UNION"):
			op = "UNION"
		case p.cur().isKeyword("INTERSECT"):
			op = "INTERSECT"
		case p.cur().isKeyword("EXCEPT"):
			op = "EXCEPT"
		default:
			return left, nil
		}
		p.pos++
		distinct := true
		if p.matchKw("ALL") {
			distinct = false
		} else {
			p.matchKw("DISTINCT")
		}
		right, err := p.parseSetOperand()
		if err != nil {
			return nil, err
		}
		u := &Union{Op: op, Distinct: distinct, Left: left, Right: right}
		retag(u, left, right)
		left = u
		// ORDER BY / LIMIT / OFFSET apply to the entire set operation
		if p.cur().isKeyword("ORDER") || p.cur().isKeyword("LIMIT") || p.cur().isKeyword("OFFSET") {
			tail, err := p.parseQueryTail()
			if err != nil {
				return nil, err
			}
			if tail != nil {
				return &unionTail{Union: u, Order: tail.Order, Limit: tail.Limit, Offset: tail.Offset}, nil
			}
		}
	}
}

// unionTail carries the trailing ORDER BY/LIMIT/OFFSET of a UNION chain.
type unionTail struct {
	base
	Union  *Union
	Order  *Order
	Limit  *Limit
	Offset *Offset
}

func (n *unionTail) Children() []Expression {
	out := []Expression{n.Union}
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
func (n *unionTail) SetChildren(ch []Expression) {
	for _, c := range ch {
		switch v := c.(type) {
		case *Union:
			n.Union = v
		case *Order:
			n.Order = v
		case *Limit:
			n.Limit = v
		case *Offset:
			n.Offset = v
		}
	}
	retag(n, n.Children()...)
}
func (n *unionTail) Copy() Expression {
	c := &unionTail{Union: n.Union.Copy().(*Union)}
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
func (n *unionTail) SQL(d *Dialect) string {
	s := n.Union.SQL(d)
	if n.Order != nil {
		s += " " + n.Order.SQL(d)
	}
	if n.Limit != nil {
		s += " " + n.Limit.SQL(d)
	}
	if n.Offset != nil {
		s += " " + n.Offset.SQL(d)
	}
	return s
}

// parseSelect parses the SELECT body.
func (p *Parser) parseSelect() (*Select, error) {
	if !p.matchKw("SELECT") {
		return nil, p.errAt("Expected SELECT")
	}
	s := &Select{}
	if p.cur().isKeyword("DISTINCT") {
		p.pos++
		s.Distinct = true
	} else if p.matchKw("ALL") {
		// sqlglot discards an explicit ALL
	}
	if p.matchKw("TOP") {
		// T-SQL TOP n: recorded as limit (whether to render it verbatim is up to the dialect)
		if p.cur().Type == TokNumber {
			n := p.cur().Text
			p.pos++
			s.Top = n
			if p.matchKw("PERCENT") {
				s.Top += " PERCENT"
			}
		}
	}
	for {
		e, err := p.parseSelectExpr()
		if err != nil {
			return nil, err
		}
		s.Expressions = append(s.Expressions, e)
		if !p.matchPunct(",") {
			break
		}
	}
	if p.cur().isKeyword("FROM") {
		p.pos++
		f, err := p.parseFrom()
		if err != nil {
			return nil, err
		}
		s.From = f
	}
	for {
		j, err := p.parseJoin()
		if err != nil {
			return nil, err
		}
		if j == nil {
			break
		}
		s.Joins = append(s.Joins, j)
	}
	if p.cur().isKeyword("WHERE") {
		p.pos++
		w, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		s.Where = w
	}
	if p.matchSeq("GROUP", "BY") {
		g := &Group{}
		for {
			e, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			g.Expressions = append(g.Expressions, e)
			if !p.matchPunct(",") {
				break
			}
		}
		s.Group = g
	}
	if p.cur().isKeyword("HAVING") {
		p.pos++
		h, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		s.Having = h
	}
	if p.cur().isKeyword("QUALIFY") {
		p.pos++
		if _, err := p.parseExpression(); err != nil {
			return nil, err
		}
	}
	tail, err := p.parseQueryTail()
	if err != nil {
		return nil, err
	}
	if tail != nil {
		s.Order, s.Limit, s.Offset = tail.Order, tail.Limit, tail.Offset
	}
	retag(s, s.Children()...)
	return s, nil
}

// parseValues parses top-level VALUES (...), (...): in sqlglot this is not a Query.
func (p *Parser) parseValues() (*Values, error) {
	p.matchKw("VALUES")
	v := &Values{}
	for {
		if !p.matchPunct("(") {
			return nil, p.errUnexpected()
		}
		var row []Expression
		for {
			e, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			row = append(row, e)
			if !p.matchPunct(",") {
				break
			}
		}
		if !p.matchPunct(")") {
			return nil, p.errUnexpected()
		}
		v.Rows = append(v.Rows, row)
		if !p.matchPunct(",") {
			break
		}
	}
	retag(v, v.Children()...)
	return v, nil
}

// Values is the top-level VALUES statement.
type Values struct {
	base
	Rows [][]Expression
}

func (n *Values) Children() []Expression {
	out := []Expression{}
	for _, r := range n.Rows {
		out = append(out, r...)
	}
	return out
}
func (n *Values) SetChildren(ch []Expression) {
	n.Rows = [][]Expression{ch}
	retag(n, n.Children()...)
}
func (n *Values) Copy() Expression {
	c := &Values{}
	for _, r := range n.Rows {
		var row []Expression
		for _, e := range r {
			row = append(row, e.Copy())
		}
		c.Rows = append(c.Rows, row)
	}
	c.typ = n.typ
	return c
}
func (n *Values) SQL(d *Dialect) string {
	rows := make([]string, 0, len(n.Rows))
	for _, r := range n.Rows {
		rows = append(rows, "("+joinSQL(d, r, ", ")+")")
	}
	return "VALUES " + strings.Join(rows, ", ")
}

type queryTail struct {
	Order  *Order
	Limit  *Limit
	Offset *Offset
}

func (p *Parser) parseQueryTail() (*queryTail, error) {
	var t *queryTail
	if p.cur().isKeyword("ORDER") && p.peek(1).isKeyword("BY") {
		p.pos += 2
		o := &Order{}
		for {
			ord, err := p.parseOrdered()
			if err != nil {
				return nil, err
			}
			o.Expressions = append(o.Expressions, ord)
			if !p.matchPunct(",") {
				break
			}
		}
		if t == nil {
			t = &queryTail{}
		}
		t.Order = o
	}
	if p.cur().isKeyword("LIMIT") {
		p.pos++
		e, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if p.matchPunct(",") { // MySQL's LIMIT off, cnt
			e2, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			if t == nil {
				t = &queryTail{}
			}
			t.Offset = &Offset{Expression: e}
			t.Limit = &Limit{Expression: e2}
		} else {
			if t == nil {
				t = &queryTail{}
			}
			t.Limit = &Limit{Expression: e}
		}
	}
	if p.cur().isKeyword("OFFSET") {
		p.pos++
		e, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		p.matchKw("ROWS")
		p.matchKw("ROW")
		if t == nil {
			t = &queryTail{}
		}
		t.Offset = &Offset{Expression: e}
	}
	if p.cur().isKeyword("FETCH") {
		p.pos++
		p.matchKw("FIRST")
		p.matchKw("NEXT")
		e, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		p.matchKw("ROWS")
		p.matchKw("ROW")
		p.matchKw("ONLY")
		p.matchKw("WITH")
		p.matchKw("TIES")
		if t == nil {
			t = &queryTail{}
		}
		t.Limit = &Limit{Expression: e}
	}
	return t, nil
}

func (p *Parser) parseOrdered() (*Ordered, error) {
	e, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	o := &Ordered{}
	if p.matchKw("DESC") {
		o.Desc = true
		o.Explicit = true
	} else if p.matchKw("ASC") {
		o.Desc = false
		o.Explicit = true
	}
	if p.matchSeq("NULLS", "FIRST") {
		v := true
		o.NullsFirst = &v
	} else if p.matchSeq("NULLS", "LAST") {
		v := false
		o.NullsFirst = &v
	}
	o.This = e
	retag(o, e)
	return o, nil
}

// ---- FROM / JOIN ----

// parseSelectExpr parses a SELECT list item: expression + optional alias.
func (p *Parser) parseSelectExpr() (Expression, error) {
	e, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	return p.parseExprAlias(e)
}

// parseExprAlias parses an optional `[AS] alias`; reserved keywords are rejected by isReservedAfterTable.
func (p *Parser) parseExprAlias(e Expression) (Expression, error) {
	hasAS := p.matchKw("AS")
	t := p.cur()
	if (t.Type == TokWord || t.Type == TokIdent) && !isReservedAfterTable(t.Text) {
		p.pos++
		a := &Alias{This: e, Alias: &Ident{Name: t.Text, Quoted: t.Type == TokIdent, QuoteChar: t.QuoteChar}}
		retag(a, e, a.Alias)
		return a, nil
	}
	if hasAS {
		return nil, p.errAt("Expected alias after AS")
	}
	return e, nil
}

// parseFrom parses the FROM clause; multiple comma-separated tables are recorded as comma joins.
func (p *Parser) parseFrom() (Expression, error) {
	first, err := p.parseTableRef()
	if err != nil {
		return nil, err
	}
	return first, nil
}

func (p *Parser) parseJoin() (*Join, error) {
	comma := false
	side, kind := "", ""
	save := p.pos
	switch {
	case p.matchKw("LEFT"):
		side = "LEFT"
		// OUTER is recorded in kind (rendered as `side kind JOIN`); only
		// optimize_joins' normalize clears it.
		if p.matchKw("OUTER") {
			kind = "OUTER"
		}
	case p.matchKw("RIGHT"):
		side = "RIGHT"
		if p.matchKw("OUTER") {
			kind = "OUTER"
		}
	case p.matchKw("FULL"):
		side = "FULL"
		if p.matchKw("OUTER") {
			kind = "OUTER"
		}
	case p.matchKw("CROSS"):
		kind = "CROSS"
	case p.matchKw("INNER"):
		kind = "INNER"
	case p.matchKw("NATURAL"):
		side = "NATURAL"
	}
	if p.matchKw("JOIN") {
		// ok
	} else if side == "NATURAL" || kind == "CROSS" {
		if !p.matchKw("JOIN") {
			p.pos = save
			return nil, nil
		}
	} else if p.cur().Type == TokPunct && p.cur().Text == "," {
		comma = true
		p.pos++
	} else {
		p.pos = save
		return nil, nil
	}
	t, err := p.parseTableRef()
	if err != nil {
		return nil, err
	}
	j := &Join{Side: side, Kind: kind, This: t, Comma: comma}
	if comma {
		retag(j, t)
		return j, nil
	}
	if p.cur().isKeyword("ON") {
		p.pos++
		on, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		j.On = on
	} else if p.cur().isKeyword("USING") {
		p.pos++
		if !p.matchPunct("(") {
			return nil, p.errUnexpected()
		}
		for {
			tk := p.cur()
			if tk.Type != TokWord && tk.Type != TokIdent {
				return nil, p.errUnexpected()
			}
			p.pos++
			j.Using = append(j.Using, &Ident{Name: tk.Text, Quoted: tk.Type == TokIdent, QuoteChar: tk.QuoteChar})
			if !p.matchPunct(",") {
				break
			}
		}
		if !p.matchPunct(")") {
			return nil, p.errUnexpected()
		}
	}
	retag(j, j.Children()...)
	return j, nil
}

// parseTableRef parses a table reference: table name / subquery / parenthesized table.
func (p *Parser) parseTableRef() (Expression, error) {
	if p.cur().Type == TokPunct && p.cur().Text == "(" {
		p.pos++
		if p.cur().isKeyword("SELECT") || p.cur().isKeyword("WITH") || p.cur().isKeyword("VALUES") {
			q, err := p.parseQuery()
			if err != nil {
				return nil, err
			}
			if !p.matchPunct(")") {
				return nil, p.errUnexpected()
			}
			sub := &Subquery{This: q}
			if !p.parseTableAliasInto(func(a *TableAlias) { sub.Alias = a }) {
				return nil, p.errAt("Expected table name")
			}
			retag(sub, sub.Children()...)
			return sub, nil
		}
		// (t1 JOIN t2 ...) — degrades to verbatim text
		depth := 1
		start := p.pos
		for p.cur().Type != TokEOF && depth > 0 {
			if p.cur().Text == "(" {
				depth++
			} else if p.cur().Text == ")" {
				depth--
				if depth == 0 {
					break
				}
			}
			p.pos++
		}
		text := p.renderTokens(start, p.pos)
		if !p.matchPunct(")") {
			return nil, p.errUnexpected()
		}
		p.parseTableAliasInto(func(a *TableAlias) {})
		return &Unknown{Kind: "table", Text: text}, nil
	}
	tok := p.cur()
	if tok.Type != TokWord && tok.Type != TokIdent {
		return nil, p.errAt("Expected table name")
	}
	p.pos++
	tbl := &Table{Parts: []*Ident{{Name: tok.Text, Quoted: tok.Type == TokIdent, QuoteChar: tok.QuoteChar}}}
	for p.cur().Type == TokPunct && p.cur().Text == "." {
		p.pos++
		nt := p.cur()
		if nt.Type != TokWord && nt.Type != TokIdent {
			return nil, p.errAt("Expected table name")
		}
		p.pos++
		tbl.Parts = append(tbl.Parts, &Ident{Name: nt.Text, Quoted: nt.Type == TokIdent, QuoteChar: nt.QuoteChar})
	}
	if !p.parseTableAliasInto(func(a *TableAlias) { tbl.Alias = a }) {
		return nil, p.errAt("Expected table name")
	}
	retag(tbl, tbl.Children()...)
	return tbl, nil
}

// parseTableAliasInto parses an optional [AS] alias and hands the result to the callback.
func (p *Parser) parseTableAliasInto(set func(*TableAlias)) bool {
	hasAS := p.matchKw("AS")
	tok := p.cur()
	if (tok.Type == TokWord || tok.Type == TokIdent) && !isReservedAfterTable(tok.Text) {
		p.pos++
		a := &TableAlias{Name: &Ident{Name: tok.Text, Quoted: tok.Type == TokIdent, QuoteChar: tok.QuoteChar}}
		if p.cur().Type == TokPunct && p.cur().Text == "(" {
			p.pos++
			for {
				ct := p.cur()
				if ct.Type != TokWord && ct.Type != TokIdent {
					return false
				}
				p.pos++
				a.Columns = append(a.Columns, &Ident{Name: ct.Text, Quoted: ct.Type == TokIdent, QuoteChar: ct.QuoteChar})
				if !p.matchPunct(",") {
					break
				}
			}
			if !p.matchPunct(")") {
				return false
			}
		}
		set(a)
		return true
	}
	if hasAS {
		return false
	}
	return true
}

// isReservedAfterTable reports whether a keyword cannot be used as a table alias.
func isReservedAfterTable(w string) bool {
	switch strings.ToUpper(w) {
	case "WHERE", "GROUP", "HAVING", "ORDER", "LIMIT", "OFFSET", "UNION",
		"INTERSECT", "EXCEPT", "JOIN", "LEFT", "RIGHT", "FULL", "INNER", "CROSS",
		"NATURAL", "ON", "USING", "FETCH", "WINDOW", "QUALIFY", "FOR", "AS",
		"SELECT", "FROM", "AND", "OR", "NOT", "IN", "IS", "LIKE", "BETWEEN", "SET", "VALUES":
		return true
	}
	return false
}

// renderTokens restores tokens in [start,end) to text (fallback path only).
func (p *Parser) renderTokens(start, end int) string {
	var sb strings.Builder
	for i := start; i < end; i++ {
		sb.WriteString(p.toks[i].Text)
		sb.WriteString(" ")
	}
	return strings.TrimSpace(sb.String())
}

// ---- expressions ----

func (p *Parser) parseExpression() (Expression, error) { return p.parseOr() }

func (p *Parser) parseOr() (Expression, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for {
		if !p.matchKw("OR") {
			// MySQL treats `||` as logical OR
			if isMySQLLike(p.name) {
				if op, ok := p.matchOp("||"); ok {
					right, err := p.parseAnd()
					if err != nil {
						return nil, err
					}
					b := &Binary{Op: op, Left: left, Right: right}
					retag(b, left, right)
					left = b
					continue
				}
			}
			return left, nil
		}
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		b := &Binary{Op: "OR", Left: left, Right: right}
		retag(b, left, right)
		left = b
	}
}

func (p *Parser) parseAnd() (Expression, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.matchKw("AND") {
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		b := &Binary{Op: "AND", Left: left, Right: right}
		retag(b, left, right)
		left = b
	}
	return left, nil
}

func (p *Parser) parseNot() (Expression, error) {
	if p.matchKw("NOT") {
		inner, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		u := &Unary{Op: "NOT", This: inner}
		retag(u, inner)
		return u, nil
	}
	return p.parseComparison()
}

func (p *Parser) parseComparison() (Expression, error) {
	left, err := p.parseBitwise()
	if err != nil {
		return nil, err
	}
	for {
		negate := false
		save := p.pos
		if p.cur().isKeyword("NOT") {
			nxt := p.peek(1)
			if nxt.isKeyword("IN") || nxt.isKeyword("LIKE") || nxt.isKeyword("ILIKE") || nxt.isKeyword("BETWEEN") || nxt.isKeyword("RLIKE") {
				p.pos++
				negate = true
			}
		}
		var cur Expression
		switch {
		case p.cur().isKeyword("IN"):
			p.pos++
			in, err := p.parseInTail(left)
			if err != nil {
				return nil, err
			}
			cur = in
		case p.cur().isKeyword("LIKE"), p.cur().isKeyword("ILIKE"), p.cur().isKeyword("RLIKE"):
			op := strings.ToUpper(p.cur().Text)
			p.pos++
			r, err := p.parseBitwise()
			if err != nil {
				return nil, err
			}
			b := &Binary{Op: op, Left: left, Right: r}
			retag(b, left, r)
			cur = b
		case p.cur().isKeyword("BETWEEN"):
			p.pos++
			low, err := p.parseBitwise()
			if err != nil {
				return nil, err
			}
			if !p.matchKw("AND") {
				return nil, p.errAt("Expected AND after BETWEEN")
			}
			high, err := p.parseBitwise()
			if err != nil {
				return nil, err
			}
			b := &Between{This: left, Low: low, High: high}
			retag(b, left, low, high)
			cur = b
		case p.cur().isKeyword("IS"):
			p.pos++
			isNot := p.matchKw("NOT")
			var rhs Expression
			switch {
			case p.matchKw("NULL"):
				rhs = &Null{}
			case p.matchKw("TRUE"):
				rhs = &Boolean{Value: true}
			case p.matchKw("FALSE"):
				rhs = &Boolean{Value: false}
			case p.matchKw("UNKNOWN"):
				rhs = &Null{}
			case p.matchSeq("DISTINCT", "FROM"):
				r, err := p.parseBitwise()
				if err != nil {
					return nil, err
				}
				b := &Binary{Op: "IS DISTINCT FROM", Left: left, Right: r}
				retag(b, left, r)
				cur = b
			default:
				r, err := p.parseBitwise()
				if err != nil {
					return nil, err
				}
				rhs = r
			}
			// sqlglot parses `x IS NOT NULL` as Not(Is(x, NULL));
			// Is itself carries no Not flag.
			is := &Is{This: left, Expr: rhs}
			retag(is, left, rhs)
			cur = is
			if isNot {
				u := &Unary{Op: "NOT", This: cur}
				retag(u, cur)
				cur = u
			}
		default:
			op, ok := p.matchOp("=", "<>", "!=", "<=", ">=", "<", ">", "<=>")
			if !ok {
				p.pos = save
				return left, nil
			}
			r, err := p.parseBitwise()
			if err != nil {
				return nil, err
			}
			b := &Binary{Op: op, Left: left, Right: r}
			retag(b, left, r)
			cur = b
		}
		if negate {
			u := &Unary{Op: "NOT", This: cur}
			retag(u, cur)
			cur = u
		}
		left = cur
	}
}

// parseInTail parses the `(list)` or `(subquery)` following IN.
func (p *Parser) parseInTail(left Expression) (*In, error) {
	if !p.matchPunct("(") {
		return nil, p.errUnexpected()
	}
	in := &In{This: left}
	if p.cur().isKeyword("SELECT") || p.cur().isKeyword("WITH") || p.cur().isKeyword("VALUES") {
		q, err := p.parseQuery()
		if err != nil {
			return nil, err
		}
		if !p.matchPunct(")") {
			return nil, p.errUnexpected()
		}
		sq := &Subquery{This: q}
		in.Query = sq
		retag(in, left, sq)
		return in, nil
	}
	for {
		e, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		in.List = append(in.List, e)
		if !p.matchPunct(",") {
			break
		}
	}
	if !p.matchPunct(")") {
		return nil, p.errUnexpected()
	}
	retag(in, in.Children()...)
	return in, nil
}

func (p *Parser) parseBitwise() (Expression, error) {
	left, err := p.parseAdditive()
	if err != nil {
		return nil, err
	}
	for {
		if isMySQLLike(p.name) {
			// Under MySQL, `||` has already been handled in parseOr
			return left, nil
		}
		op, ok := p.matchOp("||", "|", "&", "^")
		if !ok {
			return left, nil
		}
		right, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		b := &Binary{Op: op, Left: left, Right: right}
		retag(b, left, right)
		left = b
	}
}

func (p *Parser) parseAdditive() (Expression, error) {
	left, err := p.parseMultiplicative()
	if err != nil {
		return nil, err
	}
	for {
		op, ok := p.matchOp("+", "-")
		if !ok {
			return left, nil
		}
		right, err := p.parseMultiplicative()
		if err != nil {
			return nil, err
		}
		b := &Binary{Op: op, Left: left, Right: right}
		retag(b, left, right)
		left = b
	}
}

func (p *Parser) parseMultiplicative() (Expression, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		op, ok := p.matchOp("*", "/", "%")
		if !ok {
			return left, nil
		}
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		b := &Binary{Op: op, Left: left, Right: right}
		retag(b, left, right)
		left = b
	}
}

func (p *Parser) parseUnary() (Expression, error) {
	if p.cur().Type == TokOp && (p.cur().Text == "-" || p.cur().Text == "+") {
		op := p.cur().Text
		p.pos++
		inner, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		// A minus sign before a numeric literal is folded into the literal
		if lit, ok := inner.(*Literal); ok && !lit.IsString {
			return &Literal{Value: op + lit.Value}, nil
		}
		u := &Unary{Op: op, This: inner}
		retag(u, inner)
		return u, nil
	}
	return p.parsePostfix()
}

func (p *Parser) parsePostfix() (Expression, error) {
	e, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for {
		if p.cur().Type == TokOp && p.cur().Text == "::" {
			p.pos++
			tn, err := p.parseTypeName()
			if err != nil {
				return nil, err
			}
			c := &Cast{This: e, To: &DataType{Name: tn}}
			retag(c, e, c.To)
			e = c
			continue
		}
		if p.cur().Type == TokPunct && p.cur().Text == "[" {
			// Subscript access: kept as a verbatim text node
			p.pos++
			idx, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			if !p.matchPunct("]") {
				return nil, p.errUnexpected()
			}
			e = &Bracket{This: e, Index: idx}
			retag(e, e.Children()...)
			continue
		}
		return e, nil
	}
}

// Bracket represents array subscripting a[i].
type Bracket struct {
	base
	This  Expression
	Index Expression
}

func (n *Bracket) Children() []Expression { return []Expression{n.This, n.Index} }
func (n *Bracket) SetChildren(ch []Expression) {
	if len(ch) > 0 {
		n.This = ch[0]
	}
	if len(ch) > 1 {
		n.Index = ch[1]
	}
	retag(n, n.Children()...)
}
func (n *Bracket) Copy() Expression {
	c := &Bracket{}
	if n.This != nil {
		c.This = n.This.Copy()
	}
	if n.Index != nil {
		c.Index = n.Index.Copy()
	}
	c.typ = n.typ
	return c
}
func (n *Bracket) SQL(d *Dialect) string {
	return n.This.SQL(d) + "[" + n.Index.SQL(d) + "]"
}

func (p *Parser) parseTypeName() (string, error) {
	t := p.cur()
	if t.Type != TokWord && t.Type != TokIdent {
		return "", p.errUnexpected()
	}
	p.pos++
	name := strings.ToUpper(t.Text)
	if p.cur().Type == TokPunct && p.cur().Text == "(" {
		p.pos++
		var args []string
		for {
			at := p.cur()
			if at.Type != TokNumber && at.Type != TokWord {
				return "", p.errUnexpected()
			}
			p.pos++
			args = append(args, at.Text)
			if !p.matchPunct(",") {
				break
			}
		}
		if !p.matchPunct(")") {
			return "", p.errUnexpected()
		}
		name += "(" + strings.Join(args, ", ") + ")"
	}
	// Multi-word type names (e.g. DOUBLE PRECISION / CHARACTER VARYING)
	for p.cur().Type == TokWord {
		w := strings.ToUpper(p.cur().Text)
		if w == "PRECISION" || w == "VARYING" {
			name += " " + w
			p.pos++
			continue
		}
		break
	}
	return name, nil
}

func (p *Parser) parsePrimary() (Expression, error) {
	t := p.cur()
	switch {
	case t.Type == TokNumber:
		p.pos++
		return &Literal{Value: t.Text}, nil
	case t.Type == TokString:
		p.pos++
		return &Literal{Value: t.Text, IsString: true}, nil
	case t.Type == TokIdent:
		p.pos++
		return p.parseIdentTail(&Ident{Name: t.Text, Quoted: true, QuoteChar: t.QuoteChar})
	case t.Type == TokWord:
		up := strings.ToUpper(t.Text)
		// Typed literals with a literal suffix (DATE '...' / TIMESTAMP '...' / x'..')
		if (up == "DATE" || up == "TIMESTAMP") && p.peek(1).Type == TokString {
			v := p.peek(1).Text
			p.pos += 2
			return &TypedLiteral{Kind: up, Value: v}, nil
		}
		if up == "X" && p.peek(1).Type == TokString {
			v := p.peek(1).Text
			p.pos += 2
			return &HexString{Value: v}, nil
		}
		if up == "ARRAY" && p.peek(1).Type == TokPunct && p.peek(1).Text == "[" {
			p.pos += 2
			var elems []Expression
			for {
				e, err := p.parseExpression()
				if err != nil {
					return nil, err
				}
				elems = append(elems, e)
				if !p.matchPunct(",") {
					break
				}
			}
			if !p.matchPunct("]") {
				return nil, p.errUnexpected()
			}
			f := &Func{Name: "ARRAY", Args: elems}
			retag(f, f.Children()...)
			return f, nil
		}
		if handledKeyword(up) {
			return p.parseKeywordExpr(up)
		}
		p.pos++
		return p.parseIdentTail(&Ident{Name: t.Text})
	case t.Type == TokPunct && t.Text == "(":
		p.pos++
		// Subquery
		if p.cur().isKeyword("SELECT") || p.cur().isKeyword("WITH") || p.cur().isKeyword("VALUES") {
			q, err := p.parseQuery()
			if err != nil {
				return nil, err
			}
			if !p.matchPunct(")") {
				return nil, p.errUnexpected()
			}
			sub := &Subquery{This: q}
			if al := p.tryParseTableAlias(); al != nil {
				sub.Alias = al
			}
			retag(sub, sub.Children()...)
			return sub, nil
		}
		inner, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if p.cur().Type == TokPunct && p.cur().Text == "," {
			for p.matchPunct(",") {
				if _, err := p.parseExpression(); err != nil {
					return nil, err
				}
			}
			if !p.matchPunct(")") {
				return nil, p.errUnexpected()
			}
			return &Paren{This: inner}, nil
		}
		if !p.matchPunct(")") {
			return nil, p.errUnexpected()
		}
		pr := &Paren{This: inner}
		retag(pr, inner)
		return pr, nil
	case t.Type == TokPunct && t.Text == "*":
		p.pos++
		return &Star{}, nil
	case t.Type == TokUnknown:
		// UNKNOWN ∈ ID_VAR_TOKENS: _parse_field's _parse_id_var fallback treats the raw
		// text as an unquoted identifier, so lexer offsets — and parse-failure positions —
		// match sqlglot. Never reaches the primary/function parsers.
		p.pos++
		return p.parseIdentTail(&Ident{Name: t.Text})
	}
	return nil, p.errUnexpected()
}

// tryParseTableAlias attempts to parse a table alias (without reporting errors).
func (p *Parser) tryParseTableAlias() *TableAlias {
	var out *TableAlias
	if !p.parseTableAliasInto(func(a *TableAlias) { out = a }) {
		return nil
	}
	return out
}

func handledKeyword(kw string) bool {
	switch kw {
	case "CASE", "CAST", "TRY_CAST", "EXISTS", "NULL", "TRUE", "FALSE", "INTERVAL":
		return true
	}
	return false
}

func (p *Parser) parseKeywordExpr(kw string) (Expression, error) {
	switch kw {
	case "CASE":
		return p.parseCase()
	case "NULL":
		p.pos++
		return &Null{}, nil
	case "TRUE":
		p.pos++
		return &Boolean{Value: true}, nil
	case "FALSE":
		p.pos++
		return &Boolean{Value: false}, nil
	case "EXISTS":
		p.pos++
		if !p.matchPunct("(") {
			return nil, p.errUnexpected()
		}
		q, err := p.parseQuery()
		if err != nil {
			return nil, err
		}
		if !p.matchPunct(")") {
			return nil, p.errUnexpected()
		}
		sq := &Subquery{This: q}
		ex := &Exists{This: sq}
		retag(ex, sq)
		return ex, nil
	case "CAST", "TRY_CAST":
		p.pos++
		if !p.matchPunct("(") {
			return nil, p.errUnexpected()
		}
		e, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if !p.matchKw("AS") {
			return nil, p.errUnexpected()
		}
		tn, err := p.parseTypeName()
		if err != nil {
			return nil, err
		}
		if !p.matchPunct(")") {
			return nil, p.errUnexpected()
		}
		c := &Cast{This: e, To: &DataType{Name: tn}}
		if kw == "TRY_CAST" {
			c.Try = true
		}
		retag(c, e, c.To)
		return c, nil
	case "INTERVAL":
		p.pos++
		// INTERVAL '1' DAY / INTERVAL 1 DAY
		v := p.cur()
		var lit *Literal
		if v.Type == TokString {
			p.pos++
			lit = &Literal{Value: v.Text, IsString: true}
		} else if v.Type == TokNumber {
			p.pos++
			lit = &Literal{Value: v.Text, IsString: true}
		} else {
			return nil, p.errUnexpected()
		}
		unit := p.cur()
		if unit.Type == TokWord {
			p.pos++
		} else {
			return nil, p.errUnexpected()
		}
		return &Interval{Value: lit, Unit: strings.ToUpper(unit.Text)}, nil
	case "DISTINCT":
		p.pos++
		e, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		return &Distinct{This: e}, nil
	case "ARRAY":
		if p.peek(1).Type == TokPunct && p.peek(1).Text == "[" {
			p.pos++
			p.pos++
			var elems []Expression
			for {
				e, err := p.parseExpression()
				if err != nil {
					return nil, err
				}
				elems = append(elems, e)
				if !p.matchPunct(",") {
					break
				}
			}
			if !p.matchPunct("]") {
				return nil, p.errUnexpected()
			}
			f := &Func{Name: "ARRAY", Args: elems}
			retag(f, f.Children()...)
			return f, nil
		}
		p.pos++
		return p.parseIdentTail(&Ident{Name: "ARRAY"})
	case "X":
		// x'1F' hex literal: the lexer has already read it as the word X followed by a string
		if p.peek(1).Type == TokString {
			v := p.peek(1).Text
			p.pos += 2
			return &HexString{Value: v}, nil
		}
		p.pos++
		return p.parseIdentTail(&Ident{Name: "X"})
	case "DATE", "TIMESTAMP":
		if p.peek(1).Type == TokString {
			v := p.peek(1).Text
			p.pos += 2
			return &TypedLiteral{Kind: kw, Value: v}, nil
		}
		p.pos++
		return p.parseIdentTail(&Ident{Name: kw})
	case "NOT":
		p.pos++
		inner, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		u := &Unary{Op: "NOT", This: inner}
		retag(u, inner)
		return u, nil
	}
	return nil, p.errUnexpected()
}

// parseCase parses CASE [operand] WHEN ... THEN ... [ELSE ...] END.
func (p *Parser) parseCase() (Expression, error) {
	p.pos++ // CASE
	c := &Case{}
	if !p.cur().isKeyword("WHEN") {
		op, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		c.Operand = op
	}
	for p.matchKw("WHEN") {
		cond, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if !p.matchKw("THEN") {
			return nil, p.errAt("Expected THEN after WHEN")
		}
		then, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		w := &When{Cond: cond, Then: then}
		retag(w, cond, then)
		c.Ifs = append(c.Ifs, w)
	}
	if p.matchKw("ELSE") {
		d, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		c.Default = d
	}
	if !p.matchKw("END") {
		return nil, p.errAt("Expected END after CASE")
	}
	retag(c, c.Children()...)
	return c, nil
}

// parseIdentTail parses what follows an identifier: function call / column reference / alias.
func (p *Parser) parseIdentTail(id *Ident) (Expression, error) {
	if p.cur().Type == TokPunct && p.cur().Text == "(" {
		return p.parseFunc(id.Name, false)
	}
	// Column reference a.b or a.b.c
	parts := []*Ident{id}
	for p.cur().Type == TokPunct && p.cur().Text == "." {
		p.pos++
		if p.cur().Type == TokPunct && p.cur().Text == "*" {
			p.pos++
			// t.*
			return &Star{Table: parts[len(parts)-1]}, nil
		}
		nt := p.cur()
		if nt.Type != TokWord && nt.Type != TokIdent {
			return nil, p.errUnexpected()
		}
		p.pos++
		parts = append(parts, &Ident{Name: nt.Text, Quoted: nt.Type == TokIdent, QuoteChar: nt.QuoteChar})
	}
	if len(parts) > 1 {
		c := &Column{Table: parts[len(parts)-2], Name: parts[len(parts)-1]}
		retag(c, c.Children()...)
		return c, nil
	}
	c := &Column{Name: id}
	retag(c, c.Children()...)
	return c, nil
}

// parseFunc parses a function call and the optional OVER / FILTER.
func (p *Parser) parseFunc(name string, distinct bool) (Expression, error) {
	if !p.matchPunct("(") {
		return nil, p.errUnexpected()
	}
	f := &Func{Name: strings.ToUpper(name)}
	if p.cur().isKeyword("DISTINCT") {
		p.pos++
		f.Distinct = true
	} else {
		p.matchKw("ALL")
	}
	if p.cur().Type == TokPunct && p.cur().Text == "*" && p.peek(1).Text == ")" {
		p.pos++
		f.Star = true
	} else if p.cur().Type == TokPunct && p.cur().Text == ")" {
		// Zero arguments
	} else {
		for {
			e, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			f.Args = append(f.Args, e)
			if !p.matchPunct(",") {
				break
			}
		}
		// Handle aggregate arguments such as ORDER BY / SEPARATOR
		if p.cur().isKeyword("ORDER") && p.peek(1).isKeyword("BY") {
			p.pos += 2
			var parts []string
			for {
				o, err := p.parseOrdered()
				if err != nil {
					return nil, err
				}
				parts = append(parts, o.SQL(GetDialect(p.name)))
				if !p.matchPunct(",") {
					break
				}
			}
			f.Suffix = append(f.Suffix, "ORDER BY "+strings.Join(parts, ", "))
		}
	}
	if !p.matchPunct(")") {
		return nil, p.errUnexpected()
	}
	if p.cur().isKeyword("FILTER") {
		p.pos++
		if !p.matchPunct("(") {
			return nil, p.errUnexpected()
		}
		if !p.matchKw("WHERE") {
			return nil, p.errUnexpected()
		}
		cond, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		w := &WhereClause{This: cond}
		retag(w, cond)
		f.Filter = w
		if !p.matchPunct(")") {
			return nil, p.errUnexpected()
		}
	}
	if p.cur().isKeyword("OVER") {
		p.pos++
		f.Over = p.captureParens()
	}
	retag(f, f.Children()...)
	return f, nil
}

// captureParens captures parenthesized text verbatim (OVER / WINDOW etc.).
func (p *Parser) captureParens() string {
	if p.cur().Type != TokPunct || p.cur().Text != "(" {
		return ""
	}
	depth := 0
	start := p.pos
	for p.cur().Type != TokEOF {
		if p.cur().Text == "(" {
			depth++
		} else if p.cur().Text == ")" {
			depth--
			if depth == 0 {
				p.pos++
				break
			}
		}
		p.pos++
	}
	return p.renderWindowTokens(start, p.pos)
}

// renderWindowTokens restores window definition text in a compact style close to sqlglot's.
func (p *Parser) renderWindowTokens(start, end int) string {
	var sb strings.Builder
	prevWord := false
	for i := start; i < end; i++ {
		t := p.toks[i]
		switch t.Type {
		case TokPunct:
			if t.Text == "," {
				sb.WriteString(", ")
				prevWord = false
				continue
			}
			if t.Text == "." {
				sb.WriteString(".")
				prevWord = false
				continue
			}
			sb.WriteString(t.Text)
			prevWord = false
			continue
		case TokOp:
			sb.WriteString(" " + t.Text + " ")
			prevWord = false
			continue
		case TokWord:
			if prevWord {
				sb.WriteString(" ")
			}
			sb.WriteString(strings.ToUpper(t.Text))
			prevWord = true
			continue
		case TokIdent:
			sb.WriteString("`" + t.Text + "`")
			prevWord = false
			continue
		case TokString:
			sb.WriteString("'" + strings.ReplaceAll(t.Text, "'", "''") + "'")
			prevWord = false
			continue
		}
		if prevWord {
			sb.WriteString(" ")
		}
		sb.WriteString(t.Text)
		prevWord = true
	}
	return strings.TrimSpace(sb.String())
}

func isMySQLLike(d string) bool {
	switch d {
	case "mysql", "starrocks", "doris", "hive", "spark", "spark2", "clickhouse", "singlestore":
		return true
	}
	return false
}
