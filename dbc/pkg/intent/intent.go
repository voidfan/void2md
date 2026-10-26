package intent

import (
	"encoding/json"
)

// QueryType represents the type of SQL query
type QueryType string

const (
	QueryTypeSelect   QueryType = "SELECT"
	QueryTypeCount    QueryType = "COUNT"
	QueryTypeAggregate QueryType = "AGGREGATE"
	QueryTypeJoin     QueryType = "JOIN"
	QueryTypeFilter   QueryType = "FILTER"
)

// TimeSemantics represents time-related query constraints
type TimeSemantics struct {
	HasTimeConstraint bool   `json:"has_time_constraint"`
	TimeField         string `json:"time_field,omitempty"`         // e.g., "created_date", "updated_at"
	TimeRange         string `json:"time_range,omitempty"`         // e.g., "last_7_days", "this_month"
	TimeGranularity   string `json:"time_granularity,omitempty"`   // e.g., "day", "week", "month"
}

// FilterCondition represents a WHERE clause condition
type FilterCondition struct {
	Field      string `json:"field"`
	Operator   string `json:"operator"` // e.g., "=", ">", "<", "LIKE", "IN"
	Value      string `json:"value"`
	ValueType  string `json:"value_type"` // e.g., "string", "number", "date", "enum"
}

// GroupingDimension represents GROUP BY and aggregation logic
type GroupingDimension struct {
	Field        string `json:"field"`
	Aggregation  string `json:"aggregation,omitempty"` // e.g., "COUNT", "SUM", "AVG"
	Alias        string `json:"alias,omitempty"`
}

// Sorting represents ORDER BY clauses
type Sorting struct {
	Field     string `json:"field"`
	Direction string `json:"direction"` // "ASC" or "DESC"
}

// IntentIR represents the structured Intent Intermediate Representation
type IntentIR struct {
	QueryType          QueryType           `json:"query_type"`
	TargetMetrics      []string            `json:"target_metrics,omitempty"`      // e.g., ["issue_count", "resolution_time"]
	TimeSemantics      TimeSemantics       `json:"time_semantics"`
	FilterConditions   []FilterCondition   `json:"filter_conditions,omitempty"`
	GroupingDimensions []GroupingDimension `json:"grouping_dimensions,omitempty"`
	Sorting            []Sorting           `json:"sorting,omitempty"`
	ImplicitAssumptions []string           `json:"implicit_assumptions,omitempty"` // e.g., "active_issues_only", "exclude_test_data"
	BusinessDomain     string              `json:"business_domain,omitempty"`     // Will be populated in Stage 3
	ConfidenceScore    float64             `json:"confidence_score,omitempty"`    // 0.0 to 1.0
}

// MarshalJSON implements custom JSON marshaling for IntentIR
func (ir *IntentIR) MarshalJSON() ([]byte, error) {
	type Alias IntentIR
	return json.Marshal(&struct {
		*Alias
	}{
		Alias: (*Alias)(ir),
	})
}

// UnmarshalJSON implements custom JSON unmarshaling for IntentIR
func (ir *IntentIR) UnmarshalJSON(data []byte) error {
	type Alias IntentIR
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(ir),
	}
	return json.Unmarshal(data, &aux)
}