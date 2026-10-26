package dbx

import (
	"database/sql"
	"fmt"
	"strings"

	// MySQL driver
	_ "github.com/go-sql-driver/mysql"
)

// Client represents a direct MySQL client
type Client struct {
	db       *sql.DB
	host     string
	port     string
	username string
	password string
	database string
}

// NewClient creates a new direct MySQL client
// For this simplified version, we'll use connection info from config
func NewClient(mcpURL, mcpToken string) *Client {
	// Note: In a real implementation, you'd parse connection info from config
	// For now, we'll return a client that needs to be initialized with DB info
	return &Client{}
}

// Initialize sets up the database connection
func (c *Client) Initialize(host, port, username, password, database string) error {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s", username, password, host, port, database)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("failed to open database connection: %w", err)
	}

	// Test the connection
	if err := db.Ping(); err != nil {
		return fmt.Errorf("failed to ping database: %w", err)
	}

	c.db = db
	c.host = host
	c.port = port
	c.username = username
	c.password = password
	c.database = database

	return nil
}

// ExecuteQueryResponse represents the response from executing a query
type ExecuteQueryResponse struct {
	Columns []string        `json:"columns"`
	Rows    [][]interface{} `json:"rows"`
}

// ListConnectionsResponse represents the response from listing connections
type ListConnectionsResponse struct {
	Connections []Connection `json:"connections"`
}

// Connection represents a dbx connection
type Connection struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"db_type"`
}

// GetSchemaContextResponse represents the response from getting schema context
type GetSchemaContextResponse struct {
	Context string `json:"context"`
}

// ExecuteQuery executes a SQL query directly against MySQL
func (c *Client) ExecuteQuery(connectionID, database, sql string) (*ExecuteQueryResponse, error) {
	// For direct MySQL, connectionID and database are ignored (we use configured DB)
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

// ListConnections lists available connections
// For direct MySQL, we'll return a single connection
func (c *Client) ListConnections() (*ListConnectionsResponse, error) {
	return &ListConnectionsResponse{
		Connections: []Connection{
			{
				ID:   "jira-mysql",
				Name: "Jira MySQL",
				Type: "mysql",
			},
		},
	}, nil
}

// GetSchemaContext gets schema context by querying MySQL information_schema
func (c *Client) GetSchemaContext(connectionID, tables string) (*GetSchemaContextResponse, error) {
	// Build query to get table information
	query := `
	SELECT
		TABLE_NAME,
		COLUMN_NAME,
		DATA_TYPE,
		IS_NULLABLE
	FROM INFORMATION_SCHEMA.COLUMNS
	WHERE TABLE_SCHEMA = ?`

	args := []interface{}{c.database}

	if tables != "" {
		// Add table filter
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
		return nil, fmt.Errorf("schema query failed: %w", err)
	}
	defer rows.Close()

	// Build schema context string
	var context strings.Builder
	currentTable := ""

	for rows.Next() {
		var tableName, columnName, dataType, isNullable string
		if err := rows.Scan(&tableName, &columnName, &dataType, &isNullable); err != nil {
			return nil, fmt.Errorf("failed to scan schema row: %w", err)
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
		return nil, fmt.Errorf("error iterating schema rows: %w", err)
	}

	// Clean up the context string
	contextStr := strings.TrimSuffix(context.String(), ",")

	return &GetSchemaContextResponse{
		Context: contextStr,
	}, nil
}