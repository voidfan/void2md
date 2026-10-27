// Python logging-style facilities merged from the structured module:
// a custom slog.Handler (text with file:line / single-line JSON / ANSI color),
// Python level names and a logging-style facade. Format values: ""/"plain"
// (no color), "json", anything else colored per DefaultMessageFormat.
package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// levelColors matches Python's ColoredFormatter.COLORS (excluding RESET).
var levelColors = map[string]string{
	"DEBUG":    "\033[34m",
	"INFO":     "\033[32m",
	"WARNING":  "\033[33m",
	"ERROR":    "\033[31m",
	"CRITICAL": "\033[35m",
}

// colorReset corresponds to ColoredFormatter.COLORS["RESET"].
const colorReset = "\033[0m"

// DefaultMessageFormat is the Python-style layout used by the colored form.
const DefaultMessageFormat = "%(asctime)s %(levelname)-8s %(filename)s:%(lineno)d | %(message)s"

// DefaultDateFormat corresponds to ColoredFormatter's datefmt="%Y-%m-%d %H:%M:%S".
const DefaultDateFormat = "%Y-%m-%d %H:%M:%S"

// goDateFormat is the Go time layout equivalent to DefaultDateFormat.
const goDateFormat = "2006-01-02 15:04:05"

// levelNameWidth corresponds to `%(levelname)-8s` in the format string.
const levelNameWidth = 8

// levelNumbers is Python's logging._nameToLevel (with WARN/FATAL aliases).
var levelNumbers = map[string]int{
	"CRITICAL": 50,
	"FATAL":    50,
	"ERROR":    40,
	"WARNING":  30,
	"WARN":     30,
	"INFO":     20,
	"DEBUG":    10,
	"NOTSET":   0,
}

// LevelCritical corresponds to logging.CRITICAL; slog has no CRITICAL, so 12
// (ERROR+4) stands in for the "above ERROR" tier.
const LevelCritical = slog.Level(12)

// pyLevelName maps an slog level back to the Python levelname.
func pyLevelName(level slog.Level) string {
	switch {
	case level < slog.LevelInfo:
		return "DEBUG"
	case level < slog.LevelWarn:
		return "INFO"
	case level < slog.LevelError:
		return "WARNING"
	case level < LevelCritical:
		return "ERROR"
	default:
		return "CRITICAL"
	}
}

// effectiveLevel replicates Python's NOTSET handling: NOTSET(0) inherits the
// root logger's default WARNING(30).
func effectiveLevel(n int) int {
	if n == 0 {
		return 30
	}
	return n
}

// ParseLevel converts a level name (case-insensitive) or Python numeric string
// ("10".."50") to an slog level; unknown values fall back to INFO.
func ParseLevel(level string) slog.Level {
	n, ok := levelNumbers[strings.ToUpper(strings.TrimSpace(level))]
	if !ok {
		if _, err := fmt.Sscanf(level, "%d", &n); err != nil {
			return slog.LevelInfo
		}
	}
	n = effectiveLevel(n)
	switch {
	case n < 20:
		return slog.LevelDebug
	case n < 30:
		return slog.LevelInfo
	case n < 40:
		return slog.LevelWarn
	case n < 50:
		return slog.LevelError
	default:
		return LevelCritical
	}
}

// renderLogLine replicates logging.Formatter.format: the levelname is colored
// first, then %(...) substitution runs — so with color the width padding never
// kicks in (ANSI codes count toward the width, a Python quirk).
func renderLogLine(format string, color bool, now time.Time, levelName, filename string, lineno int, msg string) string {
	shaped := levelName
	if color {
		shaped = levelColors[levelName] + levelName + colorReset
	}
	fields := map[string]string{
		"asctime":   now.Format(goDateFormat),
		"levelname": shaped,
		"filename":  filename,
		"lineno":    fmt.Sprintf("%d", lineno),
		"message":   msg,
	}
	var b strings.Builder
	b.Grow(len(format) + len(msg))
	for i := 0; i < len(format); {
		if format[i] != '%' || i+1 >= len(format) || format[i+1] != '(' {
			b.WriteByte(format[i])
			i++
			continue
		}
		end := strings.IndexByte(format[i:], ')')
		if end < 0 {
			b.WriteString(format[i:])
			break
		}
		name := format[i+2 : i+end]
		val, ok := fields[name]
		if !ok {
			b.WriteString(format[i : i+end+1])
			i += end + 1
			continue
		}
		i += end + 1
		leftAlign := false
		width := 0
		if i < len(format) && format[i] == '-' {
			leftAlign = true
			i++
		}
		for i < len(format) && format[i] >= '0' && format[i] <= '9' {
			width = width*10 + int(format[i]-'0')
			i++
		}
		if i < len(format) {
			i++
		}
		if pad := width - len(val); pad > 0 {
			if leftAlign {
				val += strings.Repeat(" ", pad)
			} else {
				val = strings.Repeat(" ", pad) + val
			}
		}
		b.WriteString(val)
	}
	return b.String()
}

// baseName corresponds to %(filename)s.
func baseName(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

// Logger is a Python logging-style facade (Debug/Info/Warning/Error/Critical
// with a single message string).
type Logger struct {
	level  int
	format string
	out    io.Writer
	color  bool
	mu     sync.Mutex
}

// NewFacadeLogger corresponds to Python's get_logeer. level is case-sensitive;
// unknown names error like Python's setLevel ValueError. An empty format uses
// DefaultMessageFormat.
func NewFacadeLogger(level string, format string) (*Logger, error) {
	n, ok := levelNumbers[level]
	if !ok {
		return nil, fmt.Errorf("Unknown level: '%s'", level)
	}
	if format == "" {
		format = DefaultMessageFormat
	}
	return &Logger{level: effectiveLevel(n), format: format, out: os.Stderr, color: true}, nil
}

// SetOutput replaces the output target (for tests).
func (l *Logger) SetOutput(w io.Writer) { l.out = w }

// SetColor toggles ANSI colors (default colored).
func (l *Logger) SetColor(on bool) { l.color = on }

// Level returns the numeric level (Python's logger.level).
func (l *Logger) Level() int { return l.level }

// Enabled corresponds to logger.isEnabledFor(level).
func (l *Logger) Enabled(level int) bool { return level >= l.level }

// Debug corresponds to log.debug.
func (l *Logger) Debug(msg string) { l.log(10, "DEBUG", 2, msg) }

// Info corresponds to log.info.
func (l *Logger) Info(msg string) { l.log(20, "INFO", 2, msg) }

// Warning corresponds to log.warning.
func (l *Logger) Warning(msg string) { l.log(30, "WARNING", 2, msg) }

// Error corresponds to log.error.
func (l *Logger) Error(msg string) { l.log(40, "ERROR", 2, msg) }

// Critical corresponds to log.critical.
func (l *Logger) Critical(msg string) { l.log(50, "CRITICAL", 2, msg) }

func (l *Logger) log(level int, levelName string, skip int, msg string) {
	if !l.Enabled(level) {
		return
	}
	_, file, line, ok := runtime.Caller(skip)
	if !ok {
		file, line = "<unknown>", 0
	}
	line1 := renderLogLine(l.format, l.color, time.Now(), levelName, baseName(file), line, msg)
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintln(l.out, line1)
}

// Handler is a slog.Handler emitting the Python-style text format
// ("2006-01-02 15:04:05 INFO     main.go:35 | message k=v") or single-line JSON.
type Handler struct {
	opts   handlerOptions
	attrs  []slog.Attr
	groups []string
	mu     *sync.Mutex
}

type handlerOptions struct {
	level     slog.Level
	color     bool
	asJSON    bool
	msgFormat string
	out       io.Writer
}

// NewHandler constructs a Handler writing to w (os.Stderr when nil).
func NewHandler(level, format string, w io.Writer) *Handler {
	if w == nil {
		w = os.Stderr
	}
	return &Handler{
		opts: handlerOptions{
			level:     ParseLevel(level),
			color:     useColor(format),
			asJSON:    isJSONFormat(format),
			msgFormat: DefaultMessageFormat,
			out:       w,
		},
		mu: &sync.Mutex{},
	}
}

// isPlainFormat reports whether this is the no-color form.
func isPlainFormat(format string) bool {
	return format == "" || format == "plain"
}

// isJSONFormat reports whether this is JSON line output.
func isJSONFormat(format string) bool {
	return strings.EqualFold(strings.TrimSpace(format), "json")
}

// useColor: colored when neither plain nor json.
func useColor(format string) bool {
	return !isPlainFormat(format) && !isJSONFormat(format)
}

// Enabled drops records below the configured level.
func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.opts.level
}

// Handle renders one log record.
func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	ts := r.Time
	if ts.IsZero() {
		ts = time.Now()
	}
	level := pyLevelName(r.Level)
	file, line := sourceOf(r.PC)

	var b strings.Builder
	if h.opts.asJSON {
		h.writeJSON(&b, ts.Format(goDateFormat), level, file, line, r)
	} else {
		b.WriteString(renderLogLine(h.opts.msgFormat, h.opts.color, ts, level, file, line, r.Message))
		h.writeTextAttrs(&b, r)
		b.WriteByte('\n')
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.opts.out, b.String())
	return err
}

// WithAttrs derives a Handler carrying fixed attributes (slog.Handler contract).
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	clone := *h
	clone.attrs = make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	clone.attrs = append(clone.attrs, h.attrs...)
	clone.attrs = append(clone.attrs, attrs...)
	return &clone
}

// WithGroup derives a Handler with a group; attribute keys get a "group." prefix.
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	clone.groups = make([]string, 0, len(h.groups)+1)
	clone.groups = append(clone.groups, h.groups...)
	clone.groups = append(clone.groups, name)
	return &clone
}

// writeTextAttrs appends structured attributes to the text line.
func (h *Handler) writeTextAttrs(b *strings.Builder, r slog.Record) {
	write := func(a slog.Attr) {
		b.WriteByte(' ')
		b.WriteString(attrKey(h.groups, a.Key))
		b.WriteByte('=')
		b.WriteString(fmt.Sprintf("%v", a.Value.Resolve().Any()))
	}
	for _, a := range h.attrs {
		write(a)
	}
	r.Attrs(func(a slog.Attr) bool {
		write(a)
		return true
	})
}

// writeJSON renders single-line JSON.
func (h *Handler) writeJSON(b *strings.Builder, timeText, level, file string, line int, r slog.Record) {
	first := true
	b.WriteByte('{')
	writeJSONField(b, &first, "time", timeText)
	writeJSONField(b, &first, "level", level)
	writeJSONField(b, &first, "file", file)
	writeJSONField(b, &first, "line", line)
	writeJSONField(b, &first, "msg", r.Message)
	writeAttr := func(a slog.Attr) {
		writeJSONField(b, &first, attrKey(h.groups, a.Key), a.Value.Resolve().Any())
	}
	for _, a := range h.attrs {
		writeAttr(a)
	}
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(a)
		return true
	})
	b.WriteString("}\n")
}

func writeJSONField(b *strings.Builder, first *bool, key string, value any) {
	if !*first {
		b.WriteByte(',')
	}
	*first = false
	b.Write(jsonValue(key))
	b.WriteByte(':')
	b.Write(jsonValue(value))
}

// jsonValue serializes one value; SetEscapeHTML(false) matches Python json.dumps escaping.
func jsonValue(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return []byte(`""`)
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// attrKey folds WithGroup names into the attribute key.
func attrKey(groups []string, key string) string {
	if len(groups) == 0 {
		return key
	}
	return strings.Join(groups, ".") + "." + key
}

// sourceOf resolves caller file/line from record.PC (for %(filename)s / %(lineno)d).
func sourceOf(pc uintptr) (string, int) {
	if pc == 0 {
		return "<unknown>", 0
	}
	frames := runtime.CallersFrames([]uintptr{pc})
	frame, _ := frames.Next()
	return baseName(frame.File), frame.Line
}

// NewLoggerTo is NewLogger with an explicit output target (tests).
func NewLoggerTo(w io.Writer, level, format string) *slog.Logger {
	return slog.New(NewHandler(level, format, w))
}

// NewLogger builds a standalone slog.Logger (stderr, Python parity). Service
// code should use InitLogger + GetLogger instead.
func NewLogger(level, format string) *slog.Logger {
	return NewLoggerTo(os.Stderr, level, format)
}

var (
	logMu   sync.Mutex
	logInst *slog.Logger
)

// Log returns the package-level logger: the SetLog override if any, else the
// global logger configured by InitLogger, else a stdout INFO default.
func Log() *slog.Logger {
	logMu.Lock()
	defer logMu.Unlock()
	if logInst != nil {
		return logInst
	}
	if globalLogger != nil {
		return globalLogger
	}
	logInst = NewLoggerTo(os.Stdout, "INFO", DefaultMessageFormat)
	return logInst
}

// SetLog overrides the package-level logger (tests / custom output); pass nil
// to fall back to the global logger.
func SetLog(l *slog.Logger) {
	logMu.Lock()
	defer logMu.Unlock()
	logInst = l
}
