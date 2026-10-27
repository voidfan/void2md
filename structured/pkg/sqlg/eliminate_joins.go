package sqlg

// Ports sqlglot/optimizer/eliminate_joins.py: a JOIN is removed only when the joined
// source yields at most one row per row and the JOIN's columns are unused.

// EliminateJoins mirrors eliminate_joins.
func EliminateJoins(expression Expression) Expression {
	for _, scope := range TraverseScope(expression) {
		// Unqualified columns make join usage undecidable; skip the whole scope.
		if len(scope.UnqualifiedColumns()) > 0 {
			continue
		}
		joins := joinsOf(scope.Expression)

		// Reverse order so consecutive unused JOINs can be removed
		for i := len(joins) - 1; i >= 0; i-- {
			join := joins[i]
			if join.Kind == "SEMI" || join.Kind == "ANTI" {
				continue
			}
			alias := joinAliasOrName(join)
			if shouldEliminateJoin(scope, join, alias) {
				Pop(join)
				scope.Sources().Del(alias)
			}
		}
	}
	return expression
}

// joinsOf mirrors `scope.expression.args.get("joins", [])`.
func joinsOf(e Expression) []*Join {
	if sel, ok := e.(*Select); ok {
		return sel.Joins
	}
	return nil
}

// joinAliasOrName mirrors Join.alias_or_name.
func joinAliasOrName(j *Join) string {
	if a := exprAlias(j.This); a != "" {
		return a
	}
	return AliasOrNameOfExpr(j.This)
}

// shouldEliminateJoin mirrors _should_eliminate_join.
func shouldEliminateJoin(scope *Scope, join *Join, alias string) bool {
	src, ok := scope.Sources().Get(alias)
	if !ok {
		return false
	}
	innerSource, ok := src.(*Scope)
	if !ok {
		return false
	}
	if joinIsUsed(scope, join, alias) {
		return false
	}
	if join.Side == "LEFT" && isJoinedOnAllUniqueOutputs(innerSource, join) {
		return true
	}
	return join.On == nil && hasSingleOutputRow(innerSource)
}

// joinIsUsed mirrors _join_is_used: columns in the ON clause do not count as "used".
func joinIsUsed(scope *Scope, join *Join, alias string) bool {
	onClause := map[*Column]bool{}
	if join.On != nil {
		for _, c := range FindAllOf[*Column](join.On, true) {
			onClause[c] = true
		}
	}
	for _, c := range sourceColumns(scope, alias) {
		if !onClause[c] {
			return true
		}
	}
	return false
}

// sourceColumns mirrors Scope.source_columns.
func sourceColumns(scope *Scope, name string) []*Column {
	var out []*Column
	for _, c := range scope.Columns() {
		if identText(c.Table) == name {
			out = append(out, c)
		}
	}
	return out
}

// isJoinedOnAllUniqueOutputs mirrors _is_joined_on_all_unique_outputs.
func isJoinedOnAllUniqueOutputs(scope *Scope, join *Join) bool {
	uniqueOutputs := uniqueOutputs(scope)
	if len(uniqueOutputs) == 0 {
		return false
	}
	_, joinKeys, _ := JoinCondition(join)
	remaining := copyStringSet(uniqueOutputs)
	for _, k := range joinKeys {
		// `{c.name for c in join_keys}`: take the Column's name.
		delete(remaining, exprName(k))
	}
	return len(remaining) == 0
}

// uniqueOutputs mirrors _unique_outputs: the set of output columns that must be unique per row.
func uniqueOutputs(scope *Scope) map[string]bool {
	sel, ok := scope.Expression.(*Select)
	if !ok {
		return map[string]bool{}
	}
	if sel.Distinct {
		return stringSetOf(namedSelectsOf(sel))
	}
	if sel.Group != nil {
		grouped := map[string]bool{}
		for _, g := range sel.Group.Expressions {
			grouped[structKey(g)] = true
		}
		groupedOutputs := map[string]bool{}
		unique := map[string]bool{}
		for _, s := range selectsOf(sel) {
			output := unaliasExpr(s)
			if grouped[structKey(output)] {
				groupedOutputs[structKey(output)] = true
				unique[outputNameOf(s)] = true
			}
		}
		// All grouping expressions must appear in the outputs
		for k := range grouped {
			if !groupedOutputs[k] {
				return map[string]bool{}
			}
		}
		return unique
	}
	if hasSingleOutputRow(scope) {
		return stringSetOf(namedSelectsOf(sel))
	}
	return map[string]bool{}
}

// hasSingleOutputRow mirrors _has_single_output_row.
func hasSingleOutputRow(scope *Scope) bool {
	sel, ok := scope.Expression.(*Select)
	if !ok {
		return false
	}
	allAgg := true
	for _, e := range selectsOf(sel) {
		if !isAggFuncNode(unaliasExpr(e)) {
			allAgg = false
			break
		}
	}
	return allAgg || isLimitOne(sel) || sel.From == nil
}

// isLimitOne mirrors _is_limit_1.
func isLimitOne(sel *Select) bool {
	if sel.Limit == nil {
		return false
	}
	lit, ok := sel.Limit.Expression.(*Literal)
	return ok && lit.Value == "1"
}

// stringSetOf converts a string slice into a set.
func stringSetOf(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}

// JoinCondition mirrors join_condition: returns (source_key, join_key, on). The on is
// processed on a copy; the original Join.On is unaffected.
func JoinCondition(join *Join) ([]Expression, []Expression, Expression) {
	name := joinAliasOrName(join)
	var on Expression
	if join.On != nil {
		on = join.On.Copy()
	} else {
		on = trueExpr()
	}
	sourceKey := []Expression{}
	joinKey := []Expression{}

	extract := func(condition Expression) {
		ops := unnestOperands(condition)
		if len(ops) != 2 {
			return
		}
		left, right := ops[0], ops[1]
		leftTables := columnTableNames(left, "")
		rightTables := columnTableNames(right, "")
		if leftTables[name] && !rightTables[name] {
			joinKey = append(joinKey, left)
			sourceKey = append(sourceKey, right)
			Replace(condition, trueExpr())
		} else if rightTables[name] && !leftTables[name] {
			joinKey = append(joinKey, right)
			sourceKey = append(sourceKey, left)
			Replace(condition, trueExpr())
		}
	}

	if normalizedExpr(on, false) {
		if _, isAnd := asAnd(on); !isAnd {
			on = combineWith([]Expression{on, trueExpr()}, "AND")
		}
		for _, condition := range flattenConnector(on) {
			if isEQExpr(condition) {
				extract(condition)
			}
		}
	} else if normalizedExpr(on, true) {
		var conditions []Expression
		for _, condition := range flattenConnector(on) {
			var parts []Expression
			for _, part := range flattenConnector(condition) {
				if isEQExpr(part) {
					parts = append(parts, part)
				}
			}
			if conditions == nil {
				conditions = parts
			} else {
				var temp []Expression
				for _, p := range parts {
					for _, c := range conditions {
						if structKey(p) == structKey(c) {
							temp = append(temp, p)
							temp = append(temp, c)
						}
					}
				}
				conditions = temp
			}
		}
		for _, condition := range conditions {
			extract(condition)
		}
	}

	return sourceKey, joinKey, on
}

// asAnd reports whether e is an AND connector.
func asAnd(e Expression) (Expression, bool) {
	if op, ok := connectorOp(e); ok && op == "AND" {
		return e, true
	}
	return e, false
}

// isEQExpr mirrors isinstance(e, exp.EQ).
func isEQExpr(e Expression) bool {
	b, ok := e.(*Binary)
	return ok && b.Op == "="
}

// isOrExpr mirrors isinstance(e, exp.Or).
func isOrExpr(e Expression) bool {
	b, ok := e.(*Binary)
	return ok && b.Op == "OR"
}

// isAndExpr mirrors isinstance(e, exp.And).
func isAndExpr(e Expression) bool {
	b, ok := e.(*Binary)
	return ok && b.Op == "AND"
}
