// Package config 提供项目级（服务级）配置：监听地址、端口与日志设置，
// 以及供各模块复用的环境变量读取工具函数。
// 模块级配置请放在各模块自己的 config 包中（如 file/pkg/config）。
package config

import (
	"os"
	"strconv"
	"strings"
	"sync"
)

// Config 项目级应用配置
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

// LoadConfig 加载项目级配置，使用 sync.Once 保证单例模式
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

// GetEnv 读取字符串环境变量，未设置时返回默认值
func GetEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// GetEnvInt 读取整型环境变量，未设置或非法时返回默认值
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

// GetEnvFloat64 读取浮点型环境变量，未设置或非法时返回默认值
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

// GetEnvStringSlice 读取逗号分隔的字符串切片环境变量，未设置或为空时返回默认值
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
