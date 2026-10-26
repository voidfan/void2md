package core

import (
	"fmt"

	// SQL database clients
	"void2md/dbc/pkg/database/sql/clickhouse"
	"void2md/dbc/pkg/database/sql/duckdb"
	"void2md/dbc/pkg/database/sql/mariadb"
	"void2md/dbc/pkg/database/sql/sqlite"
	// Existing clients will be migrated later
)

// DatabaseType represents supported database types
type DatabaseType string

const (
	// SQL Databases
	MySQL      DatabaseType = "mysql"
	PostgreSQL DatabaseType = "postgresql"
	MariaDB    DatabaseType = "mariadb"
	SQLite     DatabaseType = "sqlite"
	ClickHouse DatabaseType = "clickhouse"
	DuckDB     DatabaseType = "duckdb"

	// NoSQL Databases
	Redis         DatabaseType = "redis"
	MongoDB       DatabaseType = "mongodb"
	Elasticsearch DatabaseType = "elasticsearch"
	Cassandra     DatabaseType = "cassandra"
	Neo4j         DatabaseType = "neo4j"

	// Add more as needed...
)

// OperationType represents different operation types for different databases
type OperationType string

const (
	OpQuery       OperationType = "QUERY"       // SQL SELECT queries
	OpCommand     OperationType = "COMMAND"     // Redis commands, Kafka operations
	OpAggregation OperationType = "AGGREGATION" // MongoDB aggregations
	OpSearch      OperationType = "SEARCH"      // Elasticsearch searches
	OpCypher      OperationType = "CYPHER"      // Neo4j Cypher queries
	OpScan        OperationType = "SCAN"        // Cassandra scans
)

// Operation represents a database operation
type Operation struct {
	Type       OperationType          // QUERY, COMMAND, AGGREGATION, SEARCH, CYPHER, SCAN
	Statement  string                 // The actual operation to execute
	Parameters map[string]interface{} // Optional parameters for prepared statements or operations
}

// ExecuteQueryResponse represents the response from executing a query
type ExecuteQueryResponse struct {
	Columns []string        `json:"columns"`
	Rows    [][]interface{} `json:"rows"`
	Error   string          `json:"error,omitempty"`
}

// Client represents a generic database client
type Client interface {
	// Common methods for all databases
	GetType() DatabaseType
	GetVersion() string
	Ping() error
	Close() error

	// Schema/structure discovery (database-specific implementations)
	GetSchemaContext(tables string) (string, error)

	// Execution methods - route to appropriate implementation
	ExecuteOperation(op Operation) (*ExecuteQueryResponse, error)
}

// DatabaseConfig represents database configuration
type DatabaseConfig struct {
	Type           DatabaseType
	Host           string
	Port           string
	Username       string
	Password       string
	Database       string
	Version        string
	FilePath       string // For embedded databases (SQLite, DuckDB)
	ProjectID      string // For cloud databases
	ServiceAccount string // For cloud authentication
	APIKey         string // For API-based databases
}

// NewClient creates a new database client based on configuration
func NewClient(config DatabaseConfig) (Client, error) {
	switch config.Type {
	case MySQL:
		return nil, fmt.Errorf("MySQL client not migrated yet")
	case PostgreSQL:
		return nil, fmt.Errorf("PostgreSQL client not migrated yet")
	case MariaDB:
		return mariadb.NewMariaDBClient(config)
	case SQLite:
		return sqlite.NewSQLiteClient(config)
	case ClickHouse:
		return clickhouse.NewClickHouseClient(config)
	case DuckDB:
		return duckdb.NewDuckDBClient(config)
	case Redis:
		return nil, fmt.Errorf("Redis client not migrated yet")
	case MongoDB:
		return nil, fmt.Errorf("MongoDB client not migrated yet")
	case Elasticsearch:
		return nil, fmt.Errorf("Elasticsearch client not migrated yet")
	case Cassandra:
		return nil, fmt.Errorf("Cassandra client not implemented yet")
	case Neo4j:
		return nil, fmt.Errorf("Neo4j client not implemented yet")
	default:
		return nil, fmt.Errorf("unsupported database type: %s", config.Type)
	}
}
