// Tests for the Python logging-style facilities (merged from the structured
// module); cases pin down logging quirks that must be preserved.
package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"testing"
)

// TestParseLevel: level-name parsing like logging.setLevel (case-insensitive).
func TestParseLevel(t *testing.T) {
	tests := []struct {
		in   string
		want slog.Level
	}{
		{"DEBUG", slog.LevelDebug},
		{"INFO", slog.LevelInfo},
		{"WARNING", slog.LevelWarn},
		{"WARN", slog.LevelWarn},
		{"ERROR", slog.LevelError},
		{"CRITICAL", LevelCritical},
		{"FATAL", LevelCritical},
		{"NOTSET", slog.LevelWarn},
		{"0", slog.LevelWarn},
		{"10", slog.LevelDebug},
		{"20", slog.LevelInfo},
		{"30", slog.LevelWarn},
		{"40", slog.LevelError},
		{"50", LevelCritical},
		// Case-insensitive (unlike Python's case-sensitive setLevel).
		{"debug", slog.LevelDebug},
		{"bogus", slog.LevelInfo},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := ParseLevel(tt.in); got != tt.want {
				t.Fatalf("ParseLevel(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestNotSetInheritsRootLevel: Python NOTSET means "inherit from parent"; root
// defaults to WARNING(30), so NOTSET is equivalent to WARNING.
func TestNotSetInheritsRootLevel(t *testing.T) {
	got, err := NewFacadeLogger("NOTSET", "plain")
	if err != nil {
		t.Fatalf("NewFacadeLogger(NOTSET) should not error: %v", err)
	}
	if got.Level() != 30 {
		t.Errorf("level = %d, want 30 (root's default WARNING)", got.Level())
	}
	if got.Enabled(10) {
		t.Error("DEBUG should be dropped under NOTSET (Python isEnabledFor(DEBUG) is False)")
	}
	if !got.Enabled(30) {
		t.Error("WARNING should be allowed under NOTSET")
	}
	if ParseLevel("NOTSET") != slog.LevelWarn {
		t.Errorf("ParseLevel(NOTSET) = %v, want WARN", ParseLevel("NOTSET"))
	}
}

// TestLoggerPlainFormat: plain layout "…INFO     logger_test.go:line | message" —
// 5 spaces because %(levelname)-8s pads INFO to 8 plus the separator space.
func TestLoggerPlainFormat(t *testing.T) {
	var buf bytes.Buffer
	l := NewLoggerTo(&buf, "DEBUG", "plain")
	l.Info("hello", "k", "v")

	line := buf.String()
	re := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} INFO {5}logger_test\.go:\d+ \| hello k=v\n$`)
	if !re.MatchString(line) {
		t.Fatalf("plain output does not match: %q", line)
	}
	if strings.Contains(line, "\033[") {
		t.Fatalf("plain output must not contain ANSI colors: %q", line)
	}
}

// TestLoggerColorFormat: ANSI codes count toward the width, so "%-8s" produces
// no padding when colored (Python quirk).
func TestLoggerColorFormat(t *testing.T) {
	var buf bytes.Buffer
	l := NewLoggerTo(&buf, "DEBUG", "color")
	l.Info("hello")

	line := buf.String()
	if !strings.Contains(line, "\033[32mINFO\033[0m logger_test.go:") {
		t.Fatalf("colored output does not match Python ColoredFormatter semantics: %q", line)
	}
	if strings.Contains(line, "INFO    ") {
		t.Fatalf("no padding to 8 when colored: %q", line)
	}
}

// TestLoggerColorMapping: ColoredFormatter.COLORS verbatim.
func TestLoggerColorMapping(t *testing.T) {
	tests := []struct {
		level string
		want  string
	}{
		{"DEBUG", "\033[34m"},
		{"INFO", "\033[32m"},
		{"WARNING", "\033[33m"},
		{"ERROR", "\033[31m"},
		{"CRITICAL", "\033[35m"},
	}
	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			var buf bytes.Buffer
			l := NewLoggerTo(&buf, "DEBUG", "color")

			switch tt.level {
			case "DEBUG":
				l.Debug("m")
			case "INFO":
				l.Info("m")
			case "WARNING":
				l.Warn("m")
			case "ERROR":
				l.Error("m")
			case "CRITICAL":
				l.Log(context.Background(), LevelCritical, "m")
			}

			if !strings.Contains(buf.String(), tt.want+tt.level+"\033[0m") {
				t.Fatalf("output for level %s = %q", tt.level, buf.String())
			}
		})
	}
}

// TestLoggerJSONFormat: format=json single-line output.
func TestLoggerJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	l := NewLoggerTo(&buf, "DEBUG", "json")
	l.Info("hello", "k", "v")

	line := strings.TrimSpace(buf.String())
	if strings.Contains(line, "\033[") {
		t.Fatalf("json output must not contain ANSI colors: %q", line)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(line), &parsed); err != nil {
		t.Fatalf("json output cannot be parsed: %v (raw: %q)", err, line)
	}
	if parsed["level"] != "INFO" || parsed["msg"] != "hello" || parsed["file"] != "logger_test.go" || parsed["k"] != "v" {
		t.Fatalf("json fields do not match expectations: %v", parsed)
	}
	if _, ok := parsed["time"]; !ok {
		t.Fatalf("json output is missing the time field: %v", parsed)
	}
}

// TestLoggerLevelFilter: records below the configured level are dropped.
func TestLoggerLevelFilter(t *testing.T) {
	var buf bytes.Buffer
	l := NewLoggerTo(&buf, "WARNING", "plain")

	l.Info("should be dropped")
	if buf.Len() != 0 {
		t.Fatalf("INFO record should not be emitted: %q", buf.String())
	}

	l.Warn("kept")
	if buf.Len() == 0 {
		t.Fatal("WARNING record should be emitted")
	}
}

// TestPackageLogger: package-level Log()/SetLog().
func TestPackageLogger(t *testing.T) {
	t.Cleanup(func() { SetLog(nil) })

	if Log() == nil {
		t.Fatal("Log() should not return nil")
	}

	var buf bytes.Buffer
	custom := NewLoggerTo(&buf, "DEBUG", "plain")
	SetLog(custom)
	if Log() != custom {
		t.Fatal("Log() should return the overridden logger after SetLog")
	}
	Log().Info("via package logger")
	if !strings.Contains(buf.String(), "logger_test.go:") {
		t.Fatalf("unexpected package logger output: %q", buf.String())
	}
}
