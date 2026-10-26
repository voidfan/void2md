package semantic

import (
	"encoding/json"
	"fmt"
	"strings"
)

// BusinessConcept represents a semantic layer concept that maps natural language to database schema
type BusinessConcept struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Domain      string   `json:"domain"`
	RelatedTables []string `json:"related_tables,omitempty"`
	RelatedColumns map[string][]string `json:"related_columns,omitempty"` // table -> columns
	Synonyms    []string `json:"synonyms,omitempty"`
	MetricType  string   `json:"metric_type,omitempty"` // e.g., "count", "sum", "average", "ratio"
}

// SemanticLayer represents the complete semantic layer configuration
type SemanticLayer struct {
	Concepts    []BusinessConcept       `json:"concepts"`
	DomainMap   map[string][]string     `json:"domain_map,omitempty"` // domain -> concept IDs
	conceptMap  map[string]*BusinessConcept // internal lookup map for performance
}

// NewSemanticLayer creates a new semantic layer from configuration
func NewSemanticLayer(concepts []BusinessConcept) *SemanticLayer {
	sl := &SemanticLayer{
		Concepts:   concepts,
		DomainMap:  make(map[string][]string),
		conceptMap: make(map[string]*BusinessConcept),
	}

	// Build internal lookup maps
	for i := range sl.Concepts {
		concept := &sl.Concepts[i]
		sl.conceptMap[concept.ID] = concept

		// Build domain map
		if _, exists := sl.DomainMap[concept.Domain]; !exists {
			sl.DomainMap[concept.Domain] = []string{}
		}
		sl.DomainMap[concept.Domain] = append(sl.DomainMap[concept.Domain], concept.ID)
	}

	return sl
}

// FindConceptsByTerm finds business concepts that match a given term
// This includes exact matches on name, ID, and synonyms
func (sl *SemanticLayer) FindConceptsByTerm(term string) []*BusinessConcept {
	var matches []*BusinessConcept
	termLower := strings.ToLower(term)

	for _, concept := range sl.Concepts {
		// Check name
		if strings.ToLower(concept.Name) == termLower {
			matches = append(matches, &concept)
			continue
		}

		// Check ID
		if strings.ToLower(concept.ID) == termLower {
			matches = append(matches, &concept)
			continue
		}

		// Check synonyms
		for _, synonym := range concept.Synonyms {
			if strings.ToLower(synonym) == termLower {
				matches = append(matches, &concept)
				break
			}
		}
	}

	return matches
}

// GetConceptByID retrieves a business concept by its ID
func (sl *SemanticLayer) GetConceptByID(id string) (*BusinessConcept, error) {
	concept, exists := sl.conceptMap[id]
	if !exists {
		return nil, fmt.Errorf("concept not found: %s", id)
	}
	return concept, nil
}

// GetConceptsByDomain retrieves all concepts in a specific business domain
func (sl *SemanticLayer) GetConceptsByDomain(domain string) ([]*BusinessConcept, error) {
	ids, exists := sl.DomainMap[domain]
	if !exists {
		return nil, fmt.Errorf("domain not found: %s", domain)
	}

	var concepts []*BusinessConcept
	for _, id := range ids {
		concept, err := sl.GetConceptByID(id)
		if err != nil {
			return nil, err
		}
		concepts = append(concepts, concept)
	}

	return concepts, nil
}

// LoadFromJSON loads semantic layer configuration from JSON data
func LoadFromJSON(data []byte) (*SemanticLayer, error) {
	var concepts []BusinessConcept
	if err := json.Unmarshal(data, &concepts); err != nil {
		return nil, fmt.Errorf("failed to unmarshal semantic layer: %w", err)
	}

	return NewSemanticLayer(concepts), nil
}