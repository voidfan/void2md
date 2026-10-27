package sqlg

// Predicate portion of sqlglot/optimizer/normalize.py (normalize / distributive_law /
// _distribute). Simplifier-side helpers (rewrite_between / uniq_sort / flatten) live in
// simplify.go. Gotchas:
//   - normalize's Simplifier uses annotate_new_expressions=False (no annotate_types callback).
//   - The walk(prune=Connector) result must be materialized before iterating (Python tuple()).
//   - distributive_law's distance-exceeded OptimizeError rolls back to original; here
//     reproduced with panic/recover (recover wraps only while_changing).

// optimizeError mirrors OptimizeError (normalize uses it only for distance-exceeded rollback).
type optimizeError struct{ distance int }

func (e optimizeError) Error() string {
	return "normalization distance exceeds max"
}

// Normalize mirrors normalize(expression, dnf=False, max_distance=128).
func Normalize(expression Expression, dnf bool, maxDistance float64) Expression {
	n := &normalizer{
		dnf:         dnf,
		maxDistance: maxDistance,
		simplifier:  NewSimplifier(nil, false),
	}

	// Python materializes tuple(expression.walk(prune=Connector)) before iterating
	nodes := WalkBFS(expression, isConnectorNode)

	for _, node := range nodes {
		if !isConnectorNode(node) {
			continue
		}
		if normalizedExpr(node, dnf) {
			continue
		}
		root := node == expression
		original := RefreshParents(node.Copy())

		// node.transform(simplifier.rewrite_between, copy=False)
		node = Transform(node, n.simplifier.RewriteBetween, false)

		distance := normalizationDistance(node, dnf, maxDistance)
		if float64(distance) > maxDistance {
			// Python logs and returns the expression unchanged
			return expression
		}

		res, oerr := n.tryDistribute(node)
		if oerr != nil {
			// except OptimizeError: roll back to original
			Replace(node, original)
			if root {
				return original
			}
			return expression
		}
		node = Replace(node, res)

		if root {
			expression = node
		}
	}

	return expression
}

type normalizer struct {
	dnf         bool
	maxDistance float64
	simplifier  *Simplifier
}

// tryDistribute mirrors while_changing(distributive_law), converting Python's
// try/except OptimizeError into a return value via panic/recover.
func (n *normalizer) tryDistribute(node Expression) (result Expression, oerr *optimizeError) {
	defer func() {
		if r := recover(); r != nil {
			if oe, ok := r.(optimizeError); ok {
				result = nil
				oerr = &oe
				return
			}
			panic(r)
		}
	}()
	return whileChanging(node, func(e Expression) Expression {
		return n.distributiveLaw(e)
	}), nil
}

// distributiveLaw mirrors distributive_law:
//
//	x OR (y AND z) -> (x OR y) AND (x OR z)
func (n *normalizer) distributiveLaw(expression Expression) Expression {
	if normalizedExpr(expression, n.dnf) {
		return expression
	}

	distance := normalizationDistance(expression, n.dnf, n.maxDistance)
	if float64(distance) > n.maxDistance {
		panic(optimizeError{distance: distance})
	}

	// replace_children: recurse into each direct child first, then handle self.
	for _, child := range expression.Children() {
		if isNilExpr(child) {
			continue
		}
		newChild := n.distributiveLaw(child)
		if newChild != child {
			ReplaceChild(expression, child, newChild)
		}
	}

	// to_exp/from_exp are the opposite of the outer connector
	fromOp, toOp := "OR", "AND"
	if n.dnf {
		fromOp, toOp = "AND", "OR"
	}

	if !isConnectorOp(expression, fromOp) {
		return expression
	}

	// `expression.unnest_operands()` is always (left, right)
	ops := unnestOperands(expression)
	if len(ops) < 2 {
		return expression
	}
	a, b := ops[0], ops[1]

	if isConnectorOp(a, toOp) && isConnectorOp(b, toOp) {
		if connectorCount(a) > connectorCount(b) {
			return n.distribute(a, b, fromOp, toOp)
		}
		return n.distribute(b, a, fromOp, toOp)
	}
	if isConnectorOp(a, toOp) {
		return n.distribute(b, a, fromOp, toOp)
	}
	if isConnectorOp(b, toOp) {
		return n.distribute(a, b, fromOp, toOp)
	}

	return expression
}

// connectorCount mirrors `len(tuple(a.find_all(exp.Connector)))`, including a itself.
func connectorCount(a Expression) int {
	count := 0
	for _, node := range Walk(a, true, nil) {
		if isConnectorNode(node) {
			count++
		}
	}
	return count
}

// isConnectorOp reports whether the expression is a connector of the given kind ("AND"/"OR"/"XOR").
func isConnectorOp(e Expression, op string) bool {
	o, ok := connectorOp(e)
	return ok && o == op
}

// distribute mirrors _distribute. from_func(c, b.left) uses the default copy=True, so
// both sides must be copied each time.
func (n *normalizer) distribute(a, b Expression, fromOp, toOp string) Expression {
	bt, br := b.(*Binary).Left, b.(*Binary).Right

	build := func(l Expression) Expression {
		return combineWith([]Expression{
			n.simplifier.UniqSort(FlattenExpr(combineWith([]Expression{l.Copy(), bt.Copy()}, fromOp)), true),
			n.simplifier.UniqSort(FlattenExpr(combineWith([]Expression{l.Copy(), br.Copy()}, fromOp)), true),
		}, toOp)
	}

	if isConnectorNode(a) {
		// exp.replace_children(a, lambda c: to_func(...))
		for _, c := range a.Children() {
			if isNilExpr(c) {
				continue
			}
			newChild := build(c)
			ReplaceChild(a, c, newChild)
		}
		return a
	}

	return build(a)
}

// normalizedExpr mirrors normalized(expression, dnf=False): CNF requires no OR above an
// AND; DNF is the reverse.
func normalizedExpr(expression Expression, dnf bool) bool {
	// For dnf: ancestor=And, root=Or; otherwise ancestor=Or, root=And.
	ancOp, rootOp := "AND", "OR"
	if !dnf {
		ancOp, rootOp = "OR", "AND"
	}

	for _, connector := range findAllInScope(expression, true, func(n Expression) bool {
		op, ok := connectorOp(n)
		return ok && op == rootOp
	}) {
		if findAncestor(connector, func(n Expression) bool {
			op, ok := connectorOp(n)
			return ok && op == ancOp
		}) != nil {
			return false
		}
	}
	return true
}

// normalizationDistance mirrors normalization_distance, preserving Python's lazy
// generator + early-return semantics via a callback.
func normalizationDistance(expression Expression, dnf bool, max float64) int {
	total := -(countConnectors(expression) + 1)

	stop := false
	predicateLengths(expression, dnf, max, 0, func(length int) bool {
		total += length
		if float64(total) > max {
			stop = true
			return false
		}
		return true
	})
	_ = stop
	return total
}

// countConnectors counts Connector nodes in a subtree.
func countConnectors(e Expression) int {
	n := 0
	for _, node := range Walk(e, true, nil) {
		if isConnectorExpr(node) {
			n++
		}
	}
	return n
}

// predicateLengths mirrors _predicate_lengths; yield returning false means early stop.
func predicateLengths(
	expression Expression, dnf bool, max float64, depth int, yield func(int) bool,
) bool {
	if float64(depth) > max {
		return yield(depth)
	}

	expression = unnestExpr(expression)

	op, isConn := connectorOp(expression)
	if !isConn {
		return yield(1)
	}

	depth++
	ops := unnestOperands(expression)

	// For DNF take the Cartesian product over AND (Python: isinstance(e, And if dnf else Or)).
	expand := (dnf && op == "AND") || (!dnf && op == "OR")
	if expand {
		lefts := collectLengths(ops[0], dnf, max, depth)
		rights := collectLengths(ops[1], dnf, max, depth)
		for _, a := range lefts {
			for _, b := range rights {
				if !yield(a + b) {
					return false
				}
			}
		}
		return true
	}
	if !predicateLengths(ops[0], dnf, max, depth, yield) {
		return false
	}
	return predicateLengths(ops[1], dnf, max, depth, yield)
}

// collectLengths materializes _predicate_lengths for the Cartesian-product operands.
func collectLengths(expression Expression, dnf bool, max float64, depth int) []int {
	var out []int
	predicateLengths(expression, dnf, max, depth, func(v int) bool {
		out = append(out, v)
		return true
	})
	return out
}
