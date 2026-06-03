package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

func Configure(levelName, colorMode string) {
	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(levelName)) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(NewPrettyHandler(os.Stdout, level, shouldColorize(colorMode, os.Stdout))))
}

func NewPrettyHandler(out io.Writer, level slog.Level, color bool) slog.Handler {
	return &prettySlogHandler{out: out, level: level, color: color, mu: &sync.Mutex{}}
}

type prettySlogHandler struct {
	out    io.Writer
	level  slog.Level
	color  bool
	mu     *sync.Mutex
	attrs  []slog.Attr
	groups []string
}

func (h *prettySlogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *prettySlogHandler) Handle(ctx context.Context, record slog.Record) error {
	var attrs []slog.Attr
	attrs = append(attrs, h.attrs...)
	record.Attrs(func(attr slog.Attr) bool {
		attrs = append(attrs, attr)
		return true
	})

	component, attrs := popAttr(attrs, "component")
	line := strings.Builder{}
	line.WriteString(colorize(record.Time.Format("15:04:05"), ansiDim, h.color))
	line.WriteString(" ")
	line.WriteString(colorize(formatLevel(record.Level), levelColor(record.Level), h.color))
	line.WriteString(" ")
	if component != "" {
		line.WriteString(colorize(component, ansiCyan, h.color))
		line.WriteString(colorize(" · ", ansiDim, h.color))
	}
	line.WriteString(record.Message)
	for _, attr := range attrs {
		attr.Value = attr.Value.Resolve()
		if attr.Key == "" {
			continue
		}
		line.WriteString(" ")
		line.WriteString(colorize(attr.Key, ansiDim, h.color))
		line.WriteString("=")
		line.WriteString(formatSlogValue(attr.Value))
	}
	line.WriteString("\n")

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.out, line.String())
	return err
}

func (h *prettySlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &next
}

func (h *prettySlogHandler) WithGroup(name string) slog.Handler {
	next := *h
	next.groups = append(append([]string{}, h.groups...), name)
	return &next
}

func popAttr(attrs []slog.Attr, key string) (string, []slog.Attr) {
	for i, attr := range attrs {
		if attr.Key == key {
			value := formatSlogValue(attr.Value.Resolve())
			attrs = append(attrs[:i], attrs[i+1:]...)
			return value, attrs
		}
	}
	return "", attrs
}

func formatLevel(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return "ERROR"
	case level >= slog.LevelWarn:
		return "WARN "
	case level <= slog.LevelDebug:
		return "DEBUG"
	default:
		return "INFO "
	}
}

const (
	ansiReset  = "\x1b[0m"
	ansiDim    = "\x1b[2m"
	ansiCyan   = "\x1b[36m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
	ansiBlue   = "\x1b[34m"
)

func levelColor(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return ansiRed
	case level >= slog.LevelWarn:
		return ansiYellow
	case level <= slog.LevelDebug:
		return ansiBlue
	default:
		return ansiGreen
	}
}

func colorize(text, color string, enabled bool) string {
	if !enabled || color == "" {
		return text
	}
	return color + text + ansiReset
}

func shouldColorize(mode string, out io.Writer) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "always":
		return true
	case "never":
		return false
	default:
		file, ok := out.(*os.File)
		if !ok {
			return false
		}
		var stat syscall.Stat_t
		if err := syscall.Fstat(int(file.Fd()), &stat); err != nil {
			return false
		}
		return stat.Mode&syscall.S_IFMT == syscall.S_IFCHR
	}
}

func formatSlogValue(value slog.Value) string {
	switch value.Kind() {
	case slog.KindString:
		text := value.String()
		if text == "" {
			return `""`
		}
		if strings.ContainsAny(text, " \t\n=") {
			return fmt.Sprintf("%q", text)
		}
		return text
	case slog.KindTime:
		return value.Time().Format(time.RFC3339)
	case slog.KindDuration:
		return value.Duration().String()
	case slog.KindGroup:
		parts := make([]string, 0, len(value.Group()))
		for _, attr := range value.Group() {
			parts = append(parts, attr.Key+"="+formatSlogValue(attr.Value.Resolve()))
		}
		return "{" + strings.Join(parts, ",") + "}"
	default:
		return value.String()
	}
}
