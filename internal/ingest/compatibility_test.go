package ingest

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const selfMarker = "siftail-self"

func TestPinnedFluentBitConfigurations(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"Coolify 4.1.1 bundled Fluent Bit 2.0", "../../docs/integrations/fixtures/coolify-v4.1.1-fluent-bit-2.0.conf"},
		{"generic Fluent Bit 5.0.9", "../../docs/integrations/fixtures/fluent-bit-v5.0.9.conf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := os.ReadFile(tt.path)
			if err != nil {
				t.Fatal(err)
			}
			config := string(raw)
			requireConfigDirectives(t, config,
				"[SERVICE]",
				"storage.path",
				"storage.max_chunks_up     16",
				"storage.backlog.mem_limit 16M",
				"[INPUT]",
				"Name",
				"forward",
				"storage.type",
				"filesystem",
				"[OUTPUT]",
				"http",
				"URI",
				"/api/v1/ingest",
				"Format",
				"json_lines",
				"Compress",
				"gzip",
				"Header",
				"Content-Type application/x-ndjson",
				"Header",
				"Authorization Bearer ${SIFTAIL_INGEST_TOKEN}",
				"tls",
				"On",
				"Retry_Limit",
				"False",
				"storage.total_limit_size 256M",
			)
			if strings.Contains(config, "token (shown once)") ||
				strings.Contains(config, "sft_") {
				t.Fatal("fixture contains token-like material")
			}
		})
	}
}

func TestCoolifyConfigurationExcludesSiftailBeforeMetadataRename(t *testing.T) {
	raw, err := os.ReadFile("../../docs/integrations/fixtures/coolify-v4.1.1-fluent-bit-2.0.conf")
	if err != nil {
		t.Fatal(err)
	}
	config := string(raw)
	exclusion := "Exclude COOLIFY_APP_NAME ^" + selfMarker + "$"
	rename := "Rename COOLIFY_APP_NAME coolify.app_name"
	exclusionAt := strings.Index(config, exclusion)
	renameAt := strings.Index(config, rename)
	if exclusionAt < 0 || renameAt < 0 || exclusionAt >= renameAt {
		t.Fatalf("self exclusion must precede metadata rename: exclude=%d rename=%d", exclusionAt, renameAt)
	}

	pattern := regexp.MustCompile("^" + regexp.QuoteMeta(selfMarker) + "$")
	for _, record := range []struct {
		app     string
		exclude bool
	}{
		{selfMarker, true},
		{"siftail-self-worker", false},
		{"customer-siftail-self", false},
		{"checkout", false},
		{"", false},
	} {
		if got := pattern.MatchString(record.app); got != record.exclude {
			t.Fatalf("marker %q excluded=%t, want %t", record.app, got, record.exclude)
		}
	}
}

func TestPinnedCompatibilityFixturesThroughProductionPath(t *testing.T) {
	t.Run("Coolify hierarchy, gzip, and trusted Server", func(t *testing.T) {
		fixture := newIngestionIntegration(t, 10, 1<<20)
		body := readCompatibilityFixture(t, "testdata/coolify-v4.1.1-json-lines.ndjson")
		response := compatibilityRequest(
			t, fixture, "application/x-ndjson", "gzip", gzipCompatibilityBody(t, body),
		)
		if response.Code != http.StatusNoContent {
			t.Fatalf("status = %d, body=%q", response.Code, response.Body.String())
		}

		rows, err := fixture.db.Reader().Query(`
			SELECT server_id,project_key,environment_key,application_key,service_key
			FROM sources ORDER BY service_key`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var got []string
		for rows.Next() {
			var serverID int64
			var project, environment, application, service string
			if err := rows.Scan(&serverID, &project, &environment, &application, &service); err != nil {
				t.Fatal(err)
			}
			if serverID != 1 {
				t.Fatalf("payload metadata replaced trusted Server: %d", serverID)
			}
			got = append(got, strings.Join([]string{project, environment, application, service}, "/"))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		want := []string{
			"storefront/production/web/web",
			"storefront/production/worker/worker",
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("sources = %q, want %q", got, want)
		}
		assertEventCount(t, fixture.db.Reader(), 2)
	})

	t.Run("generic JSON and idempotent source IDs", func(t *testing.T) {
		fixture := newIngestionIntegration(t, 10, 1<<20)
		body := readCompatibilityFixture(t, "testdata/fluent-bit-v5.0.9-json-array.json")
		for attempt := 0; attempt < 2; attempt++ {
			response := compatibilityRequest(t, fixture, "application/json", "", body)
			if response.Code != http.StatusNoContent {
				t.Fatalf("attempt %d status = %d, body=%q", attempt+1, response.Code, response.Body.String())
			}
		}
		assertEventCount(t, fixture.db.Reader(), 2)
	})
}

func requireConfigDirectives(t *testing.T, config string, directives ...string) {
	t.Helper()
	for _, directive := range directives {
		if !strings.Contains(config, directive) {
			t.Errorf("configuration missing %q", directive)
		}
	}
}

func readCompatibilityFixture(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func gzipCompatibilityBody(t *testing.T, body []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

func compatibilityRequest(
	t *testing.T,
	fixture *ingestionIntegration,
	contentType, contentEncoding string,
	body []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/ingest",
		bytes.NewReader(body),
	)
	request.Header.Set("Authorization", "Bearer "+fixture.token)
	request.Header.Set("Content-Type", contentType)
	if contentEncoding != "" {
		request.Header.Set("Content-Encoding", contentEncoding)
	}
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	return response
}
