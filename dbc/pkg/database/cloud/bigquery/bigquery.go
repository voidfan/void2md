package bigquery

import (
	"context"
	"fmt"
	"strings"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/iterator"

	"void2md/dbc/pkg/database/types"
)

// BigQueryClient represents a BigQuery database client
type BigQueryClient struct {
	client    *bigquery.Client
	version   string
	projectID string
	dataset   string
}

// NewBigQueryClient creates a new BigQuery client
func NewBigQueryClient(config types.DatabaseConfig) (types.Client, error) {
	ctx := context.Background()

	client, err := bigquery.NewClient(ctx, config.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to create BigQuery client: %w", err)
	}

	// Test the connection by listing datasets
	_, err = client.DatasetInProject(config.ProjectID, config.Database).Metadata(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to BigQuery dataset: %w", err)
	}

	return &BigQueryClient{
		client:    client,
		version:   "BigQuery",
		projectID: config.ProjectID,
		dataset:   config.Database,
	}, nil
}

// GetType returns the database type
func (c *BigQueryClient) GetType() types.DatabaseType {
	return types.BigQuery
}

// GetVersion returns the database version
func (c *BigQueryClient) GetVersion() string {
	return c.version
}

// Ping tests the database connection
func (c *BigQueryClient) Ping() error {
	ctx := context.Background()
	_, err := c.client.Dataset(c.dataset).Metadata(ctx)
	return err
}

// Close closes the database connection
func (c *BigQueryClient) Close() error {
	return c.client.Close()
}

// GetSchemaContext gets schema context for BigQuery
func (c *BigQueryClient) GetSchemaContext(tables string) (string, error) {
	ctx := context.Background()

	var tableList []string
	if tables != "" {
		tableList = strings.Split(tables, ",")
		for i, table := range tableList {
			tableList[i] = strings.TrimSpace(table)
		}
	}

	dataset := c.client.Dataset(c.dataset)
	tableIter := dataset.Tables(ctx)

	var context strings.Builder
	context.WriteString(fmt.Sprintf("BigQuery Dataset: %s.%s\n", c.projectID, c.dataset))

	for {
		table, err := tableIter.Next()
		if err != nil {
			// Check if we've reached the end
			if err == iterator.Done {
				break
			}
			return "", fmt.Errorf("failed to list tables: %w", err)
		}

		// If specific tables requested, skip others
		if len(tableList) > 0 {
			found := false
			for _, tableName := range tableList {
				if table.TableID == tableName {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// Get table schema
		meta, err := table.Metadata(ctx)
		if err != nil {
			continue // Skip tables we can't read
		}

		context.WriteString(fmt.Sprintf("Table %s:\n", table.TableID))
		for _, field := range meta.Schema {
			nullStr := "NULLABLE"
			if field.Required {
				nullStr = "REQUIRED"
			}
			context.WriteString(fmt.Sprintf("  %s (%s %s),\n", field.Name, field.Type, nullStr))
		}
		context.WriteString("\n")
	}

	return context.String(), nil
}

// ExecuteOperation executes a BigQuery operation
func (c *BigQueryClient) ExecuteOperation(op types.Operation) (*types.ExecuteQueryResponse, error) {
	if op.Type != types.OpQuery {
		return &types.ExecuteQueryResponse{Error: "BigQuery only supports QUERY operations"}, nil
	}

	ctx := context.Background()
	q := c.client.Query(op.Statement)
	q.DefaultDatasetID = c.dataset
	q.DefaultProjectID = c.projectID

	job, err := q.Run(ctx)
	if err != nil {
		return &types.ExecuteQueryResponse{Error: fmt.Sprintf("BigQuery query failed: %v", err)}, nil
	}

	status, err := job.Wait(ctx)
	if err != nil {
		return &types.ExecuteQueryResponse{Error: fmt.Sprintf("BigQuery job failed: %v", err)}, nil
	}

	if err := status.Err(); err != nil {
		return &types.ExecuteQueryResponse{Error: fmt.Sprintf("BigQuery execution error: %v", err)}, nil
	}

	// Get results
	iter, err := job.Read(ctx)
	if err != nil {
		return &types.ExecuteQueryResponse{Error: fmt.Sprintf("Failed to read results: %v", err)}, nil
	}

	// Get column names
	columns := make([]string, len(iter.Schema))
	for i, field := range iter.Schema {
		columns[i] = field.Name
	}

	// Read all rows
	var resultRows [][]interface{}
	rowCount := 0

	for {
		var row []bigquery.Value
		err := iter.Next(&row)
		if err != nil {
			if err == iterator.Done {
				break
			}
			return &types.ExecuteQueryResponse{Error: fmt.Sprintf("Error reading row: %v", err)}, nil
		}

		// Convert BigQuery values to interface{}
		values := make([]interface{}, len(row))
		for i, val := range row {
			values[i] = val
		}

		resultRows = append(resultRows, values)
		rowCount++

		// Limit results to prevent memory issues
		if rowCount >= 1000 {
			break
		}
	}

	return &types.ExecuteQueryResponse{
		Columns: columns,
		Rows:    resultRows,
	}, nil
}
