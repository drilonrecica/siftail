package ingest

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
	"github.com/drilonrecica/siftail/internal/sources"
	"github.com/drilonrecica/siftail/internal/web"
)

// TestHTTPBoundaryAuthenticatesWhileReaderPoolIsExhausted wires the handler
// exactly as the application does: token lookups on the dedicated
// authentication pool, token mutations through the coordinator. Every
// ordinary reader connection is held open (exports, contains searches, an
// active quick_check), yet authentication proceeds, and a revocation
// committed meanwhile rejects the very next request (audit DAT-08).
func TestHTTPBoundaryAuthenticatesWhileReaderPoolIsExhausted(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	coordinator := database.NewCoordinator(db.Writer())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	<-coordinator.Ready()
	t.Cleanup(func() {
		coordinator.Close()
		cancel()
		<-done
	})
	administration := sources.NewCoordinatedStore(db.Reader(), coordinator)
	server, err := administration.CreateServer(context.Background(), "Production", "")
	if err != nil {
		t.Fatal(err)
	}
	token, err := administration.CreateToken(context.Background(), server.ID, "primary")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	handler := NewHandler(
		sources.NewCoordinatedStore(db.AuthReader(), coordinator),
		decoderFunc(func(context.Context, DecodeRequest) (DecodedBatch, error) {
			calls++
			return DecodedBatch{}, nil
		}),
		Limits{MaxCompressedBytes: 1024, RequestTimeout: 25 * time.Millisecond},
	)

	for range 4 {
		conn, err := db.Reader().Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		tx, err := conn.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		var count int
		if err := tx.QueryRow("SELECT count(*) FROM log_events").Scan(&count); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = tx.Rollback()
			_ = conn.Close()
		})
	}
	send := func() int {
		requestCtx, cancelRequest := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancelRequest()
		request := authorizedRequest(token.Token, "application/json", `{}`).
			WithContext(requestCtx)
		response := httptest.NewRecorder()
		web.RequestID(handler).ServeHTTP(response, request)
		return response.Code
	}

	if status := send(); calls != 1 || status == http.StatusUnauthorized {
		t.Fatalf("authentication starved by the reader pool: status %d, calls %d",
			status, calls)
	}
	if err := administration.RevokeToken(context.Background(), token.ID); err != nil {
		t.Fatal(err)
	}
	if status := send(); status != http.StatusUnauthorized || calls != 1 {
		t.Fatalf("revoked token: status %d, calls %d", status, calls)
	}
}
