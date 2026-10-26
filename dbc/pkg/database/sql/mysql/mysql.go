package mysql

import (
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/go-sql-driver/mysql"

	"void2md/dbc/pkg/database/types"
)

// MySQLClient represents a MySQL database client
type MySQLClient struct {
	db       *sql.DB
	version  string
	host     string
	port     string
	username string
	password string
	database string
}

// NewMySQLClient creates a new MySQL client
func NewMySQLClient(config types.DatabaseConfig) (types.Client, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s", config.Username, config.Password, config.Host, config.Port, config.Database)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open MySQL connection: %w", err)
	}

	// Test the connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping MySQL database: %w", err)
	}

	// Get MySQL version
	var version string
	err = db.QueryRow("SELECT VERSION()").Scan(&version)
	if err != nil {
		version = config.Version
	}

	return &MySQLClient{
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
func (c *MySQLClient) GetType() types.DatabaseType {
	return types.MySQL
}

// GetVersion returns the database version
func (c *MySQLClient) GetVersion() string {
	return c.version
}

// Ping tests the database connection
func (c *MySQLClient) Ping() error {
	return c.db.Ping()
}

// Close closes the database connection
func (c *MySQLClient) Close() error {
	return c.db.Close()
}

// GetSchemaContext gets schema context for MySQL
func (c *MySQLClient) GetSchemaContext(tables string) (string, error) {
	var query string
	var args []interface{}

	query = `
	SELECT
		TABLE_NAME,
		COLUMN_NAME,
		DATA_TYPE,
		IS_NULLABLE
	FROM INFORMATION_SCHEMA.COLUMNS
	WHERE TABLE_SCHEMA = ?`
	args = []interface{}{c.database}

	if tables != "" {
		tableList := strings.Split(tables, ",")
		placeholder := strings.Repeat("?,", len(tableList)-1) + "?"
		query += " AND TABLE_NAME IN (" + placeholder + ")"
		for _, table := range tableList {
			args = append(args, strings.TrimSpace(table))
		}
	}

	query += " ORDER BY TABLE_NAME, ORDINAL_POSITION"

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

// ExecuteOperation executes a MySQL operation
func (c *MySQLClient) ExecuteOperation(op types.Operation) (*types.ExecuteQueryResponse, error) {
	if op.Type != types.OpQuery {
		return &types.ExecuteQueryResponse{Error: "MySQL only supports QUERY operations"}, nil
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
