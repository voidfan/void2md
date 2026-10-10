package logger

import (
	"io"
	"log/slog"
	"os"
)

var (
	globalLogger *slog.Logger
)

// InitLogger 初始化全局 logger
// level: debug/info/warn/error
// format: json/plain
func InitLogger(level, format string) {
	var handler slog.Handler
	opts := &slog.HandlerOptions{
		Level: parseLevel(level),
	}

	// 默认输出到 stdout
	var w io.Writer = os.Stdout

	switch format {
	case "json":
		handler = slog.NewJSONHandler(w, opts)
	default:
		handler = slog.NewTextHandler(w, opts)
	}

	globalLogger = slog.New(handler)
	slog.SetDefault(globalLogger)
}

// GetLogger 返回带模块名的子 logger
// 使用 slog.With("module", name) 添加模块标识
func GetLogger(name string) *slog.Logger {
	if globalLogger != nil {
		return globalLogger.With("module", name)
	}
	// 如果未初始化，使用 slog 默认 logger 作为 fallback
	return slog.Default().With("module", name)
}

// parseLevel 将字符串级别转换为 slog.Level
func parseLevel(level string) slog.Level {
	switch level {
	case "DEBUG", "debug":
		return slog.LevelDebug
	case "INFO", "info":
		return slog.LevelInfo
	case "WARN", "warn", "WARNING", "warning":
		return slog.LevelWarn
	case "ERROR", "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
