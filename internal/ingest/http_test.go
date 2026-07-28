package ingest

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
	"github.com/drilonrecica/siftail/internal/sources"
	"github.com/drilonrecica/siftail/internal/web"
)

type decoderFunc func(context.Context, DecodeRequest) (DecodedBatch, error)

func (f decoderFunc) Decode(ctx context.Context, request DecodeRequest) (DecodedBatch, error) {
	return f(ctx, request)
}

type fixedAvailability ErrorCategory

func (a fixedAvailability) IngestUnavailable() ErrorCategory {
	return ErrorCategory(a)
}

type handlerFixture struct {
	handler *Handler
	store   *sources.Store
	token   sources.CreatedToken
}

func newHandlerFixture(t *testing.T, decoder Decoder, maxBytes int64) handlerFixture {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := sources.NewStore(db.Writer())
	server, err := store.CreateServer(context.Background(), "Production", "")
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.CreateToken(context.Background(), server.ID, "primary")
	if err != nil {
		t.Fatal(err)
	}
	return handlerFixture{
		handler: NewHandler(store, decoder, Limits{MaxCompressedBytes: maxBytes, RequestTimeout: 25 * time.Millisecond}),
		store:   store, token: token,
	}
}

func TestHTTPBoundaryAuthenticatesBeforeDecodingAndBindsServer(t *testing.T) {
	called := false
	fixture := newHandlerFixture(t, decoderFunc(func(_ context.Context, request DecodeRequest) (DecodedBatch, error) {
		called = true
		if request.Server.ID <= 0 {
			t.Fatal("token did not establish a trusted Server")
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return DecodedBatch{}, err
		}
		if string(body) != `{"server_id":999,"log":"safe"}` {
			t.Fatalf("body = %q", body)
		}
		return DecodedBatch{}, nil
	}), 1024)

	request := authorizedRequest(fixture.token.Token, "application/json", `{"server_id":999,"log":"safe"}`)
	response := httptest.NewRecorder()
	web.RequestID(fixture.handler).ServeHTTP(response, request)
	if !called {
		t.Fatal("decoder was not called")
	}
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", response.Code)
	}
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("missing request ID")
	}
}

func TestHTTPBoundaryAdmission(t *testing.T) {
	decoder := decoderFunc(func(_ context.Context, request DecodeRequest) (DecodedBatch, error) {
		_, err := io.ReadAll(request.Body)
		return DecodedBatch{}, err
	})
	fixture := newHandlerFixture(t, decoder, 8)
	tests := []struct {
		name        string
		method      string
		path        string
		token       string
		contentType string
		encoding    string
		body        string
		want        int
	}{
		{"wrong path", http.MethodPost, "/other", fixture.token.Token, "application/json", "", `{}`, 404},
		{"wrong method", http.MethodGet, "/api/v1/ingest", fixture.token.Token, "application/json", "", "", 405},
		{"missing token", http.MethodPost, "/api/v1/ingest", "", "application/json", "", `{}`, 401},
		{"bad bearer", http.MethodPost, "/api/v1/ingest", "two words", "application/json", "", `{}`, 401},
		{"bad type", http.MethodPost, "/api/v1/ingest", fixture.token.Token, "text/plain", "", `{}`, 415},
		{"bad encoding", http.MethodPost, "/api/v1/ingest", fixture.token.Token, "application/json", "br", `{}`, 415},
		{"content length", http.MethodPost, "/api/v1/ingest", fixture.token.Token, "application/json", "", `{"long":true}`, 413},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			if tt.token != "" {
				request.Header.Set("Authorization", "Bearer "+tt.token)
			}
			request.Header.Set("Content-Type", tt.contentType)
			request.Header.Set("Content-Encoding", tt.encoding)
			response := httptest.NewRecorder()
			web.RequestID(fixture.handler).ServeHTTP(response, request)
			if response.Code != tt.want {
				t.Fatalf("status = %d, want %d; body=%q", response.Code, tt.want, response.Body.String())
			}
			if response.Header().Get("X-Request-ID") == "" {
				t.Fatal("missing request ID")
			}
		})
	}
}

func TestHTTPBoundaryRevocationIsImmediate(t *testing.T) {
	calls := 0
	fixture := newHandlerFixture(t, decoderFunc(func(context.Context, DecodeRequest) (DecodedBatch, error) {
		calls++
		return DecodedBatch{}, nil
	}), 1024)
	if err := fixture.store.RevokeToken(context.Background(), fixture.token.ID); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	web.RequestID(fixture.handler).ServeHTTP(response,
		authorizedRequest(fixture.token.Token, "application/json", `{}`))
	if response.Code != http.StatusUnauthorized || calls != 0 {
		t.Fatalf("status/calls = %d/%d", response.Code, calls)
	}
}

func TestHTTPBoundaryAuthenticatesThenShortCircuitsKnownStorageDegradation(t *testing.T) {
	for _, test := range []struct {
		category ErrorCategory
		status   int
	}{
		{CategoryStorageFull, http.StatusInsufficientStorage},
		{CategoryUnavailable, http.StatusServiceUnavailable},
	} {
		calls := 0
		fixture := newHandlerFixture(t, decoderFunc(func(
			context.Context,
			DecodeRequest,
		) (DecodedBatch, error) {
			calls++
			return DecodedBatch{}, nil
		}), 1024)
		fixture.handler.WithAvailability(fixedAvailability(test.category))

		unauthorized := httptest.NewRecorder()
		web.RequestID(fixture.handler).ServeHTTP(
			unauthorized,
			authorizedRequest("invalid", "application/json", `{"private":"payload"}`),
		)
		if unauthorized.Code != http.StatusUnauthorized || calls != 0 {
			t.Fatalf("%s unauthorized status/calls = %d/%d",
				test.category, unauthorized.Code, calls)
		}
		response := httptest.NewRecorder()
		web.RequestID(fixture.handler).ServeHTTP(
			response,
			authorizedRequest(
				fixture.token.Token, "application/json",
				`{"private":"payload"}`,
			),
		)
		if response.Code != test.status || calls != 0 ||
			strings.Contains(response.Body.String(), "private") {
			t.Fatalf("%s response = %d/%d %q",
				test.category, response.Code, calls, response.Body.String())
		}
	}
}

func TestHTTPBoundaryMapsSafeCategoriesWithoutPayload(t *testing.T) {
	payload := "private-payload-marker"
	categories := []struct {
		category ErrorCategory
		status   int
	}{
		{CategoryBadRequest, 400}, {CategoryForbidden, 403}, {CategoryConflict, 409},
		{CategoryTooLarge, 413}, {CategoryRateLimited, 429},
		{CategoryUnavailable, 503}, {CategoryStorageFull, 507},
	}
	for _, tt := range categories {
		fixture := newHandlerFixture(t, decoderFunc(func(context.Context, DecodeRequest) (DecodedBatch, error) {
			return DecodedBatch{}, &Error{Category: tt.category}
		}), 1024)
		response := httptest.NewRecorder()
		web.RequestID(fixture.handler).ServeHTTP(response,
			authorizedRequest(fixture.token.Token, "application/json", `{"log":"`+payload+`"}`))
		if response.Code != tt.status {
			t.Errorf("%s status = %d", tt.category, response.Code)
		}
		if strings.Contains(response.Body.String(), payload) {
			t.Fatal("response leaked payload")
		}
	}
}

func TestHTTPBoundaryRequestDeadline(t *testing.T) {
	fixture := newHandlerFixture(t, decoderFunc(func(ctx context.Context, _ DecodeRequest) (DecodedBatch, error) {
		<-ctx.Done()
		return DecodedBatch{}, ctx.Err()
	}), 1024)
	response := httptest.NewRecorder()
	web.RequestID(fixture.handler).ServeHTTP(response,
		authorizedRequest(fixture.token.Token, "application/json", `{}`))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", response.Code)
	}
}

func authorizedRequest(token, contentType, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/ingest", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", contentType)
	return request
}
