// Package logger provides the project-wide logging: a global slog logger
// (InitLogger/GetLogger) plus Python logging-style facilities (pylogger.go).
package logger

import (
	"log/slog"
	"os"
)

var globalLogger *slog.Logger

// InitLogger initializes the global logger and the slog default.
// level: DEBUG/INFO/WARN|WARNING/ERROR/CRITICAL (case-insensitive), Python
// numeric strings ("10".."50"), or NOTSET; unknown falls back to INFO.
// format: ""/"plain" -> text with file:line (no color); "json" -> single-line
// JSON; anything else -> colored output per DefaultMessageFormat.
// Output goes to stdout.
func InitLogger(level, format string) {
	globalLogger = slog.New(NewHandler(level, format, os.Stdout))
	slog.SetDefault(globalLogger)
}

// GetLogger returns a sub-logger tagged with the module name.
// Call it after InitLogger (or lazily at use time) so the configured
// level/format take effect.
func GetLogger(name string) *slog.Logger {
	if globalLogger != nil {
		return globalLogger.With("module", name)
	}
	return slog.Default().With("module", name)
}
