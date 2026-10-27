package sqlg

import "fmt"

// Ports sqlglot/optimizer/eliminate_subqueries.py: rewrites derived tables into CTEs and
// de-duplicates structurally identical subqueries. sqlglot's __eq__/__hash__ are
// structural, so sqlg matches via structKey() instead of pointer identity.
// Divergences: PIVOT/LATERAL are never parsed (conditions always false), and sqlg's
// CTE.This is a *Subquery (sqlglot's is a Select directly) — scopeOuterNode normalizes.

// structKey reproduces sqlglot's structural hash: "type name + compact SQL in the default
// dialect". Same structure <=> same SQL text for parser-produced nodes.
func structKey(e Expression) string {
	if isNilExpr(e) {
		return ""
	}
	return fmt.Sprintf("%T|%s", e, e.SQL(GetDialect("")))
}

// scopeOuterNode mirrors `scope.expression.parent`, normalizing sqlg's extra Subquery
// layer so a CTE scope's outer node is the CTE itself.
func scopeOuterNode(scope *Scope) Expression {
	p := scope.Expression.Parent()
	if scope.ScopeType == ScopeCTE {
		if sq, ok := p.(*Subquery); ok {
			if c, ok := sq.Parent().(*CTE); ok {
				return c
			}
		}
	}
	return p
}

// outerAlias mirrors `scope.expression.parent.alias`.
func outerAlias(scope *Scope) string {
	switch v := scopeOuterNode(scope).(type) {
	case *Subquery:
		return exprAlias(v)
	case *CTE:
		return cteName(v)
	}
	return ""
}

// EliminateSubqueries mirrors eliminate_subqueries.
func EliminateSubqueries(expression Expression) Expression {
	// The root node may be a bare Subquery, e.g. `(SELECT * FROM x) LIMIT 1`
	if sq, ok := expression.(*Subquery); ok {
		EliminateSubqueries(sq.This)
		return expression
	}

	root := BuildScope(expression)
	if root == nil {
		return expression
	}

	// taken: name -> *Scope | *Table
	taken := map[string]any{}

	// All CTE aliases of the root scope count as taken
	for _, scope := range root.CTEScopes {
		taken[outerAlias(scope)] = scope
	}
	// All table names count as taken
	for _, scope := range root.Traverse() {
		for _, k := range scope.Sources().Keys() {
			src, _ := scope.Sources().Get(k)
			if t, ok := src.(*Table); ok {
				taken[t.Name()] = t
			}
		}
	}

	existingCTEs := map[string]string{}

	with := withOf(root.Expression)
	recursive := false
	if with != nil {
		recursive = with.Recursive
		for _, cte := range with.CTEs {
			existingCTEs[structKey(cteInner(cte))] = cteName(cte)
		}
	}
	var newCTEs []*CTE

	// New CTEs must preserve DAG order: derived tables inside an existing CTE must come before it.
	for _, cteScope := range root.CTEScopes {
		for _, scope := range cteScope.Traverse() {
			if scope == cteScope {
				continue
			}
			if nc := eliminateScope(scope, existingCTEs, taken); nc != nil {
				newCTEs = append(newCTEs, nc)
			}
		}
		// The existing CTE body itself is appended afterwards
		if c, ok := scopeOuterNode(cteScope).(*CTE); ok {
			newCTEs = append(newCTEs, c)
		}
	}

	// The remaining scopes
	chain := make([]*Scope, 0, len(root.UnionScopes)+len(root.SubqueryScopes)+len(root.TableScopes))
	chain = append(chain, root.UnionScopes...)
	chain = append(chain, root.SubqueryScopes...)
	chain = append(chain, root.TableScopes...)
	for _, scope := range chain {
		for _, child := range scope.Traverse() {
			if nc := eliminateScope(child, existingCTEs, taken); nc != nil {
				newCTEs = append(newCTEs, nc)
			}
		}
	}

	if len(newCTEs) > 0 {
		setWithClause(expression, &With{CTEs: newCTEs, Recursive: recursive})
	}

	return expression
}

// setWithClause mirrors `query.set("with_", ...)`. Parent pointers must be established:
// merge_subqueries' _pop_cte walks cte.parent to find and remove the With.
func setWithClause(expression Expression, w *With) {
	switch v := expression.(type) {
	case *Select:
		v.With = w
		w.SetParent(v)
	case *Union:
		v.With = w
		w.SetParent(v)
	case *unionTail:
		if v.Union != nil {
			v.Union.With = w
			w.SetParent(v.Union)
		}
	}
	for _, c := range w.CTEs {
		if c != nil {
			c.SetParent(w)
		}
	}
}

// eliminateScope mirrors _eliminate.
func eliminateScope(scope *Scope, existingCTEs map[string]string, taken map[string]any) *CTE {
	switch scope.ScopeType {
	case ScopeDerivedTable:
		return eliminateDerivedTable(scope, existingCTEs, taken)
	case ScopeCTE:
		return eliminateCTE(scope, existingCTEs, taken)
	}
	return nil
}

// eliminateDerivedTable mirrors _eliminate_derived_table.
func eliminateDerivedTable(scope *Scope, existingCTEs map[string]string, taken map[string]any) *CTE {
	// Python also checks scope.parent.pivots / exp.Lateral — sqlg parses neither
	toReplace := unwrapSubquery(scope.Expression.Parent())

	name, cte := newCTE(scope, existingCTEs, taken)

	table := &Table{Parts: []*Ident{NewIdent(name)}}
	aliasName := exprAlias(toReplace)
	if aliasName == "" {
		aliasName = name
	}
	table.Alias = &TableAlias{Name: NewIdent(aliasName)}
	table.Alias.SetParent(table)

	Replace(toReplace, table)
	return cte
}

// unwrapSubquery mirrors `expression.unwrap()` for Subquery chains: climbs while the
// parent is the same type and the node has no alias (is_wrapper).
func unwrapSubquery(e Expression) Expression {
	for {
		sq, ok := e.(*Subquery)
		if !ok || sq.Alias != nil {
			return e
		}
		if _, ok := sq.Parent().(*Subquery); !ok {
			return e
		}
		e = sq.Parent()
	}
}

// eliminateCTE mirrors _eliminate_cte.
func eliminateCTE(scope *Scope, existingCTEs map[string]string, taken map[string]any) *CTE {
	cte, _ := scopeOuterNode(scope).(*CTE)
	if cte == nil {
		return nil
	}
	name, newCte := newCTE(scope, existingCTEs, taken)

	with, _ := cte.Parent().(*With)
	Pop(cte)
	if with != nil && len(with.CTEs) == 0 {
		Pop(with)
	}

	// Rename references to this CTE
	if scope.Parent != nil {
		for _, childScope := range scope.Parent.Traverse() {
			ss := childScope.SelectedSources()
			for _, k := range ss.keys {
				s := ss.m[k]
				if s.Source != scope {
					continue
				}
				oldName := AliasOrNameOfExpr(s.Node)
				newTable := &Table{Parts: []*Ident{NewIdent(name)}}
				newTable.Alias = &TableAlias{Name: NewIdent(oldName)}
				newTable.Alias.SetParent(newTable)
				Replace(s.Node, newTable)
			}
		}
	}

	return newCte
}

// newCTE mirrors _new_cte: returns (new name, new CTE);
// if it hits the de-duplication of an existing CTE, the second return value is nil.
func newCTE(scope *Scope, existingCTEs map[string]string, taken map[string]any) (string, *CTE) {
	duplicateAlias := existingCTEs[structKey(scope.Expression)]
	name := outerAlias(scope)

	if name == "" {
		name = findNewNameIn(anySet(taken), "cte")
	}
	if duplicateAlias != "" {
		name = duplicateAlias
	} else if _, ok := taken[name]; ok {
		name = findNewNameIn(anySet(taken), name)
	}
	taken[name] = scope

	if duplicateAlias == "" {
		existingCTEs[structKey(scope.Expression)] = name
		inner := cteInnerFromScope(scope)
		cte := &CTE{
			This:  inner,
			Alias: &TableAlias{Name: NewIdent(name)},
		}
		inner.SetParent(cte)
		cte.Alias.SetParent(cte)
		return name, cte
	}
	return name, nil
}

// cteInnerFromScope builds a new alias-less Subquery shell for CTE.This (sqlg's CTE.This
// is a *Subquery; Python hangs the Select directly).
// Do NOT reuse the outer tree's Subquery: that would reparent scope.Expression onto the
// new CTE, so _eliminate_derived_table's replace would hit the CTE instead of the FROM.
func cteInnerFromScope(scope *Scope) *Subquery {
	sq := &Subquery{This: scope.Expression}
	scope.Expression.SetParent(sq)
	return sq
}

// anySet converts the key set of taken into the form findNewNameIn needs.
func anySet(m map[string]any) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

// cteInner returns a CTE's inner query (peels sqlg's extra Subquery layer).
func cteInner(cte *CTE) Expression {
	if cte == nil {
		return nil
	}
	return unnest(cte.This)
}
