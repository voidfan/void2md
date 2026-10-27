// Package sqlg is a Go port of sqlglot 28.6.0: SQL lexing/parsing/AST/rendering/optimizer.
// It aims for byte-identical output to Python on the SQL shapes structured processes;
// divergences are measured by differential tests under tools/oracle.
package sqlg

// DialectModuleNames mirrors sqlglot.dialects.DIALECT_MODULE_NAMES (28.6.0).
// Unknown dialects fall back to startswith("SELECT") checks and no-op optimization.
var DialectModuleNames = map[string]bool{
	"athena": true, "bigquery": true, "clickhouse": true, "databricks": true,
	"doris": true, "dremio": true, "drill": true, "druid": true,
	"duckdb": true, "dune": true, "exasol": true, "fabric": true,
	"hive": true, "materialize": true, "mysql": true, "oracle": true,
	"postgres": true, "presto": true, "prql": true, "redshift": true,
	"risingwave": true, "singlestore": true, "snowflake": true, "solr": true,
	"spark": true, "spark2": true, "sqlite": true, "starrocks": true,
	"tableau": true, "teradata": true, "trino": true, "tsql": true,
}

// HasDialect reports whether sqlglot recognizes the dialect.
func HasDialect(name string) bool {
	return DialectModuleNames[name]
}

// usesFetchFirst identifies dialects using FETCH FIRST n ROWS ONLY instead of LIMIT n.
func usesFetchFirst(dialect string) bool {
	switch dialect {
	case "oracle":
		return true
	}
	return false
}
