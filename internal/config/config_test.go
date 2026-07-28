package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDefaults(t *testing.T) {
	clearEnv(t)

	cfg, err := Parse()
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if cfg.DataDir != "/data" {
		t.Errorf("DataDir = %q, want /data", cfg.DataDir)
	}
	if cfg.UIAddr != ":8080" {
		t.Errorf("UIAddr = %q, want :8080", cfg.UIAddr)
	}
	if cfg.IngestAddr != ":8081" {
		t.Errorf("IngestAddr = %q, want :8081", cfg.IngestAddr)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", cfg.LogLevel)
	}
	if cfg.LogFormat != "text" {
		t.Errorf("LogFormat = %q, want text", cfg.LogFormat)
	}
	if cfg.MaxEventsPerRequest != 10000 {
		t.Errorf("MaxEventsPerRequest = %d, want 10000", cfg.MaxEventsPerRequest)
	}
	if cfg.DatabasePath != filepath.Join(cfg.DataDir, "siftail.db") {
		t.Errorf("DatabasePath = %q, want %q", cfg.DatabasePath, filepath.Join(cfg.DataDir, "siftail.db"))
	}
}

func TestParseCustomValues(t *testing.T) {
	clearEnv(t)
	setEnv(t, "SIFTAIL_DATA_DIR", "/tmp/siftail")
	setEnv(t, "SIFTAIL_UI_ADDR", "127.0.0.1:9000")
	setEnv(t, "SIFTAIL_LOG_LEVEL", "debug")
	setEnv(t, "SIFTAIL_LOG_FORMAT", "json")
	setEnv(t, "SIFTAIL_MAX_EVENTS_PER_REQUEST", "500")

	cfg, err := Parse()
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if cfg.DataDir != "/tmp/siftail" {
		t.Errorf("DataDir = %q", cfg.DataDir)
	}
	if cfg.UIAddr != "127.0.0.1:9000" {
		t.Errorf("UIAddr = %q", cfg.UIAddr)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q", cfg.LogLevel)
	}
	if cfg.LogFormat != "json" {
		t.Errorf("LogFormat = %q", cfg.LogFormat)
	}
	if cfg.MaxEventsPerRequest != 500 {
		t.Errorf("MaxEventsPerRequest = %d", cfg.MaxEventsPerRequest)
	}
}

func TestParseInvalidAddress(t *testing.T) {
	clearEnv(t)
	setEnv(t, "SIFTAIL_UI_ADDR", "not-an-address")

	_, err := Parse()
	if err == nil {
		t.Fatal("expected error for invalid address")
	}
	if !strings.Contains(err.Error(), "SIFTAIL_UI_ADDR") {
		t.Errorf("error does not mention SIFTAIL_UI_ADDR: %v", err)
	}
}

func TestParseSameAddress(t *testing.T) {
	clearEnv(t)
	setEnv(t, "SIFTAIL_UI_ADDR", ":8080")
	setEnv(t, "SIFTAIL_INGEST_ADDR", ":8080")

	_, err := Parse()
	if err == nil {
		t.Fatal("expected error for identical UI and ingest addresses")
	}
}

func TestParseInvalidURL(t *testing.T) {
	clearEnv(t)
	setEnv(t, "SIFTAIL_PUBLIC_URL", "ftp://logs.example.com")

	_, err := Parse()
	if err == nil {
		t.Fatal("expected error for invalid URL scheme")
	}
}

func TestParseURLRejectsCredentials(t *testing.T) {
	clearEnv(t)
	setEnv(t, "SIFTAIL_PUBLIC_URL", "https://administrator:secret@logs.example.com")

	_, err := Parse()
	if err == nil {
		t.Fatal("expected URL credentials to be rejected")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("validation error leaked URL password: %v", err)
	}
}

func TestParseRejectsIncompleteIngestionPublicURL(t *testing.T) {
	for _, value := range []string{
		"https://logs.example.com",
		"https://logs.example.com/other",
		"https://logs.example.com/api/v1/ingest?token=secret",
		"https://logs.example.com/api/v1/ingest#fragment",
	} {
		t.Run(value, func(t *testing.T) {
			clearEnv(t)
			setEnv(t, "SIFTAIL_INGEST_PUBLIC_URL", value)
			_, err := Parse()
			if err == nil ||
				!strings.Contains(err.Error(), "complete /api/v1/ingest URL") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestParseInvalidLogLevel(t *testing.T) {
	clearEnv(t)
	setEnv(t, "SIFTAIL_LOG_LEVEL", "verbose")

	_, err := Parse()
	if err == nil {
		t.Fatal("expected error for invalid log level")
	}
}

func TestParseInvalidLogFormat(t *testing.T) {
	clearEnv(t)
	setEnv(t, "SIFTAIL_LOG_FORMAT", "yaml")

	_, err := Parse()
	if err == nil {
		t.Fatal("expected error for invalid log format")
	}
}

func TestParseInvalidShutdownTimeout(t *testing.T) {
	clearEnv(t)
	setEnv(t, "SIFTAIL_SHUTDOWN_TIMEOUT", "eventually")

	_, err := Parse()
	if err == nil {
		t.Fatal("expected error for invalid shutdown timeout")
	}
}

func TestParseInvalidInteger(t *testing.T) {
	clearEnv(t)
	setEnv(t, "SIFTAIL_MAX_EVENTS_PER_REQUEST", "many")

	_, err := Parse()
	if err == nil {
		t.Fatal("expected error for invalid integer")
	}
}

func TestParseInvalidCIDR(t *testing.T) {
	clearEnv(t)
	setEnv(t, "SIFTAIL_TRUSTED_PROXY_CIDRS", "10.0.0.0/24, not-a-cidr")

	_, err := Parse()
	if err == nil {
		t.Fatal("expected error for invalid CIDR")
	}
}

func TestParseQueueTooSmall(t *testing.T) {
	clearEnv(t)
	setEnv(t, "SIFTAIL_MAX_EVENT_BYTES", "2097152")
	setEnv(t, "SIFTAIL_QUEUE_MAX_BYTES", "1048576")

	_, err := Parse()
	if err == nil {
		t.Fatal("expected error for queue bytes smaller than max event bytes")
	}
}

func TestValidateRejectsEveryNonpositiveLimit(t *testing.T) {
	clearEnv(t)
	base, err := Parse()
	if err != nil {
		t.Fatalf("Parse defaults: %v", err)
	}

	tests := []struct {
		name string
		set  func(*Config)
	}{
		{"compressed bytes", func(c *Config) { c.MaxCompressedRequestBytes = 0 }},
		{"decompressed bytes", func(c *Config) { c.MaxDecompressedRequestBytes = 0 }},
		{"events per request", func(c *Config) { c.MaxEventsPerRequest = 0 }},
		{"event bytes", func(c *Config) { c.MaxEventBytes = 0 }},
		{"queue events", func(c *Config) { c.QueueMaxEvents = 0 }},
		{"queue bytes", func(c *Config) { c.QueueMaxBytes = 0 }},
		{"resident events", func(c *Config) { c.IngestResidentMaxEvents = 0 }},
		{"resident bytes", func(c *Config) { c.IngestResidentMaxBytes = 0 }},
		{"decoders", func(c *Config) { c.IngestMaxDecoders = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			tt.set(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestValidateRejectsResidentLimitsBelowQueue(t *testing.T) {
	clearEnv(t)
	cfg, err := Parse()
	if err != nil {
		t.Fatalf("Parse defaults: %v", err)
	}

	cfg.IngestResidentMaxEvents = cfg.QueueMaxEvents - 1
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected resident event limit below queue to fail")
	}

	cfg, err = Parse()
	if err != nil {
		t.Fatalf("Parse defaults: %v", err)
	}
	cfg.IngestResidentMaxBytes = cfg.QueueMaxBytes - 1
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected resident byte limit below queue to fail")
	}
}

func TestParseUnknownVariable(t *testing.T) {
	clearEnv(t)
	setEnv(t, "SIFTAIL_UNKNOWN_SETTING", "value")

	_, err := Parse()
	if err == nil {
		t.Fatal("expected error for unknown variable")
	}
	if !strings.Contains(err.Error(), "SIFTAIL_UNKNOWN_SETTING") {
		t.Errorf("error does not mention unknown variable: %v", err)
	}
}

func TestParseNonSiftailEnvIgnored(t *testing.T) {
	clearEnv(t)
	setEnv(t, "PATH", "/usr/bin")
	setEnv(t, "SIFTAIL_LOG_LEVEL", "error")

	cfg, err := Parse()
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if cfg.LogLevel != "error" {
		t.Errorf("LogLevel = %q", cfg.LogLevel)
	}
}

func TestConfigureLogger(t *testing.T) {
	cfg := Config{LogLevel: "info", LogFormat: "text"}
	logger, err := ConfigureLogger(cfg)
	if err != nil {
		t.Fatalf("ConfigureLogger failed: %v", err)
	}
	if logger == nil {
		t.Fatal("logger is nil")
	}

	_, err = ConfigureLogger(Config{LogLevel: "info", LogFormat: "yaml"})
	if err == nil {
		t.Fatal("expected error for unsupported log format")
	}
}

func TestSecretRedacted(t *testing.T) {
	s := Secret("super-secret")
	if s.String() != "[redacted]" {
		t.Errorf("String() = %q", s.String())
	}
	if s.Raw() != "super-secret" {
		t.Errorf("Raw() = %q", s.Raw())
	}
}

func TestSanitizedCopy(t *testing.T) {
	clearEnv(t)
	cfg, err := Parse()
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	cfg.TrustedProxyCIDRs = []string{"10.0.0.0/8"}

	s := cfg.Sanitized()
	if len(s.TrustedProxyCIDRs) != 1 {
		t.Fatalf("len(s.TrustedProxyCIDRs) = %d", len(s.TrustedProxyCIDRs))
	}
	s.TrustedProxyCIDRs[0] = "changed"
	if cfg.TrustedProxyCIDRs[0] == "changed" {
		t.Fatal("Sanitized returned a shallow copy")
	}
}

func TestIsWritableDataDir(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{DataDir: dir}
	if err := cfg.IsWritableDataDir(); err != nil {
		t.Fatalf("IsWritableDataDir: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".siftail-write-check-*"))
	if err != nil {
		t.Fatalf("glob write checks: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("write check left temporary files: %v", matches)
	}
}

func TestIsWritableDataDirRejectsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data-file")
	if err := os.WriteFile(path, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("write data file: %v", err)
	}
	cfg := Config{DataDir: path}
	if err := cfg.IsWritableDataDir(); err == nil {
		t.Fatal("expected file data path to fail")
	}
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, e := range os.Environ() {
		name, _, found := strings.Cut(e, "=")
		if !found {
			continue
		}
		if strings.HasPrefix(name, "SIFTAIL_") {
			os.Unsetenv(name)
		}
	}
}

func setEnv(t *testing.T, key, value string) {
	t.Helper()
	if err := os.Setenv(key, value); err != nil {
		t.Fatalf("Setenv %s: %v", key, err)
	}
	t.Cleanup(func() { os.Unsetenv(key) })
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}
