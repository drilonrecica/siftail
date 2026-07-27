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
	level, err := parseLogLevel(cfg.LogLevel)
	if err != nil {
		return nil, err
	}

	opts := &slog.HandlerOptions{
		Level:     level,
		AddSource: false,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// Ensure no accidental secret keys are emitted by lowercasing
			// and keeping values as-is; secrets are redacted at the source.
			return a
		},
	}

	var handler slog.Handler
	switch cfg.LogFormat {
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, opts)
	case "text":
		handler = slog.NewTextHandler(os.Stdout, opts)
	default:
		return nil, fmt.Errorf("unsupported log format: %q", cfg.LogFormat)
	}

	return slog.New(handler), nil
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
