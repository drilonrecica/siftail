package logs

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
)

func TestCursorCodecPersistsAndAuthenticatesKeysets(t *testing.T) {
	db, coordinator := cursorTestDatabase(t)
	codec, err := LoadCursorCodec(context.Background(), db.Reader(), coordinator)
	if err != nil {
		t.Fatal(err)
	}
	query := testHistoryQuery()
	encoded, err := codec.Encode(query, -1234567, 42)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(encoded, "+/=") {
		t.Errorf("cursor is not raw base64url: %q", encoded)
	}
	if strings.Contains(query.CanonicalValues(true).Encode(), "history_cursor_hmac_key") {
		t.Fatal("canonical URL exposed key setting")
	}

	reloaded, err := LoadCursorCodec(context.Background(), db.Reader(), coordinator)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := reloaded.Decode(query, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if cursor.EventAtUS != -1234567 || cursor.ID != 42 || cursor.Direction != DirectionOlder {
		t.Fatalf("decoded cursor = %#v", cursor)
	}
	var count int
	if err := db.Reader().QueryRow(
		`SELECT count(*) FROM settings WHERE key = ?`, historyCursorKeySetting,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("stored key count = %d", count)
	}
}

func TestCursorCodecRejectsTamperingAndQueryMismatch(t *testing.T) {
	codec, err := newCursorCodec([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	query := testHistoryQuery()
	encoded, err := codec.Encode(query, 100, 7)
	if err != nil {
		t.Fatal(err)
	}
	tampered := []byte(encoded)
	if tampered[0] == 'A' {
		tampered[0] = 'B'
	} else {
		tampered[0] = 'A'
	}
	changedQuery := query
	changedQuery.Contains = "different"
	newerQuery := query
	newerQuery.Direction = DirectionNewer
	cases := map[string]struct {
		query  HistoryQuery
		cursor string
	}{
		"tampered":       {query, string(tampered)},
		"query mismatch": {changedQuery, encoded},
		"direction":      {newerQuery, encoded},
		"empty":          {query, ""},
		"extra section":  {query, encoded + ".extra"},
		"padding":        {query, encoded + "="},
		"hostile":        {query, "<script>\n"},
		"oversized":      {query, strings.Repeat("a", maxHistoryCursorBytes+1)},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := codec.Decode(test.query, test.cursor); !errors.Is(err, ErrInvalidHistoryCursor) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestCursorCodecRejectsAuthenticatedInvalidPayloads(t *testing.T) {
	codec, err := newCursorCodec([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	query := testHistoryQuery()
	for name, mutate := range map[string]func([]byte){
		"version":   func(payload []byte) { payload[0] = 2 },
		"direction": func(payload []byte) { payload[17] = 9 },
		"zero ID":   func(payload []byte) { clear(payload[9:17]) },
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := codec.Encode(query, 100, 7)
			if err != nil {
				t.Fatal(err)
			}
			parts := strings.Split(encoded, ".")
			payload, err := base64.RawURLEncoding.DecodeString(parts[0])
			if err != nil {
				t.Fatal(err)
			}
			mutate(payload)
			signature := signCursorPayload(codec, payload)
			invalid := base64.RawURLEncoding.EncodeToString(payload) + "." +
				base64.RawURLEncoding.EncodeToString(signature)
			if _, err := codec.Decode(query, invalid); !errors.Is(err, ErrInvalidHistoryCursor) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLoadCursorCodecRejectsInvalidStoredKey(t *testing.T) {
	db, coordinator := cursorTestDatabase(t)
	if err := coordinator.Do(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO settings(key, value_json, updated_at_us)
			VALUES (?, '"short"', 1)`, historyCursorKeySetting)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCursorCodec(context.Background(), db.Reader(), coordinator); err == nil ||
		strings.Contains(err.Error(), "short") {
		t.Fatalf("unsafe or missing error: %v", err)
	}
}

func FuzzCursorDecode(f *testing.F) {
	codec, err := newCursorCodec([]byte("01234567890123456789012345678901"))
	if err != nil {
		f.Fatal(err)
	}
	query := testHistoryQuery()
	valid, err := codec.Encode(query, 100, 7)
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range []string{"", valid, "a.b", "<script>", strings.Repeat("x", 1025)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, encoded string) {
		if len(encoded) > 2048 {
			t.Skip()
		}
		_, _ = codec.Decode(query, encoded)
	})
}

func cursorTestDatabase(t *testing.T) (*database.DB, *database.Coordinator) {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	coordinator := database.NewCoordinator(db.Writer())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	<-coordinator.Ready()
	t.Cleanup(func() {
		coordinator.Close()
		cancel()
		if err := <-done; err != nil {
			t.Errorf("coordinator: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	return db, coordinator
}

func testHistoryQuery() HistoryQuery {
	return HistoryQuery{
		FromUS:    time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC).UnixMicro(),
		ToUS:      time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC).UnixMicro(),
		Levels:    []Level{LevelError},
		Direction: DirectionOlder,
		Limit:     200,
	}
}

func signCursorPayload(codec *CursorCodec, payload []byte) []byte {
	signature := hmac.New(sha256.New, codec.key[:])
	_, _ = signature.Write(payload)
	return signature.Sum(nil)
}
