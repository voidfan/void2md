package clarification_test

import (
	"testing"

	"void2md/dbc/pkg/ambiguity"
	"void2md/dbc/pkg/clarification"
	"void2md/dbc/pkg/intent"
)

func TestClarificationGenerator_GenerateClarifications(t *testing.T) {
	generator := clarification.NewClarificationGenerator()

	// Create a sample intent with ambiguities
	sampleIR := &intent.IntentIR{
		QueryType:       intent.QueryTypeSelect,
		TargetMetrics:   []string{"revenue"},
		ConfidenceScore: 0.6,
	}

	ambiguities := []ambiguity.Ambiguity{
		{
			Type:        ambiguity.AmbiguityTypeSchemaLinking,
			Field:       "target_metrics",
			Description: "Revenue term is ambiguous",
			Confidence:  0.8,
		},
		{
			Type:        ambiguity.AmbiguityTypeUnderSpecified,
			Field:       "time_semantics",
			Description: "Missing time constraints",
			Confidence:  0.7,
		},
	}

	resp := generator.GenerateClarifications(sampleIR, ambiguities)
	if !resp.RequiresClarification {
		t.Error("Expected requires clarification to be true")
	}

	if len(resp.Questions) == 0 {
		t.Error("Expected at least one clarification question")
	}

	// Check that questions have options
	for i, q := range resp.Questions {
		if len(q.Options) == 0 {
			t.Errorf("Question %d has no options", i)
		}
	}
}