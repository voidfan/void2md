package graph

import (
	"fmt"
)

// Node represents a node in the schema knowledge graph
type Node struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // "table", "column", "foreign_key", etc.
	Name     string `json:"name"`
	Table    string `json:"table,omitempty"` // For columns, the table they belong to
	DataType string `json:"data_type,omitempty"`
}

// Edge represents a relationship between nodes
type Edge struct {
	Source      string `json:"source"`
	Target      string `json:"target"`
	Relationship string `json:"relationship"` // "belongs_to", "references", "foreign_key", etc.
}

// SchemaGraph represents the complete schema knowledge graph
type SchemaGraph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
	nodeMap map[string]*Node
	adjacencyList map[string][]Edge
}

// NewSchemaGraph creates a new schema graph
func NewSchemaGraph() *SchemaGraph {
	return &SchemaGraph{
		Nodes:         make([]Node, 0),
		Edges:         make([]Edge, 0),
		nodeMap:       make(map[string]*Node),
		adjacencyList: make(map[string][]Edge),
	}
}

// AddNode adds a node to the graph
func (sg *SchemaGraph) AddNode(node Node) {
	sg.Nodes = append(sg.Nodes, node)
	sg.nodeMap[node.ID] = &sg.Nodes[len(sg.Nodes)-1]
}

// AddEdge adds an edge to the graph
func (sg *SchemaGraph) AddEdge(edge Edge) {
	sg.Edges = append(sg.Edges, edge)
	sg.adjacencyList[edge.Source] = append(sg.adjacencyList[edge.Source], edge)
}

// FindPath finds a path between two nodes (simple BFS implementation)
func (sg *SchemaGraph) FindPath(startID, endID string) ([]Edge, error) {
	if _, exists := sg.nodeMap[startID]; !exists {
		return nil, fmt.Errorf("start node not found: %s", startID)
	}
	if _, exists := sg.nodeMap[endID]; !exists {
		return nil, fmt.Errorf("end node not found: %s", endID)
	}

	// Simple BFS for path finding
	visited := make(map[string]bool)
	queue := []string{startID}
	parent := make(map[string]string)
	edgeUsed := make(map[string]Edge)

	visited[startID] = true

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if current == endID {
			// Reconstruct path
			var path []Edge
			node := endID
			for node != startID {
				path = append([]Edge{edgeUsed[node]}, path...)
				node = parent[node]
			}
			return path, nil
		}

		for _, edge := range sg.adjacencyList[current] {
			neighbor := edge.Target
			if !visited[neighbor] {
				visited[neighbor] = true
				parent[neighbor] = current
				edgeUsed[neighbor] = edge
				queue = append(queue, neighbor)
			}
		}
	}

	return nil, fmt.Errorf("no path found between %s and %s", startID, endID)
}

// GetRelatedTables finds tables related to a given table through foreign keys
func (sg *SchemaGraph) GetRelatedTables(tableID string, maxDepth int) []string {
	relatedTables := make(map[string]bool)
	visited := make(map[string]bool)
	queue := []struct {
		table string
		depth int
	}{{
		table: tableID,
		depth: 0,
	}}

	visited[tableID] = true

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if current.depth >= maxDepth {
			continue
		}

		// Find all edges from this table
		for _, edge := range sg.adjacencyList[current.table] {
			if edge.Relationship == "foreign_key" || edge.Relationship == "references" {
				targetTable := edge.Target
				if !visited[targetTable] {
					visited[targetTable] = true
					relatedTables[targetTable] = true
					queue = append(queue, struct {
						table string
						depth int
					}{
						table: targetTable,
						depth: current.depth + 1,
					})
				}
			}
		}

		// Also find tables that reference this table (reverse foreign keys)
		for _, edge := range sg.Edges {
			if (edge.Relationship == "foreign_key" || edge.Relationship == "references") &&
				edge.Target == current.table && !visited[edge.Source] {
				visited[edge.Source] = true
				relatedTables[edge.Source] = true
				queue = append(queue, struct {
					table string
					depth int
				}{
					table: edge.Source,
					depth: current.depth + 1,
				})
			}
		}
	}

	// Convert map to slice
	var result []string
	for table := range relatedTables {
		result = append(result, table)
	}

	return result
}

// BuildFromDatabaseSchema builds a schema graph from database metadata
// This is a placeholder - in a real implementation, this would connect to the database
// and extract foreign key relationships, table structures, etc.
func (sg *SchemaGraph) BuildFromDatabaseSchema(schemaContext string) error {
	// Parse schema context and build graph
	// For now, this is just a placeholder implementation

	// Example: Add some sample nodes and edges for Jira-like schema
	sg.AddNode(Node{ID: "jiraissue", Type: "table", Name: "jiraissue"})
	sg.AddNode(Node{ID: "jiraissue_id", Type: "column", Name: "id", Table: "jiraissue", DataType: "bigint"})
	sg.AddNode(Node{ID: "jiraissue_project", Type: "column", Name: "project", Table: "jiraissue", DataType: "bigint"})
	sg.AddNode(Node{ID: "project", Type: "table", Name: "project"})
	sg.AddNode(Node{ID: "project_id", Type: "column", Name: "id", Table: "project", DataType: "bigint"})

	// Add relationships
	sg.AddEdge(Edge{Source: "jiraissue", Target: "jiraissue_id", Relationship: "contains"})
	sg.AddEdge(Edge{Source: "jiraissue", Target: "jiraissue_project", Relationship: "contains"})
	sg.AddEdge(Edge{Source: "jiraissue_project", Target: "project_id", Relationship: "foreign_key"})
	sg.AddEdge(Edge{Source: "project", Target: "project_id", Relationship: "contains"})

	return nil
}