package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/sirupsen/logrus"

	"void2md/dbc/pkg/intent"
	"void2md/dbc/pkg/prompt"
)

// Client represents an LLM client for OpenAI-compatible APIs
type Client struct {
	endpoint string
	model    string
	apiKey   string
	client   *http.Client
	logger   *logrus.Logger
}

// NewClient creates a new LLM client
func NewClient(endpoint, model, apiKey string) *Client {
	return &Client{
		endpoint: endpoint,
		model:    model,
		apiKey:   apiKey,
		client:   &http.Client{},
		logger:   logrus.New(),
	}
}

// GenerateText generates text from a prompt using LLM
func (c *Client) GenerateText(prompt string, temperature float64, maxTokens int) (string, error) {
	requestBody := map[string]interface{}{
		"model": c.model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature": temperature,
		"max_tokens":  maxTokens,
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", c.endpoint+"/chat/completions", bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("LLM API returned status %d: %s", resp.StatusCode, string(body))
	}

	var apiResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	// Extract the text from the response
	choices, ok := apiResp["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return "", fmt.Errorf("no choices in LLM response")
	}

	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("invalid choice format in LLM response")
	}

	message, ok := choice["message"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("invalid message format in LLM response")
	}

	content, ok := message["content"].(string)
	if !ok {
		return "", fmt.Errorf("invalid content format in LLM response")
	}

	return content, nil
}

// GenerateSQL generates SQL from natural language query using LLM with database-specific prompts
func (c *Client) GenerateSQL(dbType, version, schemaContext, naturalLanguageQuery string) (string, error) {
	// Use the prompt system to get database-specific prompt
	promptText := prompt.GetSQLGenerationPrompt(dbType, version, schemaContext, naturalLanguageQuery)

	sql, err := c.GenerateText(promptText, 0.1, 1000)
	if err != nil {
		return "", err
	}

	// Clean and validate the SQL
	sql = strings.TrimSpace(sql)
	// Remove any markdown code blocks
	sql = strings.TrimPrefix(sql, "```sql")
	sql = strings.TrimPrefix(sql, "```")
	sql = strings.TrimSuffix(sql, "```")

	return sql, nil
}

// GenerateAnalysis generates analysis of query results using LLM
func (c *Client) GenerateAnalysis(prompt string) (string, error) {
	analysis, err := c.GenerateText(prompt, 0.3, 1000)
	if err != nil {
		return "", err
	}

	// Clean the analysis
	analysis = strings.TrimSpace(analysis)
	analysis = strings.TrimPrefix(analysis, "```")
	analysis = strings.TrimSuffix(analysis, "```")

	return analysis, nil
}

// DecomposeIntent decomposes a natural language query into structured Intent IR
func (c *Client) DecomposeIntent(naturalLanguageQuery, schemaContext string) (*intent.IntentIR, error) {
	intentPrompt := prompt.GetIntentDecompositionPrompt(naturalLanguageQuery, schemaContext)

	intentJSON, err := c.GenerateText(intentPrompt, 0.2, 800)
	if err != nil {
		return nil, fmt.Errorf("failed to decompose intent: %w", err)
	}

	// Clean the JSON
	intentJSON = strings.TrimSpace(intentJSON)
	intentJSON = strings.TrimPrefix(intentJSON, "```json")
	intentJSON = strings.TrimPrefix(intentJSON, "```")
	intentJSON = strings.TrimSuffix(intentJSON, "```")

	var intentIR intent.IntentIR
	if err := json.Unmarshal([]byte(intentJSON), &intentIR); err != nil {
		return nil, fmt.Errorf("failed to parse intent JSON: %w", err)
	}

	return &intentIR, nil
}

// GenerateClarificationQuestions generates clarification questions for ambiguous intents
func (c *Client) GenerateClarificationQuestions(intentIR *intent.IntentIR, ambiguities []string) (string, error) {
	clarificationPrompt := prompt.GetClarificationPrompt(intentIR, ambiguities)

	clarificationJSON, err := c.GenerateText(clarificationPrompt, 0.3, 1000)
	if err != nil {
		return "", fmt.Errorf("failed to generate clarification questions: %w", err)
	}

	// Clean the JSON
	clarificationJSON = strings.TrimSpace(clarificationJSON)
	clarificationJSON = strings.TrimPrefix(clarificationJSON, "```json")
	clarificationJSON = strings.TrimPrefix(clarificationJSON, "```")
	clarificationJSON = strings.TrimSuffix(clarificationJSON, "```")

	return clarificationJSON, nil
}
