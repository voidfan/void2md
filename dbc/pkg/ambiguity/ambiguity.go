package ambiguity

import (
	"fmt"
	"strings"

	"void2md/dbc/pkg/intent"
)

// AmbiguityType represents the type of ambiguity detected
type AmbiguityType string

const (
	AmbiguityTypeSchemaLinking AmbiguityType = "schema_linking"
	AmbiguityTypeQueryIntent   AmbiguityType = "query_intent"
	AmbiguityTypeUnderSpecified AmbiguityType = "under_specified"
)

// Ambiguity represents a detected ambiguity in the intent
type Ambiguity struct {
	Type        AmbiguityType `json:"type"`
	Field       string        `json:"field,omitempty"`         // Which field in IntentIR is ambiguous
	Description string        `json:"description"`
	Candidates  []string      `json:"candidates,omitempty"`    // Possible interpretations
	Confidence  float64       `json:"confidence"`              // Confidence that this is actually ambiguous (0.0-1.0)
}

// AmbiguityDetector detects ambiguities in IntentIR
type AmbiguityDetector struct {
	// Thresholds for ambiguity detection
	SchemaLinkingThreshold float64
	QueryIntentThreshold   float64
	UnderSpecifiedThreshold float64
}

// NewAmbiguityDetector creates a new ambiguity detector with default thresholds
func NewAmbiguityDetector() *AmbiguityDetector {
	return &AmbiguityDetector{
		SchemaLinkingThreshold: 0.7,
		QueryIntentThreshold:   0.6,
		UnderSpecifiedThreshold: 0.5,
	}
}

// DetectAmbiguities analyzes an IntentIR and returns detected ambiguities
func (ad *AmbiguityDetector) DetectAmbiguities(ir *intent.IntentIR) []Ambiguity {
	var ambiguities []Ambiguity

	// Check for schema linking ambiguity
	schemaAmbiguities := ad.detectSchemaLinkingAmbiguities(ir)
	ambiguities = append(ambiguities, schemaAmbiguities...)

	// Check for query intent ambiguity
	intentAmbiguities := ad.detectQueryIntentAmbiguities(ir)
	ambiguities = append(ambiguities, intentAmbiguities...)

	// Check for under-specified queries
	underSpecifiedAmbiguities := ad.detectUnderSpecifiedQueries(ir)
	ambiguities = append(ambiguities, underSpecifiedAmbiguities...)

	return ambiguities
}

// detectSchemaLinkingAmbiguities checks for fields that could map to multiple schema elements
func (ad *AmbiguityDetector) detectSchemaLinkingAmbiguities(ir *intent.IntentIR) []Ambiguity {
	var ambiguities []Ambiguity

	// This is a placeholder implementation - in a real system, this would
	// compare against actual database schema and vector similarity scores
	// For now, we'll simulate based on confidence scores and field presence

	if ir.ConfidenceScore < ad.SchemaLinkingThreshold {
		ambiguities = append(ambiguities, Ambiguity{
			Type:        AmbiguityTypeSchemaLinking,
			Description: "Low confidence in intent decomposition suggests potential schema linking ambiguity",
			Confidence:  1.0 - ir.ConfidenceScore,
		})
	}

	// Check target metrics for potential ambiguity
	for _, metric := range ir.TargetMetrics {
		if containsAmbiguousTerms(metric) {
			ambiguities = append(ambiguities, Ambiguity{
				Type:        AmbiguityTypeSchemaLinking,
				Field:       "target_metrics",
				Description: fmt.Sprintf("Target metric '%s' may map to multiple database columns", metric),
				Confidence:  0.8, // High confidence for known ambiguous terms
			})
		}
	}

	// Check filter conditions for potential ambiguity
	for _, filter := range ir.FilterConditions {
		if containsAmbiguousTerms(filter.Field) || containsAmbiguousTerms(filter.Value) {
			ambiguities = append(ambiguities, Ambiguity{
				Type:        AmbiguityTypeSchemaLinking,
				Field:       "filter_conditions",
				Description: fmt.Sprintf("Filter condition '%s %s %s' may have ambiguous mappings", filter.Field, filter.Operator, filter.Value),
				Confidence:  0.7,
			})
		}
	}

	return ambiguities
}

// detectQueryIntentAmbiguities checks for unclear query intentions
func (ad *AmbiguityDetector) detectQueryIntentAmbiguities(ir *intent.IntentIR) []Ambiguity {
	var ambiguities []Ambiguity

	// If confidence score is low, mark as query intent ambiguity
	if ir.ConfidenceScore < ad.QueryIntentThreshold {
		ambiguities = append(ambiguities, Ambiguity{
			Type:        AmbiguityTypeQueryIntent,
			Description: "Low confidence in overall query intent",
			Confidence:  1.0 - ir.ConfidenceScore,
		})
	}

	// Check for conflicting or contradictory intent elements
	if hasConflictingIntentElements(ir) {
		ambiguities = append(ambiguities, Ambiguity{
			Type:        AmbiguityTypeQueryIntent,
			Description: "Conflicting intent elements detected",
			Confidence:  0.9,
		})
	}

	return ambiguities
}

// detectUnderSpecifiedQueries checks for missing critical query components
func (ad *AmbiguityDetector) detectUnderSpecifiedQueries(ir *intent.IntentIR) []Ambiguity {
	var ambiguities []Ambiguity

	// Check if time semantics are missing for potentially time-sensitive queries
	if !ir.TimeSemantics.HasTimeConstraint && queryMightNeedTimeConstraint(ir) {
		ambiguities = append(ambiguities, Ambiguity{
			Type:        AmbiguityTypeUnderSpecified,
			Field:       "time_semantics",
			Description: "Query may need time constraints but none specified",
			Confidence:  0.6,
		})
	}

	// Check if grouping dimensions are missing for aggregate queries
	if ir.QueryType == intent.QueryTypeAggregate && len(ir.GroupingDimensions) == 0 {
		ambiguities = append(ambiguities, Ambiguity{
			Type:        AmbiguityTypeUnderSpecified,
			Field:       "grouping_dimensions",
			Description: "Aggregate query lacks grouping dimensions",
			Confidence:  0.8,
		})
	}

	// Check if target metrics are missing for non-trivial queries
	if len(ir.TargetMetrics) == 0 && queryIsNonTrivial(ir) {
		ambiguities = append(ambiguities, Ambiguity{
			Type:        AmbiguityTypeUnderSpecified,
			Field:       "target_metrics",
			Description: "Query lacks specified target metrics",
			Confidence:  0.7,
		})
	}

	return ambiguities
}

// Helper functions for ambiguity detection

func containsAmbiguousTerms(term string) bool {
	ambiguousTerms := []string{
		"sales", "revenue", "income", "profit", "cost", "price", "value",
		"user", "customer", "client", "person", "individual",
		"order", "purchase", "transaction", "sale",
		"status", "state", "condition", "phase",
	}

	termLower := strings.ToLower(term)
	for _, ambiguous := range ambiguousTerms {
		if strings.Contains(termLower, ambiguous) {
			return true
		}
	}
	return false
}

func hasConflictingIntentElements(ir *intent.IntentIR) bool {
	// Example: COUNT query with grouping dimensions might be conflicting
	if ir.QueryType == intent.QueryTypeCount && len(ir.GroupingDimensions) > 0 {
		return true
	}

	// Add more conflict detection logic as needed
	return false
}

func queryMightNeedTimeConstraint(ir *intent.IntentIR) bool {
	timeSensitiveTerms := []string{"recent", "latest", "current", "today", "yesterday", "week", "month", "year", "last", "past", "previous"}

	// Check target metrics
	for _, metric := range ir.TargetMetrics {
		metricLower := strings.ToLower(metric)
		for _, term := range timeSensitiveTerms {
			if strings.Contains(metricLower, term) {
				return true
			}
		}
	}

	// Check implicit assumptions
	for _, assumption := range ir.ImplicitAssumptions {
		assumptionLower := strings.ToLower(assumption)
		for _, term := range timeSensitiveTerms {
			if strings.Contains(assumptionLower, term) {
				return true
			}
		}
	}

	return false
}

func queryIsNonTrivial(ir *intent.IntentIR) bool {
	// A query is considered non-trivial if it's not just a simple SELECT *
	return !(ir.QueryType == intent.QueryTypeSelect &&
		len(ir.TargetMetrics) == 0 &&
		len(ir.FilterConditions) == 0 &&
		len(ir.GroupingDimensions) == 0 &&
		len(ir.Sorting) == 0)
}