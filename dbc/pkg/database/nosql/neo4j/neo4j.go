package neo4j

import (
	"fmt"
	"strings"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"void2md/dbc/pkg/database/types"
)

// Neo4jClient represents a Neo4j database client
type Neo4jClient struct {
	driver   neo4j.Driver
	version  string
	host     string
	port     string
	username string
	password string
	database string
}

// NewNeo4jClient creates a new Neo4j client
func NewNeo4jClient(config types.DatabaseConfig) (types.Client, error) {
	// Neo4j connection URI
	uri := fmt.Sprintf("neo4j://%s:%s", config.Host, config.Port)
	if config.Port == "" {
		uri = fmt.Sprintf("neo4j://%s:7687", config.Host) // Default Neo4j port
	}

	driver, err := neo4j.NewDriver(uri, neo4j.BasicAuth(config.Username, config.Password, ""))
	if err != nil {
		return nil, fmt.Errorf("failed to create Neo4j driver: %w", err)
	}

	// Test the connection
	session := driver.NewSession(neo4j.SessionConfig{DatabaseName: config.Database})
	defer session.Close()

	result, err := session.Run("CALL dbms.components() YIELD versions RETURN versions[0] AS version", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Neo4j: %w", err)
	}

	var version string
	if result.Next() {
		version = result.Record().Values[0].(string)
	} else {
		version = config.Version
	}

	return &Neo4jClient{
		driver:   driver,
		version:  version,
		host:     config.Host,
		port:     config.Port,
		username: config.Username,
		password: config.Password,
		database: config.Database,
	}, nil
}

// GetType returns the database type
func (c *Neo4jClient) GetType() types.DatabaseType {
	return types.Neo4j
}

// GetVersion returns the database version
func (c *Neo4jClient) GetVersion() string {
	return c.version
}

// Ping tests the database connection
func (c *Neo4jClient) Ping() error {
	session := c.driver.NewSession(neo4j.SessionConfig{DatabaseName: c.database})
	defer session.Close()

	_, err := session.Run("RETURN 1", nil)
	return err
}

// Close closes the database connection
func (c *Neo4jClient) Close() error {
	c.driver.Close()
	return nil
}

// GetSchemaContext gets schema context for Neo4j
func (c *Neo4jClient) GetSchemaContext(tables string) (string, error) {
	session := c.driver.NewSession(neo4j.SessionConfig{DatabaseName: c.database})
	defer session.Close()

	// Get node labels and relationship types
	result, err := session.Run(`
		MATCH (n)
		RETURN DISTINCT labels(n) as nodeLabels
		UNION
		MATCH ()-[r]->()
		RETURN DISTINCT type(r) as relType
	`, nil)

	if err != nil {
		return "", fmt.Errorf("failed to get schema context: %w", err)
	}

	var context strings.Builder
	context.WriteString("Neo4j Schema:\n")

	for result.Next() {
		record := result.Record()
		if labels, ok := record.Get("nodeLabels"); ok {
			if labelList, ok := labels.([]interface{}); ok {
				for _, label := range labelList {
					if labelStr, ok := label.(string); ok {
						context.WriteString(fmt.Sprintf("- Node Label: %s\n", labelStr))
					}
				}
			}
		}
		if relType, ok := record.Get("relType"); ok {
			if relTypeStr, ok := relType.(string); ok {
				context.WriteString(fmt.Sprintf("- Relationship Type: %s\n", relTypeStr))
			}
		}
	}

	if err := result.Err(); err != nil {
		return "", fmt.Errorf("error iterating schema results: %w", err)
	}

	return context.String(), nil
}

// ExecuteOperation executes a Neo4j operation
func (c *Neo4jClient) ExecuteOperation(op types.Operation) (*types.ExecuteQueryResponse, error) {
	if op.Type != types.OpQuery && op.Type != types.OpCypher {
		return &types.ExecuteQueryResponse{Error: "Neo4j only supports QUERY and CYPHER operations"}, nil
	}

	session := c.driver.NewSession(neo4j.SessionConfig{DatabaseName: c.database})
	defer session.Close()

	// Execute Cypher query
	result, err := session.Run(op.Statement, nil)
	if err != nil {
		return &types.ExecuteQueryResponse{Error: fmt.Sprintf("Cypher query failed: %v", err)}, nil
	}

	// Get column names from keys
	keys, _ := result.Keys()
	columns := make([]string, len(keys))
	for i, key := range keys {
		columns[i] = key
	}

	// Read all rows
	var resultRows [][]interface{}
	rowCount := 0

	for result.Next() {
		record := result.Record()
		values := make([]interface{}, len(columns))
		for i, key := range columns {
			if val, ok := record.Get(key); ok {
				values[i] = val
			} else {
				values[i] = nil
			}
		}
		resultRows = append(resultRows, values)
		rowCount++

		// Limit results to prevent memory issues
		if rowCount >= 1000 {
			break
		}
	}

	if err := result.Err(); err != nil {
		return &types.ExecuteQueryResponse{Error: fmt.Sprintf("error iterating results: %v", err)}, nil
	}

	return &types.ExecuteQueryResponse{
		Columns: columns,
		Rows:    resultRows,
	}, nil
}
