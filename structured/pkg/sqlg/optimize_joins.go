package sqlg

import "strings"

// Ports sqlglot/optimizer/optimize_joins.py: moves CROSS JOIN predicates into ON,
// topologically reorders joins, and normalizes join kinds.
// Divergences: no exp.From node (iterates *Select) and no method attribute in JOIN_ATTRS.

// OptimizeJoins mirrors optimize_joins.
func OptimizeJoins(expression Expression) Expression {
	for _, sel := range FindAllOf[*Select](expression, true) {
		joins := sel.Joins

		if !isReorderable(joins) {
			continue
		}

		references := map[string][]*Join{}
		type namedJoin struct {
			name string
			join *Join
		}
		var crossJoins []namedJoin

		for _, join := range joins {
			tables := otherTableNames(join)

			if len(tables) > 0 {
				for table := range tables {
					references[table] = append(references[table], join)
				}
			} else {
				crossJoins = append(crossJoins, namedJoin{joinAliasOrName(join), join})
			}
		}

		for _, cj := range crossJoins {
			for _, dep := range references[cj.name] {
				on := dep.On
				op, isConn := connectorOp(on)
				if !isConn {
					continue
				}
				if len(otherTableNames(dep)) < 2 {
					continue
				}
				for _, predicate := range flattenConnector(on) {
					if !columnTableNames(predicate, "")[cj.name] {
						continue
					}
					Replace(predicate, trueExpr())
					predicate = combineWith([]Expression{cj.join.On, predicate}, op)
					joinSetOn(cj.join, predicate)
				}
			}
		}
	}

	expression = ReorderJoins(expression)
	expression = NormalizeJoins(expression)
	return expression
}

// ReorderJoins mirrors reorder_joins.
func ReorderJoins(expression Expression) Expression {
	for _, sel := range FindAllOf[*Select](expression, true) {
		if sel.From == nil {
			continue
		}
		joins := sel.Joins
		if !isReorderable(joins) {
			continue
		}

		joinsByName := map[string]*Join{}
		var order []string
		for _, join := range joins {
			name := joinAliasOrName(join)
			if _, dup := joinsByName[name]; !dup {
				order = append(order, name)
			}
			joinsByName[name] = join
		}

		dag := map[string]map[string]bool{}
		for _, name := range order {
			dag[name] = otherTableNames(joinsByName[name])
		}

		fromName := AliasOrNameOfExpr(sel.From)
		var reordered []*Join
		for _, name := range tsort(dag) {
			if name == fromName {
				continue
			}
			if j, ok := joinsByName[name]; ok {
				reordered = append(reordered, j)
			}
		}
		sel.Joins = reordered
		retag(sel, sel.Children()...)
	}
	return expression
}

// NormalizeJoins mirrors the normalize function local to optimize_joins.py (NOT
// optimizer/normalize.py): removes INNER/OUTER; bare JOINs become CROSS or get ON TRUE.
func NormalizeJoins(expression Expression) Expression {
	for _, join := range FindAllOf[*Join](expression, true) {
		kind := strings.ToUpper(join.Kind)
		if join.On == nil && join.Side == "" && kind == "" && len(join.Using) == 0 {
			join.Kind = "CROSS"
			// Clear the comma-join marker; the join is now CROSS (rendering looks at Kind).
			join.Comma = false
			kind = "CROSS"
		}

		if kind == "CROSS" {
			join.On = nil
			continue
		}
		if kind == "INNER" || kind == "OUTER" {
			join.Kind = ""
		}
		if join.On == nil && len(join.Using) == 0 {
			t := trueExpr()
			join.On = t
			t.SetParent(join)
		}
	}
	return expression
}

// otherTableNames mirrors other_table_names: tables referenced in ON, excluding itself.
func otherTableNames(join *Join) map[string]bool {
	if join.On == nil {
		return map[string]bool{}
	}
	return columnTableNames(join.On, joinAliasOrName(join))
}

// isReorderable mirrors _is_reorderable: sided joins (LEFT/RIGHT/FULL) block reordering.
func isReorderable(joins []*Join) bool {
	for _, j := range joins {
		if j.Side != "" {
			return false
		}
	}
	return true
}

// joinSetOn mirrors `join.on(expr, append=False, copy=False)`; side effect: clears CROSS kind.
func joinSetOn(j *Join, on Expression) {
	j.On = on
	if !isNilExpr(on) {
		on.SetParent(j)
	}
	if strings.ToUpper(j.Kind) == "CROSS" {
		j.Kind = ""
	}
}
