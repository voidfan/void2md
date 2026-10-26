package sqlite

import (
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3"

	"void2md/dbc/pkg/database/types"
)

// SQLiteClient represents a SQLite database client
type SQLiteClient struct {
	db       *sql.DB
	version  string
	filePath string
}

// NewSQLiteClient creates a new SQLite client
func NewSQLiteClient(config types.DatabaseConfig) (types.Client, error) {
	// Use file path or default to in-memory
	dbPath := config.FilePath
	if dbPath == "" {
		dbPath = ":memory:"
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open SQLite connection: %w", err)
	}

	// Test the connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping SQLite database: %w", err)
	}

	// Get SQLite version
	var version string
	err = db.QueryRow("SELECT sqlite_version()").Scan(&version)
	if err != nil {
		version = "unknown"
	}

	return &SQLiteClient{
		db:       db,
		version:  version,
		filePath: dbPath,
	}, nil
}

// GetType returns the database type
func (c *SQLiteClient) GetType() types.DatabaseType {
	return types.SQLite
}

// GetVersion returns the database version
func (c *SQLiteClient) GetVersion() string {
	return c.version
}

// Ping tests the database connection
func (c *SQLiteClient) Ping() error {
	return c.db.Ping()
}

// Close closes the database connection
func (c *SQLiteClient) Close() error {
	return c.db.Close()
}

// GetSchemaContext gets schema context for SQLite
func (c *SQLiteClient) GetSchemaContext(tables string) (string, error) {
	query := `
	SELECT
		name as table_name,
		sql as table_sql
	FROM sqlite_master
	WHERE type = 'table'`

	rows, err := c.db.Query(query)
	if err != nil {
		return "", fmt.Errorf("schema query failed: %w", err)
	}
	defer rows.Close()

	var context string
	for rows.Next() {
		var tableName, tableSQL string
		if err := rows.Scan(&tableName, &tableSQL); err != nil {
			return "", fmt.Errorf("failed to scan schema row: %w", err)
		}
		context += fmt.Sprintf("Table %s: %s\n", tableName, tableSQL)
	}

	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("error iterating schema rows: %w", err)
	}

	return context, nil
}

// ExecuteOperation executes a SQLite operation
func (c *SQLiteClient) ExecuteOperation(op types.Operation) (*types.ExecuteQueryResponse, error) {
	if op.Type != types.OpQuery {
		return &types.ExecuteQueryResponse{Error: "SQLite only supports QUERY operations"}, nil
	}

	rows, err := c.db.Query(op.Statement)
	if err != nil {
		return &types.ExecuteQueryResponse{Error: err.Error()}, nil
	}
	defer rows.Close()

	// Get column names
	columns, err := rows.Columns()
	if err != nil {
		return &types.ExecuteQueryResponse{Error: fmt.Sprintf("failed to get columns: %v", err)}, nil
	}

	// Read all rows
	var resultRows [][]interface{}
	for rows.Next() {
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range columns {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return &types.ExecuteQueryResponse{Error: fmt.Sprintf("failed to scan row: %v", err)}, nil
		}

		resultRows = append(resultRows, values)
	}

	if err := rows.Err(); err != nil {
		return &types.ExecuteQueryResponse{Error: fmt.Sprintf("error iterating rows: %v", err)}, nil
	}

	return &types.ExecuteQueryResponse{
		Columns: columns,
		Rows:    resultRows,
	}, nil
}
