package sqlg

import "fmt"

// Port of sqlglot/optimizer/unnest_subqueries.py (subquery-in-predicate → JOIN).
// The rule's unnest() is named unnestSubqueryExpr (sqlg's unnestExpr is
// Expression.unnest()). Known divergences: no Any/All nodes and no lambda
// syntax, so those branches degrade to keeping the predicate unchanged;
// ARRAY_AGG is always used (no dialect GROUP_CONCAT); Where/Having are condition
// fields, not wrapper nodes; the `prune` arg has no semantics (matches 28.6.0).

// nameSequence replicates sqlglot.helper.NameSequence (`prefix + i`, i from 0,
// first result `_u_0`). Created once per rule entry and shared by all rewrites.
type nameSequence struct {
	prefix string
	n      int
}

func newNameSequence(prefix string) *nameSequence { return &nameSequence{prefix: prefix} }

func (s *nameSequence) next() string {
	name := fmt.Sprintf("%s%d", s.prefix, s.n)
	s.n++
	return name
}

// UnnestSubqueries corresponds to unnest_subqueries. traverse_scope yields
// child scopes before parents; the scope list is computed once before the loop,
// and rewrites happen in place.
func UnnestSubqueries(expression Expression, prune bool) Expression {
	if isNilExpr(expression) {
		return expression
	}
	nextAliasName := newNameSequence("_u_")

	for _, scope := range TraverseScope(expression) {
		selectExpr := scope.Expression
		parent := parentSelectOf(selectExpr) // select.parent_select
		if parent == nil {
			continue
		}
		if external := scope.ExternalColumns(); len(external) > 0 {
			decorrelateSubquery(selectExpr, parent, external, nextAliasName)
		} else if scope.ScopeType == ScopeSubquery {
			unnestSubqueryExpr(selectExpr, parent, nextAliasName)
		}
	}
	return expression
}

// unnestSubqueryExpr corresponds to unnest(select, parent_select, next_alias_name).
func unnestSubqueryExpr(selectExpr Expression, parentSelect *Select, next *nameSequence) {
	if len(querySelects(selectExpr)) > 1 {
		return
	}

	// The broadest exp.Condition here, not exp.Predicate.
	predicate := findAncestor(selectExpr, isConditionExpr)
	if predicate == nil || parentSelectOf(predicate) != parentSelect || parentSelect.From == nil {
		return
	}

	// SetOperation branch: rebuild `SELECT <selects> FROM (orig) AS _u_N`; the
	// Subquery must wrap a copy of the original SetOperation (copy=True in Python).
	if _, isSetOp := selectExpr.(*Union); isSetOp {
		orig := selectExpr
		rebuilt := &Select{Expressions: querySelects(orig)}
		rebuilt.From = subqueryOf(orig, next.next())
		retag(rebuilt, rebuilt.Children()...)
		selectExpr = rebuilt
	}

	alias := next.next()
	clauseKind, clauseParent := findClause(predicate)

	// ---- Non-In/Any: scalar subquery → CROSS JOIN (EXISTS → LEFT JOIN ... ON TRUE) ----
	if !isInOrAnyPredicate(predicate) {
		var column Expression = colRef(AliasOrNameOfExpr(querySelects(selectExpr)[0]), alias)

		// When the subquery projection appears in an aggregate context of the parent query,
		// wrap it in MAX to guarantee at most one row.
		if (clauseKind == clauseHaving && clauseParent == parentSelect) ||
			((clauseKind == clauseNone || clauseParent != parentSelect) &&
				(parentSelect.Group != nil || parentHasAggProjection(parentSelect))) {
			m := &Func{Name: "MAX", Args: []Expression{column}}
			retag(m, m.Children()...)
			column = m
		} else if _, ok := selectExpr.Parent().(*Subquery); !ok {
			// Only `(SELECT ...)` shapes are handled; others are abandoned.
			return
		}

		joinType := "CROSS"
		var onClause Expression
		if _, isExists := predicate.(*Exists); isExists {
			// CROSS JOIN would drop all parent rows when the subquery is empty,
			// so EXISTS becomes LEFT JOIN ON TRUE + null check. NOT EXISTS is not
			// distinguished here (Not wraps outside Exists).
			column = notExpr(isNullExpr(column))
			joinType = "LEFT"
			onClause = trueExpr()
		}

		replaceWithCondition(selectExpr.Parent(), column)
		selectJoin(parentSelect, selectExpr, onClause, joinType, alias)
		return
	}

	// ---- In/Any branch ----
	if sel, ok := selectExpr.(*Select); ok && (sel.Limit != nil || sel.Offset != nil) {
		return
	}
	// Python's exp.Any find_ancestor(EQ) branch is omitted: sqlg has no Any node.

	var column Expression = otherOperandOf(predicate)
	value := querySelects(selectExpr)[0]

	joinKey := colRef(aliasOf(value), alias)
	// is_ copies the left operand (_binop), so joinKeyNotNull holds a copy.
	joinKeyNotNull := notExpr(isNullExpr(treeCopy(joinKey)))

	// Inside a Join the predicate must not become the null check (it would break
	// join semantics): set it to TRUE and move the check to the parent WHERE.
	if clauseKind == clauseJoin {
		replaceWithCondition(predicate, trueExpr())
		selectWhereAppend(parentSelect, joinKeyNotNull)
	} else {
		replaceWithCondition(predicate, joinKeyNotNull)
	}

	sel, _ := selectExpr.(*Select)
	group := (*Group)(nil)
	if sel != nil {
		group = sel.Group
	}
	valueThis := unaliasExpr(value)
	if group != nil {
		if !isSingletonSet(group.Expressions, valueThis) {
			selectExpr = rebuildGroupedSubquery(selectExpr, aliasOf(value))
			sel, _ = selectExpr.(*Select)
		}
	} else if len(findAllInScope(valueThis, true, isAggFuncNode)) == 0 {
		// The value is neither an aggregate nor in the group by; the subquery must be
		// grouped by the value.
		selectGroupAppend(sel, valueThis)
	}

	selectJoin(parentSelect, selectExpr, eqExpr(treeCopy(column), joinKey), "LEFT", alias)
}

// decorrelateSubquery corresponds to decorrelate(): rewrites a correlated
// subquery into group-by-correlation-keys + LEFT JOIN (no many-to-many blowup).
func decorrelateSubquery(selectExpr Expression, parentSelect *Select, externalColumns []*Column, next *nameSequence) {
	sel, ok := selectExpr.(*Select)
	if !ok {
		// `select.args.get("where")` is always None for SetOperation/unionTail,
		// so Python would return immediately at the first line below.
		return
	}
	where := sel.Where
	if where == nil || hasOrNode(where) || sel.Limit != nil || sel.Offset != nil {
		return
	}

	tableAlias := next.next()
	keys := make([]decorrelateKey, 0, len(externalColumns))

	// Find the predicate each external column lives in, and take the "other side" as the
	// correlation key.
	for _, column := range externalColumns {
		// The external column must sit directly in this subquery's WHERE.
		if whereOwnerOf(column) != where {
			return
		}
		predicate := findAncestor(column, isStrictPredicate)
		if predicate == nil || whereOwnerOf(predicate) != where {
			return
		}
		b, isBinary := predicate.(*Binary)
		if !isBinary {
			return
		}
		// walk() includes the left operand itself; comparison is by pointer identity.
		key := b.Left
		if subtreeContainsIdentical(b.Left, column) {
			key = b.Right
		}
		keys = append(keys, decorrelateKey{key: key, column: column, predicate: predicate, keyText: structKey(key)})
	}

	hasEQ := false
	for _, it := range keys {
		if isEQExpr(it.predicate) {
			hasEQ = true
			break
		}
	}
	if !hasEQ {
		return
	}

	// When the subquery as a whole is one of the parent query's projections
	// (`SELECT (SELECT ...) FROM ...`), collect values with MAX instead of ARRAY_AGG.
	subqueryNode := sel.Parent()
	isSubqueryProjection := false
	for _, proj := range querySelects(parentSelect) {
		if sq, isSq := unaliasExpr(proj).(*Subquery); isSq && Expression(sq) == subqueryNode {
			isSubqueryProjection = true
			break
		}
	}

	value := querySelects(sel)[0]
	valueThis := unaliasExpr(value)
	valueAlias := aliasOf(value)

	// Python uses a dict to record key → alias, where insertion order is traversal order;
	// here a slice is used to manually maintain the same order.
	keyAliases := make([]keyAliasEntry, 0, len(keys))
	groupBy := make([]Expression, 0, len(keys))
	groupTexts := map[string]bool{}

	indexOfAlias := func(key Expression, text string) int {
		for i := range keyAliases {
			if keyAliases[i].keyText == text {
				return i
			}
		}
		return -1
	}
	inGroupBy := func(text string) bool { return groupTexts[text] }

	for _, it := range keys {
		if it.keyText == structKey(valueThis) {
			// The filter condition directly uses the subquery's projected value → that
			// value must be unique.
			keyAliases = append(keyAliases, keyAliasEntry{it.key, it.keyText, valueAlias})
			groupBy = append(groupBy, it.key)
			groupTexts[it.keyText] = true
			continue
		}
		if indexOfAlias(it.key, it.keyText) < 0 {
			keyAliases = append(keyAliases, keyAliasEntry{it.key, it.keyText, next.next()})
		}
		// Equality correlation keys must also enter the group by, otherwise we get a
		// many-to-many blowup.
		if isEQExpr(it.predicate) && !inGroupBy(it.keyText) {
			groupBy = append(groupBy, it.key)
			groupTexts[it.keyText] = true
		}
	}

	parentPredicate := findAncestor(sel, isStrictPredicate)

	// When the value is neither an aggregate nor a correlation key, it must be collected
	// into an array/MAX before we can group by the keys.
	aggFuncName := "ARRAY_AGG"
	if isSubqueryProjection {
		aggFuncName = "MAX"
	}
	valueIsAgg := HasNode(valueThis, isAggFuncNode)
	if !valueIsAgg && !inGroupBy(structKey(valueThis)) {
		agg := &Func{Name: aggFuncName, Args: []Expression{treeCopy(valueThis)}}
		retag(agg, agg.Children()...)
		a := &Alias{This: agg, Alias: NewIdent(valueAlias)}
		retag(a, a.Children()...)
		// append=False: replace the projections wholesale
		selectSetProjections(sel, []Expression{a})
	}

	// EXISTS only cares about whether rows exist; clear all projections — only the
	// correlation keys are added back below.
	if _, isExists := parentPredicate.(*Exists); isExists {
		sel.Expressions = nil
		retag(sel, sel.Children()...)
	}

	for _, ka := range keyAliases {
		if inGroupBy(ka.keyText) {
			if _, isExists := parentPredicate.(*Exists); isExists || ka.keyText != structKey(valueThis) {
				// f"{key} AS {alias}": render with the **default dialect**, then re-parse
				// (copy=False).
				sql := fmt.Sprintf("%s AS %s", ka.key.SQL(GetDialect("")), ka.alias)
				selectAddProjection(sel, parseFragment(sql))
			}
		} else {
			agg := &Func{Name: aggFuncName, Args: []Expression{treeCopy(ka.key)}}
			retag(agg, agg.Children()...)
			a := &Alias{This: agg, Alias: NewIdent(ka.alias)}
			retag(a, a.Children()...)
			selectAddProjection(sel, a)
		}
	}

	var alias Expression = colRef(valueAlias, tableAlias)
	other := otherOperandOf(parentPredicate)

	switch parentPredicate.(type) {
	case *Exists:
		// list(key_aliases.values())[0]: the alias of the **first** key
		firstAlias := ""
		if len(keyAliases) > 0 {
			firstAlias = keyAliases[0].alias
		}
		alias = colRef(firstAlias, tableAlias)
		parentPredicate = replaceWithParsed(parentPredicate,
			fmt.Sprintf("NOT %s IS NULL", sqlOf(alias)))
	case *In:
		if inGroupBy(structKey(valueThis)) {
			parentPredicate = replaceWithParsed(parentPredicate,
				fmt.Sprintf("%s = %s", sqlOf(other), sqlOf(alias)))
		} else {
			// ARRAY_ANY lambda fragment: unparseable in sqlg → predicate kept
			// as-is (see replaceWithParsed).
			parentPredicate = replaceWithParsed(parentPredicate,
				fmt.Sprintf("ARRAY_ANY(%s, _x -> _x = %s)", sqlOf(alias), sqlOf(other)))
		}
	default:
		// exp.All / exp.Any branches merged here: sqlg has neither node.
		if isSubqueryProjection && subqueryNode != nil {
			if sq, isSq := subqueryNode.(*Subquery); isSq && exprAlias(sq) != "" {
				a := &Alias{This: alias, Alias: NewIdent(exprAlias(sq))}
				retag(a, a.Children()...)
				alias = a
			}
		}

		// COUNT yields 0 on an empty set, so remove_aggs maps COUNT → 0 and other
		// aggregates → NULL, backstopped by COALESCE.
		if HasNode(value, isCountFunc) {
			coalesced := &Func{Name: "COALESCE", Args: []Expression{
				alias,
				Transform(valueThis, removeAggFuncs, true),
			}}
			retag(coalesced, coalesced.Children()...)
			alias = coalesced
		}

		// select.parent.replace(alias)
		if subqueryNode != nil {
			Replace(subqueryNode, alias)
		}
	}

	for _, it := range keys {
		Replace(it.predicate, trueExpr())
		nested := colRef(aliasOfKeyAlias(keyAliases, it.keyText), tableAlias)

		if isSubqueryProjection {
			Replace(it.key, nested)
			if !isEQExpr(it.predicate) {
				selectWhereAppend(parentSelect, it.predicate)
			}
			continue
		}

		if inGroupBy(it.keyText) {
			Replace(it.key, nested)
		} else if isEQExpr(it.predicate) {
			sql := fmt.Sprintf("(%s AND ARRAY_CONTAINS(%s, %s))",
				sqlOf(parentPredicate), sqlOf(nested), sqlOf(it.column))
			parentPredicate = replaceWithParsed(parentPredicate, sql)
		} else {
			Replace(it.key, &Ident{Name: "_x"})
			sql := fmt.Sprintf("(%s AND ARRAY_ANY(%s, _x -> %s))",
				sqlOf(parentPredicate), sqlOf(nested), sqlOf(it.predicate))
			parentPredicate = replaceWithParsed(parentPredicate, sql)
		}
	}

	joined := selectExpr
	if len(groupBy) > 0 {
		if js, isSel := selectExpr.(*Select); isSel {
			if js.Group == nil {
				js.Group = &Group{Expressions: groupBy}
			} else {
				js.Group.Expressions = append(js.Group.Expressions, groupBy...)
			}
			retag(js, js.Children()...)
		}
	}

	// on=[EQ predicates]; an empty list is falsy in Python → no ON clause (nil here).
	onList := make([]Expression, 0, len(keys))
	for _, it := range keys {
		if isEQExpr(it.predicate) {
			onList = append(onList, it.predicate)
		}
	}
	var onClause Expression
	if len(onList) > 0 {
		onClause = combineWith(onList, "AND")
	}
	selectJoin(parentSelect, joined, onClause, "LEFT", tableAlias)
}

// decorrelateKey is the (key, column, predicate) triple in decorrelate's `keys`;
// keyText is the precomputed structural key (Python uses hash+eq semantics).
type decorrelateKey struct {
	key       Expression
	column    *Column
	predicate Expression
	keyText   string
}

// keyAliasEntry corresponds to an entry of decorrelate's `key_aliases` dict (insertion
// order preserved).
type keyAliasEntry struct {
	key     Expression
	keyText string
	alias   string
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

// parentSelectOf corresponds to `select.parent_select` (find_ancestor(Select)
// starting from the parent). sqlg's extra/missing CTE wrapper layers don't
// change the result — none of them are Select.
func parentSelectOf(e Expression) *Select {
	if isNilExpr(e) {
		return nil
	}
	s, _ := findAncestorOf[*Select](e)
	return s
}

// isConditionExpr corresponds to isinstance(e, exp.Condition) — much broader
// than Predicate; Identifier/Table/Alias/Subquery/Query/Join/Group/Limit/
// Interval/When are not Conditions.
func isConditionExpr(e Expression) bool {
	switch e.(type) {
	case *Column, *Literal, *Null, *Boolean, *Paren, *Unary, *Binary, *Is, *In,
		*Exists, *Between, *Case, *Cast, *Func, *HexString, *Bracket, *TypedLiteral:
		return true
	}
	return false
}

// predicateBinaryOps whitelists the comparison-style operators that are
// exp.Predicate in sqlglot. Arithmetic is a Condition but NOT a Predicate;
// sqlg unifies both into *Binary, so the whitelist keeps `a + b` out.
var predicateBinaryOps = map[string]bool{
	"=": true, "<>": true, "!=": true, "<": true, "<=": true, ">": true, ">=": true,
	"<=>": true, "LIKE": true, "ILIKE": true, "RLIKE": true, "REGEXP": true,
	"GLOB": true, "MATCH": true, "SIMILAR TO": true,
	"IS DISTINCT FROM": true, "IS NOT DISTINCT FROM": true,
}

// isStrictPredicate corresponds to isinstance(e, exp.Predicate).
func isStrictPredicate(e Expression) bool {
	switch v := e.(type) {
	case *Binary:
		return predicateBinaryOps[v.Op]
	case *In, *Between, *Is, *Exists:
		return true
	}
	return false
}

// isInOrAnyPredicate corresponds to isinstance(predicate, (exp.In, exp.Any)).
// sqlg has no Any node, so the only possibility is In.
func isInOrAnyPredicate(e Expression) bool {
	_, ok := e.(*In)
	return ok
}

// isCountFunc corresponds to isinstance(e, exp.Count).
func isCountFunc(e Expression) bool {
	f, ok := e.(*Func)
	return ok && (f.Name == "COUNT" || f.Name == "count")
}

// querySelects corresponds to Query.selects / SetOperation.selects /
// DerivedTable.selects (SetOperation unnests left; Subquery takes `this`'s
// selects when it is a Query). Shared with annotate_types.
func querySelects(e Expression) []Expression {
	for !isNilExpr(e) {
		switch v := e.(type) {
		case *Select:
			return selectsOf(v)
		case *Union:
			e = unnestExpr(v.Left)
		case *unionTail:
			e = unnestExpr(v.Union)
		case *Subquery:
			e = unnestExpr(v.This)
		default:
			return nil
		}
	}
	return nil
}

// aliasOf corresponds to `Expression.alias` on projections; extends exprAlias
// with *Alias (Alias → name, else empty), matching sqlglot's alias property.
func aliasOf(e Expression) string {
	if a, ok := e.(*Alias); ok {
		if a.Alias != nil {
			return a.Alias.Name
		}
		return ""
	}
	return exprAlias(e)
}

// aliasOfKeyAlias returns the alias for key_aliases[key] (the key is located via its
// structural key to avoid repeated rendering).
func aliasOfKeyAlias(keyAliases []keyAliasEntry, keyText string) string {
	for i := range keyAliases {
		if keyAliases[i].keyText == keyText {
			return keyAliases[i].alias
		}
	}
	return ""
}

// whereOwnerOf corresponds to `column.find_ancestor(exp.Where)`; since sqlg has
// no Where wrapper node, it returns the condition node itself as the identity
// (callers compare against `select.Where` by pointer).
func whereOwnerOf(e Expression) Expression {
	child := e
	for p := e.Parent(); p != nil; p = p.Parent() {
		switch v := p.(type) {
		case *Select:
			if v.Where != nil && v.Where == child {
				return child
			}
		case *Update:
			if v.Where != nil && v.Where == child {
				return child
			}
		case *Delete:
			if v.Where != nil && v.Where == child {
				return child
			}
		case *WhereClause: // The Where inside FILTER(WHERE ...); likewise not this subquery's WHERE
			return child
		}
		child = p
	}
	return nil
}

// clauseKind marks which kind of clause find_ancestor(Having, Where, Join) hit.
type clauseKind int

const (
	clauseNone clauseKind = iota
	clauseHaving
	clauseWhere
	clauseJoin
)

// findClause corresponds to `predicate.find_ancestor(exp.Having, exp.Where, exp.Join)`;
// returns (kind, clause's parent Select). Search starts at predicate.Parent().
func findClause(predicate Expression) (clauseKind, *Select) {
	child := predicate
	for p := predicate.Parent(); p != nil; p = p.Parent() {
		switch v := p.(type) {
		case *Select:
			if v.Having != nil && v.Having == child {
				return clauseHaving, v
			}
			if v.Where != nil && v.Where == child {
				return clauseWhere, v
			}
		case *Join:
			return clauseJoin, parentSelectOf(v)
		}
		child = p
	}
	return clauseNone, nil
}

// parentHasAggProjection corresponds to
// `any(find_in_scope(s, exp.AggFunc) for s in parent_select.selects)`.
func parentHasAggProjection(parentSelect *Select) bool {
	for _, proj := range querySelects(parentSelect) {
		if len(findAllInScope(proj, true, isAggFuncNode)) > 0 {
			return true
		}
	}
	return false
}

// hasOrNode corresponds to `where.find(exp.Or)` (BFS search for OR).
func hasOrNode(e Expression) bool {
	return HasNode(e, func(n Expression) bool {
		b, ok := n.(*Binary)
		return ok && b.Op == "OR"
	})
}

// subtreeContainsIdentical corresponds to `any(node is column for node in root.walk())`:
// BFS including the root; pointer identity.
func subtreeContainsIdentical(root, target Expression) bool {
	for _, n := range WalkBFS(root, nil) {
		if n == target {
			return true
		}
	}
	return false
}

// isSingletonSet corresponds to the set-equality check in `{value.this} != set(group.expressions)`:
// the deduplicated group.expressions must equal exactly {value.this} (structural equality).
func isSingletonSet(expressions []Expression, single Expression) bool {
	seen := map[string]bool{}
	for _, e := range expressions {
		seen[structKey(e)] = true
	}
	return len(seen) == 1 && seen[structKey(single)]
}

// otherOperandOf corresponds to _other_operand(expression).
func otherOperandOf(e Expression) Expression {
	switch v := e.(type) {
	case *In:
		return v.This
	case *Binary:
		switch v.Left.(type) {
		case *Subquery, *Exists:
			return v.Right
		}
		// sqlglot also has exp.Any / exp.All here; sqlg has no corresponding nodes
		return v.Left
	}
	return nil
}

// replaceWithCondition corresponds to `_replace(expression, condition)`.
// exp.condition() does not copy an existing Expression, so neither do we.
func replaceWithCondition(expression, condition Expression) Expression {
	if isNilExpr(expression) {
		return condition
	}
	return Replace(expression, condition)
}

// treeCopy is the single entry point for "copy, then rebuild parent pointers with
// RefreshParents": sqlg's Copy() does not carry over parent, whereas sqlglot's copy
// maintains that invariant.
func treeCopy(e Expression) Expression { return RefreshParents(e.Copy()) }

// subqueryOf corresponds to `Expression.subquery(alias=None, copy=True)`:
// copy (by default), then wrap in a Subquery; attach a TableAlias when alias is non-empty.
func subqueryOf(e Expression, alias string) *Subquery {
	sq := &Subquery{This: treeCopy(e)}
	if alias != "" {
		sq.Alias = &TableAlias{Name: NewIdent(alias)}
	}
	retag(sq, sq.Children()...)
	return sq
}

// colRef corresponds to `exp.column(name, table)` (an unqualified column when table is
// empty).
func colRef(name, table string) *Column {
	c := &Column{Name: NewIdent(name)}
	if table != "" {
		c.Table = NewIdent(table)
	}
	retag(c, c.Children()...)
	return c
}

// notExpr corresponds to `x.not_()` (in sqlg, NOT is *Unary{Op:"NOT"}).
func notExpr(e Expression) Expression {
	n := &Unary{Op: "NOT", This: e}
	retag(n, n.Children()...)
	return n
}

// isNullExpr corresponds to `x.is_(exp.null())`. Note that `x IS NULL` and `NOT x IS NULL`
// (not_ wrapping it in a Unary) are two different spellings with different rendered output;
// do not merge them into a single IS NOT NULL.
func isNullExpr(e Expression) Expression {
	i := &Is{This: e, Expr: &Null{}}
	retag(i, i.Children()...)
	return i
}

// eqExpr corresponds to `a.eq(b)` = `EQ(this=a.copy(), expression=b)`:
// the left operand is copied, the right operand is used as-is (consistent with sqlglot's
// _binop).
func eqExpr(left, right Expression) Expression {
	b := &Binary{Op: "=", Left: left, Right: right}
	retag(b, b.Children()...)
	return b
}

// sqlOf corresponds to `Expression.__str__` (sql() with the default dialect);
// must match the dialect used to re-parse assembled fragments.
func sqlOf(e Expression) string {
	if isNilExpr(e) {
		return ""
	}
	return e.SQL(GetDialect(""))
}

// parseFragment corresponds to `maybe_parse(...)`: parses a SINGLE expression
// (not a whole statement) with the default dialect, e.g. `x AS _u_1`.
// Returns nil on parse failure; the caller decides how to degrade.
func parseFragment(sql string) Expression {
	toks, err := LexDialect(sql, "")
	if err != nil || len(toks) == 0 {
		return nil
	}
	p := &Parser{toks: toks, sql: sql, name: ""}
	if p.cur().Type == TokEOF {
		return nil
	}
	e, err := p.parseSelectExpr()
	if err != nil || isNilExpr(e) {
		return nil
	}
	return RefreshParents(e)
}

// selectJoin corresponds to
// `Select.join(expression, on=..., join_type=..., join_alias=..., append=True, copy=False)`.
// A Select join target is copied into a Subquery; only "CROSS"/"LEFT" join
// types are supported; a nil on means no ON clause; join_alias attaches a TableAlias.
func selectJoin(parentSelect *Select, expression Expression, on Expression, joinType, joinAlias string) {
	if parentSelect == nil || isNilExpr(expression) {
		return
	}
	jn := &Join{This: expression}
	if inner, isSel := jn.This.(*Select); isSel {
		jn.This = subqueryOf(inner, "")
	}
	switch joinType {
	case "CROSS":
		jn.Kind = "CROSS"
	case "LEFT":
		jn.Side = "LEFT"
	}
	if !isNilExpr(on) {
		jn.On = combineWith([]Expression{on}, "AND")
	}
	if joinAlias != "" {
		switch v := jn.This.(type) {
		case *Subquery:
			v.Alias = &TableAlias{Name: NewIdent(joinAlias)}
		case *Table:
			v.Alias = &TableAlias{Name: NewIdent(joinAlias)}
		}
		// Aliases on other node shapes are silently dropped, matching sqlglot
		// (set("alias", ...) writes args the generator never renders).
	}
	// retag the Join itself: without it Join.This keeps parent==nil and later
	// scope-based rules (eliminate_subqueries) cannot find/replace the subquery.
	retag(jn, jn.Children()...)
	parentSelect.Joins = append(parentSelect.Joins, jn)
	retag(parentSelect, parentSelect.Children()...)
}

// selectAddProjection corresponds to `Select.select(expr, append=True, copy=False)`.
func selectAddProjection(sel *Select, e Expression) {
	if sel == nil || isNilExpr(e) {
		return
	}
	sel.Expressions = append(sel.Expressions, e)
	retag(sel, sel.Children()...)
}

// selectSetProjections corresponds to `Select.select(expr, append=False, copy=False)`
// (replacing the projection list wholesale).
func selectSetProjections(sel *Select, list []Expression) {
	if sel == nil {
		return
	}
	sel.Expressions = list
	retag(sel, sel.Children()...)
}

// selectGroupAppend corresponds to `select.group_by(*group_by, copy=False)`.
func selectGroupAppend(sel *Select, list ...Expression) {
	if sel == nil || len(list) == 0 {
		return
	}
	if sel.Group == nil {
		sel.Group = &Group{Expressions: list}
	} else {
		sel.Group.Expressions = append(sel.Group.Expressions, list...)
	}
	retag(sel, sel.Children()...)
}

// selectWhereAppend corresponds to `Select.where(cond, copy=False)`: ANDs with
// the existing condition, wrapping connector operands in parens.
func selectWhereAppend(sel *Select, cond Expression) {
	if sel == nil || isNilExpr(cond) {
		return
	}
	if sel.Where == nil {
		sel.Where = cond
	} else {
		sel.Where = combineWith([]Expression{sel.Where, cond}, "AND")
	}
	retag(sel, sel.Children()...)
}

// rebuildGroupedSubquery wraps the subquery as
// `SELECT _q.<v> AS <v> FROM (subquery) AS _q GROUP BY _q.<v>` when its GROUP BY
// keys don't line up with the correlation keys. The inner node is attached
// directly (copy=False), not copied.
func rebuildGroupedSubquery(selectExpr Expression, valueAlias string) Expression {
	proj := &Alias{This: colRef(valueAlias, "_q"), Alias: NewIdent(valueAlias)}
	retag(proj, proj.Children()...)

	sq := &Subquery{This: selectExpr, Alias: &TableAlias{Name: NewIdent("_q")}}
	retag(sq, sq.Children()...)

	rebuilt := &Select{
		Expressions: []Expression{proj},
		From:        sq,
		Group:       &Group{Expressions: []Expression{colRef(valueAlias, "_q")}},
	}
	retag(rebuilt, rebuilt.Children()...)
	return rebuilt
}

// removeAggFuncs corresponds to the remove_aggs transform in decorrelate:
// COUNT → literal 0, other aggregates → NULL, all other nodes returned unchanged.
func removeAggFuncs(node Expression) Expression {
	if isCountFunc(node) {
		return &Literal{Value: "0", IsString: false}
	}
	if isAggFuncNode(node) {
		return &Null{}
	}
	return node
}

// replaceWithParsed corresponds to `_replace(x, f"...")`: parse the assembled
// fragment with the default dialect and replace x. Known divergence: lambda
// syntax fails to parse in sqlg, and the rewrite is then abandoned (x kept as-is).
func replaceWithParsed(original Expression, sql string) Expression {
	parsed := parseFragment(sql)
	if parsed == nil {
		return original
	}
	return replaceWithCondition(original, parsed)
}

// ---------------------------------------------------------------------------
// Type-assertion helpers (kept in the same style as the asXxx helpers in walk.go)
// ---------------------------------------------------------------------------
