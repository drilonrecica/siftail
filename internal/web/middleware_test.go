package web

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestIDAssigned(t *testing.T) {
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := RequestIDFromContext(r.Context())
		if id == "" {
			t.Fatal("request id missing from context")
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if rr.Header().Get("X-Request-ID") == "" {
		t.Fatal("X-Request-ID header missing")
	}
}

func TestRequestIDDoesNotTrustCallerValue(t *testing.T) {
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "client-id")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get("X-Request-ID"); got == "" || got == "client-id" {
		t.Fatalf("X-Request-ID = %q, want generated internal ID", got)
	}
}

func TestPanicRecovery(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	handler := PanicRecovery(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(body), "internal error") {
		t.Fatalf("body = %q", body)
	}
	if rr.Header().Get("X-Request-ID") == "" {
		t.Fatal("missing X-Request-ID on panic response")
	}
	if !strings.Contains(buf.String(), "panic") {
		t.Fatal("expected panic log")
	}
	if strings.Contains(buf.String(), "boom") {
		t.Fatal("panic value leaked into log")
	}
}

func TestRequestLoggerDoesNotLogBody(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	handler := RequestLogger(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("secret-body"))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	out := buf.String()
	if strings.Contains(out, "secret-body") {
		t.Fatalf("request body leaked into logs: %s", out)
	}
}

func TestRequestLoggerDoesNotLogHostilePath(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	mux := http.NewServeMux()
	mux.HandleFunc("/known", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := RequestLogger(logger)(mux)

	req := httptest.NewRequest(http.MethodGet, "/secret-token-in-path", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if strings.Contains(buf.String(), "secret-token-in-path") {
		t.Fatalf("untrusted path leaked into logs: %s", buf.String())
	}
}

func TestCommonHeaders(t *testing.T) {
	handler := WithCommonHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff header")
	}
}
