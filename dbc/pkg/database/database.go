package database

import (
	"database/sql"
	"fmt"
	"strings"

	// Database drivers
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
)

// DatabaseType represents supported database types
type DatabaseType string

const (
	MySQL         DatabaseType = "mysql"
	PostgreSQL    DatabaseType = "postgresql"
	Redis         DatabaseType = "redis"
	MongoDB       DatabaseType = "mongodb"
	Elasticsearch DatabaseType = "elasticsearch"
)

// Client represents a generic database client
type Client interface {
	GetSchemaContext(tables string) (string, error)
	GetIntentAwareSchemaContext(intentTables []string) (string, error)
	ExecuteQuery(sql string) (*ExecuteQueryResponse, error)
	GetType() DatabaseType
	GetVersion() string
}

// SQLClient represents a SQL-based database client (MySQL, PostgreSQL)
type SQLClient struct {
	db       *sql.DB
	dbType   DatabaseType
	version  string
	host     string
	port     string
	username string
	password string
	database string
}

// ExecuteQueryResponse represents the response from executing a query
type ExecuteQueryResponse struct {
	Columns []string        `json:"columns"`
	Rows    [][]interface{} `json:"rows"`
}

// NewClient creates a new database client based on type
func NewClient(dbType DatabaseType, host, port, username, password, database, version string) (Client, error) {
	switch dbType {
	case MySQL, PostgreSQL:
		return NewSQLClient(dbType, host, port, username, password, database, version)
	default:
		// For now, only support SQL databases in the current implementation
		return nil, fmt.Errorf("unsupported database type: %s", dbType)
	}
}

// NewSQLClient creates a new SQL database client
func NewSQLClient(dbType DatabaseType, host, port, username, password, database, version string) (Client, error) {
	var dsn string
	var driverName string

	switch dbType {
	case MySQL:
		dsn = fmt.Sprintf("%s:%s@tcp(%s:%s)/%s", username, password, host, port, database)
		driverName = "mysql"
	case PostgreSQL:
		dsn = fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable", host, port, username, password, database)
		driverName = "postgres"
	default:
		return nil, fmt.Errorf("unsupported SQL database type: %s", dbType)
	}

	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s connection: %w", dbType, err)
	}

	// Test the connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping %s database: %w", dbType, err)
	}

	return &SQLClient{
		db:       db,
		dbType:   dbType,
		version:  version,
		host:     host,
		port:     port,
		username: username,
		password: password,
		database: database,
	}, nil
}

// GetType returns the database type
func (c *SQLClient) GetType() DatabaseType {
	return c.dbType
}

// GetVersion returns the database version
func (c *SQLClient) GetVersion() string {
	return c.version
}

// GetSchemaContext gets schema context for SQL databases
func (c *SQLClient) GetSchemaContext(tables string) (string, error) {
	var query string
	var args []interface{}

	switch c.dbType {
	case MySQL:
		query = `
		SELECT
			TABLE_NAME,
			COLUMN_NAME,
			DATA_TYPE,
			IS_NULLABLE
		FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = ?`
		args = []interface{}{c.database}
	case PostgreSQL:
		query = `
		SELECT
			table_name,
			column_name,
			data_type,
			is_nullable
		FROM information_schema.columns
		WHERE table_schema = 'public'`
		args = []interface{}{}
	default:
		return "", fmt.Errorf("unsupported database type: %s", c.dbType)
	}

	if tables != "" {
		tableList := strings.Split(tables, ",")
		placeholder := strings.Repeat("?,", len(tableList)-1) + "?"
		switch c.dbType {
		case MySQL:
			query += " AND TABLE_NAME IN (" + placeholder + ")"
		case PostgreSQL:
			query += " AND table_name IN (" + placeholder + ")"
		}
		for _, table := range tableList {
			args = append(args, strings.TrimSpace(table))
		}
	}

	switch c.dbType {
	case MySQL:
		query += " ORDER BY TABLE_NAME, ORDINAL_POSITION"
	case PostgreSQL:
		query += " ORDER BY table_name, ordinal_position"
	}

	rows, err := c.db.Query(query, args...)
	if err != nil {
		return "", fmt.Errorf("schema query failed: %w", err)
	}
	defer rows.Close()

	// Build schema context string
	var context strings.Builder
	currentTable := ""

	for rows.Next() {
		var tableName, columnName, dataType, isNullable string
		if err := rows.Scan(&tableName, &columnName, &dataType, &isNullable); err != nil {
			return "", fmt.Errorf("failed to scan schema row: %w", err)
		}

		if tableName != currentTable {
			if currentTable != "" {
				context.WriteString("\n")
			}
			context.WriteString(fmt.Sprintf("Table %s:", tableName))
			currentTable = tableName
		}

		nullStr := "NOT NULL"
		if isNullable == "YES" || isNullable == "YES" {
			nullStr = "NULL"
		}
		context.WriteString(fmt.Sprintf(" %s (%s %s),", columnName, dataType, nullStr))
	}

	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("error iterating schema rows: %w", err)
	}

	// Clean up the context string
	contextStr := strings.TrimSuffix(context.String(), ",")

	return contextStr, nil
}

// GetIntentAwareSchemaContext gets schema context for specific tables based on intent routing
func (c *SQLClient) GetIntentAwareSchemaContext(intentTables []string) (string, error) {
	if len(intentTables) == 0 {
		// If no specific tables, return full schema context
		return c.GetSchemaContext("")
	}

	// Convert intent tables to comma-separated string for existing GetSchemaContext method
	tablesStr := strings.Join(intentTables, ",")
	return c.GetSchemaContext(tablesStr)
}

// ExecuteQuery executes a SQL query
func (c *SQLClient) ExecuteQuery(sql string) (*ExecuteQueryResponse, error) {
	rows, err := c.db.Query(sql)
	if err != nil {
		return nil, fmt.Errorf("query execution failed: %w", err)
	}
	defer rows.Close()

	// Get column names
	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("failed to get columns: %w", err)
	}

	// Read all rows
	var resultRows [][]interface{}
	for rows.Next() {
		// Create a slice of interface{} to hold the row values
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range columns {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		resultRows = append(resultRows, values)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}

	return &ExecuteQueryResponse{
		Columns: columns,
		Rows:    resultRows,
	}, nil
}