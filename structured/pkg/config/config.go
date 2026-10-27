// Package config mirrors Python structured/config/settings.py.
// Mirrors pydantic-settings: env > .env > defaults, prefix DATIVE_, case-insensitive,
// extra keys ignored.
package config

import (
	"os"
	"strings"
)

const (
	EnvPrefix = "DATIVE_" // env_prefix="DATIVE_"

	EnvFile = ".env" // relative to the working directory

	DefaultLogLevel  = "INFO"
	DefaultLogFormat = "plain"
)

const (
	envDatabaseURL = EnvPrefix + "DATABASE_URL"
	envLogLevel    = EnvPrefix + "LOG_LEVEL"
	envLogFormat   = EnvPrefix + "LOG_FORMAT"
	envRedisURL    = EnvPrefix + "REDIS_URL"
)

// Settings mirrors Python's Settings class. No global singleton: callers cache it.
type Settings struct {
	DatabaseURL string // env DATIVE_DATABASE_URL, default ""
	LogLevel    string // env DATIVE_LOG_LEVEL, default "INFO"
	LogFormat   string // env DATIVE_LOG_FORMAT, default "plain"
	RedisURL    string // env DATIVE_REDIS_URL, default ""
}

// Load is equivalent to Python's `settings = Settings()`; re-read on every call.
func Load() *Settings {
	return LoadFrom(EnvFile)
}

// LoadFrom is Load with an explicit .env path (tests). A missing file is silently skipped.
func LoadFrom(envFile string) *Settings {
	dotenv := parseEnvFile(envFile)
	return &Settings{
		DatabaseURL: lookupSetting(envDatabaseURL, dotenv, ""),
		LogLevel:    lookupSetting(envLogLevel, dotenv, DefaultLogLevel),
		LogFormat:   lookupSetting(envLogFormat, dotenv, DefaultLogFormat),
		RedisURL:    lookupSetting(envRedisURL, dotenv, ""),
	}
}

// lookupSetting resolves "env var > .env > default". Python quirk (must be preserved):
// a key that exists with an empty value wins over the default.
func lookupSetting(name string, dotenv map[string]string, def string) string {
	if v, ok := os.LookupEnv(name); ok {
		return v
	}
	// pydantic-settings is case-insensitive.
	if v, ok := lookupEnvFold(name); ok {
		return v
	}
	if v, ok := lookupMapFold(dotenv, name); ok {
		return v
	}
	return def
}

// lookupEnvFold is a case-insensitive process-env lookup.
func lookupEnvFold(name string) (string, bool) {
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if ok && strings.EqualFold(k, name) {
			return v, true
		}
	}
	return "", false
}

// lookupMapFold is a case-insensitive lookup in parsed .env values.
func lookupMapFold(m map[string]string, name string) (string, bool) {
	if v, ok := m[name]; ok {
		return v, true
	}
	for k, v := range m {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return "", false
}

// parseEnvFile is a minimal .env parser (KEY=VALUE, '#' line comments, quotes stripped,
// "export " prefix allowed, later duplicates win; no ${VAR} expansion or multi-line values).
// Returns an empty map for a missing/unreadable file.
func parseEnvFile(path string) map[string]string {
	values := make(map[string]string)
	data, err := os.ReadFile(path)
	if err != nil {
		return values
	}
	// Splitting on \n also covers CRLF (TrimSpace removes the trailing \r).
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "export "); ok {
			line = strings.TrimSpace(rest)
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		values[key] = unquoteEnvValue(strings.TrimSpace(value))
	}
	return values
}

// unquoteEnvValue strips one matched pair of surrounding quotes.
func unquoteEnvValue(v string) string {
	if len(v) < 2 {
		return v
	}
	if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
		return v[1 : len(v)-1]
	}
	return v
}
