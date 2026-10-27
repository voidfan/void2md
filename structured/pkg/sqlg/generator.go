package sqlg

import (
	"strconv"
	"strings"
)

// NormStrategy corresponds to sqlglot's NormalizationStrategy: parse-time
// identifier case normalization and generation-time case sensitivity (used by
// quote_identifiers).
type NormStrategy int

const (
	// NormCaseSensitive corresponds to CASE_SENSITIVE: case is distinguished, no conversion (mysql/starrocks).
	NormCaseSensitive NormStrategy = iota
	// NormLowercase corresponds to LOWERCASE: **unquoted** identifiers are always lowercased (postgres).
	NormLowercase
	// NormUppercase corresponds to UPPERCASE: **unquoted** identifiers are always uppercased (oracle).
	NormUppercase
	// NormCaseInsensitive corresponds to CASE_INSENSITIVE: case is ignored, **lowercased whether quoted or not** (sqlite/duckdb/tsql).
	NormCaseInsensitive
)

// Dialect is the render-time dialect configuration, corresponding to sqlglot's Dialect + Generator.
//
// It only carries the differences that structured actually reaches; everything else uses defaults.
type Dialect struct {
	// Name is the dialect name (lowercase), e.g. "mysql".
	Name string
	// Norm is the identifier normalization strategy.
	Norm NormStrategy
	// QuoteStart/QuoteEnd are the identifier quote characters, defaulting to " and ".
	QuoteStart byte
	QuoteEnd   byte
	// TableAliasAS decides whether table/subquery aliases are joined with AS (false for Oracle).
	TableAliasAS bool
	// FetchFirst, when true, renders LIMIT n as FETCH FIRST n ROWS ONLY (Oracle).
	FetchFirst bool
	// Reserved corresponds to Generator.RESERVED_KEYWORDS.
	Reserved map[string]bool
	// EscapedSequences corresponds to dialect.ESCAPED_SEQUENCES.
	EscapedSequences map[rune]string
	// TablesReferenceableAsColumns corresponds to dialect.TABLES_REFERENCEABLE_AS_COLUMNS
	// (among the dialects structured uses, only postgres has it True).
	TablesReferenceableAsColumns bool
	// DisablesAliasRefExpansion corresponds to dialect.DISABLES_ALIAS_REF_EXPANSION
	// (among the dialects structured uses, only oracle has it True).
	DisablesAliasRefExpansion bool
	// ProjectionAliasesShadowSourceNames corresponds to
	// dialect.PROJECTION_ALIASES_SHADOW_SOURCE_NAMES (always False among the dialects structured uses).
	ProjectionAliasesShadowSourceNames bool
}

// genericDialect is the fallback for unlisted dialects (sqlglot's base Dialect,
// normalization LOWERCASE).
var genericDialect = &Dialect{
	Name: "generic", Norm: NormLowercase,
	QuoteStart: '"', QuoteEnd: '"', TableAliasAS: true,
}

var dialectCache = map[string]*Dialect{}

// GetDialect returns the dialect configuration; unrecognized names get a
// fallback configuration cached by name.
func GetDialect(name string) *Dialect {
	if d, ok := dialectCache[name]; ok {
		return d
	}
	d := buildDialect(name)
	dialectCache[name] = d
	return d
}

// normStrategyByName mirrors each Dialect subclass's normalization_strategy;
// unlisted names fall back to genericDialect (LOWERCASE).
var normStrategyByName = map[string]NormStrategy{
	"athena": NormLowercase, "bigquery": NormCaseInsensitive,
	"clickhouse": NormCaseSensitive, "databricks": NormCaseInsensitive,
	"doris": NormCaseSensitive, "dremio": NormLowercase,
	"drill": NormLowercase, "druid": NormLowercase,
	"duckdb": NormCaseInsensitive, "dune": NormCaseInsensitive,
	"exasol": NormUppercase, "fabric": NormCaseSensitive,
	"hive": NormCaseInsensitive, "materialize": NormLowercase,
	"mysql": NormCaseSensitive, "oracle": NormUppercase,
	"postgres": NormLowercase, "presto": NormCaseInsensitive,
	"prql": NormLowercase, "redshift": NormCaseInsensitive,
	"risingwave": NormLowercase, "singlestore": NormCaseSensitive,
	"snowflake": NormUppercase, "solr": NormCaseInsensitive,
	"spark": NormCaseInsensitive, "spark2": NormCaseInsensitive,
	"sqlite": NormCaseInsensitive, "starrocks": NormCaseSensitive,
	"tableau": NormLowercase, "teradata": NormLowercase,
	"trino": NormCaseInsensitive, "tsql": NormCaseInsensitive,
}

func buildDialect(name string) *Dialect {
	d := *genericDialect
	d.Name = name
	if s, ok := normStrategyByName[name]; ok {
		d.Norm = s
	}
	switch name {
	// Quotes follow IDENTIFIER_START/END (the tokenizer IDENTIFIERS list's first
	// item). Caveat: clickhouse accepts backticks but its first item is `"`, so
	// it is NOT grouped with the backtick dialects.
	case "mysql", "starrocks", "hive", "spark", "spark2", "doris", "databricks":
		d.QuoteStart, d.QuoteEnd = '`', '`'
	case "tsql", "fabric", "tableau":
		d.QuoteStart, d.QuoteEnd = '[', ']'
	}
	if name == "oracle" {
		d.TableAliasAS = false
		d.FetchFirst = true
		d.DisablesAliasRefExpansion = true
	}
	if name == "postgres" {
		d.TablesReferenceableAsColumns = true
	}
	switch name {
	case "mysql":
		d.Reserved = reservedMysql
		d.EscapedSequences = escapedSequencesMysql
	case "starrocks":
		d.Reserved = reservedStarrocks
		d.EscapedSequences = escapedSequencesMysql
	case "duckdb":
		d.Reserved = reservedDuckdb
	}
	return &d
}

// NormalizeIdentifier replicates dialect.normalize_identifier: CASE_SENSITIVE
// never converts; CASE_INSENSITIVE lowercases even quoted; LOWERCASE/UPPERCASE
// convert only unquoted.
func (d *Dialect) NormalizeIdentifier(name string, quoted bool) string {
	switch d.Norm {
	case NormCaseInsensitive:
		return strings.ToLower(name)
	case NormLowercase:
		if quoted {
			return name
		}
		return strings.ToLower(name)
	case NormUppercase:
		if quoted {
			return name
		}
		return strings.ToUpper(name)
	default: // NormCaseSensitive
		return name
	}
}

// CaseSensitive replicates dialect.case_sensitive: CASE_INSENSITIVE always
// false; UPPERCASE sensitive iff text contains lowercase; CASE_SENSITIVE/
// LOWERCASE sensitive iff text contains uppercase.
func (d *Dialect) CaseSensitive(text string) bool {
	switch d.Norm {
	case NormCaseInsensitive:
		return false
	case NormUppercase:
		for _, r := range text {
			if r >= 'a' && r <= 'z' {
				return true
			}
		}
		return false
	default: // NormCaseSensitive / NormLowercase
		for _, r := range text {
			if r >= 'A' && r <= 'Z' {
				return true
			}
		}
		return false
	}
}

// safeIdentifierRe corresponds to exp.SAFE_IDENTIFIER_RE = ^[_a-zA-Z][\w]*$.
func safeIdentifierRe(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		isAlpha := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_'
		if i == 0 {
			if !isAlpha {
				return false
			}
			continue
		}
		if isAlpha || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

// CanQuote replicates dialect.can_quote. identify sentinel values:
// "true" = always quote (except function names); "safe" = quote only
// case-insensitive safe identifiers; "unsafe" = quote only case-sensitive or
// unsafe ones (structured's optimize(identify=False) maps to "unsafe").
func (d *Dialect) CanQuote(ident *Ident, identify string) bool {
	if ident.Quoted {
		return true
	}
	if identify == "" {
		return false
	}
	if _, isFunc := ident.Parent().(*Func); isFunc {
		return false
	}
	if identify == "true" {
		return true
	}
	isSafe := !d.CaseSensitive(ident.Name) && safeIdentifierRe(ident.Name)
	if identify == "safe" {
		return isSafe
	}
	return !isSafe
}

// identifiersCanStartWithDigit records IDENTIFIERS_CAN_START_WITH_DIGIT
// (default False; MySQL family overrides to True).
func (d *Dialect) identifiersCanStartWithDigit() bool {
	switch d.Name {
	case "mysql", "starrocks", "doris", "hive", "spark", "spark2", "clickhouse", "duckdb", "drill", "solr":
		return true
	}
	return false
}

// ---- Generator: corresponds to Generator's various *_sql handlers ----

// renderIdent replicates Generator.identifier_sql.
func (d *Dialect) renderIdent(n *Ident) string {
	text := n.Name
	// self.normalize defaults to False (identify is not enabled), so no case normalization here
	text = strings.ReplaceAll(text, string(d.QuoteEnd), string(d.QuoteEnd)+string(d.QuoteEnd))
	if n.Quoted ||
		d.Reserved[strings.ToLower(text)] ||
		(!d.identifiersCanStartWithDigit() && len(text) > 0 && text[0] >= '0' && text[0] <= '9') {
		return string(d.QuoteStart) + text + string(d.QuoteEnd)
	}
	return text
}

// renderTable replicates Generator.table_sql (Oracle passes sep=" " via TRANSFORMS).
func (d *Dialect) renderTable(n *Table) string {
	parts := make([]string, 0, len(n.Parts))
	for _, p := range n.Parts {
		parts = append(parts, p.SQL(d))
	}
	table := strings.Join(parts, ".")
	if n.Alias != nil {
		if d.TableAliasAS {
			return table + " AS " + n.Alias.SQL(d)
		}
		return table + " " + n.Alias.SQL(d)
	}
	return table
}

// renderTableAlias replicates Generator.tablealias_sql.
func (d *Dialect) renderTableAlias(n *TableAlias) string {
	alias := ""
	if n.Name != nil {
		alias = n.Name.SQL(d)
	}
	if len(n.Columns) == 0 {
		return alias
	}
	cols := make([]string, 0, len(n.Columns))
	for _, c := range n.Columns {
		cols = append(cols, c.SQL(d))
	}
	return alias + "(" + strings.Join(cols, ", ") + ")"
}

// renderColumn replicates Generator.column_sql.
func (d *Dialect) renderColumn(n *Column) string {
	if n.Table != nil && n.Name != nil {
		return n.Table.SQL(d) + "." + n.Name.SQL(d)
	}
	if n.Name != nil {
		return n.Name.SQL(d)
	}
	if n.Table != nil {
		return n.Table.SQL(d)
	}
	return ""
}

// renderStar replicates Generator.star_sql.
func (d *Dialect) renderStar(n *Star) string {
	if n.Table != nil {
		return n.Table.SQL(d) + ".*"
	}
	return "*"
}

// renderLiteral replicates Generator.literal_sql + escape_str.
func (d *Dialect) renderLiteral(n *Literal) string {
	text := n.Value
	if !n.IsString {
		return text
	}
	text = d.escapeStr(text)
	return "'" + text + "'"
}

// escapeStr replicates Generator.escape_str (escape_backslash defaults to True, non-pretty mode).
func (d *Dialect) escapeStr(text string) string {
	if len(d.EscapedSequences) > 0 {
		var b strings.Builder
		for _, ch := range text {
			if rep, ok := d.EscapedSequences[ch]; ok {
				b.WriteString(rep)
				continue
			}
			b.WriteRune(ch)
		}
		text = b.String()
	}
	return strings.ReplaceAll(text, "'", "''")
}

// renderAlias replicates Generator.alias_sql.
func (d *Dialect) renderAlias(n *Alias) string {
	s := n.This.SQL(d)
	if n.Alias != nil {
		return s + " AS " + n.Alias.SQL(d)
	}
	return s
}

// renderUnary replicates Generator.not_sql / neg_sql.
func (d *Dialect) renderUnary(n *Unary) string {
	switch n.Op {
	case "NOT":
		return "NOT " + n.This.SQL(d)
	case "-":
		return "-" + n.This.SQL(d)
	case "+":
		return "+" + n.This.SQL(d)
	}
	return n.Op + " " + n.This.SQL(d)
}

// renderBinary replicates Generator.binary; same-operator nesting is flattened,
// so a AND b AND c renders without inner parentheses.
func (d *Dialect) renderBinary(n *Binary) string {
	var out []string
	stack := []Expression{n}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if b, ok := node.(*Binary); ok && b.Op == n.Op {
			// Push right first, then the operator, then left; the pop order is left operator right
			stack = append(stack, b.Right, opSentinel(b.Op), b.Left)
			continue
		}
		out = append(out, node.SQL(d))
	}
	var b strings.Builder
	for _, s := range out {
		if strings.HasPrefix(s, "\x00op\x00") {
			b.WriteString(" ")
			b.WriteString(strings.TrimPrefix(s, "\x00op\x00"))
			b.WriteString(" ")
			continue
		}
		b.WriteString(s)
	}
	return b.String()
}

// opSentinel wraps an operator into a sentinel string to avoid confusing it with real expression text.
func opSentinel(op string) Expression { return &rawSQL{text: "\x00op\x00" + op} }

// rawSQL is a placeholder node used only inside the generator.
type rawSQL struct {
	base
	text string
}

func (n *rawSQL) SQL(*Dialect) string        { return n.text }
func (n *rawSQL) Copy() Expression           { return &rawSQL{text: n.text} }
func (n *rawSQL) Children() []Expression     { return nil }
func (n *rawSQL) SetChildren(_ []Expression) {}

// renderIs replicates Generator.is_sql (IS_BOOL_ALLOWED is True in all dialects structured uses).
func (d *Dialect) renderIs(n *Is) string {
	prefix := ""
	if n.Not {
		prefix = "NOT "
	}
	rhs := "NULL"
	if n.Expr != nil {
		rhs = n.Expr.SQL(d)
	}
	return prefix + n.This.SQL(d) + " IS " + rhs
}

// renderIn replicates Generator.in_sql.
func (d *Dialect) renderIn(n *In) string {
	prefix := ""
	if n.Not {
		prefix = "NOT "
	}
	if n.Query != nil {
		return prefix + n.This.SQL(d) + " IN " + n.Query.SQL(d)
	}
	parts := make([]string, 0, len(n.List))
	for _, e := range n.List {
		parts = append(parts, e.SQL(d))
	}
	return prefix + n.This.SQL(d) + " IN (" + strings.Join(parts, ", ") + ")"
}

// renderExists replicates Generator.exists_sql.
func (d *Dialect) renderExists(n *Exists) string {
	prefix := ""
	if n.Not {
		prefix = "NOT "
	}
	inner := ""
	if n.This != nil {
		inner = n.This.SQL(d)
	}
	// sqlglot's exists_sql is `EXISTS{this}`; the subquery brings its own parentheses, so there is no space in between.
	return prefix + "EXISTS" + inner
}

// renderBetween replicates Generator.between_sql (without SYMMETRIC).
func (d *Dialect) renderBetween(n *Between) string {
	prefix := ""
	if n.Not {
		prefix = "NOT "
	}
	return prefix + n.This.SQL(d) + " BETWEEN " + n.Low.SQL(d) + " AND " + n.High.SQL(d)
}

// renderCase replicates Generator.case_sql (non-pretty).
func (d *Dialect) renderCase(n *Case) string {
	stmts := []string{"CASE"}
	if n.Operand != nil {
		stmts[0] = "CASE " + n.Operand.SQL(d)
	}
	for _, w := range n.Ifs {
		stmts = append(stmts, "WHEN "+w.Cond.SQL(d), "THEN "+w.Then.SQL(d))
	}
	if n.Default != nil {
		stmts = append(stmts, "ELSE "+n.Default.SQL(d))
	}
	stmts = append(stmts, "END")
	return strings.Join(stmts, " ")
}

// renderTypeName replicates the type-name portion of Generator.datatype_sql.
func (d *Dialect) renderTypeName(name string) string {
	base, size := splitTypeSize(name)
	mapped, dropSize := d.mapTypeName(base)
	if size == "" || dropSize {
		return mapped
	}
	return mapped + "(" + size + ")"
}

// renderCastTypeName replicates the MySQL-family CAST_MAPPING: inside CAST only,
// VARCHAR/TEXT/BLOB → CHAR and integers → SIGNED (StarRocks overrides with an
// empty table, so it renders VARCHAR).
func (d *Dialect) renderCastTypeName(name string) string {
	base, size := splitTypeSize(name)
	if d.Name == "mysql" {
		switch strings.ToUpper(base) {
		case "LONGTEXT", "LONGBLOB", "MEDIUMBLOB", "MEDIUMTEXT", "TEXT",
			"TINYBLOB", "TINYTEXT", "VARCHAR":
			base = "CHAR"
		case "BIGINT", "BOOLEAN", "INT", "SMALLINT", "TINYINT", "MEDIUMINT":
			base = "SIGNED"
		}
	}
	if size == "" {
		return d.renderTypeName(base)
	}
	return d.renderTypeName(base + "(" + size + ")")
}

func splitTypeSize(name string) (string, string) {
	if i := strings.IndexByte(name, '('); i >= 0 {
		return strings.TrimSpace(name[:i]), strings.TrimSpace(strings.TrimSuffix(name[i+1:], ")"))
	}
	return name, ""
}

// mapTypeName applies the dialect's TYPE_MAPPING (common structured types only).
// The bool result means "drop the length parameter after mapping" (duckdb's TEXT
// takes no length; sqlite's TEXT(n) keeps it).
func (d *Dialect) mapTypeName(t string) (string, bool) {
	up := strings.ToUpper(t)
	switch d.Name {
	case "mysql", "starrocks":
		switch up {
		case "TIMESTAMP", "TIMESTAMPNTZ":
			return "DATETIME", false
		}
	case "postgres":
		switch up {
		case "DATETIME":
			return "TIMESTAMP", false
		case "DOUBLE":
			return "DOUBLE PRECISION", false
		case "TINYINT":
			return "SMALLINT", false
		case "BLOB", "BINARY", "VARBINARY", "ROWVERSION":
			return "BYTEA", false
		}
	case "sqlite":
		switch up {
		case "INT", "BIGINT", "SMALLINT", "TINYINT", "BOOLEAN":
			return "INTEGER", false
		case "FLOAT", "DOUBLE", "DECIMAL":
			return "REAL", false
		case "CHAR", "VARCHAR", "NCHAR", "NVARCHAR":
			return "TEXT", false
		case "BINARY", "VARBINARY":
			return "BLOB", false
		}
	case "duckdb":
		switch up {
		case "VARCHAR", "CHAR", "BPCHAR", "NCHAR", "NVARCHAR":
			return "TEXT", true
		case "BINARY", "VARBINARY":
			return "BLOB", true
		case "FLOAT":
			return "REAL", false
		}
	case "oracle":
		switch up {
		case "VARCHAR":
			return "VARCHAR2", false
		case "NVARCHAR":
			return "NVARCHAR2", false
		case "DECIMAL":
			return "NUMBER", false
		case "TINYINT":
			return "SMALLINT", false
		case "BIGINT":
			return "INT", false
		case "TEXT":
			return "CLOB", false
		case "DOUBLE":
			return "DOUBLE PRECISION", false
		}
	}
	return up, false
}

// ---------------------------------------------------------------------------
// duckdb generation-time promotion of VARCHAR / BLOB
// ---------------------------------------------------------------------------

// duckdbCastToVarchar corresponds to duckdb.py's _cast_to_varchar: wrap in a
// CAST only when already type-annotated and not VARCHAR/UNKNOWN (renders as
// CAST(x AS TEXT)). `rendered` is the caller-rendered argument text, matching
// sqlglot's self.sql(expression, "this") position.
func (d *Dialect) duckdbCastToVarchar(arg Expression, rendered string) string {
	if isNilExpr(arg) || arg.Type() == nil || IsTypeOf(arg, "VARCHAR", "UNKNOWN") {
		return rendered
	}
	return "CAST(" + rendered + " AS " + d.renderCastTypeName("VARCHAR") + ")"
}

// duckdbWrapBlob corresponds to _gen_with_cast_to_blob: when the whole node is
// annotated as a binary type, wrap the **entire rendered result** in another
// CAST ... AS BLOB (note it wraps the result, not the arguments).
func (d *Dialect) duckdbWrapBlob(n Expression, rendered string) string {
	if !IsTypeOf(n, "BINARY", "VARBINARY", "BLOB") {
		return rendered
	}
	return "CAST(" + rendered + " AS " + d.renderCastTypeName("BLOB") + ")"
}

// renderFuncDuckDBVarcharCast replicates duckdb.py's UPPER/LOWER/TRIM/REPLACE
// overrides (cast args to VARCHAR, then wrap in BLOB cast); ok=false means
// generic rendering. Unlike sqlglot's trim_sql it never rewrites the AST.
func (d *Dialect) renderFuncDuckDBVarcharCast(
	n *Func, renderArg func(Expression) string,
) (string, bool) {
	name := strings.ToUpper(n.Name)
	switch {
	case name == "UPPER" || name == "LOWER":
		if len(n.Args) != 1 {
			return "", false
		}
		inner := d.duckdbCastToVarchar(n.Args[0], renderArg(n.Args[0]))
		return d.duckdbWrapBlob(n, name+"("+inner+")"), true
	case name == "TRIM":
		if len(n.Args) != 1 {
			return "", false
		}
		inner := d.duckdbCastToVarchar(n.Args[0], renderArg(n.Args[0]))
		return d.duckdbWrapBlob(n, "TRIM("+inner+")"), true
	case name == "REPLACE":
		if len(n.Args) != 3 {
			return "", false
		}
		parts := make([]string, 0, len(n.Args))
		for _, a := range n.Args {
			parts = append(parts, d.duckdbCastToVarchar(a, renderArg(a)))
		}
		return d.duckdbWrapBlob(n, "REPLACE("+strings.Join(parts, ", ")+")"), true
	}
	return "", false
}

// renderFunc replicates Generator.function_fallback_sql + func.
func (d *Dialect) renderFunc(n *Func) string {
	return d.renderFuncWith(n, func(e Expression) string { return e.SQL(d) })
}

// renderFuncWith is the shared function-rendering implementation; the caller
// supplies the argument renderer (compact vs pretty), everything else matches
// sqlglot's single generator with a pretty flag.
func (d *Dialect) renderFuncWith(n *Func, renderArg func(Expression) string) string {
	// duckdb has generator overrides for UPPER / LOWER / TRIM / REPLACE (see below).
	if d.Name == "duckdb" {
		if out, ok := d.renderFuncDuckDBVarcharCast(n, renderArg); ok {
			return out
		}
	}
	// sqlite's TRANSFORMS turns CONCAT into chained DPipe (`a || '-' || b`),
	// flattened without parentheses by Generator.binary.
	if d.Name == "sqlite" && strings.EqualFold(n.Name, "CONCAT") && len(n.Args) >= 2 {
		parts := make([]string, 0, len(n.Args))
		for _, a := range n.Args {
			parts = append(parts, renderArg(a))
		}
		return strings.Join(parts, " || ")
	}
	args := make([]string, 0, len(n.Args)+1)
	if n.Distinct && len(n.Args) > 0 {
		args = append(args, "DISTINCT "+renderArg(n.Args[0]))
		for _, a := range n.Args[1:] {
			args = append(args, renderArg(a))
		}
	} else {
		for _, a := range n.Args {
			args = append(args, renderArg(a))
		}
	}
	if n.Star {
		args = append(args, "*")
	}
	name := strings.ToUpper(n.Name)
	var b strings.Builder
	b.WriteString(name)
	b.WriteString("(")
	b.WriteString(strings.Join(args, ", "))
	b.WriteString(")")
	if n.Filter != nil {
		b.WriteString(" FILTER(")
		b.WriteString(renderArg(n.Filter))
		b.WriteString(")")
	}
	if n.Over != "" {
		b.WriteString(" OVER (")
		b.WriteString(n.Over)
		b.WriteString(")")
	}
	// Suffix carries tails that follow the same parentheses, such as ORDER BY
	b.WriteString(strings.Join(n.Suffix, " "))
	return b.String()
}

// renderSubquery replicates Generator.subquery_sql (Oracle sep=" ").
func (d *Dialect) renderSubquery(n *Subquery) string {
	inner := ""
	if n.This != nil {
		inner = n.This.SQL(d)
	}
	s := "(" + inner + ")"
	if n.Alias != nil {
		if d.TableAliasAS {
			return s + " AS " + n.Alias.SQL(d)
		}
		return s + " " + n.Alias.SQL(d)
	}
	return s
}

// renderCTE replicates Generator.cte_sql.
func (d *Dialect) renderCTE(n *CTE) string {
	alias := ""
	if n.Alias != nil {
		alias = n.Alias.SQL(d)
	}
	inner := ""
	if n.This != nil {
		inner = n.This.SQL(d)
	}
	return alias + " AS " + inner
}

// renderWithPrefix replicates Generator.with_sql (emits only the WITH prefix, without the main query).
func (d *Dialect) renderWithPrefix(n *With) string {
	parts := make([]string, 0, len(n.CTEs))
	for _, c := range n.CTEs {
		parts = append(parts, c.SQL(d))
	}
	recursive := ""
	if n.Recursive {
		recursive = "RECURSIVE "
	}
	return "WITH " + recursive + strings.Join(parts, ", ")
}

// renderSelect replicates Generator.select_sql + query_modifiers (non-pretty).
func (d *Dialect) renderSelect(n *Select) string {
	var b strings.Builder
	if n.With != nil {
		b.WriteString(n.With.SQL(d))
		b.WriteString(" ")
	}
	b.WriteString("SELECT ")
	if n.Distinct {
		b.WriteString("DISTINCT ")
	}
	parts := make([]string, 0, len(n.Expressions))
	for _, e := range n.Expressions {
		parts = append(parts, e.SQL(d))
	}
	b.WriteString(strings.Join(parts, ", "))
	if n.From != nil {
		b.WriteString(" FROM ")
		b.WriteString(n.From.SQL(d))
	}
	for _, j := range n.Joins {
		// renderJoin already carries the leading separator (`" JOIN ..."` or `", ..."`).
		b.WriteString(j.SQL(d))
	}
	if n.Where != nil {
		b.WriteString(" WHERE ")
		b.WriteString(n.Where.SQL(d))
	}
	if n.Group != nil {
		b.WriteString(" ")
		b.WriteString(n.Group.SQL(d))
	}
	if n.Having != nil {
		b.WriteString(" HAVING ")
		b.WriteString(n.Having.SQL(d))
	}
	if n.Order != nil {
		b.WriteString(" ")
		b.WriteString(n.Order.SQL(d))
	}
	// offset_limit_modifiers: with LIMIT_FETCH == "FETCH" and a limit present,
	// OFFSET comes first; otherwise limit first.
	if d.FetchFirst && n.Limit != nil {
		if n.Offset != nil {
			b.WriteString(" ")
			b.WriteString(n.Offset.SQL(d))
		}
		b.WriteString(" ")
		b.WriteString(n.Limit.SQL(d))
		return b.String()
	}
	if n.Limit != nil {
		b.WriteString(" ")
		b.WriteString(n.Limit.SQL(d))
	}
	if n.Offset != nil {
		b.WriteString(" ")
		b.WriteString(n.Offset.SQL(d))
	}
	return b.String()
}

// joinIsCommaForm corresponds to join_sql's `, this` branch (no ON/USING/
// method/GLOBAL/side/kind/hint). This is render-time inference, not a parse
// marker: `JOIN b` and `, b` produce the same AST and both render as `, b`.
func joinIsCommaForm(n *Join) bool {
	return n.On == nil && len(n.Using) == 0 &&
		strings.ToUpper(n.Side) == "" && strings.ToUpper(n.Kind) == ""
}

// renderJoin replicates Generator.join_sql. The result carries its own leading
// separator (" JOIN b" or ", b"); the caller must not add another space.
func (d *Dialect) renderJoin(n *Join) string {
	side := strings.ToUpper(n.Side)
	kind := strings.ToUpper(n.Kind)

	onSQL := ""
	if n.On != nil {
		onSQL = n.On.SQL(d)
	}
	using := len(n.Using) > 0
	if onSQL == "" && using {
		parts := make([]string, 0, len(n.Using))
		for _, u := range n.Using {
			parts = append(parts, u.SQL(d))
		}
		onSQL = strings.Join(parts, ", ")
	}

	thisSQL := ""
	if n.This != nil {
		thisSQL = n.This.SQL(d)
	}

	if onSQL != "" {
		if using {
			onSQL = " USING (" + onSQL + ")"
		} else {
			onSQL = " ON " + onSQL
		}
	} else if side == "" && kind == "" {
		// `elif not op_sql: return f", {this_sql}"`
		return ", " + thisSQL
	}

	// op_sql = " ".join(op for op in (method, GLOBAL, side, kind, hint) if op)
	// sqlg has no method/GLOBAL/hint, leaving only the side and kind segments.
	ops := make([]string, 0, 2)
	if side != "" {
		ops = append(ops, side)
	}
	if kind != "" {
		ops = append(ops, kind)
	}
	opSQL := strings.Join(ops, " ")
	if opSQL != "STRAIGHT_JOIN" {
		if opSQL == "" {
			opSQL = "JOIN"
		} else {
			opSQL += " JOIN"
		}
	}
	return " " + opSQL + " " + thisSQL + onSQL
}

// renderUnknown writes back unrecognized statements verbatim.
func (d *Dialect) renderUnknown(n *Unknown) string { return n.Text }

// quoteIdentString quotes an identifier per the dialect (used for scenarios like metadata example-value SQL).
func (d *Dialect) quoteIdentString(name string) string {
	return string(d.QuoteStart) + name + string(d.QuoteEnd)
}

// intExpr constructs an integer Limit expression.
func intExpr(v int) Expression { return &Literal{Value: strconv.Itoa(v)} }
