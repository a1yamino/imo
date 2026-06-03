package logging

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestPrettySlogHandlerFormatsReadableLine(t *testing.T) {
	var out strings.Builder
	logger := slog.New(NewPrettyHandler(&out, slog.LevelDebug, false)).With("component", "agent_runtime")

	logger.InfoContext(context.Background(), "agent run started",
		"run_id", "run-1",
		"session_id", "session-1",
		"duration_ms", 12,
	)

	line := out.String()
	for _, want := range []string{
		"INFO",
		"agent_runtime · agent run started",
		"run_id=run-1",
		"session_id=session-1",
		"duration_ms=12",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("log line %q does not contain %q", line, want)
		}
	}
}

func TestPrettySlogHandlerRespectsLevel(t *testing.T) {
	var out strings.Builder
	logger := slog.New(NewPrettyHandler(&out, slog.LevelWarn, false))

	logger.Info("hidden")
	logger.Warn("visible", "at", time.Date(2026, 6, 3, 3, 30, 0, 0, time.UTC))

	line := out.String()
	if strings.Contains(line, "hidden") {
		t.Fatalf("info log should be filtered: %q", line)
	}
	if !strings.Contains(line, "WARN") || !strings.Contains(line, "visible") {
		t.Fatalf("warn log missing: %q", line)
	}
}

func TestPrettySlogHandlerCanColorizeLevel(t *testing.T) {
	var out strings.Builder
	logger := slog.New(NewPrettyHandler(&out, slog.LevelDebug, true)).With("component", "agent_runtime")

	logger.Error("boom", "error", "failed")

	line := out.String()
	if !strings.Contains(line, ansiRed+"ERROR"+ansiReset) {
		t.Fatalf("error level was not colorized: %q", line)
	}
	if !strings.Contains(line, ansiCyan+"agent_runtime"+ansiReset) {
		t.Fatalf("component was not colorized: %q", line)
	}
}
