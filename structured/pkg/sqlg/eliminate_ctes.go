package sqlg

// Ports sqlglot/optimizer/eliminate_ctes.py: removes CTEs unreferenced by any scope.
// Traverse scopes in reverse (deleting a CTE decrements upstream refcounts, so chains
// drop one by one) and remove the CTE node itself; the WITH goes only when empty.

// EliminateCTEs mirrors eliminate_ctes.
func EliminateCTEs(expression Expression) Expression {
	root := BuildScope(expression)
	if root == nil {
		return expression
	}

	refCount := root.RefCount()

	// Reverse order (child scopes first) so CTE chains can be deleted
	scopes := root.Traverse()
	for i := len(scopes) - 1; i >= 0; i-- {
		scope := scopes[i]
		if scope.ScopeType != ScopeCTE {
			continue
		}
		if refCount[scope] > 0 {
			continue
		}

		// scopeOuterNode smooths over sqlg's unnested CTE scope so this matches
		// Python's `cte_node`.
		cteNode := scopeOuterNode(scope)
		if cteNode == nil {
			continue
		}
		withNode := cteNode.Parent()

		Pop(cteNode)

		// Drop the whole WITH once its last CTE is removed.
		if w, ok := withNode.(*With); ok && len(w.CTEs) <= 0 {
			Pop(w)
		}

		ss := scope.SelectedSources()
		for _, k := range ss.keys {
			if sub, ok := ss.m[k].Source.(*Scope); ok {
				refCount[sub]--
			}
		}
	}
	return expression
}
