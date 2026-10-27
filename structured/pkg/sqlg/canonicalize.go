package sqlg

import "strings"

// Ports sqlglot/optimizer/canonicalize.py. Rewrites presuppose type inference, so in the
// rule chain it must run AFTER annotate_types. All functions carry dialectName because
// node classes are dialect-determined at parse time. Structural divergences:
//   - replaceTree hand-writes exp.replace_tree (see its doc).
//   - No exp.Where/exp.Having wrapper nodes: ensureBools identifies them by "parent is
//     Select and this node is exactly its Where/Having field".
//   - exp.If (IF(a,b,c)) vs exp.When (CASE branch) are distinct: *Func{Name:"IF"} vs *When.

// canonicalizer carries the dialect context needed for one canonicalize call.
type canonicalizer struct {
	dialectName string
	// promote mirrors dialect.PROMOTE_TO_INFERRED_DATETIME_TYPE (true for doris/mysql/singlestore/starrocks)
	promote bool
}

// Canonicalize mirrors canonicalize().
func Canonicalize(expression Expression, dialect *Dialect) Expression {
	if expression == nil {
		return nil
	}
	if dialect == nil {
		dialect = GetDialect("")
	}
	c := &canonicalizer{
		dialectName: dialect.Name,
		promote:     dialectPromotesInferredDatetime(dialect.Name),
	}

	return replaceTree(expression, func(node Expression) Expression {
		node = c.addTextToConcat(node)
		node = c.replaceDateFuncs(node)
		node = c.coerceType(node)
		node = c.removeRedundantCasts(node)
		node = c.ensureBools(node)
		node = c.removeAscendingOrder(node)
		return node
	})
}

// replaceTree mirrors exp.replace_tree: apply fun leaves-first. Materialize pre-order,
// pop from the tail; new nodes are replaced in place and re-pushed for another fun pass.
// Returns fun's result from the LAST call; Replace on the parentless root is a no-op
// (both match Python).
func replaceTree(expression Expression, fun func(Expression) Expression) Expression {
	// Pre-order slice, popped from the tail -> children are always processed before parents
	stack := WalkDFS(expression, nil)
	last := expression

	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		newNode := fun(node)
		last = newNode

		if newNode != node {
			// Root has no parent: Replace is a no-op, as in Python's node.replace
			Replace(node, newNode)
			if newNode != nil {
				// New node must pass through fun again (e.g. Add -> Concat needs the later steps)
				stack = append(stack, newNode)
			}
		}
	}
	return last
}

// classOfFuncIn resolves a function name to its sqlglot class name for the dialect (*Func only).
func classOfFuncIn(dialectName, name string) string {
	up := strings.ToUpper(name)
	if m := dialectFuncClassTables[dialectName]; m != nil {
		if v, ok := m[up]; ok {
			return v
		}
	}
	if v, ok := funcClass[up]; ok {
		return v
	}
	return "Anonymous"
}

// --- 1. add_text_to_concat ---

// concatUsesDPipe: dialects rendering exp.Concat as `a || b` (CONCAT_COALESCE and not coalesce).
var concatUsesDPipe = map[string]bool{
	"dremio": true, "drill": true, "duckdb": true, "exasol": true, "materialize": true,
	"postgres": true, "redshift": true, "risingwave": true, "sqlite": true,
}

// concatUsesPlus: dialects rendering Concat as `a + b` (tsql family).
var concatUsesPlus = map[string]bool{"tsql": true, "fabric": true}

// addTextToConcat mirrors add_text_to_concat: text-typed Add becomes concatenation,
// rendered `a || b` / `a + b` (tsql) / CONCAT(a,b) by dialect. The `a + b` branch must
// return the ORIGINAL node, else replaceTree re-pushes it forever (infinite loop).
func (c *canonicalizer) addTextToConcat(node Expression) Expression {
	b, ok := node.(*Binary)
	if !ok || b.Op != "+" || b.Left == nil || b.Right == nil {
		return node
	}
	t := node.Type()
	if t == nil || !textTypeSet[typeBase(t.Name)] {
		return node
	}
	switch {
	case concatUsesPlus[c.dialectName]:
		// tsql renders `a + 'x'` identically to the input; return as-is (no rewrite)
		return node
	case concatUsesDPipe[c.dialectName]:
		dp := &Binary{Op: "||", Left: b.Left, Right: b.Right}
		retag(dp, dp.Left, dp.Right)
		return dp
	}
	f := &Func{Name: "CONCAT", Args: []Expression{b.Left, b.Right}}
	retag(f, f.Args...)
	return f
}

// --- 2. replace_date_funcs ---

// replaceDateFuncs mirrors replace_date_funcs: DATE/TO_DATE of an ISO date literal becomes
// CAST(lit AS DATE); TIMESTAMP('...') annotates in place then becomes CAST(arg AS type).
// `DATE '2024-01-01'` is a Cast in sqlglot / *TypedLiteral in sqlg — both left untouched.
func (c *canonicalizer) replaceDateFuncs(node Expression) Expression {
	f, ok := node.(*Func)
	if !ok {
		return node
	}
	switch classOfFuncIn(c.dialectName, f.Name) {
	case "Date", "TsOrDsToDate":
		if len(f.Args) != 1 {
			return node
		}
		lit, isLit := f.Args[0].(*Literal)
		if !isLit || !lit.IsString || !isISODate(lit.Value) {
			return node
		}
		// exp.cast(node.this, DATE): uses the original literal, no copy
		cast := &Cast{This: lit, To: newType("DATE")}
		retag(cast, cast.This)
		return cast
	case "Timestamp":
		// sqlg's TIMESTAMP call carries no zone argument (Python checks node.args zone)
		if len(f.Args) != 1 {
			return node
		}
		if node.Type() == nil {
			node = AnnotateTypes(node, nil, GetDialect(c.dialectName), true)
			if f2, ok := node.(*Func); ok {
				f = f2
			}
		}
		to := node.Type()
		if to == nil {
			to = newType("TIMESTAMP")
		}
		cast := &Cast{This: f.Args[0], To: &DataType{Name: to.Name}}
		retag(cast, cast.This)
		return cast
	}
	return node
}

// --- 3. coerce_type ---

// coercibleDateOps mirrors COERCIBLE_DATE_OPS; sqlg decides by Binary.Op.
var coercibleDateOps = map[string]bool{
	"+": true, "-": true,
	"=": true, "<>": true, "!=": true,
	">": true, ">=": true, "<": true, "<=": true,
	"<=>":              true,
	"IS DISTINCT FROM": true, "IS NOT DISTINCT FROM": true,
}

// coerceType mirrors canonicalize.coerce_type.
func (c *canonicalizer) coerceType(node Expression) Expression {
	switch v := node.(type) {
	case *Binary:
		if coercibleDateOps[v.Op] {
			c.coerceDate(v.Left, v.Right)
		}
	case *Between:
		c.coerceDate(v.This, v.Low)
	case *Func:
		switch classOfFuncIn(c.dialectName, v.Name) {
		case "Extract":
			// CAST the argument if it is not a temporal type
			if arg := extractArgExpr(v); arg != nil {
				if t := arg.Type(); t != nil && !temporalTypeSet[typeBase(t.Name)] {
					c.replaceCast(arg, newType("DATETIME"))
				}
			}
		case "DateAdd", "DateSub", "DateTrunc":
			if len(v.Args) > 0 {
				c.coerceTimeUnitArg(v.Args[0], lastIntervalUnit(v))
			}
		case "DateDiff":
			// CAST each of (this, expression) whose type is not temporal
			for _, e := range dateDiffArgs(v) {
				if e == nil {
					continue
				}
				if t := e.Type(); t != nil && !temporalTypeSet[typeBase(t.Name)] {
					c.replaceCast(e, newType("DATETIME"))
				}
			}
		}
	}
	return node
}

// extractArgExpr returns the expr in EXTRACT(part FROM expr); sqlg keeps it in the second
// argument, degrading to the first when there is only one.
func extractArgExpr(f *Func) Expression {
	if len(f.Args) == 0 {
		return nil
	}
	if len(f.Args) >= 2 {
		return f.Args[1]
	}
	return f.Args[0]
}

// lastIntervalUnit returns the unit of the trailing `INTERVAL n unit` argument, or "" if none.
func lastIntervalUnit(f *Func) string {
	for i := len(f.Args) - 1; i >= 0; i-- {
		if iv, ok := f.Args[i].(*Interval); ok {
			return iv.Unit
		}
	}
	return ""
}

// dateDiffArgs returns DateDiff's first two arguments.
func dateDiffArgs(f *Func) []Expression {
	if len(f.Args) < 2 {
		return f.Args
	}
	return f.Args[:2]
}

// coerceDate mirrors _coerce_date. Verbatim semantics: permutations runs twice ((a,b) then
// (b,a)); the Interval branch REBINDS x to an untyped Cast so the following type check
// continues; `target_type != a_type` compares enum vs instance — in the non-promote branch
// target IS a_type (no CAST on the a side), in the promote branch it is an enum (CAST added).
func (c *canonicalizer) coerceDate(a, b Expression) {
	if a == nil || b == nil {
		return
	}
	for _, pair := range [2][2]Expression{{a, b}, {b, a}} {
		x, y := pair[0], pair[1]

		if iv, ok := y.(*Interval); ok {
			// Rebinding: the Cast produced here has no type, so the check below continues
			x = c.coerceTimeUnitArg(x, iv.Unit)
		}

		xType := x.Type()
		yType := y.Type()
		if xType == nil || !temporalTypeSet[typeBase(xType.Name)] ||
			yType == nil || !textTypeSet[typeBase(yType.Name)] {
			continue
		}

		if c.promote {
			bType := "DATETIME"
			if lit, isLit := y.(*Literal); isLit && lit.IsString {
				switch {
				case isISODate(lit.Value):
					bType = "DATE"
				case isISODatetime(lit.Value):
					bType = "DATETIME"
				default:
					// Unparseable date -> fall back to a's type (target == a_type, no a-side CAST)
					bType = typeBase(xType.Name)
				}
			}
			if baseCoercesTo[typeBase(xType.Name)][bType] {
				// target is an enum here: a-side CAST is added too
				c.replaceCast(x, newType(bType))
			}
			c.replaceCast(y, newType(bType))
			continue
		}

		// Non-promote: target is a's own type; only the b side gets a CAST
		c.replaceCast(y, &DataType{Name: xType.Name})
	}
}

// coerceTimeUnitArg mirrors _coerce_timeunit_arg. Rewrites: ISO-date text + date unit ->
// CAST AS DATE; ISO date/datetime text -> CAST AS DATETIME; DATE with non-date unit ->
// CAST AS DATETIME. Python replaces with a COPY (arg.copy()), hence a new node is returned.
func (c *canonicalizer) coerceTimeUnitArg(arg Expression, unit string) Expression {
	if arg == nil || arg.Type() == nil {
		return arg
	}
	switch {
	case textTypeSet[typeBase(arg.Type().Name)]:
		dateText := literalTextOf(arg)
		isoDate := isISODate(dateText)
		if isoDate && isDateUnit(unit) {
			return c.replaceCast(arg, newType("DATE"))
		}
		// An ISO date is also an ISO datetime, but not vice versa
		if isoDate || isISODatetime(dateText) {
			return c.replaceCast(arg, newType("DATETIME"))
		}
	case typeBase(arg.Type().Name) == "DATE" && !isDateUnit(unit):
		return c.replaceCast(arg, newType("DATETIME"))
	}
	return arg
}

// replaceCast mirrors _replace_cast: replaces node with CAST(copy AS to) and returns the
// new node. RefreshParents keeps the shallow-copy invariant.
func (c *canonicalizer) replaceCast(node Expression, to *DataType) Expression {
	if node == nil {
		return node
	}
	inner := RefreshParents(node.Copy())
	cast := &Cast{This: inner, To: to}
	retag(cast, cast.This)
	Replace(node, cast)
	return cast
}

// --- 4. remove_redundant_casts ---

// removeRedundantCasts mirrors remove_redundant_casts. Known approximation: sqlglot
// compares DataType structurally; sqlg compares type-name strings only.
func (c *canonicalizer) removeRedundantCasts(expression Expression) Expression {
	if cast, ok := expression.(*Cast); ok && cast.This != nil && cast.To != nil {
		if t := cast.This.Type(); t != nil && cast.To.Name == t.Name {
			return cast.This
		}
	}

	// Date/TsOrDsToDate over an exactly-DATE argument degrades to the argument
	if f, ok := expression.(*Func); ok && len(f.Args) == 1 {
		if cls := classOfFuncIn(c.dialectName, f.Name); cls == "Date" || cls == "TsOrDsToDate" {
			if t := f.Args[0].Type(); t != nil && t.Name == "DATE" {
				return f.Args[0]
			}
		}
	}
	return expression
}

// --- 5. ensure_bools ---

// ensureBools mirrors ensure_bools. Recurses only at connector/NOT/IF/WHERE positions —
// never scan the whole tree: the x in `CASE x WHEN <int> ...` must not be rewritten.
func (c *canonicalizer) ensureBools(expression Expression) Expression {
	// No Where/Having wrapper nodes in sqlg: identify by "parent Select's Where/Having is
	// this node" — same effect as Python's replace_func(wrapper.this).
	if sel, ok := parentOfType[*Select](expression); ok &&
		(sel.Where == expression || sel.Having == expression) {
		c.replaceIntPredicate(expression)
	}

	switch v := expression.(type) {
	case *Binary:
		if v.Op == "AND" || v.Op == "OR" {
			c.replaceIntPredicate(v.Left)
			c.replaceIntPredicate(v.Right)
		}
	case *Unary:
		if v.Op == "NOT" {
			c.replaceIntPredicate(v.This)
		}
	case *Func:
		// exp.If is the IF(a,b,c) function, not a CASE WHEN branch
		if classOfFuncIn(c.dialectName, v.Name) != "If" {
			break
		}
		// Skip IF inside `CASE x WHEN n ...` (parent Case has an operand)
		if cs, ok := parentOfType[*Case](v); ok && cs.Operand != nil {
			break
		}
		if len(v.Args) > 0 {
			c.replaceIntPredicate(v.Args[0])
		}
	case *WhereClause:
		c.replaceIntPredicate(v.This)
	}
	return expression
}

// parentOfType returns the node's parent when it has type T.
func parentOfType[T Expression](node Expression) (T, bool) {
	var zero T
	p := node.Parent()
	if p == nil {
		return zero, false
	}
	v, ok := p.(T)
	return v, ok
}

// replaceIntPredicate mirrors _replace_int_predicate: integer-typed predicates in boolean
// positions become `x <> 0` (Presto has no boolean type). Python's neq(0) uses a COPY.
func (c *canonicalizer) replaceIntPredicate(expression Expression) {
	if expression == nil {
		return
	}
	// Coalesce is variadic: recurse into each argument
	if f, ok := expression.(*Func); ok && classOfFuncIn(c.dialectName, f.Name) == "Coalesce" {
		for _, a := range f.Args {
			c.replaceIntPredicate(a)
		}
		return
	}
	t := expression.Type()
	if t == nil || !integerTypeSet[typeBase(t.Name)] {
		return
	}
	neq := &Binary{
		Op:    "<>",
		Left:  RefreshParents(expression.Copy()),
		Right: &Literal{Value: "0"},
	}
	retag(neq, neq.Left, neq.Right)
	Replace(expression, neq)
}

// --- 6. remove_ascending_order ---

// removeAscendingOrder mirrors remove_ascending_order: acts only on EXPLICIT ASC
// (`ORDER BY a ASC` -> `ORDER BY a`); unset ASC/DESC is left alone, matching Python's
// `desc is False` check via sqlg's Explicit flag.
func (c *canonicalizer) removeAscendingOrder(expression Expression) Expression {
	if o, ok := expression.(*Ordered); ok && o.Explicit && !o.Desc {
		o.Explicit = false
	}
	return expression
}
