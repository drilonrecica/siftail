package config

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestConfigureLoggerLevels(t *testing.T) {
	tests := []struct {
		level string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
	}
	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			got, err := parseLogLevel(tt.level)
			if err != nil {
				t.Fatalf("parseLogLevel failed: %v", err)
			}
			if got != tt.want {
				t.Errorf("level = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoggerDoesNotLeakSecret(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(handler)

	logger.Info("test", "token", Secret("super-secret-token"))
	out := buf.String()
	if strings.Contains(out, "super-secret-token") {
		t.Errorf("log output leaked secret: %s", out)
	}
	if !strings.Contains(out, "[redacted]") {
		t.Errorf("log output did not redact secret: %s", out)
	}
}
