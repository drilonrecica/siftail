package backup

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyRejectsInvalidIncompleteIncompatibleAndSessionArtifacts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{
			name: "corrupt",
			mutate: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("not sqlite"), 0600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unsafe permissions",
			mutate: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "incomplete",
			mutate: func(t *testing.T, path string) {
				t.Helper()
				execBackupMutation(t, path,
					"UPDATE siftail_backup_metadata SET complete=0")
			},
		},
		{
			name: "schema too new",
			mutate: func(t *testing.T, path string) {
				t.Helper()
				execBackupMutation(t, path,
					"UPDATE siftail_backup_metadata SET source_schema_version=99")
			},
		},
		{
			name: "missing required table",
			mutate: func(t *testing.T, path string) {
				t.Helper()
				execBackupMutation(t, path, "DROP TABLE log_events")
			},
		},
		{
			name: "contains session",
			mutate: func(t *testing.T, path string) {
				t.Helper()
				execBackupMutation(t, path, `INSERT INTO sessions(
					administrator_id,token_hash,created_at_us,last_used_at_us,
					expires_at_us
				) VALUES(1,randomblob(32),1,1,2)`)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := createVerifiedTestArtifact(t)
			test.mutate(t, path)
			if _, err := Verify(context.Background(), path); err == nil {
				t.Fatal("invalid artifact verified")
			}
		})
	}
}

func TestVerifyRejectsCancellationAndSymlink(t *testing.T) {
	path := createVerifiedTestArtifact(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Verify(ctx, path); err == nil {
		t.Fatal("canceled verification succeeded")
	}
	link := filepath.Join(t.TempDir(), "linked.sqlite")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(context.Background(), link); err == nil {
		t.Fatal("symlink artifact verified")
	}
}

func createVerifiedTestArtifact(t *testing.T) string {
	t.Helper()
	fixture := newBackupFixture(t)
	seedFullBackupState(t, fixture.db.Writer())
	path := filepath.Join(t.TempDir(), "verified.sqlite")
	if _, err := fixture.service.CreateFull(
		context.Background(), path, nil,
	); err != nil {
		t.Fatal(err)
	}
	return path
}

func execBackupMutation(t *testing.T, path, query string) {
	t.Helper()
	db, err := sql.Open("sqlite3", sqlitePath(path, "rw"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(query); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}
