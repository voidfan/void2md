package prompt

import (
	"encoding/json"
	"fmt"

	"void2md/dbc/pkg/intent"
)

// GetSQLGenerationPrompt returns a database-specific prompt for SQL generation
func GetSQLGenerationPrompt(dbType, version, schemaContext, naturalLanguageQuery string) string {
	switch dbType {
	case "mysql":
		return getMySQLPrompt(version, schemaContext, naturalLanguageQuery)
	case "postgresql":
		return getPostgreSQLPrompt(version, schemaContext, naturalLanguageQuery)
	case "redis":
		return getRedisPrompt(schemaContext, naturalLanguageQuery)
	case "mongodb":
		return getMongoDBPrompt(schemaContext, naturalLanguageQuery)
	case "elasticsearch":
		return getElasticsearchPrompt(schemaContext, naturalLanguageQuery)
	default:
		return getDefaultPrompt(dbType, schemaContext, naturalLanguageQuery)
	}
}

// GetIntentDecompositionPrompt returns a prompt for decomposing natural language queries into structured Intent IR
func GetIntentDecompositionPrompt(naturalLanguageQuery, schemaContext string) string {
	return fmt.Sprintf(`You are a database query intent analyzer. Analyze the following natural language query and decompose it into a structured Intent Intermediate Representation (Intent IR).

Natural Language Query:
%s

Database Schema Context:
%s

Respond with a JSON object containing the following fields:
- "query_type": One of "SELECT", "COUNT", "AGGREGATE", "JOIN", "FILTER"
- "target_metrics": Array of business metrics mentioned (e.g., ["issue_count", "resolution_time"])
- "time_semantics": Object with "has_time_constraint" (boolean), "time_field" (string), "time_range" (string), "time_granularity" (string)
- "filter_conditions": Array of filter objects with "field", "operator", "value", "value_type"
- "grouping_dimensions": Array of grouping objects with "field", "aggregation", "alias"
- "sorting": Array of sort objects with "field", "direction"
- "implicit_assumptions": Array of implicit assumptions (e.g., ["active_issues_only"])
- "business_domain": String representing the business domain (e.g., "support", "sales")
- "confidence_score": Number between 0.0 and 1.0 indicating confidence in decomposition

Use the semantic layer concepts when identifying target metrics and business domains.
If you are uncertain about any field, set appropriate default values and lower the confidence score.

Respond with ONLY valid JSON, nothing else.`,
		naturalLanguageQuery, schemaContext)
}

// GetClarificationPrompt returns a prompt for generating clarification questions
func GetClarificationPrompt(intentIR *intent.IntentIR, ambiguities []string) string {
	intentJSON, _ := json.Marshal(intentIR)
	ambiguityList := ""
	for _, amb := range ambiguities {
		ambiguityList += fmt.Sprintf("- %s\n", amb)
	}

	return fmt.Sprintf(`You are a database query clarification assistant. The following intent has been detected with potential ambiguities:

Intent IR:
%s

Detected Ambiguities:
%s

Generate 3-4 multiple-choice clarification questions to resolve these ambiguities. Each question should present clear options that map to different interpretations.

Respond with a JSON array of question objects, each containing:
- "question_text": The clarification question
- "options": Array of option objects with "option_text" and "intent_update" (how selecting this option would modify the Intent IR)

Respond with ONLY valid JSON, nothing else.`,
		string(intentJSON), ambiguityList)
}

// GetCriticVerificationPrompt returns a prompt for critic role in verification
func GetCriticVerificationPrompt(ir *intent.IntentIR, sql string, schemaContext string) string {
	intentJSON, _ := json.Marshal(ir)
	return fmt.Sprintf(`You are a SQL query critic. Evaluate how well the following SQL query matches the intended query intent.

Intent IR:
%s

Generated SQL:
%s

Database Schema Context:
%s

Evaluate the consistency between the SQL and the Intent IR. Consider:
1. Does the SQL query the correct tables and columns for the target metrics?
2. Are the filter conditions properly implemented?
3. Is the grouping/aggregation consistent with the intent?
4. Are time constraints properly handled?
5. Does the query type (SELECT, COUNT, etc.) match the intent?

Respond with a JSON object containing:
- "consistency_score": Number between 0.0 and 1.0
- "issues": Array of specific issues found
- "recommendations": Array of improvement suggestions

Respond with ONLY valid JSON, nothing else.`,
		string(intentJSON), sql, schemaContext)
}

// GetSummarizerSelectionPrompt returns a prompt for summarizer role in verification
func GetSummarizerSelectionPrompt(ir *intent.IntentIR, candidates []string, schemaContext string) string {
	intentJSON, _ := json.Marshal(ir)

	return fmt.Sprintf(`You are a SQL query summarizer. Select the best SQL query from the following candidates based on consistency with the Intent IR.

Intent IR:
%s

SQL Candidates:
%v

Database Schema Context:
%s

Select the candidate that best matches the Intent IR while being safe and executable.

Respond with a JSON object containing:
- "selected_candidate_index": Integer index of the selected candidate (0-based)
- "reason": Brief explanation for the selection

Respond with ONLY valid JSON, nothing else.`,
		string(intentJSON), candidates, schemaContext)
}

// getMySQLPrompt returns MySQL-specific prompt
func getMySQLPrompt(version, schemaContext, naturalLanguageQuery string) string {
	baseRules := `IMPORTANT RULES:
1. Use ONLY standard MySQL syntax compatible with MySQL %s
2. Always use backticks around table and column names: ` + "`" + `table_name` + "`" + `, ` + "`" + `column_name` + "`" + `
3. Never use window functions or advanced features not available in MySQL %s
4. Always specify the database schema explicitly if needed
5. Use proper JOIN syntax with ON clauses
6. Avoid using reserved keywords as aliases
7. For counting, use COUNT(*) not COUNT()
8. Always test your query mentally before responding`

	rules := fmt.Sprintf(baseRules, version, version)

	return fmt.Sprintf(`You are a MySQL expert specializing in databases. Generate a valid MySQL query based on the following schema context and natural language request.

%s

Database Schema Context:
%s

Natural Language Request:
%s

Respond with ONLY the SQL query, nothing else. Do not include any explanations, comments, or markdown formatting. Ensure the query is syntactically correct and executable on MySQL %s.`,
		rules, schemaContext, naturalLanguageQuery, version)
}

// getPostgreSQLPrompt returns PostgreSQL-specific prompt
func getPostgreSQLPrompt(version, schemaContext, naturalLanguageQuery string) string {
	return fmt.Sprintf(`You are a PostgreSQL expert. Generate a valid PostgreSQL query based on the following schema context and natural language request.

IMPORTANT RULES:
1. Use ONLY standard PostgreSQL syntax compatible with PostgreSQL %s
2. Always use double quotes around table and column names if they contain special characters: "table_name", "column_name"
3. Use proper schema qualification: schema.table_name
4. Use proper JOIN syntax with ON clauses
5. For counting, use COUNT(*)
6. Always test your query mentally before responding

Database Schema Context:
%s

Natural Language Request:
%s

Respond with ONLY the SQL query, nothing else. Do not include any explanations, comments, or markdown formatting. Ensure the query is syntactically correct and executable on PostgreSQL %s.`,
		version, schemaContext, naturalLanguageQuery, version)
}

// getRedisPrompt returns Redis-specific prompt
func getRedisPrompt(schemaContext, naturalLanguageQuery string) string {
	return fmt.Sprintf(`You are a Redis expert. Generate a valid Redis command based on the following context and natural language request.

IMPORTANT RULES:
1. Use ONLY standard Redis commands
2. Provide the exact Redis command syntax
3. Include all required arguments
4. For complex operations, provide multiple commands if needed

Redis Context:
%s

Natural Language Request:
%s

Respond with ONLY the Redis command(s), nothing else. Do not include any explanations, comments, or markdown formatting.`,
		schemaContext, naturalLanguageQuery)
}

// getMongoDBPrompt returns MongoDB-specific prompt
func getMongoDBPrompt(schemaContext, naturalLanguageQuery string) string {
	return fmt.Sprintf(`You are a MongoDB expert. Generate a valid MongoDB query based on the following schema context and natural language request.

IMPORTANT RULES:
1. Use MongoDB query syntax with proper JSON/BSON structure
2. Provide the collection name and query/filter object
3. Use proper MongoDB operators like $match, $group, etc. for aggregations
4. Always include the database name if needed

MongoDB Schema Context:
%s

Natural Language Request:
%s

Respond with ONLY the MongoDB query in JSON format, nothing else. Do not include any explanations, comments, or markdown formatting.`,
		schemaContext, naturalLanguageQuery)
}

// getElasticsearchPrompt returns Elasticsearch-specific prompt
func getElasticsearchPrompt(schemaContext, naturalLanguageQuery string) string {
	return fmt.Sprintf(`You are an Elasticsearch expert. Generate a valid Elasticsearch query based on the following schema context and natural language request.

IMPORTANT RULES:
1. Use Elasticsearch Query DSL syntax with proper JSON structure
2. Provide the index name and query object
3. Use proper Elasticsearch operators and aggregations
4. Always include the HTTP method (GET, POST) and endpoint if needed

Elasticsearch Schema Context:
%s

Natural Language Request:
%s

Respond with ONLY the Elasticsearch query in JSON format, nothing else. Do not include any explanations, comments, or markdown formatting.`,
		schemaContext, naturalLanguageQuery)
}

// getDefaultPrompt returns a generic prompt for unsupported databases
func getDefaultPrompt(dbType, schemaContext, naturalLanguageQuery string) string {
	return fmt.Sprintf(`You are a %s database expert. Generate a valid query based on the following schema context and natural language request.

Database Schema Context:
%s

Natural Language Request:
%s

Respond with ONLY the query, nothing else. Do not include any explanations, comments, or markdown formatting.`,
		dbType, schemaContext, naturalLanguageQuery)
}