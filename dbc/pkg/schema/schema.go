package schema

import (
	"strings"

	"void2md/dbc/pkg/domain"
	"void2md/dbc/pkg/graph"
	"void2md/dbc/pkg/intent"
	"void2md/dbc/pkg/vector"
)

// SchemaRouter routes queries to relevant schema elements using multiple strategies
type SchemaRouter struct {
	vectorIndex    *vector.VectorIndex
	domainClassifier *domain.DomainClassifier
	schemaGraph    *graph.SchemaGraph
}

// NewSchemaRouter creates a new schema router
func NewSchemaRouter(vectorIndex *vector.VectorIndex, domainClassifier *domain.DomainClassifier, schemaGraph *graph.SchemaGraph) *SchemaRouter {
	return &SchemaRouter{
		vectorIndex:    vectorIndex,
		domainClassifier: domainClassifier,
		schemaGraph:    schemaGraph,
	}
}

// RouteSchema finds relevant schema elements for an Intent IR
func (sr *SchemaRouter) RouteSchema(ir *intent.IntentIR) ([]string, error) {
	// Get business domain classification
	businessDomain := sr.domainClassifier.ClassifyDomain(ir)

	// Get domain-specific tables
	domainTables := sr.domainClassifier.GetDomainTables(businessDomain)

	// Use vector search if available
	var vectorResults []string
	if sr.vectorIndex != nil && len(sr.vectorIndex.Elements) > 0 {
		vectorSearchResults := sr.vectorIndex.SearchByIntentIR(ir, 5)
		for _, elements := range vectorSearchResults {
			for _, element := range elements {
				if element.Type == "table" {
					vectorResults = append(vectorResults, element.Name)
				} else if element.Type == "column" && element.Table != "" {
					vectorResults = append(vectorResults, element.Table)
				}
			}
		}
	}

	// Use graph-based expansion from core tables
	var graphResults []string
	if sr.schemaGraph != nil {
		// Start with domain tables as seed
		for _, table := range domainTables {
			relatedTables := sr.schemaGraph.GetRelatedTables(table, 2) // Max depth of 2
			graphResults = append(graphResults, relatedTables...)
		}
	}

	// Combine and deduplicate results
	allTables := make(map[string]bool)

	// Add domain tables (highest priority)
	for _, table := range domainTables {
		allTables[table] = true
	}

	// Add vector search results
	for _, table := range vectorResults {
		allTables[table] = true
	}

	// Add graph results
	for _, table := range graphResults {
		allTables[table] = true
	}

	// Convert to slice
	var result []string
	for table := range allTables {
		result = append(result, table)
	}

	return result, nil
}

// CreateFocusedSchemaContext creates a focused schema context for the routed tables
func (sr *SchemaRouter) CreateFocusedSchemaContext(fullSchemaContext string, routedTables []string) string {
	if len(routedTables) == 0 {
		return fullSchemaContext
	}

	// Simple implementation: filter schema context to include only routed tables
	// In a real implementation, this would parse the actual schema context format
	lines := strings.Split(fullSchemaContext, "\n")
	var focusedLines []string

	tableSet := make(map[string]bool)
	for _, table := range routedTables {
		tableSet[strings.ToLower(table)] = true
	}

	currentTable := ""
	includeCurrentTable := false

	for _, line := range lines {
		lineTrimmed := strings.TrimSpace(line)

		// Check if this line starts a new table definition
		if strings.HasPrefix(lineTrimmed, "Table ") {
			// Extract table name from "Table TABLE_NAME: ..."
			parts := strings.Split(lineTrimmed, ":")
			if len(parts) > 0 {
				tableName := strings.TrimPrefix(parts[0], "Table ")
				tableName = strings.TrimSpace(tableName)
				currentTable = strings.ToLower(tableName)
				includeCurrentTable = tableSet[currentTable]
			}
		}

		// Include the line if we're in a table we want to include
		if includeCurrentTable || currentTable == "" {
			focusedLines = append(focusedLines, line)
		}
	}

	return strings.Join(focusedLines, "\n")
}

// ScoreTableRelevance scores how relevant a table is to the Intent IR
func (sr *SchemaRouter) ScoreTableRelevance(table string, ir *intent.IntentIR) float64 {
	score := 0.0

	// Check if table matches business domain
	businessDomain := sr.domainClassifier.ClassifyDomain(ir)
	domainTables := sr.domainClassifier.GetDomainTables(businessDomain)
	for _, domainTable := range domainTables {
		if strings.Contains(strings.ToLower(table), strings.ToLower(domainTable)) ||
		   strings.Contains(strings.ToLower(domainTable), strings.ToLower(table)) {
			score += 0.5
			break
		}
	}

	// Check if table name matches target metrics or filter conditions
	for _, metric := range ir.TargetMetrics {
		if strings.Contains(strings.ToLower(table), strings.ToLower(metric)) ||
		   strings.Contains(strings.ToLower(metric), strings.ToLower(table)) {
			score += 0.3
			break
		}
	}

	for _, filter := range ir.FilterConditions {
		if strings.Contains(strings.ToLower(table), strings.ToLower(filter.Field)) ||
		   strings.Contains(strings.ToLower(filter.Field), strings.ToLower(table)) {
			score += 0.2
			break
		}
	}

	// Cap score at 1.0
	if score > 1.0 {
		score = 1.0
	}

	return score
}