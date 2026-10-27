package sqlg

import (
	"strconv"
	"strings"
)

// Rest of sqlglot.optimizer.qualify_columns: _expand_alias_refs / _qualify_columns /
// _expand_stars / qualify_outputs / _expand_group_by / _expand_order_by_and_distinct_on /
// validate_qualify_columns. Divergences: no Pivot/Unpivot/Struct/Dot/TableColumn/Qualify/
// Window nodes (those branches never fire); Distinct is a bool so DISTINCT ON is only
// handled on the order side; Star EXCEPT/REPLACE/RENAME are not parsed.

// isStarExpr mirrors Expression.is_star. sqlglot's `t.*` is Column(this=Star); sqlg uses
// Star{Table: t} — both take the same branch in _expand_stars.
func isStarExpr(e Expression) bool {
	_, ok := e.(*Star)
	return ok
}

// starTableName returns the t in `t.*` (empty for `*`).
func starTableName(s *Star) string {
	if s.Table == nil {
		return ""
	}
	return s.Table.Name
}

// --- _expand_alias_refs ---

// aliasExprRef mirrors (alias_expr, i) in alias_to_expression.
type aliasExprRef struct {
	Expr   Expression
	Index  int
	Exists bool
}

// expandAliasRefsFunc mirrors _expand_alias_refs.
func expandAliasRefsFunc(
	scope *Scope, resolver *Resolver, d *Dialect, expandOnlyGroupby bool,
) {
	selectExpr, ok := scope.Expression.(*Select)
	if !ok || d.DisablesAliasRefExpansion {
		return
	}

	aliasToExpression := map[string]aliasExprRef{}
	projections := map[string]bool{}
	for _, s := range selectExpr.Expressions {
		projections[outputNameOf(s)] = true
	}
	replaced := false

	replaceColumns := func(node Expression, resolveTable, literalIndex bool) {
		isGroupBy := false
		if _, ok := node.(*Group); ok {
			isGroupBy = true
		}
		if node == nil || (expandOnlyGroupby && !isGroupBy) {
			return
		}

		for _, n := range walkInScope(node, true) {
			column, ok := n.(*Column)
			if !ok {
				continue
			}
			// BigQuery GROUP BY expands aliases only for bare column names;
			// EXPAND_ONLY_GROUP_ALIAS_REF is always False in the corpus, kept for parity.
			if expandOnlyGroupby && isGroupBy && column.Parent() != node {
				continue
			}

			var table *Ident
			if resolveTable && column.Table == nil {
				table = resolver.GetTable(colName(column), nil)
			}

			ref := aliasToExpression[colName(column)]
			skipReplace := false

			if ref.Exists {
				// Skip when expanding would nest an aggregate inside an aggregate
				// (no Window node in between).
				agg := findAncestorAggFunc(column)
				if containsAggFunc(ref.Expr) && agg != nil &&
					!nodeIsWindow(findAncestorWindowOrSelect(column)) {
					skipReplace = true
				}
				// PROJECTION_ALIASES_SHADOW_SOURCE_NAMES is always False for the dialects used by structured.
			} else if d.ProjectionAliasesShadowSourceNames && (isGroupBy || isHavingNode(node)) {
				columnTable := identText(column.Table)
				if table != nil {
					columnTable = table.Name
				}
				if projections[columnTable] {
					// Replace `column` with a bare identifier of the same name
					Replace(column, column.Name.Copy())
					replaced = true
					return
				}
			}

			if table != nil && (!ref.Exists || skipReplace) {
				column.Table = table
				retag(column, table, column.Name)
			} else if column.Table == nil && ref.Exists && !skipReplace {
				// Literal or number: locate the i-th projection
				if (ref.Expr != nil && (isLiteralNode(ref.Expr) || isNumberExpr(ref.Expr))) &&
					(literalIndex || resolveTable) {
					if literalIndex {
						Replace(column, &Literal{Value: itoa(ref.Index)})
						replaced = true
					}
				} else {
					replaced = true
					p := &Paren{This: ref.Expr.Copy()}
					retag(p, p.This)
					Replace(column, p)
					simplified := SimplifyParens(p, d)
					if simplified != Expression(p) {
						Replace(p, simplified)
					}
				}
			}
		}
	}

	for i, projection := range selectExpr.Expressions {
		replaceColumns(projection, false, false)
		if al, ok := projection.(*Alias); ok && al.Alias != nil {
			aliasToExpression[al.Alias.Name] = aliasExprRef{Expr: al.This, Index: i + 1, Exists: true}
		}
	}

	// Aliases must not be expanded inside a recursive CTE's right subtree (they would
	// conflict with the CTE column names).
	parentScope := scope
	onRightSubTree := false
	for parentScope != nil && parentScope.ScopeType != ScopeCTE {
		if parentScope.ScopeType == ScopeUnion && parentScope.Parent != nil {
			if u, ok := parentScope.Parent.Expression.(*Union); ok {
				onRightSubTree = u.Right == parentScope.Expression
			}
		}
		parentScope = parentScope.Parent
	}
	if parentScope != nil && onRightSubTree && parentScope.Expression != nil {
		if cte, ok := parentScope.Expression.Parent().(*CTE); ok {
			if w := cteWith(cte); w != nil && w.Recursive {
				var names []string
				if a := aliasOfNode(cte); a != nil && len(a.Columns) > 0 {
					names = aliasColumnNames(a)
				} else {
					names = namedSelectsOf(cte.This)
				}
				for _, n := range names {
					delete(aliasToExpression, n)
				}
			}
		}
	}

	// Nil-check before passing: a nil *Group boxed into Expression would defeat
	// Python's `not node` check.
	if selectExpr.Where != nil {
		replaceColumns(selectExpr.Where, false, false)
	}
	if selectExpr.Group != nil {
		replaceColumns(selectExpr.Group, false, true)
	}
	if selectExpr.Having != nil {
		replaceColumns(selectExpr.Having, true, false)
	}
	// qualify (the QUALIFY clause) is not parsed by sqlg, so it is skipped.

	if replaced {
		scope.clearCache()
	}
}

// cteWith returns the WITH clause a CTE belongs to (mirrors cte.find_ancestor(exp.With)).
func cteWith(cte *CTE) *With {
	for p := cte.Parent(); p != nil; p = p.Parent() {
		if w, ok := p.(*With); ok {
			return w
		}
	}
	return nil
}

func isHavingNode(e Expression) bool { return false } // sqlg has no standalone Having node type

func isLiteralNode(e Expression) bool {
	_, ok := e.(*Literal)
	return ok
}

func nodeIsWindow(e Expression) bool {
	// sqlg keeps OVER(...) verbatim in Func.Over; there is no standalone Window node.
	return false
}

// findAncestorAggFunc mirrors column.find_ancestor(exp.AggFunc).
func findAncestorAggFunc(c *Column) Expression {
	for p := c.Parent(); p != nil; p = p.Parent() {
		if isAggFuncNode(p) {
			return p
		}
	}
	return nil
}

// findAncestorWindowOrSelect mirrors column.find_ancestor(exp.Window, exp.Select).
func findAncestorWindowOrSelect(c *Column) Expression {
	for p := c.Parent(); p != nil; p = p.Parent() {
		if nodeIsSelect(p) {
			return p
		}
	}
	return nil
}

// containsAggFunc mirrors expression.find(exp.AggFunc): a DFS lookup including the node itself.
func containsAggFunc(e Expression) bool {
	for _, n := range WalkDFS(e, nil) {
		if isAggFuncNode(n) {
			return true
		}
	}
	return false
}

func isAggFuncNode(e Expression) bool {
	f, ok := e.(*Func)
	return ok && isAggFunc(f)
}

// --- SimplifyParens ---

// SimplifyParens mirrors simplify.simplify_parens.
// REQUIRES_PARENTHESIZED_STRUCT_ACCESS is always False for the dialects used.
func SimplifyParens(expression Expression, d *Dialect) Expression {
	p, ok := expression.(*Paren)
	if !ok {
		return expression
	}
	this := p.This
	parent := p.Parent()

	// SubqueryPredicate/Bracket parents: sqlg has neither node type
	if _, ok := this.(*Select); ok {
		return expression
	}

	if !isConditionOrBinary(parent) || nodeIsParen(parent) {
		return this
	}
	if !isBinaryNode(this) && !(isNotNode(this) || nodeIsIs(this)) {
		return this
	}
	if isPredicateNode(this) && !(isPredicateNode(parent) || isNegNode(parent)) {
		return this
	}
	if isOp(this, "+") && isOp(parent, "+") {
		return this
	}
	if isMulNode(this) && isMulNode(parent) {
		return this
	}
	if isMulNode(this) && (isOp(parent, "+") || isOp(parent, "-")) {
		return this
	}
	return expression
}

func isOp(e Expression, op string) bool {
	b, ok := e.(*Binary)
	return ok && b.Op == op
}

func isMulNode(e Expression) bool {
	b, ok := e.(*Binary)
	return ok && (b.Op == "*" || b.Op == "/" || b.Op == "%")
}

func isNotNode(e Expression) bool {
	u, ok := e.(*Unary)
	return ok && strings.EqualFold(u.Op, "NOT")
}

func isNegNode(e Expression) bool {
	u, ok := e.(*Unary)
	return ok && (u.Op == "-" || strings.EqualFold(u.Op, "NOT"))
}

func nodeIsIs(e Expression) bool {
	_, ok := e.(*Is)
	return ok
}

func nodeIsParen(e Expression) bool {
	_, ok := e.(*Paren)
	return ok
}

func isPredicateNode(e Expression) bool {
	switch e.(type) {
	case *In, *Between, *Is:
		return true
	}
	return false
}

func isBinaryNode(e Expression) bool {
	_, ok := e.(*Binary)
	return ok
}

func isConditionOrBinary(e Expression) bool {
	switch e.(type) {
	case *Binary, *In, *Between, *Is, *Exists, *Unary:
		return true
	}
	return false
}

// --- _qualify_columns ---

func qualifyColumns(scope *Scope, resolver *Resolver, allowPartial bool) {
	for _, column := range scope.Columns() {
		columnTable := identText(column.Table)
		columnName := colName(column)

		if columnTable != "" && scope.Sources().Has(columnTable) {
			sourceColumns, _ := resolver.GetSourceColumns(columnTable, false)
			if !allowPartial && len(sourceColumns) > 0 &&
				!containsStr(sourceColumns, columnName) && !containsStr(sourceColumns, "*") {
				panic(&OptimizeError{Msg: "Unknown column: " + columnName})
			}
		}

		if columnTable == "" {
			// pivots branch: sqlg has no Pivot node.
			t := resolver.GetTable(columnName, column)
			if t != nil {
				column.Table = t
				retag(column, t, column.Name)
			} else if resolver.Dialect.TablesReferenceableAsColumns &&
				scope.SelectedSources().hasKey(columnName) {
				// postgres: table names are referenceable as columns; sqlg has no
				// TableColumn node, so the column stays unqualified (known divergence).
			}
		}
	}
}

// --- _expand_stars ---

func expandStarsFunc(scope *Scope, resolver *Resolver, usingColumnTables *orderedStrSliceMap) {
	selectExpr, ok := scope.Expression.(*Select)
	if !ok {
		return
	}

	var newSelections []Expression
	coalescedColumns := map[string]bool{}

	for _, expression := range selectExpr.Expressions {
		var tables []string
		if star, ok := expression.(*Star); ok {
			if star.Table == nil {
				tables = append(tables, scope.SelectedSources().keys...)
			} else {
				tables = append(tables, starTableName(star))
			}
			// Star EXCEPT/REPLACE/RENAME are not parsed; all three tables are always empty.
		}

		if len(tables) == 0 {
			newSelections = append(newSelections, expression)
			continue
		}

		for _, table := range tables {
			if !scope.Sources().Has(table) {
				panic(&OptimizeError{Msg: "Unknown table: " + table})
			}

			columns, _ := resolver.GetSourceColumns(table, true)
			if len(columns) == 0 {
				columns = scope.OuterColumns
			}

			// pseudocolumns filtering: EXCLUDES_PSEUDOCOLUMNS_FROM_STAR always False here

			if len(columns) == 0 || containsStr(columns, "*") {
				// Python returns here, abandoning ALL star expansion
				return
			}

			for _, name := range columns {
				if coalescedColumns[name] {
					continue
				}
				if usingColumnTables.has(name) && containsStr(usingColumnTables.get(name), table) {
					coalescedColumns[name] = true
					var args []Expression
					for _, t := range usingColumnTables.get(name) {
						c := &Column{Table: NewIdent(t), Name: NewIdent(name)}
						retag(c, c.Table, c.Name)
						args = append(args, c)
					}
					newSelections = append(newSelections, makeAlias(newFunc("COALESCE", args, false), name))
				} else {
					c := &Column{Table: NewIdent(table), Name: NewIdent(name)}
					retag(c, c.Table, c.Name)
					newSelections = append(newSelections, c)
				}
			}
		}
	}

	if len(newSelections) > 0 {
		setSelectExpressions(selectExpr, newSelections)
	}
}

func setSelectExpressions(sel *Select, exprs []Expression) {
	sel.Expressions = exprs
	retag(sel, exprs...)
}

// makeAlias mirrors sqlglot.helper.alias(expression, alias, copy=False).
func makeAlias(e Expression, name string) *Alias {
	// to_identifier(alias, quoted=None): unsafe names get auto-quoted (MySQL `1` -> `` `1` ``)
	a := &Alias{This: e, Alias: IdentFromName(name)}
	retag(a, e, a.Alias)
	return a
}

// --- qualify_outputs ---

func qualifyOutputs(scope *Scope) {
	selectExpr, ok := scope.Expression.(*Select)
	if !ok {
		return
	}
	if len(selectExpr.Expressions) == 0 && len(scope.OuterColumns) == 0 {
		return
	}

	n := len(selectExpr.Expressions)
	if len(scope.OuterColumns) > n {
		n = len(scope.OuterColumns)
	}

	var newSelections []Expression
	for i := 0; i < n; i++ {
		var selection Expression
		if i < len(selectExpr.Expressions) {
			selection = selectExpr.Expressions[i]
		}
		var aliasedColumn string
		if i < len(scope.OuterColumns) {
			aliasedColumn = scope.OuterColumns[i]
		}
		if selection == nil {
			break
		}

		if sq, ok := selection.(*Subquery); ok {
			if outputNameOf(sq) == "" {
				sq.Alias = &TableAlias{Name: NewIdent("_col_" + itoa(i))}
				retag(sq, sq.Alias)
			}
		} else if _, isAlias := selection.(*Alias); !isAlias && !isStarExpr(selection) {
			name := outputNameOf(selection)
			if name == "" {
				name = "_col_" + itoa(i)
			}
			selection = makeAlias(selection, name)
		}

		if aliasedColumn != "" {
			setOutputAlias(selection, aliasedColumn)
		}
		newSelections = append(newSelections, selection)
	}

	if len(newSelections) > 0 {
		setSelectExpressions(selectExpr, newSelections)
	}
}

// setOutputAlias mirrors selection.set("alias", exp.to_identifier(aliased_column)).
func setOutputAlias(selection Expression, name string) {
	// to_identifier uses quoted=None (auto-determined)
	switch v := selection.(type) {
	case *Alias:
		v.Alias = IdentFromName(name)
		retag(v, v.Alias)
	case *Subquery:
		v.Alias = &TableAlias{Name: IdentFromName(name)}
		retag(v, v.Alias)
	case *Table:
		v.Alias = &TableAlias{Name: IdentFromName(name)}
		retag(v, v.Alias)
	}
	// sqlg's Column has no alias field and outer_columns is empty in the corpus, so
	// this branch never triggers.
}

// --- _expand_group_by / _expand_order_by_and_distinct_on ---

func expandGroupBy(scope *Scope) {
	selectExpr, ok := scope.Expression.(*Select)
	if !ok || selectExpr.Group == nil {
		return
	}
	g := selectExpr.Group
	g.Expressions = expandPositionalReferences(scope, g.Expressions, false)
	retag(g, g.Expressions...)
}

func expandOrderByAndDistinctOn(scope *Scope, resolver *Resolver) {
	selectExpr, ok := scope.Expression.(*Select)
	if !ok {
		return
	}
	// DISTINCT ON is inexpressible with bool Distinct; only ORDER BY is handled.
	if selectExpr.Order == nil {
		return
	}
	order := selectExpr.Order

	modifierExpressions := make([]Expression, 0, len(order.Expressions))
	for _, o := range order.Expressions {
		modifierExpressions = append(modifierExpressions, o.This)
	}

	expanded := expandPositionalReferenceAliases(scope, modifierExpressions, resolver.Dialect)
	for i, original := range modifierExpressions {
		if i >= len(expanded) {
			break
		}
		for _, agg := range FindAllOf[*Func](original, true) {
			if !isAggFunc(agg) {
				continue
			}
			for _, col := range FindAllOf[*Column](agg, true) {
				if col.Table == nil {
					t := resolver.GetTable(colName(col), nil)
					if t != nil {
						col.Table = t
						retag(col, t, col.Name)
					}
				}
			}
		}
		Replace(original, expanded[i])
	}

	if selectExpr.Group != nil {
		// Python keys this dict by STRUCTURAL equality (Expression.__eq__/__hash__), not
		// pointers: ORDER BY's COUNT(*) must match the projection's COUNT(*).
		selects := map[string]Expression{}
		for _, s := range selectExpr.Expressions {
			var this Expression
			switch v := s.(type) {
			case *Alias:
				this = v.This
			case *Column:
				this = v
			}
			if this != nil {
				c := &Column{Name: NewIdent(outputNameOf(s))}
				retag(c, c.Name)
				selects[structKey(this)] = c
			}
		}
		for _, expr := range modifierExpressions {
			if lit, ok := expr.(*Literal); ok && isIntLiteral(lit) {
				Replace(expr, NewIdent(selectByPos(scope, lit).AliasName))
			} else if repl, ok := selects[structKey(expr)]; ok {
				Replace(expr, repl)
			}
		}
	}
}

// expandPositionalReferences mirrors _expand_positional_references(alias=False).
func expandPositionalReferences(scope *Scope, expressions []Expression, alias bool) []Expression {
	return expandPositional(scope, expressions, alias)
}

// expandPositionalReferenceAliases mirrors _expand_positional_references(alias=True).
func expandPositionalReferenceAliases(scope *Scope, expressions []Expression, d *Dialect) []Expression {
	return expandPositional(scope, expressions, true)
}

func expandPositional(scope *Scope, expressions []Expression, alias bool) []Expression {
	var newNodes []Expression
	for _, node := range expressions {
		lit, ok := node.(*Literal)
		if !ok || !isIntLiteral(lit) {
			newNodes = append(newNodes, node)
			continue
		}
		sel := selectByPos(scope, lit)
		if alias {
			c := &Column{Name: NewIdent(sel.AliasName)}
			retag(c, c.Name)
			newNodes = append(newNodes, c)
			continue
		}
		inner := sel.This
		// PROJECTION_ALIASES_SHADOW_SOURCE_NAMES: always False for the dialects used by structured.
		if isConstantExpr(inner) || isNumberExpr(inner) ||
			HasNode(inner, func(n Expression) bool {
				f, ok := n.(*Func)
				return ok && (strings.EqualFold(f.Name, "EXPLODE") || strings.EqualFold(f.Name, "UNNEST"))
			}) {
			newNodes = append(newNodes, node)
		} else {
			newNodes = append(newNodes, inner.Copy())
		}
	}
	return newNodes
}

// isConstantExpr mirrors isinstance(node, exp.CONSTANTS).
func isConstantExpr(e Expression) bool {
	switch e.(type) {
	case *Literal, *Null, *Boolean:
		return true
	}
	return false
}

// selectResult is the return of _select_by_pos (an exp.Alias in Python).
type selectResult struct {
	This      Expression
	AliasName string
}

// selectByPos mirrors _select_by_pos.
func selectByPos(scope *Scope, node *Literal) selectResult {
	sel, ok := scope.Expression.(*Select)
	if !ok {
		panic(&OptimizeError{Msg: "Unknown output column: " + node.Value})
	}
	pos := intLiteralValue(node)
	idx := pos - 1
	if idx < 0 || idx >= len(sel.Expressions) {
		panic(&OptimizeError{Msg: "Unknown output column: " + node.Value})
	}
	projection := sel.Expressions[idx]
	al, ok := projection.(*Alias)
	if !ok {
		// Python's assert_is(exp.Alias) raises AssertionError; reported as OptimizeError
		panic(&OptimizeError{Msg: "Unknown output column: " + node.Value})
	}
	name := ""
	if al.Alias != nil {
		name = al.Alias.Name
	}
	return selectResult{This: al.This, AliasName: name}
}

// parseIntStrict approximates Python int(text) (trim + sign); underscore separators and
// Unicode digits are not accepted (never appear in SQL literals).
func parseIntStrict(s string) (int, bool) {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0, false
	}
	v, err := strconv.Atoi(t)
	if err != nil {
		return 0, false
	}
	return v, true
}

// isIntLiteral mirrors Literal.is_int.
func isIntLiteral(l *Literal) bool {
	if l.IsString || l.Value == "" {
		return false
	}
	_, ok := parseIntStrict(l.Value)
	return ok
}

// isNumberExpr mirrors Expression.is_number.
func isNumberExpr(e Expression) bool {
	if l, ok := e.(*Literal); ok {
		return !l.IsString && l.Value != ""
	}
	return false
}

// intLiteralValue mirrors int(node.this).
func intLiteralValue(l *Literal) int {
	v, _ := parseIntStrict(l.Value)
	return v
}

// --- AliasesOrNames ---

// AliasOrNameOfExpr mirrors Expression.alias_or_name.
func AliasOrNameOfExpr(e Expression) string {
	switch v := e.(type) {
	case nil:
		return ""
	case *Table:
		return v.AliasOrName()
	case *Subquery:
		return exprAlias(v)
	case *CTE:
		return exprAlias(v)
	case *Join:
		return AliasOrNameOfExpr(v.This)
	case *Ident:
		return v.Name
	case *Alias:
		if v.Alias != nil {
			return v.Alias.Name
		}
		return ""
	case *Literal:
		return v.Value
	case *Star:
		return "*"
	case *Column:
		return colName(v)
	}
	return ""
}

// outputNameOf mirrors Expression.output_name (overloaded for sqlg's node set).
func outputNameOf(e Expression) string {
	switch v := e.(type) {
	case *Paren:
		return outputNameOf(v.This)
	case *Cast:
		return outputNameOf(v.This)
	}
	return selectName(e)
}

// --- validate_qualify_columns ---

// ValidateQualifyColumns mirrors validate_qualify_columns. sqlglot suffixes errors with
// " Line: {line}, Col: {col}"; sqlg has no position metadata, so only the message body is
// produced (compare up to the first " Line: ").
func ValidateQualifyColumns(expression Expression) {
	var allUnqualified []*Column
	for _, scope := range TraverseScope(expression) {
		if _, ok := scope.Expression.(*Select); !ok {
			continue
		}
		unqualified := scope.UnqualifiedColumns()

		if len(scope.ExternalColumns()) > 0 && !scope.IsCorrelatedSubquery() {
			column := scope.ExternalColumns()[0]
			forTable := ""
			if t := identText(column.Table); t != "" {
				forTable = " for table: '" + t + "'"
			}
			panic(&OptimizeError{Msg: "Column '" + colName(column) + "' could not be resolved" + forTable + "."})
		}

		allUnqualified = append(allUnqualified, unqualified...)
	}

	if len(allUnqualified) > 0 {
		panic(&OptimizeError{Msg: "Ambiguous column '" + colName(allUnqualified[0]) + "'"})
	}
}
