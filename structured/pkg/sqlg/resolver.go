package sqlg

// Reproduces sqlglot.optimizer.resolver.Resolver.

// orderedStrSliceMap is an order-preserving string -> []string mapping (mirrors Python's dict).
type orderedStrSliceMap struct {
	keys []string
	m    map[string][]string
}

func newOrderedStrSliceMap() *orderedStrSliceMap {
	return &orderedStrSliceMap{m: map[string][]string{}}
}

func (o *orderedStrSliceMap) set(k string, v []string) {
	if _, ok := o.m[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.m[k] = v
}

func (o *orderedStrSliceMap) get(k string) []string { return o.m[k] }
func (o *orderedStrSliceMap) len() int              { return len(o.keys) }

func (o *orderedStrSliceMap) has(k string) bool {
	_, ok := o.m[k]
	return ok
}

// Resolver mirrors sqlglot.optimizer.resolver.Resolver.
type Resolver struct {
	Scope       *Scope
	Schema      *MappingSchema
	Dialect     *Dialect
	InferSchema bool

	sourceColumns      *orderedStrSliceMap
	unambiguousColumns map[string]string
	allColumnsCache    map[string]bool
	sourceColsCache    map[string][]string
}

// NewResolver mirrors Resolver(scope, schema, infer_schema=...).
func NewResolver(scope *Scope, schema *MappingSchema, inferSchema bool) *Resolver {
	d := GetDialect("")
	if schema != nil && schema.dialect != nil {
		d = schema.dialect
	}
	return &Resolver{
		Scope:           scope,
		Schema:          schema,
		Dialect:         d,
		InferSchema:     inferSchema,
		sourceColsCache: map[string][]string{},
	}
}

// AllColumns mirrors Resolver.all_columns.
func (r *Resolver) AllColumns() map[string]bool {
	if r.allColumnsCache == nil {
		out := map[string]bool{}
		for _, cols := range r.allSourceColumns().m {
			for _, c := range cols {
				out[c] = true
			}
		}
		r.allColumnsCache = out
	}
	return r.allColumnsCache
}

// GetSourceColumns mirrors Resolver.get_source_columns(name, only_visible).
func (r *Resolver) GetSourceColumns(name string, onlyVisible bool) ([]string, error) {
	key := name
	if onlyVisible {
		key = name + "\x00visible"
	}
	if v, ok := r.sourceColsCache[key]; ok {
		return v, nil
	}
	if !r.Scope.Sources().Has(name) {
		return nil, &OptimizeError{Msg: "Unknown table: " + name}
	}
	source, _ := r.Scope.Sources().Get(name)

	var columns []string
	switch src := source.(type) {
	case *Table:
		columns = r.Schema.ColumnNames(src)
	case *Scope:
		if isSetOpExpr(src.Expression) {
			var err error
			columns, err = getSourceColumnsFromSetOp(src.Expression)
			if err != nil {
				return nil, err
			}
		} else {
			columns = namedSelectsOf(src.Expression)
		}
	}

	node := r.selectedSourceNode(name)
	if aliases := aliasColumnNamesOf(node); len(aliases) > 0 {
		// Aliases shadow source columns at the same positions.
		out := make([]string, 0, len(columns))
		for i, n := range columns {
			if i < len(aliases) && aliases[i] != "" {
				out = append(out, aliases[i])
			} else {
				out = append(out, n)
			}
		}
		columns = out
	}

	r.sourceColsCache[key] = columns
	return columns, nil
}

// selectedSourceNode returns the node of a given source in selected_sources (nil if absent).
func (r *Resolver) selectedSourceNode(name string) Expression {
	ss := r.Scope.SelectedSources()
	if v, ok := ss.m[name]; ok {
		return v.Node
	}
	return nil
}

// aliasColumnNamesOf mirrors Expression.alias_column_names.
func aliasColumnNamesOf(e Expression) []string {
	switch v := e.(type) {
	case *Table:
		return aliasColumnNames(v.Alias)
	case *Subquery:
		return aliasColumnNames(v.Alias)
	case *CTE:
		return aliasColumnNames(v.Alias)
	}
	return nil
}

func isSetOpExpr(e Expression) bool {
	switch e.(type) {
	case *Union, *unionTail:
		return true
	}
	return false
}

// getSourceColumnsFromSetOp mirrors Resolver.get_source_columns_from_set_op.
func getSourceColumnsFromSetOp(expression Expression) ([]string, error) {
	switch v := expression.(type) {
	case *Select:
		return namedSelectsOf(v), nil
	case *Subquery:
		if isSetOpExpr(v.This) {
			return getSourceColumnsFromSetOp(v.This)
		}
	case *Union, *unionTail:
		return namedSelectsOf(v), nil
	}
	return nil, &OptimizeError{Msg: "Unknown set operation: " + expression.SQL(GetDialect(""))}
}

// allSourceColumns mirrors Resolver._get_all_source_columns.
func (r *Resolver) allSourceColumns() *orderedStrSliceMap {
	if r.sourceColumns != nil {
		return r.sourceColumns
	}
	out := newOrderedStrSliceMap()
	for _, name := range r.Scope.SelectedSources().keys {
		cols, err := r.GetSourceColumns(name, false)
		if err != nil {
			cols = nil
		}
		out.set(name, cols)
	}
	r.sourceColumns = out
	return out
}

// getTableNameFromSources mirrors Resolver._get_table_name_from_sources.
func (r *Resolver) getTableNameFromSources(
	columnName string, sourceColumns *orderedStrSliceMap,
) string {
	var unambiguous map[string]string
	if sourceColumns == nil || sourceColumns.len() == 0 {
		if r.unambiguousColumns == nil {
			r.unambiguousColumns = getUnambiguousColumns(r.allSourceColumns())
		}
		unambiguous = r.unambiguousColumns
	} else {
		unambiguous = getUnambiguousColumns(sourceColumns)
	}
	return unambiguous[columnName]
}

// getUnambiguousColumns mirrors Resolver._get_unambiguous_columns.
func getUnambiguousColumns(sourceColumns *orderedStrSliceMap) map[string]string {
	if sourceColumns == nil || sourceColumns.len() == 0 {
		return map[string]string{}
	}

	firstTable := sourceColumns.keys[0]
	firstColumns := sourceColumns.get(firstTable)

	out := make(map[string]string, len(firstColumns))
	if sourceColumns.len() == 1 {
		// Single-source fast path: equivalent to SingleValuedMapping(first_columns, first_table)
		for _, c := range firstColumns {
			out[c] = firstTable
		}
		return out
	}

	allColumns := map[string]bool{}
	for _, c := range firstColumns {
		out[c] = firstTable
		allColumns[c] = true
	}

	for _, table := range sourceColumns.keys[1:] {
		columns := sourceColumns.get(table)
		ambiguous := map[string]bool{}
		for _, c := range columns {
			if allColumns[c] {
				ambiguous[c] = true
			}
		}
		for _, c := range columns {
			allColumns[c] = true
		}
		for c := range ambiguous {
			delete(out, c)
		}
		for _, c := range columns {
			if !ambiguous[c] {
				out[c] = table
			}
		}
	}
	return out
}

// getColumnJoinContext mirrors Resolver._get_column_join_context.
func (r *Resolver) getColumnJoinContext(column *Column) *Join {
	sel, ok := r.Scope.Expression.(*Select)
	if !ok {
		return nil
	}
	if len(sel.Joins) == 0 {
		// sqlg has no laterals/pivots, matching Python's args.get("laterals")/("pivots") being empty
		return nil
	}
	ancestor := columnFindAncestorJoinOrSelect(column)
	join, ok := ancestor.(*Join)
	if !ok {
		return nil
	}
	if !r.Scope.SelectedSources().hasKey(AliasOrNameOfExpr(join)) {
		return nil
	}
	return join
}

func (s *selectedSources) hasKey(k string) bool {
	_, ok := s.m[k]
	return ok
}

// columnFindAncestorJoinOrSelect mirrors column.find_ancestor(exp.Join, exp.Select).
func columnFindAncestorJoinOrSelect(c *Column) Expression {
	for p := c.Parent(); p != nil; p = p.Parent() {
		switch p.(type) {
		case *Join, *Select:
			return p
		}
	}
	return nil
}

// getAvailableSourceColumns mirrors Resolver._get_available_source_columns.
func (r *Resolver) getAvailableSourceColumns(joinAncestor *Join) *orderedStrSliceMap {
	out := newOrderedStrSliceMap()
	sel, ok := r.Scope.Expression.(*Select)
	if !ok {
		return out
	}
	if sel.From != nil {
		fromName := AliasOrNameOfExpr(sel.From)
		if cols, err := r.GetSourceColumns(fromName, false); err == nil {
			out.set(fromName, cols)
		}
	}
	for _, join := range sel.Joins {
		name := AliasOrNameOfExpr(join)
		if cols, err := r.GetSourceColumns(name, false); err == nil {
			out.set(name, cols)
		}
		if join == joinAncestor {
			break
		}
	}
	return out
}

// GetTable mirrors Resolver.get_table(column); nil column matches Python passing a string
// (name disambiguation only, no downward inference).
func (r *Resolver) GetTable(columnName string, column *Column) *Ident {
	tableName := r.getTableNameFromSources(columnName, nil)

	if tableName == "" && column != nil {
		// Fallback: retry with the sources visible at the column's JOIN ancestor
		if joinCtx := r.getColumnJoinContext(column); joinCtx != nil {
			avail := r.getAvailableSourceColumns(joinCtx)
			func() {
				defer func() { recover() }()
				tableName = r.getTableNameFromSources(columnName, avail)
			}()
		}
	}

	if tableName == "" && r.InferSchema {
		var without []string
		asc := r.allSourceColumns()
		for _, name := range asc.keys {
			if len(asc.get(name)) == 0 || containsStr(asc.get(name), "*") {
				without = append(without, name)
			}
		}
		if len(without) == 1 {
			tableName = without[0]
		}
	}

	if tableName == "" {
		// Python: exp.to_identifier(None) → None
		return nil
	}

	ss := r.Scope.SelectedSources()
	if !ss.hasKey(tableName) {
		return NewIdent(tableName)
	}

	node := ss.m[tableName].Node
	if isQueryLike(node) {
		cur := node
		for cur != nil && exprAlias(cur) != tableName {
			cur = cur.Parent()
		}
		if cur != nil {
			if a := aliasOfNode(cur); a != nil && a.Name != nil {
				return NewIdent(a.Name.Name)
			}
		}
	}
	return NewIdent(tableName)
}

// aliasOfNode returns a node's TableAlias (mirrors node.args.get("alias")).
func aliasOfNode(e Expression) *TableAlias {
	switch v := e.(type) {
	case *Table:
		return v.Alias
	case *Subquery:
		return v.Alias
	case *CTE:
		return v.Alias
	}
	return nil
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
