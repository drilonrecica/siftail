package sources

import (
	"context"
	"crypto/sha256"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drilonrecica/siftail/internal/database"
)

func TestServerAndTokenLifecycle(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewStore(db.Writer())

	server, err := store.CreateServer(context.Background(), "Production", "prod.example")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateServer(context.Background(), "Production", "other"); err == nil {
		t.Fatal("duplicate server name accepted")
	}
	servers, err := store.ListServers(context.Background())
	if err != nil || len(servers) != 1 || servers[0] != server {
		t.Fatalf("servers = %#v, err=%v", servers, err)
	}

	first, err := store.CreateToken(context.Background(), server.ID, "primary")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateToken(context.Background(), server.ID, "replacement")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.Token, "sft_") || first.Token == second.Token {
		t.Fatal("invalid token generation")
	}
	for _, plaintext := range []string{first.Token, second.Token} {
		authenticated, err := store.VerifyToken(context.Background(), plaintext)
		if err != nil || authenticated.ID != server.ID || authenticated.TokenID <= 0 {
			t.Fatalf("verify = %#v, %v", authenticated, err)
		}
	}

	var stored []byte
	if err := db.Writer().QueryRow("SELECT token_hash FROM ingestion_tokens WHERE id=?", first.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(first.Token))
	if string(stored) != string(hash[:]) || strings.Contains(string(stored), first.Token) {
		t.Fatal("token was not stored as SHA-256 only")
	}

	if err := store.RevokeToken(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.VerifyToken(context.Background(), first.Token); err == nil {
		t.Fatal("revoked token authenticated")
	}
	if _, err := store.VerifyToken(context.Background(), second.Token); err != nil {
		t.Fatal("replacement token was revoked with old token")
	}
	if _, err := store.VerifyToken(context.Background(), "sft_missing"); err == nil {
		t.Fatal("unknown token authenticated")
	}
}

func TestTokenCreateRollsBackOnMissingServer(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewStore(db.Writer())
	if _, err := store.CreateToken(context.Background(), 404, "missing"); err == nil {
		t.Fatal("token for missing server was created")
	}
	var count int
	if err := db.Writer().QueryRow("SELECT count(*) FROM ingestion_tokens").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("token rows = %d", count)
	}
}

func TestValidTextRejectsEveryControlCharacter(t *testing.T) {
	for _, value := range []string{"a\x00b", "a\x1bb", "a\x7fb", "a\u0080b", "a\u009b31mb", "a\u009fb", "line\nbreak"} {
		if err := validText(value, 128, false); err == nil {
			t.Errorf("validText(%q) accepted a control character", value)
		}
	}
	for _, value := range []string{"Production", "Prod – Zürich", "東京 node"} {
		if err := validText(value, 128, false); err != nil {
			t.Errorf("validText(%q) = %v", value, err)
		}
	}

	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := NewStore(db.Writer()).CreateServer(context.Background(), "evil\u009b2J", ""); err == nil {
		t.Fatal("server name with a C1 control character accepted")
	}
}
