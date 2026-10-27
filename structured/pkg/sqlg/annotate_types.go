package sqlg

import (
	"strings"
	"sync"
	"time"
)

// Port of sqlglot optimizer/annotate_types.py plus its exported typing/dialect
// tables. Types are encoded as strings: "INT" / "VARCHAR(255)" (parameterized)
// / "ARRAY<INT>" (nested). Invariants: child scopes annotate before parents;
// metadata lookup is by exact class name; parameterized types never promote.

// ---------------------------------------------------------------------------
// Type sets: copied verbatim from the DataType class attributes in sqlglot/expressions.py
// ---------------------------------------------------------------------------

var (
	textTypes = []string{"CHAR", "NCHAR", "NVARCHAR", "TEXT", "VARCHAR", "NAME"}

	signedIntegerTypes = []string{
		"BIGINT", "INT", "INT128", "INT256", "MEDIUMINT", "SMALLINT", "TINYINT",
	}
	unsignedIntegerTypes = []string{
		"UBIGINT", "UINT", "UINT128", "UINT256", "UMEDIUMINT", "USMALLINT", "UTINYINT",
	}
	integerTypes = concatTypes(
		signedIntegerTypes,
		unsignedIntegerTypes,
		[]string{"BIT"},
	)
	floatTypes = []string{"DOUBLE", "FLOAT"}
	// REAL_TYPES = FLOAT_TYPES ∪ {BIGDECIMAL, DECIMAL*, DECFLOAT, MONEY, ...}
	realTypes = concatTypes(floatTypes, []string{
		"BIGDECIMAL", "DECIMAL", "DECIMAL32", "DECIMAL64", "DECIMAL128", "DECIMAL256",
		"DECFLOAT", "MONEY", "SMALLMONEY", "UDECIMAL", "UDOUBLE",
	})
	// NUMERIC_TYPES = INTEGER_TYPES ∪ REAL_TYPES
	numericTypes = concatTypes(integerTypes, realTypes)
	// TEMPORAL_TYPES listed exactly as in expressions.py
	temporalTypes = []string{
		"DATE", "DATE32", "DATETIME", "DATETIME2", "DATETIME64", "SMALLDATETIME",
		"TIME", "TIMESTAMP", "TIMESTAMPNTZ", "TIMESTAMPLTZ", "TIMESTAMPTZ",
		"TIMESTAMP_MS", "TIMESTAMP_NS", "TIMESTAMP_S", "TIMETZ",
	}
)

var (
	textTypeSet     = typeNameSet(textTypes)
	integerTypeSet  = typeNameSet(integerTypes)
	floatTypeSet    = typeNameSet(floatTypes)
	realTypeSet     = typeNameSet(realTypes)
	numericTypeSet  = typeNameSet(numericTypes)
	temporalTypeSet = typeNameSet(temporalTypes)
)

func concatTypes(groups ...[]string) []string {
	var out []string
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

func typeNameSet(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}

// ---------------------------------------------------------------------------
// Type-name utilities
// ---------------------------------------------------------------------------

// newType creates a scalar type (not parameterized, not nested).
func newType(name string) *DataType { return &DataType{Name: name} }

// typeBase returns the base name: VARCHAR(255) → VARCHAR, ARRAY<INT> → ARRAY
// (sqlglot's `DataType.this`); all set membership and type comparisons use it.
func typeBase(name string) string {
	if i := strings.IndexAny(name, "(<"); i >= 0 {
		return strings.TrimSpace(name[:i])
	}
	return strings.TrimSpace(name)
}

func typeBaseOf(t *DataType) string {
	if t == nil {
		return ""
	}
	return typeBase(t.Name)
}

// typeIsParameterized corresponds to "`DataType.expressions` is non-empty":
// true for both types with precision/length and nested types.
func typeIsParameterized(t *DataType) bool {
	if t == nil {
		return false
	}
	return strings.ContainsAny(t.Name, "(<")
}

// typeIsNested corresponds to sqlglot's `DataType.args["nested"]`:
// true only for nested containers like ARRAY/STRUCT/MAP; DECIMAL(10,2) does not count.
func typeIsNested(t *DataType) bool {
	if t == nil {
		return false
	}
	return strings.Contains(t.Name, "<")
}

// isTypeBase corresponds to sqlglot's `expression.is_type(...)`: compares only
// the base name (VARCHAR(255).is_type(VARCHAR) is true), unlike IsTypeOf.
func isTypeBase(t *DataType, name string) bool {
	return t != nil && typeBase(t.Name) == name
}

// typeValue returns (base name, present); t == nil corresponds to None in Python.
func typeValue(t *DataType) (string, bool) {
	if t == nil {
		return "", false
	}
	return typeBase(t.Name), true
}

// splitTypeText splits a type string into (base name, parameter body, nested or not).
func splitTypeText(s string) (string, string, bool) {
	if i := strings.IndexByte(s, '('); i >= 0 {
		if j := strings.LastIndexByte(s, ')'); j > i {
			return s[:i], s[i+1 : j], false
		}
		return s[:i], s[i+1:], false
	}
	if i := strings.IndexByte(s, '<'); i >= 0 {
		if j := strings.LastIndexByte(s, '>'); j > i {
			return s[:i], s[i+1 : j], true
		}
		return s[:i], s[i+1:], true
	}
	return s, "", false
}

// splitTopLevel splits type parameters on top-level commas (commas inside nested <> / () are not split).
func splitTopLevel(s string) []string {
	var out []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '<':
			depth++
		case ')', '>':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	out = append(out, strings.TrimSpace(s[start:]))
	return out
}

// typeElements returns the element types of a nested type: ARRAY<INT> → [INT];
// MAP<VARCHAR,INT> → [VARCHAR, INT]; STRUCT<a:INT> → [INT].
func typeElements(t *DataType) []string {
	if !typeIsNested(t) {
		return nil
	}
	_, params, _ := splitTypeText(t.Name)
	parts := splitTopLevel(params)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		// STRUCT fields are written as `name:type`; the type is the part after the colon
		if i := strings.IndexByte(p, ':'); i >= 0 {
			p = p[i+1:]
		}
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

// buildTypeName corresponds to `DataType.build(text, dialect=dialect)`
// (parser-side TYPE_MAPPING; distinct from the generator-side mapTypeName).
func buildTypeName(raw, dialectName string) *DataType {
	s := strings.TrimSpace(raw)
	if s == "" {
		return newType("UNKNOWN")
	}
	base, params, nested := splitTypeText(s)
	base = strings.ToUpper(strings.TrimSpace(base))
	if base == "" {
		return newType("UNKNOWN")
	}
	mapped := base
	if m := parserTypeMap[dialectName]; m != nil {
		if v, ok := m[base]; ok {
			mapped = v
		}
	}
	// This dialect drops size/precision when building this type (e.g. duckdb's VARCHAR(255) → TEXT)
	if params != "" && parserTypeDropParams[dialectName][base] {
		params = ""
	}
	if params == "" {
		return newType(mapped)
	}
	if nested {
		elems := splitTopLevel(params)
		out := make([]string, 0, len(elems))
		for _, e := range elems {
			out = append(out, buildTypeName(e, dialectName).Name)
		}
		return newType(mapped + "<" + strings.Join(out, ",") + ">")
	}
	// Strip parameter whitespace so DECIMAL(10, 2) equals DECIMAL(10,2).
	return newType(mapped + "(" + strings.ReplaceAll(strings.TrimSpace(params), " ", "") + ")")
}

// ---------------------------------------------------------------------------
// COERCES_TO
// ---------------------------------------------------------------------------

// coercePrecedence holds sqlglot's three precedence chains (text/numeric/timelike),
// built as the `_TypeAnnotator.__new__` metaclass does: each type absorbs only
// the types before it in the chain.
var coercePrecedence = [][]string{
	{"TEXT", "NVARCHAR", "VARCHAR", "NCHAR", "CHAR"},
	{"DECFLOAT", "DOUBLE", "FLOAT", "BIGDECIMAL", "DECIMAL", "BIGINT", "INT", "SMALLINT", "TINYINT"},
	{"TIMESTAMPLTZ", "TIMESTAMPTZ", "TIMESTAMP", "DATETIME", "DATE"},
}

// baseCoercesTo corresponds to TypeAnnotator.COERCES_TO (a class attribute populated by the metaclass).
var baseCoercesTo = buildBaseCoercesTo()

func buildBaseCoercesTo() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, chain := range coercePrecedence {
		acc := map[string]bool{}
		for _, t := range chain {
			cp := make(map[string]bool, len(acc))
			for k := range acc {
				cp[k] = true
			}
			out[t] = cp
			acc[t] = true
		}
	}
	return out
}

// dialectCoercesTo corresponds to the COERCES_TO overridden per Dialect subclass;
// only hive/spark/spark2, databricks and bigquery override it, all others fall
// back to the base table.
func dialectCoercesTo(name string) map[string]map[string]bool {
	switch name {
	case "hive", "spark", "spark2":
		return overlayCoercesTo(func(m map[string]map[string]bool) {
			for t := range numericTypeSet {
				mergeTypeSet(m, t, textTypes)
			}
			for t := range temporalTypeSet {
				mergeTypeSet(m, t, textTypes)
			}
			mergeTypeSet(m, "INTERVAL", textTypes)
		})
	case "databricks":
		return overlayCoercesTo(func(m map[string]map[string]bool) {
			targets := concatTypes(numericTypes, temporalTypes, []string{"BINARY", "BOOLEAN", "INTERVAL"})
			for _, t := range textTypes {
				mergeTypeSet(m, t, targets)
			}
		})
	case "bigquery":
		return overlayCoercesTo(func(m map[string]map[string]bool) {
			m["BIGDECIMAL"] = map[string]bool{"DOUBLE": true}
			mergeTypeSet(m, "DECIMAL", []string{"BIGDECIMAL"})
			mergeTypeSet(m, "BIGINT", []string{"BIGDECIMAL"})
			mergeTypeSet(m, "VARCHAR", []string{"DATE", "DATETIME", "TIME", "TIMESTAMP", "TIMESTAMPTZ"})
		})
	}
	return nil
}

// overlayCoercesTo deep-copies the base-class table and hands it to f to modify (mirrors `deepcopy(TypeAnnotator.COERCES_TO)`).
func overlayCoercesTo(f func(map[string]map[string]bool)) map[string]map[string]bool {
	m := make(map[string]map[string]bool, len(baseCoercesTo))
	for k, v := range baseCoercesTo {
		cp := make(map[string]bool, len(v))
		for kk := range v {
			cp[kk] = true
		}
		m[k] = cp
	}
	f(m)
	return m
}

func mergeTypeSet(m map[string]map[string]bool, key string, add []string) {
	if m[key] == nil {
		m[key] = map[string]bool{}
	}
	for _, a := range add {
		m[key][a] = true
	}
}

// ---------------------------------------------------------------------------
// BINARY_COERCIONS
// ---------------------------------------------------------------------------

// binaryCoercion computes the result type for the two operand types of a binary operation.
type binaryCoercion func(l, r Expression) *DataType

// buildBinaryCoercions replicates the `swap_all` of TypeAnnotator.BINARY_COERCIONS:
// every (a, b) key also registers the reversed (b, a) version, which swaps the two sides and calls the original function.
func buildBinaryCoercions() map[[2]string]binaryCoercion {
	m := map[[2]string]binaryCoercion{}
	add := func(l, r string, f binaryCoercion) {
		m[[2]string{l, r}] = f
		m[[2]string{r, l}] = func(x, y Expression) *DataType { return f(y, x) }
	}
	// (text, INTERVAL): DATE/DATETIME decided by whether the literal parses as an ISO date
	for _, t := range textTypes {
		add(t, "INTERVAL", func(l, r Expression) *DataType {
			return coerceDateLiteral(l, intervalUnitOf(r))
		})
	}
	// (text, numeric): take the numeric side. Python's `l.type in NUMERIC_TYPES`
	// is always False, so with swap_all the net effect is "numeric side wins";
	// the DataType instance is kept so precision is not lost.
	for _, t := range textTypes {
		for _, n := range numericTypes {
			add(t, n, func(l, r Expression) *DataType {
				if isTypeBase(l.Type(), "TEXT") || textTypeSet[typeBaseOf(l.Type())] {
					return r.Type()
				}
				return l.Type()
			})
		}
	}
	// (DATE, INTERVAL)
	add("DATE", "INTERVAL", func(l, r Expression) *DataType {
		return coerceDate(l, intervalUnitOf(r))
	})
	return m
}

// intervalUnitOf returns the INTERVAL's unit (`r.args.get("unit")`); "" for
// non-INTERVAL operands.
func intervalUnitOf(r Expression) string {
	if iv, ok := r.(*Interval); ok {
		return iv.Unit
	}
	return ""
}

// coerceDateLiteral corresponds to _coerce_date_literal in annotate_types.py.
func coerceDateLiteral(l Expression, unit string) *DataType {
	text := literalTextOf(l)
	isoDate := isISODate(text)
	if isoDate && isDateUnit(unit) {
		return newType("DATE")
	}
	// An ISO date is also an ISO datetime, but not vice versa
	if isoDate || isISODatetime(text) {
		return newType("DATETIME")
	}
	return newType("UNKNOWN")
}

// coerceDate corresponds to _coerce_date; returns the base type (`l.type.this`),
// dropping precision/nesting.
func coerceDate(l Expression, unit string) *DataType {
	if !isDateUnit(unit) {
		return newType("DATETIME")
	}
	if t := l.Type(); t != nil {
		return newType(typeBase(t.Name))
	}
	return newType("UNKNOWN")
}

// literalTextOf corresponds to `expression.name` (literal text; "" otherwise).
func literalTextOf(e Expression) string {
	if lit, ok := e.(*Literal); ok {
		return lit.Value
	}
	return ""
}

// dateUnits corresponds to DATE_UNITS in sqlglot/helper.py.
var dateUnits = map[string]bool{
	"day": true, "week": true, "month": true, "quarter": true, "year": true,
	"year_month": true,
}

// isDateUnit corresponds to is_date_unit in sqlglot/helper.py.
func isDateUnit(unit string) bool {
	return unit != "" && dateUnits[strings.ToLower(unit)]
}

// Layouts accepted for date/datetime literals (equivalent to Python's fromisoformat).
var isoDateLayouts = []string{"2006-01-02", "20060102"}

var isoDatetimeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04",
	"20060102T150405",
	"20060102150405",
}

func isISODate(text string) bool {
	for _, layout := range isoDateLayouts {
		if _, err := time.Parse(layout, text); err == nil {
			return true
		}
	}
	return false
}

func isISODatetime(text string) bool {
	if isISODate(text) {
		return true
	}
	for _, layout := range isoDatetimeLayouts {
		if _, err := time.Parse(layout, text); err == nil {
			return true
		}
	}
	return false
}

// isIntLiteralText corresponds to a literal's `is_int`: first `is_number` (non-string),
// then whether `int(text)` succeeds (Python allows underscores between digits).
func isIntLiteralText(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '+' || s[0] == '-' {
		s = s[1:]
	}
	if s == "" {
		return false
	}
	// The first and last characters must be digits (Python's int() rejects leading/trailing underscores)
	if s[0] < '0' || s[0] > '9' || s[len(s)-1] < '0' || s[len(s)-1] > '9' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c != '_' && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Node → sqlglot class name
// ---------------------------------------------------------------------------

// binaryClassOf maps sqlg's *Binary operators to sqlglot class names; the name
// selects the EXPRESSION_METADATA entry (e.g. EQ is a Predicate → BOOLEAN).
func binaryClassOf(op string) string {
	switch op {
	case "AND":
		return "And"
	case "OR":
		return "Or"
	case "=":
		return "EQ"
	case "<>", "!=":
		return "NEQ"
	case "<":
		return "LT"
	case "<=":
		return "LTE"
	case ">":
		return "GT"
	case ">=":
		return "GTE"
	case "<=>":
		return "NullSafeEQ"
	case "IS DISTINCT FROM", "IS NOT DISTINCT FROM":
		return "NullSafeNEQ"
	case "+":
		return "Add"
	case "-":
		return "Sub"
	case "*":
		return "Mul"
	case "/":
		return "Div"
	case "%":
		return "Mod"
	case "||":
		return "DPipe"
	case "|":
		return "BitwiseOr"
	case "&":
		return "BitwiseAnd"
	case "^":
		return "BitwiseXor"
	case "LIKE":
		return "Like"
	case "ILIKE":
		return "ILike"
	case "RLIKE":
		return "RegexpLike"
	}
	return ""
}

// classOf returns the node's exact sqlglot class name (lookup does not follow
// inheritance; a wrong name falls back to UNKNOWN).
func (a *TypeAnnotator) classOf(e Expression) string {
	switch v := e.(type) {
	case *Ident:
		return "Identifier"
	case *Table:
		return "Table"
	case *TableAlias:
		return "TableAlias"
	case *Column:
		return "Column"
	case *Star:
		return "Star"
	case *Literal:
		return "Literal"
	case *Null:
		return "Null"
	case *Boolean:
		return "Boolean"
	case *Alias:
		return "Alias"
	case *Paren:
		return "Paren"
	case *Unary:
		if v.Op == "NOT" {
			return "Not"
		}
		// sqlglot's unary minus is exp.Neg; unary plus annotates identically.
		return "Neg"
	case *Binary:
		return binaryClassOf(v.Op)
	case *Is:
		return "Is"
	case *In:
		return "In"
	case *Exists:
		return "Exists"
	case *Between:
		return "Between"
	case *Case:
		return "Case"
	case *When:
		// sqlglot's exp.When is not in EXPRESSION_METADATA → UNKNOWN, matching this behavior.
		return "When"
	case *Cast:
		return "Cast"
	case *DataType:
		return "DataType"
	case *Func:
		return a.funcClassOf(v.Name)
	case *Subquery:
		return "Subquery"
	case *CTE:
		return "CTE"
	case *With:
		return "With"
	case *Select:
		return "Select"
	case *Group:
		return "Group"
	case *Order:
		return "Order"
	case *Ordered:
		return "Ordered"
	case *Limit:
		return "Limit"
	case *Offset:
		return "Offset"
	case *Join:
		return "Join"
	case *Union:
		return "Union"
	case *Distinct:
		return "Distinct"
	case *Interval:
		return "Interval"
	case *HexString:
		return "HexString"
	case *TypedLiteral:
		// See applySpec: sqlglot parses `DATE '...'` as a Cast; it is special-cased here.
		return "TypedLiteral"
	case *WhereClause:
		// sqlglot's Where is not in the metadata table → UNKNOWN, matching this.
		return "Where"
	}
	return ""
}

// funcClassOf looks up the class name by function name: dialect override table first, then the base table, finally Anonymous (→ UNKNOWN).
func (a *TypeAnnotator) funcClassOf(name string) string {
	up := strings.ToUpper(name)
	if m := dialectFuncClassTables[a.dialectName]; m != nil {
		if v, ok := m[up]; ok {
			return v
		}
	}
	if v, ok := funcClass[up]; ok {
		return v
	}
	return "Anonymous"
}

// ---------------------------------------------------------------------------
// Metadata tables (merged once per dialect and cached)
// ---------------------------------------------------------------------------

var (
	metaCacheMu sync.Mutex
	metaCache   = map[string]*annotatorTables{}
)

// annotatorTables is the cache carrier for "dialect → merged metadata".
type annotatorTables struct {
	classes     map[string]string // sqlglot class name → spec
	funcClasses map[string]string // uppercase function name → class name
	coercesTo   map[string]map[string]bool
}

// tablesFor merges the base tables with the dialect override tables (sqlglot's
// per-dialect EXPRESSION_METADATA / FUNCTIONS copies), cached per dialect.
func tablesFor(dialectName string) *annotatorTables {
	metaCacheMu.Lock()
	defer metaCacheMu.Unlock()
	if t, ok := metaCache[dialectName]; ok {
		return t
	}
	t := &annotatorTables{
		classes:     make(map[string]string, len(classSpec)+16),
		funcClasses: make(map[string]string, len(funcClass)+16),
	}
	for k, v := range classSpec {
		t.classes[k] = v
	}
	if m := dialectClassSpecTables[dialectName]; m != nil {
		for k, v := range m {
			t.classes[k] = v
		}
	}
	for k, v := range funcClass {
		t.funcClasses[k] = v
	}
	if m := dialectFuncClassTables[dialectName]; m != nil {
		for k, v := range m {
			t.funcClasses[k] = v
		}
	}
	t.coercesTo = dialectCoercesTo(dialectName)
	if t.coercesTo == nil {
		t.coercesTo = baseCoercesTo
	}
	metaCache[dialectName] = t
	return t
}

// ---------------------------------------------------------------------------
// TypeAnnotator
// ---------------------------------------------------------------------------

// TypeAnnotator corresponds to annotate_types.TypeAnnotator.
type TypeAnnotator struct {
	schema      *MappingSchema
	dialect     *Dialect
	dialectName string
	tables      *annotatorTables

	// visited corresponds to `_visited`: dedup by node identity so each node is annotated only once.
	visited map[Expression]bool
	// nullExprs / nullSeen correspond to `_null_expressions` (order-preserving id → expression).
	nullExprs []Expression
	nullSeen  map[Expression]bool

	// setopColumnTypes corresponds to `_setop_column_types`: SetOperation → merged type per projection.
	setopColumnTypes map[Expression]map[string]*DataType

	// nonnull is side-channel storage for sqlglot's `expression.meta["nonnull"]`;
	// read only by _annotate_binary, never affects a type decision. Unlike Python's
	// meta it does not survive across annotator instances.
	nonnull map[Expression]bool

	// supportsNullType corresponds to dialect.SUPPORTS_NULL_TYPE (true for databricks/spark)
	supportsNullType bool
	// defaultNullType corresponds to dialect.DEFAULT_NULL_TYPE
	defaultNullType string
	// promoteToInferredDatetimeType corresponds to dialect.PROMOTE_TO_INFERRED_DATETIME_TYPE
	promoteToInferredDatetimeType bool
	// prioritizeNonLiteralTypes corresponds to dialect.PRIORITIZE_NON_LITERAL_TYPES (true only for bigquery)
	prioritizeNonLiteralTypes bool

	overwriteTypes  bool
	binaryCoercions map[[2]string]binaryCoercion
}

// NewTypeAnnotator corresponds to `TypeAnnotator(schema=schema, overwrite_types=...)`.
//
// The dialect is taken from the schema's own dialect (Python's `schema.dialect or Dialect()`).
func NewTypeAnnotator(schema *MappingSchema, overwriteTypes bool) *TypeAnnotator {
	var d *Dialect
	if schema != nil && schema.dialect != nil {
		d = schema.dialect
	} else {
		d = GetDialect("")
	}
	a := &TypeAnnotator{
		schema:                        schema,
		dialect:                       d,
		dialectName:                   d.Name,
		tables:                        tablesFor(d.Name),
		visited:                       map[Expression]bool{},
		nullSeen:                      map[Expression]bool{},
		setopColumnTypes:              map[Expression]map[string]*DataType{},
		nonnull:                       map[Expression]bool{},
		supportsNullType:              dialectSupportsNullType(d.Name),
		defaultNullType:               dialectDefaultNullTypeOf(d.Name),
		promoteToInferredDatetimeType: dialectPromotesInferredDatetime(d.Name),
		prioritizeNonLiteralTypes:     dialectPrioritizesNonLiteral(d.Name),
		overwriteTypes:                overwriteTypes,
		binaryCoercions:               buildBinaryCoercions(),
	}
	return a
}

// Clear corresponds to TypeAnnotator.clear: it only clears the caches, keeping the schema / dialect configuration.
func (a *TypeAnnotator) Clear() {
	a.visited = map[Expression]bool{}
	a.nullExprs = nil
	a.nullSeen = map[Expression]bool{}
	a.setopColumnTypes = map[Expression]map[string]*DataType{}
}

// ---------------------------------------------------------------------------
// Dialect switches (sqlg's Dialect has no type-system fields; tables exported
// from sqlglot live here).
// ---------------------------------------------------------------------------

func dialectSupportsNullType(name string) bool {
	return name == "databricks" || name == "spark"
}

func dialectPromotesInferredDatetime(name string) bool {
	switch name {
	case "doris", "mysql", "singlestore", "starrocks":
		return true
	}
	return false
}

func dialectPrioritizesNonLiteral(name string) bool {
	return name == "bigquery"
}

// dialectDefaultNullTypeOf corresponds to dialect.DEFAULT_NULL_TYPE (defaults to UNKNOWN).
func dialectDefaultNullTypeOf(name string) string {
	if v, ok := dialectDefaultNullType[name]; ok {
		return v
	}
	return "UNKNOWN"
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

// AnnotateTypes corresponds to annotate_types(); a nil schema becomes an empty
// one per ensure_schema(None, dialect), so the dialect still takes effect.
func AnnotateTypes(expression Expression, schema *MappingSchema, dialect *Dialect, overwriteTypes bool) Expression {
	if dialect == nil {
		dialect = GetDialect("")
	}
	if schema == nil {
		empty, err := NewMappingSchema(nil, dialect.Name)
		if err == nil {
			schema = empty
		}
	}
	return NewTypeAnnotator(schema, overwriteTypes).Annotate(expression, true)
}

// Annotate corresponds to TypeAnnotator.annotate().
func (a *TypeAnnotator) Annotate(expression Expression, annotateScope bool) Expression {
	if annotateScope {
		// traverse_scope is post-order (child scopes before parent scopes); the selects tables depend on this order
		for _, scope := range TraverseScope(expression) {
			a.annotateScope(scope)
		}
	}
	// Handle expressions that are not inside any scope (e.g. standalone fragments)
	a.annotateExpression(expression, nil, nil)

	// NULL is not a real type; after annotation finishes, replace it uniformly with the dialect's default type
	for _, e := range a.nullExprs {
		e.SetType(newType(a.defaultNullType))
	}
	return expression
}

// ---------------------------------------------------------------------------
// Scope-level annotation
// ---------------------------------------------------------------------------

// annotateScope corresponds to TypeAnnotator.annotate_scope.
func (a *TypeAnnotator) annotateScope(scope *Scope) {
	selects := map[string]map[string]*DataType{}
	sources := scope.Sources()

	for _, name := range sources.Keys() {
		v, _ := sources.Get(name)
		src, ok := v.(*Scope)
		if !ok {
			// Non-Scope sources (exp.Table) are skipped here
			continue
		}
		expr := src.Expression
		// The UDTF branch (Lateral/Explode/Unnest/TableFromRows) is unreachable:
		// sqlg has none of these nodes.
		if u, isUnion := expr.(*Union); isUnion {
			if len(querySelects(u.Left)) == len(querySelects(u.Right)) {
				selects[name] = a.setopColumnTypesFor(u)
			}
			continue
		}
		m := map[string]*DataType{}
		for _, s := range querySelects(expr) {
			m[AliasOrNameOfExpr(s)] = s.Type()
		}
		selects[name] = m
	}

	// The TableColumn branch and the QUERY_RESULTS_ARE_STRUCTS (bigquery) branch
	// are unreachable/not implemented in sqlg (no TableColumn node, no meta).

	a.annotateExpression(scope.Expression, scope, selects)
}

// querySelects: see unnest_subqueries.go — corresponds to `query.selects` and shares the same
// implementation with unnest_subqueries (SetOperation recurses through this, Subquery takes this).

// setopColumnTypesFor corresponds to TypeAnnotator._get_setop_column_types (cached).
func (a *TypeAnnotator) setopColumnTypesFor(setop *Union) map[string]*DataType {
	if cached, ok := a.setopColumnTypes[setop]; ok {
		return cached
	}
	colTypes := map[string]*DataType{}

	// On count mismatch return the empty table without caching, as Python does.
	if len(querySelects(setop.Left)) == 0 || len(querySelects(setop.Right)) == 0 ||
		len(querySelects(setop.Left)) != len(querySelects(setop.Right)) {
		return colTypes
	}

	// BFS over the whole set-operation subtree, descending only through SetOperation / Subquery
	for _, node := range WalkBFS(setop, func(n Expression) bool {
		switch n.(type) {
		case *Union, *Subquery:
			return false
		}
		return true
	}) {
		u, ok := node.(*Union)
		if !ok {
			continue
		}
		// The by_name branch has no counterpart in sqlg (BY NAME is not retained).
		left := querySelects(u.Left)
		right := querySelects(u.Right)
		if len(left) != len(right) {
			continue
		}
		for i := range left {
			colName := AliasOrNameOfExpr(left[i])
			colType := a.maybeCoerce(left[i].Type(), right[i].Type())
			// Merge once more with the intermediate result registered so far
			colTypes[colName] = a.maybeCoerce(colType, orNullType(colTypes[colName]))
		}
	}

	a.setopColumnTypes[setop] = colTypes
	return colTypes
}

// orNullType corresponds to `col_types.get(col_name, exp.DataType.Type.NULL)`.
func orNullType(t *DataType) *DataType {
	if t == nil {
		return newType("NULL")
	}
	return t
}

// ---------------------------------------------------------------------------
// Expression-level annotation
// ---------------------------------------------------------------------------

type annotFrame struct {
	expr Expression
	done bool
}

// annotateExpression corresponds to TypeAnnotator._annotate_expression:
// post-order traversal with an explicit stack; the type is computed on the
// second pop, after all children.
func (a *TypeAnnotator) annotateExpression(
	expression Expression, scope *Scope, selects map[string]map[string]*DataType,
) {
	if expression == nil {
		return
	}
	if selects == nil {
		selects = map[string]map[string]*DataType{}
	}
	stack := []annotFrame{{expression, false}}

	for len(stack) > 0 {
		fr := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		expr := fr.expr
		if expr == nil {
			continue
		}
		// Already annotated; or, when overwrite_types=False, it already has a non-UNKNOWN type
		if a.visited[expr] {
			continue
		}
		if !a.overwriteTypes {
			if t := expr.Type(); t != nil && !isTypeBase(t, "UNKNOWN") {
				continue
			}
		}

		if !fr.done {
			stack = append(stack, annotFrame{expr, true})
			for _, child := range expr.Children() {
				if child == nil {
					continue
				}
				stack = append(stack, annotFrame{child, false})
			}
			continue
		}

		// Column: infer the type from the scope sources
		if scope != nil {
			if col, ok := expr.(*Column); ok && colTableName(col) != "" {
				a.annotateColumn(col, scope, selects)
				continue
			}
		}

		spec := a.tables.classes[a.classOf(expr)]
		if spec == "" {
			a.setType(expr, newType("UNKNOWN"))
			continue
		}
		a.applySpec(expr, spec)
	}
}

func colTableName(col *Column) string {
	if col.Table == nil {
		return ""
	}
	return col.Table.Name
}

func colNameOf(col *Column) string {
	if col.Name == nil {
		return ""
	}
	return col.Name.Name
}

// annotateColumn corresponds to the column branch inside _annotate_expression.
func (a *TypeAnnotator) annotateColumn(
	col *Column, scope *Scope, selects map[string]map[string]*DataType,
) {
	table := colTableName(col)
	src, has := scope.Sources().Get(table)
	tbl, isTable := src.(*Table)
	switch {
	case isTable:
		a.setType(col, a.columnType(tbl, colNameOf(col)))
	case has && src != nil:
		if m, ok := selects[table]; ok {
			if t, ok := m[colNameOf(col)]; ok {
				a.setType(col, t)
				break
			}
		}
		if s, ok := src.(*Scope); ok {
			if _, isUnnest := s.Expression.(*Unknown); isUnnest {
				// sqlg has no Unnest node; this branch is unreachable
			}
		}
		a.setType(col, newType("UNKNOWN"))
	default:
		a.setType(col, newType("UNKNOWN"))
	}

	if a.nonnull[col] {
		return
	}
	// The `nullable is False` meta branch is unreachable: sqlg's DataType has no
	// nullable field.
}

// columnType corresponds to `schema.get_column_type(table, column)`:
// the result must pass through DataType.build (parser-side TYPE_MAPPING) instead of being used as-is.
func (a *TypeAnnotator) columnType(t *Table, column string) *DataType {
	if a.schema == nil {
		return newType("UNKNOWN")
	}
	return buildTypeName(a.schema.GetColumnType(t, column), a.dialectName)
}

// setType corresponds to TypeAnnotator._set_type.
func (a *TypeAnnotator) setType(expression Expression, target *DataType) Expression {
	prev := expression.Type()
	if target == nil {
		target = newType("UNKNOWN")
	}
	expression.SetType(target)
	a.visited[expression] = true

	if !a.supportsNullType && typeBase(target.Name) == "NULL" {
		if !a.nullSeen[expression] {
			a.nullSeen[expression] = true
			a.nullExprs = append(a.nullExprs, expression)
		}
	} else if prev != nil && typeBase(prev.Name) == "NULL" {
		if a.nullSeen[expression] {
			delete(a.nullSeen, expression)
			for i, e := range a.nullExprs {
				if e == expression {
					a.nullExprs = append(a.nullExprs[:i], a.nullExprs[i+1:]...)
					break
				}
			}
		}
	}

	// The case restoration for JSON dot access (`meta["dot_parts"]`) is unreachable in sqlg:
	// there is neither an exp.Dot node nor a meta dict.
	return expression
}

// applySpec executes an EXPRESSION_METADATA annotation spec. Spec grammar:
// R|TYPE (fixed return), A|<handler> (_annotate_* function), A|flagtype|key,
// A|arrayof|TYPE, A|byargs|args[|promote|array], A|similar|args.
func (a *TypeAnnotator) applySpec(e Expression, spec string) {
	parts := strings.Split(spec, "|")
	switch parts[0] {
	case "R":
		a.setType(e, newType(parts[1]))
		return
	case "A":
	default:
		a.setType(e, newType("UNKNOWN"))
		return
	}

	switch parts[1] {
	case "binary":
		a.annotateBinary(e)
	case "unary":
		a.annotateUnary(e)
	case "literal":
		a.annotateLiteral(e)
	case "div":
		a.annotateDiv(e)
	case "extract":
		a.annotateExtract(e)
	case "case":
		a.annotateCase(e)
	case "cast":
		// `_set_type(e, e.args["to"])`: the type is the `to` DataType node itself
		if c, ok := e.(*Cast); ok {
			a.setType(e, c.To)
		} else {
			a.setType(e, newType("UNKNOWN"))
		}
	case "datatype":
		// `_set_type(e, e.copy())`: a DataType node's type is a copy of itself
		if d, ok := e.(*DataType); ok {
			a.setType(e, &DataType{Name: d.Name})
		} else {
			a.setType(e, newType("UNKNOWN"))
		}
	case "flagtype":
		a.annotateFlagType(e, parts[2])
	case "arrayof":
		a.setType(e, newType("ARRAY<"+parts[2]+">"))
	case "byargs":
		flags := ""
		if len(parts) > 3 {
			flags = parts[3]
		}
		a.annotateByArgsFromSpec(e, parts[2], strings.Contains(flags, "promote"),
			strings.Contains(flags, "array"))
	case "similar":
		a.annotateBySimilarArgs(e, parts[2], "TEXT")
	case "struct":
		a.annotateStruct(e)
	case "map":
		a.annotateMap(e)
	case "to_map":
		a.annotateToMap(e)
	case "subquery":
		a.annotateSubquery(e)
	case "unnest":
		a.annotateUnnest(e)
	case "explode":
		// exp.Explode has no corresponding node in sqlg
		a.setType(e, newType("UNKNOWN"))
	case "dot":
		// exp.Dot has no corresponding node in sqlg (a dot is the Column's table qualifier)
		a.setType(e, nil)
	case "bracket":
		// exp.Bracket has no corresponding node in sqlg
		a.setType(e, newType("UNKNOWN"))
	case "by_array_element":
		a.annotateByArrayElement(e)
	case "timeunit":
		a.annotateTimeUnit(e)
	default:
		a.setType(e, newType("UNKNOWN"))
	}
}

// annotateFlagType handles the three "same class, pick one of two types by arg/dialect" annotations.
func (a *TypeAnnotator) annotateFlagType(e Expression, key string) {
	switch key {
	case "big_int":
		// Count/DateDiff: big_int flag varies by dialect (base COUNT true, tsql false).
		if a.bigIntFlag(e) {
			a.setType(e, newType("BIGINT"))
		} else {
			a.setType(e, newType("INT"))
		}
	case "is_integer":
		// HexString: driven by HEX_STRING_IS_INTEGER_TYPE (bigquery/clickhouse only).
		if dialectHexIsInteger[a.dialectName] {
			a.setType(e, newType("BIGINT"))
		} else {
			a.setType(e, newType("BINARY"))
		}
	case "with_tz":
		// Unreachable: sqlg never produces exp.Timestamp nodes.
		a.setType(e, newType("TIMESTAMP"))
	default:
		a.setType(e, newType("UNKNOWN"))
	}
}

// bigIntFlag returns the big_int flag for this node's function name.
func (a *TypeAnnotator) bigIntFlag(e Expression) bool {
	fn, ok := e.(*Func)
	if !ok {
		return false
	}
	up := strings.ToUpper(fn.Name)
	if m := dialectFuncBigInt[a.dialectName]; m != nil {
		if v, ok := m[up]; ok {
			return v
		}
	}
	if v, ok := baseFuncBigInt[up]; ok {
		return v
	}
	return false
}

// annotateBinary corresponds to TypeAnnotator._annotate_binary.
func (a *TypeAnnotator) annotateBinary(expression Expression) {
	left, right, ok := binarySides(expression)
	if !ok || left == nil || right == nil {
		// Mirrors Python's "Failed to annotate badly formed binary expression" branch:
		// log a warning and set the type to UNKNOWN.
		a.setType(expression, nil)
		return
	}

	lt := typeBaseOf(left.Type())
	rt := typeBaseOf(right.Type())

	switch {
	case isConnectorExpr(expression) || isPredicateClassNode(expression):
		a.setType(expression, newType("BOOLEAN"))
	default:
		if f, hit := a.binaryCoercions[[2]string{lt, rt}]; hit {
			a.setType(expression, f(left, right))
		} else {
			a.annotateByArgs(expression, []Expression{left, right}, false, false)
		}
	}

	if isIsNode(expression) || (a.nonnull[left] && a.nonnull[right]) {
		a.nonnull[expression] = true
	}
}

// binarySides returns the two sides of a binary node (*Binary or *Is, both of
// which go through _annotate_binary).
func binarySides(e Expression) (Expression, Expression, bool) {
	switch v := e.(type) {
	case *Binary:
		return v.Left, v.Right, true
	case *Is:
		return v.This, v.Expr, true
	}
	return nil, nil, false
}

// isPredicateClassNode corresponds to `isinstance(expression, exp.Predicate)`
// (Connector is a Predicate subclass in sqlglot, so it counts too).
func isPredicateClassNode(e Expression) bool {
	if _, ok := e.(*Is); ok {
		return true
	}
	b, ok := e.(*Binary)
	if !ok {
		return false
	}
	switch b.Op {
	case "AND", "OR", "=", "<>", "!=", "<", "<=", ">", ">=", "<=>",
		"IS DISTINCT FROM", "IS NOT DISTINCT FROM", "LIKE", "ILIKE", "RLIKE":
		return true
	}
	return false
}

// isIsNode: see simplify.go — both implementations are identical (`_is_is_node` in this port only checks the node type).

// annotateUnary corresponds to TypeAnnotator._annotate_unary.
func (a *TypeAnnotator) annotateUnary(expression Expression) {
	var this Expression
	switch v := expression.(type) {
	case *Unary:
		this = v.This
	case *Alias:
		this = v.This
	case *Paren:
		this = v.This
	}
	if u, ok := expression.(*Unary); ok && u.Op == "NOT" {
		a.setType(expression, newType("BOOLEAN"))
	} else if this != nil {
		a.setType(expression, this.Type())
	} else {
		a.setType(expression, newType("UNKNOWN"))
	}
	if this != nil && a.nonnull[this] {
		a.nonnull[expression] = true
	}
}

// annotateLiteral corresponds to TypeAnnotator._annotate_literal.
func (a *TypeAnnotator) annotateLiteral(expression Expression) {
	lit, ok := expression.(*Literal)
	if !ok {
		a.setType(expression, newType("UNKNOWN"))
		return
	}
	switch {
	case lit.IsString:
		a.setType(expression, newType("VARCHAR"))
	case isIntLiteralText(lit.Value):
		a.setType(expression, newType("INT"))
	default:
		a.setType(expression, newType("DOUBLE"))
	}
	a.nonnull[expression] = true
}

// annotateDiv corresponds to TypeAnnotator._annotate_div; sqlg has no `typed`
// (integer-division) flag, so only the else branch applies.
func (a *TypeAnnotator) annotateDiv(expression Expression) {
	left, right, ok := binarySides(expression)
	if !ok || left == nil || right == nil {
		a.setType(expression, newType("UNKNOWN"))
		return
	}
	lt := typeBaseOf(left.Type())
	rt := typeBaseOf(right.Type())
	a.setType(expression, a.maybeCoerce(newType(lt), newType(rt)))
	if t := expression.Type(); t != nil && !realTypeSet[typeBase(t.Name)] {
		a.setType(expression, a.maybeCoerce(t, newType("DOUBLE")))
	}
}

// annotateExtract corresponds to TypeAnnotator._annotate_extract; the EXTRACT
// part sits in sqlg's first argument position.
func (a *TypeAnnotator) annotateExtract(expression Expression) {
	part := ""
	if f, ok := expression.(*Func); ok && len(f.Args) > 0 {
		part = strings.ToUpper(extractNodeText(f.Args[0]))
	}
	switch part {
	case "TIME":
		a.setType(expression, newType("TIME"))
	case "DATE":
		a.setType(expression, newType("DATE"))
	default:
		a.setType(expression, newType("INT"))
	}
}

func extractNodeText(e Expression) string {
	switch v := e.(type) {
	case *Ident:
		return v.Name
	case *Literal:
		return v.Value
	}
	return ""
}

// annotateCase corresponds to `exp.Case: _annotate_by_args(e, *[if.args["true"] for if in e.args["ifs"]], "default")`.
func (a *TypeAnnotator) annotateCase(expression Expression) {
	c, ok := expression.(*Case)
	if !ok {
		a.setType(expression, newType("UNKNOWN"))
		return
	}
	args := make([]Expression, 0, len(c.Ifs)+1)
	for _, w := range c.Ifs {
		if w != nil && w.Then != nil {
			args = append(args, w.Then)
		}
	}
	// "default" is sqlglot's arg name and holds a single expression (ensure_list wraps it into a one-element list)
	if c.Default != nil {
		args = append(args, c.Default)
	}
	a.annotateByArgs(expression, args, false, false)
}

// annotateByArgsFromSpec collects the sub-expressions named in the spec's argument list, then calls annotateByArgs.
func (a *TypeAnnotator) annotateByArgsFromSpec(e Expression, argList string, promote, array bool) {
	var exprs []Expression
	cls := a.classOf(e)
	for _, name := range strings.Split(argList, ",") {
		exprs = append(exprs, a.specArgExprs(e, cls, strings.TrimSpace(name))...)
	}
	a.annotateByArgs(e, exprs, promote, array)
}

// specArgExprs corresponds to `ensure_list(expression.args.get(arg))`, mapping
// sqlglot arg names to sqlg struct fields. For classes in classExprsAll,
// "expressions" covers all args; otherwise all but the first.
func (a *TypeAnnotator) specArgExprs(e Expression, cls, arg string) []Expression {
	one := func(x Expression) []Expression {
		if x == nil {
			return nil
		}
		return []Expression{x}
	}
	switch v := e.(type) {
	case *Binary:
		switch arg {
		case "this", "left":
			return one(v.Left)
		case "expression", "right":
			return one(v.Right)
		}
	case *Is:
		switch arg {
		case "this":
			return one(v.This)
		case "expression":
			return one(v.Expr)
		}
	case *Unary:
		if arg == "this" {
			return one(v.This)
		}
	case *Alias:
		if arg == "this" {
			return one(v.This)
		}
	case *Paren:
		if arg == "this" {
			return one(v.This)
		}
	case *Cast:
		if arg == "this" {
			return one(v.This)
		}
	case *Subquery:
		if arg == "this" {
			return one(v.This)
		}
	case *Distinct:
		// sqlg's Distinct wraps a single expression, corresponding to sqlglot's expressions=[...]
		if arg == "this" || arg == "expressions" {
			return one(v.This)
		}
	case *Between:
		switch arg {
		case "this":
			return one(v.This)
		case "low":
			return one(v.Low)
		case "high":
			return one(v.High)
		}
	case *When:
		switch arg {
		case "this":
			return one(v.Cond)
		case "true":
			return one(v.Then)
		}
	case *Case:
		switch arg {
		case "this":
			return one(v.Operand)
		case "default", "else":
			return one(v.Default)
		}
	case *Limit:
		if arg == "this" || arg == "expression" {
			return one(v.Expression)
		}
	case *Order:
		if arg == "expressions" {
			out := make([]Expression, 0, len(v.Expressions))
			for _, o := range v.Expressions {
				if o != nil {
					out = append(out, o)
				}
			}
			return out
		}
	case *Func:
		nth := func(i int) Expression {
			if i < len(v.Args) {
				return v.Args[i]
			}
			return nil
		}
		switch arg {
		case "this":
			return one(nth(0))
		case "expression":
			return one(nth(1))
		case "true":
			return one(nth(1))
		case "false":
			return one(nth(2))
		case "start":
			return one(nth(0))
		case "end":
			return one(nth(1))
		case "step":
			return one(nth(2))
		case "expressions":
			if classExprsAll[cls] {
				out := make([]Expression, 0, len(v.Args))
				for _, x := range v.Args {
					if x != nil {
						out = append(out, x)
					}
				}
				return out
			}
			out := make([]Expression, 0, len(v.Args))
			for _, x := range skipFirst(v.Args) {
				if x != nil {
					out = append(out, x)
				}
			}
			return out
		}
	}
	return nil
}

// skipFirst corresponds to sqlglot's "the remaining arguments besides this" semantics.
func skipFirst(args []Expression) []Expression {
	if len(args) <= 1 {
		return nil
	}
	return args[1:]
}

// annotateByArgs corresponds to TypeAnnotator._annotate_by_args.
func (a *TypeAnnotator) annotateByArgs(
	expression Expression, args []Expression, promote, array bool,
) {
	var literalType, nonLiteralType, nestedType *DataType

loop:
	for _, arg := range args {
		if arg == nil {
			continue
		}
		exprType := arg.Type()
		if exprType == nil {
			exprType = newType("UNKNOWN")
		}
		// Stop at the first nested type: nested types do not take part in _maybe_coerce
		if typeIsNested(exprType) {
			nestedType = exprType
			break loop
		}
		if _, isLit := arg.(*Literal); isLit {
			literalType = a.maybeCoerce(orUnknown(literalType, exprType), exprType)
		} else {
			nonLiteralType = a.maybeCoerce(orUnknown(nonLiteralType, exprType), exprType)
		}
	}

	var result *DataType
	switch {
	case nestedType != nil:
		result = nestedType
	case literalType != nil && nonLiteralType != nil:
		// Only bigquery enables PRIORITIZE_NON_LITERAL_TYPES; it is false for every corpus dialect
		if a.prioritizeNonLiteralTypes {
			lt, nl := typeBase(literalType.Name), typeBase(nonLiteralType.Name)
			if (integerTypeSet[lt] && integerTypeSet[nl]) || (realTypeSet[lt] && realTypeSet[nl]) {
				result = nonLiteralType
			}
		}
	case literalType != nil:
		result = literalType
	case nonLiteralType != nil:
		result = nonLiteralType
	default:
		result = newType("UNKNOWN")
	}
	if result == nil {
		result = a.maybeCoerce(nonLiteralType, literalType)
	}
	a.setType(expression, result)

	if promote {
		switch {
		case integerTypeSet[typeBaseOf(expression.Type())]:
			a.setType(expression, newType("BIGINT"))
		case floatTypeSet[typeBaseOf(expression.Type())]:
			a.setType(expression, newType("DOUBLE"))
		}
	}

	if array {
		a.setType(expression, newType("ARRAY<"+typeOrUnknown(expression.Type()).Name+">"))
	}
}

// orUnknown corresponds to `x or expr_type`: when the accumulator is empty, use the current value.
func orUnknown(acc, t *DataType) *DataType {
	if acc == nil {
		return t
	}
	return acc
}

func typeOrUnknown(t *DataType) *DataType {
	if t == nil {
		return newType("UNKNOWN")
	}
	return t
}

// annotateBySimilarArgs corresponds to _annotate_by_similar_args in sqlglot/typing/spark2.py.
//
// Rule: all args share one type, or any arg is targetType → take that type;
// any arg is UNKNOWN and none is targetType → UNKNOWN.
func (a *TypeAnnotator) annotateBySimilarArgs(e Expression, argList, targetType string) {
	var exprs []Expression
	cls := a.classOf(e)
	for _, name := range strings.Split(argList, ",") {
		exprs = append(exprs, a.specArgExprs(e, cls, strings.TrimSpace(name))...)
	}
	var lastType *DataType
	hasUnknown := false
	for _, expr := range exprs {
		t := expr.Type()
		switch {
		case isTypeBase(t, "UNKNOWN"):
			hasUnknown = true
		case isTypeBase(t, targetType):
			hasUnknown = false
			lastType = newType(targetType)
			goto done
		default:
			lastType = t
		}
	}
done:
	if hasUnknown {
		a.setType(e, newType("UNKNOWN"))
	} else {
		a.setType(e, lastType)
	}
}

// annotateByArrayElement corresponds to TypeAnnotator._annotate_by_array_element.
func (a *TypeAnnotator) annotateByArrayElement(expression Expression) {
	var this Expression
	switch v := expression.(type) {
	case *Func:
		if len(v.Args) > 0 {
			this = v.Args[0]
		}
	case *Unary:
		this = v.This
	}
	if t := typeOrUnknown(expressionThisType(this)); isTypeBase(t, "ARRAY") {
		elems := typeElements(t)
		if len(elems) > 0 {
			a.setType(expression, newType(elems[0]))
			return
		}
		a.setType(expression, newType("UNKNOWN"))
		return
	}
	a.setType(expression, newType("UNKNOWN"))
}

func expressionThisType(e Expression) *DataType {
	if e == nil {
		return nil
	}
	return e.Type()
}

// annotateUnnest corresponds to TypeAnnotator._annotate_unnest. sqlg has no
// Unnest node (`UNNEST(x)` is a *Func), so this approximates via the first
// argument: ARRAY<T> → T, otherwise UNKNOWN.
func (a *TypeAnnotator) annotateUnnest(expression Expression) {
	var child Expression
	if f, ok := expression.(*Func); ok && len(f.Args) > 0 {
		child = f.Args[0]
	}
	if t := typeOrUnknown(expressionThisType(child)); isTypeBase(t, "ARRAY") {
		elems := typeElements(t)
		if len(elems) > 0 {
			a.setType(expression, newType(elems[0]))
			return
		}
	}
	a.setType(expression, newType("UNKNOWN"))
}

// annotateStruct corresponds to TypeAnnotator._annotate_struct; builds
// STRUCT<field type,...> directly since field names do not affect the type.
func (a *TypeAnnotator) annotateStruct(expression Expression) {
	f, ok := expression.(*Func)
	if !ok {
		a.setType(expression, newType("UNKNOWN"))
		return
	}
	kinds := make([]string, 0, len(f.Args))
	for _, arg := range f.Args {
		if arg == nil {
			a.setType(expression, nil)
			return
		}
		kind := structValueKind(arg)
		if kind == nil || isTypeBase(kind, "UNKNOWN") {
			a.setType(expression, nil)
			return
		}
		kinds = append(kinds, kind.Name)
	}
	a.setType(expression, newType("STRUCT<"+strings.Join(kinds, ",")+">"))
}

// structValueKind corresponds to how kind is extracted in _annotate_struct_value.
func structValueKind(expr Expression) *DataType {
	switch v := expr.(type) {
	case *Alias:
		// STRUCT(key AS value): kind is the alias's own type (already propagated by the unary annotator)
		return v.Type()
	case *Binary:
		// STRUCT(key = value) / STRUCT(key := value): kind is the right operand's type
		if v.Op == "=" || v.Op == ":=" {
			return v.Right.Type()
		}
	case *Column:
		return v.Type()
	}
	return expr.Type()
}

// annotateMap corresponds to TypeAnnotator._annotate_map; sqlg's `MAP(...)`
// never has the two-Array shape, so it is always a bare (non-nested) MAP.
func (a *TypeAnnotator) annotateMap(expression Expression) {
	a.setType(expression, newType("MAP"))
}

// annotateToMap corresponds to TypeAnnotator._annotate_to_map.
func (a *TypeAnnotator) annotateToMap(expression Expression) {
	f, ok := expression.(*Func)
	if !ok || len(f.Args) == 0 {
		a.setType(expression, newType("MAP"))
		return
	}
	t := f.Args[0].Type()
	if typeIsNested(t) && typeBaseOf(t) == "STRUCT" {
		for _, elem := range typeElements(t) {
			if typeBase(elem) != "UNKNOWN" {
				a.setType(expression, newType("MAP<VARCHAR,"+elem+">"))
				return
			}
		}
	}
	a.setType(expression, newType("MAP"))
}

// annotateSubquery corresponds to TypeAnnotator._annotate_subquery.
func (a *TypeAnnotator) annotateSubquery(expression Expression) {
	sub, ok := expression.(*Subquery)
	if !ok {
		a.setType(expression, newType("UNKNOWN"))
		return
	}
	query := unnestExpr(sub.This)
	selects := querySelects(query)
	if len(selects) == 1 {
		a.setType(expression, selects[0].Type())
		return
	}
	a.setType(expression, newType("UNKNOWN"))
}

// annotateTimeUnit corresponds to TypeAnnotator._annotate_timeunit
// (DateAdd/DateSub/DateTrunc). sqlg shape approximation: the unit comes from a
// trailing INTERVAL argument or the second argument.
func (a *TypeAnnotator) annotateTimeUnit(expression Expression) {
	f, ok := expression.(*Func)
	if !ok {
		a.setType(expression, newType("UNKNOWN"))
		return
	}
	var this Expression
	unit := ""
	if len(f.Args) > 0 {
		this = f.Args[0]
	}
	for _, arg := range f.Args[1:] {
		if iv, ok := arg.(*Interval); ok {
			unit = iv.Unit
		} else if lit, ok := arg.(*Literal); ok && unit == "" {
			unit = lit.Value
		}
	}
	tt := typeOrUnknown(expressionThisType(this))
	switch {
	case textTypeSet[typeBaseOf(tt)]:
		a.setType(expression, coerceDateLiteral(this, unit))
	case temporalTypeSet[typeBaseOf(tt)]:
		a.setType(expression, coerceDate(this, unit))
	default:
		a.setType(expression, newType("UNKNOWN"))
	}
}

// ---------------------------------------------------------------------------
// _maybe_coerce
// ---------------------------------------------------------------------------

// maybeCoerce corresponds to TypeAnnotator._maybe_coerce: returns t2 when t1
// promotes to t2, else t1. Parameterized types return as-is; UNKNOWN
// propagates; NULL yields to the other side. nil means Python's None.
func (a *TypeAnnotator) maybeCoerce(t1, t2 *DataType) *DataType {
	if typeIsParameterized(t1) {
		return t1
	}
	if typeIsParameterized(t2) {
		return t2
	}
	v1, ok1 := typeValue(t1)
	v2, ok2 := typeValue(t2)

	if (ok1 && v1 == "UNKNOWN") || (ok2 && v2 == "UNKNOWN") {
		return newType("UNKNOWN")
	}
	if ok1 && v1 == "NULL" {
		if !ok2 {
			return nil
		}
		return newType(v2)
	}
	if ok2 && v2 == "NULL" {
		return newType(v1)
	}
	if ok1 && ok2 && a.tables.coercesTo[v1][v2] {
		return newType(v2)
	}
	if !ok1 {
		return nil
	}
	return newType(v1)
}

// ---------------------------------------------------------------------------
// The metadata tables below are mechanically exported from sqlglot
// classSpec base entries: 235
var classSpec = map[string]string{
	"Abs":                     "A|byargs|this",
	"Add":                     "A|binary",
	"Adjacent":                "A|binary",
	"Alias":                   "A|unary",
	"All":                     "R|BOOLEAN",
	"And":                     "A|binary",
	"Anonymous":               "R|UNKNOWN",
	"Any":                     "R|BOOLEAN",
	"AnyValue":                "A|byargs|this",
	"ApproxDistinct":          "R|BIGINT",
	"ApproxQuantile":          "R|DOUBLE",
	"Array":                   "A|byargs|expressions|array",
	"ArrayAgg":                "A|byargs|this|array",
	"ArrayConcat":             "A|byargs|this,expressions",
	"ArrayConcatAgg":          "A|byargs|this",
	"ArrayContains":           "A|binary",
	"ArrayContainsAll":        "A|binary",
	"ArrayFirst":              "A|by_array_element",
	"ArrayLast":               "A|by_array_element",
	"ArrayOverlaps":           "A|binary",
	"ArrayReverse":            "A|byargs|this",
	"ArraySize":               "R|BIGINT",
	"ArraySlice":              "A|byargs|this",
	"ArrayToString":           "R|VARCHAR",
	"Ascii":                   "R|INT",
	"Avg":                     "R|DOUBLE",
	"Between":                 "R|BOOLEAN",
	"Binary":                  "A|binary",
	"BitwiseAnd":              "A|binary",
	"BitwiseLeftShift":        "A|binary",
	"BitwiseNot":              "A|unary",
	"BitwiseOr":               "A|binary",
	"BitwiseRightShift":       "A|binary",
	"BitwiseXor":              "A|binary",
	"Boolean":                 "R|BOOLEAN",
	"Bracket":                 "A|bracket",
	"Case":                    "A|case",
	"Cast":                    "A|cast",
	"Ceil":                    "R|INT",
	"Chr":                     "R|VARCHAR",
	"Coalesce":                "A|byargs|this,expressions",
	"Collate":                 "A|binary",
	"Concat":                  "R|VARCHAR",
	"ConcatWs":                "R|VARCHAR",
	"Connector":               "A|binary",
	"Contains":                "R|BOOLEAN",
	"Corr":                    "A|binary",
	"Count":                   "A|flagtype|big_int",
	"CountIf":                 "R|BIGINT",
	"CurrentDate":             "R|DATE",
	"CurrentDatetime":         "R|DATETIME",
	"CurrentTime":             "R|TIME",
	"CurrentTimestamp":        "R|TIMESTAMP",
	"CurrentTimestampLTZ":     "R|TIMESTAMPTZ",
	"DPipe":                   "R|VARCHAR",
	"DataType":                "A|datatype",
	"Date":                    "R|DATE",
	"DateAdd":                 "A|timeunit",
	"DateDiff":                "A|flagtype|big_int",
	"DateFromParts":           "R|DATE",
	"DateStrToDate":           "R|DATE",
	"DateSub":                 "A|timeunit",
	"DateToDateStr":           "R|VARCHAR",
	"DateToDi":                "R|INT",
	"DateTrunc":               "A|timeunit",
	"Datetime":                "R|DATETIME",
	"DatetimeAdd":             "R|DATETIME",
	"DatetimeDiff":            "R|INT",
	"DatetimeSub":             "R|DATETIME",
	"Day":                     "R|TINYINT",
	"DayOfMonth":              "R|TINYINT",
	"DayOfWeek":               "R|TINYINT",
	"DayOfWeekIso":            "R|TINYINT",
	"DayOfYear":               "R|TINYINT",
	"DiToDate":                "R|DATE",
	"Distance":                "A|binary",
	"Distinct":                "A|byargs|expressions",
	"Div":                     "A|div",
	"Dot":                     "A|dot",
	"EQ":                      "A|binary",
	"EndsWith":                "R|BOOLEAN",
	"Escape":                  "A|binary",
	"Exists":                  "R|BOOLEAN",
	"Exp":                     "R|DOUBLE",
	"Explode":                 "A|explode",
	"ExtendsLeft":             "A|binary",
	"ExtendsRight":            "A|binary",
	"Extract":                 "A|extract",
	"Filter":                  "A|byargs|this",
	"FromBase32":              "R|BINARY",
	"FromBase64":              "R|BINARY",
	"GT":                      "A|binary",
	"GTE":                     "A|binary",
	"GenerateDateArray":       "A|arrayof|DATE",
	"GenerateSeries":          "A|byargs|start,end,step|array",
	"GenerateTimestampArray":  "A|arrayof|TIMESTAMP",
	"Glob":                    "A|binary",
	"Greatest":                "A|byargs|this,expressions",
	"GroupConcat":             "R|VARCHAR",
	"HavingMax":               "A|byargs|this",
	"HexString":               "A|flagtype|is_integer",
	"ILike":                   "A|binary",
	"If":                      "A|byargs|true,false",
	"In":                      "R|BOOLEAN",
	"Initcap":                 "R|VARCHAR",
	"Int64":                   "R|BIGINT",
	"IntDiv":                  "A|binary",
	"Interval":                "R|INTERVAL",
	"Is":                      "A|binary",
	"JSONArrayContains":       "A|binary",
	"JSONBContains":           "A|binary",
	"JSONBContainsAllTopKeys": "A|binary",
	"JSONBContainsAnyTopKeys": "A|binary",
	"JSONBDeleteAtPath":       "A|binary",
	"JSONBExtract":            "A|binary",
	"JSONBExtractScalar":      "A|binary",
	"JSONExtract":             "A|binary",
	"JSONExtractScalar":       "A|binary",
	"JustifyDays":             "R|INTERVAL",
	"JustifyHours":            "R|INTERVAL",
	"JustifyInterval":         "R|INTERVAL",
	"Kwarg":                   "A|binary",
	"LT":                      "A|binary",
	"LTE":                     "A|binary",
	"LastDay":                 "R|DATE",
	"LastValue":               "A|byargs|this",
	"Least":                   "A|byargs|this,expressions",
	"Length":                  "R|BIGINT",
	"Levenshtein":             "R|INT",
	"Like":                    "A|binary",
	"Limit":                   "A|byargs|this",
	"Literal":                 "A|literal",
	"Ln":                      "R|DOUBLE",
	"Localtime":               "R|TIME",
	"Log":                     "R|DOUBLE",
	"LogicalAnd":              "R|BOOLEAN",
	"LogicalOr":               "R|BOOLEAN",
	"Lower":                   "R|VARCHAR",
	"MakeInterval":            "R|INTERVAL",
	"Map":                     "A|map",
	"Match":                   "A|binary",
	"Max":                     "A|byargs|this,expressions",
	"Min":                     "A|byargs|this,expressions",
	"Mod":                     "A|binary",
	"Month":                   "R|TINYINT",
	"Mul":                     "A|binary",
	"NEQ":                     "A|binary",
	"Neg":                     "A|unary",
	"Not":                     "A|unary",
	"Null":                    "R|NULL",
	"NullSafeEQ":              "A|binary",
	"NullSafeNEQ":             "A|binary",
	"Nullif":                  "A|byargs|this,expression",
	"Operator":                "A|binary",
	"Or":                      "A|binary",
	"Order":                   "A|byargs|this",
	"Overlaps":                "A|binary",
	"Paren":                   "A|unary",
	"ParseJSON":               "R|JSON",
	"Pi":                      "R|DOUBLE",
	"PivotAlias":              "A|unary",
	"Pow":                     "R|DOUBLE",
	"PropertyEQ":              "A|byargs|expression",
	"Quantile":                "R|DOUBLE",
	"Quarter":                 "R|TINYINT",
	"Radians":                 "R|DOUBLE",
	"RawString":               "R|VARCHAR",
	"RegexpFullMatch":         "A|binary",
	"RegexpILike":             "A|binary",
	"RegexpLike":              "R|BOOLEAN",
	"Round":                   "R|DOUBLE",
	"SafeDivide":              "R|DOUBLE",
	"Sign":                    "R|INT",
	"SimilarTo":               "A|binary",
	"Skewness":                "R|DOUBLE",
	"Slice":                   "R|UNKNOWN",
	"SortArray":               "A|byargs|this",
	"Sqrt":                    "R|DOUBLE",
	"StartsWith":              "R|BOOLEAN",
	"Stddev":                  "R|DOUBLE",
	"StddevPop":               "R|DOUBLE",
	"StddevSamp":              "R|DOUBLE",
	"StrPosition":             "R|INT",
	"StrToDate":               "R|DATE",
	"StrToTime":               "R|TIMESTAMP",
	"String":                  "R|VARCHAR",
	"Struct":                  "A|struct",
	"Sub":                     "A|binary",
	"Subquery":                "A|subquery",
	"Substring":               "R|VARCHAR",
	"Sum":                     "A|byargs|this,expressions|promote",
	"Time":                    "R|TIME",
	"TimeAdd":                 "R|TIME",
	"TimeDiff":                "R|INT",
	"TimeStrToDate":           "R|DATE",
	"TimeStrToTime":           "R|TIMESTAMP",
	"TimeSub":                 "R|TIME",
	"TimeToStr":               "R|VARCHAR",
	"TimeToTimeStr":           "R|VARCHAR",
	"Timestamp":               "A|flagtype|with_tz",
	"TimestampAdd":            "R|TIMESTAMP",
	"TimestampDiff":           "R|INT",
	"TimestampLtzFromParts":   "R|TIMESTAMPLTZ",
	"TimestampSub":            "R|TIMESTAMP",
	"TimestampTzFromParts":    "R|TIMESTAMPTZ",
	"ToBase32":                "R|VARCHAR",
	"ToBase64":                "R|VARCHAR",
	"ToDouble":                "R|DOUBLE",
	"ToMap":                   "A|to_map",
	"Trim":                    "R|VARCHAR",
	"TryCast":                 "A|cast",
	"TsOrDiToDi":              "R|INT",
	"TsOrDsToDate":            "R|DATE",
	"TsOrDsToDateStr":         "R|VARCHAR",
	"Unary":                   "A|unary",
	"Unicode":                 "R|INT",
	"UnixDate":                "R|BIGINT",
	"UnixMicros":              "R|BIGINT",
	"UnixMillis":              "R|BIGINT",
	"UnixSeconds":             "R|BIGINT",
	"UnixToStr":               "R|VARCHAR",
	"UnixToTime":              "R|TIMESTAMP",
	"UnixToTimeStr":           "R|VARCHAR",
	"Unnest":                  "A|unnest",
	"Upper":                   "R|VARCHAR",
	"VarMap":                  "A|map",
	"Variance":                "R|DOUBLE",
	"VariancePop":             "R|DOUBLE",
	"Week":                    "R|TINYINT",
	"WeekOfYear":              "R|TINYINT",
	"Window":                  "A|byargs|this",
	"Xor":                     "A|binary",
	"Year":                    "R|TINYINT",
	"YearOfWeek":              "R|TINYINT",
	"YearOfWeekIso":           "R|TINYINT",
}

// funcClass base entries: 226
var funcClass = map[string]string{
	"ABS":                             "Abs",
	"AND":                             "And",
	"ANY_VALUE":                       "AnyValue",
	"APPROX_COUNT_DISTINCT":           "ApproxDistinct",
	"APPROX_DISTINCT":                 "ApproxDistinct",
	"APPROX_QUANTILE":                 "ApproxQuantile",
	"ARRAY":                           "Array",
	"ARRAYAGG":                        "ArrayAgg",
	"ARRAY_AGG":                       "ArrayAgg",
	"ARRAY_CAT":                       "ArrayConcat",
	"ARRAY_CONCAT":                    "ArrayConcat",
	"ARRAY_CONCAT_AGG":                "ArrayConcatAgg",
	"ARRAY_CONTAINS":                  "ArrayContains",
	"ARRAY_CONTAINS_ALL":              "ArrayContainsAll",
	"ARRAY_FIRST":                     "ArrayFirst",
	"ARRAY_HAS":                       "ArrayContains",
	"ARRAY_HAS_ALL":                   "ArrayContainsAll",
	"ARRAY_JOIN":                      "ArrayToString",
	"ARRAY_LAST":                      "ArrayLast",
	"ARRAY_LENGTH":                    "ArraySize",
	"ARRAY_OVERLAPS":                  "ArrayOverlaps",
	"ARRAY_REVERSE":                   "ArrayReverse",
	"ARRAY_SIZE":                      "ArraySize",
	"ARRAY_SLICE":                     "ArraySlice",
	"ARRAY_TO_STRING":                 "ArrayToString",
	"ASCII":                           "Ascii",
	"AVG":                             "Avg",
	"BOOLAND_AGG":                     "LogicalAnd",
	"BOOLOR_AGG":                      "LogicalOr",
	"BOOL_AND":                        "LogicalAnd",
	"BOOL_OR":                         "LogicalOr",
	"CASE":                            "Case",
	"CAST":                            "Cast",
	"CEIL":                            "Ceil",
	"CEILING":                         "Ceil",
	"CHAR":                            "Chr",
	"CHARACTER_LENGTH":                "Length",
	"CHARINDEX":                       "StrPosition",
	"CHAR_LENGTH":                     "Length",
	"CHR":                             "Chr",
	"COALESCE":                        "Coalesce",
	"COLLATE":                         "Collate",
	"CONCAT":                          "Concat",
	"CONCAT_WS":                       "ConcatWs",
	"CONTAINS":                        "Contains",
	"CORR":                            "Corr",
	"COUNT":                           "Count",
	"COUNTIF":                         "CountIf",
	"COUNT_IF":                        "CountIf",
	"CURRENT_DATE":                    "CurrentDate",
	"CURRENT_DATETIME":                "CurrentDatetime",
	"CURRENT_TIME":                    "CurrentTime",
	"CURRENT_TIMESTAMP":               "CurrentTimestamp",
	"CURRENT_TIMESTAMP_L_T_Z":         "CurrentTimestampLTZ",
	"DATE":                            "Date",
	"DATEDIFF":                        "DateDiff",
	"DATEFROMPARTS":                   "DateFromParts",
	"DATETIME":                        "Datetime",
	"DATETIME_ADD":                    "DatetimeAdd",
	"DATETIME_DIFF":                   "DatetimeDiff",
	"DATETIME_SUB":                    "DatetimeSub",
	"DATE_ADD":                        "DateAdd",
	"DATE_DIFF":                       "DateDiff",
	"DATE_FROM_PARTS":                 "DateFromParts",
	"DATE_STR_TO_DATE":                "DateStrToDate",
	"DATE_SUB":                        "DateSub",
	"DATE_TO_DATE_STR":                "Cast",
	"DATE_TO_DI":                      "DateToDi",
	"DATE_TRUNC":                      "DateTrunc",
	"DAY":                             "Day",
	"DAYOFMONTH":                      "DayOfMonth",
	"DAYOFWEEK":                       "DayOfWeek",
	"DAYOFWEEK_ISO":                   "DayOfWeekIso",
	"DAYOFYEAR":                       "DayOfYear",
	"DAY_OF_MONTH":                    "DayOfMonth",
	"DAY_OF_WEEK":                     "DayOfWeek",
	"DAY_OF_YEAR":                     "DayOfYear",
	"DI_TO_DATE":                      "DiToDate",
	"ENDSWITH":                        "EndsWith",
	"ENDS_WITH":                       "EndsWith",
	"EXISTS":                          "Exists",
	"EXP":                             "Exp",
	"EXPLODE":                         "Explode",
	"EXTRACT":                         "Extract",
	"FROM_BASE32":                     "FromBase32",
	"FROM_BASE64":                     "FromBase64",
	"GENERATE_DATE_ARRAY":             "GenerateDateArray",
	"GENERATE_SERIES":                 "GenerateSeries",
	"GENERATE_TIMESTAMP_ARRAY":        "GenerateTimestampArray",
	"GLOB":                            "Glob",
	"GREATEST":                        "Greatest",
	"GROUP_CONCAT":                    "GroupConcat",
	"IF":                              "If",
	"IFNULL":                          "Coalesce",
	"IIF":                             "If",
	"INITCAP":                         "Initcap",
	"INSTR":                           "StrPosition",
	"INT64":                           "Int64",
	"ISODOW":                          "DayOfWeekIso",
	"JSONB_CONTAINS":                  "JSONBContains",
	"JSONB_EXTRACT":                   "JSONBExtract",
	"JSONB_EXTRACT_SCALAR":            "JSONBExtractScalar",
	"JSON_ARRAY_CONTAINS":             "JSONArrayContains",
	"JSON_EXTRACT":                    "JSONExtract",
	"JSON_EXTRACT_PATH_TEXT":          "JSONExtractScalar",
	"JSON_EXTRACT_SCALAR":             "JSONExtractScalar",
	"JSON_PARSE":                      "ParseJSON",
	"JUSTIFY_DAYS":                    "JustifyDays",
	"JUSTIFY_HOURS":                   "JustifyHours",
	"JUSTIFY_INTERVAL":                "JustifyInterval",
	"J_S_O_N_B_CONTAINS_ALL_TOP_KEYS": "JSONBContainsAllTopKeys",
	"J_S_O_N_B_CONTAINS_ANY_TOP_KEYS": "JSONBContainsAnyTopKeys",
	"J_S_O_N_B_DELETE_AT_PATH":        "JSONBDeleteAtPath",
	"LAST_DAY":                        "LastDay",
	"LAST_DAY_OF_MONTH":               "LastDay",
	"LAST_VALUE":                      "LastValue",
	"LCASE":                           "Lower",
	"LEAST":                           "Least",
	"LEN":                             "Length",
	"LENGTH":                          "Length",
	"LEVENSHTEIN":                     "Levenshtein",
	"LIKE":                            "Like",
	"LN":                              "Ln",
	"LOCALTIME":                       "Localtime",
	"LOCATE":                          "StrPosition",
	"LOG":                             "Log",
	"LOG10":                           "Log",
	"LOG2":                            "Log",
	"LOGICAL_AND":                     "LogicalAnd",
	"LOGICAL_OR":                      "LogicalOr",
	"LOWER":                           "Lower",
	"LTRIM":                           "Trim",
	"MAKE_INTERVAL":                   "MakeInterval",
	"MAP":                             "Map",
	"MAX":                             "Max",
	"MIN":                             "Min",
	"MOD":                             "Mod",
	"MONTH":                           "Month",
	"NULLIF":                          "Nullif",
	"NVL":                             "Coalesce",
	"OR":                              "Or",
	"PARSE_JSON":                      "ParseJSON",
	"PI":                              "Pi",
	"POW":                             "Pow",
	"POWER":                           "Pow",
	"QUANTILE":                        "Quantile",
	"QUARTER":                         "Quarter",
	"RADIANS":                         "Radians",
	"REGEXP_FULL_MATCH":               "RegexpFullMatch",
	"REGEXP_I_LIKE":                   "RegexpILike",
	"REGEXP_LIKE":                     "RegexpLike",
	"ROUND":                           "Round",
	"RTRIM":                           "Trim",
	"SAFE_DIVIDE":                     "SafeDivide",
	"SIGN":                            "Sign",
	"SIGNUM":                          "Sign",
	"SKEWNESS":                        "Skewness",
	"SORT_ARRAY":                      "SortArray",
	"SQRT":                            "Sqrt",
	"STARTSWITH":                      "StartsWith",
	"STARTS_WITH":                     "StartsWith",
	"STDDEV":                          "Stddev",
	"STDDEV_POP":                      "StddevPop",
	"STDDEV_SAMP":                     "StddevSamp",
	"STDEV":                           "Stddev",
	"STRING":                          "String",
	"STRPOS":                          "StrPosition",
	"STRUCT":                          "Struct",
	"STR_POSITION":                    "StrPosition",
	"STR_TO_DATE":                     "StrToDate",
	"STR_TO_TIME":                     "StrToTime",
	"SUBSTR":                          "Substring",
	"SUBSTRING":                       "Substring",
	"SUM":                             "Sum",
	"TIME":                            "Time",
	"TIMESTAMP":                       "Timestamp",
	"TIMESTAMPDIFF":                   "TimestampDiff",
	"TIMESTAMPLTZFROMPARTS":           "TimestampLtzFromParts",
	"TIMESTAMPTZFROMPARTS":            "TimestampTzFromParts",
	"TIMESTAMP_ADD":                   "TimestampAdd",
	"TIMESTAMP_DIFF":                  "TimestampDiff",
	"TIMESTAMP_LTZ_FROM_PARTS":        "TimestampLtzFromParts",
	"TIMESTAMP_SUB":                   "TimestampSub",
	"TIMESTAMP_TZ_FROM_PARTS":         "TimestampTzFromParts",
	"TIME_ADD":                        "TimeAdd",
	"TIME_DIFF":                       "TimeDiff",
	"TIME_STR_TO_DATE":                "TimeStrToDate",
	"TIME_STR_TO_TIME":                "TimeStrToTime",
	"TIME_SUB":                        "TimeSub",
	"TIME_TO_STR":                     "TimeToStr",
	"TIME_TO_TIME_STR":                "Cast",
	"TO_BASE32":                       "ToBase32",
	"TO_BASE64":                       "ToBase64",
	"TO_DOUBLE":                       "ToDouble",
	"TO_MAP":                          "ToMap",
	"TRIM":                            "Trim",
	"TRY_CAST":                        "TryCast",
	"TS_OR_DI_TO_DI":                  "TsOrDiToDi",
	"TS_OR_DS_TO_DATE":                "TsOrDsToDate",
	"TS_OR_DS_TO_DATE_STR":            "Substring",
	"UCASE":                           "Upper",
	"UNICODE":                         "Unicode",
	"UNIX_DATE":                       "UnixDate",
	"UNIX_MICROS":                     "UnixMicros",
	"UNIX_MILLIS":                     "UnixMillis",
	"UNIX_SECONDS":                    "UnixSeconds",
	"UNIX_TO_STR":                     "UnixToStr",
	"UNIX_TO_TIME":                    "UnixToTime",
	"UNIX_TO_TIME_STR":                "UnixToTimeStr",
	"UNNEST":                          "Unnest",
	"UPPER":                           "Upper",
	"VARIANCE":                        "Variance",
	"VARIANCE_POP":                    "VariancePop",
	"VARIANCE_SAMP":                   "Variance",
	"VAR_MAP":                         "VarMap",
	"VAR_POP":                         "VariancePop",
	"VAR_SAMP":                        "Variance",
	"WEEK":                            "Week",
	"WEEKOFYEAR":                      "WeekOfYear",
	"WEEK_OF_YEAR":                    "WeekOfYear",
	"XOR":                             "Xor",
	"YEAR":                            "Year",
	"YEAROFWEEK":                      "YearOfWeek",
	"YEAROFWEEKISO":                   "YearOfWeekIso",
	"YEAR_OF_WEEK":                    "YearOfWeek",
	"YEAR_OF_WEEK_ISO":                "YearOfWeekIso",
}

// dialect funcClass overrides
var dialectFuncClass_bigquery = map[string]string{
	"CONTAINS_SUBSTR":   "Contains",
	"DIV":               "IntDiv",
	"EDIT_DISTANCE":     "Levenshtein",
	"FORMAT_DATE":       "TimeToStr",
	"FORMAT_DATETIME":   "TimeToStr",
	"FORMAT_TIME":       "TimeToStr",
	"FORMAT_TIMESTAMP":  "TimeToStr",
	"GENERATE_ARRAY":    "GenerateSeries",
	"JSON_QUERY":        "JSONExtract",
	"JSON_VALUE":        "JSONExtractScalar",
	"PARSE_DATE":        "StrToDate",
	"PARSE_TIMESTAMP":   "StrToTime",
	"REGEXP_CONTAINS":   "RegexpLike",
	"TIMESTAMP_MICROS":  "UnixToTime",
	"TIMESTAMP_MILLIS":  "UnixToTime",
	"TIMESTAMP_SECONDS": "UnixToTime",
}
var dialectFuncClass_clickhouse = map[string]string{
	"ANY":                 "AnyValue",
	"ARRAYREVERSE":        "ArrayReverse",
	"ARRAYSLICE":          "ArraySlice",
	"DATEADD":             "DateAdd",
	"DATESUB":             "DateSub",
	"DATE_FORMAT":         "TimeToStr",
	"EDITDISTANCE":        "Levenshtein",
	"FORMATDATETIME":      "TimeToStr",
	"HAS":                 "ArrayContains",
	"ILIKE":               "ILike",
	"JSONEXTRACTSTRING":   "JSONExtractScalar",
	"LEVENSHTEINDISTANCE": "Levenshtein",
	"MAP":                 "VarMap",
	"MATCH":               "RegexpLike",
	"NOTLIKE":             "Not",
	"STR_TO_DATE":         "Cast",
	"TIMESTAMPADD":        "TimestampAdd",
	"TIMESTAMPSUB":        "TimestampSub",
	"UNIQ":                "ApproxDistinct",
}
var dialectFuncClass_databricks = map[string]string{
	"APPROX_PERCENTILE": "ApproxQuantile",
	"BASE64":            "ToBase64",
	"BOOLEAN":           "Cast",
	"COLLECT_LIST":      "ArrayAgg",
	"DATE":              "Cast",
	"DATEADD":           "DateAdd",
	"DATE_FORMAT":       "TimeToStr",
	"DOUBLE":            "Cast",
	"FLOAT":             "Cast",
	"FROM_UNIXTIME":     "UnixToStr",
	"GETDATE":           "CurrentTimestamp",
	"GET_JSON_OBJECT":   "JSONExtractScalar",
	"ILIKE":             "ILike",
	"INT":               "Cast",
	"LISTAGG":           "GroupConcat",
	"MAP":               "VarMap",
	"MAP_FROM_ARRAYS":   "Map",
	"NOW":               "CurrentTimestamp",
	"PERCENTILE":        "Quantile",
	"PERCENTILE_APPROX": "ApproxQuantile",
	"RLIKE":             "RegexpLike",
	"SEQUENCE":          "GenerateSeries",
	"SHIFTLEFT":         "BitwiseLeftShift",
	"SHIFTRIGHT":        "BitwiseRightShift",
	"SIZE":              "ArraySize",
	"SLICE":             "ArraySlice",
	"STRING":            "Cast",
	"TIMESTAMP":         "Cast",
	"TIMESTAMPADD":      "TimestampAdd",
	"TIMESTAMP_LTZ":     "Cast",
	"TIMESTAMP_NTZ":     "Cast",
	"TO_DATE":           "TsOrDsToDate",
	"TO_TIMESTAMP":      "Cast",
	"TRUNC":             "DateTrunc",
	"TRY_ELEMENT_AT":    "Bracket",
	"UNBASE64":          "FromBase64",
}
var dialectFuncClass_doris = map[string]string{
	"CURDATE":       "CurrentDate",
	"DATE":          "TsOrDsToDate",
	"DATE_FORMAT":   "TimeToStr",
	"FROM_UNIXTIME": "UnixToTime",
	"ISNULL":        "Paren",
	"MONTHNAME":     "TimeToStr",
	"REGEXP":        "RegexpLike",
	"TO_DATE":       "TsOrDsToDate",
	"TO_DAYS":       "Paren",
}
var dialectFuncClass_dremio = map[string]string{
	"ARRAY_GENERATE_RANGE": "GenerateSeries",
	"DATE_FORMAT":          "TimeToStr",
	"DATE_PART":            "Extract",
	"REGEXP_MATCHES":       "RegexpLike",
	"TO_DATE":              "TsOrDsToDate",
}
var dialectFuncClass_drill = map[string]string{
	"LEVENSHTEIN_DISTANCE": "Levenshtein",
	"REPEATED_COUNT":       "ArraySize",
	"TO_CHAR":              "TimeToStr",
	"TO_TIMESTAMP":         "TimeStrToTime",
}
var dialectFuncClass_duckdb = map[string]string{
	"ARRAY_REVERSE_SORT":  "SortArray",
	"ARRAY_SORT":          "SortArray",
	"EDITDIST3":           "Levenshtein",
	"EPOCH_MS":            "UnixToTime",
	"JSON":                "ParseJSON",
	"JSON_EXTRACT_PATH":   "JSONExtract",
	"JSON_EXTRACT_STRING": "JSONExtractScalar",
	"LIST_CONTAINS":       "ArrayContains",
	"LIST_HAS":            "ArrayContains",
	"LIST_HAS_ANY":        "ArrayOverlaps",
	"LIST_REVERSE_SORT":   "SortArray",
	"LIST_SORT":           "SortArray",
	"LIST_VALUE":          "Array",
	"MAKE_DATE":           "DateFromParts",
	"MAKE_TIMESTAMP":      "UnixToTime",
	"RANGE":               "GenerateSeries",
	"REGEXP_MATCHES":      "RegexpLike",
	"STRFTIME":            "TimeToStr",
	"STRPTIME":            "StrToTime",
	"STRUCT_PACK":         "Struct",
	"TO_TIMESTAMP":        "UnixToTime",
	"UNNEST":              "Explode",
	"XOR":                 "BitwiseXor",
}
var dialectFuncClass_dune = map[string]string{
	"APPROX_PERCENTILE":    "ApproxQuantile",
	"ARBITRARY":            "AnyValue",
	"BITWISE_AND":          "BitwiseAnd",
	"BITWISE_NOT":          "BitwiseNot",
	"BITWISE_OR":           "BitwiseOr",
	"BITWISE_XOR":          "BitwiseXor",
	"CARDINALITY":          "ArraySize",
	"CONTAINS":             "ArrayContains",
	"DATE_FORMAT":          "TimeToStr",
	"DATE_PARSE":           "StrToTime",
	"DAY_OF_WEEK":          "DayOfWeekIso",
	"DOW":                  "DayOfWeekIso",
	"DOY":                  "DayOfYear",
	"ELEMENT_AT":           "Bracket",
	"FROM_UNIXTIME":        "UnixToTime",
	"LEVENSHTEIN_DISTANCE": "Levenshtein",
	"NOW":                  "CurrentTimestamp",
	"ROW":                  "Struct",
	"SEQUENCE":             "GenerateSeries",
	"SLICE":                "ArraySlice",
	"TO_CHAR":              "TimeToStr",
	"WEEK":                 "WeekOfYear",
}
var dialectFuncClass_exasol = map[string]string{
	"ADD_DAYS":                   "DateAdd",
	"ADD_HOURS":                  "DateAdd",
	"ADD_MINUTES":                "DateAdd",
	"ADD_MONTHS":                 "DateAdd",
	"ADD_SECONDS":                "DateAdd",
	"ADD_WEEKS":                  "DateAdd",
	"ADD_YEARS":                  "DateAdd",
	"APPROXIMATE_COUNT_DISTINCT": "ApproxDistinct",
	"BIT_AND":                    "BitwiseAnd",
	"BIT_LSHIFT":                 "BitwiseLeftShift",
	"BIT_NOT":                    "BitwiseNot",
	"BIT_OR":                     "BitwiseOr",
	"BIT_RSHIFT":                 "BitwiseRightShift",
	"BIT_XOR":                    "BitwiseXor",
	"DAYS_BETWEEN":               "DateDiff",
	"DIV":                        "IntDiv",
	"EDIT_DISTANCE":              "Levenshtein",
	"EVERY":                      "All",
	"HOURS_BETWEEN":              "DateDiff",
	"MINUTES_BETWEEN":            "DateDiff",
	"MONTHS_BETWEEN":             "DateDiff",
	"NULLIFZERO":                 "If",
	"SECONDS_BETWEEN":            "DateDiff",
	"TO_DATE":                    "TsOrDsToDate",
	"WEEKS_BETWEEN":              "DateDiff",
	"YEARS_BETWEEN":              "DateDiff",
	"ZEROIFNULL":                 "If",
}
var dialectFuncClass_fabric = map[string]string{
	"COUNT_BIG":         "Count",
	"DATEADD":           "DateAdd",
	"DATEDIFF_BIG":      "DateDiff",
	"DATENAME":          "TimeToStr",
	"EOMONTH":           "LastDay",
	"FORMAT":            "TimeToStr",
	"GETDATE":           "CurrentTimestamp",
	"ISNULL":            "Coalesce",
	"JSON_QUERY":        "JSONExtract",
	"JSON_VALUE":        "JSONExtractScalar",
	"SQUARE":            "Pow",
	"SYSDATETIME":       "CurrentTimestamp",
	"SYSDATETIMEOFFSET": "CurrentTimestampLTZ",
}
var dialectFuncClass_hive = map[string]string{
	"BASE64":            "ToBase64",
	"COLLECT_LIST":      "ArrayAgg",
	"DATE_FORMAT":       "TimeToStr",
	"FROM_UNIXTIME":     "UnixToStr",
	"GET_JSON_OBJECT":   "JSONExtractScalar",
	"MAP":               "VarMap",
	"PERCENTILE":        "Quantile",
	"PERCENTILE_APPROX": "ApproxQuantile",
	"SEQUENCE":          "GenerateSeries",
	"SIZE":              "ArraySize",
	"TO_DATE":           "TsOrDsToDate",
	"UNBASE64":          "FromBase64",
}
var dialectFuncClass_materialize = map[string]string{
	"DIV":                    "Cast",
	"JSON_EXTRACT_PATH":      "JSONExtract",
	"LEVENSHTEIN_LESS_EQUAL": "Levenshtein",
	"NOW":                    "CurrentTimestamp",
	"TO_CHAR":                "TimeToStr",
	"TO_DATE":                "StrToDate",
	"TO_TIMESTAMP":           "UnixToTime",
	"UNNEST":                 "Explode",
}
var dialectFuncClass_mysql = map[string]string{
	"CURDATE":       "CurrentDate",
	"DATE":          "TsOrDsToDate",
	"DATE_FORMAT":   "TimeToStr",
	"FROM_UNIXTIME": "UnixToTime",
	"ISNULL":        "Paren",
	"MONTHNAME":     "TimeToStr",
	"TO_DAYS":       "Paren",
}
var dialectFuncClass_oracle = map[string]string{
	"SQUARE":  "Pow",
	"TO_DATE": "StrToDate",
	"TRUNC":   "DateTrunc",
}
var dialectFuncClass_postgres = map[string]string{
	"DIV":                    "Cast",
	"JSON_EXTRACT_PATH":      "JSONExtract",
	"LEVENSHTEIN_LESS_EQUAL": "Levenshtein",
	"NOW":                    "CurrentTimestamp",
	"TO_CHAR":                "TimeToStr",
	"TO_DATE":                "StrToDate",
	"TO_TIMESTAMP":           "UnixToTime",
	"UNNEST":                 "Explode",
}
var dialectFuncClass_presto = map[string]string{
	"APPROX_PERCENTILE":    "ApproxQuantile",
	"ARBITRARY":            "AnyValue",
	"BITWISE_AND":          "BitwiseAnd",
	"BITWISE_NOT":          "BitwiseNot",
	"BITWISE_OR":           "BitwiseOr",
	"BITWISE_XOR":          "BitwiseXor",
	"CARDINALITY":          "ArraySize",
	"CONTAINS":             "ArrayContains",
	"DATE_FORMAT":          "TimeToStr",
	"DATE_PARSE":           "StrToTime",
	"DAY_OF_WEEK":          "DayOfWeekIso",
	"DOW":                  "DayOfWeekIso",
	"DOY":                  "DayOfYear",
	"ELEMENT_AT":           "Bracket",
	"FROM_UNIXTIME":        "UnixToTime",
	"LEVENSHTEIN_DISTANCE": "Levenshtein",
	"NOW":                  "CurrentTimestamp",
	"ROW":                  "Struct",
	"SEQUENCE":             "GenerateSeries",
	"SLICE":                "ArraySlice",
	"TO_CHAR":              "TimeToStr",
	"WEEK":                 "WeekOfYear",
}
var dialectFuncClass_prql = map[string]string{
	"AVERAGE": "Avg",
	"SUM":     "Coalesce",
}
var dialectFuncClass_redshift = map[string]string{
	"DIV":                    "Cast",
	"GETDATE":                "CurrentTimestamp",
	"JSON_EXTRACT_PATH":      "JSONExtract",
	"LEVENSHTEIN_LESS_EQUAL": "Levenshtein",
	"LISTAGG":                "GroupConcat",
	"NOW":                    "CurrentTimestamp",
	"TO_CHAR":                "TimeToStr",
	"TO_DATE":                "StrToDate",
	"TO_TIMESTAMP":           "UnixToTime",
	"UNNEST":                 "Explode",
}
var dialectFuncClass_risingwave = map[string]string{
	"DIV":                    "Cast",
	"JSON_EXTRACT_PATH":      "JSONExtract",
	"LEVENSHTEIN_LESS_EQUAL": "Levenshtein",
	"NOW":                    "CurrentTimestamp",
	"TO_CHAR":                "TimeToStr",
	"TO_DATE":                "StrToDate",
	"TO_TIMESTAMP":           "UnixToTime",
	"UNNEST":                 "Explode",
}
var dialectFuncClass_singlestore = map[string]string{
	"APPROX_PERCENTILE":          "ApproxQuantile",
	"BSON_EXTRACT_BIGINT":        "JSONBExtractScalar",
	"BSON_EXTRACT_BSON":          "JSONBExtract",
	"BSON_EXTRACT_DOUBLE":        "JSONBExtractScalar",
	"BSON_EXTRACT_STRING":        "JSONBExtractScalar",
	"CURDATE":                    "CurrentDate",
	"DATE_FORMAT":                "TimeToStr",
	"DAYNAME":                    "TimeToStr",
	"FROM_UNIXTIME":              "UnixToTime",
	"HOUR":                       "Cast",
	"INSTR":                      "Contains",
	"ISNULL":                     "Paren",
	"JSON_ARRAY_CONTAINS_DOUBLE": "JSONArrayContains",
	"JSON_ARRAY_CONTAINS_JSON":   "JSONArrayContains",
	"JSON_ARRAY_CONTAINS_STRING": "JSONArrayContains",
	"JSON_EXTRACT_BIGINT":        "JSONExtractScalar",
	"JSON_EXTRACT_DOUBLE":        "JSONExtractScalar",
	"JSON_EXTRACT_JSON":          "JSONExtract",
	"JSON_EXTRACT_STRING":        "JSONExtractScalar",
	"MICROSECOND":                "Cast",
	"MINUTE":                     "Cast",
	"MONTHNAME":                  "TimeToStr",
	"SECOND":                     "Cast",
	"TIME_FORMAT":                "TimeToStr",
	"TO_DATE":                    "TsOrDsToDate",
	"TO_DAYS":                    "Paren",
	"TO_TIMESTAMP":               "StrToTime",
	"VARIANCE":                   "VariancePop",
	"WEEKDAY":                    "Mod",
}
var dialectFuncClass_snowflake = map[string]string{
	"APPROX_PERCENTILE":     "ApproxQuantile",
	"ARRAY_CONSTRUCT":       "Array",
	"ARRAY_GENERATE_RANGE":  "GenerateSeries",
	"ARRAY_SORT":            "SortArray",
	"BITAND":                "BitwiseAnd",
	"BITNOT":                "BitwiseNot",
	"BITOR":                 "BitwiseOr",
	"BITSHIFTLEFT":          "BitwiseLeftShift",
	"BITSHIFTRIGHT":         "BitwiseRightShift",
	"BITXOR":                "BitwiseXor",
	"BIT_AND":               "BitwiseAnd",
	"BIT_NOT":               "BitwiseNot",
	"BIT_OR":                "BitwiseOr",
	"BIT_SHIFTLEFT":         "BitwiseLeftShift",
	"BIT_SHIFTRIGHT":        "BitwiseRightShift",
	"BIT_XOR":               "BitwiseXor",
	"BOOLXOR":               "Xor",
	"DATEADD":               "DateAdd",
	"DAYOFWEEKISO":          "DayOfWeekIso",
	"DIV0":                  "If",
	"DIV0NULL":              "If",
	"EDITDISTANCE":          "Levenshtein",
	"FLATTEN":               "Explode",
	"GETDATE":               "CurrentTimestamp",
	"GET_PATH":              "JSONExtract",
	"GREATEST_IGNORE_NULLS": "Greatest",
	"IFF":                   "If",
	"ILIKE":                 "ILike",
	"LEAST_IGNORE_NULLS":    "Least",
	"LOCALTIMESTAMP":        "CurrentTimestamp",
	"NULLIFZERO":            "If",
	"OBJECT_CONSTRUCT":      "Struct",
	"RLIKE":                 "RegexpLike",
	"SKEW":                  "Skewness",
	"SQUARE":                "Pow",
	"STDDEV_SAMP":           "Stddev",
	"SYSDATE":               "CurrentTimestamp",
	"SYSTIMESTAMP":          "CurrentTimestamp",
	"TIMEADD":               "TimeAdd",
	"TIMEDIFF":              "DateDiff",
	"TIMESTAMPADD":          "DateAdd",
	"TIMESTAMPDIFF":         "DateDiff",
	"TO_TIMESTAMP":          "UnixToTime",
	"TO_TIMESTAMP_LTZ":      "UnixToTime",
	"TO_TIMESTAMP_NTZ":      "UnixToTime",
	"TO_TIMESTAMP_TZ":       "UnixToTime",
	"TRY_PARSE_JSON":        "ParseJSON",
	"TRY_TO_DOUBLE":         "ToDouble",
	"WEEKISO":               "WeekOfYear",
	"WEEKOFYEAR":            "Week",
	"ZEROIFNULL":            "If",
}
var dialectFuncClass_spark = map[string]string{
	"APPROX_PERCENTILE": "ApproxQuantile",
	"BASE64":            "ToBase64",
	"BOOLEAN":           "Cast",
	"COLLECT_LIST":      "ArrayAgg",
	"DATE":              "Cast",
	"DATEADD":           "TimestampAdd",
	"DATE_ADD":          "TimestampAdd",
	"DATE_FORMAT":       "TimeToStr",
	"DOUBLE":            "Cast",
	"FLOAT":             "Cast",
	"FROM_UNIXTIME":     "UnixToStr",
	"GET_JSON_OBJECT":   "JSONExtractScalar",
	"ILIKE":             "ILike",
	"INT":               "Cast",
	"LISTAGG":           "GroupConcat",
	"MAP":               "VarMap",
	"MAP_FROM_ARRAYS":   "Map",
	"PERCENTILE":        "Quantile",
	"PERCENTILE_APPROX": "ApproxQuantile",
	"RLIKE":             "RegexpLike",
	"SEQUENCE":          "GenerateSeries",
	"SHIFTLEFT":         "BitwiseLeftShift",
	"SHIFTRIGHT":        "BitwiseRightShift",
	"SIZE":              "ArraySize",
	"SLICE":             "ArraySlice",
	"STRING":            "Cast",
	"TIMESTAMP":         "Cast",
	"TIMESTAMPADD":      "TimestampAdd",
	"TIMESTAMP_LTZ":     "Cast",
	"TIMESTAMP_NTZ":     "Cast",
	"TO_DATE":           "TsOrDsToDate",
	"TO_TIMESTAMP":      "Cast",
	"TRUNC":             "DateTrunc",
	"TRY_ELEMENT_AT":    "Bracket",
	"UNBASE64":          "FromBase64",
}
var dialectFuncClass_spark2 = map[string]string{
	"APPROX_PERCENTILE": "ApproxQuantile",
	"BASE64":            "ToBase64",
	"BOOLEAN":           "Cast",
	"COLLECT_LIST":      "ArrayAgg",
	"DATE":              "Cast",
	"DATE_FORMAT":       "TimeToStr",
	"DOUBLE":            "Cast",
	"FLOAT":             "Cast",
	"FROM_UNIXTIME":     "UnixToStr",
	"GET_JSON_OBJECT":   "JSONExtractScalar",
	"INT":               "Cast",
	"MAP":               "VarMap",
	"MAP_FROM_ARRAYS":   "Map",
	"PERCENTILE":        "Quantile",
	"PERCENTILE_APPROX": "ApproxQuantile",
	"RLIKE":             "RegexpLike",
	"SEQUENCE":          "GenerateSeries",
	"SHIFTLEFT":         "BitwiseLeftShift",
	"SHIFTRIGHT":        "BitwiseRightShift",
	"SIZE":              "ArraySize",
	"SLICE":             "ArraySlice",
	"STRING":            "Cast",
	"TIMESTAMP":         "Cast",
	"TO_DATE":           "TsOrDsToDate",
	"TO_TIMESTAMP":      "Cast",
	"TRUNC":             "DateTrunc",
	"UNBASE64":          "FromBase64",
}
var dialectFuncClass_sqlite = map[string]string{
	"EDITDIST3": "Levenshtein",
	"STRFTIME":  "TimeToStr",
}
var dialectFuncClass_starrocks = map[string]string{
	"CURDATE":       "CurrentDate",
	"DATE":          "TsOrDsToDate",
	"DATE_FORMAT":   "TimeToStr",
	"FROM_UNIXTIME": "UnixToTime",
	"ISNULL":        "Paren",
	"MONTHNAME":     "TimeToStr",
	"REGEXP":        "RegexpLike",
	"TO_DAYS":       "Paren",
}
var dialectFuncClass_tableau = map[string]string{
	"COUNTD":  "Count",
	"FIND":    "StrPosition",
	"FINDNTH": "StrPosition",
}
var dialectFuncClass_teradata = map[string]string{
	"CARDINALITY": "ArraySize",
}
var dialectFuncClass_trino = map[string]string{
	"APPROX_PERCENTILE":    "ApproxQuantile",
	"ARBITRARY":            "AnyValue",
	"BITWISE_AND":          "BitwiseAnd",
	"BITWISE_NOT":          "BitwiseNot",
	"BITWISE_OR":           "BitwiseOr",
	"BITWISE_XOR":          "BitwiseXor",
	"CARDINALITY":          "ArraySize",
	"CONTAINS":             "ArrayContains",
	"DATE_FORMAT":          "TimeToStr",
	"DATE_PARSE":           "StrToTime",
	"DAY_OF_WEEK":          "DayOfWeekIso",
	"DOW":                  "DayOfWeekIso",
	"DOY":                  "DayOfYear",
	"ELEMENT_AT":           "Bracket",
	"FROM_UNIXTIME":        "UnixToTime",
	"LEVENSHTEIN_DISTANCE": "Levenshtein",
	"NOW":                  "CurrentTimestamp",
	"ROW":                  "Struct",
	"SEQUENCE":             "GenerateSeries",
	"SLICE":                "ArraySlice",
	"TO_CHAR":              "TimeToStr",
	"WEEK":                 "WeekOfYear",
}
var dialectFuncClass_tsql = map[string]string{
	"COUNT_BIG":         "Count",
	"DATEADD":           "DateAdd",
	"DATEDIFF_BIG":      "DateDiff",
	"DATENAME":          "TimeToStr",
	"EOMONTH":           "LastDay",
	"FORMAT":            "TimeToStr",
	"GETDATE":           "CurrentTimestamp",
	"ISNULL":            "Coalesce",
	"JSON_QUERY":        "JSONExtract",
	"JSON_VALUE":        "JSONExtractScalar",
	"SQUARE":            "Pow",
	"SYSDATETIME":       "CurrentTimestamp",
	"SYSDATETIMEOFFSET": "CurrentTimestampLTZ",
}

// dialect classSpec overrides
var dialectClassSpec_databricks = map[string]string{
	"Atan2":           "R|DOUBLE",
	"Coalesce":        "A|byargs|this,expressions|promote",
	"Concat":          "A|similar|expressions",
	"CurrentTimezone": "R|VARCHAR",
	"Encode":          "R|BINARY",
	"If":              "A|byargs|true,false|promote",
	"Localtimestamp":  "R|TIMESTAMPNTZ",
	"Pad":             "A|similar|this,fill_pattern",
	"StrToUnix":       "R|BIGINT",
	"Substring":       "A|byargs|this",
	"Tanh":            "R|DOUBLE",
}
var dialectClassSpec_doris = map[string]string{
	"Elt":       "R|VARCHAR",
	"Localtime": "R|DATETIME",
}
var dialectClassSpec_dune = map[string]string{
	"Ceil":  "A|byargs|this",
	"Floor": "A|byargs|this",
	"Mod":   "A|byargs|this,expression",
	"Rand":  "A|byargs|this",
	"Round": "A|byargs|this",
	"Sign":  "A|byargs|this",
}
var dialectClassSpec_fabric = map[string]string{
	"Radians": "A|byargs|this",
}
var dialectClassSpec_hive = map[string]string{
	"Coalesce":  "A|byargs|this,expressions|promote",
	"Encode":    "R|BINARY",
	"If":        "A|byargs|true,false|promote",
	"StrToUnix": "R|BIGINT",
	"Tanh":      "R|DOUBLE",
}
var dialectClassSpec_mysql = map[string]string{
	"Elt":       "R|VARCHAR",
	"Localtime": "R|DATETIME",
}
var dialectClassSpec_presto = map[string]string{
	"Ceil":  "A|byargs|this",
	"Floor": "A|byargs|this",
	"Mod":   "A|byargs|this,expression",
	"Rand":  "A|byargs|this",
	"Round": "A|byargs|this",
	"Sign":  "A|byargs|this",
}
var dialectClassSpec_singlestore = map[string]string{
	"Elt":       "R|VARCHAR",
	"Localtime": "R|DATETIME",
}
var dialectClassSpec_spark = map[string]string{
	"Atan2":           "R|DOUBLE",
	"Coalesce":        "A|byargs|this,expressions|promote",
	"Concat":          "A|similar|expressions",
	"CurrentTimezone": "R|VARCHAR",
	"Encode":          "R|BINARY",
	"If":              "A|byargs|true,false|promote",
	"Localtimestamp":  "R|TIMESTAMPNTZ",
	"Pad":             "A|similar|this,fill_pattern",
	"StrToUnix":       "R|BIGINT",
	"Substring":       "A|byargs|this",
	"Tanh":            "R|DOUBLE",
}
var dialectClassSpec_spark2 = map[string]string{
	"Atan2":     "R|DOUBLE",
	"Coalesce":  "A|byargs|this,expressions|promote",
	"Concat":    "A|similar|expressions",
	"Encode":    "R|BINARY",
	"If":        "A|byargs|true,false|promote",
	"Pad":       "A|similar|this,fill_pattern",
	"StrToUnix": "R|BIGINT",
	"Substring": "A|byargs|this",
	"Tanh":      "R|DOUBLE",
}
var dialectClassSpec_starrocks = map[string]string{
	"Elt":       "R|VARCHAR",
	"Localtime": "R|DATETIME",
}
var dialectClassSpec_trino = map[string]string{
	"Ceil":  "A|byargs|this",
	"Floor": "A|byargs|this",
	"Mod":   "A|byargs|this,expression",
	"Rand":  "A|byargs|this",
	"Round": "A|byargs|this",
	"Sign":  "A|byargs|this",
}
var dialectClassSpec_tsql = map[string]string{
	"Radians": "A|byargs|this",
}

// classExprsAll: classes whose arg_types start with expressions
var classExprsAll = map[string]bool{
	"Array":    true,
	"Chr":      true,
	"Concat":   true,
	"ConcatWs": true,
	"Distinct": true,
	"Struct":   true,
	"Unnest":   true,
}

var dialectFuncClassTables = map[string]map[string]string{
	"bigquery":    dialectFuncClass_bigquery,
	"clickhouse":  dialectFuncClass_clickhouse,
	"databricks":  dialectFuncClass_databricks,
	"doris":       dialectFuncClass_doris,
	"dremio":      dialectFuncClass_dremio,
	"drill":       dialectFuncClass_drill,
	"duckdb":      dialectFuncClass_duckdb,
	"dune":        dialectFuncClass_dune,
	"exasol":      dialectFuncClass_exasol,
	"fabric":      dialectFuncClass_fabric,
	"hive":        dialectFuncClass_hive,
	"materialize": dialectFuncClass_materialize,
	"mysql":       dialectFuncClass_mysql,
	"oracle":      dialectFuncClass_oracle,
	"postgres":    dialectFuncClass_postgres,
	"presto":      dialectFuncClass_presto,
	"prql":        dialectFuncClass_prql,
	"redshift":    dialectFuncClass_redshift,
	"risingwave":  dialectFuncClass_risingwave,
	"singlestore": dialectFuncClass_singlestore,
	"snowflake":   dialectFuncClass_snowflake,
	"spark":       dialectFuncClass_spark,
	"spark2":      dialectFuncClass_spark2,
	"sqlite":      dialectFuncClass_sqlite,
	"starrocks":   dialectFuncClass_starrocks,
	"tableau":     dialectFuncClass_tableau,
	"teradata":    dialectFuncClass_teradata,
	"trino":       dialectFuncClass_trino,
	"tsql":        dialectFuncClass_tsql,
}
var dialectClassSpecTables = map[string]map[string]string{
	"databricks":  dialectClassSpec_databricks,
	"doris":       dialectClassSpec_doris,
	"dune":        dialectClassSpec_dune,
	"fabric":      dialectClassSpec_fabric,
	"hive":        dialectClassSpec_hive,
	"mysql":       dialectClassSpec_mysql,
	"presto":      dialectClassSpec_presto,
	"singlestore": dialectClassSpec_singlestore,
	"spark":       dialectClassSpec_spark,
	"spark2":      dialectClassSpec_spark2,
	"starrocks":   dialectClassSpec_starrocks,
	"trino":       dialectClassSpec_trino,
	"tsql":        dialectClassSpec_tsql,
}

// parserTypeMap / parserTypeDropParams
var parserTypeMap = map[string]map[string]string{
	"athena": {
		"BLOB": "VARBINARY",
	},
	"base": {
		"BLOB": "VARBINARY",
	},
	"bigquery": {
		"BLOB":      "VARBINARY",
		"DATETIME":  "TIMESTAMP",
		"TIMESTAMP": "TIMESTAMPTZ",
	},
	"clickhouse": {
		"BLOB": "VARBINARY",
	},
	"databricks": {
		"BLOB":      "VARBINARY",
		"TIMESTAMP": "TIMESTAMPTZ",
	},
	"doris": {
		"TIMESTAMP": "TIMESTAMPTZ",
	},
	"dremio": {
		"BLOB": "VARBINARY",
	},
	"drill": {
		"BLOB": "VARBINARY",
	},
	"druid": {
		"BLOB": "VARBINARY",
	},
	"duckdb": {
		"BINARY":    "VARBINARY",
		"BLOB":      "VARBINARY",
		"BPCHAR":    "TEXT",
		"CHAR":      "TEXT",
		"DATETIME":  "TIMESTAMPNTZ",
		"TIMESTAMP": "TIMESTAMPNTZ",
		"VARCHAR":   "TEXT",
	},
	"dune": {
		"BLOB": "VARBINARY",
	},
	"exasol": {
		"BLOB": "VARBINARY",
	},
	"fabric": {
		"BLOB":    "VARBINARY",
		"TINYINT": "UTINYINT",
	},
	"hive": {
		"BLOB": "VARBINARY",
	},
	"materialize": {
		"BLOB":  "VARBINARY",
		"FLOAT": "DOUBLE",
	},
	"mysql": {
		"TIMESTAMP": "TIMESTAMPTZ",
	},
	"oracle": {
		"BLOB": "VARBINARY",
	},
	"postgres": {
		"BLOB":  "VARBINARY",
		"FLOAT": "DOUBLE",
	},
	"presto": {
		"BLOB": "VARBINARY",
	},
	"prql": {
		"BLOB": "VARBINARY",
	},
	"redshift": {
		"BLOB":  "VARBINARY",
		"FLOAT": "DOUBLE",
	},
	"risingwave": {
		"BLOB":  "VARBINARY",
		"FLOAT": "DOUBLE",
	},
	"snowflake": {
		"BLOB":  "VARBINARY",
		"FLOAT": "DOUBLE",
	},
	"solr": {
		"BLOB": "VARBINARY",
	},
	"spark": {
		"BLOB":      "VARBINARY",
		"TIMESTAMP": "TIMESTAMPTZ",
	},
	"spark2": {
		"BLOB":      "VARBINARY",
		"TIMESTAMP": "TIMESTAMPTZ",
	},
	"sqlite": {
		"BLOB": "VARBINARY",
	},
	"starrocks": {
		"TIMESTAMP": "TIMESTAMPTZ",
	},
	"tableau": {
		"BLOB": "VARBINARY",
	},
	"teradata": {
		"BLOB": "VARBINARY",
	},
	"trino": {
		"BLOB": "VARBINARY",
	},
	"tsql": {
		"BLOB":      "VARBINARY",
		"TIMESTAMP": "ROWVERSION",
		"TINYINT":   "UTINYINT",
	},
}
var parserTypeDropParams = map[string]map[string]bool{
	"duckdb": {
		"BPCHAR":  true,
		"CHAR":    true,
		"TEXT":    true,
		"VARCHAR": true,
	},
}

// DEFAULT_NULL_TYPE per dialect (defaults to UNKNOWN).
var dialectDefaultNullType = map[string]string{
	"bigquery": "BIGINT",
}

// dialectFuncBigInt: per-dialect big_int flag for Count/DateDiff
var baseFuncBigInt = map[string]bool{
	"COUNT": true,
}
var dialectFuncBigInt = map[string]map[string]bool{
	"fabric": {
		"COUNT":        false,
		"COUNT_BIG":    true,
		"DATEDIFF":     false,
		"DATEDIFF_BIG": true,
	},
	"tsql": {
		"COUNT":        false,
		"COUNT_BIG":    true,
		"DATEDIFF":     false,
		"DATEDIFF_BIG": true,
	},
}

// dialectHexIsInteger: dialect.HEX_STRING_IS_INTEGER_TYPE
var dialectHexIsInteger = map[string]bool{
	"bigquery":   true,
	"clickhouse": true,
}
