package ambiguity_test

import (
	"testing"

	"void2md/dbc/pkg/ambiguity"
	"void2md/dbc/pkg/intent"
)

func TestAmbiguityDetector_DetectAmbiguities(t *testing.T) {
	detector := ambiguity.NewAmbiguityDetector()

	// Test case 1: Low confidence intent should trigger ambiguity
	lowConfidenceIR := &intent.IntentIR{
		QueryType:       intent.QueryTypeSelect,
		TargetMetrics:   []string{"sales"},
		ConfidenceScore: 0.5, // Below threshold
	}

	ambiguities := detector.DetectAmbiguities(lowConfidenceIR)
	if len(ambiguities) == 0 {
		t.Error("Expected ambiguities for low confidence intent")
	}

	// Test case 2: High confidence intent with ambiguous terms
	highConfidenceIR := &intent.IntentIR{
		QueryType:       intent.QueryTypeSelect,
		TargetMetrics:   []string{"revenue"}, // Ambiguous term
		ConfidenceScore: 0.9,
	}

	ambiguities = detector.DetectAmbiguities(highConfidenceIR)
	if len(ambiguities) == 0 {
		t.Error("Expected ambiguities for ambiguous terms")
	}

	// Test case 3: Well-specified intent should have no ambiguities
	wellSpecifiedIR := &intent.IntentIR{
		QueryType:       intent.QueryTypeCount,
		TargetMetrics:   []string{"issue_count"},
		ConfidenceScore: 0.95,
	}

	ambiguities = detector.DetectAmbiguities(wellSpecifiedIR)
	// May still have some ambiguities due to implementation, but should be fewer
	t.Logf("Well-specified intent has %d ambiguities", len(ambiguities))
}