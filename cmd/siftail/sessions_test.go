package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drilonrecica/siftail/internal/auth"
	"github.com/drilonrecica/siftail/internal/database"
	"github.com/drilonrecica/siftail/internal/sessions"
)

func TestSessionsRevokeAllCLIOffline(t *testing.T) {
	clearSiftailEnv(t)
	dataDir := t.TempDir()
	t.Setenv("SIFTAIL_DATA_DIR", dataDir)
	db, err := database.Open(context.Background(), filepath.Join(dataDir, "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.NewStore(db.Writer()).Create(
		context.Background(), "Admin", []byte("sessions-password"),
	); err != nil {
		t.Fatal(err)
	}
	store := sessions.NewStore(db.Writer())
	issued, err := store.Issue(context.Background(), 1, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runSessionCommand([]string{"revoke-all"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "revoked 1") ||
		strings.Contains(output.String(), issued.Token) {
		t.Fatalf("session CLI output = %q", output.String())
	}
	db, err = database.Open(context.Background(), filepath.Join(dataDir, "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := sessions.NewStore(db.Reader()).Lookup(context.Background(), issued.Token); err == nil {
		t.Fatal("CLI did not revoke session")
	}
}
