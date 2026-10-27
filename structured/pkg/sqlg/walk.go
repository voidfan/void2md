package sqlg

import "reflect"

// Traversal/rewrite primitives of sqlglot's exp.Expression: walk/dfs/bfs, find_all,
// find, transform, replace, pop. Defaults that are easy to get wrong: walk/find_all
// default to BFS, and transform's DFS prunes subtrees once fun returns a new object.

// WalkBFS mirrors Expression.bfs; prune=true skips descending into the branch.
func WalkBFS(root Expression, prune func(Expression) bool) []Expression {
	if root == nil {
		return nil
	}
	var out []Expression
	queue := []Expression{root}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		out = append(out, n)
		if prune != nil && prune(n) {
			continue
		}
		queue = append(queue, n.Children()...)
	}
	return out
}

// WalkDFS mirrors Expression.dfs: pre-order; children pushed in reverse for left-to-right order.
func WalkDFS(root Expression, prune func(Expression) bool) []Expression {
	if root == nil {
		return nil
	}
	var out []Expression
	stack := []Expression{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		out = append(out, n)
		if prune != nil && prune(n) {
			continue
		}
		ch := n.Children()
		for i := len(ch) - 1; i >= 0; i-- {
			stack = append(stack, ch[i])
		}
	}
	return out
}

// Walk mirrors Expression.walk; bfs determines the traversal order.
func Walk(root Expression, bfs bool, prune func(Expression) bool) []Expression {
	if bfs {
		return WalkBFS(root, prune)
	}
	return WalkDFS(root, prune)
}

// FindAllOf mirrors Expression.find_all(*types, bfs=True). Note the BFS default:
// sqlglot's rules rely on that order.
func FindAllOf[T Expression](root Expression, bfs bool) []T {
	var out []T
	for _, n := range Walk(root, bfs, nil) {
		if v, ok := n.(T); ok {
			out = append(out, v)
		}
	}
	return out
}

// FindFirstOf mirrors Expression.find(*expression_types, bfs=True).
func FindFirstOf[T Expression](root Expression, bfs bool) (T, bool) {
	var zero T
	for _, n := range Walk(root, bfs, nil) {
		if v, ok := n.(T); ok {
			return v, true
		}
	}
	return zero, false
}

// HasNode reports whether the subtree contains a node satisfying the predicate (BFS).
func HasNode(root Expression, pred func(Expression) bool) bool {
	for _, n := range WalkBFS(root, nil) {
		if pred(n) {
			return true
		}
	}
	return false
}

// ReplaceChild replaces one direct child in place, mirroring parent.set(arg_key, value,
// index). Locates the slot by identity scan of Children() and writes back via type
// switch; returns false if old is not a direct child.
func ReplaceChild(parent, old, new Expression) bool {
	if parent == nil || old == nil {
		return false
	}
	idx := -1
	for i, c := range parent.Children() {
		if c == old {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false
	}
	ok := setChildAt(parent, idx, new)
	if ok && new != nil {
		// Fill in the parent pointer (sqlglot maintains it at construction);
		// without it, scope logic diverges after Transform/Copy.
		new.SetParent(parent)
	}
	return ok
}

// setChildAt writes back the idx-th child of parent. Every branch must match the
// Children() order of its node exactly, or positions get scrambled.
func setChildAt(parent Expression, idx int, new Expression) bool {
	switch n := parent.(type) {
	case *Alias:
		// Children: [This, Alias]
		if idx == 0 {
			n.This = new
		} else {
			n.Alias = asIdent(new)
		}
	case *Between:
		switch idx {
		case 0:
			n.This = new
		case 1:
			n.Low = new
		case 2:
			n.High = new
		default:
			return false
		}
	case *Binary:
		if idx == 0 {
			n.Left = new
		} else {
			n.Right = new
		}
	case *Bracket:
		if idx == 0 {
			n.This = new
		} else {
			n.Index = new
		}
	case *Case:
		// Children: [Operand?] + Ifs... + [Default?]
		cursor := 0
		if n.Operand != nil {
			if cursor == idx {
				n.Operand = new
				break
			}
			cursor++
		}
		if idx < cursor+len(n.Ifs) {
			w := n.Ifs[idx-cursor]
			if new != nil {
				if nw, ok := new.(*When); ok {
					n.Ifs[idx-cursor] = nw
					_ = w
					break
				}
			}
			if new == nil {
				n.Ifs = append(n.Ifs[:idx-cursor], n.Ifs[idx-cursor+1:]...)
				break
			}
			return false
		}
		cursor += len(n.Ifs)
		if n.Default != nil && cursor == idx {
			n.Default = new
			break
		}
		return false
	case *Cast:
		// Children: [This, To?]
		if idx == 0 {
			n.This = new
		} else {
			dt, ok := new.(*DataType)
			if !ok {
				return false
			}
			n.To = dt
		}
	case *Column:
		if idx == 0 {
			n.Table = asIdent(new)
		} else {
			n.Name = asIdent(new)
		}
	case *CTE:
		// Children: [Alias?, This?]
		cursor := 0
		if n.Alias != nil {
			if cursor == idx {
				n.Alias = asTableAlias(new)
				break
			}
			cursor++
		}
		if cursor == idx {
			n.This = asSubquery(new)
			break
		}
		return false
	case *Delete:
		// Children: [Table, Where?, Limit?]
		cursor := 1
		if n.Where != nil {
			if cursor == idx {
				n.Where = new
				break
			}
			cursor++
		}
		if n.Limit != nil && cursor == idx {
			n.Limit = asLimit(new)
			break
		}
		return false
	case *Distinct:
		n.This = new
	case *Exists:
		n.This = asSubquery(new)
	case *Func:
		// Children: Args... + [Filter?]
		if idx < len(n.Args) {
			n.Args[idx] = new
			break
		}
		if n.Filter != nil && idx == len(n.Args) {
			n.Filter = new
			break
		}
		return false
	case *Group:
		n.Expressions[idx] = new
	case *In:
		// Children: [This] + List... + [Query?]
		if idx == 0 {
			n.This = new
			break
		}
		li := idx - 1
		if li < len(n.List) {
			n.List[li] = new
			break
		}
		if n.Query != nil && li == len(n.List) {
			n.Query = asSubquery(new)
			break
		}
		return false
	case *Insert:
		// Children: [Table, Query?, Values...]
		if idx == 0 {
			n.Table = new
			break
		}
		flat := 0
		if n.Query != nil {
			if idx == 1 {
				n.Query = new
				break
			}
			flat = 1
		}
		flat++
		for r := range n.Values {
			if idx-flat < len(n.Values[r]) {
				n.Values[r][idx-flat] = new
				return true
			}
			flat += len(n.Values[r])
		}
		return false
	case *Interval:
		n.Value = new
	case *Is:
		if idx == 0 {
			n.This = new
		} else {
			n.Expr = new
		}
	case *Join:
		// Children: [This, On?]
		if idx == 0 {
			n.This = new
		} else {
			n.On = new
		}
	case *Limit:
		n.Expression = new
	case *Offset:
		n.Expression = new
	case *Order:
		if idx < len(n.Expressions) && new != nil {
			o, ok := new.(*Ordered)
			if !ok {
				return false
			}
			n.Expressions[idx] = o
			break
		}
		if new == nil && idx < len(n.Expressions) {
			n.Expressions = append(n.Expressions[:idx], n.Expressions[idx+1:]...)
			break
		}
		return false
	case *Ordered:
		n.This = new
	case *Paren:
		n.This = new
	case *Select:
		return setSelectChildAt(n, idx, new)
	case *Star:
		n.Table = asIdent(new)
	case *Subquery:
		// Children: [This, Alias?]
		if idx == 0 {
			n.This = new
		} else {
			n.Alias = asTableAlias(new)
		}
	case *Table:
		// Children: Parts... + [Alias?]
		if idx < len(n.Parts) {
			id, ok := new.(*Ident)
			if !ok {
				return false
			}
			n.Parts[idx] = id
			break
		}
		if n.Alias != nil && idx == len(n.Parts) {
			n.Alias = asTableAlias(new)
			break
		}
		return false
	case *TableAlias:
		cursor := 0
		if n.Name != nil {
			if cursor == idx {
				n.Name = asIdent(new)
				break
			}
			cursor++
		}
		if idx-cursor < len(n.Columns) {
			id, ok := new.(*Ident)
			if !ok {
				return false
			}
			n.Columns[idx-cursor] = id
			break
		}
		return false
	case *Unary:
		n.This = new
	case *Union:
		// Children: [With?, Left?] + Right?
		cursor := 0
		if n.With != nil {
			if cursor == idx {
				n.With = asWith(new)
				break
			}
			cursor++
		}
		if n.Left != nil {
			if cursor == idx {
				n.Left = new
				break
			}
			cursor++
		}
		if cursor == idx {
			n.Right = new
			break
		}
		return false
	case *Update:
		// Children: [Table] + Set pairs... + [Where?]
		if idx == 0 {
			n.Table = new
			break
		}
		cursor := 1
		for i := range n.Set {
			if cursor == idx {
				n.Set[i][0] = new
				return true
			}
			cursor++
			if cursor == idx {
				n.Set[i][1] = new
				return true
			}
			cursor++
		}
		if n.Where != nil {
			if cursor == idx {
				n.Where = new
				break
			}
			cursor++
		}
		if n.Limit != nil && cursor == idx {
			n.Limit = asLimit(new)
			break
		}
		return false
	case *Values:
		cur := 0
		for r := range n.Rows {
			if idx-cur < len(n.Rows[r]) {
				n.Rows[r][idx-cur] = new
				return true
			}
			cur += len(n.Rows[r])
		}
		return false
	case *When:
		if idx == 0 {
			n.Cond = new
		} else {
			n.Then = new
		}
	case *WhereClause:
		n.This = new
	case *With:
		if idx < len(n.CTEs) && new != nil {
			c, ok := new.(*CTE)
			if !ok {
				return false
			}
			n.CTEs[idx] = c
			break
		}
		if new == nil && idx < len(n.CTEs) {
			n.CTEs = append(n.CTEs[:idx], n.CTEs[idx+1:]...)
			break
		}
		return false
	case *unionTail:
		return setUnionTailChildAt(n, idx, new)
	default:
		return false
	}
	retag(parent, parent.Children()...)
	return true
}

// setSelectChildAt writes back the idx-th child according to the order of Select.Children().
func setSelectChildAt(n *Select, idx int, new Expression) bool {
	cursor := 0
	if n.With != nil {
		if cursor == idx {
			n.With = asWith(new)
			retag(n, n.Children()...)
			return true
		}
		cursor++
	}
	if idx-cursor < len(n.Expressions) {
		if new == nil {
			n.Expressions = append(n.Expressions[:idx-cursor], n.Expressions[idx-cursor+1:]...)
			retag(n, n.Children()...)
			return true
		}
		n.Expressions[idx-cursor] = new
		retag(n, n.Children()...)
		return true
	}
	cursor += len(n.Expressions)
	if n.From != nil {
		if cursor == idx {
			n.From = new
			retag(n, n.Children()...)
			return true
		}
		cursor++
	}
	if idx-cursor < len(n.Joins) {
		if new == nil {
			n.Joins = append(n.Joins[:idx-cursor], n.Joins[idx-cursor+1:]...)
			retag(n, n.Children()...)
			return true
		}
		j, ok := new.(*Join)
		if !ok {
			return false
		}
		n.Joins[idx-cursor] = j
		retag(n, n.Children()...)
		return true
	}
	cursor += len(n.Joins)
	type slot struct {
		get  func() Expression
		set  func(Expression)
		live bool
	}
	slots := []slot{
		{func() Expression { return n.Where }, func(e Expression) { n.Where = e }, n.Where != nil},
		{func() Expression { return n.Group }, func(e Expression) { n.Group = asGroup(e) }, n.Group != nil},
		{func() Expression { return n.Having }, func(e Expression) { n.Having = e }, n.Having != nil},
		{func() Expression { return n.Order }, func(e Expression) { n.Order = asOrder(e) }, n.Order != nil},
		{func() Expression { return n.Limit }, func(e Expression) { n.Limit = asLimit(e) }, n.Limit != nil},
		{func() Expression { return n.Offset }, func(e Expression) { n.Offset = asOffset(e) }, n.Offset != nil},
	}
	for _, s := range slots {
		if !s.live {
			continue
		}
		if cursor == idx {
			s.set(new)
			retag(n, n.Children()...)
			return true
		}
		cursor++
	}
	return false
}

// setUnionTailChildAt writes back according to the order of unionTail.Children():
// [Union, Order?, Limit?, Offset?].
func setUnionTailChildAt(n *unionTail, idx int, new Expression) bool {
	cursor := 0
	if n.Union != nil {
		if cursor == idx {
			u, ok := new.(*Union)
			if !ok {
				return false
			}
			n.Union = u
			retag(n, n.Children()...)
			return true
		}
		cursor++
	}
	if n.Order != nil {
		if cursor == idx {
			o, ok := new.(*Order)
			if !ok {
				return false
			}
			n.Order = o
			retag(n, n.Children()...)
			return true
		}
		cursor++
	}
	if n.Limit != nil {
		if cursor == idx {
			l, ok := new.(*Limit)
			if !ok {
				return false
			}
			n.Limit = l
			retag(n, n.Children()...)
			return true
		}
		cursor++
	}
	if n.Offset != nil && cursor == idx {
		o, ok := new.(*Offset)
		if !ok {
			return false
		}
		n.Offset = o
		retag(n, n.Children()...)
		return true
	}
	return false
}

// Replace mirrors Expression.replace: swaps itself out of the corresponding
// slot in its parent for new.
func Replace(node, new Expression) Expression {
	parent := node.Parent()
	if parent == nil || parent == new {
		return new
	}
	if new == node {
		// sqlglot no-ops when self and expression are the same object
		return new
	}
	if new == nil {
		Pop(node)
		return nil
	}
	ReplaceChild(parent, node, new)
	new.SetParent(parent)
	node.SetParent(nil)
	return new
}

// Pop mirrors Expression.pop: detaches from parent and returns the node.
func Pop(node Expression) Expression {
	parent := node.Parent()
	if parent == nil {
		return node
	}
	ReplaceChild(parent, node, nil)
	node.SetParent(nil)
	return node
}

// Transform mirrors Expression.transform(fun, copy=True): DFS pre-order; once fun
// returns a new object the subtree counts as replaced and is not descended into.
func Transform(root Expression, fun func(Expression) Expression, copyTree bool) Expression {
	if root == nil {
		return nil
	}
	if copyTree {
		// sqlg's Copy does not fix parent pointers; without RefreshParents the
		// ReplaceChild below would silently discard callback-returned nodes.
		root = RefreshParents(root.Copy())
	}
	var out Expression
	stack := []Expression{root}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		parent := node.Parent()
		next := fun(node)
		if out == nil {
			out = next
		} else if parent != nil && next != node {
			ReplaceChild(parent, node, next)
		}
		if next != node {
			continue // this branch was replaced as a whole; do not descend
		}
		ch := node.Children()
		for i := len(ch) - 1; i >= 0; i-- {
			stack = append(stack, ch[i])
		}
	}
	if out == nil {
		return root
	}
	return out
}

// ---- Type-normalization helpers: Transform may insert nodes of other types ----

func asIdent(e Expression) *Ident {
	if e == nil {
		return nil
	}
	if v, ok := e.(*Ident); ok {
		return v
	}
	return nil
}

func asTableAlias(e Expression) *TableAlias {
	if e == nil {
		return nil
	}
	if v, ok := e.(*TableAlias); ok {
		return v
	}
	return nil
}

func asSubquery(e Expression) *Subquery {
	if e == nil {
		return nil
	}
	if v, ok := e.(*Subquery); ok {
		return v
	}
	return nil
}

func asWith(e Expression) *With {
	if e == nil {
		return nil
	}
	if v, ok := e.(*With); ok {
		return v
	}
	return nil
}

func asGroup(e Expression) *Group {
	if e == nil {
		return nil
	}
	if v, ok := e.(*Group); ok {
		return v
	}
	return nil
}

func asOrder(e Expression) *Order {
	if e == nil {
		return nil
	}
	if v, ok := e.(*Order); ok {
		return v
	}
	return nil
}

func asLimit(e Expression) *Limit {
	if e == nil {
		return nil
	}
	if v, ok := e.(*Limit); ok {
		return v
	}
	return nil
}

func asOffset(e Expression) *Offset {
	if e == nil {
		return nil
	}
	if v, ok := e.(*Offset); ok {
		return v
	}
	return nil
}

// isNilExpr reports whether the interface holds a typed nil pointer (which would make
// `e == nil` false and crash on SetParent).
func isNilExpr(e Expression) bool {
	if e == nil {
		return true
	}
	v := reflect.ValueOf(e)
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func:
		return v.IsNil()
	}
	return false
}

// RefreshParents rebuilds parent pointers top-down and returns the root. Needed because
// sqlg's Copy() and some parse branches leave deep nodes parentless; refresh after parse
// and after Copy to restore sqlglot's parent invariant.
func RefreshParents(root Expression) Expression {
	if isNilExpr(root) {
		return root
	}
	root.SetParent(nil)
	stack := []Expression{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, c := range n.Children() {
			if isNilExpr(c) {
				continue
			}
			c.SetParent(n)
			stack = append(stack, c)
		}
	}
	return root
}
