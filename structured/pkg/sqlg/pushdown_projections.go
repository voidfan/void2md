package sqlg

// Ports sqlglot/optimizer/pushdown_projections.py: pushes columns used by the outer
// query down into inner queries and drops unneeded projections.

// selectAllSentinel mirrors Python's SELECT_ALL = object(): "outer query selected all columns".
const selectAllSentinel = "\x00SELECT_ALL"

// defaultSelection mirrors default_selection: `1 AS _`, or `MAX(1) AS _` when aggregate.
func defaultSelection(isAgg bool) *Alias {
	var this Expression = &Literal{Value: "1"}
	if isAgg {
		f := &Func{Name: "MAX", Args: []Expression{&Literal{Value: "1"}}}
		retag(f, f.Args...)
		this = f
	}
	a := &Alias{This: this, Alias: NewIdent("_")}
	retag(a, this, a.Alias)
	return a
}

// PushdownProjections mirrors pushdown_projections.
func PushdownProjections(expression Expression, ctx *OptCtx) Expression {
	schema := ctx.Schema

	sourceColumnAliasCount := map[*Scope]int{}
	referencedColumns := map[*Scope]map[string]bool{}

	scopes := TraverseScope(expression)
	for i := len(scopes) - 1; i >= 0; i-- {
		scope := scopes[i]

		parentSelections := referencedColumns[scope]
		if parentSelections == nil {
			parentSelections = map[string]bool{selectAllSentinel: true}
		}
		aliasCount := sourceColumnAliasCount[scope]

		// SELECT DISTINCT / UNION DISTINCT columns must not be removed.
		if sel, ok := scope.Expression.(*Select); ok && sel.Distinct {
			parentSelections = map[string]bool{selectAllSentinel: true}
		}

		if setOp, ok := scope.Expression.(*Union); ok {
			_ = setOp
			// Pushdown only when no kind/side (Python: `not (set_op.kind or set_op.side)`).
			if !setOpHasKindOrSide(setOp) && len(scope.UnionScopes) == 2 {
				left, right := scope.UnionScopes[0], scope.UnionScopes[1]
				leftSel, leftOK := left.Expression.(*Select)
				rightSel, rightOK := right.Expression.(*Select)
				if leftOK && rightOK {
					if len(leftSel.Expressions) != len(rightSel.Expressions) {
						panic(&OptimizeError{Msg: "Invalid set operation due to column mismatch: " +
							scope.Expression.SQL(ctx.Dialect) + "."})
					}
					referencedColumns[left] = parentSelections

					if anyStarSelect(rightSel) {
						referencedColumns[right] = parentSelections
					} else if !anyStarSelect(leftSel) {
						// by_name is never parsed by sqlg, so always the else branch.
						next := map[string]bool{}
						for j, s := range leftSel.Expressions {
							if parentSelections[selectAllSentinel] ||
								parentSelections[outputNameOf(s)] {
								next[outputNameOf(rightSel.Expressions[j])] = true
							}
						}
						referencedColumns[right] = next
					}
				}
			}
		}

		if sel, ok := scope.Expression.(*Select); ok {
			_removeUnusedSelections(scope, sel, parentSelections, schema, aliasCount)

			if anyStarSelect(sel) {
				continue
			}

			// Group by source name: table_name -> {col_name}
			selects := map[string]map[string]bool{}
			for _, col := range scope.Columns() {
				tableName := identText(col.Table)
				if selects[tableName] == nil {
					selects[tableName] = map[string]bool{}
				}
				selects[tableName][colName(col)] = true
			}

			ss := scope.SelectedSources()
			for _, name := range ss.keys {
				node := ss.m[name].Node
				source := ss.m[name].Source
				if inner, ok := source.(*Scope); ok {
					// scope.pivots: sqlg has no pivots node → always empty
					columns := selects[name]
					if columns == nil {
						columns = map[string]bool{}
					}
					if existing := referencedColumns[inner]; existing == nil {
						referencedColumns[inner] = columns
					} else {
						for k := range columns {
							existing[k] = true
						}
					}
				}
				if aliases := aliasColumnNamesOf(node); len(aliases) > 0 {
					if inner, ok := source.(*Scope); ok {
						sourceColumnAliasCount[inner] = len(aliases)
					}
				}
			}
		}
	}
	return expression
}

// setOpHasKindOrSide mirrors `set_op.kind or set_op.side`; sqlg's Union does not
// distinguish kind/side (BY NAME syntax is not parsed).
func setOpHasKindOrSide(*Union) bool { return false }

// anyStarSelect mirrors `any(select.is_star for select in ...selects)`.
func anyStarSelect(sel *Select) bool {
	for _, e := range sel.Expressions {
		if isStarExpr(e) {
			return true
		}
	}
	return false
}

// _removeUnusedSelections mirrors _remove_unused_selections.
func _removeUnusedSelections(
	scope *Scope, sel *Select, parentSelections map[string]bool,
	schema *MappingSchema, aliasCount int,
) {
	orderRefs := map[string]bool{}
	if sel.Order != nil {
		// Column names without a table qualifier count as references to output columns.
		for _, c := range FindAllOf[*Column](sel.Order, true) {
			if c.Table == nil {
				orderRefs[colName(c)] = true
			}
		}
	}

	var newSelections []Expression
	removed := false
	star := false
	isAgg := false
	selectAll := parentSelections[selectAllSentinel]

	for _, selection := range sel.Expressions {
		name := outputNameOf(selection)

		if selectAll || parentSelections[name] || orderRefs[name] || aliasCount > 0 {
			newSelections = append(newSelections, selection)
			aliasCount--
		} else {
			if isStarExpr(selection) {
				star = true
			}
			removed = true
		}

		if !isAgg && HasNode(selection, func(n Expression) bool {
			f, ok := n.(*Func)
			return ok && isAggFunc(f)
		}) {
			isAgg = true
		}
	}

	if star {
		resolver := NewResolver(scope, schema, false)
		names := map[string]bool{}
		for _, s := range newSelections {
			names[outputNameOf(s)] = true
		}
		for _, name := range sortedKeys(parentSelections) {
			if name == selectAllSentinel || names[name] {
				continue
			}
			c := &Column{Table: resolver.GetTable(name, nil), Name: NewIdent(name)}
			retag(c, c.Table, c.Name)
			a := &Alias{This: c, Alias: NewIdent(name)}
			retag(a, c, a.Alias)
			newSelections = append(newSelections, a)
		}
	}

	if len(newSelections) == 0 {
		newSelections = append(newSelections, defaultSelection(isAgg))
	}

	sel.Expressions = newSelections
	retag(sel, newSelections...)

	if removed {
		scope.clearCache()
	}
}

// sortedKeys returns sorted keys, skipping the SELECT_ALL sentinel (the sentinel only
// exists when select_all is true, in which case star is false and sorting never runs on it).
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		if k == selectAllSentinel {
			continue
		}
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
