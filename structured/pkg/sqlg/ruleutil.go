package sqlg

import "sort"

// Shared primitives for the ported sqlglot optimizer rules. Easy-to-get-wrong semantics:
//   - unnestExpr is polymorphic: strips only the starting node's own type (Paren or Subquery).
//   - flattenConnector prunes: `A AND (B AND C)` yields [A, B AND C], not [A, B, C].
//   - tsort emits each round's dependency-free nodes as a lexicographically sorted batch.

// connectorOp returns the AND/OR operator name; false for non-connectors.
func connectorOp(e Expression) (string, bool) {
	if b, ok := e.(*Binary); ok && (b.Op == "AND" || b.Op == "OR") {
		return b.Op, true
	}
	return "", false
}

// isConnectorExpr mirrors isinstance(e, exp.Connector).
func isConnectorExpr(e Expression) bool {
	_, ok := connectorOp(e)
	return ok
}

// unnestExpr mirrors Expression.unnest(). Polymorphic: Paren(Subquery(...)) yields the
// Subquery, not the Select.
func unnestExpr(e Expression) Expression {
	switch e.(type) {
	case *Paren:
		for {
			p, ok := e.(*Paren)
			if !ok {
				return e
			}
			e = p.This
		}
	case *Subquery:
		for {
			s, ok := e.(*Subquery)
			if !ok {
				return e
			}
			e = s.This
		}
	}
	return e
}

// unnestOperands mirrors Connector.unnest_operands(): each child is unnested on its own.
func unnestOperands(e Expression) []Expression {
	out := []Expression{}
	for _, c := range e.Children() {
		if c == nil {
			out = append(out, nil)
			continue
		}
		out = append(out, unnestExpr(c))
	}
	return out
}

// flattenConnector mirrors Connector.flatten(): operands of the same connector, with
// parenthesized same-type connectors not expanded further (sqlglot prune semantics).
func flattenConnector(e Expression) []Expression {
	op, ok := connectorOp(e)
	if !ok {
		return nil
	}
	var out []Expression
	stack := []Expression{e}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		nOp, nIsConn := connectorOp(n)
		if !nIsConn {
			// flatten does not strip Subquery when unnesting (sqlglot's `not isinstance(node, Subquery)`).
			if _, isSub := n.(*Subquery); isSub {
				out = append(out, n)
			} else {
				out = append(out, unnestExpr(n))
			}
		}
		if n.Parent() != nil && !(nIsConn && nOp == op) {
			continue
		}
		ch := n.Children()
		for i := len(ch) - 1; i >= 0; i-- {
			stack = append(stack, ch[i])
		}
	}
	return out
}

// findAllInScope mirrors find_all_in_scope.
func findAllInScope(e Expression, bfs bool, pred func(Expression) bool) []Expression {
	var out []Expression
	for _, n := range walkInScope(e, bfs) {
		if pred(n) {
			out = append(out, n)
		}
	}
	return out
}

// findAncestor mirrors Expression.find_ancestor.
func findAncestor(e Expression, pred func(Expression) bool) Expression {
	if e == nil {
		return nil
	}
	for p := e.Parent(); p != nil; p = p.Parent() {
		if pred(p) {
			return p
		}
	}
	return nil
}

// findAncestorOf is the single-type version of findAncestor.
func findAncestorOf[T Expression](e Expression) (T, bool) {
	var zero T
	p := findAncestor(e, func(n Expression) bool { _, ok := n.(T); return ok })
	if p == nil {
		return zero, false
	}
	v, _ := p.(T)
	return v, true
}

// trueExpr mirrors exp.true().
func trueExpr() *Boolean { return &Boolean{Value: true} }

// isTrueExpr reports whether the expression is a literal TRUE.
func isTrueExpr(e Expression) bool {
	b, ok := e.(*Boolean)
	return ok && b.Value
}

// falseExpr mirrors exp.false().
func falseExpr() *Boolean { return &Boolean{Value: false} }

// combineWith mirrors exp._combine(expressions, operator, copy=False): left-fold into an
// operator chain, wrapping connector operands in parens.
func combineWith(expressions []Expression, op string) Expression {
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
		n := &Binary{Op: op, Left: this, Right: wrapConnector(e)}
		retag(n, n.Children()...)
		this = n
	}
	return this
}

// sortedStringSet returns the lexicographically sorted slice of a string set.
func sortedStringSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// copyStringSet deep-copies a string set.
func copyStringSet(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

// tsort mirrors sqlglot.helper.tsort. Each round removes all dependency-free nodes as a
// lexicographically sorted batch; deps on nonexistent nodes are added as dependency-free.
func tsort(dag map[string]map[string]bool) []string {
	d := make(map[string]map[string]bool, len(dag))
	for k, v := range dag {
		d[k] = copyStringSet(v)
	}
	for _, deps := range dag {
		for dep := range deps {
			if _, ok := d[dep]; !ok {
				d[dep] = map[string]bool{}
			}
		}
	}

	var result []string
	for len(d) > 0 {
		var current []string
		for node, deps := range d {
			if len(deps) == 0 {
				current = append(current, node)
			}
		}
		if len(current) == 0 {
			// Mirrors Python's ValueError("Cycle error")
			panic("Cycle error")
		}
		for _, node := range current {
			delete(d, node)
		}
		for _, deps := range d {
			for _, node := range current {
				delete(deps, node)
			}
		}
		sort.Strings(current)
		result = append(result, current...)
	}
	return result
}

// selectsOf mirrors Select.selects: the projection list with Star removed.
func selectsOf(sel *Select) []Expression {
	out := make([]Expression, 0, len(sel.Expressions))
	for _, e := range sel.Expressions {
		if isStarExpr(e) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// unaliasExpr mirrors Expression.unalias().
func unaliasExpr(e Expression) Expression {
	if a, ok := e.(*Alias); ok {
		return a.This
	}
	return e
}

// isPredicateExpr approximates isinstance(e, exp.Predicate): sqlg's *Binary covers both
// comparison and logical ops, so AND/OR are excluded by operator.
func isPredicateExpr(e Expression) bool {
	switch v := e.(type) {
	case *Binary:
		switch v.Op {
		case "AND", "OR":
			return false
		}
		return true
	case *In, *Between, *Is, *Exists:
		return true
	}
	return false
}

// setStringAdd merges the elements of src into dst.
func setStringAdd(dst, src map[string]bool) {
	for k := range src {
		dst[k] = true
	}
}

// setStringIntersect computes the intersection in place.
func setStringIntersect(dst, other map[string]bool) {
	for k := range dst {
		if !other[k] {
			delete(dst, k)
		}
	}
}
