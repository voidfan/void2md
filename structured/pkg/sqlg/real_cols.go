package sqlg

import (
	"fmt"
	"os"
	"strings"
)

// Mirrors parse_col_name_from_scope / parse_real_cols from Python core/utils.py —
// the only two utils that depend on the sqlglot optimizer (qualify + traverse_scope).
// Backs evaluation.ParseRealColsFunc (calculate_retrieval_metrics).

// realColsJoinChar mirrors JOIN_CHAR = "." in core/utils.py.
const realColsJoinChar = "."

// Qualify mirrors sqlglot qualify() with its DEFAULT arguments — different from the
// optimization pipeline's qualify rule: empty schema, isolate_tables=False,
// quote_identifiers=True, identify=True. Empty schema ⇒ no-schema inference branch.
func Qualify(expression Expression, dialect string) Expression {
	// Empty schema must still carry the dialect for column-name normalization.
	schema, err := NewMappingSchema(nil, dialect)
	if err != nil {
		panic(err)
	}
	ctx := &OptCtx{
		Schema:        schema,
		Dialect:       GetDialect(dialect),
		DialectName:   dialect,
		IsolateTables: false,
		QuoteQualify:  true,
		Identify:      true,
	}
	return ruleQualify(expression, ctx)
}

// ParseColNameFromScope mirrors parse_col_name_from_scope: collects a scope's columns
// as "table.column" into realCols. Divergence (unobservable): Python rewrites
// scope.sources in place when case_insensitive; here we use a local lowercase copy.
func ParseColNameFromScope(
	scope *Scope, realCols map[string]struct{}, cteNames map[string]bool, caseInsensitive bool,
) {
	sources := map[string]any{}
	for _, k := range scope.Sources().Keys() {
		v, _ := scope.Sources().Get(k)
		key := k
		if caseInsensitive {
			key = strings.ToLower(k)
		}
		// Dict comprehension semantics: for equal keys the later value wins.
		sources[key] = v
	}

	for _, col := range scope.Columns() {
		tn := colTableName(col)
		if tn != "" {
			if caseInsensitive {
				tn = strings.ToLower(tn)
			}
			source, ok := sources[tn]
			if !ok {
				continue
			}
			// CTE sources are *Scope, filtered by the assertion; the cte_names check
			// covers a real table shadowed by a CTE name.
			tbl, isTable := source.(*Table)
			if !isTable || cteNames[tn] {
				continue
			}
			name := tbl.Name() + realColsJoinChar + colName(col)
			if caseInsensitive {
				name = strings.ToLower(name)
			}
			realCols[name] = struct{}{}
			continue
		}

		// No table qualifier: use the FIRST table of the scope (scope.tables, not selected_sources).
		tables := scope.Tables()
		if len(tables) == 0 {
			continue
		}
		tn = tables[0].Name()
		if cteNames[tn] {
			continue
		}
		name := tn + realColsJoinChar + colName(col)
		if caseInsensitive {
			name = strings.ToLower(name)
		}
		realCols[name] = struct{}{}
	}
}

// ParseRealCols mirrors parse_real_cols. No default-dialect fallback: an unknown
// dialect errors and is swallowed to an empty set (replicated via HasDialect).
// Empty dialect "" means the default dialect and does not error.
func ParseRealCols(sql string, dialect string, caseInsensitive bool) map[string]struct{} {
	realCols := map[string]struct{}{}

	if dialect != "" && !HasDialect(dialect) {
		// Python prints the error to stdout and returns an empty set.
		printParseError("Error when parsing, error: Unknown dialect '" + dialect + "'")
		return realCols
	}

	parsed, err := ParseOne(sql, dialect)
	if err != nil {
		printParseError("Error when parsing, error: " + err.Error())
		return realCols
	}

	// Qualify before scoping: it expands columns to "table.column" form.
	parsed = Qualify(parsed, dialect)

	cteNames := map[string]bool{}
	for _, cte := range FindAllOf[*CTE](parsed, true) {
		if cte.Alias != nil && cte.Alias.Name != nil {
			cteNames[cte.Alias.Name.Name] = true
		}
	}

	for _, scope := range TraverseScope(parsed) {
		ParseColNameFromScope(scope, realCols, cteNames, caseInsensitive)
	}
	return realCols
}

// printParseError mirrors Python print(): stdout with a newline.
func printParseError(msg string) {
	_, _ = fmt.Fprintln(os.Stdout, msg)
}
