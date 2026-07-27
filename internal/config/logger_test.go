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
	logger, err := configureLogger(Config{LogLevel: "info", LogFormat: "text"}, &buf)
	if err != nil {
		t.Fatalf("configureLogger: %v", err)
	}

	logger.Info("test", "token", Secret("super-secret-token"))
	out := buf.String()
	if strings.Contains(out, "super-secret-token") {
		t.Errorf("log output leaked secret: %s", out)
	}
	if !strings.Contains(out, "[redacted]") {
		t.Errorf("log output did not redact secret: %s", out)
	}
}

func TestLoggerRedactsSensitiveKeysWithoutWrapper(t *testing.T) {
	var buf bytes.Buffer
	logger, err := configureLogger(Config{LogLevel: "info", LogFormat: "json"}, &buf)
	if err != nil {
		t.Fatalf("configureLogger: %v", err)
	}

	sensitive := "value-that-must-not-appear"
	logger.Info("safe operation",
		"authorization", sensitive,
		"ingestion_token", sensitive,
		"password_hash", sensitive,
		"request_body", sensitive,
		"payload", sensitive,
		"request_id", "safe-request-id",
	)

	out := buf.String()
	if strings.Contains(out, sensitive) {
		t.Fatalf("log output leaked a sensitive value: %s", out)
	}
	if !strings.Contains(out, "safe-request-id") {
		t.Fatalf("log output removed a safe field: %s", out)
	}
}

func TestSensitiveLogKeyClassification(t *testing.T) {
	for _, key := range []string{
		"authorization", "proxy_secret", "password", "password_hash",
		"session_token", "request_body", "application_payload", "raw",
	} {
		if !isSensitiveLogKey(key) {
			t.Errorf("%q was not classified as sensitive", key)
		}
	}
	for _, key := range []string{"request_id", "duration", "operation", "status", "token_count"} {
		if isSensitiveLogKey(key) {
			t.Errorf("%q was incorrectly classified as sensitive", key)
		}
	}
}
