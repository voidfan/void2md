package vector

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"void2md/dbc/pkg/intent"
)

// Vector represents a numerical vector for embeddings
type Vector []float64

// SchemaElement represents a database schema element with its vector embedding
type SchemaElement struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"` // "table", "column", "index", etc.
	Description string `json:"description,omitempty"`
	Vector      Vector `json:"vector"`
	Table       string `json:"table,omitempty"` // For columns, the table they belong to
}

// VectorIndex represents a collection of schema elements with their vectors
type VectorIndex struct {
	Elements []SchemaElement `json:"elements"`
}

// NewVectorIndex creates a new vector index
func NewVectorIndex() *VectorIndex {
	return &VectorIndex{
		Elements: make([]SchemaElement, 0),
	}
}

// AddElement adds a schema element to the index
func (vi *VectorIndex) AddElement(element SchemaElement) {
	vi.Elements = append(vi.Elements, element)
}

// CosineSimilarity calculates cosine similarity between two vectors
func CosineSimilarity(a, b Vector) float64 {
	if len(a) != len(b) {
		return 0.0
	}

	var dotProduct float64
	var normA float64
	var normB float64

	for i := range a {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0.0
	}

	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}

// FindSimilarElements finds the top K most similar schema elements to a query vector
func (vi *VectorIndex) FindSimilarElements(queryVector Vector, topK int) []SchemaElement {
	type scoredElement struct {
		element SchemaElement
		score   float64
	}

	var scoredElements []scoredElement

	for _, element := range vi.Elements {
		score := CosineSimilarity(queryVector, element.Vector)
		scoredElements = append(scoredElements, scoredElement{
			element: element,
			score:   score,
		})
	}

	// Sort by score in descending order
	for i := 0; i < len(scoredElements); i++ {
		for j := i + 1; j < len(scoredElements); j++ {
			if scoredElements[i].score < scoredElements[j].score {
				scoredElements[i], scoredElements[j] = scoredElements[j], scoredElements[i]
			}
		}
	}

	// Return top K elements
	var result []SchemaElement
	for i := 0; i < topK && i < len(scoredElements); i++ {
		result = append(result, scoredElements[i].element)
	}

	return result
}

// GenerateQueryVector generates a vector for an Intent IR field
// In a real implementation, this would use a proper embedding model
// For now, we'll use a simple hash-based approach for demonstration
func GenerateQueryVector(text string, dimensions int) Vector {
	vector := make(Vector, dimensions)
	textLower := strings.ToLower(text)

	// Simple hash-based vector generation for demonstration
	// In production, this would use sentence transformers or similar
	for i := 0; i < dimensions; i++ {
		hashValue := hashString(textLower, i)
		// Normalize to [-1, 1] range
		vector[i] = (float64(hashValue%2000) - 1000.0) / 1000.0
	}

	return vector
}

// hashString generates a simple hash for a string with a seed
func hashString(s string, seed int) int {
	hash := seed
	for _, c := range s {
		hash = (hash*31 + int(c)) % 1000000
	}
	return hash
}

// LoadFromJSON loads a vector index from JSON data
func LoadFromJSON(data []byte) (*VectorIndex, error) {
	var elements []SchemaElement
	if err := json.Unmarshal(data, &elements); err != nil {
		return nil, fmt.Errorf("failed to unmarshal vector index: %w", err)
	}

	return &VectorIndex{Elements: elements}, nil
}

// SearchByIntentIR searches for relevant schema elements based on Intent IR
func (vi *VectorIndex) SearchByIntentIR(ir *intent.IntentIR, topK int) map[string][]SchemaElement {
	results := make(map[string][]SchemaElement)

	// Search for target metrics
	for _, metric := range ir.TargetMetrics {
		queryVector := GenerateQueryVector(metric, len(vi.Elements[0].Vector))
		results["target_metrics"] = append(results["target_metrics"], vi.FindSimilarElements(queryVector, topK)...)
	}

	// Search for filter conditions
	for _, filter := range ir.FilterConditions {
		fieldVector := GenerateQueryVector(filter.Field, len(vi.Elements[0].Vector))
		valueVector := GenerateQueryVector(filter.Value, len(vi.Elements[0].Vector))

		results["filter_fields"] = append(results["filter_fields"], vi.FindSimilarElements(fieldVector, topK)...)
		results["filter_values"] = append(results["filter_values"], vi.FindSimilarElements(valueVector, topK)...)
	}

	// Search for grouping dimensions
	for _, dim := range ir.GroupingDimensions {
		dimVector := GenerateQueryVector(dim.Field, len(vi.Elements[0].Vector))
		results["grouping_dimensions"] = append(results["grouping_dimensions"], vi.FindSimilarElements(dimVector, topK)...)
	}

	return results
}