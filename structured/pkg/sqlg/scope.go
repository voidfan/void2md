package sqlg

// Port of sqlglot.optimizer.scope: slices the AST into nested scopes.
// Known divergence: UDTF/LATERAL/UNNEST/PIVOT/JoinHint/Dot/TableColumn/Window/
// Qualify/SEMI-ANTI JOIN syntax is never produced by sqlg, so those scope
// branches are treated as nonexistent (fill in if sqlg ever parses them).

// ScopeType corresponds to sqlglot's ScopeType.
type ScopeType int

const (
	ScopeRoot ScopeType = iota
	ScopeSubquery
	ScopeDerivedTable
	ScopeCTE
	ScopeUnion
	ScopeUDTF
)

// isQueryLike corresponds to exp.Query (Select / SetOperation / Subquery, etc.).
func isQueryLike(e Expression) bool {
	switch e.(type) {
	case *Select, *Union, *unionTail, *Subquery:
		return true
	}
	return false
}

// isUnwrappedQuery corresponds to exp.UNWRAPPED_QUERIES = (Select, SetOperation). Note that Subquery is not included.
func isUnwrappedQuery(e Expression) bool {
	switch e.(type) {
	case *Select, *Union:
		return true
	}
	return false
}

// isDDLLike corresponds to exp.DDL.
func isDDLLike(e Expression) bool {
	switch e.(type) {
	case *Create, *Drop:
		return true
	}
	return false
}

// isDMLLike corresponds to exp.DML.
func isDMLLike(e Expression) bool {
	switch e.(type) {
	case *Insert, *Update, *Delete:
		return true
	}
	return false
}

// isTraversable corresponds to TRAVERSABLES = (Query, DDL, DML).
func isTraversable(e Expression) bool {
	return isQueryLike(e) || isDDLLike(e) || isDMLLike(e)
}

// isDerivedTableNode corresponds to _is_derived_table: an aliased Subquery, or
// one wrapping an unwrapped query.
func isDerivedTableNode(e Expression) bool {
	sq, ok := e.(*Subquery)
	if !ok {
		return false
	}
	return sq.Alias != nil || isUnwrappedQuery(sq.This)
}

// isFromOrJoin corresponds to _is_from_or_join. sqlg has no exp.From wrapper:
// the criterion is "parent is a Join" or "parent is a Select and this child is
// exactly Select.From".
func isFromOrJoin(e Expression) bool {
	p := e.Parent()
	for {
		if _, ok := p.(*Subquery); ok {
			p = p.Parent()
			continue
		}
		break
	}
	switch v := p.(type) {
	case *Join:
		return true
	case *Select:
		return v.From == e
	}
	return false
}

// unnest corresponds to Expression.unnest: strips off the Subquery / Paren wrappers.
func unnest(e Expression) Expression {
	for e != nil {
		switch v := e.(type) {
		case *Subquery:
			e = v.This
		case *Paren:
			e = v.This
		default:
			return e
		}
	}
	return e
}

// sourceMap is an insertion-ordered sources map: rules like qualify_tables
// iterate sources in insertion order, which affects alias assignment and thus
// the final SQL.
type sourceMap struct {
	keys []string
	m    map[string]any // *Table or *Scope
}

func newSourceMap() *sourceMap { return &sourceMap{m: map[string]any{}} }

func (s *sourceMap) Has(k string) bool { _, ok := s.m[k]; return ok }
func (s *sourceMap) Get(k string) (any, bool) {
	v, ok := s.m[k]
	return v, ok
}
func (s *sourceMap) Set(k string, v any) {
	if _, ok := s.m[k]; !ok {
		s.keys = append(s.keys, k)
	}
	s.m[k] = v
}
func (s *sourceMap) Del(k string) {
	if _, ok := s.m[k]; !ok {
		return
	}
	delete(s.m, k)
	for i, kk := range s.keys {
		if kk == k {
			s.keys = append(s.keys[:i], s.keys[i+1:]...)
			break
		}
	}
}
func (s *sourceMap) Keys() []string { return append([]string(nil), s.keys...) }
func (s *sourceMap) Len() int       { return len(s.keys) }

// Rename corresponds to Scope.rename_source: pop (no error if missing) then
// set — a missing oldName still writes an empty value at newName, matching
// Python's key-order behavior.
func (s *sourceMap) Rename(oldName, newName string) {
	v := s.m[oldName]
	s.Del(oldName)
	s.Set(newName, v)
}

// Scope corresponds to sqlglot's Scope.
type Scope struct {
	Expression   Expression
	sources      *sourceMap
	OuterColumns []string
	Parent       *Scope
	ScopeType    ScopeType

	SubqueryScopes     []*Scope
	DerivedTableScopes []*Scope
	TableScopes        []*Scope
	CTEScopes          []*Scope
	UnionScopes        []*Scope

	CanBeCorrelated bool

	// ---- lazily collected caches ----
	collected         bool
	tables            []*Table
	ctes              []*CTE
	subqueries        []Expression
	derivedTables     []Expression
	rawColumns        []*Column
	stars             []Expression
	columnsCache      []*Column
	externalColsCache []*Column
	localColsCache    []*Column
	selectedSources   *selectedSources
	referencesCache   []sourceRef
}

// sourceRef corresponds to the (name, node) pair in references.
type sourceRef struct {
	Name string
	Node Expression
}

// selectedSources is an insertion-ordered selected_sources that also records duplicate aliases.
type selectedSources struct {
	keys []string
	m    map[string]selectedSource
	// When DuplicateAlias is non-empty it corresponds to the OptimizeError("Alias already used: ...") raised by Python
	DuplicateAlias string
}

type selectedSource struct {
	Node   Expression
	Source any
}

func (s *selectedSources) len() int { return len(s.keys) }

// NewScope corresponds to Scope.__init__.
func NewScope(expression Expression, parent *Scope, scopeType ScopeType) *Scope {
	return &Scope{
		Expression:      unnest(expression),
		sources:         newSourceMap(),
		Parent:          parent,
		ScopeType:       scopeType,
		CanBeCorrelated: parent != nil && (scopeType == ScopeSubquery || scopeType == ScopeUDTF),
	}
}

// branch corresponds to Scope.branch. cte_sources inheritance is approximated
// as "parent sources whose value is a *Scope" (CTE sources are always Scopes).
func (s *Scope) branch(expression Expression, scopeType ScopeType, outerColumns []string) *Scope {
	child := &Scope{
		Expression:      unnest(expression),
		sources:         newSourceMap(),
		OuterColumns:    outerColumns,
		Parent:          s,
		ScopeType:       scopeType,
		CanBeCorrelated: s.CanBeCorrelated || scopeType == ScopeSubquery || scopeType == ScopeUDTF,
	}
	for _, k := range s.sources.Keys() {
		v, _ := s.sources.Get(k)
		if _, isScope := v.(*Scope); isScope {
			child.sources.Set(k, v)
		}
	}
	return child
}

// branchWithCTEs corresponds to `parent.branch(expr, cte_sources=extra, scope_type=t)`:
// after branch, merge extra's *Scope sources to emulate cte_sources inheritance.
func branchWithCTEs(parent *Scope, expression Expression, t ScopeType, outerColumns []string, extra *sourceMap) *Scope {
	child := parent.branch(expression, t, outerColumns)
	if extra != nil {
		for _, k := range extra.Keys() {
			v, _ := extra.Get(k)
			if _, isScope := v.(*Scope); isScope {
				child.sources.Set(k, v)
			}
		}
	}
	return child
}

// clearCache corresponds to Scope.clear_cache.
func (s *Scope) clearCache() {
	s.collected = false
	s.tables = nil
	s.ctes = nil
	s.subqueries = nil
	s.derivedTables = nil
	s.rawColumns = nil
	s.stars = nil
	s.columnsCache = nil
	s.externalColsCache = nil
	s.localColsCache = nil
	s.selectedSources = nil
	s.referencesCache = nil
}

// collect corresponds to Scope._collect.
func (s *Scope) collect() {
	s.tables = nil
	s.ctes = nil
	s.subqueries = nil
	s.derivedTables = nil
	s.rawColumns = nil
	s.stars = nil

	for _, node := range walkInScope(s.Expression, false) {
		if node == s.Expression {
			continue
		}
		switch n := node.(type) {
		case *Star:
			s.stars = append(s.stars, n)
		case *Column:
			s.rawColumns = append(s.rawColumns, n)
		case *Table:
			s.tables = append(s.tables, n)
		case *CTE:
			s.ctes = append(s.ctes, n)
		default:
			if isDerivedTableNode(node) && isFromOrJoin(node) {
				s.derivedTables = append(s.derivedTables, node)
			} else if isUnwrappedQuery(node) {
				s.subqueries = append(s.subqueries, node)
			}
		}
	}
	s.collected = true
}

func (s *Scope) ensureCollected() {
	if !s.collected {
		s.collect()
	}
}

// walk corresponds to Scope.walk: does not cross scope boundaries.
func (s *Scope) walk(bfs bool) []Expression { return walkInScope(s.Expression, bfs) }

// Walk is the exported wrapper of Scope.walk.
func (s *Scope) Walk(bfs bool) []Expression { return s.walk(bfs) }

// FindAll corresponds to Scope.find_all (does not cross scope boundaries).
func (s *Scope) FindAll(bfs bool, pred func(Expression) bool) []Expression {
	var out []Expression
	for _, n := range s.walk(bfs) {
		if pred(n) {
			out = append(out, n)
		}
	}
	return out
}

// Tables corresponds to Scope.tables.
func (s *Scope) Tables() []*Table { s.ensureCollected(); return s.tables }

// CTEs corresponds to Scope.ctes.
func (s *Scope) CTEs() []*CTE { s.ensureCollected(); return s.ctes }

// DerivedTables corresponds to Scope.derived_tables.
func (s *Scope) DerivedTables() []Expression { s.ensureCollected(); return s.derivedTables }

// Subqueries corresponds to Scope.subqueries.
func (s *Scope) Subqueries() []Expression { s.ensureCollected(); return s.subqueries }

// Stars corresponds to Scope.stars.
func (s *Scope) Stars() []Expression { s.ensureCollected(); return s.stars }

// RawColumns corresponds to Scope._raw_columns.
func (s *Scope) RawColumns() []*Column { s.ensureCollected(); return s.rawColumns }

// Columns corresponds to Scope.columns.
func (s *Scope) Columns() []*Column {
	if s.columnsCache != nil {
		return s.columnsCache
	}
	s.ensureCollected()
	columns := append([]*Column(nil), s.rawColumns...)

	var external []*Column
	for _, sc := range s.SubqueryScopes {
		external = append(external, sc.ExternalColumns()...)
	}
	for _, sc := range s.DerivedTableScopes {
		if sc.CanBeCorrelated {
			external = append(external, sc.ExternalColumns()...)
		}
	}

	namedSelects := map[string]bool{}
	for _, n := range namedSelectsOf(s.Expression) {
		namedSelects[n] = true
	}

	out := make([]*Column, 0, len(columns)+len(external))
	for _, col := range append(columns, external...) {
		ancestor := col.findAncestor()
		if ancestor == nil ||
			col.Table != nil ||
			nodeIsSelect(ancestor) ||
			(nodeIsTable(ancestor) && !tableHoldsFunc(ancestor)) ||
			(nodeIsOrderOrDistinct(ancestor) &&
				(isWindowed(ancestor) ||
					!parentIsSelect(ancestor) ||
					!namedSelects[colName(col)])) ||
			nodeIsStar(ancestor) {
			out = append(out, col)
		}
	}
	s.columnsCache = out
	return out
}

func colName(c *Column) string {
	if c.Name == nil {
		return ""
	}
	return c.Name.Name
}

// findAncestor corresponds to column.find_ancestor(Select, Qualify, Order,
// Having, Hint, Table, Star, Distinct). sqlg has no Qualify/Hint/Having wrapper
// nodes; HAVING is detected by "the step reaching Select lands on its Having
// field". Load-bearing: Scope.columns excludes bare HAVING columns, so
// qualify_columns leaves them unqualified.
func (c *Column) findAncestor() Expression {
	child := Expression(c)
	for p := c.Parent(); p != nil; p = p.Parent() {
		switch v := p.(type) {
		case *Select:
			if v.Having != nil && v.Having == child {
				return v.Having
			}
			return v
		case *Order, *Table, *Star, *Distinct:
			return v
		}
		child = p
	}
	return nil
}

func nodeIsSelect(a Expression) bool {
	_, ok := a.(*Select)
	return ok
}
func nodeIsTable(a Expression) bool {
	_, ok := a.(*Table)
	return ok
}

// tableHoldsFunc corresponds to isinstance(ancestor.this, exp.Func).
// sqlg's Table.Parts only holds *Ident, never a function, so this is always false.
func tableHoldsFunc(Expression) bool { return false }

func nodeIsOrderOrDistinct(a Expression) bool {
	switch a.(type) {
	case *Order, *Distinct:
		return true
	}
	return false
}

// isWindowed corresponds to isinstance(ancestor.parent, (Window, WithinGroup)). This implementation has neither node.
func isWindowed(Expression) bool { return false }

func parentIsSelect(a Expression) bool {
	_, ok := a.Parent().(*Select)
	return ok
}

func nodeIsStar(a Expression) bool {
	_, ok := a.(*Star)
	return ok
}

// References corresponds to Scope.references.
func (s *Scope) References() []sourceRef {
	if s.referencesCache != nil {
		return s.referencesCache
	}
	var out []sourceRef
	for _, t := range s.Tables() {
		out = append(out, sourceRef{Name: t.AliasOrName(), Node: t})
	}
	for _, d := range s.DerivedTables() {
		out = append(out, sourceRef{Name: getSourceAlias(d), Node: unnest(d)})
	}
	s.referencesCache = out
	return out
}

// SelectedSources corresponds to Scope.selected_sources.
func (s *Scope) SelectedSources() *selectedSources {
	if s.selectedSources != nil {
		return s.selectedSources
	}
	res := &selectedSources{m: map[string]selectedSource{}}
	for _, ref := range s.References() {
		if _, dup := res.m[ref.Name]; dup {
			// Corresponds to Python's OptimizeError(f"Alias already used: {name}")
			// We record it here but do not panic; the caller checks DuplicateAlias.
			res.DuplicateAlias = ref.Name
			continue
		}
		if src, ok := s.sources.Get(ref.Name); ok {
			res.keys = append(res.keys, ref.Name)
			res.m[ref.Name] = selectedSource{Node: ref.Node, Source: src}
		}
	}
	s.selectedSources = res
	return res
}

// ExternalColumns corresponds to Scope.external_columns.
func (s *Scope) ExternalColumns() []*Column {
	if s.externalColsCache != nil {
		return s.externalColsCache
	}
	switch s.Expression.(type) {
	case *Union, *unionTail:
		if len(s.UnionScopes) == 2 {
			left, right := s.UnionScopes[0], s.UnionScopes[1]
			s.externalColsCache = append(left.ExternalColumns(), right.ExternalColumns()...)
			return s.externalColsCache
		}
	}
	var out []*Column
	for _, c := range s.Columns() {
		if c.Table == nil {
			out = append(out, c)
			continue
		}
		if !s.sources.Has(c.Table.Name) {
			out = append(out, c)
		}
	}
	s.externalColsCache = out
	return out
}

// LocalColumns corresponds to Scope.local_columns.
func (s *Scope) LocalColumns() []*Column {
	if s.localColsCache != nil {
		return s.localColsCache
	}
	ext := map[*Column]bool{}
	for _, c := range s.ExternalColumns() {
		ext[c] = true
	}
	var out []*Column
	for _, c := range s.Columns() {
		if !ext[c] {
			out = append(out, c)
		}
	}
	s.localColsCache = out
	return out
}

// UnqualifiedColumns corresponds to Scope.unqualified_columns.
func (s *Scope) UnqualifiedColumns() []*Column {
	var out []*Column
	for _, c := range s.Columns() {
		if c.Table == nil {
			out = append(out, c)
		}
	}
	return out
}

// IsCorrelatedSubquery corresponds to Scope.is_correlated_subquery.
func (s *Scope) IsCorrelatedSubquery() bool {
	return s.CanBeCorrelated && len(s.ExternalColumns()) > 0
}

// Traverse corresponds to Scope.traverse: DFS post-order.
func (s *Scope) Traverse() []*Scope {
	var result []*Scope
	stack := []*Scope{s}
	for len(stack) > 0 {
		sc := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		result = append(result, sc)
		stack = append(stack, sc.CTEScopes...)
		stack = append(stack, sc.UnionScopes...)
		stack = append(stack, sc.TableScopes...)
		stack = append(stack, sc.SubqueryScopes...)
	}
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result
}

// RefCount corresponds to Scope.ref_count.
func (s *Scope) RefCount() map[*Scope]int {
	counts := map[*Scope]int{}
	for _, sc := range s.Traverse() {
		ss := sc.SelectedSources()
		for _, k := range ss.keys {
			if sub, ok := ss.m[k].Source.(*Scope); ok {
				counts[sub]++
			}
		}
	}
	return counts
}

// Sources exposes sources (insertion-ordered).
func (s *Scope) Sources() *sourceMap { return s.sources }

// RenameSource corresponds to Scope.rename_source.
func (s *Scope) RenameSource(oldName, newName string) { s.sources.Rename(oldName, newName) }

// ---- scope construction ----

// TraverseScope corresponds to traverse_scope.
func TraverseScope(expression Expression) []*Scope {
	if !isTraversable(expression) {
		return nil
	}
	return traverseScopeInner(NewScope(expression, nil, ScopeRoot))
}

// BuildScope corresponds to build_scope: returns the outermost (last produced) scope.
func BuildScope(expression Expression) *Scope {
	scopes := TraverseScope(expression)
	if len(scopes) == 0 {
		return nil
	}
	return scopes[len(scopes)-1]
}

// traverseScopeInner corresponds to _traverse_scope: child scopes are produced
// first and self is yielded last, except SetOperation (self produced by
// _traverse_union) and DDL/DML (no self).
func traverseScopeInner(scope *Scope) []*Scope {
	children := traverseScopeNode(scope)
	switch scope.Expression.(type) {
	case *Union, *unionTail, *Create, *Drop:
		return children
	case *Insert, *Update, *Delete:
		return children
	}
	return append(children, scope)
}

// traverseScopeNode corresponds to the "produce child scopes" part of _traverse_scope.
func traverseScopeNode(scope *Scope) []*Scope {
	expression := scope.Expression
	var out []*Scope
	switch e := expression.(type) {
	case *Select:
		out = append(out, traverseCTEs(scope)...)
		out = append(out, traverseTables(scope)...)
		out = append(out, traverseSubqueries(scope)...)
	case *unionTail:
		// unionTail is sqlg's split representation of "set operation + trailing ORDER/LIMIT";
		// in sqlglot these are the same SetOperation node, so we descend into the Union here.
		inner := NewScope(e.Union, scope.Parent, scope.ScopeType)
		inner.OuterColumns = scope.OuterColumns
		inner.CanBeCorrelated = scope.CanBeCorrelated
		for _, k := range scope.sources.Keys() {
			v, _ := scope.sources.Get(k)
			if _, isScope := v.(*Scope); isScope {
				inner.sources.Set(k, v)
			}
		}
		out = append(out, traverseScopeNode(inner)...)
		scope.CTEScopes = inner.CTEScopes
		scope.UnionScopes = inner.UnionScopes
		scope.TableScopes = inner.TableScopes
		scope.SubqueryScopes = inner.SubqueryScopes
		scope.sources = inner.sources
		return out
	case *Union:
		out = append(out, traverseCTEs(scope)...)
		out = append(out, traverseUnion(scope)...)
		return out
	case *Subquery:
		if scope.ScopeType == ScopeRoot {
			out = append(out, traverseCTEs(scope)...)
			out = append(out, traverseTables(scope)...)
			out = append(out, traverseSubqueries(scope)...)
		} else {
			out = append(out, traverseSubqueries(scope)...)
		}
	case *Table:
		out = append(out, traverseTables(scope)...)
	case *Create, *Drop:
		// sqlg's Create/Drop only retains the original text (structured never rewrites DDL); there is no query body to descend into
		return out
	case *Insert, *Update, *Delete:
		out = append(out, traverseCTEs(scope)...)
		for _, q := range findQueryInScope(expression) {
			// Avoid producing CTEs / nested queries more than once
			switch q.Parent().(type) {
			case *CTE, *Subquery:
				continue
			}
			out = append(out, traverseScopeInner(NewScope(q, nil, ScopeRoot))...)
		}
		return out
	default:
		return out
	}
	return out
}

// findQueryInScope corresponds to find_all_in_scope(expression, exp.Query).
func findQueryInScope(expression Expression) []Expression {
	var out []Expression
	for _, n := range walkInScope(expression, true) {
		if isQueryLike(n) {
			out = append(out, n)
		}
	}
	return out
}

// traverseUnion corresponds to _traverse_union.
func traverseUnion(scope *Scope) []*Scope {
	var out []*Scope
	var prevScope *Scope

	type item struct {
		expr Expression
	}

	rootUnion, _ := scope.Expression.(*Union)
	if rootUnion == nil {
		return out
	}

	unionScopeStack := []*Scope{scope}
	expressionStack := []Expression{rootUnion.Right, rootUnion.Left}

	for len(expressionStack) > 0 {
		expression := expressionStack[len(expressionStack)-1]
		expressionStack = expressionStack[:len(expressionStack)-1]
		unionScope := unionScopeStack[len(unionScopeStack)-1]

		newScope := unionScope.branch(expression, ScopeUnion, unionScope.OuterColumns)

		if u, ok := unnest(expression).(*Union); ok {
			out = append(out, traverseCTEs(newScope)...)
			unionScopeStack = append(unionScopeStack, newScope)
			expressionStack = append(expressionStack, u.Right, u.Left)
			continue
		}

		children := traverseScopeInner(newScope)
		out = append(out, children...)
		last := newScope
		if len(children) > 0 {
			last = children[len(children)-1]
		}

		if prevScope != nil {
			unionScopeStack = unionScopeStack[:len(unionScopeStack)-1]
			unionScope.UnionScopes = []*Scope{prevScope, last}
			prevScope = unionScope
			out = append(out, unionScope)
		} else {
			prevScope = last
		}
	}
	return out
}

// traverseCTEs corresponds to _traverse_ctes. Ordering constraint: earlier CTEs
// in the same WITH become sources for later CTEs' scopes (`WITH a AS (...),
// b AS (SELECT ... FROM a)`); merge_ctes relies on this.
func traverseCTEs(scope *Scope) []*Scope {
	var out []*Scope
	sources := newSourceMap()

	for _, cte := range scope.CTEs() {
		cteName := ""
		if cte.Alias != nil && cte.Alias.Name != nil {
			cteName = cte.Alias.Name.Name
		}

		if w := withOf(scope.Expression); w != nil && w.Recursive {
			if cte.This != nil {
				if u, ok := cte.This.This.(*Union); ok {
					sources.Set(cteName, scope.branch(u.Left, ScopeCTE, nil))
				}
			}
		}

		children := traverseScopeInner(branchWithCTEs(scope, cte.This, ScopeCTE, aliasColumnNames(cte.Alias), sources))
		out = append(out, children...)
		if len(children) > 0 {
			childScope := children[len(children)-1]
			sources.Set(cteName, childScope)
			scope.CTEScopes = append(scope.CTEScopes, childScope)
		}
	}

	for _, k := range sources.Keys() {
		v, _ := sources.Get(k)
		scope.sources.Set(k, v)
	}
	return out
}

// traverseTables corresponds to _traverse_tables.
func traverseTables(scope *Scope) []*Scope {
	var out []*Scope
	sources := newSourceMap()

	var expressions []Expression
	if s, ok := scope.Expression.(*Select); ok {
		if s.From != nil {
			expressions = append(expressions, s.From)
		}
		for _, j := range s.Joins {
			expressions = append(expressions, j.This)
		}
	}
	if _, ok := scope.Expression.(*Table); ok {
		expressions = append(expressions, scope.Expression)
	}

	for i := 0; i < len(expressions); i++ {
		expression := expressions[i]

		if t, ok := expression.(*Table); ok {
			tableName := t.Name()
			sourceName := t.AliasOrName()

			if scope.sources.Has(tableName) && len(t.Parts) < 2 {
				// A reference to an outer source (e.g. a CTE), not a real table
				sources.Set(sourceName, mustGet(scope.sources, tableName))
			} else if sources.Has(sourceName) {
				sources.Set(findNewName(sources, tableName), t)
			} else {
				sources.Set(sourceName, t)
			}
			continue
		}

		if !isDerivedTableNode(expression) {
			// Check for possible sources inside nested table constructs
			if sq, ok := expression.(*Subquery); ok {
				expressions = append(expressions, sq.This)
			}
			continue
		}

		sq := expression.(*Subquery)
		children := traverseScopeInner(scope.branch(expression, ScopeDerivedTable, aliasColumnNames(sq.Alias)))
		out = append(out, children...)
		if len(children) > 0 {
			childScope := children[len(children)-1]
			sources.Set(getSourceAlias(expression), childScope)
			scope.TableScopes = append(scope.TableScopes, childScope)
			scope.DerivedTableScopes = append(scope.DerivedTableScopes, childScope)
		}
	}

	for _, k := range sources.Keys() {
		v, _ := sources.Get(k)
		scope.sources.Set(k, v)
	}
	return out
}

// traverseSubqueries corresponds to _traverse_subqueries.
func traverseSubqueries(scope *Scope) []*Scope {
	var out []*Scope
	for _, subquery := range scope.Subqueries() {
		children := traverseScopeInner(scope.branch(subquery, ScopeSubquery, nil))
		out = append(out, children...)
		if len(children) > 0 {
			scope.SubqueryScopes = append(scope.SubqueryScopes, children[len(children)-1])
		} else {
			scope.SubqueryScopes = append(scope.SubqueryScopes, nil)
		}
	}
	return out
}

// walkInScope corresponds to walk_in_scope: stops descending as soon as a child scope boundary is reached.
func walkInScope(expression Expression, bfs bool) []Expression {
	var out []Expression
	var stack, queue []Expression
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
		// The root itself is never treated as a boundary (it is the traversal
		// start; continuing there would skip the whole subtree).
		if node != expression && isScopeBoundary(node) {
			// Boundary children belong to the inner scope. (sqlglot also visits
			// a Subquery's joins/laterals/pivots here; sqlg's Subquery has none.)
			continue
		}
		push(node.Children())
	}
	return out
}

// isScopeBoundary corresponds to the "crosses a scope boundary" criterion in walk_in_scope.
func isScopeBoundary(node Expression) bool {
	if _, ok := node.(*CTE); ok {
		return true
	}
	if isDerivedTableNode(node) && isFromOrJoin(node) {
		return true
	}
	if isUnwrappedQuery(node) {
		return true
	}
	return false
}

// ---- small helpers ----

func mustGet(s *sourceMap, k string) any {
	v, _ := s.Get(k)
	return v
}

// withOf retrieves the WITH clause on an expression.
func withOf(e Expression) *With {
	switch v := e.(type) {
	case *Select:
		return v.With
	case *Union:
		return v.With
	case *unionTail:
		if v.Union != nil {
			return v.Union.With
		}
	}
	return nil
}

func aliasColumnNames(a *TableAlias) []string {
	if a == nil {
		return nil
	}
	out := make([]string, 0, len(a.Columns))
	for _, c := range a.Columns {
		out = append(out, c.Name)
	}
	return out
}

// getSourceAlias corresponds to _get_source_alias.
func getSourceAlias(expression Expression) string {
	aliasName := exprAlias(expression)
	if aliasName == "" {
		if sq, ok := expression.(*Subquery); ok && sq.Alias != nil && len(sq.Alias.Columns) == 1 {
			aliasName = sq.Alias.Columns[0].Name
		}
	}
	return aliasName
}

// exprAlias corresponds to Expression.alias.
func exprAlias(e Expression) string {
	switch v := e.(type) {
	case *Subquery:
		if v.Alias != nil && v.Alias.Name != nil {
			return v.Alias.Name.Name
		}
	case *Table:
		if v.Alias != nil && v.Alias.Name != nil {
			return v.Alias.Name.Name
		}
	}
	return ""
}

// findNewName corresponds to sqlglot.helper.find_new_name: finds a name in existing that is not taken.
func findNewName(existing *sourceMap, base string) string {
	taken := map[string]bool{}
	for _, k := range existing.keys {
		taken[k] = true
	}
	return findNewNameIn(taken, base)
}

// namedSelectsOf corresponds to Expression.named_selects.
func namedSelectsOf(e Expression) []string {
	var out []string
	switch v := e.(type) {
	case *Select:
		for _, ex := range v.Expressions {
			out = append(out, selectName(ex))
		}
	case *Union:
		return namedSelectsOf(v.Left)
	case *unionTail:
		if v.Union != nil {
			return namedSelectsOf(v.Union)
		}
	case *Subquery:
		return namedSelectsOf(v.This)
	}
	return out
}

// selectName corresponds to Expression.output_name: Alias → alias; Column/
// Identifier → name; Literal → text; Star → "*"; Subquery → its alias;
// Paren/Cast recurse into this; everything else → "".
func selectName(e Expression) string {
	switch v := e.(type) {
	case *Alias:
		if v.Alias != nil {
			return v.Alias.Name
		}
	case *Column:
		if v.Name != nil {
			return v.Name.Name
		}
	case *Ident:
		return v.Name
	case *Literal:
		return v.Value
	case *Star:
		return "*"
	case *Subquery:
		return exprAlias(v)
	case *Paren:
		return selectName(v.This)
	case *Cast:
		return selectName(v.This)
	}
	return ""
}
