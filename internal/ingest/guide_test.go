package ingest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/sources"
)

func TestGenerateGuideHTTPSHTTPAndSafeMaterialization(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		host     string
		port     string
		tls      string
	}{
		{"HTTPS default", "https://logs.example.test/api/v1/ingest", "logs.example.test", "443", "On"},
		{"HTTPS port", "https://logs.example.test:8443/api/v1/ingest", "logs.example.test", "8443", "On"},
		{"private HTTP", "http://192.0.2.10:8081/api/v1/ingest", "192.0.2.10", "8081", "Off"},
		{"IPv6", "http://[2001:db8::10]:8081/api/v1/ingest", "2001:db8::10", "8081", "Off"},
	}
	eventAt := time.Date(2026, 7, 28, 10, 0, 0, 123456000, time.UTC)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guide, err := GenerateGuide(tt.endpoint, "siftail-setup-fixed", eventAt)
			if err != nil {
				t.Fatal(err)
			}
			for _, artifact := range []string{
				guide.CoolifyTemplate, guide.GenericTemplate,
			} {
				for _, want := range []string{
					"Host                     " + tt.host,
					"Port                     " + tt.port,
					"tls                      " + tt.tls,
					"Header                   Content-Type application/x-ndjson",
					"Retry_Limit              False",
					"storage.total_limit_size 256M",
					GuideTokenPlaceholder,
				} {
					if !strings.Contains(artifact, want) {
						t.Errorf("artifact missing %q", want)
					}
				}
			}
			excludeAt := strings.Index(guide.CoolifyTemplate,
				"Exclude COOLIFY_APP_NAME ^siftail-self$")
			renameAt := strings.Index(guide.CoolifyTemplate,
				"Rename COOLIFY_APP_NAME coolify.app_name")
			if excludeAt < 0 || renameAt < 0 || excludeAt >= renameAt {
				t.Fatal("Coolify self-exclusion is absent or ordered after rename")
			}
			if !strings.Contains(guide.CurlTemplate, tt.endpoint) ||
				!strings.Contains(guide.CurlTemplate, `"source_event_id":"siftail-setup-fixed"`) ||
				!strings.Contains(guide.CurlTemplate, eventAt.Format(time.RFC3339Nano)) {
				t.Fatalf("curl = %q", guide.CurlTemplate)
			}
			materialized, err := guide.Materialize(
				"sft_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			)
			if err != nil {
				t.Fatal(err)
			}
			for _, artifact := range []string{
				materialized.CoolifyTemplate,
				materialized.GenericTemplate,
				materialized.CurlTemplate,
			} {
				if strings.Contains(artifact, GuideTokenPlaceholder) ||
					strings.Count(artifact, "sft_A") != 1 {
					t.Fatalf("materialized artifact token placement = %q", artifact)
				}
			}
		})
	}
}

func TestGenerateGuideRejectsUnsafeEndpointEventAndToken(t *testing.T) {
	endpoints := []string{
		"",
		"https://user:secret@logs.test/api/v1/ingest",
		"https://logs.test/other",
		"https://logs.test/api/v1/ingest?token=secret",
		"https://logs.test/api/v1/ingest#fragment",
		"https://logs.test/\nHost injected/api/v1/ingest",
		"https://logs_test/api/v1/ingest",
		"ftp://logs.test/api/v1/ingest",
	}
	for _, endpoint := range endpoints {
		if _, err := GenerateGuide(endpoint, "safe-id", time.Now()); err == nil {
			t.Errorf("unsafe endpoint accepted: %q", endpoint)
		}
	}
	if _, err := GenerateGuide(
		"https://logs.test/api/v1/ingest", "unsafe id\n", time.Now(),
	); err == nil {
		t.Fatal("unsafe event ID accepted")
	}
	guide, err := GenerateGuide(
		"https://logs.test/api/v1/ingest", "safe-id", time.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guide.Materialize("sft_bad\nHeader injected"); err == nil {
		t.Fatal("unsafe token accepted")
	}
}

func TestGuideTesterUsesDirectBoundedRedirectRefusingClient(t *testing.T) {
	tester, err := NewGuideTester(
		"https://logs.test/api/v1/ingest", &sources.Store{},
	)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := tester.client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil ||
		tester.client.Timeout != 15*time.Second ||
		tester.client.CheckRedirect == nil {
		t.Fatalf("unsafe guided client = %#v", tester.client)
	}
	if err := tester.client.CheckRedirect(
		&http.Request{}, []*http.Request{{}},
	); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect result = %v", err)
	}
}

func TestGuideTesterClassifiesHTTPOutcomesWithoutLeakingResponse(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		marker  bool
		outcome TestOutcome
	}{
		{"committed", http.StatusNoContent, true, TestCommitted},
		{"missing commit marker", http.StatusNoContent, false, TestRejected},
		{"unauthorized", http.StatusUnauthorized, false, TestAuthRejected},
		{"forbidden", http.StatusForbidden, false, TestAuthRejected},
		{"rate limited", http.StatusTooManyRequests, false, TestRetryable},
		{"unavailable", http.StatusServiceUnavailable, false, TestRetryable},
		{"storage full", http.StatusInsufficientStorage, false, TestRetryable},
		{"bad request", http.StatusBadRequest, false, TestRejected},
		{"redirect", http.StatusFound, false, TestRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newIngestionIntegration(t, 10, 1<<20)
			var received *http.Request
			tester := &GuideTester{
				endpoint: "https://logs.test/api/v1/ingest",
				tokens:   fixture.handler.tokens,
				client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
					received = request
					headers := make(http.Header)
					if tt.marker {
						headers.Set("X-Siftail-Ingest-Outcome", "committed")
					}
					return &http.Response{
						StatusCode: tt.status,
						Body: io.NopCloser(strings.NewReader(
							"private-secret-response",
						)),
						Header: headers,
					}, nil
				})},
				now: func() time.Time {
					return time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
				},
			}
			result := tester.Test(context.Background(), 1, fixture.token)
			if result.Outcome != tt.outcome ||
				strings.Contains(result.Detail, "private-secret-response") {
				t.Fatalf("result = %#v", result)
			}
			if received == nil ||
				received.Header.Get("Authorization") != "Bearer "+fixture.token ||
				received.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("request = %#v", received)
			}
			body, err := io.ReadAll(received.Body)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), fixture.token) ||
				!strings.Contains(string(body), `"source_event_id":"siftail-setup-`) {
				t.Fatalf("guided body = %q", body)
			}
		})
	}

	t.Run("delivery failure", func(t *testing.T) {
		fixture := newIngestionIntegration(t, 10, 1<<20)
		tester := &GuideTester{
			endpoint: "https://logs.test/api/v1/ingest",
			tokens:   fixture.handler.tokens,
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("private transport detail")
			})},
			now: time.Now,
		}
		result := tester.Test(context.Background(), 1, fixture.token)
		if result.Outcome != TestDeliveryFailed ||
			strings.Contains(result.Detail, "private transport detail") {
			t.Fatalf("result = %#v", result)
		}
	})

	t.Run("wrong Server stops before delivery", func(t *testing.T) {
		fixture := newIngestionIntegration(t, 10, 1<<20)
		called := false
		tester := &GuideTester{
			endpoint: "https://logs.test/api/v1/ingest",
			tokens:   fixture.handler.tokens,
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				called = true
				return nil, errors.New("must not run")
			})},
			now: time.Now,
		}
		result := tester.Test(context.Background(), 999, fixture.token)
		if result.Outcome != TestAuthRejected || called {
			t.Fatalf("result/called = %#v/%t", result, called)
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
