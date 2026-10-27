// Tests for config settings; cases pin down pydantic-settings quirks that must be preserved.
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// unsetEnv temporarily removes env vars (testing.T has no Unsetenv).
func unsetEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, key := range keys {
		old, existed := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(key, old)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
}

// clearDativeEnv isolates tests from external env vars.
func clearDativeEnv(t *testing.T) {
	t.Helper()
	unsetEnv(t, "DATIVE_DATABASE_URL", "DATIVE_LOG_LEVEL", "DATIVE_LOG_FORMAT", "DATIVE_REDIS_URL")
}

func writeEnvFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write .env: %v", err)
	}
	return path
}

// TestLoadDefaults: defaults with no .env and no env vars.
func TestLoadDefaults(t *testing.T) {
	clearDativeEnv(t)

	got := LoadFrom(filepath.Join(t.TempDir(), "not-exists.env"))
	if got.DatabaseURL != "" {
		t.Errorf("DatabaseURL = %q, want empty string", got.DatabaseURL)
	}
	if got.LogLevel != "INFO" {
		t.Errorf("LogLevel = %q, want INFO", got.LogLevel)
	}
	if got.LogFormat != "plain" {
		t.Errorf("LogFormat = %q, want plain", got.LogFormat)
	}
	if got.RedisURL != "" {
		t.Errorf("RedisURL = %q, want empty string", got.RedisURL)
	}
}

// TestLoadFromEnvFile: .env parsing shapes (quotes, comments, blank lines, export, case).
func TestLoadFromEnvFile(t *testing.T) {
	clearDativeEnv(t)

	path := writeEnvFile(t, strings.Join([]string{
		"# 注释行",
		"",
		"   ",
		"DATIVE_DATABASE_URL=postgres://user:pass@localhost:5432/db",
		`DATIVE_LOG_FORMAT="json"`,
		"export DATIVE_REDIS_URL='redis://localhost:6379/0'",
		"dative_log_level=WARNING", // case-insensitive
		"UNKNOWN_KEY=ignored",      // extra="ignore"
		"NOT_A_PAIR",               // skipped
	}, "\n"))

	got := LoadFrom(path)
	if got.DatabaseURL != "postgres://user:pass@localhost:5432/db" {
		t.Errorf("DatabaseURL = %q", got.DatabaseURL)
	}
	if got.LogFormat != "json" {
		t.Errorf("LogFormat = %q, want json with double quotes stripped", got.LogFormat)
	}
	if got.RedisURL != "redis://localhost:6379/0" {
		t.Errorf("RedisURL = %q, want single quotes stripped", got.RedisURL)
	}
	if got.LogLevel != "WARNING" {
		t.Errorf("LogLevel = %q, want lowercase keys to match too", got.LogLevel)
	}
}

// TestEnvVarOverridesEnvFile: precedence env var > .env > default.
func TestEnvVarOverridesEnvFile(t *testing.T) {
	clearDativeEnv(t)
	t.Setenv("DATIVE_LOG_LEVEL", "ERROR")

	path := writeEnvFile(t, "DATIVE_LOG_LEVEL=DEBUG\nDATIVE_LOG_FORMAT=plain\n")
	got := LoadFrom(path)

	if got.LogLevel != "ERROR" {
		t.Errorf("LogLevel = %q, want real env var to take precedence", got.LogLevel)
	}
	if got.LogFormat != "plain" {
		t.Errorf("LogFormat = %q, want value from .env", got.LogFormat)
	}
}

// TestEnvVarNameCaseInsensitive: pydantic-settings case_sensitive=False.
func TestEnvVarNameCaseInsensitive(t *testing.T) {
	clearDativeEnv(t)
	// Windows env vars are case-insensitive already; this checks the lookup's case tolerance.
	t.Setenv("dative_log_level", "CRITICAL")

	got := LoadFrom(filepath.Join(t.TempDir(), "not-exists.env"))
	if got.LogLevel != "CRITICAL" {
		t.Errorf("LogLevel = %q, want case-insensitive match", got.LogLevel)
	}
}

// TestEnvFileEmptyValueOverridesDefault: Python quirk — an existing key with empty value
// beats the default.
func TestEnvFileEmptyValueOverridesDefault(t *testing.T) {
	clearDativeEnv(t)

	path := writeEnvFile(t, "DATIVE_LOG_LEVEL=\n")
	if got := LoadFrom(path); got.LogLevel != "" {
		t.Errorf("LogLevel = %q, want empty string (pydantic-settings does not fall back to the default)", got.LogLevel)
	}
}

// TestEnvFileDuplicateKeyLastWins: python-dotenv duplicate-key semantics.
func TestEnvFileDuplicateKeyLastWins(t *testing.T) {
	clearDativeEnv(t)

	path := writeEnvFile(t, "DATIVE_LOG_LEVEL=DEBUG\nDATIVE_LOG_LEVEL=ERROR\n")
	if got := LoadFrom(path); got.LogLevel != "ERROR" {
		t.Errorf("LogLevel = %q, want later keys to override earlier ones", got.LogLevel)
	}
}

// TestLoadReadsCwdEnvFile: .env relative to the working directory.
func TestLoadReadsCwdEnvFile(t *testing.T) {
	clearDativeEnv(t)
	t.Chdir(t.TempDir())
	if err := os.WriteFile(".env", []byte("DATIVE_REDIS_URL=redis://cwd\n"), 0o600); err != nil {
		t.Fatalf("failed to write .env: %v", err)
	}

	if got := Load(); got.RedisURL != "redis://cwd" {
		t.Errorf("RedisURL = %q, want .env in the working directory to be read", got.RedisURL)
	}
}

// TestEnvFileCRLF: CRLF-terminated .env (Windows-edited files).
func TestEnvFileCRLF(t *testing.T) {
	clearDativeEnv(t)

	path := writeEnvFile(t, "# comment\r\nDATIVE_LOG_LEVEL=DEBUG\r\n\r\nDATIVE_REDIS_URL=redis://x\r\n")
	got := LoadFrom(path)
	if got.LogLevel != "DEBUG" {
		t.Errorf("LogLevel = %q, want DEBUG (CRLF handled)", got.LogLevel)
	}
	if got.RedisURL != "redis://x" {
		t.Errorf("RedisURL = %q", got.RedisURL)
	}
}
