package factory

import (
	"fmt"

	// SQL database clients
	"void2md/dbc/pkg/database/sql/clickhouse"
	"void2md/dbc/pkg/database/sql/mariadb"
	"void2md/dbc/pkg/database/sql/sqlite"

	// "void2md/dbc/pkg/database/sql/duckdb" // Temporarily disabled due to CGO issues

	// NoSQL database clients
	"void2md/dbc/pkg/database/nosql/cassandra"
	"void2md/dbc/pkg/database/nosql/neo4j"

	// Cloud database clients
	"void2md/dbc/pkg/database/cloud/bigquery"
	"void2md/dbc/pkg/database/cloud/snowflake"

	"void2md/dbc/pkg/database/types"
)

// NewClient creates a new database client based on configuration
func NewClient(config types.DatabaseConfig) (types.Client, error) {
	switch config.Type {
	case types.MySQL:
		return nil, fmt.Errorf("MySQL client not migrated yet")
	case types.PostgreSQL:
		return nil, fmt.Errorf("PostgreSQL client not migrated yet")
	case types.MariaDB:
		return mariadb.NewMariaDBClient(config)
	case types.SQLite:
		return sqlite.NewSQLiteClient(config)
	case types.ClickHouse:
		return clickhouse.NewClickHouseClient(config)
	// case types.DuckDB:
	// 	return duckdb.NewDuckDBClient(config)
	case types.Redis:
		return nil, fmt.Errorf("Redis client not migrated yet")
	case types.MongoDB:
		return nil, fmt.Errorf("MongoDB client not migrated yet")
	case types.Elasticsearch:
		return nil, fmt.Errorf("Elasticsearch client not migrated yet")
	case types.Cassandra:
		return cassandra.NewCassandraClient(config)
	case types.Neo4j:
		return neo4j.NewNeo4jClient(config)
	case types.BigQuery:
		return bigquery.NewBigQueryClient(config)
	case types.Snowflake:
		return snowflake.NewSnowflakeClient(config)
	default:
		return nil, fmt.Errorf("unsupported database type: %s", config.Type)
	}
}
