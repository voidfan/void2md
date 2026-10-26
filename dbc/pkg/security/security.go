package security

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/sirupsen/logrus"
)

// Validator validates SQL queries for safety
type Validator struct {
	allowedOperations []string
	maxRows           int
	queryTimeout      int
	logger            *logrus.Logger
}

// NewValidator creates a new SQL validator
func NewValidator(allowedOperations []string, maxRows, queryTimeout int) *Validator {
	return &Validator{
		allowedOperations: allowedOperations,
		maxRows:           maxRows,
		queryTimeout:      queryTimeout,
		logger:            logrus.New(),
	}
}

// ValidateSQL checks if a SQL query is safe to execute
func (v *Validator) ValidateSQL(sql string) error {
	sql = strings.TrimSpace(strings.ToLower(sql))
	if sql == "" {
		return fmt.Errorf("empty SQL query")
	}

	// Check for dangerous operations
	dangerousPatterns := []string{
		`(?i)\bdrop\b`,
		`(?i)\btruncate\b`,
		`(?i)\balter\b`,
		`(?i)\bcreate\b`,
		`(?i)\bgrant\b`,
		`(?i)\brevoke\b`,
		`(?i)\bdelete\b\s+from\b.*\bwhere\b\s+1\s*=\s*1\b`,
		`(?i)\bupdate\b.*\bset\b.*\bwhere\b\s+1\s*=\s*1\b`,
		`(?i)\bexec\b`,
		`(?i)\bexecute\b`,
		`(?i)\bshutdown\b`,
		`(?i)\breboot\b`,
		`(?i)\bkill\b`,
	}

	for _, pattern := range dangerousPatterns {
		matched, err := regexp.MatchString(pattern, sql)
		if err != nil {
			return fmt.Errorf("failed to check dangerous pattern: %w", err)
		}
		if matched {
			return fmt.Errorf("dangerous SQL operation detected")
		}
	}

	// Check allowed operations
	allowed := false
	for _, op := range v.allowedOperations {
		if strings.HasPrefix(sql, op) {
			allowed = true
			break
		}
	}

	if !allowed {
		// Try to detect the operation from common patterns
		if strings.Contains(sql, "select") && !strings.Contains(sql, "insert") && !strings.Contains(sql, "update") && !strings.Contains(sql, "delete") {
			allowed = true
		} else if strings.Contains(sql, "insert") {
			allowed = contains(v.allowedOperations, "insert")
		} else if strings.Contains(sql, "update") {
			allowed = contains(v.allowedOperations, "update")
		} else if strings.Contains(sql, "delete") {
			allowed = contains(v.allowedOperations, "delete")
		}
	}

	if !allowed {
		return fmt.Errorf("operation not allowed by security policy")
	}

	return nil
}

// GetMaxRows returns the maximum number of rows allowed
func (v *Validator) GetMaxRows() int {
	return v.maxRows
}

// GetQueryTimeout returns the query timeout in seconds
func (v *Validator) GetQueryTimeout() int {
	return v.queryTimeout
}

// contains checks if a slice contains a string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}