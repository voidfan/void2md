package types

// DatabaseType represents supported database types
type DatabaseType string

const (
	// SQL Databases
	MySQL         DatabaseType = "mysql"
	PostgreSQL    DatabaseType = "postgresql"
	MariaDB       DatabaseType = "mariadb"
	SQLite        DatabaseType = "sqlite"
	ClickHouse    DatabaseType = "clickhouse"
	DuckDB        DatabaseType = "duckdb"

	// Analytical/OLAP Databases
	Doris         DatabaseType = "doris"
	StarRocks     DatabaseType = "starrocks"
	Exasol        DatabaseType = "exasol"
	Databricks    DatabaseType = "databricks"
	Snowflake     DatabaseType = "snowflake"
	BigQuery      DatabaseType = "bigquery"
	CloudSpanner  DatabaseType = "cloudspanner"
	Trino         DatabaseType = "trino"
	Dremio        DatabaseType = "dremio"
	Kylin         DatabaseType = "kylin"

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
	OpQuery        OperationType = "QUERY"        // SQL SELECT queries
	OpCommand      OperationType = "COMMAND"      // Redis commands, Kafka operations
	OpAggregation  OperationType = "AGGREGATION"  // MongoDB aggregations
	OpSearch       OperationType = "SEARCH"       // Elasticsearch searches
	OpCypher       OperationType = "CYPHER"       // Neo4j Cypher queries
	OpScan         OperationType = "SCAN"         // Cassandra scans
)

// Operation represents a database operation
type Operation struct {
	Type       OperationType           // QUERY, COMMAND, AGGREGATION, SEARCH, CYPHER, SCAN
	Statement  string                  // The actual operation to execute
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
	Type          DatabaseType
	Host          string
	Port          string
	Username      string
	Password      string
	Database      string
	Version       string
	FilePath      string // For embedded databases (SQLite, DuckDB)
	ProjectID     string // For cloud databases
	ServiceAccount string // For cloud authentication
	APIKey        string // For API-based databases
	Keyspace      string // For Cassandra
	Topic         string // For streaming systems
	Warehouse     string // For Snowflake
	Role          string // For Snowflake
}