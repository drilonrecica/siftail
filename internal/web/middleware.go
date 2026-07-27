package web

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

const requestIDHeader = "X-Request-ID"

// RequestID assigns a correlation ID to every request and response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if id == "" {
			id = generateID()
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(ContextWithRequestID(r.Context(), id)))
	})
}

// PanicRecovery recovers request panics and returns a generic 500 with the
// request ID. It does not recover lifecycle panics; panics outside request
// handling remain process-fatal.
func PanicRecovery(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					id := RequestIDFromContext(r.Context())
					if id == "" {
						id = generateID()
						w.Header().Set(requestIDHeader, id)
					}
					logger.Error("request panic recovered",
						"request_id", id,
						"panic", rec,
						"stack", debug.Stack(),
					)
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = fmt.Fprintf(w, "internal error (request %s)\n", id)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// RequestLogger logs request metadata without bodies or headers.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}
			next.ServeHTTP(ww, r)
			logger.Info("http request",
				"request_id", RequestIDFromContext(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.statusCode,
				"duration", time.Since(start),
			)
		})
	}
}

// WithCommonHeaders sets baseline headers shared by both listeners.
func WithCommonHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

type responseRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (rr *responseRecorder) WriteHeader(code int) {
	rr.statusCode = code
	rr.ResponseWriter.WriteHeader(code)
}

func generateID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fall back to a timestamp-based ID if crypto/rand fails.
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
