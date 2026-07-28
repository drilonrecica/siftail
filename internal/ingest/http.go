// Package ingest owns the authenticated public ingestion transport and bounded
// request admission.
package ingest

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/drilonrecica/siftail/internal/logs"
	"github.com/drilonrecica/siftail/internal/sources"
	"github.com/drilonrecica/siftail/internal/web"
)

type DecodeRequest struct {
	Body       io.Reader
	MediaType  string
	Gzip       bool
	ReceivedAt time.Time
	Server     logs.TrustedServer
	RequestID  string
}

type DecodedBatch struct {
	Events      []logs.CanonicalEvent
	ApproxBytes int64
	lease       *residentLease
}

// Decoder is the hostile-input boundary owned by the ingestion consumer.
type Decoder interface {
	Decode(context.Context, DecodeRequest) (DecodedBatch, error)
}

type Limits struct {
	MaxCompressedBytes int64
	RequestTimeout     time.Duration
}

type Handler struct {
	tokens  *sources.Store
	decoder Decoder
	queue   *Queue
	limits  Limits
	now     func() time.Time
}

func (h *Handler) WithQueue(queue *Queue) *Handler {
	h.queue = queue
	return h
}

func NewHandler(tokens *sources.Store, decoder Decoder, limits Limits) *Handler {
	if limits.RequestTimeout <= 0 {
		limits.RequestTimeout = 30 * time.Second
	}
	return &Handler{tokens: tokens, decoder: decoder, limits: limits, now: time.Now}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/v1/ingest" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeSafe(w, http.StatusMethodNotAllowed)
		return
	}
	token, ok := bearerToken(r.Header.Values("Authorization"))
	if !ok {
		writeSafe(w, http.StatusUnauthorized)
		return
	}
	server, err := h.tokens.VerifyToken(r.Context(), token)
	if err != nil {
		writeSafe(w, http.StatusUnauthorized)
		return
	}
	mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/json" && mediaType != "application/x-ndjson") {
		writeSafe(w, http.StatusUnsupportedMediaType)
		return
	}
	for key, value := range parameters {
		if !strings.EqualFold(key, "charset") || !strings.EqualFold(value, "utf-8") {
			writeSafe(w, http.StatusUnsupportedMediaType)
			return
		}
	}
	encodings := r.Header.Values("Content-Encoding")
	if len(encodings) > 1 {
		writeSafe(w, http.StatusUnsupportedMediaType)
		return
	}
	encoding := strings.TrimSpace(strings.ToLower(r.Header.Get("Content-Encoding")))
	if encoding != "" && encoding != "gzip" {
		writeSafe(w, http.StatusUnsupportedMediaType)
		return
	}
	if r.ContentLength > h.limits.MaxCompressedBytes && r.ContentLength >= 0 {
		writeSafe(w, http.StatusRequestEntityTooLarge)
		return
	}
	if h.decoder == nil {
		writeSafe(w, http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.limits.RequestTimeout)
	defer cancel()
	body := http.MaxBytesReader(w, r.Body, h.limits.MaxCompressedBytes)
	defer body.Close()
	batch, err := h.decoder.Decode(ctx, DecodeRequest{
		Body: body, MediaType: mediaType, Gzip: encoding == "gzip",
		ReceivedAt: h.now(), Server: logs.TrustedServer{ID: server.ID},
		RequestID: requestID(r),
	})
	if err == nil {
		if h.queue == nil {
			if batch.lease != nil {
				batch.lease.release()
			}
			writeSafe(w, http.StatusServiceUnavailable)
			return
		}
		writeBatch := NewWriteBatch(batch, requestID(r), server.ID)
		writeBatch.AuthenticatedTokenID = server.TokenID
		if err := h.queue.Enqueue(writeBatch); err != nil {
			writeSafe(w, statusFor(err))
			return
		}
		select {
		case err := <-writeBatch.Result:
			if err != nil {
				writeSafe(w, statusFor(err))
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case <-ctx.Done():
			// Queue ownership survives a disconnected or timed-out request.
			// The buffered result lets the writer finish without blocking.
			return
		}
		return
	}
	writeSafe(w, statusFor(err))
}

func bearerToken(values []string) (string, bool) {
	if len(values) != 1 {
		return "", false
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || scheme != "Bearer" || token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", false
	}
	return token, true
}

type ErrorCategory string

const (
	CategoryBadRequest  ErrorCategory = "bad_request"
	CategoryForbidden   ErrorCategory = "forbidden"
	CategoryConflict    ErrorCategory = "conflict"
	CategoryTooLarge    ErrorCategory = "too_large"
	CategoryRateLimited ErrorCategory = "rate_limited"
	CategoryUnavailable ErrorCategory = "unavailable"
	CategoryStorageFull ErrorCategory = "storage_full"
)

type Error struct{ Category ErrorCategory }

func (e *Error) Error() string { return "ingestion " + string(e.Category) }

func statusFor(err error) int {
	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) {
		return http.StatusRequestEntityTooLarge
	}
	if errors.Is(err, ErrAdmissionClosed) || errors.Is(err, ErrQueueClosed) {
		return http.StatusServiceUnavailable
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusServiceUnavailable
	}
	var ingestErr *Error
	if !errors.As(err, &ingestErr) {
		return http.StatusBadRequest
	}
	switch ingestErr.Category {
	case CategoryForbidden:
		return http.StatusForbidden
	case CategoryConflict:
		return http.StatusConflict
	case CategoryTooLarge:
		return http.StatusRequestEntityTooLarge
	case CategoryRateLimited:
		return http.StatusTooManyRequests
	case CategoryUnavailable:
		return http.StatusServiceUnavailable
	case CategoryStorageFull:
		return http.StatusInsufficientStorage
	default:
		return http.StatusBadRequest
	}
}

func writeSafe(w http.ResponseWriter, status int) {
	http.Error(w, http.StatusText(status), status)
}

func requestID(r *http.Request) string {
	if id := web.RequestIDFromContext(r.Context()); id != "" {
		return id
	}
	return r.Header.Get("X-Request-ID")
}
