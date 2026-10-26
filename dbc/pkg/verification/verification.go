package verification

import (
	"fmt"
	"strings"

	"void2md/dbc/pkg/intent"
	"void2md/dbc/pkg/llm"
	"void2md/dbc/pkg/prompt"
)

// VerificationRole represents the role in multi-role verification
type VerificationRole string

const (
	RoleProposer VerificationRole = "proposer"
	RoleCritic   VerificationRole = "critic"
	RoleSummarizer VerificationRole = "summarizer"
)

// SQLCandidate represents a candidate SQL query with metadata
type SQLCandidate struct {
	SQL          string  `json:"sql"`
	Confidence   float64 `json:"confidence"`
	Source       string  `json:"source"` // How this candidate was generated
	ConsistencyScore float64 `json:"consistency_score,omitempty"`
}

// VerificationResult represents the result of multi-role verification
type VerificationResult struct {
	Candidates      []SQLCandidate `json:"candidates"`
	BestCandidate   *SQLCandidate  `json:"best_candidate,omitempty"`
	RequiresRollback bool          `json:"requires_rollback"`
	RollbackReason  string         `json:"rollback_reason,omitempty"`
}

// Verifier performs multi-role verification of SQL candidates against Intent IR
type Verifier struct {
	llmClient *llm.Client
}

// NewVerifier creates a new verifier
func NewVerifier(llmClient *llm.Client) *Verifier {
	return &Verifier{
		llmClient: llmClient,
	}
}

// VerifySQLCandidates verifies SQL candidates against Intent IR using multi-role approach
func (v *Verifier) VerifySQLCandidates(ir *intent.IntentIR, candidates []string, schemaContext string) (*VerificationResult, error) {
	var sqlCandidates []SQLCandidate

	// Convert string candidates to SQLCandidate objects
	for i, sql := range candidates {
		sqlCandidates = append(sqlCandidates, SQLCandidate{
			SQL:        sql,
			Confidence: 1.0 - float64(i)*0.1, // First candidate has highest confidence
			Source:     fmt.Sprintf("candidate_%d", i+1),
		})
	}

	// Role 1: Critic - Validate each candidate against Intent IR
	for i := range sqlCandidates {
		consistencyScore, err := v.criticValidate(ir, &sqlCandidates[i], schemaContext)
		if err != nil {
			return nil, fmt.Errorf("critic validation failed: %w", err)
		}
		sqlCandidates[i].ConsistencyScore = consistencyScore
	}

	// Check if rollback is needed (very low consistency scores)
	requiresRollback := false
	rollbackReason := ""

	minConsistency := 0.3 // Threshold for rollback
	for _, candidate := range sqlCandidates {
		if candidate.ConsistencyScore < minConsistency {
			requiresRollback = true
			rollbackReason = fmt.Sprintf("Candidate SQL has low consistency score (%.2f) with Intent IR", candidate.ConsistencyScore)
			break
		}
	}

	if requiresRollback {
		return &VerificationResult{
			Candidates:      sqlCandidates,
			RequiresRollback: true,
			RollbackReason:  rollbackReason,
		}, nil
	}

	// Role 2: Summarizer - Select the best candidate
	bestCandidate, err := v.summarizerSelectBest(ir, sqlCandidates, schemaContext)
	if err != nil {
		return nil, fmt.Errorf("summarizer selection failed: %w", err)
	}

	return &VerificationResult{
		Candidates:    sqlCandidates,
		BestCandidate: bestCandidate,
	}, nil
}

// criticValidate validates a SQL candidate against Intent IR
func (v *Verifier) criticValidate(ir *intent.IntentIR, candidate *SQLCandidate, schemaContext string) (float64, error) {
	criticPrompt := prompt.GetCriticVerificationPrompt(ir, candidate.SQL, schemaContext)

	criticResponse, err := v.llmClient.GenerateText(criticPrompt, 0.2, 500)
	if err != nil {
		return 0.0, fmt.Errorf("critic LLM call failed: %w", err)
	}

	// Parse critic response to extract consistency score
	// In a real implementation, this would be more robust JSON parsing
	consistencyScore := extractConsistencyScore(criticResponse)
	return consistencyScore, nil
}

// summarizerSelectBest selects the best SQL candidate from multiple options
func (v *Verifier) summarizerSelectBest(ir *intent.IntentIR, candidates []SQLCandidate, schemaContext string) (*SQLCandidate, error) {
	// Extract just the SQL strings for the prompt
	var candidateStrings []string
	for _, candidate := range candidates {
		candidateStrings = append(candidateStrings, candidate.SQL)
	}

	summarizerPrompt := prompt.GetSummarizerSelectionPrompt(ir, candidateStrings, schemaContext)

	_, err := v.llmClient.GenerateText(summarizerPrompt, 0.1, 500)
	if err != nil {
		return nil, fmt.Errorf("summarizer LLM call failed: %w", err)
	}

	// Parse summarizer response to select best candidate
	// For now, return the candidate with highest consistency score
	bestCandidate := &candidates[0]
	for i := 1; i < len(candidates); i++ {
		if candidates[i].ConsistencyScore > bestCandidate.ConsistencyScore {
			bestCandidate = &candidates[i]
		}
	}

	return bestCandidate, nil
}

// extractConsistencyScore extracts consistency score from critic response
// This is a simplified implementation - in production, use proper JSON parsing
func extractConsistencyScore(response string) float64 {
	responseLower := strings.ToLower(response)

	// Look for explicit score mentions
	if strings.Contains(responseLower, "high") || strings.Contains(responseLower, "consistent") {
		return 0.9
	} else if strings.Contains(responseLower, "medium") || strings.Contains(responseLower, "somewhat") {
		return 0.6
	} else if strings.Contains(responseLower, "low") || strings.Contains(responseLower, "inconsistent") {
		return 0.3
	}

	// Default to medium consistency
	return 0.7
}

// GenerateMultipleCandidates generates multiple SQL candidates for verification
func (v *Verifier) GenerateMultipleCandidates(ir *intent.IntentIR, schemaContext, dbType, version string) ([]string, error) {
	var candidates []string

	// Candidate 1: Standard LLM generation
	candidate1, err := v.llmClient.GenerateSQL(dbType, version, schemaContext, "")
	if err == nil && candidate1 != "" {
		candidates = append(candidates, candidate1)
	}

	// Candidate 2: Intent-focused generation (using Intent IR fields as context)
	intentContext := buildIntentContext(ir)
	candidate2Prompt := fmt.Sprintf("Generate SQL based on this intent: %s\nSchema: %s", intentContext, schemaContext)
	candidate2, err := v.llmClient.GenerateText(candidate2Prompt, 0.1, 800)
	if err == nil && candidate2 != "" {
		candidate2 = cleanSQL(candidate2)
		if candidate2 != "" {
			candidates = append(candidates, candidate2)
		}
	}

	// Candidate 3: Conservative generation (safe queries only)
	conservativePrompt := fmt.Sprintf("Generate a SAFE and CONSERVATIVE SQL query for this intent: %s\nSchema: %s\nOnly use SELECT statements with LIMIT clauses.", intentContext, schemaContext)
	candidate3, err := v.llmClient.GenerateText(conservativePrompt, 0.05, 600)
	if err == nil && candidate3 != "" {
		candidate3 = cleanSQL(candidate3)
		if candidate3 != "" {
			candidates = append(candidates, candidate3)
		}
	}

	// Ensure at least one candidate
	if len(candidates) == 0 {
		// Fallback to basic query
		candidates = append(candidates, generateFallbackQuery(dbType))
	}

	return candidates, nil
}

// buildIntentContext builds a natural language context from Intent IR
func buildIntentContext(ir *intent.IntentIR) string {
	var parts []string

	if len(ir.TargetMetrics) > 0 {
		parts = append(parts, fmt.Sprintf("Target metrics: %v", ir.TargetMetrics))
	}

	if ir.TimeSemantics.HasTimeConstraint {
		parts = append(parts, fmt.Sprintf("Time constraint: %s", ir.TimeSemantics.TimeRange))
	}

	if len(ir.FilterConditions) > 0 {
		var filterParts []string
		for _, filter := range ir.FilterConditions {
			filterParts = append(filterParts, fmt.Sprintf("%s %s %s", filter.Field, filter.Operator, filter.Value))
		}
		parts = append(parts, fmt.Sprintf("Filters: %s", strings.Join(filterParts, ", ")))
	}

	if len(ir.GroupingDimensions) > 0 {
		var groupParts []string
		for _, dim := range ir.GroupingDimensions {
			groupParts = append(groupParts, dim.Field)
		}
		parts = append(parts, fmt.Sprintf("Group by: %s", strings.Join(groupParts, ", ")))
	}

	return strings.Join(parts, "; ")
}

// cleanSQL cleans SQL output from LLM
func cleanSQL(sql string) string {
	sql = strings.TrimSpace(sql)
	sql = strings.TrimPrefix(sql, "```sql")
	sql = strings.TrimPrefix(sql, "```")
	sql = strings.TrimSuffix(sql, "```")
	return sql
}

// generateFallbackQuery generates a safe fallback query based on database type
func generateFallbackQuery(dbType string) string {
	switch dbType {
	case "mysql":
		return "SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() LIMIT 10;"
	case "postgresql":
		return "SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' LIMIT 10;"
	default:
		return "SELECT * FROM data LIMIT 10;"
	}
}