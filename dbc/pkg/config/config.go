package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config represents the service configuration
type Config struct {
	LLM      LLMConfig            `json:"llm"`
	DBX      DBXConfig            `json:"dbx"`
	Database DatabaseConfig        `json:"database"`
	Security SecurityConfig       `json:"security"`
	AgenticWorkflow AgenticWorkflowConfig `json:"agentic_workflow,omitempty"`
}

// LLMConfig holds LLM settings
type LLMConfig struct {
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"`
	APIKey   string `json:"api_key"`
}

// DBXConfig holds dbx MCP settings (kept for compatibility)
type DBXConfig struct {
	MCPURL       string `json:"mcp_url"`
	MCPToken     string `json:"mcp_token"`
	ConnectionID string `json:"connection_id"`
}

// DatabaseConfig holds database connection settings
type DatabaseConfig struct {
	Type     string `json:"type"`     // mysql, postgresql, redis, mongodb, elasticsearch
	Host     string `json:"host"`
	Port     string `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	Database string `json:"database"`
	Version  string `json:"version"`  // mysql: "5.7", postgresql: "13", etc.
}

// SecurityConfig holds security settings
type SecurityConfig struct {
	AllowedOperations []string `json:"allowed_operations"`
	MaxRows           int      `json:"max_rows"`
	QueryTimeout      int      `json:"query_timeout_seconds"`
}

// AgenticWorkflowConfig holds agentic workflow settings
type AgenticWorkflowConfig struct {
	Enabled bool `json:"enabled"`
	SemanticLayer SemanticLayerConfig `json:"semantic_layer"`
	Clarification ClarificationConfig `json:"clarification"`
	VectorSearch VectorSearchConfig `json:"vector_search"`
}

// SemanticLayerConfig holds semantic layer settings
type SemanticLayerConfig struct {
	Enabled  bool   `json:"enabled"`
	FilePath string `json:"file_path"`
}

// ClarificationConfig holds clarification settings
type ClarificationConfig struct {
	Enabled             bool    `json:"enabled"`
	MaxQuestions        int     `json:"max_questions"`
	ConfidenceThreshold float64 `json:"confidence_threshold"`
}

// VectorSearchConfig holds vector search settings
type VectorSearchConfig struct {
	Enabled    bool   `json:"enabled"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
}

// LoadSemanticLayerFile loads semantic layer configuration from file
func LoadSemanticLayerFile(filePath string) ([]byte, error) {
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

// LoadConfig loads configuration from a JSON file
func LoadConfig(filePath string) (*Config, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Backward compatibility: if database section is empty but mysql section exists, migrate it
	// Note: This requires parsing the raw JSON to check for mysql section
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse config file for backward compatibility: %w", err)
	}

	if config.Database.Type == "" {
		if mysqlConfig, ok := raw["mysql"].(map[string]interface{}); ok {
			// Migrate from old mysql section to new database section
			config.Database.Type = "mysql"
			if host, ok := mysqlConfig["host"].(string); ok {
				config.Database.Host = host
			}
			if port, ok := mysqlConfig["port"].(string); ok {
				config.Database.Port = port
			}
			if username, ok := mysqlConfig["username"].(string); ok {
				config.Database.Username = username
			}
			if password, ok := mysqlConfig["password"].(string); ok {
				config.Database.Password = password
			}
			if database, ok := mysqlConfig["database"].(string); ok {
				config.Database.Database = database
			}
			// Version can be set to default
			config.Database.Version = "8.0"
		}
	}

	// Set default values if not provided
	if config.Security.MaxRows == 0 {
		config.Security.MaxRows = 1000
	}
	if config.Security.QueryTimeout == 0 {
		config.Security.QueryTimeout = 30
	}
	if len(config.Security.AllowedOperations) == 0 {
		config.Security.AllowedOperations = []string{"select", "insert", "update", "delete"}
	}

	// Set database defaults based on type
	if config.Database.Type == "" {
		config.Database.Type = "mysql"
	}
	if config.Database.Host == "" {
		config.Database.Host = "localhost"
	}
	if config.Database.Port == "" {
		switch config.Database.Type {
		case "mysql":
			config.Database.Port = "3306"
		case "postgresql":
			config.Database.Port = "5432"
		case "redis":
			config.Database.Port = "6379"
		case "mongodb":
			config.Database.Port = "27017"
		case "elasticsearch":
			config.Database.Port = "9200"
		default:
			config.Database.Port = "3306"
		}
	}
	if config.Database.Username == "" {
		switch config.Database.Type {
		case "mysql":
			config.Database.Username = "jirauser"
		case "postgresql":
			config.Database.Username = "postgres"
		case "redis":
			config.Database.Username = ""
		case "mongodb":
			config.Database.Username = ""
		case "elasticsearch":
			config.Database.Username = ""
		default:
			config.Database.Username = "jirauser"
		}
	}
	if config.Database.Password == "" {
		switch config.Database.Type {
		case "mysql":
			config.Database.Password = "JiraPass@2026"
		case "postgresql":
			config.Database.Password = ""
		case "redis":
			config.Database.Password = ""
		case "mongodb":
			config.Database.Password = ""
		case "elasticsearch":
			config.Database.Password = ""
		default:
			config.Database.Password = "JiraPass@2026"
		}
	}
	if config.Database.Database == "" {
		switch config.Database.Type {
		case "mysql":
			config.Database.Database = "jiradb"
		case "postgresql":
			config.Database.Database = "postgres"
		case "redis":
			config.Database.Database = "0"
		case "mongodb":
			config.Database.Database = "admin"
		case "elasticsearch":
			config.Database.Database = ""
		default:
			config.Database.Database = "jiradb"
		}
	}

	// Set agentic workflow defaults
	if !config.AgenticWorkflow.Enabled {
		config.AgenticWorkflow.Enabled = true // Enable by default
	}

	if config.AgenticWorkflow.SemanticLayer.Enabled && config.AgenticWorkflow.SemanticLayer.FilePath == "" {
		config.AgenticWorkflow.SemanticLayer.FilePath = "./semantic_layer.json"
	}

	if config.AgenticWorkflow.Clarification.Enabled {
		if config.AgenticWorkflow.Clarification.MaxQuestions == 0 {
			config.AgenticWorkflow.Clarification.MaxQuestions = 3
		}
		if config.AgenticWorkflow.Clarification.ConfidenceThreshold == 0 {
			config.AgenticWorkflow.Clarification.ConfidenceThreshold = 0.7
		}
	}

	if config.AgenticWorkflow.VectorSearch.Enabled {
		if config.AgenticWorkflow.VectorSearch.Model == "" {
			config.AgenticWorkflow.VectorSearch.Model = "all-MiniLM-L6-v2"
		}
		if config.AgenticWorkflow.VectorSearch.Dimensions == 0 {
			config.AgenticWorkflow.VectorSearch.Dimensions = 384
		}
	}

	return &config, nil
}