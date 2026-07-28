package ingest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/drilonrecica/siftail/internal/sources"
)

const (
	GuideTokenPlaceholder = "__SIFTAIL_INGEST_TOKEN__"
	GuideSourcePreview    = "siftail-test / setup / guided-ingestion / probe"
)

var safeGuideToken = regexp.MustCompile(`^sft_[A-Za-z0-9_-]{43}$`)

type Guide struct {
	Endpoint        string
	CoolifyTemplate string
	GenericTemplate string
	CurlTemplate    string
	EventID         string
	EventAt         time.Time
}

type TestOutcome string

const (
	TestCommitted      TestOutcome = "committed"
	TestDeliveryFailed TestOutcome = "delivery-failed"
	TestAuthRejected   TestOutcome = "authentication-rejected"
	TestRetryable      TestOutcome = "retryable-failure"
	TestRejected       TestOutcome = "request-rejected"
	TestUnavailable    TestOutcome = "unavailable"
)

type TestResult struct {
	Outcome       TestOutcome `json:"outcome"`
	Title         string      `json:"title"`
	Detail        string      `json:"detail"`
	SourcePreview string      `json:"source_preview,omitempty"`
	HTTPStatus    int         `json:"http_status,omitempty"`
}

type GuideTester struct {
	endpoint string
	tokens   *sources.Store
	client   *http.Client
	now      func() time.Time
}

func GenerateGuide(endpoint, eventID string, eventAt time.Time) (Guide, error) {
	target, err := parseGuideEndpoint(endpoint)
	if err != nil {
		return Guide{}, err
	}
	if !safeEventID(eventID) {
		return Guide{}, errors.New("invalid guided-test event ID")
	}
	eventAt = eventAt.UTC()
	if eventAt.IsZero() || eventAt.Year() < 1 || eventAt.Year() > 9999 {
		return Guide{}, errors.New("invalid guided-test timestamp")
	}

	host := target.Hostname()
	port := target.Port()
	if port == "" {
		if target.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	tls := "Off"
	if target.Scheme == "https" {
		tls = "On"
	}
	output := fmt.Sprintf(`[OUTPUT]
    Name                     http
    Match                    *
    Host                     %s
    Port                     %s
    URI                      /api/v1/ingest
    Format                   json_lines
    json_date_key            date
    json_date_format         iso8601
    Compress                 gzip
    Header                   Content-Type application/x-ndjson
    Header                   Authorization Bearer %s
    tls                      %s
    Retry_Limit              False
    storage.total_limit_size 256M
`, host, port, GuideTokenPlaceholder, tls)

	coolify := `[SERVICE]
    Flush                     2
    Daemon                    Off
    Log_Level                 info
    storage.path              /tmp/siftail-fluent-bit
    storage.sync              normal
    storage.checksum          off
    storage.max_chunks_up     16
    storage.backlog.mem_limit 16M

[INPUT]
    Name              forward
    Listen            0.0.0.0
    Port              24224
    Buffer_Chunk_Size 1M
    Buffer_Max_Size   6M
    storage.type      filesystem

[FILTER]
    Name    grep
    Match   *
    Exclude COOLIFY_APP_NAME ^siftail-self$

[FILTER]
    Name   modify
    Match  *
    Rename COOLIFY_APP_NAME coolify.app_name
    Rename COOLIFY_PROJECT_NAME coolify.project_name
    Rename COOLIFY_SERVER_IP coolify.server_ip
    Rename COOLIFY_ENVIRONMENT_NAME coolify.environment_name

` + output

	generic := `[SERVICE]
    Flush                     2
    Daemon                    Off
    Log_Level                 info
    storage.path              /var/lib/fluent-bit/siftail
    storage.sync              normal
    storage.checksum          off
    storage.max_chunks_up     16
    storage.backlog.mem_limit 16M

[INPUT]
    Name         forward
    Listen       0.0.0.0
    Port         24224
    storage.type filesystem

` + output

	body, err := json.Marshal(map[string]any{
		"timestamp":       eventAt.Format(time.RFC3339Nano),
		"project":         "siftail-test",
		"environment":     "setup",
		"application":     "guided-ingestion",
		"service":         "probe",
		"stream":          "stdout",
		"level":           "info",
		"log":             "Siftail guided ingestion test",
		"source_event_id": eventID,
	})
	if err != nil {
		return Guide{}, errors.New("encode guided-test event")
	}
	curl := fmt.Sprintf(`curl --fail-with-body --silent --show-error \
  --request POST \
  --header 'Authorization: Bearer %s' \
  --header 'Content-Type: application/json' \
  --data-binary '%s' \
  '%s'`, GuideTokenPlaceholder, body, target.String())

	return Guide{
		Endpoint: target.String(), CoolifyTemplate: coolify,
		GenericTemplate: generic, CurlTemplate: curl,
		EventID: eventID, EventAt: eventAt,
	}, nil
}

func (g Guide) Materialize(token string) (Guide, error) {
	if !safeGuideToken.MatchString(token) {
		return Guide{}, errors.New("invalid ingestion token")
	}
	replace := func(value string) string {
		return strings.ReplaceAll(value, GuideTokenPlaceholder, token)
	}
	g.CoolifyTemplate = replace(g.CoolifyTemplate)
	g.GenericTemplate = replace(g.GenericTemplate)
	g.CurlTemplate = replace(g.CurlTemplate)
	return g, nil
}

func NewGuideEventID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", errors.New("generate guided-test identity")
	}
	return "siftail-setup-" + base64.RawURLEncoding.EncodeToString(random), nil
}

func NewGuideTester(endpoint string, tokens *sources.Store) (*GuideTester, error) {
	if endpoint == "" {
		return nil, nil
	}
	target, err := parseGuideEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = (&net.Dialer{
		Timeout: 5 * time.Second, KeepAlive: 30 * time.Second,
	}).DialContext
	transport.TLSHandshakeTimeout = 5 * time.Second
	transport.ResponseHeaderTimeout = 10 * time.Second
	client := &http.Client{
		Transport: transport, Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &GuideTester{
		endpoint: target.String(), tokens: tokens, client: client, now: time.Now,
	}, nil
}

func (t *GuideTester) WithClient(client *http.Client) *GuideTester {
	if client != nil {
		t.client = client
	}
	return t
}

func (t *GuideTester) Test(ctx context.Context, serverID int64, token string) TestResult {
	if t == nil || t.tokens == nil || t.client == nil || t.endpoint == "" {
		return TestResult{
			Outcome: TestUnavailable, Title: "Guided test unavailable",
			Detail: "Configure SIFTAIL_INGEST_PUBLIC_URL and restart Siftail.",
		}
	}
	authenticated, err := t.tokens.VerifyToken(ctx, token)
	if err != nil && !errors.Is(err, sources.ErrInvalidToken) {
		return TestResult{
			Outcome: TestRetryable, Title: "Token verification is temporarily unavailable",
			Detail: "The event was not sent and no commit was attempted. Check Siftail status and try again.",
		}
	}
	if err != nil || authenticated.ID != serverID {
		return TestResult{
			Outcome: TestAuthRejected, Title: "Token authentication failed",
			Detail: "Use the token created for this Server. Revoked or unrelated tokens are rejected.",
		}
	}
	eventID, err := NewGuideEventID()
	if err != nil {
		return unavailableTestResult()
	}
	body, err := json.Marshal(map[string]any{
		"timestamp":       t.now().UTC().Format(time.RFC3339Nano),
		"project":         "siftail-test",
		"environment":     "setup",
		"application":     "guided-ingestion",
		"service":         "probe",
		"stream":          "stdout",
		"level":           "info",
		"log":             "Siftail guided ingestion test",
		"source_event_id": eventID,
	})
	if err != nil {
		return unavailableTestResult()
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, t.endpoint, bytes.NewReader(body),
	)
	if err != nil {
		return unavailableTestResult()
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "siftail-guided-test")
	response, err := t.client.Do(request)
	if err != nil {
		return TestResult{
			Outcome: TestDeliveryFailed, Title: "Ingestion endpoint was not reached",
			Detail: "Check the configured public ingestion URL, DNS, TLS, proxy route, and listener reachability.",
		}
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))

	switch response.StatusCode {
	case http.StatusNoContent:
		if response.Header.Get("X-Siftail-Ingest-Outcome") != "committed" {
			return TestResult{
				Outcome: TestRejected, Title: "Commit confirmation was missing",
				Detail:     "The endpoint returned 204 without Siftail's commit marker. Check proxy routing and response-header preservation.",
				HTTPStatus: response.StatusCode,
			}
		}
		return TestResult{
			Outcome: TestCommitted, Title: "Test event committed",
			Detail:        "The configured endpoint received, authenticated, normalized, and durably committed the event.",
			SourcePreview: GuideSourcePreview, HTTPStatus: response.StatusCode,
		}
	case http.StatusUnauthorized, http.StatusForbidden:
		return TestResult{
			Outcome: TestAuthRejected, Title: "Ingestion token was rejected",
			Detail:     "The endpoint was reached, but authentication did not succeed.",
			HTTPStatus: response.StatusCode,
		}
	case http.StatusTooManyRequests, http.StatusServiceUnavailable,
		http.StatusInsufficientStorage:
		return TestResult{
			Outcome: TestRetryable, Title: "Ingestion is temporarily unavailable",
			Detail:     "The event was not confirmed committed. Retry after resolving capacity, database, or storage pressure.",
			HTTPStatus: response.StatusCode,
		}
	default:
		return TestResult{
			Outcome: TestRejected, Title: "Test event was rejected",
			Detail:     "The endpoint responded, but no durable commit was confirmed. Check the URL, proxy, request limits, and Siftail status.",
			HTTPStatus: response.StatusCode,
		}
	}
}

func parseGuideEndpoint(raw string) (*url.URL, error) {
	if raw == "" || strings.TrimSpace(raw) != raw ||
		strings.ContainsAny(raw, "\x00\r\n\t") {
		return nil, errors.New("ingestion public URL is required")
	}
	target, err := url.Parse(raw)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") ||
		target.Host == "" || target.User != nil || target.RawQuery != "" ||
		target.Fragment != "" || target.Path != "/api/v1/ingest" {
		return nil, errors.New("ingestion public URL must be an absolute HTTP(S) /api/v1/ingest URL")
	}
	host := target.Hostname()
	if host == "" || strings.ContainsAny(host, " \r\n\t") {
		return nil, errors.New("ingestion public URL host is invalid")
	}
	if net.ParseIP(host) == nil {
		for _, char := range host {
			if !((char >= 'a' && char <= 'z') ||
				(char >= 'A' && char <= 'Z') ||
				(char >= '0' && char <= '9') ||
				char == '.' || char == '-') {
				return nil, errors.New("ingestion public URL host must be an IP address or ASCII DNS name")
			}
		}
	}
	if port := target.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return nil, errors.New("ingestion public URL port is invalid")
		}
	}
	target.RawPath = ""
	return target, nil
}

func safeEventID(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '-' || char == '_') {
			return false
		}
	}
	return true
}

func unavailableTestResult() TestResult {
	return TestResult{
		Outcome: TestUnavailable, Title: "Guided test unavailable",
		Detail: "Siftail could not prepare the bounded test event. Try again.",
	}
}
