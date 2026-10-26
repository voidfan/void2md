package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"

	"void2md/dbc/pkg/config"
	"void2md/dbc/pkg/database"
	"void2md/dbc/pkg/llm"
	"void2md/dbc/pkg/security"
)

// Service represents the API service
type Service struct {
	config *config.Config

	// mu protects the runtime-mutable LLM configuration and client.
	mu sync.RWMutex
	// llmCfg is the currently effective LLM config (mutable via /config/llm).
	llmCfg config.LLMConfig
	// defaultLLMCfg is the startup snapshot loaded from the config file,
	// used by /config/llm/reset.
	defaultLLMCfg config.LLMConfig
	llmClient     *llm.Client

	dbClient  database.Client
	validator *security.Validator
	logger    *logrus.Logger
}

// getLLMClient returns the current LLM client (safe against concurrent
// config updates via HandleLLMConfig / HandleResetLLMConfig).
func (s *Service) getLLMClient() *llm.Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.llmClient
}

// NewService creates a new API service
func NewService(cfg *config.Config) *Service {
	llmClient := llm.NewClient(cfg.LLM.Endpoint, cfg.LLM.Model, cfg.LLM.APIKey)

	// Create database client based on configuration
	var dbClient database.Client
	var err error

	dbClient, err = database.NewClient(
		database.DatabaseType(cfg.Database.Type),
		cfg.Database.Host,
		cfg.Database.Port,
		cfg.Database.Username,
		cfg.Database.Password,
		cfg.Database.Database,
		cfg.Database.Version,
	)

	if err != nil {
		// Do not exit the process: the dbc API is embedded in a larger service.
		// Continue with a nil client; handlers return 503 until it is available.
		log.Printf("WARNING: failed to create database client: %v (dbc API will return 503)", err)
		dbClient = nil
	}

	validator := security.NewValidator(cfg.Security.AllowedOperations, cfg.Security.MaxRows, cfg.Security.QueryTimeout)

	return &Service{
		config:        cfg,
		llmCfg:        cfg.LLM,
		defaultLLMCfg: cfg.LLM,
		llmClient:     llmClient,
		dbClient:      dbClient,
		validator:     validator,
		logger:        logrus.New(),
	}
}

// loadSemanticLayerFile loads semantic layer configuration from file
func loadSemanticLayerFile(filePath string) ([]byte, error) {
	// Try to load from the given path
	data, err := os.ReadFile(filePath)
	if err == nil {
		return data, nil
	}

	// If that fails, try with ./ prefix
	data, err = os.ReadFile("./" + filePath)
	if err == nil {
		return data, nil
	}

	return nil, fmt.Errorf("failed to load semantic layer file %s: %w", filePath, err)
}

// QueryRequest represents a natural language query request
type QueryRequest struct {
	Query        string `json:"query"`
	ConnectionID string `json:"connection_id,omitempty"`
}

// Connection represents a database connection
type Connection struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"db_type"`
}

// QueryResponse represents a query response
type QueryResponse struct {
	SQL      string                         `json:"sql,omitempty"`
	Result   *database.ExecuteQueryResponse `json:"result,omitempty"`
	Analysis string                         `json:"analysis,omitempty"` // LLM analysis of the results
	Error    string                         `json:"error,omitempty"`
}

// ConnectionsResponse represents a connections response
type ConnectionsResponse struct {
	Connections []Connection `json:"connections"`
	Error       string       `json:"error,omitempty"`
}

// SchemaResponse represents a schema response
type SchemaResponse struct {
	Context string `json:"context,omitempty"`
	Error   string `json:"error,omitempty"`
}

// HandleQuery handles natural language to SQL queries with enhanced multi-step approach
func (s *Service) HandleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.dbClient == nil {
		http.Error(w, "Database client not available", http.StatusServiceUnavailable)
		return
	}

	var req QueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Query == "" {
		http.Error(w, "Query is required", http.StatusBadRequest)
		return
	}

	// Step 1: Get comprehensive schema context including all tables and metadata
	schemaContext, err := s.dbClient.GetSchemaContext("")
	if err != nil {
		s.logger.Errorf("Failed to get schema context: %v", err)
		// Fallback: use minimal context instead of failing
		schemaContext = fmt.Sprintf("Database type: %s, Version: %s", s.dbClient.GetType(), s.dbClient.GetVersion())
	} else {
		s.logger.Infof("Retrieved schema context (%d chars): %.200s...", len(schemaContext), schemaContext)
	}

	// Step 2: First LLM call - Analyze user intent and suggest query strategy
	// Extract key table information from schema context to make it more manageable
	var relevantSchema string
	if len(schemaContext) > 20000 {
		// Focus on tables that might contain issue data
		relevantSchema = "Database contains many tables including potential issue-related tables. Key tables to consider: jiraissue, issuestatus, project, issuetype, priority, resolution, customfield, etc. Full schema available but truncated for clarity."
	} else {
		relevantSchema = schemaContext
	}

	intentPrompt := fmt.Sprintf(`You are a database expert. Analyze the following user request and suggest a query strategy.

	User Request: %s

	Database Schema Summary:
	%s

	Respond with a JSON object containing:
	1. "intent": Brief description of what the user wants
	2. "suggested_tables": Array of table names that might be relevant (focus on issue/ticket/task related tables)
	3. "query_approach": Suggested approach for querying (e.g., "count_and_details", "broad_search", "specific_lookup")

	Respond with ONLY valid JSON, nothing else.`, req.Query, relevantSchema)

	var intentAnalysis string
	var intentErr error
	intentAnalysis, intentErr = s.getLLMClient().GenerateText(intentPrompt, 0.3, 500)
	if intentErr != nil {
		s.logger.Warnf("Failed to analyze intent: %v", intentErr)
		// Continue with direct SQL generation if intent analysis fails
	} else {
		s.logger.Infof("Intent analysis: %s", intentAnalysis)
	}

	// Step 3: Generate initial SQL query using database-specific prompts
	var sql string

	// First try using intent analysis results if available
	intentUsed := false
	if intentAnalysis != "" {
		var intentResult map[string]interface{}
		if json.Unmarshal([]byte(intentAnalysis), &intentResult) == nil {
			if suggestedTables, ok := intentResult["suggested_tables"].([]interface{}); ok {
				tableNames := make([]string, len(suggestedTables))
				for i, table := range suggestedTables {
					if tableName, ok := table.(string); ok {
						tableNames[i] = tableName
					}
				}

				if len(tableNames) > 0 {
					targetedQuery := s.createTargetedQuery(tableNames, req.Query, schemaContext)
					if targetedQuery != "" {
						sql = targetedQuery
						intentUsed = true
						s.logger.Infof("Using targeted query from intent analysis: %s", sql)
					}
				}
			}
		}
	}

	// If intent analysis didn't provide a query, use standard LLM generation
	if !intentUsed {
		var genErr error
		sql, genErr = s.getLLMClient().GenerateSQL(string(s.dbClient.GetType()), s.dbClient.GetVersion(), schemaContext, req.Query)
		if genErr != nil {
			s.logger.Errorf("Failed to generate SQL: %v", genErr)
			http.Error(w, "Failed to generate SQL", http.StatusInternalServerError)
			return
		}
	}

	// Step 4: Validate and handle empty SQL with fallback strategies
	if strings.TrimSpace(sql) == "" {
		s.logger.Warnf("LLM returned empty SQL query, attempting fallback strategies...")

		// Fallback Strategy 1: Generate a broad exploratory query
		broadQueryPrompt := fmt.Sprintf(`You are a database expert. The user asked: "%s"

		Since we couldn't generate a specific query, create a broad exploratory query that will return useful data from the database.

		Database Schema Context:
		%s

		For MySQL/PostgreSQL databases, consider SAFE SELECT queries like:
		- SELECT table_name, table_type FROM information_schema.tables WHERE table_schema = DATABASE();
		- SELECT * FROM any_relevant_table LIMIT 10;
		- SELECT COUNT(*) as record_count, 'table_name' as source FROM table_name;

		Choose the most appropriate broad SELECT query based on the database type and schema.
		ONLY use SELECT statements - no SHOW, DESCRIBE, or other commands.

		Respond with ONLY the SQL query, nothing else.`, req.Query, schemaContext)

		broadSQL, err := s.getLLMClient().GenerateText(broadQueryPrompt, 0.2, 300)
		if err != nil {
			s.logger.Warnf("Fallback strategy 1 failed: %v", err)
		} else {
			broadSQL = strings.TrimSpace(broadSQL)
			broadSQL = strings.TrimPrefix(broadSQL, "```sql")
			broadSQL = strings.TrimPrefix(broadSQL, "```")
			broadSQL = strings.TrimSuffix(broadSQL, "```")

			if broadSQL != "" {
				sql = broadSQL
				s.logger.Infof("Using fallback broad query: %s", sql)
			}
		}

		// If still empty, use a very basic query
		if strings.TrimSpace(sql) == "" {
			dbType := string(s.dbClient.GetType())
			if dbType == "mysql" {
				sql = "SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() AND (table_name LIKE '%issue%' OR table_name LIKE '%ticket%' OR table_name LIKE '%task%' OR table_name LIKE '%work%' OR table_name LIKE 'AO_%') ORDER BY table_name LIMIT 20;"
			} else if dbType == "postgresql" {
				sql = "SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' AND (table_name LIKE '%issue%' OR table_name LIKE '%ticket%' OR table_name LIKE '%task%' OR table_name LIKE '%work%') ORDER BY table_name LIMIT 20;"
			} else {
				// For other databases, return an error
				s.logger.Errorf("Unable to generate any query for database type: %s", dbType)
				http.Error(w, "Unable to understand your request. Please try a simpler question or specify which tables you want to query.", http.StatusBadRequest)
				return
			}
			s.logger.Infof("Using basic fallback query: %s", sql)
		}
	}

	// Step 5: Execute the query with safety validation
	if err := s.validator.ValidateSQL(sql); err != nil {
		s.logger.Warnf("SQL validation failed, attempting to clean query: %v", err)
		// Try to clean the query by removing potentially dangerous parts
		cleanedSQL := s.cleanSQLQuery(sql)
		if cleanedSQL != "" && s.validator.ValidateSQL(cleanedSQL) == nil {
			sql = cleanedSQL
			s.logger.Infof("Using cleaned SQL query")
		} else {
			s.logger.Errorf("Could not clean SQL query: %s", sql)
			http.Error(w, fmt.Sprintf("Generated query contains unsafe operations: %v", err), http.StatusBadRequest)
			return
		}
	}

	// Step 6: Execute SQL with multiple attempts and error handling
	var result *database.ExecuteQueryResponse
	var executionErr error

	// First attempt with generated SQL
	result, executionErr = s.dbClient.ExecuteQuery(sql)

	// If query fails due to syntax or table issues, try direct data queries on common Jira tables
	if executionErr != nil {
		s.logger.Warnf("Query execution failed: %v", executionErr)

		// Get list of potential issue tables from schema context
		potentialTables := s.extractPotentialIssueTables(schemaContext)

		// Try querying actual data from potential tables
		for _, tableName := range potentialTables {
			// Try a safe count query first
			countQuery := fmt.Sprintf("SELECT COUNT(*) as pending_count FROM `%s` LIMIT 100", tableName)
			if s.validator.ValidateSQL(countQuery) == nil {
				s.logger.Infof("Trying count query on table %s: %s", tableName, countQuery)
				result, executionErr = s.dbClient.ExecuteQuery(countQuery)
				if executionErr == nil {
					s.logger.Infof("Successfully queried table %s", tableName)
					sql = countQuery // Update SQL for response
					break
				}
			}

			// If count fails, try a simple SELECT *
			selectQuery := fmt.Sprintf("SELECT * FROM `%s` LIMIT 5", tableName)
			if s.validator.ValidateSQL(selectQuery) == nil {
				s.logger.Infof("Trying select query on table %s: %s", tableName, selectQuery)
				result, executionErr = s.dbClient.ExecuteQuery(selectQuery)
				if executionErr == nil {
					s.logger.Infof("Successfully queried table %s", tableName)
					sql = selectQuery // Update SQL for response
					break
				}
			}
		}

		// If all data queries fail, fall back to metadata query
		if executionErr != nil {
			simpleQuery := s.getSimpleFallbackQuery(s.dbClient.GetType())
			if simpleQuery != "" {
				s.logger.Infof("Falling back to metadata query: %s", simpleQuery)
				result, executionErr = s.dbClient.ExecuteQuery(simpleQuery)
				if executionErr == nil {
					sql = simpleQuery
				}
			}
		}
	}

	// If all queries fail, return error with helpful message
	if executionErr != nil {
		s.logger.Errorf("All query attempts failed: %v", executionErr)
		http.Error(w, "Unable to execute queries on the database. The database might be unavailable or the schema might be different than expected.", http.StatusInternalServerError)
		return
	}

	// Step 7: Generate comprehensive analysis using LLM
	analysisPrompt := fmt.Sprintf(`You are a data analyst. Analyze the following query results and provide a comprehensive summary.

	Original User Question: %s

	Database Type: %s

	Query Results:
	Columns: %v
	Rows: %v

	Provide a detailed, natural language summary that answers the user's original question.
	If the results show table names or schema information, explain what tables exist and their potential relevance.
	If the results show actual data, summarize the key findings and insights.
	Be helpful and informative, even if the data doesn't directly answer the question.

	Respond in the same language as the original question.`,
		req.Query, s.dbClient.GetType(), result.Columns, result.Rows)

	var analysis string
	var analysisErr error
	analysis, analysisErr = s.getLLMClient().GenerateAnalysis(analysisPrompt)
	if analysisErr != nil {
		s.logger.Errorf("Failed to generate analysis: %v", analysisErr)
		// Don't fail the entire request if analysis fails, just return without analysis
		analysis = "Analysis unavailable - but here are the raw results."
	}

	resp := QueryResponse{
		SQL:      sql,
		Result:   result,
		Analysis: analysis,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// cleanSQLQuery attempts to remove potentially dangerous parts from SQL queries
func (s *Service) cleanSQLQuery(sql string) string {
	// Remove any DROP, DELETE, UPDATE, INSERT statements for safety
	lowerSQL := strings.ToLower(strings.TrimSpace(sql))
	if strings.Contains(lowerSQL, "drop") ||
		strings.Contains(lowerSQL, "delete") ||
		strings.Contains(lowerSQL, "update") ||
		strings.Contains(lowerSQL, "insert") ||
		strings.Contains(lowerSQL, "alter") ||
		strings.Contains(lowerSQL, "create") {
		return "" // Return empty if dangerous operations detected
	}

	// Allow only SELECT and SHOW statements
	if strings.HasPrefix(lowerSQL, "select") || strings.HasPrefix(lowerSQL, "show") {
		return sql
	}

	return ""
}

// getSimpleFallbackQuery returns a simple safe query based on database type
func (s *Service) getSimpleFallbackQuery(dbType database.DatabaseType) string {
	switch dbType {
	case database.MySQL:
		// Look for issue-related tables specifically
		return "SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() AND (table_name LIKE '%issue%' OR table_name LIKE '%ticket%' OR table_name LIKE '%task%' OR table_name LIKE '%work%' OR table_name LIKE 'AO_%') ORDER BY table_name LIMIT 20;"
	case database.PostgreSQL:
		return "SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' AND (table_name LIKE '%issue%' OR table_name LIKE '%ticket%' OR table_name LIKE '%task%' OR table_name LIKE '%work%') ORDER BY table_name LIMIT 20;"
	default:
		return ""
	}
}

// extractPotentialIssueTables extracts likely issue-related table names from schema context
func (s *Service) extractPotentialIssueTables(schemaContext string) []string {
	var potentialTables []string

	// Common Jira table names to try first
	commonJiraTables := []string{
		"jiraissue", "issues", "issue", "tickets", "ticket", "tasks", "task",
		"work_issues", "problem_tickets", "incidents", "bugs", "defects",
	}

	// Add AO_ tables from schema context
	lines := strings.Split(schemaContext, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "Table ") {
			// Extract table name from "Table TABLE_NAME: ..."
			parts := strings.Split(line, ":")
			if len(parts) > 0 {
				tableName := strings.TrimPrefix(parts[0], "Table ")
				tableName = strings.TrimSpace(tableName)
				if tableName != "" {
					potentialTables = append(potentialTables, tableName)
				}
			}
		}
	}

	// Prioritize common tables first, then add extracted tables
	result := make([]string, 0, len(commonJiraTables)+len(potentialTables))
	result = append(result, commonJiraTables...)

	// Add unique tables from schema that aren't already in common list
	existing := make(map[string]bool)
	for _, table := range commonJiraTables {
		existing[table] = true
	}

	for _, table := range potentialTables {
		if !existing[table] {
			result = append(result, table)
			existing[table] = true
		}
	}

	return result
}

// createTargetedQuery creates a SQL query based on suggested tables and user request
func (s *Service) createTargetedQuery(suggestedTables []string, userQuery, schemaContext string) string {
	// Create a targeted LLM prompt using the suggested tables
	targetedPrompt := fmt.Sprintf(`You are a MySQL expert specializing in Jira databases. Generate a valid MySQL query based on the following information.

	User Request: %s

	Suggested Tables: %v

	Database Schema Context:
	%s

	IMPORTANT RULES:
	1. Use ONLY standard MySQL syntax compatible with MySQL 5.7
	2. Always use backticks around table and column names: `+"`"+`table_name`+"`"+`, `+"`"+`column_name`+"`"+`
	3. Never use window functions or advanced features not available in MySQL 5.7
	4. Focus on querying actual data tables, not metadata tables
	5. For counting pending issues, look for status columns that indicate "pending", "open", "unresolved", etc.
	6. Include relevant details like issue key, summary, status, priority, assignee, etc.

	Respond with ONLY the SQL query, nothing else. Ensure the query is syntactically correct and executable on MySQL 5.7.`,
		userQuery, suggestedTables, schemaContext)

	targetedSQL, err := s.getLLMClient().GenerateText(targetedPrompt, 0.1, 800)
	if err != nil {
		s.logger.Warnf("Failed to generate targeted query: %v", err)
		return ""
	}

	// Clean the SQL
	targetedSQL = strings.TrimSpace(targetedSQL)
	targetedSQL = strings.TrimPrefix(targetedSQL, "```sql")
	targetedSQL = strings.TrimPrefix(targetedSQL, "```")
	targetedSQL = strings.TrimSuffix(targetedSQL, "```")

	return targetedSQL
}

// HandleListConnections handles listing available connections
func (s *Service) HandleListConnections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.dbClient == nil {
		http.Error(w, "Database client not available", http.StatusServiceUnavailable)
		return
	}

	// Return single connection based on configured database
	connection := Connection{
		ID:   "configured-db",
		Name: fmt.Sprintf("%s Database", s.dbClient.GetType()),
		Type: string(s.dbClient.GetType()),
	}

	resp := ConnectionsResponse{Connections: []Connection{connection}}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// HandleGetSchema handles getting schema information
func (s *Service) HandleGetSchema(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.dbClient == nil {
		http.Error(w, "Database client not available", http.StatusServiceUnavailable)
		return
	}

	tables := r.URL.Query().Get("tables")

	schemaContext, err := s.dbClient.GetSchemaContext(tables)
	if err != nil {
		s.logger.Errorf("Failed to get schema context: %v", err)
		resp := SchemaResponse{Error: err.Error()}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	resp := SchemaResponse{Context: schemaContext}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// SetupRoutes sets up the HTTP routes
func (s *Service) SetupRoutes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/query", s.HandleQuery)
	mux.HandleFunc("/connections", s.HandleListConnections)
	mux.HandleFunc("/schema", s.HandleGetSchema)
	mux.HandleFunc("/config/llm", s.HandleLLMConfig)
	mux.HandleFunc("/config/llm/reset", s.HandleResetLLMConfig)
	return mux
}
