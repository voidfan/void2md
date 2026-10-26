package clarification

import (
	"fmt"
	"strings"

	"void2md/dbc/pkg/ambiguity"
	"void2md/dbc/pkg/intent"
)

// ClarificationOption represents a possible interpretation for ambiguous input
type ClarificationOption struct {
	OptionID   string                 `json:"option_id"`
	OptionText string                 `json:"option_text"`
	IntentUpdate map[string]interface{} `json:"intent_update"` // How to update Intent IR if this option is selected
}

// ClarificationQuestion represents a question to resolve ambiguity
type ClarificationQuestion struct {
	QuestionID    string               `json:"question_id"`
	QuestionText  string               `json:"question_text"`
	Options       []ClarificationOption `json:"options"`
	AmbiguityType string               `json:"ambiguity_type"` // "schema_linking" or "query_intent" or "under_specified"
	Field         string               `json:"field,omitempty"` // Which field in IntentIR this clarifies
}

// ClarificationResponse represents the response to a clarification request
type ClarificationResponse struct {
	RequiresClarification bool                    `json:"requires_clarification"`
	Questions             []ClarificationQuestion `json:"questions,omitempty"`
	OriginalIntent        *intent.IntentIR        `json:"original_intent"`
}

// ClarificationGenerator generates clarification questions for detected ambiguities
type ClarificationGenerator struct {
	// Configuration for clarification generation
	MaxQuestions int
	MaxOptions   int
}

// NewClarificationGenerator creates a new clarification generator
func NewClarificationGenerator() *ClarificationGenerator {
	return &ClarificationGenerator{
		MaxQuestions: 3,
		MaxOptions:   4,
	}
}

// GenerateClarifications generates clarification questions from detected ambiguities
func (cg *ClarificationGenerator) GenerateClarifications(ir *intent.IntentIR, ambiguities []ambiguity.Ambiguity) *ClarificationResponse {
	if len(ambiguities) == 0 {
		return &ClarificationResponse{
			RequiresClarification: false,
			OriginalIntent:        ir,
		}
	}

	// Limit the number of questions
	questionCount := len(ambiguities)
	if questionCount > cg.MaxQuestions {
		questionCount = cg.MaxQuestions
	}

	var questions []ClarificationQuestion
	for i := 0; i < questionCount; i++ {
		question := cg.generateQuestionForAmbiguity(&ambiguities[i], ir)
		if question != nil {
			questions = append(questions, *question)
		}
	}

	return &ClarificationResponse{
		RequiresClarification: len(questions) > 0,
		Questions:             questions,
		OriginalIntent:        ir,
	}
}

// generateQuestionForAmbiguity generates a single clarification question for an ambiguity
func (cg *ClarificationGenerator) generateQuestionForAmbiguity(ambig *ambiguity.Ambiguity, ir *intent.IntentIR) *ClarificationQuestion {
	switch ambig.Type {
	case ambiguity.AmbiguityTypeSchemaLinking:
		return cg.generateSchemaLinkingQuestion(ambig, ir)
	case ambiguity.AmbiguityTypeQueryIntent:
		return cg.generateQueryIntentQuestion(ambig, ir)
	case ambiguity.AmbiguityTypeUnderSpecified:
		return cg.generateUnderSpecifiedQuestion(ambig, ir)
	default:
		return nil
	}
}

// generateSchemaLinkingQuestion generates a question for schema linking ambiguity
func (cg *ClarificationGenerator) generateSchemaLinkingQuestion(ambig *ambiguity.Ambiguity, ir *intent.IntentIR) *ClarificationQuestion {
	questionText := "I need help understanding your request better."

	// Customize question text based on the field
	if ambig.Field == "target_metrics" {
		questionText = "What do you mean by the metric you mentioned?"
	} else if ambig.Field == "filter_conditions" {
		questionText = "Can you clarify the filter condition you want to apply?"
	}

	options := cg.generateSchemaLinkingOptions(ambig, ir)

	return &ClarificationQuestion{
		QuestionID:    fmt.Sprintf("schema_link_%s", ambig.Field),
		QuestionText:  questionText,
		Options:       options,
		AmbiguityType: string(ambig.Type),
		Field:         ambig.Field,
	}
}

// generateQueryIntentQuestion generates a question for query intent ambiguity
func (cg *ClarificationGenerator) generateQueryIntentQuestion(ambig *ambiguity.Ambiguity, ir *intent.IntentIR) *ClarificationQuestion {
	questionText := "I'm not sure what kind of query you want. Can you clarify?"

	options := []ClarificationOption{
		{
			OptionID:   "option_1",
			OptionText: "Show me a summary/count of records",
			IntentUpdate: map[string]interface{}{
				"query_type": intent.QueryTypeCount,
			},
		},
		{
			OptionID:   "option_2",
			OptionText: "Show me detailed records with all fields",
			IntentUpdate: map[string]interface{}{
				"query_type": intent.QueryTypeSelect,
			},
		},
		{
			OptionID:   "option_3",
			OptionText: "Show me aggregated data grouped by categories",
			IntentUpdate: map[string]interface{}{
				"query_type": intent.QueryTypeAggregate,
			},
		},
	}

	return &ClarificationQuestion{
		QuestionID:    "query_intent_general",
		QuestionText:  questionText,
		Options:       options,
		AmbiguityType: string(ambig.Type),
	}
}

// generateUnderSpecifiedQuestion generates a question for under-specified queries
func (cg *ClarificationGenerator) generateUnderSpecifiedQuestion(ambig *ambiguity.Ambiguity, ir *intent.IntentIR) *ClarificationQuestion {
	switch ambig.Field {
	case "time_semantics":
		return &ClarificationQuestion{
			QuestionID:   "time_constraint",
			QuestionText: "What time period are you interested in?",
			Options: []ClarificationOption{
				{
					OptionID:   "last_7_days",
					OptionText: "Last 7 days",
					IntentUpdate: map[string]interface{}{
						"time_semantics": map[string]interface{}{
							"has_time_constraint": true,
							"time_range": "last_7_days",
						},
					},
				},
				{
					OptionID:   "last_30_days",
					OptionText: "Last 30 days",
					IntentUpdate: map[string]interface{}{
						"time_semantics": map[string]interface{}{
							"has_time_constraint": true,
							"time_range": "last_30_days",
						},
					},
				},
				{
					OptionID:   "this_month",
					OptionText: "This month",
					IntentUpdate: map[string]interface{}{
						"time_semantics": map[string]interface{}{
							"has_time_constraint": true,
							"time_range": "this_month",
						},
					},
				},
				{
					OptionID:   "all_time",
					OptionText: "All time (no time constraint)",
					IntentUpdate: map[string]interface{}{
						"time_semantics": map[string]interface{}{
							"has_time_constraint": false,
						},
					},
				},
			},
			AmbiguityType: string(ambig.Type),
			Field:         ambig.Field,
		}
	case "grouping_dimensions":
		return &ClarificationQuestion{
			QuestionID:   "grouping_dims",
			QuestionText: "How would you like to group the results?",
			Options: []ClarificationOption{
				{
					OptionID:   "by_status",
					OptionText: "Group by status",
					IntentUpdate: map[string]interface{}{
						"grouping_dimensions": []map[string]interface{}{
							{"field": "status"},
						},
					},
				},
				{
					OptionID:   "by_priority",
					OptionText: "Group by priority",
					IntentUpdate: map[string]interface{}{
						"grouping_dimensions": []map[string]interface{}{
							{"field": "priority"},
						},
					},
				},
				{
					OptionID:   "by_assignee",
					OptionText: "Group by assignee",
					IntentUpdate: map[string]interface{}{
						"grouping_dimensions": []map[string]interface{}{
							{"field": "assignee"},
						},
					},
				},
				{
					OptionID:   "no_grouping",
					OptionText: "Don't group (show individual records)",
					IntentUpdate: map[string]interface{}{
						"grouping_dimensions": []map[string]interface{}{},
					},
				},
			},
			AmbiguityType: string(ambig.Type),
			Field:         ambig.Field,
		}
	default:
		return &ClarificationQuestion{
			QuestionID:   "general_clarification",
			QuestionText: "Can you provide more details about what you're looking for?",
			Options: []ClarificationOption{
				{
					OptionID:   "more_details",
					OptionText: "Yes, I'll provide more details",
					IntentUpdate: map[string]interface{}{},
				},
				{
					OptionID:   "proceed_anyway",
					OptionText: "Proceed with your best guess",
					IntentUpdate: map[string]interface{}{},
				},
			},
			AmbiguityType: string(ambig.Type),
		}
	}
}

// generateSchemaLinkingOptions generates options for schema linking ambiguity
// This is a simplified implementation - in a real system, this would use
// semantic layer mappings and vector similarity to generate relevant options
func (cg *ClarificationGenerator) generateSchemaLinkingOptions(ambig *ambiguity.Ambiguity, ir *intent.IntentIR) []ClarificationOption {
	// For now, return generic options based on common business terms
	if containsBusinessTerm(ambig.Description) {
		return []ClarificationOption{
			{
				OptionID:   "option_1",
				OptionText: "Revenue/Gross Sales",
				IntentUpdate: map[string]interface{}{
					"target_metrics": []string{"revenue_gross"},
				},
			},
			{
				OptionID:   "option_2",
				OptionText: "Order Count",
				IntentUpdate: map[string]interface{}{
					"target_metrics": []string{"order_count"},
				},
			},
			{
				OptionID:   "option_3",
				OptionText: "Customer Count",
				IntentUpdate: map[string]interface{}{
					"target_metrics": []string{"customer_count"},
				},
			},
			{
				OptionID:   "option_4",
				OptionText: "Other/Custom",
				IntentUpdate: map[string]interface{}{
					"target_metrics": []string{"custom_metric"},
				},
			},
		}
	}

	// Default options
	return []ClarificationOption{
		{
			OptionID:   "option_a",
			OptionText: "Option A",
			IntentUpdate: map[string]interface{}{},
		},
		{
			OptionID:   "option_b",
			OptionText: "Option B",
			IntentUpdate: map[string]interface{}{},
		},
		{
			OptionID:   "option_c",
			OptionText: "Option C",
			IntentUpdate: map[string]interface{}{},
		},
	}
}

// Helper function to check if description contains business terms
func containsBusinessTerm(description string) bool {
	businessTerms := []string{"sales", "revenue", "income", "profit", "cost", "price", "value", "metric", "measure"}
	descLower := strings.ToLower(description)
	for _, term := range businessTerms {
		if strings.Contains(descLower, term) {
			return true
		}
	}
	return false
}

// ApplyClarificationResponse updates an IntentIR based on user's clarification choices
func ApplyClarificationResponse(originalIR *intent.IntentIR, selectedOptions map[string]string) (*intent.IntentIR, error) {
	// Create a copy of the original intent
	newIR := &intent.IntentIR{}
	*newIR = *originalIR

	// This is a simplified implementation - in a real system, this would
	// properly merge the intent updates from selected options
	// For now, we'll just return the original intent as-is
	// The actual implementation would need to handle JSON merging properly

	return newIR, nil
}