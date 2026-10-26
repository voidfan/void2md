package snowflake

import (
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/snowflakedb/gosnowflake"

	"void2md/dbc/pkg/database/types"
)

// SnowflakeClient represents a Snowflake database client
type SnowflakeClient struct {
	db        *sql.DB
	version   string
	host      string
	username  string
	password  string
	database  string
	warehouse string
	role      string
}

// NewSnowflakeClient creates a new Snowflake client
func NewSnowflakeClient(config types.DatabaseConfig) (types.Client, error) {
	// Snowflake DSN format
	dsn := fmt.Sprintf("%s:%s@%s/%s", config.Username, config.Password, config.Host, config.Database)

	// Add optional parameters
	params := make([]string, 0)
	if config.Warehouse != "" {
		params = append(params, fmt.Sprintf("warehouse=%s", config.Warehouse))
	}
	if config.Role != "" {
		params = append(params, fmt.Sprintf("role=%s", config.Role))
	}

	if len(params) > 0 {
		dsn += "?" + strings.Join(params, "&")
	}

	db, err := sql.Open("snowflake", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open Snowflake connection: %w", err)
	}

	// Test the connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping Snowflake database: %w", err)
	}

	// Get Snowflake version
	var version string
	err = db.QueryRow("SELECT current_version()").Scan(&version)
	if err != nil {
		version = config.Version
	}

	return &SnowflakeClient{
		db:        db,
		version:   version,
		host:      config.Host,
		username:  config.Username,
		password:  config.Password,
		database:  config.Database,
		warehouse: config.Warehouse,
		role:      config.Role,
	}, nil
}

// GetType returns the database type
func (c *SnowflakeClient) GetType() types.DatabaseType {
	return types.Snowflake
}

// GetVersion returns the database version
func (c *SnowflakeClient) GetVersion() string {
	return c.version
}

// Ping tests the database connection
func (c *SnowflakeClient) Ping() error {
	return c.db.Ping()
}

// Close closes the database connection
func (c *SnowflakeClient) Close() error {
	return c.db.Close()
}

// GetSchemaContext gets schema context for Snowflake
func (c *SnowflakeClient) GetSchemaContext(tables string) (string, error) {
	var query string
	var args []interface{}

	query = `
	SELECT
		table_name,
		column_name,
		data_type,
		is_nullable
	FROM information_schema.columns
	WHERE table_schema = 'PUBLIC'`

	if tables != "" {
		tableList := strings.Split(tables, ",")
		placeholder := strings.Repeat("?,", len(tableList)-1) + "?"
		query += " AND table_name IN (" + placeholder + ")"
		for _, table := range tableList {
			args = append(args, strings.TrimSpace(table))
		}
	}

	query += " ORDER BY table_name, ordinal_position"

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
		if isNullable == "YES" {
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

// ExecuteOperation executes a Snowflake operation
func (c *SnowflakeClient) ExecuteOperation(op types.Operation) (*types.ExecuteQueryResponse, error) {
	if op.Type != types.OpQuery {
		return &types.ExecuteQueryResponse{Error: "Snowflake only supports QUERY operations"}, nil
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
