package sqlg

// Ports sqlglot/optimizer/merge_subqueries.py: merges derived tables / CTEs referenced
// only once into the outer query. Divergences: UNMERGABLE_ARGS covers only
// with/distinct/group/having/limit/offset (the rest are never parsed); window/join-hint/
// explode/query-transform checks are no-ops (no such nodes); Python's
// new_subquery.set("joins", ...) is unrendered dead state and is not reproduced.

// fromOrJoin unifies exp.From / exp.Join; join==nil means the FROM position (sqlg has no
// From wrapper node). node is the From/Join's "this".
type fromOrJoin struct {
	join *Join
	node Expression
}

func (f *fromOrJoin) isJoin() bool { return f.join != nil }

// aliasOrName mirrors from_or_join.alias_or_name.
func (f *fromOrJoin) aliasOrName() string {
	if f.isJoin() {
		return AliasOrNameOfExpr(f.join.This)
	}
	return AliasOrNameOfExpr(f.node)
}

// findAncestorFromOrJoin mirrors node.find_ancestor(exp.From, exp.Join). With no From
// node, "being some Select's From child" counts as a From hit — it must be judged via the
// child, else in `FROM a JOIN (SELECT ... FROM t) x` t's ancestor is misread as the Join.
func findAncestorFromOrJoin(node Expression) *fromOrJoin {
	child := node
	cur := node.Parent()
	for cur != nil {
		switch v := cur.(type) {
		case *Join:
			return &fromOrJoin{join: v, node: v.This}
		case *Select:
			if v.From == child {
				return &fromOrJoin{node: v.From}
			}
		}
		child = cur
		cur = cur.Parent()
	}
	return nil
}

// MergeSubqueries mirrors merge_subqueries.
func MergeSubqueries(expression Expression, leaveTablesIsolated bool) Expression {
	expression = mergeCTEs(expression, leaveTablesIsolated)
	expression = mergeDerivedTables(expression, leaveTablesIsolated)
	return expression
}

// cteSelection mirrors one (outer_scope, inner_scope, table) entry from cte_selections.
type cteSelection struct {
	outer *Scope
	inner *Scope
	table Expression
}

func mergeCTEs(expression Expression, leaveTablesIsolated bool) Expression {
	scopes := TraverseScope(expression)

	// cte_selections groups by inner-scope pointer identity (Python keys by id(scope)).
	var order []*Scope
	byInner := map[*Scope][]cteSelection{}

	for _, outer := range scopes {
		ss := outer.SelectedSources()
		for _, name := range ss.keys {
			s := ss.m[name]
			inner, ok := s.Source.(*Scope)
			if !ok {
				continue
			}
			if inner.ScopeType != ScopeCTE {
				continue
			}
			if _, seen := byInner[inner]; !seen {
				order = append(order, inner)
			}
			byInner[inner] = append(byInner[inner], cteSelection{outer, inner, s.Node})
		}
	}

	for _, inner := range order {
		v := byInner[inner]
		if len(v) != 1 {
			continue
		}
		outer, innerScope, table := v[0].outer, v[0].inner, v[0].table
		fromOrJoin := findAncestorFromOrJoin(table)
		if !mergeable(outer, innerScope, leaveTablesIsolated, fromOrJoin) {
			continue
		}
		alias := AliasOrNameOfExpr(table)
		renameInnerSources(outer, innerScope, alias)
		mergeFrom(outer, innerScope, table, alias)
		mergeExpressions(outer, innerScope, alias)
		mergeOrder(outer, innerScope)
		mergeJoins(outer, innerScope, fromOrJoin)
		mergeWhere(outer, innerScope, fromOrJoin)
		popCTE(innerScope)
		outer.clearCache()
	}
	return expression
}

func mergeDerivedTables(expression Expression, leaveTablesIsolated bool) Expression {
	for _, outer := range TraverseScope(expression) {
		for _, subquery := range outer.DerivedTables() {
			fromOrJoin := findAncestorFromOrJoin(subquery)
			alias := AliasOrNameOfExpr(subquery)
			src, ok := outer.Sources().Get(alias)
			if !ok {
				continue
			}
			innerScope, ok := src.(*Scope)
			if !ok {
				continue
			}
			if !mergeable(outer, innerScope, leaveTablesIsolated, fromOrJoin) {
				continue
			}
			renameInnerSources(outer, innerScope, alias)
			mergeFrom(outer, innerScope, subquery, alias)
			mergeExpressions(outer, innerScope, alias)
			mergeOrder(outer, innerScope)
			mergeJoins(outer, innerScope, fromOrJoin)
			mergeWhere(outer, innerScope, fromOrJoin)
			outer.clearCache()
		}
	}
	return expression
}

// mergeable mirrors _mergeable.
func mergeable(outer, inner *Scope, leaveTablesIsolated bool, fromOrJoin *fromOrJoin) bool {
	outerSel, ok := outer.Expression.(*Select)
	if !ok {
		return false
	}
	innerSelect, ok := unnest(inner.Expression).(*Select)
	if !ok {
		return false
	}

	if outerSelectIsStar(outerSel) {
		return false
	}
	// UNMERGABLE_ARGS (the part expressible in sqlg)
	if innerSelect.With != nil || innerSelect.Distinct || innerSelect.Group != nil ||
		innerSelect.Having != nil || innerSelect.Limit != nil || innerSelect.Offset != nil {
		return false
	}
	if innerSelect.From == nil {
		return false
	}
	// outer.pivots: sqlg has no pivots node → always empty
	if innerSelectHasUnmergableProjection(innerSelect) {
		return false
	}
	// When leave_tables_isolated is False this branch does not apply
	if leaveTablesIsolated && outer.SelectedSources().len() > 1 {
		return false
	}
	if fromOrJoin != nil && fromOrJoin.isJoin() {
		if len(innerSelect.Joins) > 0 {
			return false
		}
		side := fromOrJoin.join.Side
		if innerSelect.Where != nil && (side == "FULL" || side == "LEFT" || side == "RIGHT") {
			return false
		}
	} else if fromOrJoin != nil {
		if innerSelect.Where != nil {
			for _, j := range outerSel.Joins {
				if j.Side == "FULL" || j.Side == "RIGHT" {
					return false
				}
			}
		}
	}
	if outerSelectJoinsOnInnerSelectJoin(fromOrJoin, inner) {
		return false
	}
	// _is_a_window_expression_in_unmergable_operation: sqlg has no Window node → always False
	if isRecursive(outer, inner) {
		return false
	}
	if innerSelect.Order != nil && outer.ScopeType == ScopeUnion {
		return false
	}
	// seq_get(inner_select.expressions, 0) being a QueryTransform: sqlg has no such node → always False
	return true
}

// outerSelectIsStar mirrors Select.is_star: any expression is Star or Column(t.*).
func outerSelectIsStar(sel *Select) bool {
	for _, e := range sel.Expressions {
		if isStarExpr(e) {
			return true
		}
	}
	return false
}

// innerSelectHasUnmergableProjection mirrors
// `any(e.find(exp.AggFunc, exp.Select, exp.Explode) for e in inner_select.expressions)`.
func innerSelectHasUnmergableProjection(innerSelect *Select) bool {
	for _, e := range innerSelect.Expressions {
		if HasNode(e, func(n Expression) bool {
			switch v := n.(type) {
			case *Select:
				return true
			case *Func:
				return isAggFunc(v)
			}
			return false
		}) {
			return true
		}
	}
	return false
}

// outerSelectJoinsOnInnerSelectJoin mirrors _outer_select_joins_on_inner_select_join.
func outerSelectJoinsOnInnerSelectJoin(fromOrJoin *fromOrJoin, inner *Scope) bool {
	if fromOrJoin == nil || !fromOrJoin.isJoin() {
		return false
	}
	alias := fromOrJoin.aliasOrName()
	on := fromOrJoin.join.On
	if on == nil {
		return false
	}
	var selections []string
	for _, c := range FindAllOf[*Column](on, true) {
		if c.Table != nil && c.Table.Name == alias {
			selections = append(selections, colName(c))
		}
	}
	innerExpr, ok := unnest(inner.Expression).(*Select)
	if !ok || innerExpr.From == nil {
		return false
	}
	innerFromTable := AliasOrNameOfExpr(innerExpr.From)

	innerProjections := map[string]Expression{}
	for _, s := range innerExpr.Expressions {
		innerProjections[AliasOrNameOfExpr(s)] = s
	}
	for _, selection := range selections {
		proj, ok := innerProjections[selection]
		if !ok {
			// Python raises KeyError here; treated as "not optimizable"
			return true
		}
		for _, col := range FindAllOf[*Column](proj, true) {
			if col.Table == nil || col.Table.Name != innerFromTable {
				return true
			}
		}
	}
	return false
}

// isRecursive mirrors _is_recursive.
func isRecursive(outer, inner *Scope) bool {
	// scopeOuterNode smooths sqlg's extra Subquery layer (Python: parent is the CTE)
	cte := scopeOuterNode(inner)
	node := outer.Expression.Parent()
	for node != nil {
		if node == cte {
			return true
		}
		node = node.Parent()
	}
	return false
}

// renameInnerSources mirrors _rename_inner_sources.
func renameInnerSources(outer, inner *Scope, alias string) {
	innerTaken := map[string]bool{}
	for _, k := range inner.SelectedSources().keys {
		innerTaken[k] = true
	}
	outerTaken := map[string]bool{}
	for _, k := range outer.SelectedSources().keys {
		outerTaken[k] = true
	}

	taken := map[string]bool{}
	for k := range outerTaken {
		taken[k] = true
	}
	for k := range innerTaken {
		taken[k] = true
	}

	// conflicts = (outer ∩ inner) - {alias}. Python iterates in CPython set order;
	// here we use inner selected_sources insertion order (conflicts rarely exceed 1).
	for _, conflict := range inner.SelectedSources().keys {
		if !outerTaken[conflict] || conflict == alias {
			continue
		}
		newName := findNewNameIn(taken, conflict)
		taken[newName] = true

		src, _ := inner.SelectedSources().m[conflict]
		// Python takes the tuple's FIRST element (the node, not the inner Scope)
		switch v := src.Node.(type) {
		case *Table:
			if v.Alias != nil {
				v.Alias = &TableAlias{Name: NewIdent(newName)}
				v.Alias.SetParent(v)
			} else {
				replaceWithAlias(v, newName)
			}
		default:
			if p, ok := src.Node.Parent().(*Subquery); ok {
				p.Alias = &TableAlias{Name: NewIdent(newName)}
				p.Alias.SetParent(p)
			}
		}

		for _, column := range inner.SourceColumns(conflict) {
			column.Table = NewIdent(newName)
		}
		inner.RenameSource(conflict, newName)
	}
}

// replaceWithAlias mirrors `source.replace(exp.alias_(source, new_alias))`: sets the
// Table's alias in place and returns the same node.
func replaceWithAlias(t *Table, name string) {
	t.Alias = &TableAlias{Name: NewIdent(name)}
	t.Alias.SetParent(t)
}

// mergeFrom mirrors _merge_from.
func mergeFrom(outer, inner *Scope, nodeToReplace Expression, alias string) {
	innerSelect, _ := unnest(inner.Expression).(*Select)
	if innerSelect == nil || innerSelect.From == nil {
		return
	}
	newSubquery := innerSelect.From
	// Python also sets the unrendered "joins" arg here; skipped (see file header)
	Replace(nodeToReplace, newSubquery)

	outer.RemoveSource(alias)
	if src, ok := inner.Sources().Get(AliasOrNameOfExpr(newSubquery)); ok {
		outer.AddSource(AliasOrNameOfExpr(newSubquery), src)
	}
}

// mergeJoins mirrors _merge_joins.
func mergeJoins(outer, inner *Scope, fromOrJoin *fromOrJoin) {
	if fromOrJoin == nil {
		return
	}
	innerSelect, ok := unnest(inner.Expression).(*Select)
	if !ok {
		return
	}
	newJoins := make([]*Join, 0, len(innerSelect.Joins))
	for _, join := range innerSelect.Joins {
		join.SetParent(nil)
		newJoins = append(newJoins, join)
		if src, ok := inner.Sources().Get(AliasOrNameOfExpr(join.This)); ok {
			outer.AddSource(AliasOrNameOfExpr(join.This), src)
		}
	}
	if len(newJoins) == 0 {
		return
	}
	outerSelect, ok := outer.Expression.(*Select)
	if !ok {
		return
	}
	position := 0
	if fromOrJoin.isJoin() {
		position = indexOfJoin(outerSelect.Joins, fromOrJoin.join) + 1
	}
	outerSelect.Joins = insertJoins(outerSelect.Joins, position, newJoins)
	innerSelect.Joins = nil
	retag(outerSelect, outerSelect.Children()...)
}

func indexOfJoin(joins []*Join, target *Join) int {
	for i, j := range joins {
		if j == target {
			return i
		}
	}
	return -1
}

func insertJoins(joins []*Join, pos int, add []*Join) []*Join {
	if pos < 0 {
		pos = 0
	}
	if pos > len(joins) {
		pos = len(joins)
	}
	out := make([]*Join, 0, len(joins)+len(add))
	out = append(out, joins[:pos]...)
	out = append(out, add...)
	out = append(out, joins[pos:]...)
	return out
}

// mergeExpressions mirrors _merge_expressions. Python rebinds `expression` inside the
// column loop (paren/alias wrappings accumulate across columns); reproduced verbatim.
func mergeExpressions(outer, inner *Scope, alias string) {
	innerSelect, ok := unnest(inner.Expression).(*Select)
	if !ok {
		return
	}

	outerColumns := map[string][]*Column{}
	for _, column := range outer.Columns() {
		if column.Table != nil && column.Table.Name == alias {
			outerColumns[colName(column)] = append(outerColumns[colName(column)], column)
		}
	}

	for _, proj := range innerSelect.Expressions {
		projectionName := AliasOrNameOfExpr(proj)
		if projectionName == "" {
			continue
		}
		columnsToReplace := outerColumns[projectionName]

		expression := exprUnalias(proj)
		mustWrap := !isSafeToReplaceUnwrapped(expression)

		for _, column := range columnsToReplace {
			if isUnaryOrBinary(column.Parent()) && mustWrap {
				expression = wrapParen(expression)
			}
			if _, isSel := column.Parent().(*Select); isSel && colName(column) != exprName(expression) {
				expression = aliasOfExpr(expression, colName(column))
			}
			Replace(column, expression.Copy())
		}
	}
}

// isSafeToReplaceUnwrapped mirrors SAFE_TO_REPLACE_UNWRAPPED.
func isSafeToReplaceUnwrapped(e Expression) bool {
	switch v := e.(type) {
	case *Column:
		return true
	case *Binary:
		return v.Op == "=" || v.Op == "<>"
	case *Paren:
		return true
	case *Func:
		return true
	}
	return false
}

// wrapParen mirrors exp.paren(expression, copy=False).
func wrapParen(e Expression) Expression {
	p := &Paren{This: e}
	e.SetParent(p)
	return p
}

// aliasOfExpr mirrors exp.alias_(expression, name) (copy=False): nodes with an alias arg
// (Alias/Table/Subquery) are written in place; others are wrapped in a new Alias.
func aliasOfExpr(e Expression, name string) Expression {
	switch v := e.(type) {
	case *Alias:
		v.Alias = NewIdent(name)
		return v
	case *Table:
		v.Alias = &TableAlias{Name: NewIdent(name)}
		v.Alias.SetParent(v)
		return v
	case *Subquery:
		v.Alias = &TableAlias{Name: NewIdent(name)}
		v.Alias.SetParent(v)
		return v
	}
	a := &Alias{This: e, Alias: NewIdent(name)}
	e.SetParent(a)
	return a
}

// exprUnalias mirrors Expression.unalias.
func exprUnalias(e Expression) Expression {
	if a, ok := e.(*Alias); ok {
		return a.This
	}
	return e
}

func isUnaryOrBinary(e Expression) bool {
	switch e.(type) {
	case *Unary, *Binary:
		return true
	}
	return false
}

// mergeWhere mirrors _merge_where.
func mergeWhere(outer, inner *Scope, fromOrJoin *fromOrJoin) {
	innerSelect, ok := unnest(inner.Expression).(*Select)
	if !ok || innerSelect.Where == nil {
		return
	}
	whereExpr := innerSelect.Where
	outerSelect, ok := outer.Expression.(*Select)
	if !ok {
		return
	}

	if fromOrJoin != nil && fromOrJoin.isJoin() {
		sources := map[string]bool{}
		if outerSelect.From != nil {
			sources[AliasOrNameOfExpr(outerSelect.From)] = true
		}
		for _, join := range outerSelect.Joins {
			source := AliasOrNameOfExpr(join.This)
			sources[source] = true
			if source == fromOrJoin.aliasOrName() {
				break
			}
		}
		if isSubset(columnTableNames(whereExpr, ""), sources) {
			joinPrependOn(fromOrJoin.join, whereExpr)
			return
		}
	}
	// Python `expression.where(where.this, copy=False)`: APPEND semantics
	// (and_(outer, inner)), not an overwrite.
	appendWhere(outerSelect, whereExpr)
}

// appendWhere mirrors Select.where(cond, copy=False, append=True). sqlg's Select.Where
// stores the condition itself (no Where node), so the AND result is assigned directly.
func appendWhere(sel *Select, cond Expression) {
	if cond == nil {
		return
	}
	node := cond
	if sel.Where != nil {
		node = combineAnd([]Expression{sel.Where, cond})
	}
	sel.Where = node
	// combineAnd may return cond as-is, in which case its parent is still the inner Select.
	node.SetParent(sel)
}

// joinPrependOn mirrors join.on(pred, copy=False): and_(existing_on, pred).
func joinPrependOn(j *Join, pred Expression) {
	existing := j.On
	if existing == nil {
		existing = pred
	} else {
		existing = combineAnd([]Expression{existing, pred})
	}
	j.On = existing
	j.On.SetParent(j)
}

// combineAnd mirrors exp.and_(*expressions, copy=False) (including _wrap).
func combineAnd(expressions []Expression) Expression {
	cond := make([]Expression, 0, len(expressions))
	for _, e := range expressions {
		if e != nil {
			cond = append(cond, e)
		}
	}
	if len(cond) == 0 {
		return nil
	}
	this := cond[0]
	if len(cond) > 1 {
		this = wrapConnector(this)
	}
	for _, e := range cond[1:] {
		n := &Binary{Op: "AND", Left: this, Right: wrapConnector(e)}
		retag(n, n.Children()...)
		this = n
	}
	return this
}

// wrapConnector mirrors _wrap: Connector (AND/OR) nodes get parenthesized.
func wrapConnector(e Expression) Expression {
	if b, ok := e.(*Binary); ok && (b.Op == "AND" || b.Op == "OR") {
		p := &Paren{This: e}
		e.SetParent(p)
		return p
	}
	return e
}

// mergeOrder mirrors _merge_order.
func mergeOrder(outer, inner *Scope) {
	outerSelect, ok := outer.Expression.(*Select)
	if !ok {
		return
	}
	innerSelect, ok := unnest(inner.Expression).(*Select)
	if !ok {
		return
	}
	if outerSelect.Group != nil || outerSelect.Distinct || outerSelect.Having != nil ||
		outerSelect.Order != nil {
		return
	}
	if outer.SelectedSources().len() != 1 {
		return
	}
	for _, e := range outerSelect.Expressions {
		if HasNode(e, func(n Expression) bool {
			f, ok := n.(*Func)
			return ok && isAggFunc(f)
		}) {
			return
		}
	}
	outerSelect.Order = innerSelect.Order
	if outerSelect.Order != nil {
		outerSelect.Order.SetParent(outerSelect)
	}
}

// popCTE mirrors _pop_cte.
func popCTE(inner *Scope) {
	// Same as isRecursive: sqlglot's inner.expression.parent is the CTE itself;
	// sqlg needs scopeOuterNode to smooth over the extra Subquery layer.
	cte, ok := scopeOuterNode(inner).(*CTE)
	if !ok {
		return
	}
	with, ok := cte.Parent().(*With)
	if !ok {
		return
	}
	if len(with.CTEs) == 1 {
		Pop(with)
		return
	}
	Pop(cte)
}

// ---- General-purpose small helpers ----

// findNewNameIn mirrors sqlglot.helper.find_new_name (counter starts at 2).
func findNewNameIn(taken map[string]bool, base string) string {
	if !taken[base] {
		return base
	}
	i := 2
	for {
		name := base + "_" + itoa(i)
		if !taken[name] {
			return name
		}
		i++
	}
}

// columnTableNames mirrors exp.column_table_names.
func columnTableNames(e Expression, exclude string) map[string]bool {
	out := map[string]bool{}
	for _, c := range FindAllOf[*Column](e, true) {
		if c.Table != nil && c.Table.Name != "" && c.Table.Name != exclude {
			out[c.Table.Name] = true
		}
	}
	return out
}

func isSubset(a, b map[string]bool) bool {
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// exprName mirrors Expression.name (= self.text("this"), overloaded for sqlg's node set).
func exprName(e Expression) string {
	switch v := e.(type) {
	case *Column:
		return colName(v)
	case *Ident:
		return v.Name
	case *Literal:
		return v.Value
	case *Star:
		return "*"
	case *Null:
		return "NULL"
	case *Func:
		// COUNT(*) is Count(this=Star) → "*"; COUNT(t.*) has a Column this → ""
		if v.Star {
			return "*"
		}
		if len(v.Args) > 0 {
			switch a := v.Args[0].(type) {
			case *Ident:
				return a.Name
			case *Literal:
				return a.Value
			}
		}
		return ""
	}
	return ""
}

// SourceColumns mirrors Scope.source_columns.
func (s *Scope) SourceColumns(name string) []*Column {
	var out []*Column
	for _, c := range s.Columns() {
		if c.Table != nil && c.Table.Name == name {
			out = append(out, c)
		}
	}
	return out
}

// RemoveSource mirrors Scope.remove_source.
func (s *Scope) RemoveSource(name string) {
	if s.sources != nil {
		s.sources.Del(name)
	}
	s.clearCache()
}

// AddSource mirrors Scope.add_source.
func (s *Scope) AddSource(name string, source any) {
	if s.sources == nil {
		s.sources = newSourceMap()
	}
	s.sources.Set(name, source)
	s.clearCache()
}
