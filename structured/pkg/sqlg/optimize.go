package sqlg

import (
	"strconv"
	"strings"
)

// Port of Python structured/core/sql_inspection/optimization.py: the rule
// pipeline skeleton of sqlglot.optimizer.optimize plus structured's four extra
// rewrite steps (set_limit / cte_to_subquery / optimize_in_limit_subquery /
// fix_missing_group_by_when_agg_func).

// OptCtx carries the named arguments optimize() passes to each rule
// (sqlglot's possible_kwargs: schema, dialect, isolate_tables=True,
// quote_identifiers=False, identify=False).
type OptCtx struct {
	Schema *MappingSchema
	// Dialect is the dialect used for parsing/normalization (corresponds to
	// optimize's dialect parameter).
	Dialect *Dialect
	// DialectName is the original dialect name (structured passes a string
	// rather than a Dialect object).
	DialectName string

	// IsolateTables corresponds to possible_kwargs["isolate_tables"] = True.
	IsolateTables bool
	// QuoteQualify corresponds to quote_identifiers=False passed to qualify().
	QuoteQualify bool
	// Identify corresponds to identify=False in **kwargs.
	Identify bool
}

// rule is a single optimization rule; name mirrors sqlglot's argspec-based
// identity so rules can be dropped by name.
type rule struct {
	name string
	fn   func(Expression, *OptCtx) Expression
}

// OptimizationRules corresponds to sqlglot.optimizer.optimizer.RULES (list order is execution order).
func OptimizationRules() []rule {
	return []rule{
		{"qualify", ruleQualify},
		{"pushdown_projections", rulePushdownProjections},
		{"normalize", ruleNormalize},
		{"unnest_subqueries", ruleUnnestSubqueries},
		{"pushdown_predicates", rulePushdownPredicates},
		{"optimize_joins", ruleOptimizeJoins},
		{"eliminate_subqueries", ruleEliminateSubqueries},
		{"merge_subqueries", ruleMergeSubqueries},
		{"eliminate_joins", ruleEliminateJoins},
		{"eliminate_ctes", ruleEliminateCTEs},
		{"quote_identifiers", ruleQuoteIdentifiers},
		{"annotate_types", ruleAnnotateTypes},
		{"canonicalize", ruleCanonicalize},
		{"simplify", ruleSimplify},
	}
}

// removeRule drops a rule by name (mirrors Python's rules.remove(...)).
func removeRule(rules []rule, name string) []rule {
	out := make([]rule, 0, len(rules))
	for _, r := range rules {
		if r.name != name {
			out = append(out, r)
		}
	}
	return out
}

// ruleNamed retrieves a rule by name (used by tests and when incrementally filling in rules).
func ruleNamed(name string) func(Expression, *OptCtx) Expression {
	for _, r := range OptimizationRules() {
		if r.name == name {
			return r.fn
		}
	}
	return nil
}

// CteToSubqueryRule / OptimizeInLimitSubqueryRule / FixGroupByRule are the
// pipeline wrapper names of the three rules added by structured.
const (
	nameCteToSubquery         = "cte_to_subquery"
	nameOptimizeInLimit       = "optimize_in_limit_subquery"
	nameFixMissingGroupBy     = "fix_missing_group_by_when_agg_func"
	namePassThroughExpression = "" // Kept: placeholder to avoid constant-list drift
)

func ruleCteToSubquery(e Expression, _ *OptCtx) Expression   { return CteToSubquery(e) }
func ruleOptimizeInLimit(e Expression, _ *OptCtx) Expression { return OptimizeInLimitSubquery(e) }
func ruleFixGroupBy(e Expression, _ *OptCtx) Expression      { return FixMissingGroupByWhenAggFunc(e) }

// optimizeRules mirrors sqlglot's optimize(): copy the input first, then apply each rule in order.
func optimizeRules(expression Expression, ctx *OptCtx, rules []rule) Expression {
	// After Copy(), parent pointers must be rebuilt (sqlglot maintains parents
	// during construction, so this holds naturally there).
	out := RefreshParents(expression.Copy())
	for _, r := range rules {
		out = r.fn(out, ctx)
	}
	return out
}

// ---- Rules not yet ported: currently identity transforms (keep the pipeline shape; fill in incrementally) ----

// rulePushdownProjections corresponds to pushdown_projections.
func rulePushdownProjections(e Expression, ctx *OptCtx) Expression {
	return PushdownProjections(e, ctx)
}

// ruleNormalize corresponds to normalize(expression, dnf=False, max_distance=128)
// (neither parameter is in possible_kwargs, so defaults are used).
func ruleNormalize(e Expression, _ *OptCtx) Expression {
	resetExprMeta()
	return Normalize(e, false, 128)
}

// ruleUnnestSubqueries corresponds to unnest_subqueries; the prune arg does not
// exist in 28.6.0 and is unused (true passed by convention).
func ruleUnnestSubqueries(e Expression, _ *OptCtx) Expression { return UnnestSubqueries(e, true) }

// rulePushdownPredicates corresponds to pushdown_predicates.
func rulePushdownPredicates(e Expression, ctx *OptCtx) Expression {
	return PushdownPredicates(e, ctx.Dialect)
}

// ruleOptimizeJoins corresponds to optimize_joins.
func ruleOptimizeJoins(e Expression, _ *OptCtx) Expression { return OptimizeJoins(e) }

// ruleEliminateSubqueries corresponds to eliminate_subqueries.
func ruleEliminateSubqueries(e Expression, _ *OptCtx) Expression {
	return EliminateSubqueries(e)
}

// ruleMergeSubqueries corresponds to merge_subqueries; leave_tables_isolated is
// not in possible_kwargs, so it is always the default False.
func ruleMergeSubqueries(e Expression, _ *OptCtx) Expression {
	return MergeSubqueries(e, false)
}

// ruleEliminateJoins corresponds to eliminate_joins.
func ruleEliminateJoins(e Expression, _ *OptCtx) Expression { return EliminateJoins(e) }

// ruleEliminateCTEs corresponds to eliminate_ctes.
func ruleEliminateCTEs(e Expression, _ *OptCtx) Expression { return EliminateCTEs(e) }

// ruleQuoteIdentifiers corresponds to qualify_columns.quote_identifiers.
//
// Note that optimize passes identify=False (only quote identifiers that "must be quoted").
func ruleQuoteIdentifiers(e Expression, ctx *OptCtx) Expression {
	return QuoteIdentifiers(e, ctx.Dialect, ctx.Identify)
}

// ruleAnnotateTypes corresponds to `annotate_types(expression, schema, dialect)`;
// overwrite_types takes its default True.
func ruleAnnotateTypes(e Expression, ctx *OptCtx) Expression {
	return AnnotateTypes(e, ctx.Schema, ctx.Dialect, true)
}

// ruleCanonicalize corresponds to canonicalize(expression, dialect=dialect).
// Ordering constraint: depends on the previous rule's type annotations.
func ruleCanonicalize(e Expression, ctx *OptCtx) Expression {
	return Canonicalize(e, ctx.Dialect)
}

// ruleSimplify corresponds to simplify(expression, dialect=dialect);
// constant_propagation / coalesce_simplification default to False.
func ruleSimplify(e Expression, ctx *OptCtx) Expression {
	resetExprMeta()
	return Simplify(e, ctx.Dialect, false, false)
}

// ---- Rewrite steps added by structured ----

// CteToSubquery corresponds to SQLOptimization.cte_to_subquery: removes CTEs in
// place, replacing references with subqueries (MySQL < 8.0). `cte.parent.pop()`
// detaches the whole With node, so no "WITH" prefix remains and later pops are no-ops.
func CteToSubquery(expression Expression) Expression {
	cteNames := map[string]Expression{}
	for _, cte := range FindAllOf[*CTE](expression, true) {
		// sqlg's CTE.This is a wrapped *Subquery (sqlglot holds the query body
		// directly); unnest to avoid rendering `((SELECT ...)) AS c`.
		cteNames[cteName(cte)] = unnest(cte.This)
		// Pop the CTE's parent (the With node); see CteToSubquery doc.
		if p := cte.Parent(); p != nil {
			Pop(p)
		}
	}
	if len(cteNames) == 0 {
		return expression
	}

	// Replace all references to CTEs with subqueries
	return Transform(expression, func(node Expression) Expression {
		t, ok := node.(*Table)
		if !ok {
			return node
		}
		sub, hit := cteNames[t.Name()]
		if !hit {
			return node
		}
		var alias *TableAlias
		if t.Alias != nil {
			alias = t.Alias.Copy().(*TableAlias)
		} else {
			alias = &TableAlias{Name: NewIdent(t.Name())}
		}
		sq := &Subquery{This: sub.Copy(), Alias: alias}
		retag(sq, sq.This, sq.Alias)
		return sq
	}, true)
}

// cteName corresponds to cte.alias (a TableAlias node) and returns its name text.
func cteName(cte *CTE) string {
	if cte.Alias == nil || cte.Alias.Name == nil {
		return ""
	}
	return cte.Alias.Name.Name
}

// OptimizeInLimitSubquery corresponds to SQLOptimization.optimize_in_limit_subquery:
// rewrites `x IN (SELECT ... LIMIT n)` into
// `x IN (SELECT <projections> FROM (SELECT ... LIMIT n) AS t)`.
func OptimizeInLimitSubquery(expression Expression) Expression {
	for _, inExpr := range FindAllOf[*In](expression, true) {
		if inExpr.Query == nil {
			continue
		}
		inner, ok := inExpr.Query.This.(*Select)
		if !ok || inner.From == nil {
			continue
		}
		if !HasNode(inner, func(n Expression) bool { _, isLimit := n.(*Limit); return isLimit }) {
			continue
		}

		var alias *TableAlias
		if t := tableOf(inner.From); t != nil && t.Alias != nil {
			alias = t.Alias.Copy().(*TableAlias)
		} else {
			alias = &TableAlias{Name: NewIdent("t")}
		}
		derived := &Subquery{This: inner.Copy(), Alias: alias}
		retag(derived, derived.This, derived.Alias)

		// Build the new SELECT t.id FROM (subquery) AS t
		newSelect := &Select{
			Expressions: copyExprs(inner.Expressions),
			From:        derived,
		}
		newSub := &Subquery{This: newSelect}
		retag(newSelect, append([]Expression{derived}, newSelect.Expressions...)...)
		retag(newSub, newSelect)

		inExpr.Query = newSub
		inExpr.SetParent(inExpr.Parent())
	}
	return expression
}

// copyExprs deep-copies a slice of expressions node by node.
func copyExprs(in []Expression) []Expression {
	out := make([]Expression, 0, len(in))
	for _, e := range in {
		if e == nil {
			out = append(out, nil)
			continue
		}
		out = append(out, e.Copy())
	}
	return out
}

// tableOf extracts the Table at the From position (returns nil for a Subquery, etc.).
func tableOf(e Expression) *Table {
	switch v := e.(type) {
	case *Table:
		return v
	case *Alias:
		if t, ok := v.This.(*Table); ok {
			return t
		}
	}
	return nil
}

// FixMissingGroupByWhenAggFunc corresponds to
// fix_missing_group_by_when_agg_func: `SELECT a, sum(b) FROM x` gains GROUP BY a.
// Python quirk preserved: group/order come from the rule's input node, not each
// found Select.
func FixMissingGroupByWhenAggFunc(expression Expression) Expression {
	rootSel, _ := expression.(*Select)

	for _, sel := range FindAllOf[*Select](expression, true) {
		selectAgg := false
		notAggQueryCols := newOrderedExprMap()
		groupCols := newOrderedExprMap()

		for _, col := range sel.Expressions {
			switch c := col.(type) {
			case *Column:
				notAggQueryCols.set(identText(c.Name), c)
			case *Func:
				if isAggFunc(c) {
					selectAgg = true
				}
			case *Alias:
				switch inner := c.This.(type) {
				case *Column:
					notAggQueryCols.set(identText(inner.Name), inner)
				case *Func:
					if isAggFunc(inner) {
						selectAgg = true
					}
				}
			}
		}

		// Note: this reads expression (the node passed into the rule), not sel.
		var rootGroup *Group
		var rootOrder *Order
		if rootSel != nil {
			rootGroup = rootSel.Group
			rootOrder = rootSel.Order
		}

		if rootGroup != nil {
			for _, col := range rootGroup.Expressions {
				groupCols.set(groupColKey(col), col)
			}
		}
		orderByAgg := false
		if rootOrder != nil {
			for _, oc := range rootOrder.Expressions {
				if oc.This == nil {
					continue
				}
				if f, ok := oc.This.(*Func); ok && isAggFunc(f) {
					orderByAgg = true
				}
			}
		}

		if groupCols.len() > 0 || (selectAgg && notAggQueryCols.len() > 0) || orderByAgg {
			for _, k := range notAggQueryCols.keys {
				if !groupCols.has(k) {
					groupCols.set(k, notAggQueryCols.get(k))
				}
			}
		}

		if groupCols.len() > 0 {
			g := &Group{Expressions: groupCols.values()}
			sel.Group = g
			retag(g, g.Expressions...)
		}
	}
	return expression
}

// orderedExprMap is an order-preserving string→Expression map, corresponding to Python's dict.
type orderedExprMap struct {
	keys []string
	m    map[string]Expression
}

func newOrderedExprMap() *orderedExprMap {
	return &orderedExprMap{m: map[string]Expression{}}
}

func (o *orderedExprMap) set(k string, v Expression) {
	if _, ok := o.m[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.m[k] = v
}

func (o *orderedExprMap) get(k string) Expression { return o.m[k] }
func (o *orderedExprMap) has(k string) bool       { _, ok := o.m[k]; return ok }
func (o *orderedExprMap) len() int                { return len(o.keys) }

func (o *orderedExprMap) values() []Expression {
	out := make([]Expression, 0, len(o.keys))
	for _, k := range o.keys {
		out = append(out, o.m[k])
	}
	return out
}

// keyOf corresponds to Python's `col.this.this` (the Column → Ident text).
func keyOf(e Expression) string {
	switch v := e.(type) {
	case *Column:
		return identText(v.Name)
	case *Alias:
		if c, ok := v.This.(*Column); ok {
			return identText(c.Name)
		}
	}
	return ""
}

// groupColKey corresponds to `col.this.this` in fix_missing_group_by's group
// branch. Python evaluates it unconditionally, so a Case without an operand
// raises AttributeError (bubbling to a 500) — the port must panic identically.
// Unmodeled node types are treated as Python's None.
func groupColKey(col Expression) string {
	switch v := col.(type) {
	case *Column:
		return identText(v.Name)
	case *Case:
		if v.Operand == nil {
			panic(&PythonError{
				Kind: "AttributeError",
				Msg:  "'NoneType' object has no attribute 'this'",
			})
		}
		// `CASE x WHEN ...`: col.this.this is an Identifier object, not a string.
		// Not ported (never occurs in the corpus): empty key, equivalent to Python
		// using the object as a dict key (never matches a projection name).
		return ""
	}
	return ""
}

func identText(i *Ident) string {
	if i == nil {
		return ""
	}
	return i.Name
}

// isAggFunc reports whether the function is an aggregate (corresponds to sqlglot's exp.AggFunc family).
func isAggFunc(f *Func) bool {
	switch strings.ToUpper(f.Name) {
	case "SUM", "AVG", "COUNT", "MIN", "MAX", "GROUP_CONCAT", "ARRAY_AGG",
		"STRING_AGG", "LISTAGG", "STDDEV", "STDDEV_POP", "STDDEV_SAMP",
		"VARIANCE", "VAR_POP", "VAR_SAMP", "ANY_VALUE", "BIT_AND", "BIT_OR",
		"BOOL_AND", "BOOL_OR", "LOGICAL_AND", "LOGICAL_OR", "MEDIAN",
		"PERCENTILE_CONT", "PERCENTILE_DISC", "APPROX_DISTINCT", "CORR",
		"COVAR_POP", "COVAR_SAMP", "JSON_ARRAYAGG", "JSON_OBJECTAGG",
		"COLLECT_LIST", "COLLECT_SET", "FIRST", "LAST", "MODE":
		return true
	}
	return false
}

// setLimit corresponds to SQLOptimization.set_limit. Difference: sqlg splits
// set-op trailing modifiers into unionTail, so a bare *Union root gets a
// unionTail wrapper to carry the LIMIT (rendering is identical).
func (o *SQLOptimization) setLimit(expression Expression, resultNumLimit int) Expression {
	limit := resultNumLimit
	if cur := existingLimitText(expression); cur != nil {
		if v, err := strconv.Atoi(*cur); err == nil && v < limit {
			limit = v
		}
	}
	lim := &Limit{Expression: Num(strconv.Itoa(limit))}

	switch v := expression.(type) {
	case *Select:
		v.Limit = lim
		lim.SetParent(v)
		return v
	case *Update:
		// Python's expression.set("limit", ...) works for any node with a limit arg.
		v.Limit = lim
		lim.SetParent(v)
		return v
	case *Delete:
		v.Limit = lim
		lim.SetParent(v)
		return v
	case *Show:
		// Show has a limit arg too (MySQL's SHOW ... LIMIT).
		v.Limit = lim
		lim.SetParent(v)
		return v
	case *unionTail:
		v.Limit = lim
		lim.SetParent(v)
		return v
	case *Union:
		tail := &unionTail{Union: v, Limit: lim}
		retag(tail, v, lim)
		return tail
	}
	return expression
}

// existingLimitText extracts the numeric text of the LIMIT already present at
// the top level of the expression (nil if none).
func existingLimitText(expression Expression) *string {
	var e Expression
	switch v := expression.(type) {
	case *Select:
		if v.Limit != nil {
			e = v.Limit.Expression
		}
	case *Update:
		if v.Limit != nil {
			e = v.Limit.Expression
		}
	case *Delete:
		if v.Limit != nil {
			e = v.Limit.Expression
		}
	case *Show:
		if v.Limit != nil {
			e = v.Limit.Expression
		}
	case *unionTail:
		if v.Limit != nil {
			e = v.Limit.Expression
		}
	}
	if lit, ok := e.(*Literal); ok {
		s := lit.Value
		return &s
	}
	return nil
}

// SQLOptimization corresponds to Python's SQLOptimization.
type SQLOptimization struct {
	Dialect        string
	DBMajorVersion int
}

// NewSQLOptimization corresponds to SQLOptimization(dialect, db_major_version).
func NewSQLOptimization(dialect string, dbMajorVersion int) *SQLOptimization {
	return &SQLOptimization{Dialect: dialect, DBMajorVersion: dbMajorVersion}
}

// Run corresponds to the "input is a SQL string" branch of arun. schema is
// structured's order-preserving {table: {col: type}} (nil = none); a nil
// resultNumLimit is Python's None.
func (o *SQLOptimization) Run(
	sql string, schema *SchemaInput, resultNumLimit *int,
) (string, error) {
	// Dialect not in DIALECT_MODULE_NAMES: return as-is, without even applying set_limit.
	if !HasDialect(o.Dialect) {
		return sql, nil
	}
	expression, err := o.parseForRun(sql)
	if err != nil {
		return "", err
	}
	return o.optimizeAndRender(expression, schema, resultNumLimit)
}

// parseForRun corresponds to the parsing and fallback of the SQL-string branch in arun:
// parse with the dialect first; on failure fall back to the default dialect
// (a parse failure there keeps propagating).
func (o *SQLOptimization) parseForRun(sql string) (Expression, error) {
	expression, err := ParseOne(sql, o.Dialect)
	if err != nil {
		// Corresponds to `except ParseError: parse_one(sql)` (fall back to the default dialect)
		expression, err = ParseOne(sql, "")
		if err != nil {
			return nil, err
		}
	}
	return expression, nil
}

// RunExpr corresponds to the "input is already an expression" branch of arun.
func (o *SQLOptimization) RunExpr(
	expression Expression, schema *SchemaInput, resultNumLimit *int,
) (string, error) {
	return o.optimizeAndRender(expression, schema, resultNumLimit)
}

// optimizeAndRender runs set_limit → rule pipeline → rendering. Panic/recover
// convention: rules panic(&OptimizeError/&SchemaError/&PythonError) for Python
// exceptions; they are recovered into returned errors, anything else re-panics.
func (o *SQLOptimization) optimizeAndRender(
	expression Expression, schema *SchemaInput, resultNumLimit *int,
) (out string, err error) {
	defer func() {
		if r := recover(); r != nil {
			// Only deliberate rule-raised semantic errors become errors;
			// anything else is a porting defect and must re-panic.
			switch e := r.(type) {
			case *OptimizeError:
				out, err = "", e
			case *SchemaError:
				out, err = "", e
			case *PythonError:
				out, err = "", e
			default:
				panic(r)
			}
		}
	}()

	if resultNumLimit != nil && *resultNumLimit > 0 {
		expression = o.setLimit(expression, *resultNumLimit)
	}

	rules := o.rulesFor()
	ctx, err := o.newOptCtx(schema)
	if err != nil {
		return "", err
	}
	result := optimizeRules(expression, ctx, rules)
	return result.SQL(GetDialect(o.Dialect)), nil
}

// rulesFor assembles the rule list: copy RULES; for mysql < 8 drop
// eliminate_subqueries and append cte_to_subquery; then structured's two rules.
func (o *SQLOptimization) rulesFor() []rule {
	rules := OptimizationRules()
	if o.Dialect == "mysql" && o.DBMajorVersion < 8 {
		// CTEs are only supported from MySQL 8.0 on; otherwise subqueries must be used
		rules = removeRule(rules, "eliminate_subqueries")
		rules = append(rules, rule{"cte_to_subquery", ruleCteToSubquery})
	}
	// Optimize IN ... LIMIT subqueries
	rules = append(rules, rule{nameOptimizeInLimit, ruleOptimizeInLimit})
	// For aggregate queries, fix columns missing from the SQL's GROUP BY
	rules = append(rules, rule{nameFixMissingGroupBy, ruleFixGroupBy})
	return rules
}

// newOptCtx builds the named arguments optimize() hands to each rule (corresponds to possible_kwargs).
func (o *SQLOptimization) newOptCtx(schema *SchemaInput) (*OptCtx, error) {
	ms, err := NewMappingSchema(schema, o.Dialect)
	if err != nil {
		return nil, err
	}
	return &OptCtx{
		Schema:        ms,
		Dialect:       GetDialect(o.Dialect),
		DialectName:   o.Dialect,
		IsolateTables: true,
		QuoteQualify:  false,
		Identify:      false,
	}, nil
}

// RuleStep is the single-step result of RunSteps.
type RuleStep struct {
	// Rule name; stepParse = initial state after parse+set_limit,
	// stepPassthrough = unrecognized dialect, SQL returned as-is (single step).
	Rule string
	// SQL rendered after this step (compact mode, same as arun's output).
	SQL string
	// Non-nil Err = this step raised a semantic error; later steps are skipped.
	Err error
}

const (
	stepParse       = "<parse>"
	stepPassthrough = "<passthrough>"
)

// RunSteps follows the same pipeline as Run but records the rendered result
// after each step. Diagnostics only: attributes divergence to the first rule
// that diverges. Each step re-renders, so rendering differences surface at
// their step too.
func (o *SQLOptimization) RunSteps(
	sql string, schema *SchemaInput, resultNumLimit *int,
) (steps []RuleStep, err error) {
	defer func() {
		if r := recover(); r != nil {
			// Same convention as optimizeAndRender; only err is set so the
			// already-collected steps are kept.
			switch e := r.(type) {
			case *OptimizeError:
				err = e
			case *SchemaError:
				err = e
			case *PythonError:
				err = e
			default:
				panic(r)
			}
		}
	}()

	if !HasDialect(o.Dialect) {
		return []RuleStep{{Rule: stepPassthrough, SQL: sql}}, nil
	}
	expression, err := o.parseForRun(sql)
	if err != nil {
		// Python still records a `<parse>` step (error only) before returning,
		// so step-by-step comparisons line up.
		return []RuleStep{{Rule: stepParse, Err: err}}, err
	}
	if resultNumLimit != nil && *resultNumLimit > 0 {
		expression = o.setLimit(expression, *resultNumLimit)
	}
	d := GetDialect(o.Dialect)

	// Copy semantics identical to optimizeRules: the whole pipeline copies only once.
	expression = RefreshParents(expression.Copy())
	steps = append(steps, RuleStep{Rule: stepParse, SQL: expression.SQL(d)})

	ctx, err := o.newOptCtx(schema)
	if err != nil {
		return steps, err
	}
	for _, r := range o.rulesFor() {
		step := RuleStep{Rule: r.name}
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					switch e := rec.(type) {
					case *OptimizeError:
						step.Err = e
					case *SchemaError:
						step.Err = e
					case *PythonError:
						step.Err = e
					default:
						panic(rec)
					}
				}
			}()
			expression = r.fn(expression, ctx)
			step.SQL = expression.SQL(d)
		}()
		steps = append(steps, step)
		if step.Err != nil {
			return steps, step.Err
		}
	}
	return steps, nil
}
