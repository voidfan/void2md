package cassandra

import (
	"fmt"
	"strings"

	"github.com/gocql/gocql"

	"void2md/dbc/pkg/database/types"
)

// CassandraClient represents a Cassandra database client
type CassandraClient struct {
	session  *gocql.Session
	version  string
	host     string
	port     string
	username string
	password string
	keyspace string
}

// NewCassandraClient creates a new Cassandra client
func NewCassandraClient(config types.DatabaseConfig) (types.Client, error) {
	cluster := gocql.NewCluster(config.Host)
	cluster.Port = 9042 // Default Cassandra port
	if config.Port != "" {
		cluster.Port, _ = fmt.Sscan(config.Port)
	}

	if config.Username != "" && config.Password != "" {
		cluster.Authenticator = gocql.PasswordAuthenticator{
			Username: config.Username,
			Password: config.Password,
		}
	}

	if config.Keyspace != "" {
		cluster.Keyspace = config.Keyspace
	}

	session, err := cluster.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create Cassandra session: %w", err)
	}

	var version string
	// Test the connection and get version
	if err := session.Query("SELECT release_version FROM system.local").Scan(&version); err != nil {
		version = config.Version
	}

	return &CassandraClient{
		session:  session,
		version:  version,
		host:     config.Host,
		port:     config.Port,
		username: config.Username,
		password: config.Password,
		keyspace: config.Keyspace,
	}, nil
}

// GetType returns the database type
func (c *CassandraClient) GetType() types.DatabaseType {
	return types.Cassandra
}

// GetVersion returns the database version
func (c *CassandraClient) GetVersion() string {
	return c.version
}

// Ping tests the database connection
func (c *CassandraClient) Ping() error {
	var version string
	return c.session.Query("SELECT release_version FROM system.local").Scan(&version)
}

// Close closes the database connection
func (c *CassandraClient) Close() error {
	c.session.Close()
	return nil
}

// GetSchemaContext gets schema context for Cassandra
func (c *CassandraClient) GetSchemaContext(tables string) (string, error) {
	query := "SELECT keyspace_name, table_name, column_name, kind, type FROM system_schema.columns"

	if c.keyspace != "" {
		query += fmt.Sprintf(" WHERE keyspace_name = '%s'", c.keyspace)
	}

	if tables != "" {
		tableList := strings.Split(tables, ",")
		conditions := make([]string, len(tableList))
		for i, table := range tableList {
			conditions[i] = fmt.Sprintf("table_name = '%s'", strings.TrimSpace(table))
		}
		if c.keyspace != "" {
			query += " AND (" + strings.Join(conditions, " OR ") + ")"
		} else {
			query += " WHERE (" + strings.Join(conditions, " OR ") + ")"
		}
	}

	iter := c.session.Query(query).Iter()

	var context strings.Builder
	currentTable := ""

	var keyspaceName, tableName, columnName, kind, dataType string
	for iter.Scan(&keyspaceName, &tableName, &columnName, &kind, &dataType) {
		if tableName != currentTable {
			if currentTable != "" {
				context.WriteString("\n")
			}
			context.WriteString(fmt.Sprintf("Table %s.%s:", keyspaceName, tableName))
			currentTable = tableName
		}

		context.WriteString(fmt.Sprintf(" %s (%s %s),", columnName, dataType, kind))
	}

	if err := iter.Close(); err != nil {
		return "", fmt.Errorf("error iterating schema rows: %w", err)
	}

	// Clean up the context string
	contextStr := strings.TrimSuffix(context.String(), ",")

	return contextStr, nil
}

// ExecuteOperation executes a Cassandra operation
func (c *CassandraClient) ExecuteOperation(op types.Operation) (*types.ExecuteQueryResponse, error) {
	if op.Type != types.OpQuery && op.Type != types.OpScan {
		return &types.ExecuteQueryResponse{Error: "Cassandra only supports QUERY and SCAN operations"}, nil
	}

	// For Cassandra, we'll treat both QUERY and SCAN as CQL queries
	iter := c.session.Query(op.Statement).Iter()

	// Get column names from the first row (Cassandra doesn't provide column metadata easily)
	// We'll use generic column names
	columns := []string{"col_0", "col_1", "col_2", "col_3", "col_4", "col_5", "col_6", "col_7", "col_8", "col_9"}

	var resultRows [][]interface{}
	rowCount := 0

	for {
		// Create a slice to hold row values (max 10 columns for simplicity)
		values := make([]interface{}, 10)
		valuePtrs := make([]interface{}, 10)
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if !iter.Scan(valuePtrs...) {
			break
		}

		// Trim unused columns
		actualValues := make([]interface{}, 0, 10)
		for _, val := range values {
			if val != nil {
				actualValues = append(actualValues, val)
			}
		}

		resultRows = append(resultRows, actualValues)
		rowCount++

		// Limit results to prevent memory issues
		if rowCount >= 1000 {
			break
		}
	}

	if err := iter.Close(); err != nil {
		return &types.ExecuteQueryResponse{Error: fmt.Sprintf("error executing query: %v", err)}, nil
	}

	return &types.ExecuteQueryResponse{
		Columns: columns[:len(resultRows[0])],
		Rows:    resultRows,
	}, nil
}
