package config

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config holds all process-level configuration for Siftail.
type Config struct {
	DataDir           string
	DatabasePath      string
	UIAddr            string
	IngestAddr        string
	PublicURL         string
	IngestPublicURL   string
	LogLevel          string
	LogFormat         string
	ShutdownTimeout   time.Duration
	TrustedProxyCIDRs []string

	// Ingestion limits.
	MaxCompressedRequestBytes   int64
	MaxDecompressedRequestBytes int64
	MaxEventsPerRequest         int64
	MaxEventBytes               int64
	QueueMaxEvents              int64
	QueueMaxBytes               int64
	IngestResidentMaxEvents     int64
	IngestResidentMaxBytes      int64
	IngestMaxDecoders           int64

	// Commit-group bounds (ADR 0009).
	IngestGroupMaxBatches int64
	IngestGroupMaxBytes   int64
	IngestGroupMaxWait    time.Duration
}

// Defaults aligned with ARCHITECTURE.md §9 and §11.3.
const (
	defaultDataDir                     = "/data"
	defaultUIAddr                      = ":8080"
	defaultIngestAddr                  = ":8081"
	defaultLogLevel                    = "info"
	defaultLogFormat                   = "text"
	defaultShutdownTimeout             = 30 * time.Second
	defaultMaxCompressedRequestBytes   = 5 * 1024 * 1024  // 5 MiB
	defaultMaxDecompressedRequestBytes = 25 * 1024 * 1024 // 25 MiB
	defaultMaxEventsPerRequest         = 10000
	defaultMaxEventBytes               = 1 * 1024 * 1024 // 1 MiB
	defaultQueueMaxEvents              = 20000
	defaultQueueMaxBytes               = 32 * 1024 * 1024 // 32 MiB
	defaultIngestResidentMaxEvents     = 30000
	defaultIngestResidentMaxBytes      = 64 * 1024 * 1024 // 64 MiB
	defaultIngestMaxDecoders           = 4
	defaultIngestGroupMaxBatches       = 64
	defaultIngestGroupMaxBytes         = 8 * 1024 * 1024 // 8 MiB
	defaultIngestGroupMaxWait          = time.Duration(0)

	maxIngestGroupMaxBatches = 1024
	maxIngestGroupMaxWait    = 100 * time.Millisecond
)

// Parse reads the process configuration from the environment.
func Parse() (Config, error) {
	known := make(map[string]struct{})

	read := func(name, fileName, fallback string) (string, error) {
		known[name] = struct{}{}
		if fileName != "" {
			known[fileName] = struct{}{}
		}
		return readString(name, fileName, fallback)
	}

	var c Config
	var err error

	c.DataDir, err = read("SIFTAIL_DATA_DIR", "", defaultDataDir)
	if err != nil {
		return c, err
	}
	c.DatabasePath = filepath.Join(c.DataDir, "siftail.db")

	c.UIAddr, err = read("SIFTAIL_UI_ADDR", "", defaultUIAddr)
	if err != nil {
		return c, err
	}
	c.IngestAddr, err = read("SIFTAIL_INGEST_ADDR", "", defaultIngestAddr)
	if err != nil {
		return c, err
	}
	c.PublicURL, err = read("SIFTAIL_PUBLIC_URL", "", "")
	if err != nil {
		return c, err
	}
	c.IngestPublicURL, err = read("SIFTAIL_INGEST_PUBLIC_URL", "", "")
	if err != nil {
		return c, err
	}
	c.LogLevel, err = read("SIFTAIL_LOG_LEVEL", "", defaultLogLevel)
	if err != nil {
		return c, err
	}
	c.LogFormat, err = read("SIFTAIL_LOG_FORMAT", "", defaultLogFormat)
	if err != nil {
		return c, err
	}

	shutdownStr, err := read("SIFTAIL_SHUTDOWN_TIMEOUT", "", defaultShutdownTimeout.String())
	if err != nil {
		return c, err
	}
	c.ShutdownTimeout, err = time.ParseDuration(shutdownStr)
	if err != nil {
		return c, fmt.Errorf("SIFTAIL_SHUTDOWN_TIMEOUT: invalid duration: %w", err)
	}

	cidrsStr, err := read("SIFTAIL_TRUSTED_PROXY_CIDRS", "", "")
	if err != nil {
		return c, err
	}
	c.TrustedProxyCIDRs = splitAndFilterEmpty(cidrsStr, ",")

	c.MaxCompressedRequestBytes, err = readInt64(read, "SIFTAIL_MAX_COMPRESSED_REQUEST_BYTES", defaultMaxCompressedRequestBytes)
	if err != nil {
		return c, err
	}
	c.MaxDecompressedRequestBytes, err = readInt64(read, "SIFTAIL_MAX_DECOMPRESSED_REQUEST_BYTES", defaultMaxDecompressedRequestBytes)
	if err != nil {
		return c, err
	}
	c.MaxEventsPerRequest, err = readInt64(read, "SIFTAIL_MAX_EVENTS_PER_REQUEST", defaultMaxEventsPerRequest)
	if err != nil {
		return c, err
	}
	c.MaxEventBytes, err = readInt64(read, "SIFTAIL_MAX_EVENT_BYTES", defaultMaxEventBytes)
	if err != nil {
		return c, err
	}
	c.QueueMaxEvents, err = readInt64(read, "SIFTAIL_QUEUE_MAX_EVENTS", defaultQueueMaxEvents)
	if err != nil {
		return c, err
	}
	c.QueueMaxBytes, err = readInt64(read, "SIFTAIL_QUEUE_MAX_BYTES", defaultQueueMaxBytes)
	if err != nil {
		return c, err
	}
	c.IngestResidentMaxEvents, err = readInt64(read, "SIFTAIL_INGEST_RESIDENT_MAX_EVENTS", defaultIngestResidentMaxEvents)
	if err != nil {
		return c, err
	}
	c.IngestResidentMaxBytes, err = readInt64(read, "SIFTAIL_INGEST_RESIDENT_MAX_BYTES", defaultIngestResidentMaxBytes)
	if err != nil {
		return c, err
	}
	c.IngestMaxDecoders, err = readInt64(read, "SIFTAIL_INGEST_MAX_DECODERS", defaultIngestMaxDecoders)
	if err != nil {
		return c, err
	}

	c.IngestGroupMaxBatches, err = readInt64(read, "SIFTAIL_INGEST_GROUP_MAX_BATCHES", defaultIngestGroupMaxBatches)
	if err != nil {
		return c, err
	}
	c.IngestGroupMaxBytes, err = readInt64(read, "SIFTAIL_INGEST_GROUP_MAX_BYTES", defaultIngestGroupMaxBytes)
	if err != nil {
		return c, err
	}
	groupWaitStr, err := read("SIFTAIL_INGEST_GROUP_MAX_WAIT", "", defaultIngestGroupMaxWait.String())
	if err != nil {
		return c, err
	}
	c.IngestGroupMaxWait, err = time.ParseDuration(groupWaitStr)
	if err != nil {
		return c, fmt.Errorf("SIFTAIL_INGEST_GROUP_MAX_WAIT: invalid duration: %w", err)
	}

	if unknown := listUnknownVars(known); len(unknown) > 0 {
		return c, fmt.Errorf("unknown environment variable(s): %s", strings.Join(unknown, ", "))
	}

	return c, c.Validate()
}

// Validate checks that the parsed configuration is internally consistent.
func (c Config) Validate() error {
	if c.DataDir == "" {
		return fmt.Errorf("SIFTAIL_DATA_DIR must not be empty")
	}
	if c.DatabasePath == "" {
		return fmt.Errorf("database path must not be empty")
	}

	if err := validateAddr(c.UIAddr, "SIFTAIL_UI_ADDR"); err != nil {
		return err
	}
	if err := validateAddr(c.IngestAddr, "SIFTAIL_INGEST_ADDR"); err != nil {
		return err
	}
	if c.UIAddr == c.IngestAddr {
		return fmt.Errorf("SIFTAIL_UI_ADDR and SIFTAIL_INGEST_ADDR must not be identical")
	}

	if c.PublicURL != "" {
		if err := validatePublicOrigin(c.PublicURL); err != nil {
			return err
		}
	}
	if c.IngestPublicURL != "" {
		if err := validateURL(c.IngestPublicURL, "SIFTAIL_INGEST_PUBLIC_URL"); err != nil {
			return err
		}
		u, _ := url.Parse(c.IngestPublicURL)
		if u.Path != "/api/v1/ingest" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("SIFTAIL_INGEST_PUBLIC_URL must be the complete /api/v1/ingest URL without query or fragment")
		}
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("SIFTAIL_LOG_LEVEL must be one of debug, info, warn, error")
	}
	switch c.LogFormat {
	case "text", "json":
	default:
		return fmt.Errorf("SIFTAIL_LOG_FORMAT must be one of text, json")
	}

	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("SIFTAIL_SHUTDOWN_TIMEOUT must be positive")
	}

	for _, cidr := range c.TrustedProxyCIDRs {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			return fmt.Errorf("SIFTAIL_TRUSTED_PROXY_CIDRS: invalid CIDR %q", cidr)
		}
	}

	if err := validatePositive(c.MaxCompressedRequestBytes, "SIFTAIL_MAX_COMPRESSED_REQUEST_BYTES"); err != nil {
		return err
	}
	if err := validatePositive(c.MaxDecompressedRequestBytes, "SIFTAIL_MAX_DECOMPRESSED_REQUEST_BYTES"); err != nil {
		return err
	}
	if err := validatePositive(c.MaxEventsPerRequest, "SIFTAIL_MAX_EVENTS_PER_REQUEST"); err != nil {
		return err
	}
	if err := validatePositive(c.MaxEventBytes, "SIFTAIL_MAX_EVENT_BYTES"); err != nil {
		return err
	}
	if err := validatePositive(c.QueueMaxEvents, "SIFTAIL_QUEUE_MAX_EVENTS"); err != nil {
		return err
	}
	if err := validatePositive(c.QueueMaxBytes, "SIFTAIL_QUEUE_MAX_BYTES"); err != nil {
		return err
	}
	if err := validatePositive(c.IngestResidentMaxEvents, "SIFTAIL_INGEST_RESIDENT_MAX_EVENTS"); err != nil {
		return err
	}
	if err := validatePositive(c.IngestResidentMaxBytes, "SIFTAIL_INGEST_RESIDENT_MAX_BYTES"); err != nil {
		return err
	}
	if err := validatePositive(c.IngestMaxDecoders, "SIFTAIL_INGEST_MAX_DECODERS"); err != nil {
		return err
	}

	if c.IngestGroupMaxBatches < 1 || c.IngestGroupMaxBatches > maxIngestGroupMaxBatches {
		return fmt.Errorf("SIFTAIL_INGEST_GROUP_MAX_BATCHES must be between 1 and %d", maxIngestGroupMaxBatches)
	}
	if c.IngestGroupMaxBytes < 1 || c.IngestGroupMaxBytes > c.QueueMaxBytes {
		return fmt.Errorf("SIFTAIL_INGEST_GROUP_MAX_BYTES must be between 1 and SIFTAIL_QUEUE_MAX_BYTES")
	}
	if c.IngestGroupMaxWait < 0 || c.IngestGroupMaxWait > maxIngestGroupMaxWait {
		return fmt.Errorf("SIFTAIL_INGEST_GROUP_MAX_WAIT must be between 0s and %s", maxIngestGroupMaxWait)
	}

	if c.QueueMaxBytes < c.MaxEventBytes {
		return fmt.Errorf("SIFTAIL_QUEUE_MAX_BYTES must be at least SIFTAIL_MAX_EVENT_BYTES")
	}
	if c.IngestResidentMaxBytes < c.QueueMaxBytes {
		return fmt.Errorf("SIFTAIL_INGEST_RESIDENT_MAX_BYTES must be at least SIFTAIL_QUEUE_MAX_BYTES")
	}
	if c.IngestResidentMaxEvents < c.QueueMaxEvents {
		return fmt.Errorf("SIFTAIL_INGEST_RESIDENT_MAX_EVENTS must be at least SIFTAIL_QUEUE_MAX_EVENTS")
	}

	return nil
}

// ValidateServe checks the configuration that `siftail serve` (and therefore
// `siftail config validate`) needs on top of Validate. The browser origin, the
// session cookie's security, and HSTS derive only from SIFTAIL_PUBLIC_URL
// (ADR 0010), so serving without it is refused. Commands that serve no
// browser traffic call Validate alone.
func (c Config) ValidateServe() error {
	if c.PublicURL == "" {
		return fmt.Errorf("SIFTAIL_PUBLIC_URL is required to serve the browser interface: " +
			"set it to the exact browser origin, for example https://logs.example.com")
	}
	if err := c.Validate(); err != nil {
		return err
	}
	return nil
}

// PublicURLPlainHTTPOutsideLoopback reports whether the browser origin uses
// plain http on a host that is neither a loopback address nor localhost. That
// mode sends the session cookie without Secure across a network, so serve logs
// one warning for it (ADR 0010).
func (c Config) PublicURLPlainHTTPOutsideLoopback() bool {
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	address, err := netip.ParseAddr(host)
	return err != nil || !address.IsLoopback()
}

// Sanitized returns a config safe for logging and diagnostics.
// It redacts any secret fields and omits full environment contents.
func (c Config) Sanitized() Config {
	// Currently no secret fields are present in process configuration.
	// Clone the struct so callers cannot mutate the original.
	s := c
	s.TrustedProxyCIDRs = make([]string, len(c.TrustedProxyCIDRs))
	copy(s.TrustedProxyCIDRs, c.TrustedProxyCIDRs)
	return s
}

// IsWritableDataDir checks whether the configured data directory exists and is
// writable. It is separate from Validate so that config validation can run
// without touching the filesystem.
func (c Config) IsWritableDataDir() error {
	info, err := os.Stat(c.DataDir)
	if err != nil {
		return fmt.Errorf("data directory %q: %w", c.DataDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("data directory %q is not a directory", c.DataDir)
	}
	probe, err := os.CreateTemp(c.DataDir, ".siftail-write-check-*")
	if err != nil {
		return fmt.Errorf("data directory %q is not writable: %w", c.DataDir, err)
	}
	probePath := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(probePath)
		return fmt.Errorf("closing data directory write check: %w", err)
	}
	if err := os.Remove(probePath); err != nil {
		return fmt.Errorf("removing data directory write check: %w", err)
	}
	return nil
}

func validateAddr(addr, name string) error {
	if addr == "" {
		return fmt.Errorf("%s must not be empty", name)
	}
	if _, err := net.ResolveTCPAddr("tcp", addr); err != nil {
		return fmt.Errorf("%s: invalid address %q", name, addr)
	}
	return nil
}

func validateURL(raw, name string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: invalid URL", name)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s: URL must use http or https scheme", name)
	}
	if u.Host == "" {
		return fmt.Errorf("%s: URL must include a host", name)
	}
	if u.User != nil {
		return fmt.Errorf("%s: URL must not contain credentials", name)
	}
	return nil
}

// validatePublicOrigin accepts only an exact browser origin: http or https, a
// host, an optional port, and an empty or "/" path, with no credentials,
// query, or fragment. Sub-path deployment is not supported (ADR 0010).
func validatePublicOrigin(raw string) error {
	const name = "SIFTAIL_PUBLIC_URL"
	if err := validateURL(raw, name); err != nil {
		return err
	}
	u, _ := url.Parse(raw)
	if u.Path != "" && u.Path != "/" || u.RawPath != "" {
		return fmt.Errorf("%s must be an origin without a path; sub-path deployment is not supported", name)
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("%s must not contain a query or fragment", name)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("%s: URL must include a host", name)
	}
	if port := u.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return fmt.Errorf("%s: invalid port", name)
		}
	}
	return nil
}

func validatePositive(v int64, name string) error {
	if v <= 0 {
		return fmt.Errorf("%s must be positive", name)
	}
	return nil
}

func readInt64(read func(string, string, string) (string, error), name string, fallback int64) (int64, error) {
	s, err := read(name, "", strconv.FormatInt(fallback, 10))
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid integer: %w", name, err)
	}
	return v, nil
}

func splitAndFilterEmpty(s, sep string) []string {
	parts := strings.Split(s, sep)
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
