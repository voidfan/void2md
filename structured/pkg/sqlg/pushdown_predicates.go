package sqlg

import "strings"

// Ports sqlglot/optimizer/pushdown_predicates.py: pushes WHERE / JOIN ON predicates down
// into their sources. CNF (or neither): push per predicate and replace the original with
// TRUE (stop at JOINs); DNF: only tables present in EVERY branch push down and the
// original is NOT cleared. sqlg divergences: no From/Where/Window nodes (see
// joinOrFromAncestor / whereConditionOf / hasWindowExpr), Select.selects == Expressions
// (Star included), and append-style on/where/having reproduced by andAppend.

// PushdownPredicates mirrors pushdown_predicates.
func PushdownPredicates(expression Expression, dialect *Dialect) Expression {
	root := BuildScope(expression)
	if root == nil {
		return expression
	}

	// presto/athena forbid an inner join's right side being UNNEST. The corpus dialects
	// include neither, so this branch never fires; kept in case the dialect set changes.
	unnestRequiresCrossJoin := dialect != nil &&
		(dialect.Name == "athena" || dialect.Name == "presto")

	scopeRefCount := root.RefCount()

	scopes := root.Traverse()
	for i := len(scopes) - 1; i >= 0; i-- {
		scope := scopes[i]
		sel, ok := scope.Expression.(*Select)
		if !ok {
			// Non-Select scopes: Python gets None for both where/joins → skip
			continue
		}
		selectedSources := scope.SelectedSources()

		if sel.Where != nil {
			joinIndex := map[string]int{}
			for idx, j := range sel.Joins {
				joinIndex[joinAliasOrName(j)] = idx
			}

			// A RIGHT JOIN can only push down to itself; narrow sources to that one.
			selected := selectedSources
			pushdownAllowed := true
			for _, k := range selectedSources.keys {
				node := selectedSources.m[k].Node
				if node == nil {
					continue
				}
				anc := joinOrFromAncestor(node)
				if !anc.isJoin {
					continue
				}
				if anc.joinSide == "RIGHT" {
					selected = singleSource(selectedSources, k)
					break
				}
				if isUnnestNode(node) && unnestRequiresCrossJoin {
					pushdownAllowed = false
					break
				}
			}

			if pushdownAllowed {
				pushdown(sel.Where, selected, scopeRefCount, dialect, joinIndex)
			}
		}

		// A JOIN's ON only pushes down to itself, not across JOINs.
		for _, join := range sel.Joins {
			name := joinAliasOrName(join)
			if _, ok := selectedSources.m[name]; ok {
				pushdown(
					join.On,
					singleSource(selectedSources, name),
					scopeRefCount,
					dialect,
					nil,
				)
			}
		}
	}
	return expression
}

// pushdown mirrors pushdown(condition, sources, scope_ref_count, dialect, join_index=None).
func pushdown(
	condition Expression,
	sources *selectedSources,
	scopeRefCount map[*Scope]int,
	dialect *Dialect,
	joinIndex map[string]int,
) {
	if condition == nil {
		return
	}

	// simplify's defaults are constant_propagation=False, coalesce_simplification=False
	// (same at the top-level optimize call); do not change them.
	condition = Replace(condition, Simplify(condition, dialect, false, false))

	cnfLike := normalizedExpr(condition, false) || !normalizedExpr(condition, true)

	var predicates []Expression
	flattenThis := false
	if op, isConn := connectorOp(condition); isConn {
		flattenThis = (cnfLike && op == "AND") || (!cnfLike && op == "OR")
	}
	if flattenThis {
		predicates = flattenConnector(condition)
	} else {
		predicates = []Expression{condition}
	}

	if cnfLike {
		pushdownCNF(predicates, sources, scopeRefCount, joinIndex)
	} else {
		pushdownDNF(predicates, sources, scopeRefCount)
	}
}

// pushdownCNF mirrors pushdown_cnf: CNF form, replace pushed predicates with TRUE.
func pushdownCNF(
	predicates []Expression,
	sources *selectedSources,
	scopeRefCount map[*Scope]int,
	joinIndex map[string]int,
) {
	for _, predicate := range predicates {
		for _, entry := range nodesForPredicate(predicate, sources, scopeRefCount) {
			node := entry.node
			if j, isJoin := node.(*Join); isJoin {
				name := joinAliasOrName(j)
				predicateTables := columnTableNames(predicate, name)

				// No pushdown when the predicate references tables in a LATER JOIN.
				thisIndex, found := joinIndex[name]
				if !found {
					// Python's join_index[name] would KeyError; conservatively skip.
					continue
				}
				ok := true
				for table := range predicateTables {
					if idx, has := joinIndex[table]; has && idx >= thisIndex {
						ok = false
						break
					}
				}
				if ok {
					Replace(predicate, trueExpr())
					setJoinOn(j, predicate)
					break
				}
			}
			if inner, isSelect := node.(*Select); isSelect {
				Replace(predicate, trueExpr())
				innerPredicate := replaceAliases(inner, predicate)
				if containsAggFuncInScope(innerPredicate) {
					setHavingAppend(inner, innerPredicate)
				} else {
					setWhereAppend(inner, innerPredicate)
				}
			}
		}
	}
}

// pushdownDNF mirrors pushdown_dnf: only tables present in EVERY branch push down and
// the original predicate is not cleared.
func pushdownDNF(predicates []Expression, sources *selectedSources, scopeRefCount map[*Scope]int) {
	pushdownTables := map[string]bool{}
	for _, a := range predicates {
		aTables := columnTableNames(a, "")
		for _, b := range predicates {
			bTables := columnTableNames(b, "")
			for t := range aTables {
				if !bTables[t] {
					delete(aTables, t)
				}
			}
		}
		for t := range aTables {
			pushdownTables[t] = true
		}
	}

	conditions := map[string]Expression{}

	for _, table := range sortedStringSet(pushdownTables) {
		// `nodes` lives outside the predicate loop in Python: the LAST iteration's
		// result is what gets used below.
		var nodes []namedNode
		for _, predicate := range predicates {
			nodes = nodesForPredicate(predicate, sources, scopeRefCount)
			if !hasNamedNode(nodes, table) {
				continue
			}
			if prev, ok := conditions[table]; ok {
				// exp.or_ defaults: copy=True, wrap=True
				conditions[table] = orExprs(prev, predicate)
			} else {
				conditions[table] = predicate
			}
		}

		for _, entry := range nodes {
			name, node := entry.name, entry.node
			predicate, ok := conditions[name]
			if !ok {
				continue
			}
			if j, isJoin := node.(*Join); isJoin {
				setJoinOn(j, predicate)
			} else if inner, isSelect := node.(*Select); isSelect {
				innerPredicate := replaceAliases(inner, predicate)
				if containsAggFuncInScope(innerPredicate) {
					setHavingAppend(inner, innerPredicate)
				} else {
					setWhereAppend(inner, innerPredicate)
				}
			}
		}
	}
}

// namedNode is an order-preserving "table name → target node" entry.
type namedNode struct {
	name string
	node Expression
}

// nodesForPredicate mirrors nodes_for_predicate.
func nodesForPredicate(
	predicate Expression, sources *selectedSources, scopeRefCount map[*Scope]int,
) []namedNode {
	var nodes []namedNode
	tables := columnTableNames(predicate, "")
	whereCondition := whereConditionOf(predicate)

	for _, table := range sortedStringSet(tables) {
		var node Expression
		var source any
		if e, ok := sources.m[table]; ok {
			node, source = e.Node, e.Source
		}

		// Only WHERE predicates hunt for the nearest JOIN/FROM ancestor.
		isFrom := false
		if node != nil && whereCondition {
			anc := joinOrFromAncestor(node)
			if anc.isJoin {
				node = anc.join
			} else if anc.isFrom {
				isFrom = true
				node = nil
			} else {
				node = nil
			}
		}

		// CTE / derived-table sources push into the inner query.
		if isFrom {
			srcScope, isScope := source.(*Scope)
			if !isScope {
				// FROM hit a physical table: sqlglot's From node is neither Join nor
				// Select, so it is not collected.
				continue
			}
			// Recursive CTEs do not participate in pushdown.
			if w := withOfQuery(parentScopeExpression(srcScope)); w != nil && w.Recursive {
				return nil
			}
			node = srcScope.Expression
		}

		if j, isJoin := node.(*Join); isJoin {
			if j.Side != "" && j.Side != "RIGHT" {
				return nil
			}
			nodes = append(nodes, namedNode{name: table, node: j})
		} else if sel, isSelect := node.(*Select); isSelect && len(tables) == 1 {
			// Window expressions block pushdown (sqlg stores OVER on Func.Over).
			hasWindowExpression := false
			for _, e := range sel.Expressions {
				if hasWindowExpr(e) {
					hasWindowExpression = true
					break
				}
			}
			// A select referenced from multiple scopes cannot be pushed down either.
			refCount := 0
			if sub, isScope := source.(*Scope); isScope {
				refCount = scopeRefCount[sub]
			}
			if sel.Group == nil && refCount < 2 && !hasWindowExpression {
				nodes = append(nodes, namedNode{name: table, node: sel})
			}
		}
	}
	return nodes
}

// replaceAliases mirrors replace_aliases: replaces columns referencing projection aliases
// with the projections themselves. transform's default copy=True returns a new tree.
func replaceAliases(source *Select, predicate Expression) Expression {
	aliases := map[string]Expression{}
	// source.selects == source.Expressions (Star included)
	for _, proj := range source.Expressions {
		if a, isAlias := proj.(*Alias); isAlias {
			// exprAlias only handles Subquery/Table — not usable for Alias nodes
			aliases[AliasOrNameOfExpr(a)] = a.This
		} else {
			aliases[exprName(proj)] = proj
		}
	}

	return Transform(predicate, func(column Expression) Expression {
		if c, isColumn := column.(*Column); isColumn {
			if rep, ok := aliases[colName(c)]; ok {
				return rep.Copy()
			}
		}
		return column
	}, true)
}

// --- Helpers ---

// joinFromAncestor is the result of find_ancestor(exp.Join, exp.From). sqlg has no From
// node, so "hit From" means climbing out through a Select's From branch.
type joinFromAncestor struct {
	isJoin   bool
	join     *Join
	joinSide string
	isFrom   bool
}

// joinOrFromAncestor mirrors `node.find_ancestor(exp.Join, exp.From)`.
func joinOrFromAncestor(node Expression) joinFromAncestor {
	for child, p := node, node.Parent(); p != nil; child, p = p, p.Parent() {
		if j, ok := p.(*Join); ok {
			return joinFromAncestor{isJoin: true, join: j, joinSide: j.Side}
		}
		if sel, ok := p.(*Select); ok && sel.From == child {
			return joinFromAncestor{isFrom: true}
		}
	}
	return joinFromAncestor{}
}

// whereConditionOf mirrors `isinstance(predicate.find_ancestor(Join, Where), Where)`.
// sqlg has no Where node: a Select's Where branch counts. Must keep walking the parent
// chain — cannot bail out at the first Select.
func whereConditionOf(predicate Expression) bool {
	for child, p := predicate, predicate.Parent(); p != nil; child, p = p, p.Parent() {
		if _, ok := p.(*Join); ok {
			return false
		}
		if sel, ok := p.(*Select); ok && sel.Where == child {
			return true
		}
	}
	return false
}

// parentScopeExpression mirrors `source.parent.expression`; nil instead of Python's
// AttributeError when there is no parent scope.
func parentScopeExpression(scope *Scope) Expression {
	if scope.Parent == nil {
		return nil
	}
	return scope.Parent.Expression
}

// withOfQuery gets the WITH clause on a query node (mirrors `expression.args.get("with_")`).
func withOfQuery(e Expression) *With {
	switch n := e.(type) {
	case *Select:
		return n.With
	case *Union:
		return n.With
	}
	return nil
}

// singleSource takes the single-element subset of selectedSources (mirrors Python's
// `selected_sources = {k: (node, source)}`).
func singleSource(ss *selectedSources, k string) *selectedSources {
	out := &selectedSources{m: map[string]selectedSource{}}
	if v, ok := ss.m[k]; ok {
		out.keys = []string{k}
		out.m[k] = v
	}
	return out
}

// hasNamedNode mirrors `table not in nodes`.
func hasNamedNode(nodes []namedNode, name string) bool {
	for _, n := range nodes {
		if n.name == name {
			return true
		}
	}
	return false
}

// isUnnestNode approximates `isinstance(node, exp.Unnest)` by function name (sqlg has no
// Unnest node); only affects the presto/athena cross-join branch.
func isUnnestNode(e Expression) bool {
	f, ok := e.(*Func)
	return ok && strings.EqualFold(f.Name, "UNNEST")
}

// hasWindowExpr mirrors `select.find(exp.Window)`; sqlg keeps the OVER clause verbatim
// on Func.Over with no Window node.
func hasWindowExpr(e Expression) bool {
	for _, f := range FindAllOf[*Func](e, true) {
		if f.Over != "" {
			return true
		}
	}
	return false
}

// containsAggFuncInScope mirrors `find_in_scope(inner_predicate, exp.AggFunc)`
// (does not descend past subscope boundaries).
func containsAggFuncInScope(e Expression) bool {
	for _, n := range findAllInScope(e, true, isAggFuncNode) {
		_ = n
		return true
	}
	return false
}

// setJoinOn mirrors `join.on(expression, copy=False)`; also clears the CROSS kind.
func setJoinOn(j *Join, on Expression) {
	j.On = andAppend(j.On, on)
	if !isNilExpr(j.On) {
		j.On.SetParent(j)
	}
	if strings.ToUpper(j.Kind) == "CROSS" {
		j.Kind = ""
	}
}

// setWhereAppend mirrors `select.where(expression, copy=False)`.
func setWhereAppend(sel *Select, cond Expression) {
	sel.Where = andAppend(sel.Where, cond)
	if !isNilExpr(sel.Where) {
		sel.Where.SetParent(sel)
	}
}

// setHavingAppend mirrors `select.having(expression, copy=False)`.
func setHavingAppend(sel *Select, cond Expression) {
	sel.Having = andAppend(sel.Having, cond)
	if !isNilExpr(sel.Having) {
		sel.Having.SetParent(sel)
	}
}

// andAppend mirrors _apply_conjunction_builder (append=True, copy=False): AND-combines
// with the existing condition, wrapping connectors.
func andAppend(existing, cond Expression) Expression {
	if existing == nil {
		return cond
	}
	and := &Binary{Op: "AND", Left: wrapConnector(existing), Right: wrapConnector(cond)}
	and.SetParent(nil)
	retag(and, and.Left, and.Right)
	return and
}

// orExprs mirrors `exp.or_(a, b)` (copy=True, wrap=True): copies both sides and wraps
// non-Connector operands in parens.
func orExprs(a, b Expression) Expression {
	left := wrapConnector(a.Copy())
	right := wrapConnector(b.Copy())
	or := &Binary{Op: "OR", Left: left, Right: right}
	or.SetParent(nil)
	retag(or, or.Left, or.Right)
	return or
}
