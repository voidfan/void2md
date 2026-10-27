package sqlg

// The qualify family from sqlglot.optimizer: normalize_identifiers / qualify_tables /
// isolate_table_selects / qualify_columns / validate_qualify_columns / quote_identifiers,
// plus ruleQualify chaining them.

// --- quote_identifiers ---

// QuoteIdentifiers mirrors qualify_columns.quote_identifiers; identify=False behaves as
// can_quote(..., "unsafe"): quote only case-sensitive or unsafe identifiers.
func QuoteIdentifiers(expression Expression, d *Dialect, identify bool) Expression {
	mode := "unsafe"
	if identify {
		mode = "true"
	}
	return Transform(expression, func(n Expression) Expression {
		if id, ok := n.(*Ident); ok {
			id.Quoted = d.CanQuote(id, mode)
		}
		return n
	}, false)
}

// --- normalize_identifiers ---

// NormalizeIdentifiers mirrors normalize_identifiers. storeOriginalColumnIdentifiers is
// skipped: it only matters with a Dot ancestor, and sqlg has no Dot node.
func NormalizeIdentifiers(expression Expression, d *Dialect, _ bool) Expression {
	for _, n := range WalkBFS(expression, nil) {
		switch v := n.(type) {
		case *Ident:
			v.Name = d.NormalizeIdentifier(v.Name, v.Quoted)
		case *Insert:
			// Insert.Columns is flattened out of Children(); normalize explicitly
			// or Oracle's `(name)` would not become `(NAME)`.
			for _, id := range v.Columns {
				id.Name = d.NormalizeIdentifier(id.Name, id.Quoted)
			}
		}
	}
	return expression
}

// --- qualify_tables ---

// aliasNameSeq mirrors sqlglot.helper.name_sequence("_"): yields _0, _1, ...
type aliasNameSeq struct{ n int }

func (s *aliasNameSeq) next() string {
	out := "_" + itoa(s.n)
	s.n++
	return out
}

// QualifyTables mirrors qualify_tables (on the structured path db/catalog are always nil,
// and canonicalize_table_aliases is always False).
func QualifyTables(expression Expression, d *Dialect) Expression {
	nextAliasName := &aliasNameSeq{}

	for _, scope := range TraverseScope(expression) {
		for _, q := range scope.Subqueries() {
			// Python: `subquery.unwrap().replace(subquery)` — only rewrites outer
			// pure-shell Subqueries. Must NOT be Replace(sq, sq.This): the parent
			// (In/Exists/Paren) would lose the subquery, rendering as `IN ()`.
			if sq, ok := q.Parent().(*Subquery); ok {
				Replace(unwrapWrapper(sq), sq)
			}
		}

		for _, dt := range scope.DerivedTables() {
			unnested := unnest(dt)
			if t, ok := unnested.(*Table); ok {
				// derived_table.this.replace(SELECT * FROM <table> KEEP joins)
				inner := subqueryInner(dt)
				if inner != nil {
					sel := &Select{Expressions: []Expression{&Star{}}, From: t.Copy()}
					retag(sel, append([]Expression{sel.From}, sel.Expressions...)...)
					Replace(inner, sel)
				}
			}
			setAlias(dt, "", false, nil, scope, nextAliasName, d)
		}

		for _, name := range scope.Sources().Keys() {
			source, _ := scope.Sources().Get(name)
			t, ok := source.(*Table)
			if !ok {
				continue
			}
			target := name
			if target == "" {
				target = t.Name()
			}
			setAlias(t, target, true, nil, nil, nextAliasName, d)
		}

		// Python checks `isinstance(table.parent, (From, Join))`; sqlg has no From node,
		// so check Join parent OR "this table is exactly some Select's From". Checking
		// only the Select parent would miss tables inside a JOIN (no `AS c2`).
		for _, t := range scope.Tables() {
			if t.Alias != nil {
				continue
			}
			if j, ok := t.Parent().(*Join); ok && j != nil {
				setAlias(t, t.Name(), false, nil, nil, nextAliasName, d)
				continue
			}
			if sel, ok := t.Parent().(*Select); ok && sel != nil && Expression(t) == sel.From {
				setAlias(t, t.Name(), false, nil, nil, nextAliasName, d)
			}
		}
	}
	return expression
}

// subqueryInner returns the inner query node of a derived table.
func subqueryInner(e Expression) Expression {
	if sq, ok := e.(*Subquery); ok {
		return sq.This
	}
	return nil
}

// unwrapWrapper mirrors Subquery.unwrap(): climb while the node is a pure shell
// (Alias == nil) whose parent is also a Subquery.
func unwrapWrapper(sq *Subquery) *Subquery {
	cur := sq
	for {
		if cur.Alias != nil {
			return cur
		}
		p, ok := cur.Parent().(*Subquery)
		if !ok {
			return cur
		}
		cur = p
	}
}

// setAlias mirrors qualify_tables._set_alias (the canonicalize_table_aliases=False branch).
func setAlias(
	expression Expression, targetAlias string, normalize bool,
	columns []string, scope *Scope, seq *aliasNameSeq, d *Dialect,
) {
	alias := aliasOfNode(expression)
	if alias == nil {
		alias = &TableAlias{}
	}

	var newAliasName string
	if alias.Name == nil || alias.Name.Name == "" {
		newAliasName = targetAlias
		if newAliasName == "" {
			newAliasName = seq.next()
		}
		if normalize && targetAlias != "" {
			// Must normalize with the CURRENT dialect; the default dialect would
			// lowercase Oracle's USERS and desync the alias from the source key.
			newAliasName = d.NormalizeIdentifier(newAliasName, false)
		}
	} else {
		return
	}

	alias.Name = NewIdent(newAliasName)
	if len(columns) > 0 {
		alias.Columns = nil
		for _, c := range columns {
			alias.Columns = append(alias.Columns, NewIdent(c))
		}
	}
	setAliasOn(expression, alias)
	retag(expression, alias)

	if scope != nil {
		scope.RenameSource("", newAliasName)
	}
}

// setAliasOn mirrors expression.set("alias", alias).
func setAliasOn(expression Expression, alias *TableAlias) {
	switch v := expression.(type) {
	case *Table:
		v.Alias = alias
	case *Subquery:
		v.Alias = alias
	case *CTE:
		v.Alias = alias
	}
}

// --- isolate_table_selects ---

// IsolateTableSelects mirrors isolate_table_selects. The table body MUST be copied into
// the inner Select (Python's alias() copies by default): hanging t directly would point
// t.Parent() at the inner Select, making Replace(t, sq) land in the wrong place and the
// rule spin without effect.
func IsolateTableSelects(expression Expression, schema *MappingSchema) Expression {
	for _, scope := range TraverseScope(expression) {
		if scope.SelectedSources().len() == 1 {
			continue
		}
		for _, name := range scope.SelectedSources().keys {
			source := scope.SelectedSources().m[name].Node
			t, ok := source.(*Table)
			if !ok {
				continue
			}
			if len(schema.ColumnNames(t)) == 0 {
				continue
			}
			if _, ok := t.Parent().(*Subquery); ok {
				continue
			}
			if p, ok := t.Parent().(*Table); ok && p != nil {
				continue
			}
			if t.Alias == nil {
				panic(&OptimizeError{Msg: "Tables require an alias. Run qualify_tables optimization."})
			}

			inner := t.Copy().(*Table)
			sel := &Select{
				Expressions: []Expression{&Star{}},
				From:        inner,
			}
			sq := &Subquery{
				This:  sel,
				Alias: &TableAlias{Name: NewIdent(t.AliasOrName())},
			}
			retag(sel, inner, sel.Expressions[0])
			retag(sq, sel, sq.Alias)
			Replace(t, sq)
		}
	}
	return expression
}

// --- qualify_columns ---

// PseudocolumnNames mirrors dialect.PSEUDOCOLUMNS (only Oracle is non-empty in the corpus).
func PseudocolumnNames(d *Dialect) map[string]bool {
	if d != nil && d.Name == "oracle" {
		return map[string]bool{
			"OBJECT_VALUE": true, "ROWID": true, "OBJECT_ID": true,
			"LEVEL": true, "ROWNUM": true,
		}
	}
	return nil
}

// QualifyColumns mirrors qualify_columns().
func QualifyColumns(
	expression Expression, schema *MappingSchema, expandAliasRefs, expandStars, allowPartial bool,
) Expression {
	d := schema.dialect
	if d == nil {
		d = GetDialect("")
	}
	inferSchema := schema.Empty()
	pseudocolumns := PseudocolumnNames(d)

	for _, scope := range TraverseScope(expression) {
		scopeExpr := scope.Expression
		_, isSelect := scopeExpr.(*Select)

		_ = pseudocolumns // separate_pseudocolumns: sqlg has no Pseudocolumn node

		resolver := NewResolver(scope, schema, inferSchema)

		popTableColumnAliases(cteExprs(scope.CTEs()))
		popTableColumnAliases(scope.DerivedTables())

		usingColumnTables := expandUsing(scope, resolver)

		if schema.Empty() && expandAliasRefs {
			expandAliasRefsFunc(scope, resolver, d, false)
		}

		qualifyColumns(scope, resolver, allowPartial)

		if !schema.Empty() && expandAliasRefs {
			expandAliasRefsFunc(scope, resolver, d, false)
		}

		if isSelect {
			if expandStars {
				expandStarsFunc(scope, resolver, usingColumnTables)
			}
			qualifyOutputs(scope)
		}

		expandGroupBy(scope)

		expandOrderByAndDistinctOn(scope, resolver)
	}
	return expression
}

func cteExprs(ctes []*CTE) []Expression {
	out := make([]Expression, 0, len(ctes))
	for _, c := range ctes {
		out = append(out, c)
	}
	return out
}

// popTableColumnAliases mirrors _pop_table_column_aliases.
func popTableColumnAliases(derivedTables []Expression) {
	for _, dt := range derivedTables {
		if w, ok := dt.Parent().(*With); ok && w.Recursive {
			continue
		}
		if a := aliasOfNode(dt); a != nil {
			a.Columns = nil
		}
	}
}

// expandUsing mirrors _expand_using.
func expandUsing(scope *Scope, resolver *Resolver) *orderedStrSliceMap {
	columns := newOrderedStrSliceMap()
	updateSourceColumns := func(sourceName string) {
		cols, err := resolver.GetSourceColumns(sourceName, false)
		if err != nil {
			return
		}
		for _, c := range cols {
			if !columns.has(c) {
				columns.set(c, []string{sourceName})
			}
		}
	}

	joins := scope.FindAll(true, func(n Expression) bool { _, ok := n.(*Join); return ok })
	names := map[string]bool{}
	for _, j := range joins {
		names[AliasOrNameOfExpr(j)] = true
	}
	var ordered []string
	for _, k := range scope.SelectedSources().keys {
		if !names[k] {
			ordered = append(ordered, k)
		}
	}
	if len(names) > 0 && len(ordered) == 0 {
		panic(&OptimizeError{Msg: "Joins missing source table " + scope.Expression.SQL(GetDialect(""))})
	}

	columnTables := newOrderedStrSliceMap()

	for _, name := range ordered {
		updateSourceColumns(name)
	}

	for i, join := range joins {
		jd := join.(*Join)
		sourceTable := ""
		if len(ordered) > 0 {
			sourceTable = ordered[len(ordered)-1]
		}
		if sourceTable != "" {
			updateSourceColumns(sourceTable)
		}
		joinTable := AliasOrNameOfExpr(jd)
		ordered = append(ordered, joinTable)

		if len(jd.Using) == 0 {
			continue
		}
		joinColumns, _ := resolver.GetSourceColumns(joinTable, false)
		var conditions []Expression

		for _, ident := range jd.Using {
			identifier := ident.Name
			var table string
			if v := columns.get(identifier); len(v) > 0 {
				table = v[0]
			}
			if table == "" || !containsStr(joinColumns, identifier) {
				if columns.len() > 0 && !columns.has("*") && len(joinColumns) > 0 {
					panic(&OptimizeError{Msg: "Cannot automatically join: " + identifier})
				}
			}
			if table == "" {
				table = sourceTable
			}

			var lhs Expression
			if i == 0 || len(jd.Using) == 1 {
				lhs = &Column{Table: NewIdent(table), Name: NewIdent(identifier)}
				retag(lhs, lhs.(*Column).Table, lhs.(*Column).Name)
			} else {
				var coalesce []Expression
				for _, t := range ordered[:len(ordered)-1] {
					sc, _ := resolver.GetSourceColumns(t, false)
					if containsStr(sc, identifier) {
						c := &Column{Table: NewIdent(t), Name: NewIdent(identifier)}
						retag(c, c.Table, c.Name)
						coalesce = append(coalesce, c)
					}
				}
				if len(coalesce) > 1 {
					lhs = newFunc("COALESCE", coalesce, false)
				} else {
					c := &Column{Table: NewIdent(table), Name: NewIdent(identifier)}
					retag(c, c.Table, c.Name)
					lhs = c
				}
			}

			rhs := &Column{Table: NewIdent(joinTable), Name: NewIdent(identifier)}
			retag(rhs, rhs.Table, rhs.Name)
			eq := &Binary{Op: "=", Left: lhs, Right: rhs}
			retag(eq, lhs, rhs)
			conditions = append(conditions, eq)

			if !columnTables.has(identifier) {
				columnTables.set(identifier, nil)
			}
			tables := columnTables.get(identifier)
			if !containsStr(tables, table) {
				tables = append(tables, table)
			}
			if !containsStr(tables, joinTable) {
				tables = append(tables, joinTable)
			}
			columnTables.set(identifier, tables)
		}

		jd.Using = nil
		jd.On = andAll(conditions)
		retag(jd, jd.This, jd.On)
	}

	if columnTables.len() > 0 {
		for _, column := range scope.Columns() {
			if column.Table != nil {
				continue
			}
			name := identText(column.Name)
			tables := columnTables.get(name)
			if len(tables) == 0 {
				continue
			}
			var args []Expression
			for _, t := range tables {
				c := &Column{Table: NewIdent(t), Name: NewIdent(name)}
				retag(c, c.Table, c.Name)
				args = append(args, c)
			}
			var replacement Expression = newFunc("COALESCE", args, false)
			if _, ok := column.Parent().(*Select); ok {
				al := &Alias{This: replacement, Alias: NewIdent(name)}
				retag(al, replacement, al.Alias)
				replacement = al
			}
			Replace(column, replacement)
		}
	}
	return columnTables
}

// andAll mirrors exp.and_(*expressions): left-folds into an AND chain.
func andAll(conditions []Expression) Expression {
	if len(conditions) == 0 {
		return nil
	}
	out := conditions[0]
	for _, c := range conditions[1:] {
		b := &Binary{Op: "AND", Left: out, Right: c}
		retag(b, out, c)
		out = b
	}
	return out
}

// newFunc constructs a named function-call node.
func newFunc(name string, args []Expression, distinct bool) *Func {
	f := &Func{Name: name, Args: args, Distinct: distinct}
	retag(f, args...)
	return f
}

// --- qualify rule entry point ---

// ruleQualify mirrors the qualify rule as invoked by optimize(): isolate_tables=True,
// quote_identifiers=False, identify=False.
func ruleQualify(e Expression, ctx *OptCtx) Expression {
	d := ctx.Dialect
	e = NormalizeIdentifiers(e, d, true)
	e = QualifyTables(e, d)
	if ctx.IsolateTables {
		e = IsolateTableSelects(e, ctx.Schema)
	}
	e = QualifyColumns(e, ctx.Schema, true, true, false)
	if ctx.QuoteQualify {
		e = QuoteIdentifiers(e, d, ctx.Identify)
	}
	ValidateQualifyColumns(e)
	return e
}
