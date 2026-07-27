package config

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// ConfigureLogger initializes process logging according to the configuration.
// It writes to stdout and never logs incoming payloads, bodies, or credentials.
func ConfigureLogger(cfg Config) (*slog.Logger, error) {
	return configureLogger(cfg, os.Stdout)
}

func configureLogger(cfg Config, output io.Writer) (*slog.Logger, error) {
	level, err := parseLogLevel(cfg.LogLevel)
	if err != nil {
		return nil, err
	}

	opts := &slog.HandlerOptions{
		Level:       level,
		AddSource:   false,
		ReplaceAttr: redactSensitiveAttr,
	}

	var handler slog.Handler
	switch cfg.LogFormat {
	case "json":
		handler = slog.NewJSONHandler(output, opts)
	case "text":
		handler = slog.NewTextHandler(output, opts)
	default:
		return nil, fmt.Errorf("unsupported log format: %q", cfg.LogFormat)
	}

	return slog.New(handler), nil
}

func redactSensitiveAttr(_ []string, attr slog.Attr) slog.Attr {
	if isSensitiveLogKey(attr.Key) {
		return slog.String(attr.Key, "[redacted]")
	}
	return attr
}

func isSensitiveLogKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.NewReplacer("-", "_", ".", "_").Replace(key)

	switch key {
	case "authorization", "cookie", "set_cookie",
		"password", "passwd", "secret",
		"token", "session", "session_id",
		"token_hash", "password_hash", "session_hash",
		"body", "request_body", "response_body",
		"payload", "message", "raw", "attributes",
		"environment", "env":
		return true
	}

	for _, fragment := range []string{
		"_authorization", "_password", "_passwd", "_secret",
		"_token", "_token_hash", "_password_hash", "_session_hash",
		"_body", "_payload", "_message", "_raw",
	} {
		if strings.HasSuffix(key, fragment) {
			return true
		}
	}
	return false
}

func parseLogLevel(level string) (slog.Level, error) {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unsupported log level: %q", level)
	}
}

// LogWriter returns the configured log output writer. It always returns stdout
// in this implementation so log output remains consolidated.
func LogWriter() io.Writer {
	return os.Stdout
}
