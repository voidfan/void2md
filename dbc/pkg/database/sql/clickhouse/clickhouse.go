package clickhouse

import (
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/ClickHouse/clickhouse-go/v2"

	"void2md/dbc/pkg/database/types"
)

// ClickHouseClient represents a ClickHouse database client
type ClickHouseClient struct {
	db       *sql.DB
	version  string
	host     string
	port     string
	username string
	password string
	database string
}

// NewClickHouseClient creates a new ClickHouse client
func NewClickHouseClient(config types.DatabaseConfig) (types.Client, error) {
	// ClickHouse DSN format
	dsn := fmt.Sprintf("tcp://%s:%s?username=%s&password=%s&database=%s",
		config.Host, config.Port, config.Username, config.Password, config.Database)

	db, err := sql.Open("clickhouse", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open ClickHouse connection: %w", err)
	}

	// Test the connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping ClickHouse database: %w", err)
	}

	// Get ClickHouse version
	var version string
	err = db.QueryRow("SELECT version()").Scan(&version)
	if err != nil {
		version = config.Version
	}

	return &ClickHouseClient{
		db:       db,
		version:  version,
		host:     config.Host,
		port:     config.Port,
		username: config.Username,
		password: config.Password,
		database: config.Database,
	}, nil
}

// GetType returns the database type
func (c *ClickHouseClient) GetType() types.DatabaseType {
	return types.ClickHouse
}

// GetVersion returns the database version
func (c *ClickHouseClient) GetVersion() string {
	return c.version
}

// Ping tests the database connection
func (c *ClickHouseClient) Ping() error {
	return c.db.Ping()
}

// Close closes the database connection
func (c *ClickHouseClient) Close() error {
	return c.db.Close()
}

// GetSchemaContext gets schema context for ClickHouse
func (c *ClickHouseClient) GetSchemaContext(tables string) (string, error) {
	var query string
	var args []interface{}

	query = `
	SELECT
		table_name,
		column_name,
		data_type,
		is_nullable
	FROM system.columns
	WHERE database = ?`
	args = []interface{}{c.database}

	if tables != "" {
		tableList := strings.Split(tables, ",")
		placeholder := strings.Repeat("?,", len(tableList)-1) + "?"
		query += " AND table_name IN (" + placeholder + ")"
		for _, table := range tableList {
			args = append(args, strings.TrimSpace(table))
		}
	}

	query += " ORDER BY table_name, position"

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

// ExecuteOperation executes a ClickHouse operation
func (c *ClickHouseClient) ExecuteOperation(op types.Operation) (*types.ExecuteQueryResponse, error) {
	if op.Type != types.OpQuery {
		return &types.ExecuteQueryResponse{Error: "ClickHouse only supports QUERY operations"}, nil
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
