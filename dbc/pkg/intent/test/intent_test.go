package intent_test

import (
	"testing"

	"void2md/dbc/pkg/intent"
)

func TestIntentIR_MarshalJSON(t *testing.T) {
	ir := &intent.IntentIR{
		QueryType:       intent.QueryTypeSelect,
		TargetMetrics:   []string{"issue_count"},
		ConfidenceScore: 0.95,
	}

	data, err := ir.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON failed: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("MarshalJSON returned empty data")
	}

	var ir2 intent.IntentIR
	err = ir2.UnmarshalJSON(data)
	if err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}

	if ir2.QueryType != ir.QueryType {
		t.Errorf("QueryType mismatch: got %v, want %v", ir2.QueryType, ir.QueryType)
	}

	if len(ir2.TargetMetrics) != len(ir.TargetMetrics) {
		t.Errorf("TargetMetrics length mismatch: got %d, want %d", len(ir2.TargetMetrics), len(ir.TargetMetrics))
	}
}

func TestIntentIR_UnmarshalJSON(t *testing.T) {
	jsonData := `{
		"query_type": "SELECT",
		"target_metrics": ["issue_count"],
		"confidence_score": 0.95
	}`

	var ir intent.IntentIR
	err := ir.UnmarshalJSON([]byte(jsonData))
	if err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}

	if ir.QueryType != intent.QueryTypeSelect {
		t.Errorf("QueryType mismatch: got %v, want %v", ir.QueryType, intent.QueryTypeSelect)
	}

	if len(ir.TargetMetrics) != 1 || ir.TargetMetrics[0] != "issue_count" {
		t.Errorf("TargetMetrics mismatch: got %v", ir.TargetMetrics)
	}

	if ir.ConfidenceScore != 0.95 {
		t.Errorf("ConfidenceScore mismatch: got %f, want %f", ir.ConfidenceScore, 0.95)
	}
}