package duckdb

import (
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/marcboeker/go-duckdb"

	"void2md/dbc/pkg/database/types"
)

// DuckDBClient represents a DuckDB database client
type DuckDBClient struct {
	db       *sql.DB
	version  string
	filePath string
}

// NewDuckDBClient creates a new DuckDB client
func NewDuckDBClient(config types.DatabaseConfig) (types.Client, error) {
	// Use file path or default to in-memory
	dbPath := config.FilePath
	if dbPath == "" {
		dbPath = ":memory:"
	}

	db, err := sql.Open("duckdb", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open DuckDB connection: %w", err)
	}

	// Test the connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping DuckDB database: %w", err)
	}

	// Get DuckDB version
	var version string
	err = db.QueryRow("SELECT version()").Scan(&version)
	if err != nil {
		version = config.Version
	}

	return &DuckDBClient{
		db:       db,
		version:  version,
		filePath: dbPath,
	}, nil
}

// GetType returns the database type
func (c *DuckDBClient) GetType() types.DatabaseType {
	return types.DuckDB
}

// GetVersion returns the database version
func (c *DuckDBClient) GetVersion() string {
	return c.version
}

// Ping tests the database connection
func (c *DuckDBClient) Ping() error {
	return c.db.Ping()
}

// Close closes the database connection
func (c *DuckDBClient) Close() error {
	return c.db.Close()
}

// GetSchemaContext gets schema context for DuckDB
func (c *DuckDBClient) GetSchemaContext(tables string) (string, error) {
	var query string

	query = `
	SELECT
		table_name,
		column_name,
		data_type,
		'NO' as is_nullable  -- DuckDB doesn't have nullable info in this view
	FROM duckdb_information_schema.columns`

	if tables != "" {
		tableList := strings.Split(tables, ",")
		placeholder := strings.Repeat("?,", len(tableList)-1) + "?"
		query += " WHERE table_name IN (" + placeholder + ")"
	}

	query += " ORDER BY table_name, ordinal_position"

	rows, err := c.db.Query(query)
	if err != nil {
		// Fallback to sqlite_master if duckdb_information_schema doesn't exist
		query = `
		SELECT
			name as table_name,
			'' as column_name,
			'' as data_type,
			'NO' as is_nullable
		FROM sqlite_master
		WHERE type = 'table'`

		if tables != "" {
			tableList := strings.Split(tables, ",")
			conditions := make([]string, len(tableList))
			for i, table := range tableList {
				conditions[i] = fmt.Sprintf("name = '%s'", strings.TrimSpace(table))
			}
			query += " AND (" + strings.Join(conditions, " OR ") + ")"
		}

		rows, err = c.db.Query(query)
		if err != nil {
			return "", fmt.Errorf("schema query failed: %w", err)
		}
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

		if columnName != "" {
			nullStr := "NOT NULL"
			if isNullable == "YES" {
				nullStr = "NULL"
			}
			context.WriteString(fmt.Sprintf(" %s (%s %s),", columnName, dataType, nullStr))
		}
	}

	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("error iterating schema rows: %w", err)
	}

	// Clean up the context string
	contextStr := strings.TrimSuffix(context.String(), ",")

	return contextStr, nil
}

// ExecuteOperation executes a DuckDB operation
func (c *DuckDBClient) ExecuteOperation(op types.Operation) (*types.ExecuteQueryResponse, error) {
	if op.Type != types.OpQuery {
		return &types.ExecuteQueryResponse{Error: "DuckDB only supports QUERY operations"}, nil
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
