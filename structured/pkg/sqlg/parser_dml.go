package sqlg

import "strings"

// Minimal DML/DDL parsing (structured only needs verbatim normalized output).

func (p *Parser) parseInsert() (Expression, error) {
	p.pos++ // INSERT
	p.matchKw("IGNORE")
	p.matchKw("OVERWRITE")
	if !p.matchKw("INTO") {
		return nil, p.errAt("Expected INTO after INSERT")
	}
	t, err := p.parseTableRef()
	if err != nil {
		return nil, err
	}
	ins := &Insert{Table: t}
	if p.cur().Type == TokPunct && p.cur().Text == "(" && !p.peek(1).isKeyword("SELECT") {
		p.pos++
		for {
			tk := p.cur()
			if tk.Type != TokWord && tk.Type != TokIdent {
				return nil, p.errUnexpected()
			}
			p.pos++
			ins.Columns = append(ins.Columns, &Ident{Name: tk.Text, Quoted: tk.Type == TokIdent, QuoteChar: tk.QuoteChar})
			if !p.matchPunct(",") {
				break
			}
		}
		if !p.matchPunct(")") {
			return nil, p.errUnexpected()
		}
	}
	switch {
	case p.cur().isKeyword("VALUES"):
		p.pos++
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
			ins.Values = append(ins.Values, row)
			if !p.matchPunct(",") {
				break
			}
		}
	case p.cur().isKeyword("SELECT") || p.cur().isKeyword("WITH"):
		q, err := p.parseQuery()
		if err != nil {
			return nil, err
		}
		ins.Query = q
	}
	retag(ins, ins.Children()...)
	return ins, nil
}

func (p *Parser) parseUpdate() (Expression, error) {
	p.pos++ // UPDATE
	t, err := p.parseTableRef()
	if err != nil {
		return nil, err
	}
	u := &Update{Table: t}
	if !p.matchKw("SET") {
		return nil, p.errAt("Expected SET after UPDATE")
	}
	for {
		k, err := p.parsePostfix()
		if err != nil {
			return nil, err
		}
		if _, ok := p.matchOp("="); !ok {
			return nil, p.errAt("Expected = in SET clause")
		}
		v, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		u.Set = append(u.Set, [2]Expression{k, v})
		if !p.matchPunct(",") {
			break
		}
	}
	if p.cur().isKeyword("WHERE") {
		p.pos++
		w, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		u.Where = w
	}
	retag(u, u.Children()...)
	return u, nil
}

func (p *Parser) parseDelete() (Expression, error) {
	p.pos++ // DELETE
	p.matchKw("FROM")
	t, err := p.parseTableRef()
	if err != nil {
		return nil, err
	}
	d := &Delete{Table: t}
	if p.cur().isKeyword("WHERE") {
		p.pos++
		w, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		d.Where = w
	}
	retag(d, d.Children()...)
	return d, nil
}

func (p *Parser) parseDrop() (Expression, error) {
	p.pos++ // DROP
	kinds := []string{"TABLE", "VIEW", "INDEX", "DATABASE", "SCHEMA", "FUNCTION"}
	kind := ""
	for _, k := range kinds {
		if p.matchKw(k) {
			kind = k
			break
		}
	}
	if kind == "" {
		return nil, p.errAt("Expected object kind after DROP")
	}
	p.matchKw("IF")
	p.matchKw("EXISTS")
	t, err := p.parseTableRef()
	if err != nil {
		return nil, err
	}
	d := &Drop{Kind: kind, This: t}
	retag(d, d.Children()...)
	return d, nil
}

// parseCreate parses `CREATE <kind> <table> [(<col> <type>, ...)]`.
// Real Table/Ident/DataType nodes are required: Oracle identifier upper-casing and
// sqlite type rewriting operate on them.
func (p *Parser) parseCreate() (Expression, error) {
	p.pos++ // CREATE
	// OR REPLACE / TEMPORARY prefixes are skipped (not in the structured corpus).
	if p.matchKw("OR") {
		p.matchKw("REPLACE")
	}
	p.matchKw("TEMPORARY")
	kind := ""
	for _, k := range []string{"TABLE", "VIEW", "INDEX", "DATABASE", "SCHEMA", "FUNCTION"} {
		if p.matchKw(k) {
			kind = k
			break
		}
	}
	if kind == "" {
		return nil, p.errAt("Expected object kind after CREATE")
	}
	p.matchKw("IF")
	p.matchKw("NOT")
	p.matchKw("EXISTS")

	t, err := p.parseTableRef()
	if err != nil {
		return nil, err
	}
	sch := &Schema{This: t}
	if p.cur().Type == TokPunct && p.cur().Text == "(" {
		p.pos++
		for {
			id, err := p.parseIdentLike()
			if err != nil {
				return nil, err
			}
			def := &ColumnDef{This: id}
			// Type name (with args, e.g. VARCHAR(10)) captured verbatim; rendering
			// goes through Dialect.renderTypeName.
			if p.cur().Type == TokWord || p.cur().Type == TokIdent {
				start := p.pos
				p.pos++
				if p.cur().Type == TokPunct && p.cur().Text == "(" {
					depth := 0
					for p.cur().Type != TokEOF {
						if p.cur().Type == TokPunct && p.cur().Text == "(" {
							depth++
						} else if p.cur().Type == TokPunct && p.cur().Text == ")" {
							depth--
							if depth == 0 {
								p.pos++
								break
							}
						}
						p.pos++
					}
				}
				def.Kind = &DataType{Name: p.renderTokens(start, p.pos)}
			}
			retag(def, def.Children()...)
			sch.Expressions = append(sch.Expressions, def)
			if !p.matchPunct(",") {
				break
			}
		}
		if !p.matchPunct(")") {
			return nil, p.errUnexpected()
		}
	}
	retag(sch, sch.Children()...)
	c := &Create{Kind: kind, This: sch}
	retag(c, c.Children()...)
	return c, nil
}

// parseIdentLike reads a bare identifier/keyword as an identifier node.
func (p *Parser) parseIdentLike() (*Ident, error) {
	tk := p.cur()
	if tk.Type != TokWord && tk.Type != TokIdent {
		return nil, p.errUnexpected()
	}
	p.pos++
	return &Ident{Name: tk.Text, Quoted: tk.Type == TokIdent, QuoteChar: tk.QuoteChar}, nil
}

// parseDescribe parses `DESCRIBE <table>`.
func (p *Parser) parseDescribe() (Expression, error) {
	p.pos++ // DESCRIBE / DESC
	t, err := p.parseTableRef()
	if err != nil {
		return nil, err
	}
	d := &Describe{This: t}
	retag(d, d.Children()...)
	return d, nil
}

// parseShow parses MySQL/StarRocks `SHOW <what>`; only these dialects treat SHOW as a
// statement node, others pass through verbatim (see parseStatement).
func (p *Parser) parseShow() (Expression, error) {
	p.pos++ // SHOW
	var parts []string
	for p.cur().Type != TokEOF && !(p.cur().Type == TokPunct && p.cur().Text == ";") {
		parts = append(parts, p.cur().Text)
		p.pos++
	}
	s := &Show{This: strings.Join(parts, " ")}
	retag(s, s.Children()...)
	return s, nil
}

// parseCommandPassthrough passes a whole statement through verbatim (e.g. SHOW in postgres).
func (p *Parser) parseCommandPassthrough() (Expression, error) {
	start := p.pos
	for p.cur().Type != TokEOF && !(p.cur().Type == TokPunct && p.cur().Text == ";") {
		p.pos++
	}
	return &Command{Text: strings.Join(strings.Fields(p.renderTokens(start, p.pos)), " ")}, nil
}
