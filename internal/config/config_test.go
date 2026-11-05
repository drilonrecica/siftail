package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/ingest"
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

func TestParseGroupCommitDefaultsMatchWriter(t *testing.T) {
	clearEnv(t)
	cfg, err := Parse()
	if err != nil {
		t.Fatalf("Parse defaults: %v", err)
	}
	want := ingest.DefaultGroupLimits()
	if cfg.IngestGroupMaxBatches != int64(want.MaxBatches) ||
		cfg.IngestGroupMaxBytes != want.MaxBytes ||
		cfg.IngestGroupMaxWait != want.MaxWait {
		t.Fatalf("group defaults = %d/%d/%s, writer defaults = %+v",
			cfg.IngestGroupMaxBatches, cfg.IngestGroupMaxBytes, cfg.IngestGroupMaxWait, want)
	}
	if maxIngestGroupMaxBatches != ingest.MaxGroupMaxBatches || maxIngestGroupMaxWait != ingest.MaxGroupMaxWait {
		t.Fatal("configuration and writer group limits disagree")
	}
}

func TestParseGroupCommitOverrides(t *testing.T) {
	clearEnv(t)
	setEnv(t, "SIFTAIL_INGEST_GROUP_MAX_BATCHES", "8")
	setEnv(t, "SIFTAIL_INGEST_GROUP_MAX_BYTES", "1048576")
	setEnv(t, "SIFTAIL_INGEST_GROUP_MAX_WAIT", "5ms")
	cfg, err := Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.IngestGroupMaxBatches != 8 || cfg.IngestGroupMaxBytes != 1<<20 ||
		cfg.IngestGroupMaxWait != 5*time.Millisecond {
		t.Fatalf("group bounds = %d/%d/%s", cfg.IngestGroupMaxBatches, cfg.IngestGroupMaxBytes, cfg.IngestGroupMaxWait)
	}
	if err := (ingest.GroupLimits{
		MaxBatches: int(cfg.IngestGroupMaxBatches), MaxBytes: cfg.IngestGroupMaxBytes,
		MaxWait: cfg.IngestGroupMaxWait,
	}).Validate(); err != nil {
		t.Fatalf("writer rejects valid configuration: %v", err)
	}
}

func TestParseGroupCommitRejectsOutOfRangeBounds(t *testing.T) {
	for _, tt := range []struct {
		name, variable, value string
	}{
		{"zero batches", "SIFTAIL_INGEST_GROUP_MAX_BATCHES", "0"},
		{"too many batches", "SIFTAIL_INGEST_GROUP_MAX_BATCHES", "1025"},
		{"zero bytes", "SIFTAIL_INGEST_GROUP_MAX_BYTES", "0"},
		{"bytes above queue", "SIFTAIL_INGEST_GROUP_MAX_BYTES", "33554433"},
		{"negative wait", "SIFTAIL_INGEST_GROUP_MAX_WAIT", "-1ms"},
		{"wait above limit", "SIFTAIL_INGEST_GROUP_MAX_WAIT", "101ms"},
		{"malformed wait", "SIFTAIL_INGEST_GROUP_MAX_WAIT", "soon"},
		{"malformed batches", "SIFTAIL_INGEST_GROUP_MAX_BATCHES", "many"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			setEnv(t, tt.variable, tt.value)
			_, err := Parse()
			if err == nil || !strings.Contains(err.Error(), tt.variable) {
				t.Fatalf("Parse error = %v, want one naming %s", err, tt.variable)
			}
		})
	}
}

func TestServeRequiresPublicURLButOtherCommandsDoNot(t *testing.T) {
	clearEnv(t)
	cfg, err := Parse()
	if err != nil {
		t.Fatalf("commands without browser traffic must parse without SIFTAIL_PUBLIC_URL: %v", err)
	}
	err = cfg.ValidateServe()
	if err == nil || !strings.Contains(err.Error(), "SIFTAIL_PUBLIC_URL is required") {
		t.Fatalf("serve without public URL = %v", err)
	}

	setEnv(t, "SIFTAIL_PUBLIC_URL", "https://logs.example.com")
	cfg, err = Parse()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("serve with public URL = %v", err)
	}
	// ValidateServe still applies every Validate rule.
	cfg.UIAddr = cfg.IngestAddr
	if err := cfg.ValidateServe(); err == nil {
		t.Fatal("ValidateServe skipped Validate")
	}
}

func TestPublicURLMustBeExactOrigin(t *testing.T) {
	for _, valid := range []string{
		"https://logs.example.com",
		"https://logs.example.com/",
		"http://127.0.0.1:8080",
		"http://[::1]:8080",
		"https://logs.example.com:8443",
	} {
		t.Run("valid "+valid, func(t *testing.T) {
			clearEnv(t)
			setEnv(t, "SIFTAIL_PUBLIC_URL", valid)
			if _, err := Parse(); err != nil {
				t.Fatalf("Parse(%q) = %v", valid, err)
			}
		})
	}
	for _, invalid := range []string{
		"logs.example.com",
		"ftp://logs.example.com",
		"https://",
		"https://:8443",
		"https://logs.example.com/siftail",
		"https://logs.example.com/%2F",
		"https://logs.example.com?next=/",
		"https://logs.example.com/?",
		"https://logs.example.com#top",
		"https://logs.example.com:0",
		"https://logs.example.com:70000",
		"https://user@logs.example.com",
	} {
		t.Run("invalid "+invalid, func(t *testing.T) {
			clearEnv(t)
			setEnv(t, "SIFTAIL_PUBLIC_URL", invalid)
			_, err := Parse()
			if err == nil || !strings.Contains(err.Error(), "SIFTAIL_PUBLIC_URL") {
				t.Fatalf("Parse(%q) = %v", invalid, err)
			}
		})
	}
	clearEnv(t)
	setEnv(t, "SIFTAIL_PUBLIC_URL_FILE", "/run/secrets/public-url")
	if _, err := Parse(); err == nil || !strings.Contains(err.Error(), "SIFTAIL_PUBLIC_URL_FILE") {
		t.Fatalf("non-secret _FILE variant = %v", err)
	}
}

func TestPublicURLPlainHTTPOutsideLoopback(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://logs.example.com":  false,
		"http://127.0.0.1:8080":     false,
		"http://127.1.2.3:8080":     false,
		"http://[::1]:8080":         false,
		"http://localhost:8080":     false,
		"http://siftail.localhost":  false,
		"http://logs.example.com":   true,
		"http://192.168.1.10:8080":  true,
		"http://[2001:db8::1]:8080": true,
		"":                          false,
	} {
		if got := (Config{PublicURL: raw}).PublicURLPlainHTTPOutsideLoopback(); got != want {
			t.Errorf("%q warns = %v, want %v", raw, got, want)
		}
	}
}

func TestParseBackupDir(t *testing.T) {
	clearEnv(t)
	cfg, err := Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.BackupDir != "/data/backups" {
		t.Fatalf("default BackupDir = %q, want /data/backups", cfg.BackupDir)
	}

	clearEnv(t)
	setEnv(t, "SIFTAIL_DATA_DIR", "/srv/siftail")
	if cfg, err = Parse(); err != nil || cfg.BackupDir != "/srv/siftail/backups" {
		t.Fatalf("derived BackupDir = %q, %v", cfg.BackupDir, err)
	}

	clearEnv(t)
	setEnv(t, "SIFTAIL_BACKUP_DIR", "/backups")
	if cfg, err = Parse(); err != nil || cfg.BackupDir != "/backups" {
		t.Fatalf("explicit BackupDir = %q, %v", cfg.BackupDir, err)
	}

	for _, value := range []string{"", "   ", "/data", "/data/", "/backups\nx"} {
		clearEnv(t)
		setEnv(t, "SIFTAIL_BACKUP_DIR", value)
		if _, err := Parse(); err == nil ||
			!strings.Contains(err.Error(), "SIFTAIL_BACKUP_DIR") {
			t.Errorf("SIFTAIL_BACKUP_DIR=%q error = %v", value, err)
		}
	}
}
