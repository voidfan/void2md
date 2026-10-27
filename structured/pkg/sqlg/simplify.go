package sqlg

import (
	"math/big"
	"sort"
	"strconv"
	"strings"
)

// Port of sqlglot/optimizer/simplify.py plus its minimal Gen generator.
// Differences: class identity via nodeClassKey, meta via exprMetaStore,
// equality via structKey, parents set explicitly; negative literals fold the
// minus sign (Literal{"-1"}); node kinds sqlg never produces are stubbed out.

// exprMetaStore replicates sqlglot's `Expression.meta` (annotate_types writes
// "nonnull"; simplify only reads). Cleared at each rule entry to avoid leaks.
var exprMetaStore = map[Expression]map[string]any{}

// exprMetaGet corresponds to `expression.meta.get(key)`.
func exprMetaGet(e Expression, key string) any {
	if isNilExpr(e) {
		return nil
	}
	if m, ok := exprMetaStore[e]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	return nil
}

// exprMetaSet corresponds to `expression.meta[key] = value` (used by the type annotator).
func exprMetaSet(e Expression, key string, value any) {
	if isNilExpr(e) {
		return
	}
	m, ok := exprMetaStore[e]
	if !ok {
		m = map[string]any{}
		exprMetaStore[e] = m
	}
	m[key] = value
}

// resetExprMeta clears meta at each top-level rule entry (each tree serves only one call).
func resetExprMeta() { exprMetaStore = map[Expression]map[string]any{} }

// ---- sqlglot's constant sets ----

// DataType name sets mirroring sqlglot's Type enums (sqlg keeps only names).
var (
	dtSignedInteger = map[string]bool{
		"BIGINT": true, "INT": true, "INT128": true, "INT256": true,
		"MEDIUMINT": true, "SMALLINT": true, "TINYINT": true,
	}
	dtUnsignedInteger = map[string]bool{
		"UBIGINT": true, "UINT": true, "UINT128": true, "UINT256": true,
		"UMEDIUMINT": true, "USMALLINT": true, "UTINYINT": true,
	}
	dtTemporal = map[string]bool{
		"DATE": true, "DATE32": true, "DATETIME": true, "DATETIME2": true,
		"DATETIME64": true, "SMALLDATETIME": true, "TIME": true,
		"TIMESTAMP": true, "TIMESTAMPNTZ": true, "TIMESTAMPLTZ": true,
		"TIMESTAMPTZ": true, "TIMESTAMP_MS": true, "TIMESTAMP_NS": true,
		"TIMESTAMP_S": true, "TIMETZ": true,
	}
)

// ---- Node classification: map sqlg nodes back to sqlglot's "class-identity key" ----

// nodeClassKey maps a node to its sqlglot class-identity key (Expression.key).
// Each *Binary Op must map individually so flatten/uniq_sort/same_parent can
// distinguish operators sharing the *Binary type.
func nodeClassKey(e Expression) string {
	switch n := e.(type) {
	case *Binary:
		switch n.Op {
		case "=":
			return "eq"
		case "<>", "!=":
			return "neq"
		case ">":
			return "gt"
		case ">=":
			return "gte"
		case "<":
			return "lt"
		case "<=":
			return "lte"
		case "+":
			return "add"
		case "-":
			return "sub"
		case "*":
			return "mul"
		case "/":
			return "div"
		case "%":
			return "mod"
		case "AND":
			return "and"
		case "OR":
			return "or"
		case "XOR":
			return "xor"
		case "LIKE":
			return "like"
		case "ILIKE":
			return "ilike"
		case "DIV":
			return "intdiv"
		case "||":
			return "dpipe"
		case ".":
			return "dot"
		case "<=>":
			return "nullsafeeq"
		case "IS DISTINCT FROM":
			return "nullsafeneq"
		case "&":
			return "bitwiseand"
		case "|":
			return "bitwiseor"
		case "^":
			return "bitwisexor"
		}
		return "binary"
	case *Unary:
		switch n.Op {
		case "NOT":
			return "not"
		case "-":
			return "neg"
		case "+":
			return "positive"
		}
		return "unary"
	case *Paren:
		return "paren"
	case *Is:
		return "is"
	case *In:
		return "in"
	case *Between:
		return "between"
	case *Exists:
		return "exists"
	case *Case:
		return "case"
	case *When:
		return "when"
	case *Cast:
		return "cast"
	case *DataType:
		// Needed so Gen dispatches datatype_sql instead of the fallback.
		return "datatype"
	case *Var2:
		// Placeholder for exp.Var (Interval unit).
		return "var"
	case *From2:
		// exp.From (kept for Gen's from_sql).
		return "from"
	case *TypedLiteral:
		// sqlglot parses `DATE '2024-01-01'` as Cast(Literal, DataType).
		return "cast"
	case *Column:
		return "column"
	case *Literal:
		return "literal"
	case *Null:
		return "null"
	case *Boolean:
		return "boolean"
	case *Func:
		return funcClassKey(n.Name)
	case *Subquery:
		return "subquery"
	case *Table:
		return "table"
	case *TableAlias:
		return "tablealias"
	case *Ident:
		return "identifier"
	case *Alias:
		return "alias"
	case *Star:
		return "star"
	case *Bracket:
		return "bracket"
	case *Interval:
		return "interval"
	case *Distinct:
		return "distinct"
	case *Select:
		return "select"
	case *Union:
		return "union"
	case *unionTail:
		return "uniontail"
	case *Group:
		return "group"
	case *Order:
		return "order"
	case *Ordered:
		return "ordered"
	case *Limit:
		return "limit"
	case *Offset:
		return "offset"
	case *Join:
		return "join"
	case *With:
		return "with"
	case *CTE:
		return "cte"
	case *WhereClause:
		return "where"
	case *Schema:
		return "schema"
	case *ColumnDef:
		return "columndef"
	}
	return "expression"
}

// funcClassKey maps sqlg's *Func (by function name) to the concrete sqlglot Func subclass key.
// Unrecognized names correspond to sqlglot's exp.Anonymous (key = "anonymous").
func funcClassKey(name string) string {
	switch strings.ToUpper(name) {
	case "COALESCE", "IFNULL", "NVL":
		return "coalesce"
	case "CONCAT":
		return "concat"
	case "CONCAT_WS":
		return "concatws"
	case "DATE_TRUNC":
		return "datetrunc"
	case "TIMESTAMP_TRUNC":
		return "timestamptrunc"
	case "STARTS_WITH", "STARTSWITH":
		return "startswith"
	case "IF", "IIF":
		return "if"
	case "DATE_ADD":
		return "dateadd"
	case "DATE_SUB":
		return "datesub"
	case "DATETIME_ADD":
		return "datetimeadd"
	case "DATETIME_SUB":
		return "datetimesub"
	case "TS_OR_DS_TO_DATE":
		return "tsordstodate"
	case "RAND":
		return "rand"
	case "RANDN":
		return "randn"
	}
	return "anonymous"
}

// Connector (sqlglot's exp.Connector = And/Or/Xor).
func isConnectorNode(e Expression) bool {
	switch nodeClassKey(e) {
	case "and", "or", "xor":
		return true
	}
	return false
}

// Comparison (the key set of sqlglot's COMPLEMENT_COMPARISONS).
func isComparisonNode(e Expression) bool {
	switch nodeClassKey(e) {
	case "eq", "neq", "gt", "gte", "lt", "lte":
		return true
	}
	return false
}

// Corresponds to isinstance(e, exp.Binary): in sqlglot Is / NullSafeEQ / NullSafeNEQ are also Binary.
func isBinaryClassNode(e Expression) bool {
	switch e.(type) {
	case *Binary, *Is:
		return true
	}
	return false
}

// Corresponds to isinstance(e, exp.Predicate). Unlike ruleutil's
// isPredicateExpr, arithmetic operators are NOT predicates (simplify_parens
// relies on this).
func isPredicateNodeOf(e Expression) bool {
	switch n := e.(type) {
	case *Binary:
		switch n.Op {
		case "=", "<>", "!=", ">", ">=", "<", "<=", "<=>", "LIKE", "ILIKE",
			"IS DISTINCT FROM":
			return true
		}
		return false
	case *In, *Between, *Is, *Exists:
		return true
	}
	return false
}

// Corresponds to isinstance(e, exp.Condition). Excludes Query/Table/
// Identifier/Alias/Interval.
func isConditionNode(e Expression) bool {
	switch e.(type) {
	case *Binary, *Unary, *Func, *Case, *Cast, *In, *Between, *Is, *Exists,
		*Column, *Literal, *Null, *Boolean, *TypedLiteral, *Bracket:
		return true
	}
	return false
}

// Corresponds to isinstance(e, exp.Query).
func isQueryNode(e Expression) bool { return IsQueryExpr(e) }

// sameParent corresponds to `expression.same_parent`: the parent node is the same class as the node itself.
func sameParent(e Expression) bool {
	p := e.Parent()
	if p == nil {
		return false
	}
	return nodeClassKey(p) == nodeClassKey(e)
}

// ---- Dialect switches (sqlg's Dialect struct has no such fields; recovered by name) ----

// safeToEliminateDoubleNegation corresponds to dialect.SAFE_TO_ELIMINATE_DOUBLE_NEGATION.
// MySQL / SQLite and their derived dialects (StarRocks, Doris) are False; the rest are True.
func safeToEliminateDoubleNegation(d *Dialect) bool {
	if d == nil {
		return true
	}
	switch d.Name {
	case "mysql", "sqlite", "starrocks", "doris", "singlestore", "maria", "tidb":
		return false
	}
	return true
}

// requiresParenthesizedStructAccess corresponds to dialect.REQUIRES_PARENTHESIZED_STRUCT_ACCESS.
func requiresParenthesizedStructAccess(d *Dialect) bool {
	return d != nil && d.Name == "risingwave"
}

// coalesceComparisonNonStandard corresponds to dialect.COALESCE_COMPARISON_NON_STANDARD.
func coalesceComparisonNonStandard(d *Dialect) bool {
	return d != nil && d.Name == "redshift"
}

// weekOffset corresponds to dialect.WEEK_OFFSET (bigquery is -1, the rest are 0).
func weekOffset(d *Dialect) int {
	if d != nil && d.Name == "bigquery" {
		return -1
	}
	return 0
}

// ---- Top-level API ----

// Simplify corresponds to the module-level `simplify(expression, constant_propagation, coalesce_simplification, dialect)`.
func Simplify(
	expression Expression, dialect *Dialect,
	constantPropagation bool, coalesceSimplification bool,
) Expression {
	return NewSimplifier(dialect, true).Simplify(expression, constantPropagation, coalesceSimplification)
}

// Simplifier corresponds to sqlglot.optimizer.simplify.Simplifier.
type Simplifier struct {
	dialect                *Dialect
	annotateNewExpressions bool
	annotator              *TypeAnnotator
}

// NewSimplifier corresponds to `Simplifier(dialect=..., annotate_new_expressions=...)`.
func NewSimplifier(dialect *Dialect, annotateNewExpressions bool) *Simplifier {
	d := dialect
	if d == nil {
		d = GetDialect("")
	}
	return &Simplifier{
		dialect:                d,
		annotateNewExpressions: annotateNewExpressions,
		annotator:              newSimplifierAnnotator(d),
	}
}

// newSimplifierAnnotator corresponds to
// `TypeAnnotator(schema=ensure_schema(None, dialect=self.dialect), overwrite_types=False)`.
func newSimplifierAnnotator(d *Dialect) *TypeAnnotator {
	schema, err := NewMappingSchema(nil, d.Name)
	if err != nil {
		schema = nil
	}
	return NewTypeAnnotator(schema, false)
}

// annotateOnChange corresponds to the @annotate_types_on_change decorator:
// re-annotate only when the expression changed, then unconditionally restore
// the original type (a nil original type clears the new expression's type).
func (s *Simplifier) annotateOnChange(orig Expression, res Expression) Expression {
	if isNilExpr(res) {
		return nil
	}
	if s.annotateNewExpressions && structKey(orig) != structKey(res) {
		s.annotator.Clear()
		res = s.annotator.Annotate(res, false)
		res.SetType(orig.Type())
	}
	return res
}

// Simplify corresponds to Simplifier.simplify.
func (s *Simplifier) Simplify(
	expression Expression,
	constantPropagation bool,
	coalesceSimplification bool,
) Expression {
	var whereOwners []Expression
	var joins []Expression

	// walk(prune=Condition or FINAL); BFS by default.
	prune := func(n Expression) bool {
		return isConditionNode(n) || exprMetaGet(n, metaFinal) == true
	}

	for _, node := range Walk(expression, true, prune) {
		if exprMetaGet(node, metaFinal) == true {
			continue
		}

		// Projections/HAVING matching GROUP BY keys must not be simplified
		// (they must correspond verbatim to the group keys).
		if sel, ok := node.(*Select); ok && sel.Group != nil {
			groups := map[string]bool{}
			for _, g := range sel.Group.Expressions {
				groups[structKey(g)] = true
			}
			exprMetaSet(sel.Group, metaFinal, true)

			for _, proj := range sel.Expressions {
				for _, n := range Walk(proj, true, nil) {
					if groups[structKey(n)] {
						exprMetaSet(proj, metaFinal, true)
						break
					}
				}
			}

			if sel.Having != nil {
				for _, n := range Walk(sel.Having, true, nil) {
					if groups[structKey(n)] {
						exprMetaSet(sel.Having, metaFinal, true)
						break
					}
				}
			}
		}

		if isConditionNode(node) {
			// sqlg has no exp.Where wrapper: record the container
			// (Select/Update/Delete) and read its Where field at the end.
			// Ownership must be resolved BEFORE simplification — Replace clears
			// the old node's parent.
			rootIsNode := node == expression
			var owner Expression
			if !rootIsNode {
				switch p := node.Parent().(type) {
				case *Select:
					if p.Where == node {
						owner = p
					}
				case *Update:
					if p.Where == node {
						owner = p
					}
				case *Delete:
					if p.Where == node {
						owner = p
					}
				}
			}

			simplified := whileChanging(node, func(e Expression) Expression {
				return s.simplifyNode(e, constantPropagation, coalesceSimplification)
			})
			if rootIsNode {
				expression = simplified
			}
			if owner != nil {
				whereOwners = append(whereOwners, owner)
			}
		} else if j, ok := node.(*Join); ok {
			// snowflake's match_condition has strict ordering requirements (sqlg does not parse that argument).
			joins = append(joins, j)
		}
	}

	for _, owner := range whereOwners {
		if w := whereOf(owner); alwaysTrue(w) {
			Pop(w)
		}
	}
	for _, join := range joins {
		j := join.(*Join)
		if alwaysTrue(j.On) && len(j.Using) == 0 && (j.Side == "" || j.Side == "RIGHT") &&
			(j.Kind == "" || j.Kind == "INNER" || j.Kind == "OUTER") {
			// Only ("", ""), ("", "INNER"), ("RIGHT", ""), ("RIGHT", "OUTER") can be downgraded to CROSS
			if (j.Side == "" && j.Kind == "") || (j.Side == "" && j.Kind == "INNER") ||
				(j.Side == "RIGHT" && j.Kind == "") || (j.Side == "RIGHT" && j.Kind == "OUTER") {
				Pop(j.On)
				j.Side = ""
				j.Kind = "CROSS"
			}
		}
	}

	return expression
}

// metaFinal corresponds to the module-level constant FINAL = "final".
const metaFinal = "final"

// whereOf extracts the container's current Where condition (sqlg has no exp.Where wrapper node).
func whereOf(owner Expression) Expression {
	switch p := owner.(type) {
	case *Select:
		return p.Where
	case *Update:
		return p.Where
	case *Delete:
		return p.Where
	}
	return nil
}

// simplifyNode corresponds to Simplifier._simplify (two phases: pre-order rewrite + post-order rewrite).
func (s *Simplifier) simplifyNode(
	expression Expression,
	constantPropagation bool,
	coalesceSimplification bool,
) Expression {
	preStack := []Expression{expression}
	type postEntry struct {
		node   Expression
		parent Expression
	}
	var postStack []postEntry

	for len(preStack) > 0 {
		original := preStack[len(preStack)-1]
		preStack = preStack[:len(preStack)-1]
		node := original

		if !isSimpifiableNode(node) {
			if isQueryNode(node) {
				s.Simplify(node, constantPropagation, coalesceSimplification)
			}
			continue
		}

		parent := node.Parent()
		root := node == expression

		node = s.RewriteBetween(node)
		node = s.UniqSort(node, root)
		node = s.absorbAndEliminate(node, root)
		node = s.simplifyConcat(node)
		node = s.simplifyConditionals(node)

		if constantPropagation {
			node = propagateConstants(node, root)
		}

		if node != original {
			Replace(original, node)
		}

		// Python's FINAL raise-guard never triggers (FINAL nodes can't be
		// children of SIMPLIFIABLE nodes); only the filter is kept.
		chs := childrenOf(node)
		for i := len(chs) - 1; i >= 0; i-- {
			if exprMetaGet(chs[i], metaFinal) == true {
				continue
			}
			preStack = append(preStack, chs[i])
		}
		postStack = append(postStack, postEntry{node: node, parent: parent})
	}

	// Must start from expression: when the root is not SIMPLIFIABLE the post
	// loop never runs and the original root must be returned (Python's shared
	// `node` variable behaves the same).
	node := expression
	for len(postStack) > 0 {
		entry := postStack[len(postStack)-1]
		postStack = postStack[:len(postStack)-1]
		original, parent := entry.node, entry.parent
		root := original == expression

		// Re-attach children only (sqlglot's `original.set(k, v)` loop). Never
		// RefreshParents here: it would nil the root's parent and make the final
		// Replace silently fail.
		reparentChildren(original)

		n := s.simplifyNot(original)
		n = FlattenExpr(n)
		n = s.simplifyConnectors(n, root)
		n = s.removeComplements(n, root)

		if coalesceSimplification {
			n = s.simplifyCoalesce(n)
		}
		n.SetParent(parent)

		n = s.simplifyLiterals(n, root)
		n = s.simplifyEquality(n)
		n = simplifyParens(n, s.dialect)
		n = s.simplifyDatetrunc(n)
		n = s.sortComparison(n)
		n = s.simplifyStartswith(n)

		if n != original {
			Replace(original, n)
		}
		node = n
	}

	return node
}

// isSimpifiableNode corresponds to SIMPLIFIABLE = (Binary, Func, Lambda, Predicate, Unary).
func isSimpifiableNode(e Expression) bool {
	switch e.(type) {
	case *Binary, *Unary, *Paren, *Func, *Case, *Cast, *In, *Between, *Is, *Exists:
		return true
	}
	return false
}

// childrenOf returns the result of iter_expressions() (in forward order).
func childrenOf(e Expression) []Expression {
	if isNilExpr(e) {
		return nil
	}
	out := make([]Expression, 0, 2)
	for _, c := range e.Children() {
		if !isNilExpr(c) {
			out = append(out, c)
		}
	}
	return out
}

// reparentChildren re-points e's direct children back to e (sqlglot's
// set(k, v) loop); it does not modify e's own parent.
func reparentChildren(e Expression) {
	if isNilExpr(e) {
		return
	}
	for _, c := range e.Children() {
		if isNilExpr(c) {
			continue
		}
		c.SetParent(e)
	}
}

// whileChanging corresponds to sqlglot.helper.while_changing: apply fn until
// the structKey stops changing.
func whileChanging(expression Expression, fn func(Expression) Expression) Expression {
	for {
		startHash := structKey(expression)
		expression = fn(expression)
		if structKey(expression) == startHash {
			break
		}
	}
	return expression
}

// ---- flatten ----

// FlattenExpr corresponds to the module-level flatten:
// A AND (B AND C) -> A AND B AND C. Replaces a child only when, after
// unnesting, it is the same class as the node itself.
func FlattenExpr(expression Expression) Expression {
	if !isConnectorNode(expression) {
		return expression
	}
	for _, node := range childrenOf(expression) {
		child := unnestExpr(node)
		if nodeClassKey(child) == nodeClassKey(expression) {
			Replace(node, child)
		}
	}
	return expression
}

// flattenConnectorNoUnnest corresponds to `expression.flatten(unnest=False)`.
//
// The only difference from flattenConnector: it does not unnest the operands (keeps the Paren shell).
func flattenConnectorNoUnnest(e Expression) []Expression {
	var out []Expression
	cls := nodeClassKey(e)
	stack := []Expression{e}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if nodeClassKey(n) != cls {
			out = append(out, n)
		}
		// prune: `n.parent and type(n) is not self.__class__`
		if n.Parent() != nil && nodeClassKey(n) != cls {
			continue
		}
		ch := childrenOf(n)
		for i := len(ch) - 1; i >= 0; i-- {
			stack = append(stack, ch[i])
		}
	}
	return out
}

// flattenLike corresponds to the general `Expression.flatten()` (not limited to Connector):
// class identity is determined by nodeClassKey.
func flattenLike(e Expression) []Expression {
	cls := nodeClassKey(e)
	var out []Expression
	stack := []Expression{e}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if nodeClassKey(n) != cls {
			out = append(out, n)
		}
		if n.Parent() != nil && nodeClassKey(n) != cls {
			continue
		}
		ch := childrenOf(n)
		for i := len(ch) - 1; i >= 0; i-- {
			stack = append(stack, ch[i])
		}
	}
	return out
}

// ---- The @annotate_types_on_change methods ----

// tagKids re-points a newly built node's children's parents at it, replicating
// sqlglot's constructor behavior. Any new node reusing existing children must
// be wrapped, or later parent checks and Replace would target the old tree.
func tagKids(e Expression) Expression {
	if !isNilExpr(e) {
		retag(e, e.Children()...)
	}
	return e
}

// RewriteBetween corresponds to Simplifier.rewrite_between:
// `x BETWEEN y AND z` -> `x >= y AND x <= z` (wrapped in Paren when the outer is NOT).
func (s *Simplifier) RewriteBetween(expression Expression) Expression {
	b, ok := expression.(*Between)
	if !ok {
		return expression
	}
	// sqlglot's criterion is `isinstance(expression.parent, exp.Not)`; sqlg's NOT is a Unary.
	if u, isNot := expression.Parent().(*Unary); isNot && u.Op == "NOT" {
		_ = b
	}
	negate := false
	if u, isNot := expression.Parent().(*Unary); isNot && u.Op == "NOT" {
		negate = true
	}

	res := combineWith([]Expression{
		tagKids(&Binary{Op: ">=", Left: b.This.Copy(), Right: b.Low}),
		tagKids(&Binary{Op: "<=", Left: b.This.Copy(), Right: b.High}),
	}, "AND")
	if negate {
		res = &Paren{This: res}
		retag(res, res.(*Paren).This)
	}
	return s.annotateOnChange(expression, res)
}

// simplifyNot corresponds to Simplifier.simplify_not (De Morgan's laws + constant folding).
func (s *Simplifier) simplifyNot(expression Expression) Expression {
	res := s.simplifyNotImpl(expression)
	return s.annotateOnChange(expression, res)
}

func (s *Simplifier) simplifyNotImpl(expression Expression) Expression {
	u, ok := expression.(*Unary)
	if !ok || u.Op != "NOT" {
		return expression
	}
	this := u.This

	if isNullNode(this) {
		return combineWith([]Expression{&Null{}, trueExpr()}, "AND")
	}
	if isComparisonNode(this) {
		b := this.(*Binary)
		right := b.Right
		// COMPLEMENT_SUBQUERY_PREDICATES (All/Any) not produced by sqlg.
		if comp, hit := complementComparisons[b.Op]; hit {
			// No copy (as in Python); tagKids re-parents b.Left to the new node.
			return tagKids(&Binary{Op: comp, Left: b.Left, Right: right})
		}
	}
	if p, isParen := this.(*Paren); isParen {
		condition := unnestExpr(p)
		if isConnectorNode(condition) {
			c := condition.(*Binary)
			if c.Op == "AND" {
				return tagKids(&Paren{This: combineWith([]Expression{
					tagKids(&Unary{Op: "NOT", This: c.Left}),
					tagKids(&Unary{Op: "NOT", This: c.Right}),
				}, "OR")})
			}
			return tagKids(&Paren{This: combineWith([]Expression{
				tagKids(&Unary{Op: "NOT", This: c.Left}),
				tagKids(&Unary{Op: "NOT", This: c.Right}),
			}, "AND")})
		}
		if isNullNode(condition) {
			return combineWith([]Expression{&Null{}, trueExpr()}, "AND")
		}
	}
	if alwaysTrue(this) {
		return falseExpr()
	}
	if isFalseNode(this) {
		return trueExpr()
	}
	if inner, isNot := this.(*Unary); isNot && inner.Op == "NOT" &&
		safeToEliminateDoubleNegation(s.dialect) {
		if IsTypeOf(inner.This, "BOOLEAN") {
			// NOT NOT x -> x (only when x is of BOOLEAN type)
			return inner.This
		}
	}
	return expression
}

// complementComparisons corresponds to Simplifier.COMPLEMENT_COMPARISONS (recovered by Op).
var complementComparisons = map[string]string{
	"<": ">=", ">": "<=", "<=": ">", ">=": "<", "=": "<>", "<>": "=",
}

// simplifyConnectors corresponds to Simplifier.simplify_connectors.
func (s *Simplifier) simplifyConnectors(expression Expression, root bool) Expression {
	res := s.simplifyConnectorsImpl(expression, root)
	return s.annotateOnChange(expression, res)
}

func (s *Simplifier) simplifyConnectorsImpl(expression Expression, root bool) Expression {
	if !isConnectorNode(expression) {
		return expression
	}
	originalParent := expression.Parent()
	expression = s.flatSimplify(expression, s.simplifyConnectorPair, root)

	// A connector reduced to a single non-boolean operand must be wrapped as
	// `x AND TRUE` unless it sits under a connector/Paren chain.
	if !isConnectorNode(expression) {
		if _, isBool := expression.(*Boolean); !isBool {
			if !IsTypeOf(expression, "BOOLEAN") {
				p := originalParent
				for {
					if isConnectorNode(p) {
						break
					}
					if _, isParen := p.(*Paren); !isParen {
						expression = &Binary{Op: "AND", Left: expression, Right: trueExpr()}
						retag(expression, expression.(*Binary).Left, expression.(*Binary).Right)
						break
					}
					if p == nil {
						break
					}
					p = p.Parent()
				}
			}
		}
	}
	return expression
}

// simplifyConnectorPair corresponds to _simplify_connectors(expression, left, right).
func (s *Simplifier) simplifyConnectorPair(
	expression Expression, left Expression, right Expression,
) Expression {
	switch nodeClassKey(expression) {
	case "and":
		if isFalseNode(left) || isFalseNode(right) {
			return falseExpr()
		}
		if isZero(left) || isZero(right) {
			return falseExpr()
		}
		if (isNullNode(left) && isNullNode(right)) ||
			(isNullNode(left) && alwaysTrue(right)) ||
			(alwaysTrue(left) && isNullNode(right)) {
			return &Null{}
		}
		if alwaysTrue(left) && alwaysTrue(right) {
			return trueExpr()
		}
		if alwaysTrue(left) {
			return right
		}
		if alwaysTrue(right) {
			return left
		}
		return s.simplifyComparison(expression, left, right, false)
	case "or":
		if alwaysTrue(left) || alwaysTrue(right) {
			return trueExpr()
		}
		if (isNullNode(left) && isNullNode(right)) ||
			(isNullNode(left) && alwaysFalse(right)) ||
			(alwaysFalse(left) && isNullNode(right)) {
			return &Null{}
		}
		if isFalseNode(left) {
			return right
		}
		if isFalseNode(right) {
			return left
		}
		return s.simplifyComparison(expression, left, right, true)
	}
	return nil
}

// simplifyComparison corresponds to Simplifier._simplify_comparison.
func (s *Simplifier) simplifyComparison(expression Expression, left, right Expression, or bool) Expression {
	res := s.simplifyComparisonImpl(expression, left, right, or)
	return s.annotateOnChange(expression, res)
}

func (s *Simplifier) simplifyComparisonImpl(
	expression Expression, left, right Expression, or bool,
) Expression {
	if !isComparisonNode(left) || !isComparisonNode(right) {
		return nil
	}
	lb, rb := left.(*Binary), right.(*Binary)
	ll, lr := lb.Left, lb.Right
	rl, rr := rb.Left, rb.Right

	largs := []Expression{ll, lr}
	rargs := []Expression{rl, rr}

	var matching []Expression
	for _, a := range largs {
		for _, b := range rargs {
			if structKey(a) == structKey(b) {
				matching = append(matching, a)
				break
			}
		}
	}

	var columns []Expression
	for _, m := range matching {
		if isConstant(m) {
			continue
		}
		if hasNonDeterministic(m) {
			continue
		}
		dup := false
		for _, c := range columns {
			if structKey(c) == structKey(m) {
				dup = true
				break
			}
		}
		if !dup {
			columns = append(columns, m)
		}
	}

	if len(matching) == 0 || len(columns) == 0 {
		return nil
	}

	l, okL := firstNotIn(largs, columns)
	r, okR := firstNotIn(rargs, columns)
	if !okL || !okR {
		return expression
	}

	// Conversion failure returns nil (not expression), as in Python.
	type numOrStr struct {
		isStr bool
		str   string
		num   pyNum
		date  *sqlDateVal
	}
	var lv, rv numOrStr
	if isNumberNode(l) && isNumberNode(r) {
		lv = numOrStr{num: toPyNumber(l)}
		rv = numOrStr{num: toPyNumber(r)}
	} else if isStringNode(l) && isStringNode(r) {
		lv = numOrStr{isStr: true, str: literalText(l)}
		rv = numOrStr{isStr: true, str: literalText(r)}
	} else {
		ld := extractDate(l)
		if ld == nil {
			return nil
		}
		rd := extractDate(r)
		if rd == nil {
			return nil
		}
		// Python does not compare date with datetime, but many engines upcast.
		lv = numOrStr{date: ld.asDateTime()}
		rv = numOrStr{date: rd.asDateTime()}
	}

	cmp := func(a, b numOrStr) int {
		if a.isStr {
			return strings.Compare(a.str, b.str)
		}
		if a.date != nil {
			return a.date.compare(b.date)
		}
		return a.num.cmp(b.num)
	}

	type pair struct {
		expr Expression
		val  numOrStr
	}
	pairs := []pair{{left, lv}, {right, rv}}
	for _, a := range pairs {
		for _, b := range pairs {
			if a.expr == b.expr {
				continue
			}
			ak, bk := nodeClassKey(a.expr), nodeClassKey(b.expr)
			c := cmp(a.val, b.val)

			if (ak == "lt" || ak == "lte") && (bk == "lt" || bk == "lte") {
				if or {
					if c > 0 {
						return left
					}
					return right
				}
				if c <= 0 {
					return left
				}
				return right
			}
			if (ak == "gt" || ak == "gte") && (bk == "gt" || bk == "gte") {
				if or {
					if c < 0 {
						return left
					}
					return right
				}
				if c >= 0 {
					return left
				}
				return right
			}

			// Note: never short-circuit to TRUE, because the column may be NULL.
			if or {
				continue
			}
			if ak == "lt" && (bk == "gt" || bk == "gte") {
				if c <= 0 {
					return falseExpr()
				}
			} else if ak == "gt" && (bk == "lt" || bk == "lte") {
				if c >= 0 {
					return falseExpr()
				}
			} else if ak == "eq" {
				switch bk {
				case "lt":
					if c >= 0 {
						return falseExpr()
					}
					return a.expr
				case "lte":
					if c > 0 {
						return falseExpr()
					}
					return a.expr
				case "gt":
					if c <= 0 {
						return falseExpr()
					}
					return a.expr
				case "gte":
					if c < 0 {
						return falseExpr()
					}
					return a.expr
				case "neq":
					if c == 0 {
						return falseExpr()
					}
					return a.expr
				}
			}
		}
	}
	return nil
}

// firstNotIn corresponds to `first(largs - columns)` (the first item of the set
// difference, ordered by largs's order).
func firstNotIn(args []Expression, exclude []Expression) (Expression, bool) {
	for _, a := range args {
		found := false
		for _, e := range exclude {
			if structKey(e) == structKey(a) {
				found = true
				break
			}
		}
		if !found {
			return a, true
		}
	}
	return nil, false
}

// hasNonDeterministic corresponds to `m.find(exp.Rand, exp.Randn)`.
func hasNonDeterministic(e Expression) bool {
	return HasNode(e, func(n Expression) bool {
		switch nodeClassKey(n) {
		case "rand", "randn":
			return true
		}
		return false
	})
}

// removeComplements corresponds to Simplifier.remove_complements:
// A AND NOT A -> FALSE, A OR NOT A -> TRUE (only when meta "nonnull", written
// by annotate_types via exprMetaStore).
func (s *Simplifier) removeComplements(expression Expression, root bool) Expression {
	res := s.removeComplementsImpl(expression, root)
	return s.annotateOnChange(expression, res)
}

func (s *Simplifier) removeComplementsImpl(expression Expression, root bool) Expression {
	if !isConnectorNode(expression) || !(root || !sameParent(expression)) {
		return expression
	}
	ops := flattenConnector(expression)
	set := map[string]bool{}
	for _, op := range ops {
		set[structKey(op)] = true
	}
	for _, op := range ops {
		u, isNot := op.(*Unary)
		if !isNot || u.Op != "NOT" {
			continue
		}
		if !set[structKey(u.This)] {
			continue
		}
		if v, ok := exprMetaGet(expression, "nonnull").(bool); ok && v {
			if nodeClassKey(expression) == "and" {
				return falseExpr()
			}
			return trueExpr()
		}
	}
	return expression
}

// UniqSort corresponds to Simplifier.uniq_sort: deduplicate + sort a connector.
//
//	C AND A AND B AND B -> A AND B AND C
func (s *Simplifier) UniqSort(expression Expression, root bool) Expression {
	res := s.uniqSortImpl(expression, root)
	return s.annotateOnChange(expression, res)
}

func (s *Simplifier) uniqSortImpl(expression Expression, root bool) Expression {
	if !isConnectorNode(expression) || !(root || !sameParent(expression)) {
		return expression
	}
	flattened := flattenConnector(expression)

	type kv struct {
		sql string
		e   Expression
	}
	var arr []kv
	dedupedLen := 0
	isXor := nodeClassKey(expression) == "xor"

	if isXor {
		// XOR cannot be deduplicated: A XOR A != A (when A == TRUE)
		for _, e := range flattened {
			arr = append(arr, kv{GenSQL(e), e})
		}
	} else {
		seen := map[string]bool{}
		for _, e := range flattened {
			k := GenSQL(e)
			if seen[k] {
				continue
			}
			seen[k] = true
			arr = append(arr, kv{k, e})
		}
		dedupedLen = len(arr)
	}

	sorted := false
	for i := 1; i < len(arr); i++ {
		if arr[i].sql < arr[i-1].sql {
			sorted = true
			break
		}
	}
	if sorted {
		cp := append([]kv(nil), arr...)
		sort.SliceStable(cp, func(i, j int) bool { return cp[i].sql < cp[j].sql })
		operands := make([]Expression, 0, len(cp))
		for _, x := range cp {
			operands = append(operands, x.e)
		}
		if isXor {
			return combineWith(operands, "XOR")
		}
		if nodeClassKey(expression) == "and" {
			return combineWith(operands, "AND")
		}
		return combineWith(operands, "OR")
	}

	// No sorting needed, but deduplication may still be required
	if !isXor && dedupedLen < len(flattened) {
		uniqueOperand := flattened[0]
		if dedupedLen == 1 {
			return tagKids(&Binary{Op: "AND", Left: uniqueOperand, Right: trueExpr()})
		}
		operands := make([]Expression, 0, len(arr))
		for _, x := range arr {
			operands = append(operands, x.e)
		}
		if nodeClassKey(expression) == "and" {
			return combineWith(operands, "AND")
		}
		return combineWith(operands, "OR")
	}
	return expression
}

// absorbAndEliminate corresponds to Simplifier.absorb_and_eliminate:
// absorption (A AND (A OR B) -> A, A AND (NOT A OR B) -> A AND B) and
// elimination ((A AND B) OR (A AND NOT B) -> A).
func (s *Simplifier) absorbAndEliminate(expression Expression, root bool) Expression {
	res := s.absorbAndEliminateImpl(expression, root)
	return s.annotateOnChange(expression, res)
}

func (s *Simplifier) absorbAndEliminateImpl(expression Expression, root bool) Expression {
	if !isConnectorNode(expression) || !(root || !sameParent(expression)) {
		return expression
	}
	kind := "and"
	if nodeClassKey(expression) == "and" {
		kind = "or"
	}

	ops := flattenConnector(expression)

	// opSet: all operands; subops: sub-operand sets; pairs: complementary pairs.
	opSet := map[string]bool{}
	subops := map[string][][]string{}
	pairs := map[string][]elimPair{}

	for _, op := range ops {
		opSet[structKey(op)] = true

		if nodeClassKey(op) != kind {
			// Of the form A OR (A AND B): the subop is A itself
			subops[structKey(op)] = append(subops[structKey(op)], []string{structKey(op)})
			continue
		}

		subset := flattenConnector(op)
		keys := make([]string, 0, len(subset))
		for _, i := range subset {
			keys = append(keys, structKey(i))
		}
		for _, k := range keys {
			subops[k] = append(subops[k], keys)
		}

		operands := unnestOperands(op)
		a, b := operands[0], operands[1]
		if u, isNot := a.(*Unary); isNot && u.Op == "NOT" {
			pairs[frozensetKey(u.This, b)] = append(pairs[frozensetKey(u.This, b)], elimPair{op, b})
		}
		if u, isNot := b.(*Unary); isNot && u.Op == "NOT" {
			pairs[frozensetKey(a, u.This)] = append(pairs[frozensetKey(a, u.This)], elimPair{op, a})
		}
	}

	for _, op := range ops {
		if nodeClassKey(op) != kind {
			continue
		}
		operands := unnestOperands(op)
		a, b := operands[0], operands[1]

		// Absorb
		if u, isNot := a.(*Unary); isNot && u.Op == "NOT" && opSet[structKey(u.This)] {
			Replace(a, trueOrFalse(kind))
			continue
		}
		if u, isNot := b.(*Unary); isNot && u.Op == "NOT" && opSet[structKey(u.This)] {
			Replace(b, trueOrFalse(kind))
			continue
		}
		superset := map[string]bool{}
		for _, x := range flattenConnector(op) {
			superset[structKey(x)] = true
		}
		absorbed := false
		for k := range superset {
			for _, sub := range subops[k] {
				if isStrictSubsetStr(sub, superset) {
					absorbed = true
					break
				}
			}
			if absorbed {
				break
			}
		}
		if absorbed {
			// Opposite polarity from the NOT-absorb branch above (matches Python).
			Replace(op, supersetValue(kind))
			continue
		}

		// Eliminate
		for _, pair := range pairs[frozensetKey(a, b)] {
			Replace(pair.op, pair.complement)
			Replace(op, pair.complement)
		}
	}
	return expression
}

type elimPair struct {
	op         Expression
	complement Expression
}

// trueOrFalse corresponds to the Absorb branch's `exp.true() if kind == exp.And else exp.false()`.
// kind is the inner sub-expression type (opposite of the outer connector).
func trueOrFalse(kind string) Expression {
	if kind == "and" {
		return trueExpr()
	}
	return falseExpr()
}

// supersetValue corresponds to the superset absorption branch's `exp.false() if kind == exp.And else exp.true()`.
func supersetValue(kind string) Expression {
	if kind == "and" {
		return falseExpr()
	}
	return trueExpr()
}

// frozensetKey corresponds to the hash key of `frozenset((a, b))` (the two elements are order-independent).
func frozensetKey(a, b Expression) string {
	ka, kb := structKey(a), structKey(b)
	if ka > kb {
		ka, kb = kb, ka
	}
	return ka + "\x00" + kb
}

// isStrictSubsetStr reports whether sub is a strict subset of superset.
func isStrictSubsetStr(sub []string, superset map[string]bool) bool {
	if len(sub) >= len(superset) {
		return false
	}
	for _, k := range sub {
		if !superset[k] {
			return false
		}
	}
	return true
}

// simplifyEquality corresponds to Simplifier.simplify_equality: x + 1 = 3 -> x = 2.
func (s *Simplifier) simplifyEquality(expression Expression) Expression {
	res := s.simplifyEqualityImpl(expression)
	return s.annotateOnChange(expression, res)
}

func (s *Simplifier) simplifyEqualityImpl(expression Expression) Expression {
	if !isComparisonNode(expression) {
		return expression
	}
	b := expression.(*Binary)
	l, r := b.Left, b.Right

	if !isInverseOp(l) {
		return expression
	}

	var aPred, bPred func(Expression) bool
	if isNumberNode(r) {
		aPred, bPred = isNumberNode, isNumberNode
	} else if isDateLiteral(r) {
		aPred, bPred = isDateLiteral, isIntervalNode
	} else {
		return expression
	}

	var a, bb Expression
	if isInverseDateOp(l) {
		f := l.(*Func)
		a = f.Args[0]
		bb = intervalOfIntervalOp(f)
	} else {
		lb := l.(*Binary)
		a, bb = lb.Left, lb.Right
	}

	if !aPred(a) && bPred(bb) {
		// keep unchanged
	} else if !aPred(bb) && bPred(a) {
		a, bb = bb, a
	} else {
		return expression
	}

	inverse := inverseOpFor(l)
	var rhs Expression
	switch {
	case inverse == "add" || inverse == "sub":
		op := map[string]string{"add": "+", "sub": "-"}[inverse]
		bin := &Binary{Op: op, Left: r, Right: bb}
		retag(bin, bin.Left, bin.Right)
		rhs = bin
	case inverse == "dateadd" || inverse == "datesub" || inverse == "datetimeadd" || inverse == "datetimesub":
		// INVERSE_DATE_OPS: DateAdd -> Sub, DateSub -> Add, DatetimeAdd -> Sub, DatetimeSub -> Add
		op := "-"
		if inverse == "datesub" || inverse == "datetimesub" {
			op = "+"
		}
		bin := &Binary{Op: op, Left: r, Right: bb}
		retag(bin, bin.Left, bin.Right)
		rhs = bin
	default:
		return expression
	}
	out := &Binary{Op: b.Op, Left: a, Right: rhs}
	retag(out, out.Left, out.Right)
	return out
}

// isInverseOp corresponds to `l.__class__ in self.INVERSE_OPS` (Add/Sub/DateAdd/DateSub/...).
func isInverseOp(e Expression) bool {
	switch nodeClassKey(e) {
	case "add", "sub", "dateadd", "datesub", "datetimeadd", "datetimesub":
		return true
	}
	return false
}

// isInverseDateOp corresponds to `l.__class__ in self.INVERSE_DATE_OPS`.
func isInverseDateOp(e Expression) bool {
	switch nodeClassKey(e) {
	case "dateadd", "datesub", "datetimeadd", "datetimesub":
		return true
	}
	return false
}

// inverseOpFor corresponds to `self.INVERSE_OPS[l.__class__]`, returning the target class-identity key.
func inverseOpFor(l Expression) string {
	switch nodeClassKey(l) {
	// INVERSE_DATE_OPS: DateAdd->Sub, DateSub->Add, DatetimeAdd->Sub, DatetimeSub->Add
	case "dateadd", "datetimeadd":
		return "sub"
	case "datesub", "datetimesub":
		return "add"
	// INVERSE_OPS: Add->Sub, Sub->Add
	case "add":
		return "sub"
	case "sub":
		return "add"
	}
	return ""
}

// simplifyLiterals corresponds to Simplifier.simplify_literals.
func (s *Simplifier) simplifyLiterals(expression Expression, root bool) Expression {
	res := s.simplifyLiteralsImpl(expression, root)
	return s.annotateOnChange(expression, res)
}

func (s *Simplifier) simplifyLiteralsImpl(expression Expression, root bool) Expression {
	if isBinaryClassNode(expression) && !isConnectorNode(expression) {
		return s.flatSimplify(expression, s.simplifyBinary, root)
	}
	if u, ok := expression.(*Unary); ok && u.Op == "-" {
		if inner, ok := u.This.(*Unary); ok && inner.Op == "-" {
			return inner.This
		}
	}
	if isInverseDateOp(expression) {
		f := expression.(*Func)
		if r := s.simplifyBinary(expression, f.Args[0], intervalOfIntervalOp(f)); r != nil {
			return r
		}
		return expression
	}
	return expression
}

// IntervalOf takes the IntervalOp's interval(): Interval(this=expression, unit=unit).
func intervalOfIntervalOp(f *Func) Expression {
	iv := &Interval{}
	if len(f.Args) > 1 {
		iv.Value = f.Args[1].Copy()
	}
	if len(f.Args) > 2 {
		if lit, ok := f.Args[2].(*Literal); ok {
			iv.Unit = strings.ToUpper(lit.Value)
		} else if c, ok := f.Args[2].(*Column); ok {
			iv.Unit = strings.ToUpper(identText(c.Name))
		}
	}
	retag(iv, iv.Value)
	return iv
}

// simplifyIntegerCast corresponds to Simplifier._simplify_integer_cast.
func (s *Simplifier) simplifyIntegerCast(expr Expression) Expression {
	c, ok := expr.(*Cast)
	if !ok {
		return expr
	}
	var this Expression
	if inner, isCast := c.This.(*Cast); isCast {
		this = s.simplifyIntegerCast(inner)
	} else {
		this = c.This
	}

	if isIntNode(this) && c.To != nil {
		n := toPyNumber(this)
		if !n.isInt {
			return expr
		}
		// Only byte-range upcasts are safe to remove; downcasts may overflow.
		if n.i >= -128 && n.i <= 127 && dtSignedInteger[c.To.Name] {
			return this
		}
		if n.i >= 0 && n.i <= 255 && dtUnsignedInteger[c.To.Name] {
			return this
		}
	}
	return expr
}

// simplifyBinary corresponds to Simplifier._simplify_binary (returning nil means "no result").
func (s *Simplifier) simplifyBinary(expression, a, b Expression) Expression {
	if isComparisonNode(expression) {
		a = s.simplifyIntegerCast(a)
		b = s.simplifyIntegerCast(b)
	}

	if isNode, ok := expression.(*Is); ok {
		var c Expression
		not := false
		if u, isNot := b.(*Unary); isNot && u.Op == "NOT" {
			c = u.This
			not = true
		} else {
			c = b
		}
		if isNullNode(c) {
			if _, isLit := a.(*Literal); isLit {
				if not {
					return trueExpr()
				}
				return falseExpr()
			}
			if isNullNode(a) {
				if not {
					return falseExpr()
				}
				return trueExpr()
			}
		}
		_ = isNode
	} else if isNullOkNode(expression) {
		return nil
	} else if (isNullNode(a) || isNullNode(b)) && isIfFunc(expression.Parent()) {
		return &Null{}
	}

	if isNumberNode(a) && isNumberNode(b) {
		na, nb := toPyNumber(a), toPyNumber(b)

		switch nodeClassKey(expression) {
		case "add":
			return numberLiteral(na.add(nb))
		case "mul":
			return numberLiteral(na.mul(nb))
		case "sub":
			// Sub/Div are not associative; only simplify when a and b share the same parent
			if a.Parent() == b.Parent() {
				return numberLiteral(na.sub(nb))
			}
			return nil
		case "div":
			// Engines differ on integer division; intdiv is unsafe
			if (na.isInt && nb.isInt) || a.Parent() != b.Parent() {
				return nil
			}
			return numberLiteral(na.div(nb))
		}

		if bo := evalBoolean(expression, na, nb); bo != nil {
			return bo
		}
	} else if isStringNode(a) && isStringNode(b) {
		if bo := evalBooleanStr(expression, literalText(a), literalText(b)); bo != nil {
			return bo
		}
	} else if isDateLiteral(a) && isIntervalClassNode(b) {
		date, bi := extractDate(a), extractInterval(b)
		if date != nil && bi != nil {
			switch nodeClassKey(expression) {
			case "add", "dateadd", "datetimeadd":
				return dateLiteral(date.add(bi), extractTypeOf(a))
			case "sub", "datesub", "datetimesub":
				return dateLiteral(date.sub(bi), extractTypeOf(a))
			}
		}
	} else if isIntervalClassNode(a) && isDateLiteral(b) {
		ai, date := extractInterval(a), extractDate(b)
		// an interval cannot be subtracted from a date
		if ai != nil && date != nil && nodeClassKey(expression) == "add" {
			return dateLiteral(ai.addToDate(date), extractTypeOf(b))
		}
	} else if isDateLiteral(a) && isDateLiteral(b) {
		if isPredicateNodeOf(expression) {
			da, db := extractDate(a), extractDate(b)
			if bo := evalBooleanDate(expression, da, db); bo != nil {
				return bo
			}
		}
	}

	return nil
}

// isNullOkNode corresponds to `isinstance(expression, self.NULL_OK)` (NullSafeEQ/NullSafeNEQ/PropertyEQ).
func isNullOkNode(e Expression) bool {
	switch nodeClassKey(e) {
	case "nullsafeeq", "nullsafeneq", "propertyeq":
		return true
	}
	return false
}

// isIfFunc corresponds to `isinstance(node, exp.If)` (in sqlg IF/IIF are *Func).
func isIfFunc(e Expression) bool {
	f, ok := e.(*Func)
	if !ok {
		return false
	}
	n := strings.ToUpper(f.Name)
	return n == "IF" || n == "IIF"
}

// simplifyCoalesce corresponds to Simplifier.simplify_coalesce.
func (s *Simplifier) simplifyCoalesce(expression Expression) Expression {
	res := s.simplifyCoalesceImpl(expression)
	return s.annotateOnChange(expression, res)
}

func (s *Simplifier) simplifyCoalesceImpl(expression Expression) Expression {
	// COALESCE(x) -> x
	if f, ok := expression.(*Func); ok && nodeClassKey(f) == "coalesce" {
		// f.Args[0] is sqlglot's `this`; the rest are `expressions`.
		var rest []Expression
		if len(f.Args) > 0 {
			rest = f.Args[1:]
		}
		if (len(rest) == 0 || isNonnullConstant(f.Args[0])) && !isHintParent(expression.Parent()) {
			return f.Args[0]
		}
	}

	if coalesceComparisonNonStandard(s.dialect) {
		return expression
	}
	if !isComparisonNode(expression) {
		return expression
	}

	b := expression.(*Binary)
	var coalesce *Func
	var other Expression
	if cf, ok := b.Left.(*Func); ok && nodeClassKey(cf) == "coalesce" {
		coalesce, other = cf, b.Right
	} else if cf, ok := b.Right.(*Func); ok && nodeClassKey(cf) == "coalesce" {
		coalesce, other = cf, b.Left
	} else {
		return expression
	}

	// Holds for non-constants too, but only takes effect when both are constants.
	if !isConstant(other) {
		return expression
	}

	// find the first constant argument
	argIndex := -1
	for i, arg := range coalesce.Args[1:] {
		if isConstant(arg) {
			argIndex = i
			break
		}
	}
	if argIndex < 0 {
		return expression
	}

	coalesce.Args = coalesce.Args[:1+argIndex]
	retag(coalesce, coalesce.Args...)

	// Drop COALESCE early to save one simplification iteration.
	var core Expression = coalesce
	if len(coalesce.Args) <= 1 {
		core = coalesce.Args[0]
	}

	isNullCall := tagKids(&Is{This: core.Copy(), Expr: &Null{}})
	isNullCallNegated := tagKids(&Is{This: core.Copy(), Expr: &Null{}})
	and1 := tagKids(&Binary{Op: "AND", Left: tagKids(&Unary{Op: "NOT", This: isNullCallNegated}), Right: expression.Copy()})
	and2 := tagKids(&Binary{Op: "AND", Left: isNullCall, Right: tagKids(&Binary{Op: b.Op, Left: argCopyAt(coalesce, argIndex), Right: other.Copy()})})
	return tagKids(&Paren{This: combineWith([]Expression{and1, and2}, "OR")})
}

// argCopyAt takes a copy of coalesce's "first constant argument".
func argCopyAt(coalesce *Func, argIndex int) Expression {
	// Args[0] is this; the constant argument is at Args[1+argIndex]
	idx := 1 + argIndex
	if idx < len(coalesce.Args) {
		return coalesce.Args[idx].Copy()
	}
	return &Null{}
}

// isHintParent corresponds to `isinstance(expression.parent, exp.Hint)` (sqlg has no Hint node).
func isHintParent(Expression) bool { return false }

// simplifyConcat corresponds to Simplifier.simplify_concat: concatenate consecutive string literals.
func (s *Simplifier) simplifyConcat(expression Expression) Expression {
	res := s.simplifyConcatImpl(expression)
	return s.annotateOnChange(expression, res)
}

func (s *Simplifier) simplifyConcatImpl(expression Expression) Expression {
	f, ok := expression.(*Func)
	if !ok {
		return expression
	}
	key := nodeClassKey(f)
	if key != "concat" && key != "concatws" && key != "dpipe" {
		return expression
	}
	// CONCAT_WS cannot be merged when its separator is statically unknown
	if key == "concatws" && (len(f.Args) == 0 || !isStringNode(f.Args[0])) {
		return expression
	}

	var expressions []Expression
	var sep string
	sepExpr := Expression(nil)
	if key == "concatws" {
		sepExpr = f.Args[0]
		separator := eText(sepExpr)
		sep = separator
		expressions = f.Args[1:]
	} else {
		if len(f.Args) > 0 {
			expressions = f.Args
		} else {
			expressions = flattenLike(f)
		}
	}

	// group and merge consecutive string literals
	var newArgs []Expression
	var group []Expression
	flush := func() {
		if len(group) == 0 {
			return
		}
		parts := make([]string, 0, len(group))
		for _, g := range group {
			parts = append(parts, eText(g))
		}
		newArgs = append(newArgs, Str(strings.Join(parts, sep)))
		group = nil
	}
	for _, e := range expressions {
		if isStringNode(e) {
			group = append(group, e)
			continue
		}
		flush()
		newArgs = append(newArgs, e)
	}
	flush()

	if len(newArgs) == 1 && isStringNode(newArgs[0]) {
		return newArgs[0]
	}

	if key == "concatws" {
		newArgs = append([]Expression{sepExpr}, newArgs...)
		return tagKids(&Func{Name: f.Name, Args: newArgs, Distinct: f.Distinct, Filter: f.Filter, Star: f.Star, Over: f.Over, Suffix: f.Suffix})
	}
	if key == "dpipe" {
		var acc Expression
		for _, a := range newArgs {
			if acc == nil {
				acc = a
				continue
			}
			acc = tagKids(&Binary{Op: "||", Left: acc, Right: a})
		}
		return acc
	}
	return tagKids(&Func{Name: f.Name, Args: newArgs, Distinct: f.Distinct, Filter: f.Filter, Star: f.Star, Over: f.Over, Suffix: f.Suffix})
}

// eText corresponds to sqlglot's `Expression.name` (Literal/Identifier → the `this` text).
func eText(e Expression) string {
	switch n := e.(type) {
	case *Literal:
		return n.Value
	case *Ident:
		return n.Name
	case *Column:
		return identText(n.Name)
	case *Var2:
		return n.Value
	}
	return ""
}

// Var2 is a minimal placeholder used by eText (sqlg has no Var node).
type Var2 struct {
	base
	Value string
}

func (n *Var2) Children() []Expression     { return nil }
func (n *Var2) SetChildren(_ []Expression) {}
func (n *Var2) Copy() Expression           { return &Var2{Value: n.Value} }
func (n *Var2) SQL(_ *Dialect) string      { return n.Value }

// simplifyConditionals corresponds to Simplifier.simplify_conditionals.
func (s *Simplifier) simplifyConditionals(expression Expression) Expression {
	res := s.simplifyConditionalsImpl(expression)
	return s.annotateOnChange(expression, res)
}

func (s *Simplifier) simplifyConditionalsImpl(expression Expression) Expression {
	if c, ok := expression.(*Case); ok {
		// Replicates Python's "detach operand once, then copy per WHEN" behavior.
		this := c.Operand
		popped := false

		// Index + live length replicates Python's for-in iterator skipping
		// elements deleted in place by case.pop().
		for i := 0; i < len(c.Ifs); i++ {
			w := c.Ifs[i]
			cond := w.Cond
			if this != nil {
				// Convert CASE x WHEN matching_value ... to CASE WHEN x = matching_value ...
				if !popped {
					c.Operand = nil
					this.SetParent(nil)
					popped = true
				}
				eq := &Binary{Op: "=", Left: this.Copy(), Right: cond.Copy()}
				retag(eq, eq.Left, eq.Right)
				Replace(w.Cond, eq)
				cond = w.Cond
			}
			if alwaysTrue(cond) {
				return w.Then
			}
			if alwaysFalse(cond) {
				Pop(w)
				if len(c.Ifs) == 0 {
					if c.Default != nil {
						return c.Default
					}
					return &Null{}
				}
			}
		}
		return expression
	}

	if f, ok := expression.(*Func); ok && isIfFunc(f) && !isCaseParent(expression.Parent()) {
		if len(f.Args) == 0 {
			return expression
		}
		if alwaysTrue(f.Args[0]) {
			if len(f.Args) > 1 {
				return f.Args[1]
			}
			return &Null{}
		}
		if alwaysFalse(f.Args[0]) {
			if len(f.Args) > 2 {
				return f.Args[2]
			}
			return &Null{}
		}
	}

	return expression
}

// isCaseParent corresponds to `isinstance(node.parent, exp.Case)`.
func isCaseParent(e Expression) bool {
	_, ok := e.(*Case)
	return ok
}

// simplifyStartswith corresponds to Simplifier.simplify_startswith.
func (s *Simplifier) simplifyStartswith(expression Expression) Expression {
	res := s.simplifyStartswithImpl(expression)
	return s.annotateOnChange(expression, res)
}

func (s *Simplifier) simplifyStartswithImpl(expression Expression) Expression {
	f, ok := expression.(*Func)
	if !ok || nodeClassKey(f) != "startswith" || len(f.Args) < 2 {
		return expression
	}
	if !isStringNode(f.Args[0]) || !isStringNode(f.Args[1]) {
		return expression
	}
	if strings.HasPrefix(eText(f.Args[0]), eText(f.Args[1])) {
		return trueExpr()
	}
	return falseExpr()
}

// isDatetruncPredicate corresponds to Simplifier._is_datetrunc_predicate.
func (s *Simplifier) isDatetruncPredicate(left, right Expression) bool {
	switch nodeClassKey(left) {
	case "datetrunc", "timestamptrunc":
	default:
		return false
	}
	return isDateLiteral(right)
}

// simplifyDatetrunc corresponds to Simplifier.simplify_datetrunc.
func (s *Simplifier) simplifyDatetrunc(expression Expression) Expression {
	res := s.simplifyDatetruncImpl(expression)
	return s.annotateOnChange(expression, res)
}

func (s *Simplifier) simplifyDatetruncImpl(expression Expression) Expression {
	comparison := nodeClassKey(expression)

	if f, ok := expression.(*Func); ok {
		if k := nodeClassKey(f); k == "datetrunc" || k == "timestamptrunc" {
			if len(f.Args) < 2 {
				return expression
			}
			this := f.Args[1]
			truncType := extractTypeOf(this)
			date := extractDate(this)
			if date != nil {
				unit := strings.ToLower(eText(f.Args[0]))
				if unit != "" {
					fl := dateFloor(date, unit, s.dialect)
					if fl != nil {
						return dateLiteral(fl, truncType)
					}
				}
			}
		}
	}

	switch comparison {
	case "lt", "gt", "lte", "gte", "eq", "neq", "in":
	default:
		return expression
	}

	if b, ok := expression.(*Binary); ok {
		l, r := b.Left, b.Right
		if !s.isDatetruncPredicate(l, r) {
			return expression
		}
		trunc := l.(*Func)
		if len(trunc.Args) < 2 {
			return expression
		}
		truncArg := trunc.Args[1]
		unit := strings.ToLower(eText(trunc.Args[0]))
		date := extractDate(r)
		if date == nil {
			return expression
		}
		targetType := extractTypeOf(r)
		res := datetruncBinary(comparison, truncArg, date, unit, s.dialect, targetType)
		if res == nil {
			return expression
		}
		return res
	}

	if in, ok := expression.(*In); ok {
		l := in.This
		rs := in.List
		if len(rs) > 0 {
			all := true
			for _, r := range rs {
				if !s.isDatetruncPredicate(l, r) {
					all = false
					break
				}
			}
			if all {
				trunc := l.(*Func)
				if len(trunc.Args) < 2 {
					return expression
				}
				unit := strings.ToLower(eText(trunc.Args[0]))

				var ranges []dateRange
				for _, r := range rs {
					date := extractDate(r)
					if date == nil {
						return expression
					}
					dr, ok := datetruncRange(date, unit, s.dialect)
					if ok {
						ranges = append(ranges, dr)
					}
				}
				if len(ranges) == 0 {
					return expression
				}
				ranges = mergeRanges(ranges)
				targetType := extractTypeOf(rs[0])
				var ors []Expression
				for _, dr := range ranges {
					ors = append(ors, datetruncEqExpression(l, dr, targetType))
				}
				return combineWith(ors, "OR")
			}
		}
	}

	return expression
}

// sortComparison corresponds to Simplifier.sort_comparison.
func (s *Simplifier) sortComparison(expression Expression) Expression {
	res := s.sortComparisonImpl(expression)
	return s.annotateOnChange(expression, res)
}

func (s *Simplifier) sortComparisonImpl(expression Expression) Expression {
	b, ok := expression.(*Binary)
	if !ok {
		return expression
	}
	if _, hit := complementComparisons[b.Op]; !hit {
		return expression
	}
	l, r := b.Left, b.Right
	_, lColumn := l.(*Column)
	_, rColumn := r.(*Column)
	lConst := isConstant(l)
	rConst := isConstant(r)

	if (lColumn && !rColumn) || (rConst && !lConst) || isSubqueryPredicate(r) {
		return expression
	}
	if (rColumn && !lColumn) || (lConst && !rConst) || (GenSQL(l) > GenSQL(r)) {
		if inv, hit := inverseComparisons[b.Op]; hit {
			return tagKids(&Binary{Op: inv, Left: r, Right: l})
		}
		return tagKids(&Binary{Op: b.Op, Left: r, Right: l})
	}
	return expression
}

// inverseComparisons corresponds to Simplifier.INVERSE_COMPARISONS.
var inverseComparisons = map[string]string{
	"<": ">", ">": "<", "<=": ">=", ">=": "<=",
}

// isSubqueryPredicate corresponds to `isinstance(r, exp.SubqueryPredicate)` (All/Any, which sqlg does not produce).
func isSubqueryPredicate(Expression) bool { return false }

// flatSimplify corresponds to Simplifier._flat_simplify.
func (s *Simplifier) flatSimplify(
	expression Expression,
	simplifier func(Expression, Expression, Expression) Expression,
	root bool,
) Expression {
	if !(root || !sameParent(expression)) {
		return expression
	}
	var operands []Expression
	queue := flattenConnectorNoUnnest(expression)
	size := len(queue)

	for len(queue) > 0 {
		a := queue[0]
		queue = queue[1:]
		replaced := false
		for bi, b := range queue {
			result := simplifier(expression, a, b)
			if result != nil && result != expression {
				// Python's list.remove searches first by identity, then by structural equality
				target := bi
				for i := 0; i < len(queue); i++ {
					if queue[i] == b || structKey(queue[i]) == structKey(b) {
						target = i
						break
					}
				}
				queue = append(queue[:target], queue[target+1:]...)
				queue = append([]Expression{result}, queue...)
				replaced = true
				break
			}
		}
		if !replaced {
			operands = append(operands, a)
		}
	}

	if len(operands) < size {
		acc := operands[0]
		for _, o := range operands[1:] {
			acc = makeBinaryLike(expression, acc, o)
		}
		return acc
	}
	return expression
}

// makeBinaryLike corresponds to `expression.__class__(this=a, expression=b)`.
func makeBinaryLike(tmpl Expression, left, right Expression) Expression {
	switch tmpl.(type) {
	case *Is:
		return tagKids(&Is{This: left, Expr: right})
	case *Binary:
		return tagKids(&Binary{Op: tmpl.(*Binary).Op, Left: left, Right: right})
	}
	return tmpl
}

// simplifyParens corresponds to the module-level simplify_parens.
func simplifyParens(expression Expression, dialect *Dialect) Expression {
	p, ok := expression.(*Paren)
	if !ok {
		return expression
	}
	this := p.This
	parent := expression.Parent()
	parentIsPredicate := isPredicateNodeOf(parent)

	if _, isSelect := this.(*Select); isSelect {
		return expression
	}
	// isinstance(parent, (exp.SubqueryPredicate, exp.Bracket))
	if _, isBracket := parent.(*Bracket); isBracket {
		return expression
	}

	if requiresParenthesizedStructAccess(dialect) {
		if _, isDot := parent.(*Binary); isDot && parent.(*Binary).Op == "." {
			if r := parent.(*Binary).Right; isIdentOrStar(r) {
				return expression
			}
		}
	}

	if !isConditionNode(parent) ||
		isParenNode(parent) ||
		(!isBinaryClassNode(this) && !((isNotUnary(this) || isIsNode(this)) && parentIsPredicate)) ||
		(isPredicateNodeOf(this) && !(parentIsPredicate || isNegParent(parent))) ||
		(isBinaryOp(this, "+") && isBinaryOp(parent, "+")) ||
		(isBinaryOp(this, "*") && isBinaryOp(parent, "*")) ||
		(isBinaryOp(this, "*") && (isBinaryOp(parent, "+") || isBinaryOp(parent, "-"))) {
		return this
	}

	return expression
}

func isIdentOrStar(e Expression) bool {
	switch e.(type) {
	case *Ident, *Star:
		return true
	}
	return false
}

func isParenNode(e Expression) bool {
	_, ok := e.(*Paren)
	return ok
}

func isNotUnary(e Expression) bool {
	u, ok := e.(*Unary)
	return ok && u.Op == "NOT"
}

func isIsNode(e Expression) bool {
	_, ok := e.(*Is)
	return ok
}

func isNegParent(e Expression) bool {
	u, ok := e.(*Unary)
	return ok && u.Op == "-"
}

func isBinaryOp(e Expression, op string) bool {
	b, ok := e.(*Binary)
	return ok && b.Op == op
}

// ---- Constant/literal predicates ----

// alwaysTrue corresponds to always_true.
func alwaysTrue(expression Expression) bool {
	if b, ok := expression.(*Boolean); ok {
		return b.Value
	}
	if l, ok := expression.(*Literal); ok && !l.IsString {
		// Negative literals are Neg in sqlglot, hence not always_true.
		if isNegLiteralText(l) {
			return false
		}
		return !isZero(expression)
	}
	return false
}

// alwaysFalse corresponds to always_false.
func alwaysFalse(expression Expression) bool {
	return isFalseNode(expression) || isNullNode(expression) || isZero(expression)
}

// isZero corresponds to is_zero.
func isZero(expression Expression) bool {
	l, ok := expression.(*Literal)
	if !ok || l.IsString {
		return false
	}
	n := toPyNumber(l)
	return n.isZero()
}

// isFalseNode corresponds to is_false (`type(a) is exp.Boolean and not a.this`).
func isFalseNode(a Expression) bool {
	b, ok := a.(*Boolean)
	return ok && !b.Value
}

// isNullNode corresponds to is_null (`type(a) is exp.Null`).
func isNullNode(a Expression) bool {
	_, ok := a.(*Null)
	return ok
}

// isNumberNode corresponds to `Expression.is_number`
// (a Literal that is not a string, or a Neg-wrapped number).
func isNumberNode(e Expression) bool {
	if l, ok := e.(*Literal); ok {
		return !l.IsString
	}
	if u, ok := e.(*Unary); ok && u.Op == "-" {
		return isNumberNode(u.This)
	}
	return false
}

// isIntNode corresponds to `Expression.is_int`.
func isIntNode(e Expression) bool {
	if !isNumberNode(e) {
		return false
	}
	return toPyNumber(e).isInt
}

// isStringNode corresponds to `Expression.is_string`.
func isStringNode(e Expression) bool {
	l, ok := e.(*Literal)
	return ok && l.IsString
}

// literalText corresponds to a Literal's `this` (the raw text).
func literalText(e Expression) string {
	switch n := e.(type) {
	case *Literal:
		return n.Value
	case *Unary:
		if n.Op == "-" {
			return "-" + literalText(n.This)
		}
	}
	return ""
}

// isConstant corresponds to `isinstance(expression, exp.CONSTANTS)` (Literal/Boolean/Null).
func isConstant(e Expression) bool {
	switch n := e.(type) {
	case *Literal:
		// sqlg folds "-" into the literal; sqlglot's Neg(Literal) is not a
		// CONSTANT, so negative literals are non-constant here.
		return !isNegLiteralText(n)
	case *Boolean, *Null:
		return true
	case *Unary:
		return false
	}
	// _is_constant also counts date literals (CAST('...' AS DATE)).
	return isDateLiteral(e)
}

// isNegLiteralText reports whether a literal is what sqlglot sees as Neg(Literal(1)) (the parser folded the minus sign).
func isNegLiteralText(l *Literal) bool {
	return !l.IsString && strings.HasPrefix(l.Value, "-")
}

// isNonnullConstant corresponds to `isinstance(expression, exp.NONNULL_CONSTANTS) or _is_date_literal(...)`.
func isNonnullConstant(e Expression) bool {
	switch n := e.(type) {
	case *Literal:
		// A negative numeric literal is Neg in sqlglot (not NONNULL_CONSTANTS); see the isConstant comment.
		return !isNegLiteralText(n)
	case *Boolean:
		return true
	}
	return isDateLiteral(e)
}

// isIntervalNode corresponds to `_is_interval`:
// `isinstance(e, exp.Interval) and extract_interval(e) is not None`.
func isIntervalNode(e Expression) bool {
	if _, ok := e.(*Interval); !ok {
		return false
	}
	return extractInterval(e) != nil
}

// isIntervalClassNode corresponds to `isinstance(e, exp.Interval)`; the looser
// criterion used by _simplify_binary's date branch.
func isIntervalClassNode(e Expression) bool {
	_, ok := e.(*Interval)
	return ok
}

// isDateLiteral corresponds to _is_date_literal.
func isDateLiteral(e Expression) bool { return extractDate(e) != nil }

// extractTypeOf corresponds to extract_type.
func extractTypeOf(e Expression) *DataType {
	switch n := e.(type) {
	case *Cast:
		return n.To
	case *TypedLiteral:
		return &DataType{Name: n.Kind}
	}
	return e.Type()
}

// evalBoolean corresponds to eval_boolean (the numeric branch).
func evalBoolean(expression Expression, a, b pyNum) Expression {
	c := a.cmp(b)
	switch nodeClassKey(expression) {
	case "eq", "is":
		return booleanLiteral(c == 0)
	case "neq":
		return booleanLiteral(c != 0)
	case "gt":
		return booleanLiteral(c > 0)
	case "gte":
		return booleanLiteral(c >= 0)
	case "lt":
		return booleanLiteral(c < 0)
	case "lte":
		return booleanLiteral(c <= 0)
	}
	return nil
}

// evalBooleanStr corresponds to the string branch of eval_boolean.
func evalBooleanStr(expression Expression, a, b string) Expression {
	c := strings.Compare(a, b)
	switch nodeClassKey(expression) {
	case "eq", "is":
		return booleanLiteral(c == 0)
	case "neq":
		return booleanLiteral(c != 0)
	case "gt":
		return booleanLiteral(c > 0)
	case "gte":
		return booleanLiteral(c >= 0)
	case "lt":
		return booleanLiteral(c < 0)
	case "lte":
		return booleanLiteral(c <= 0)
	}
	return nil
}

// evalBooleanDate corresponds to the date branch of eval_boolean (None means it cannot be compared).
func evalBooleanDate(expression Expression, a, b *sqlDateVal) Expression {
	if a == nil || b == nil {
		return nil
	}
	da, db := a.asDateTime(), b.asDateTime()
	c := da.compare(db)
	switch nodeClassKey(expression) {
	case "eq", "is":
		return booleanLiteral(c == 0)
	case "neq":
		return booleanLiteral(c != 0)
	case "gt":
		return booleanLiteral(c > 0)
	case "gte":
		return booleanLiteral(c >= 0)
	case "lt":
		return booleanLiteral(c < 0)
	case "lte":
		return booleanLiteral(c <= 0)
	}
	return nil
}

// booleanLiteral corresponds to boolean_literal.
func booleanLiteral(condition bool) Expression {
	if condition {
		return trueExpr()
	}
	return falseExpr()
}

// ---- propagate_constants ----

// propagateConstants corresponds to propagate_constants: propagate constants within a DNF conjunction.
//
//	SELECT * FROM t WHERE a = b AND b = 5  ->  ... WHERE a = 5 AND b = 5
func propagateConstants(expression Expression, root bool) Expression {
	b, ok := expression.(*Binary)
	if !ok || b.Op != "AND" {
		return expression
	}
	if !(root || !sameParent(expression)) {
		return expression
	}
	if !normalizedExpr(expression, true) {
		return expression
	}

	type constEntry struct {
		id  Expression
		val Expression
	}
	mapping := map[string]constEntry{}

	for _, expr := range walkInScopePruned(expression, true, func(n Expression) bool {
		return isIfFunc(n)
	}) {
		eq, isEq := expr.(*Binary)
		if !isEq || eq.Op != "=" {
			continue
		}
		l, r := eq.Left, eq.Right
		if _, isCol := l.(*Column); !isCol {
			continue
		}
		if _, isLit := r.(*Literal); !isLit {
			continue
		}
		mapping[structKey(l)] = constEntry{id: l, val: r}
	}

	if len(mapping) == 0 {
		return expression
	}

	for _, column := range findAllInScope(expression, true, func(n Expression) bool {
		_, isCol := n.(*Column)
		return isCol
	}) {
		parent := column.Parent()
		entry, has := mapping[structKey(column)]
		if !has || entry.id == column {
			continue
		}
		// `not (isinstance(parent, exp.Is) and isinstance(parent.expression, exp.Null))`
		if isNode, isIs := parent.(*Is); isIs && isNullNode(isNode.Expr) {
			continue
		}
		Replace(column, entry.val.Copy())
	}

	return expression
}

// walkInScopePruned corresponds to walk_in_scope(expression, bfs, prune);
// rebuilt here because scope.go's walkInScope lacks a custom prune. Root is
// never pruned; descent stops at scope boundaries.
func walkInScopePruned(expression Expression, bfs bool, prune func(Expression) bool) []Expression {
	var out []Expression
	var queue, stack []Expression
	if bfs {
		queue = []Expression{expression}
	} else {
		stack = []Expression{expression}
	}
	pop := func() (Expression, bool) {
		if bfs {
			if len(queue) == 0 {
				return nil, false
			}
			n := queue[0]
			queue = queue[1:]
			return n, true
		}
		if len(stack) == 0 {
			return nil, false
		}
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		return n, true
	}
	push := func(ch []Expression) {
		if bfs {
			queue = append(queue, ch...)
			return
		}
		for i := len(ch) - 1; i >= 0; i-- {
			stack = append(stack, ch[i])
		}
	}

	for {
		node, ok := pop()
		if !ok {
			break
		}
		out = append(out, node)
		if node != expression {
			if (prune != nil && prune(node)) || isScopeBoundary(node) {
				continue
			}
		}
		push(childrenOf(node))
	}
	return out
}

// ---- Date/time infrastructure ----
// Minimal stand-ins for Python datetime + dateutil.relativedelta:
// sqlDateVal (date/datetime) and sqlDelta (months/days/seconds).

// sqlDateVal represents a date or datetime value (TZ is ignored, matching sqlglot's fromisoformat).
type sqlDateVal struct {
	y, m, d    int
	hasTime    bool
	h, mi, sec int
	micro      int
}

// asDateTime corresponds to cast_as_datetime (date -> datetime(00:00:00)).
func (v *sqlDateVal) asDateTime() *sqlDateVal {
	if v == nil {
		return nil
	}
	c := *v
	c.hasTime = true
	c.h = 0
	c.mi = 0
	c.sec = 0
	c.micro = 0
	return &c
}

func (v *sqlDateVal) compare(o *sqlDateVal) int {
	a, b := v.asDateTime(), o.asDateTime()
	if a.y != b.y {
		return sign(a.y - b.y)
	}
	if a.m != b.m {
		return sign(a.m - b.m)
	}
	if a.d != b.d {
		return sign(a.d - b.d)
	}
	if a.h != b.h {
		return sign(a.h - b.h)
	}
	if a.mi != b.mi {
		return sign(a.mi - b.mi)
	}
	if a.sec != b.sec {
		return sign(a.sec - b.sec)
	}
	return sign(a.micro - b.micro)
}

func sign(x int) int {
	if x < 0 {
		return -1
	}
	if x > 0 {
		return 1
	}
	return 0
}

// pyStr corresponds to Python's str(date) / str(datetime).
func (v *sqlDateVal) pyStr() string {
	if !v.hasTime {
		return pad(v.y, 4) + "-" + pad(v.m, 2) + "-" + pad(v.d, 2)
	}
	s := pad(v.y, 4) + "-" + pad(v.m, 2) + "-" + pad(v.d, 2) +
		" " + pad(v.h, 2) + ":" + pad(v.mi, 2) + ":" + pad(v.sec, 2)
	if v.micro != 0 {
		s += "." + pad(v.micro, 6)
	}
	return s
}

func pad(n, width int) string {
	s := strconv.Itoa(n)
	for len(s) < width {
		s = "0" + s
	}
	return s
}

// sqlDelta corresponds to the (months, days, seconds) decomposition of dateutil.relativedelta.
type sqlDelta struct {
	months  int
	days    int
	seconds int
}

func (d *sqlDelta) isZero() bool {
	return d == nil || (d.months == 0 && d.days == 0 && d.seconds == 0)
}

// add corresponds to Python's `date + relativedelta` (months added first, clamped to month-end).
func (v *sqlDateVal) add(d *sqlDelta) *sqlDateVal { return d.addToDate(v) }

// sub corresponds to Python's `date - relativedelta`.
func (v *sqlDateVal) sub(d *sqlDelta) *sqlDateVal { return applyDelta(v, d, -1) }

func (d *sqlDelta) addToDate(v *sqlDateVal) *sqlDateVal {
	return applyDelta(v, d, 1)
}

func (d *sqlDelta) subFromDate(v *sqlDateVal) *sqlDateVal {
	return applyDelta(v, d, -1)
}

// interval corresponds to the module-level interval(unit, n=1): an unsupported unit returns nil (Python raises UnsupportedUnit).
func makeInterval(unit string, n int) *sqlDelta {
	switch unit {
	case "year":
		return &sqlDelta{months: 12 * n}
	case "quarter":
		return &sqlDelta{months: 3 * n}
	case "month":
		return &sqlDelta{months: 1 * n}
	case "week":
		return &sqlDelta{days: 7 * n}
	case "day":
		return &sqlDelta{days: 1 * n}
	case "hour":
		return &sqlDelta{seconds: 3600 * n}
	case "minute":
		return &sqlDelta{seconds: 60 * n}
	case "second":
		return &sqlDelta{seconds: n}
	}
	return nil
}

// extractInterval corresponds to extract_interval.
func extractInterval(expression Expression) *sqlDelta {
	iv, ok := expression.(*Interval)
	if !ok {
		return nil
	}
	n, err := strconv.Atoi(eText(iv.Value))
	if err != nil {
		return nil
	}
	return makeInterval(strings.ToLower(iv.Unit), n)
}

// extractDate corresponds to extract_date: recognizes only Cast and
// TsOrDsToDate-without-format; recurses only into those, else nil.
func extractDate(cast Expression) *sqlDateVal {
	var toName string
	var value string
	hasValue := false

	switch c := cast.(type) {
	case *Cast:
		if c.To == nil {
			return nil
		}
		toName = c.To.Name
		switch t := c.This.(type) {
		case *Literal:
			value, hasValue = t.Value, true
		case *Cast, *TypedLiteral:
			inner := extractDate(c.This)
			if inner == nil {
				return nil
			}
			value, hasValue = inner.pyStr(), true
		default:
			if f, ok := c.This.(*Func); ok && nodeClassKey(f) == "tsordstodate" &&
				!hasFuncArg(f, "format") {
				inner := extractDate(c.This)
				if inner == nil {
					return nil
				}
				value, hasValue = inner.pyStr(), true
			} else {
				return nil
			}
		}
	case *TypedLiteral:
		// sqlg parses `DATE '...'` as TypedLiteral; handle with Cast semantics.
		return castValueAsDate(c.Value, c.Kind)
	case *Func:
		if nodeClassKey(c) != "tsordstodate" || hasFuncArg(c, "format") {
			return nil
		}
		toName = "DATE"
		switch t := firstFuncArg(c).(type) {
		case *Literal:
			value, hasValue = t.Value, true
		default:
			return nil
		}
	default:
		return nil
	}

	if !hasValue {
		return nil
	}
	return castValueAsDate(value, toName)
}

// hasFuncArg reports whether a *Func carries an optional sqlglot arg; always
// false since sqlg's parser never produces TsOrDsToDate's format arg.
func hasFuncArg(*Func, string) bool { return false }

func firstFuncArg(f *Func) Expression {
	if len(f.Args) > 0 {
		return f.Args[0]
	}
	return nil
}

// castValueAsDate corresponds to cast_value: DATE via cast_as_date (drops the
// time part), other temporal types via cast_as_datetime.
func castValueAsDate(value string, typeName string) *sqlDateVal {
	if value == "" {
		// Python's `if not value: return None`
		return nil
	}
	if typeName == "DATE" || typeName == "DATE32" {
		v := parseISODateTime(value)
		if v == nil {
			return nil
		}
		// cast_as_date drops the time part
		if v.hasTime {
			return &sqlDateVal{y: v.y, m: v.m, d: v.d}
		}
		return v
	}
	if dtTemporal[typeName] {
		return parseISODateTime(value)
	}
	return nil
}

// dateLiteral corresponds to date_literal: CAST('...' AS DATE/DATETIME).
func dateLiteral(v *sqlDateVal, targetType *DataType) Expression {
	name := ""
	if targetType != nil && dtTemporal[targetType.Name] {
		name = targetType.Name
	} else if v.hasTime {
		name = "DATETIME"
	} else {
		name = "DATE"
	}
	return tagKids(&Cast{This: Str(v.pyStr()), To: &DataType{Name: name}})
}

// dateRange corresponds to Python's DateRange = Tuple[date, date] (absence is indicated by ok).
type dateRange [2]*sqlDateVal

// datetruncRange corresponds to _datetrunc_range.
func datetruncRange(date *sqlDateVal, unit string, d *Dialect) (dateRange, bool) {
	floor := dateFloor(date, unit, d)
	if floor == nil {
		return dateRange{}, false
	}
	if date.compare(floor) != 0 {
		// always False (except for NULL)
		return dateRange{}, false
	}
	delta := makeInterval(unit, 1)
	if delta == nil {
		return dateRange{}, false
	}
	return dateRange{floor, delta.addToDate(floor)}, true
}

// datetruncEqExpression corresponds to _datetrunc_eq_expression; left must be
// Copied per comparison (Python's _binop copies).
func datetruncEqExpression(left Expression, dr dateRange, targetType *DataType) Expression {
	return combineWith([]Expression{
		tagKids(&Binary{Op: ">=", Left: left.Copy(), Right: dateLiteral(dr[0], targetType)}),
		tagKids(&Binary{Op: "<", Left: left.Copy(), Right: dateLiteral(dr[1], targetType)}),
	}, "AND")
}

// datetruncBinary corresponds to the DATETRUNC_BINARY_COMPARISONS lambdas.
func datetruncBinary(
	comparison string, left Expression, date *sqlDateVal, unit string,
	d *Dialect, targetType *DataType,
) Expression {
	floor := dateFloor(date, unit, d)
	if floor == nil {
		return nil
	}
	one := makeInterval(unit, 1)
	if one == nil {
		return nil
	}
	switch comparison {
	case "lt":
		target := floor
		if date.compare(floor) != 0 {
			target = one.addToDate(floor)
		}
		return tagKids(&Binary{Op: "<", Left: left.Copy(), Right: dateLiteral(target, targetType)})
	case "gt":
		return tagKids(&Binary{Op: ">=", Left: left.Copy(), Right: dateLiteral(one.addToDate(floor), targetType)})
	case "lte":
		return tagKids(&Binary{Op: "<", Left: left.Copy(), Right: dateLiteral(one.addToDate(floor), targetType)})
	case "gte":
		return tagKids(&Binary{Op: ">=", Left: left.Copy(), Right: dateLiteral(dateCeil(date, unit, d), targetType)})
	case "eq":
		dr, ok := datetruncRange(date, unit, d)
		if !ok {
			return nil
		}
		return datetruncEqExpression(left, dr, targetType)
	case "neq":
		dr, ok := datetruncRange(date, unit, d)
		if !ok {
			return nil
		}
		return combineWith([]Expression{
			tagKids(&Binary{Op: "<", Left: left.Copy(), Right: dateLiteral(dr[0], targetType)}),
			tagKids(&Binary{Op: ">=", Left: left.Copy(), Right: dateLiteral(dr[1], targetType)}),
		}, "AND")
	}
	return nil
}

// dateFloor corresponds to date_floor.
func dateFloor(v *sqlDateVal, unit string, d *Dialect) *sqlDateVal {
	c := *v
	switch unit {
	case "year":
		c.m, c.d = 1, 1
		return &c
	case "quarter":
		switch {
		case v.m <= 3:
			c.m, c.d = 1, 1
		case v.m <= 6:
			c.m, c.d = 4, 1
		case v.m <= 9:
			c.m, c.d = 7, 1
		default:
			c.m, c.d = 10, 1
		}
		return &c
	case "month":
		c.d = 1
		return &c
	case "week":
		// Monday is the start of the week (0)
		wd := weekdayOf(v)
		diff := wd - weekOffset(d)
		return addDays(&c, -diff)
	case "day":
		return &c
	}
	return nil
}

// dateCeil corresponds to date_ceil.
func dateCeil(v *sqlDateVal, unit string, d *Dialect) *sqlDateVal {
	floor := dateFloor(v, unit, d)
	if floor == nil {
		return nil
	}
	if floor.compare(v) == 0 {
		return v
	}
	delta := makeInterval(unit, 1)
	if delta == nil {
		return nil
	}
	return delta.addToDate(floor)
}

// mergeRanges corresponds to sqlglot.helper.merge_ranges.
func mergeRanges(ranges []dateRange) []dateRange {
	if len(ranges) == 0 {
		return nil
	}
	cp := append([]dateRange(nil), ranges...)
	sort.SliceStable(cp, func(i, j int) bool {
		return cp[i][0].compare(cp[j][0]) < 0
	})
	merged := []dateRange{cp[0]}
	for _, r := range cp[1:] {
		last := merged[len(merged)-1]
		// Ranges [a, b) and [c, d): they can be merged when c <= b
		if r[0].compare(last[1]) <= 0 {
			if r[1].compare(last[1]) > 0 {
				merged[len(merged)-1] = dateRange{last[0], r[1]}
			}
			continue
		}
		merged = append(merged, r)
	}
	return merged
}

// applyDelta applies a (months/days/seconds) delta to a date (sign 1 means add, -1 means subtract).
func applyDelta(v *sqlDateVal, d *sqlDelta, sign int) *sqlDateVal {
	if v == nil || d == nil {
		return v
	}
	c := *v
	if d.months != 0 {
		total := c.y*12 + (c.m - 1) + sign*d.months
		ny := total / 12
		nm := total%12 + 1
		if total < 0 && total%12 != 0 {
			ny = total/12 - 1
			nm = total - ny*12 + 1
		}
		c.y, c.m = ny, nm
		if c.d > daysInMonth(c.y, c.m) {
			c.d = daysInMonth(c.y, c.m)
		}
	}
	seconds := sign * (d.days*86400 + d.seconds)
	if seconds != 0 {
		t := toUnix(&c) + int64(seconds)
		c = *fromUnix(t)
		// relativedelta preserves date vs datetime; fromUnix always adds a time
		// part, so restore the original type.
		if !v.hasTime {
			c = *c.dateOnly()
		}
	}
	return &c
}

// dateOnly returns the same "date" value but downgrades the type to date (corresponding to Python's distinction between date and datetime).
func (v *sqlDateVal) dateOnly() *sqlDateVal {
	c := *v
	c.hasTime = false
	c.h, c.mi, c.sec, c.micro = 0, 0, 0, 0
	return &c
}

// addDays adds/subtracts days on a date/datetime (preserving the time part and type).
func addDays(v *sqlDateVal, days int) *sqlDateVal {
	if days == 0 {
		return v
	}
	c := *v
	t := toUnix(&c) + int64(days)*86400
	out := fromUnix(t)
	if !v.hasTime {
		out = out.dateOnly()
	}
	return out
}

// weekdayOf corresponds to Python date.weekday(): Monday = 0.
func weekdayOf(v *sqlDateVal) int {
	// use a variant of Zeller's formula (with 1970-01-01 as Thursday)
	days := daysFromCivil(v.y, v.m, v.d)
	// 1970-01-01 is Thursday -> weekday 3
	w := (days + 3) % 7
	if w < 0 {
		w += 7
	}
	return int(w)
}

func isLeap(y int) bool { return y%4 == 0 && (y%100 != 0 || y%400 == 0) }

func daysInMonth(y, m int) int {
	switch m {
	case 1, 3, 5, 7, 8, 10, 12:
		return 31
	case 4, 6, 9, 11:
		return 30
	}
	if isLeap(y) {
		return 29
	}
	return 28
}

// daysFromCivil returns the number of days since 1970-01-01 (Howard Hinnant's algorithm).
func daysFromCivil(y, m, d int) int64 {
	if m <= 2 {
		y--
	}
	era := y / 400
	if y < 0 && y%400 != 0 {
		era--
	}
	yoe := y - era*400
	mp := (m + 9) % 12
	doy := (153*mp+2)/5 + d - 1
	doe := yoe*365 + yoe/4 - yoe/100 + doy
	return int64(era)*146097 + int64(doe) - 719468
}

func civilFromDays(z int64) (int, int, int) {
	z += 719468
	era := z / 146097
	if z < 0 && z%146097 != 0 {
		era--
	}
	doe := z - era*146097
	yoe := (doe - doe/1460 + doe/36524 - doe/146096) / 365
	y := int(yoe) + int(era)*400
	doy := doe - (365*yoe + yoe/4 - yoe/100)
	mp := (5*doy + 2) / 153
	d := int(doy - (153*mp+2)/5 + 1)
	m := int(mp) + 3
	if mp >= 10 {
		m = int(mp) - 9
	}
	if m <= 2 {
		y++
	}
	return y, m, d
}

func toUnix(v *sqlDateVal) int64 {
	days := daysFromCivil(v.y, v.m, v.d)
	secs := int64(days)*86400 + int64(v.h)*3600 + int64(v.mi)*60 + int64(v.sec)
	return secs
}

func fromUnix(t int64) *sqlDateVal {
	days := t / 86400
	rem := t % 86400
	if rem < 0 {
		rem += 86400
		days--
	}
	y, m, d := civilFromDays(days)
	return &sqlDateVal{
		y: y, m: m, d: d, hasTime: true,
		h: int(rem / 3600), mi: int((rem % 3600) / 60), sec: int(rem % 60),
	}
}

// parseISODate corresponds to datetime.date.fromisoformat (returns nil on failure).
func parseISODate(s string) *sqlDateVal {
	v := parseISODateTime(s)
	if v == nil {
		return nil
	}
	if v.hasTime {
		// date.fromisoformat only accepts a pure date
		return nil
	}
	return v
}

// parseISODateTime corresponds to datetime.datetime.fromisoformat (covers common ISO 8601 forms).
func parseISODateTime(s string) *sqlDateVal {
	s = strings.TrimSpace(s)
	if len(s) < 8 {
		return nil
	}
	// strip the timezone suffix (Z / ±HH:MM)
	if i := strings.IndexAny(s, "Zz"); i > 0 {
		s = s[:i]
	}
	for i := 10; i < len(s); i++ {
		if (s[i] == '+' || s[i] == '-') && i > 10 {
			s = s[:i]
			break
		}
	}

	// separator: 'T' or ' '
	sep := -1
	for i := 0; i < len(s); i++ {
		if s[i] == 'T' || s[i] == 't' || s[i] == ' ' {
			sep = i
			break
		}
	}
	datePart := s
	timePart := ""
	if sep >= 0 {
		datePart = s[:sep]
		timePart = s[sep+1:]
	}

	// all-digit compact form YYYYMMDD
	if len(datePart) == 8 && !strings.Contains(datePart, "-") {
		y, err1 := strconv.Atoi(datePart[0:4])
		m, err2 := strconv.Atoi(datePart[4:6])
		d, err3 := strconv.Atoi(datePart[6:8])
		if err1 != nil || err2 != nil || err3 != nil {
			return nil
		}
		return &sqlDateVal{y: y, m: m, d: d}
	}

	parts := strings.Split(datePart, "-")
	if len(parts) != 3 {
		return nil
	}
	y, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	d, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return nil
	}
	if m < 1 || m > 12 || d < 1 || d > daysInMonth(y, m) {
		return nil
	}
	v := &sqlDateVal{y: y, m: m, d: d}
	if timePart == "" {
		return v
	}

	v.hasTime = true
	frac := ""
	if i := strings.IndexByte(timePart, '.'); i >= 0 {
		frac = timePart[i+1:]
		timePart = timePart[:i]
	}
	// compact form HHMMSS
	if len(timePart) == 6 && !strings.Contains(timePart, ":") {
		h, e1 := strconv.Atoi(timePart[0:2])
		mi, e2 := strconv.Atoi(timePart[2:4])
		se, e3 := strconv.Atoi(timePart[4:6])
		if e1 != nil || e2 != nil || e3 != nil {
			return nil
		}
		v.h, v.mi, v.sec = h, mi, se
	} else {
		tp := strings.Split(timePart, ":")
		if len(tp) < 2 {
			return nil
		}
		h, e1 := strconv.Atoi(tp[0])
		mi, e2 := strconv.Atoi(tp[1])
		if e1 != nil || e2 != nil {
			return nil
		}
		se := 0
		if len(tp) > 2 {
			x, e3 := strconv.Atoi(tp[2])
			if e3 != nil {
				return nil
			}
			se = x
		}
		v.h, v.mi, v.sec = h, mi, se
	}
	if frac != "" {
		for len(frac) < 6 {
			frac += "0"
		}
		x, err := strconv.Atoi(frac[:6])
		if err != nil {
			return nil
		}
		v.micro = x
	}
	return v
}

// ---- Numbers: replicate Python's int / decimal.Decimal ----

// pyNum corresponds to `Literal.to_py()`: int, or Decimal stored as
// coeff * 10^exp so str() matches Python's Decimal.__str__ exactly.
type pyNum struct {
	isInt bool
	i     int64
	coeff *big.Int
	exp   int
}

func newIntNum(i int64) pyNum { return pyNum{isInt: true, i: i} }

func newDecNum(coeff *big.Int, exp int) pyNum {
	n := pyNum{coeff: coeff, exp: exp}
	n.normalizeToInt()
	return n
}

// normalizeToInt is a no-op: Python keeps Decimal results as Decimal even
// when integral.
func (n *pyNum) normalizeToInt() {}

func (n pyNum) isZero() bool {
	if n.isInt {
		return n.i == 0
	}
	return n.coeff.Sign() == 0
}

func (n pyNum) cmp(o pyNum) int {
	if n.isInt && o.isInt {
		return sign64(n.i - o.i)
	}
	a, b := n.asDec(), o.asDec()
	return a.Cmp(b)
}

func sign64(x int64) int {
	if x < 0 {
		return -1
	}
	if x > 0 {
		return 1
	}
	return 0
}

// asDec converts the number into an exact rational for comparison.
func (n pyNum) asDec() *big.Rat {
	if n.isInt {
		return new(big.Rat).SetInt64(n.i)
	}
	r := new(big.Rat).SetInt(n.coeff)
	if n.exp > 0 {
		r.Mul(r, new(big.Rat).SetInt(pow10(n.exp)))
	} else if n.exp < 0 {
		r.Quo(r, new(big.Rat).SetInt(pow10(-n.exp)))
	}
	return r
}

func pow10(k int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(k)), nil)
}

// decOf returns the decimal representation in (coeff, exp) form.
func (n pyNum) decOf() (*big.Int, int) {
	if n.isInt {
		return big.NewInt(n.i), 0
	}
	return n.coeff, n.exp
}

func (n pyNum) add(o pyNum) pyNum {
	ac, ae := n.decOf()
	bc, be := o.decOf()
	e := ae
	if be < e {
		e = be
	}
	ai := new(big.Int).Mul(ac, pow10(ae-e))
	bi := new(big.Int).Mul(bc, pow10(be-e))
	return newDecNum(new(big.Int).Add(ai, bi), e)
}

func (n pyNum) sub(o pyNum) pyNum {
	ac, ae := n.decOf()
	bc, be := o.decOf()
	e := ae
	if be < e {
		e = be
	}
	ai := new(big.Int).Mul(ac, pow10(ae-e))
	bi := new(big.Int).Mul(bc, pow10(be-e))
	return newDecNum(new(big.Int).Sub(ai, bi), e)
}

func (n pyNum) mul(o pyNum) pyNum {
	ac, ae := n.decOf()
	bc, be := o.decOf()
	return newDecNum(new(big.Int).Mul(ac, bc), ae+be)
}

// div corresponds to Decimal.__truediv__ (28 significant digits,
// round-half-to-even); only reached for same-parent Decimal operands.
func (n pyNum) div(o pyNum) pyNum {
	if o.isZero() {
		// Python raises DivisionByZero here.
		panic("division by zero")
	}
	r := new(big.Rat).Quo(n.asDec(), o.asDec())
	return ratToDecimal(r, 28)
}

// ratToDecimal rounds a rational to a decimal with sig significant digits.
func ratToDecimal(r *big.Rat, sig int) pyNum {
	if r.Sign() == 0 {
		return newDecNum(big.NewInt(0), 0)
	}
	neg := r.Sign() < 0
	r = new(big.Rat).Abs(r)

	// find n such that 10^(sig-1) <= r * 10^n < 10^sig
	num, den := r.Num(), r.Denom()
	// initial estimate: decimal exponent = floor(log10(num/den))
	exp := len(num.String()) - len(den.String())
	scale := func(k int) *big.Int {
		// round(r * 10^k)
		scaled := new(big.Rat).Mul(r, new(big.Rat).SetInt(pow10(k)))
		q := new(big.Int)
		m := new(big.Int)
		q.QuoRem(scaled.Num(), scaled.Denom(), m)
		// round half to even
		twice := new(big.Int).Lsh(m, 1)
		cmp := twice.Cmp(scaled.Denom())
		if cmp > 0 || (cmp == 0 && q.Bit(0) == 1) {
			q.Add(q, big.NewInt(1))
		}
		return q
	}
	digits := sig - 1 - exp
	coeff := scale(digits)
	// handle a digit-count change caused by carry
	if len(coeff.String()) > sig {
		coeff = scale(digits - 1)
		digits--
	}
	if neg {
		coeff.Neg(coeff)
	}
	return newDecNum(coeff, -digits)
}

// pyStr corresponds to Python's `str(Literal.to_py())`.
func (n pyNum) pyStr() string {
	if n.isInt {
		return strconv.FormatInt(n.i, 10)
	}
	return decimalStr(n.coeff, n.exp)
}

// decimalStr replicates decimal.Decimal.__str__.
func decimalStr(coeff *big.Int, exp int) string {
	sign := ""
	c := coeff
	if c.Sign() < 0 {
		sign = "-"
		c = new(big.Int).Neg(c)
	}
	digits := c.String()
	leftDigits := exp + len(digits)

	if exp <= 0 && leftDigits > -6 {
		dotplace := leftDigits
		if dotplace <= 0 {
			return sign + "0." + strings.Repeat("0", -dotplace) + digits
		}
		if dotplace >= len(digits) {
			return sign + digits + strings.Repeat("0", dotplace-len(digits))
		}
		return sign + digits[:dotplace] + "." + digits[dotplace:]
	}
	// scientific notation
	e := exp + len(digits) - 1
	tail := ""
	if len(digits) > 1 {
		tail = "." + digits[1:]
	}
	esign := "+"
	if e < 0 {
		esign = ""
	}
	return sign + digits[0:1] + tail + "E" + esign + strconv.Itoa(e)
}

// toPyNumber corresponds to `Literal.to_py()` (including the Neg branch).
func toPyNumber(e Expression) pyNum {
	switch n := e.(type) {
	case *Literal:
		if n.IsString {
			// string literals do not take part in numeric operations (is_number is False)
			return pyNum{}
		}
		return parseNumText(n.Value)
	case *Unary:
		if n.Op == "-" {
			v := toPyNumber(n.This)
			return v.neg()
		}
	}
	return pyNum{}
}

func (n pyNum) neg() pyNum {
	if n.isInt {
		return newIntNum(-n.i)
	}
	return newDecNum(new(big.Int).Neg(n.coeff), n.exp)
}

// parseNumText corresponds to falling back to `Decimal(self.this)` when `int(self.this)` fails.
func parseNumText(s string) pyNum {
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return newIntNum(i)
	}
	// Decimal(s): allow a leading +, a fractional part, and an exponent
	coeff, exp, ok := parseDecimalText(s)
	if !ok {
		return newIntNum(0)
	}
	return newDecNum(coeff, exp)
}

// parseDecimalText parses decimal text into (coeff, exp).
func parseDecimalText(s string) (*big.Int, int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, 0, false
	}
	neg := false
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		neg = true
		s = s[1:]
	}
	mant := s
	expPart := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mant = s[:i]
		x, err := strconv.Atoi(s[i+1:])
		if err != nil {
			return nil, 0, false
		}
		expPart = x
	}
	frac := 0
	if i := strings.IndexByte(mant, '.'); i >= 0 {
		frac = len(mant) - i - 1
		mant = mant[:i] + mant[i+1:]
	}
	if mant == "" {
		return nil, 0, false
	}
	c, ok := new(big.Int).SetString(mant, 10)
	if !ok {
		return nil, 0, false
	}
	if neg {
		c.Neg(c)
	}
	return c, expPart - frac, true
}

// numberLiteral corresponds to `exp.Literal.number(v)` (str(v) then used as a non-string literal).
func numberLiteral(n pyNum) Expression { return Num(n.pyStr()) }

// ============================================================================
// Gen: sqlglot's minimal SQL generator, replicated verbatim. Not a renderer;
// output serves only as the sort key for uniq_sort / sort_comparison.
// Quirks: like_sql uses " Like "; _function omits argument names; the fallback
// emits "KEY :arg,value..."; list expansion pops one trailing comma unconditionally.
// ============================================================================

// GenSQL corresponds to the module-level gen(expression, comments=False).
func GenSQL(e Expression) string { return (&genState{}).gen(e, false) }

type genState struct {
	stack []any
	sqls  []string
}

// gen corresponds to Gen.gen. Stack holds Expression / string / []any / nil.
func (g *genState) gen(expression Expression, comments bool) string {
	g.stack = []any{expression}
	g.sqls = g.sqls[:0]

	for len(g.stack) > 0 {
		node := g.stack[len(g.stack)-1]
		g.stack = g.stack[:len(g.stack)-1]

		switch v := node.(type) {
		case Expression:
			key := nodeClassKey(v)
			if !g.dispatch(v, key) {
				if vals, ok := genFuncValues(v); ok {
					g.function(v, vals)
				} else {
					up := classKeyword(key)
					if g.args(v, 0) {
						g.stack = append(g.stack, up+" ")
					} else {
						g.stack = append(g.stack, up)
					}
				}
			}
		case []any:
			for i := len(v) - 1; i >= 0; i-- {
				if !isNilAny(v[i]) {
					g.stack = append(g.stack, v[i], ",")
				}
			}
			if len(v) > 0 {
				// Corresponds to Python's `self.stack.pop()`: unconditionally pop one (the extra comma)
				g.stack = g.stack[:len(g.stack)-1]
			}
		case string:
			g.sqls = append(g.sqls, v)
		case nil:
			// In Python, None outputs nothing
		default:
			g.sqls = append(g.sqls, stringifyAny(v))
		}
	}
	return strings.Join(g.sqls, "")
}

// isNilAny handles the case where "an interface holds a typed nil pointer".
func isNilAny(x any) bool {
	if x == nil {
		return true
	}
	if e, ok := x.(Expression); ok {
		return isNilExpr(e)
	}
	return false
}

// classKeyword corresponds to the fallback branch's `node.key.upper()`.
func classKeyword(key string) string { return strings.ToUpper(key) }

// dispatch corresponds to the `{key}_sql` method dispatch on Gen. Returning false means there is no corresponding handler.
func (g *genState) dispatch(v Expression, key string) bool {
	switch key {
	case "add":
		g.binary(v, " + ")
	case "alias":
		a := v.(*Alias)
		g.stack = append(g.stack, a.Alias, " AS ", a.This)
	case "and":
		g.binary(v, " AND ")
	case "anonymous":
		f := v.(*Func)
		// Anonymous.this is the function name in its original spelling; Gen does .upper() (the Identifier branch also quotes it)
		name := strings.ToUpper(f.Name)
		vals := make([]any, 0, len(f.Args))
		for _, a := range f.Args {
			vals = append(vals, a)
		}
		g.stack = append(g.stack, ")", vals, "(", name)
	case "between":
		b := v.(*Between)
		g.stack = append(g.stack, b.High, " AND ", b.Low, " BETWEEN ", b.This)
	case "boolean":
		if v.(*Boolean).Value {
			g.stack = append(g.stack, "TRUE")
		} else {
			g.stack = append(g.stack, "FALSE")
		}
	case "bracket":
		// sqlglot's Bracket.expressions is a list; sqlg has only one Index, aligned with a single-element list
		br := v.(*Bracket)
		vals := []any{}
		if !isNilExpr(br.Index) {
			vals = append(vals, br.Index)
		}
		g.stack = append(g.stack, "]", vals, "[", br.This)
	case "column":
		c := v.(*Column)
		// Corresponds to `for p in reversed(e.parts): stack.extend((p, "."))` + `stack.pop()`
		parts := make([]any, 0, 2)
		if c.Table != nil {
			parts = append(parts, c.Table)
		}
		if c.Name != nil {
			parts = append(parts, c.Name)
		}
		for i := len(parts) - 1; i >= 0; i-- {
			g.stack = append(g.stack, parts[i], ".")
		}
		if len(parts) > 0 {
			g.stack = g.stack[:len(g.stack)-1]
		}
	case "datatype":
		// datatype_sql: `_args(e, 1)` then the bare type name (size lives in
		// expressions as DataTypeParam on the Python side).
		g.args(v, 1)
		base, _ := splitTypeSize(v.(*DataType).Name)
		g.stack = append(g.stack, base+" ")
	case "div":
		g.binary(v, " / ")
	case "dot":
		g.binary(v, ".")
	case "eq":
		g.binary(v, " = ")
	case "from":
		g.stack = append(g.stack, v.(*From2).This, "FROM ")
	case "gt":
		g.binary(v, " > ")
	case "gte":
		g.binary(v, " >= ")
	case "identifier":
		id := v.(*Ident)
		if id.Quoted {
			g.stack = append(g.stack, "\""+id.Name+"\"")
		} else {
			g.stack = append(g.stack, id.Name)
		}
	case "ilike":
		g.binary(v, " ILIKE ")
	case "in":
		in := v.(*In)
		g.stack = append(g.stack, ")")
		g.args(v, 1)
		g.stack = append(g.stack, "(", " IN ", in.This)
	case "intdiv":
		g.binary(v, " DIV ")
	case "is":
		g.binary(v, " IS ")
	case "like":
		g.binary(v, " Like ")
	case "literal":
		l := v.(*Literal)
		if l.IsString {
			g.stack = append(g.stack, "'"+l.Value+"'")
		} else {
			g.stack = append(g.stack, l.Value)
		}
	case "lt":
		g.binary(v, " < ")
	case "lte":
		g.binary(v, " <= ")
	case "mod":
		g.binary(v, " % ")
	case "mul":
		g.binary(v, " * ")
	case "neg":
		g.stack = append(g.stack, v.(*Unary).This, "-")
	case "neq":
		g.binary(v, " <> ")
	case "not":
		g.stack = append(g.stack, v.(*Unary).This, "NOT ")
	case "null":
		g.stack = append(g.stack, "NULL")
	case "or":
		g.binary(v, " OR ")
	case "paren":
		g.stack = append(g.stack, ")", v.(*Paren).This, "(")
	case "sub":
		g.binary(v, " - ")
	case "subquery":
		sq := v.(*Subquery)
		g.args(v, 2)
		if sq.Alias != nil {
			g.stack = append(g.stack, sq.Alias)
		}
		g.stack = append(g.stack, ")", sq.This, "(")
	case "table":
		t := v.(*Table)
		g.args(v, 4)
		if t.Alias != nil {
			g.stack = append(g.stack, t.Alias)
		}
		parts := make([]any, 0, len(t.Parts))
		for _, pid := range t.Parts {
			parts = append(parts, pid)
		}
		for i := len(parts) - 1; i >= 0; i-- {
			g.stack = append(g.stack, parts[i], ".")
		}
		if len(parts) > 0 {
			g.stack = g.stack[:len(g.stack)-1]
		}
	case "tablealias":
		ta := v.(*TableAlias)
		if len(ta.Columns) > 0 {
			cols := make([]any, 0, len(ta.Columns))
			for _, c := range ta.Columns {
				cols = append(cols, c)
			}
			g.stack = append(g.stack, ")", cols, "(")
		}
		g.stack = append(g.stack, ta.Name, " AS ")
	case "var":
		g.stack = append(g.stack, v.(*Var2).Value)
	default:
		return false
	}
	return true
}

// binary corresponds to Gen._binary: `stack.extend((e.expression, op, e.this))` (pop order this → op → expression).
func (g *genState) binary(v Expression, op string) {
	var left, right Expression
	switch n := v.(type) {
	case *Binary:
		left, right = n.Left, n.Right
	case *Is:
		left, right = n.This, n.Expr
	}
	g.stack = append(g.stack, right, op, left)
}

// genFuncValues gives the `list(e.args.values())` used by _function (no arg
// names). ok=false means the node is not a sqlglot Func and takes the fallback
// _args branch.
func genFuncValues(v Expression) ([]any, bool) {
	switch n := v.(type) {
	case *Func:
		vals := make([]any, 0, len(n.Args))
		for _, a := range n.Args {
			vals = append(vals, a)
		}
		return vals, true
	case *Cast:
		return []any{n.This, n.To}, true
	case *TypedLiteral:
		return []any{Str(n.Value), &DataType{Name: n.Kind}}, true
	case *Case:
		// the insertion order of args is this? → ifs → default?
		vals := []any{}
		if n.Operand != nil {
			vals = append(vals, n.Operand)
		}
		ws := make([]any, 0, len(n.Ifs))
		for _, w := range n.Ifs {
			// sqlglot's Case.ifs are exp.If; build the same "IF(cond,then)" text.
			ws = append(ws, "IF("+GenSQL(w.Cond)+","+GenSQL(w.Then)+")")
		}
		vals = append(vals, ws)
		if n.Default != nil {
			vals = append(vals, n.Default)
		}
		return vals, true
	}
	return nil, false
}

// function corresponds to Gen._function: `)` + args values + `(` + sql_name (**without argument names**).
func (g *genState) function(v Expression, vals []any) {
	g.stack = append(g.stack, ")", vals, "(", sqlNameOf(v))
}

// sqlNameOf corresponds to `e.sql_name()` (sql_names()[0]).
func sqlNameOf(v Expression) string {
	switch nodeClassKey(v) {
	case "datetrunc":
		return "DATE_TRUNC"
	case "timestamptrunc":
		return "TIMESTAMP_TRUNC"
	case "dateadd":
		return "DATE_ADD"
	case "datesub":
		return "DATE_SUB"
	case "datetimeadd":
		return "DATETIME_ADD"
	case "datetimesub":
		return "DATETIME_SUB"
	case "tsordstodate":
		return "TS_OR_DS_TO_DATE"
	case "startswith":
		return "STARTS_WITH"
	case "concatws":
		return "CONCAT_WS"
	case "coalesce":
		return "COALESCE"
	case "concat":
		return "CONCAT"
	case "if":
		return "IF"
	case "cast":
		if _, ok := v.(*TypedLiteral); ok {
			return "CAST"
		}
		return "CAST"
	case "case":
		return "CASE"
	}
	if f, ok := v.(*Func); ok {
		return strings.ToUpper(f.Name)
	}
	return strings.ToUpper(nodeClassKey(v))
}

// args corresponds to Gen._args(node, arg_index): push set args from
// arg_types[arg_index:] as `[":name", value]`; true when non-empty.
func (g *genState) args(v Expression, argIndex int) bool {
	kvs := genKVs(v, argIndex)
	if len(kvs) > 0 {
		g.stack = append(g.stack, kvs)
		return true
	}
	return false
}

// genKVs builds _args' kvs, each `[]any{":name", value}`. argIndex>0 is only
// aligned with sqlglot's arg_types for the call sites that need it
// (datatype:1 / in:1 / subquery:2 / table:4).
func genKVs(v Expression, argIndex int) []any {
	type kv struct {
		name string
		val  any
	}
	var pairs []kv

	switch n := v.(type) {
	case *Binary:
		pairs = []kv{{"this", n.Left}, {"expression", n.Right}}
	case *Is:
		pairs = []kv{{"this", n.This}, {"expression", n.Expr}}
	case *Between:
		pairs = []kv{{"this", n.This}, {"low", n.Low}, {"high", n.High}}
	case *In:
		// In.arg_types = {"this", "expressions", "query", ...}
		if argIndex <= 1 {
			vals := make([]any, 0, len(n.List))
			for _, e := range n.List {
				vals = append(vals, e)
			}
			pairs = append(pairs, kv{"expressions", vals})
			if n.Query != nil {
				pairs = append(pairs, kv{"query", n.Query})
			}
		}
		if argIndex == 0 {
			pairs = append([]kv{{"this", n.This}}, pairs...)
		}
	case *Unary:
		pairs = []kv{{"this", n.This}}
	case *Paren:
		pairs = []kv{{"this", n.This}}
	case *TypedLiteral:
		// TypedLiteral takes the fallback; give it Cast's arg_types.
		pairs = []kv{{"this", Str(n.Value)}, {"to", &DataType{Name: n.Kind}}}
	case *Cast:
		pairs = []kv{{"this", n.This}, {"to", n.To}}
	case *DataType:
		// DataType.arg_types = ("this", "expressions", "nested"). sqlg stores the
		// size in the name; rebuild the "DATATYPEPARAM :this,N" strings sqlglot's
		// fallback would emit.
		base, size := splitTypeSize(n.Name)
		pairs = []kv{{"this", base}}
		if size != "" {
			vals := []any{}
			for _, p := range strings.Split(size, ",") {
				vals = append(vals, "DATATYPEPARAM :this,"+strings.TrimSpace(p))
			}
			pairs = append(pairs, kv{"expressions", vals})
		}
		// nested is always False (in sqlglot it is always set, so Gen always outputs it)
		pairs = append(pairs, kv{"nested", false})
	case *When:
		pairs = []kv{{"this", n.Cond}, {"true", n.Then}}
	case *Interval:
		// Interval.arg_types = {"this": True, "unit": False}, where unit is a Var
		var unit any
		if n.Unit != "" {
			unit = &Var2{Value: n.Unit}
		}
		pairs = []kv{{"this", n.Value}, {"unit", unit}}
	case *Exists:
		pairs = []kv{{"this", n.This}}
	case *Distinct:
		pairs = []kv{{"expressions", []any{n.This}}}
	case *Subquery:
		// Call site uses _args(e, 2): this/alias are skipped.
		if argIndex == 0 {
			pairs = []kv{{"this", n.This}, {"alias", n.Alias}}
		}
	case *Column:
		pairs = []kv{{"this", n.Name}, {"table", n.Table}}
	case *Alias:
		pairs = []kv{{"this", n.This}, {"alias", n.Alias}}
	case *Ordered:
		pairs = []kv{{"this", n.This}}
		if n.Explicit {
			pairs = append(pairs, kv{"desc", n.Desc})
		}
		if n.NullsFirst != nil {
			pairs = append(pairs, kv{"nulls_first", *n.NullsFirst})
		}
	case *Order:
		vals := make([]any, 0, len(n.Expressions))
		for _, o := range n.Expressions {
			vals = append(vals, o)
		}
		pairs = []kv{{"expressions", vals}}
	case *Group:
		vals := make([]any, 0, len(n.Expressions))
		for _, e := range n.Expressions {
			vals = append(vals, e)
		}
		pairs = []kv{{"expressions", vals}}
	case *Case:
		if n.Operand != nil {
			pairs = append(pairs, kv{"this", n.Operand})
		}
		ws := make([]any, 0, len(n.Ifs))
		for _, w := range n.Ifs {
			ws = append(ws, w)
		}
		pairs = append(pairs, kv{"ifs", ws})
		if n.Default != nil {
			pairs = append(pairs, kv{"default", n.Default})
		}
	case *Bracket:
		vals := []any{}
		if !isNilExpr(n.Index) {
			vals = append(vals, n.Index)
		}
		pairs = []kv{{"this", n.This}, {"expressions", vals}}
	case *Literal:
		pairs = []kv{{"this", n.Value}, {"is_string", n.IsString}}
	case *Boolean:
		pairs = []kv{{"this", n.Value}}
	case *Ident:
		pairs = []kv{{"this", n.Name}, {"quoted", n.Quoted}}
	case *Table:
		// Table.arg_types' first 4 are this/alias/db/catalog; the call site is _args(e, 4)
		if argIndex == 0 {
			var last any
			if len(n.Parts) > 0 {
				last = n.Parts[len(n.Parts)-1]
			}
			pairs = []kv{{"this", last}, {"alias", n.Alias}}
		}
	case *TableAlias:
		var cols []any
		for _, c := range n.Columns {
			cols = append(cols, c)
		}
		pairs = []kv{{"this", n.Name}}
		if len(cols) > 0 {
			pairs = append(pairs, kv{"expressions", cols})
		}
	case *Star:
		// Star.arg_types has no required items, so it is usually empty
		return nil
	case *Null:
		return nil
	case *Select:
		return nil
	case *Union:
		return nil
	default:
		return nil
	}

	if argIndex > 0 {
		if argIndex >= len(pairs) {
			return nil
		}
		pairs = pairs[argIndex:]
	}

	out := make([]any, 0, len(pairs))
	for _, p := range pairs {
		if isNilAny(p.val) {
			continue
		}
		out = append(out, []any{":" + p.name, p.val})
	}
	return out
}

func stringifyAny(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	}
	return ""
}

// From2 is the From node needed by Gen's from_sql (sqlg does not produce From nodes; kept for the future).
type From2 struct {
	base
	This Expression
}

func (n *From2) Children() []Expression { return []Expression{n.This} }
func (n *From2) SetChildren(ch []Expression) {
	if len(ch) > 0 {
		n.This = ch[0]
	}
	retag(n, ch...)
}
func (n *From2) Copy() Expression {
	c := &From2{}
	if n.This != nil {
		c.This = n.This.Copy()
	}
	return c
}
func (n *From2) SQL(d *Dialect) string { return "FROM " + n.This.SQL(d) }
