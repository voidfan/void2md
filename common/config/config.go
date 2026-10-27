// Package config
package config

import (
	"os"
	"strconv"
	"strings"
	"sync"
)

// Config
type Config struct {
	Host      string `json:"host"`
	Port      int    `json:"port"`
	LogLevel  string `json:"logLevel"`
	LogFormat string `json:"logFormat"`
}

var (
	cfg  *Config
	once sync.Once
)

// LoadConfig
func LoadConfig() *Config {
	once.Do(func() {
		cfg = &Config{}
		cfg.loadFromEnv()
	})
	return cfg
}

func (c *Config) loadFromEnv() {
	c.Host = GetEnv("VOID2MD_HOST", "0.0.0.0")
	c.Port = GetEnvInt("VOID2MD_PORT", 3457)
	c.LogLevel = GetEnv("VOID2MD_LOG_LEVEL", "INFO")
	c.LogFormat = GetEnv("VOID2MD_LOG_FORMAT", "plain")
}

// GetEnv
func GetEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// GetEnvInt
func GetEnvInt(key string, defaultVal int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return defaultVal
	}
	return n
}

// GetEnvFloat64
func GetEnvFloat64(key string, defaultVal float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return defaultVal
	}
	return n
}

// GetEnvStringSlice reads a comma-separated list env var; returns the default when unset or empty.
func GetEnvStringSlice(key string, defaultVal []string) []string {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	parts := strings.Split(v, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	if len(result) == 0 {
		return defaultVal
	}
	return result
}
