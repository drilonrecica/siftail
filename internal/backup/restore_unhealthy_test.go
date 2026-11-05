package backup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drilonrecica/siftail/internal/database"
)

// currentFixture prepares the data directory's current database in one of the
// states restore must classify (ADR 0009, ARCHITECTURE.md §25.5.1). It returns
// the bytes of each file it left in place so preservation can be checked.
type currentFixture struct {
	name  string
	state string
	setup func(t *testing.T, databasePath string)
}

var unhealthyCurrentFixtures = []currentFixture{
	{
		name: "missing", state: CurrentMissing,
		setup: func(*testing.T, string) {},
	},
	{
		name: "missing with stray wal", state: CurrentMissing,
		setup: func(t *testing.T, databasePath string) {
			writePrivateFile(t, databasePath+"-wal", "stray-wal-content")
		},
	},
	{
		name: "not sqlite", state: CurrentUnhealthy,
		setup: func(t *testing.T, databasePath string) {
			writePrivateFile(t, databasePath, "private-not-sqlite-content")
			writePrivateFile(t, databasePath+"-wal", "private-wal-content")
		},
	},
	{
		name: "damaged pages", state: CurrentUnhealthy,
		setup: func(t *testing.T, databasePath string) {
			current := newBackupFixtureAt(t, databasePath)
			seedFullBackupState(t, current.db.Writer())
			current.stop()
			info, err := os.Stat(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			file, err := os.OpenFile(databasePath, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			garbage := bytes.Repeat([]byte{0xA5}, 4096)
			for offset := int64(4096); offset+4096 <= info.Size(); offset += 8192 {
				if _, err := file.WriteAt(garbage, offset); err != nil {
					t.Fatal(err)
				}
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		},
	},
	{
		name: "newer schema", state: CurrentUnhealthy,
		setup: func(t *testing.T, databasePath string) {
			current := newBackupFixtureAt(t, databasePath)
			seedFullBackupState(t, current.db.Writer())
			current.stop()
			execBackupMutation(t, databasePath, `
				INSERT INTO schema_migrations(version,name,applied_at_us)
				VALUES(999,'future',1)
			`)
		},
	},
}

func TestRestoreRefusesUnusableCurrentDatabaseWithoutFlag(t *testing.T) {
	for _, fixture := range unhealthyCurrentFixtures {
		t.Run(fixture.name, func(t *testing.T) {
			dataDirectory := t.TempDir()
			databasePath := filepath.Join(dataDirectory, "siftail.db")
			fixture.setup(t, databasePath)
			before := snapshotDirectory(t, dataDirectory)
			artifact := createMarkedRestoreArtifact(t)

			_, err := NewRestorer().Restore(
				context.Background(), RestoreOptions{
					DataDirectory: dataDirectory, DatabasePath: databasePath,
					ArtifactPath: artifact, Confirmation: RestoreConfirmation,
				},
			)
			want := RestoreErrCurrentUnhealthy
			if fixture.state == CurrentMissing {
				want = RestoreErrCurrentMissing
			}
			if err == nil || err.Error() != want ||
				!strings.Contains(err.Error(), "--replace-unhealthy") {
				t.Fatalf("restore without flag = %v, want %q", err, want)
			}
			after := snapshotDirectory(t, dataDirectory)
			delete(after, database.MaintenanceLockPath(dataDirectory))
			delete(before, database.MaintenanceLockPath(dataDirectory))
			for path, content := range before {
				// A read-only check may add an empty -shm beside a WAL
				// database; nothing that existed may change.
				if after[path] != content {
					t.Fatalf("refused restore changed %s", filepath.Base(path))
				}
			}
			for path := range after {
				if _, existed := before[path]; !existed &&
					!strings.HasSuffix(path, "-shm") &&
					!strings.HasSuffix(path, "-wal") {
					t.Fatalf("refused restore created %s", filepath.Base(path))
				}
			}
		})
	}
}

func TestRestoreReplaceUnhealthyRestoresAndPreservesCurrentFiles(t *testing.T) {
	for _, fixture := range unhealthyCurrentFixtures {
		t.Run(fixture.name, func(t *testing.T) {
			dataDirectory := t.TempDir()
			databasePath := filepath.Join(dataDirectory, "siftail.db")
			fixture.setup(t, databasePath)
			rollbackPath := databasePath + ".rollback"
			writePrivateFile(t, rollbackPath, "previous-managed-rollback")
			mainBefore, mainErr := os.ReadFile(databasePath)
			walBefore, walErr := os.ReadFile(databasePath + "-wal")
			artifact := createMarkedRestoreArtifact(t)

			result, err := NewRestorer().Restore(
				context.Background(), RestoreOptions{
					DataDirectory: dataDirectory, DatabasePath: databasePath,
					ArtifactPath: artifact, Confirmation: RestoreConfirmation,
					ReplaceUnhealthy: true,
				},
			)
			if err != nil || result.CurrentState != fixture.state ||
				result.RollbackName != "" || result.Validate() != nil {
				t.Fatalf("restore = %#v, %v", result, err)
			}
			assertRestoredState(t, databasePath, TypeFull)

			rollback, err := os.ReadFile(rollbackPath)
			if err != nil || string(rollback) != "previous-managed-rollback" {
				t.Fatalf("managed rollback changed: %q, %v", rollback, err)
			}
			if _, err := os.Stat(
				filepath.Join(dataDirectory, "restore-staging"),
			); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("restore staging remains: %v", err)
			}

			nothingToPreserve := errors.Is(mainErr, os.ErrNotExist) &&
				errors.Is(walErr, os.ErrNotExist)
			if nothingToPreserve {
				if result.PreservedName != "" {
					t.Fatalf("preserved nothing as %q", result.PreservedName)
				}
				assertNoUnhealthyDirectory(t, dataDirectory)
				return
			}
			if !strings.HasPrefix(
				result.PreservedName, "siftail.db.unhealthy-",
			) {
				t.Fatalf("preserved name = %q", result.PreservedName)
			}
			preserved := filepath.Join(dataDirectory, result.PreservedName)
			info, err := os.Stat(preserved)
			if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
				t.Fatalf("preserved directory = %#v, %v", info, err)
			}
			if mainErr == nil {
				moved, err := os.ReadFile(filepath.Join(preserved, "siftail.db"))
				if err != nil || !bytes.Equal(moved, mainBefore) {
					t.Fatalf("preserved main file changed: %v", err)
				}
			} else if _, err := os.Stat(
				filepath.Join(preserved, "siftail.db"),
			); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("preserved a main file that did not exist: %v", err)
			}
			if walErr == nil {
				moved, err := os.ReadFile(
					filepath.Join(preserved, "siftail.db-wal"),
				)
				if err != nil || !bytes.Equal(moved, walBefore) {
					t.Fatalf("preserved WAL changed: %v", err)
				}
			}
		})
	}
}

func TestRestoreReplaceUnhealthyHasNoEffectOnHealthyCurrentDatabase(
	t *testing.T,
) {
	dataDirectory := t.TempDir()
	databasePath := filepath.Join(dataDirectory, "siftail.db")
	current := newBackupFixtureAt(t, databasePath)
	seedFullBackupState(t, current.db.Writer())
	if _, err := current.db.Writer().Exec(`
		INSERT INTO settings(key,value_json,updated_at_us)
		VALUES('current_only','{"current":true}',2)
	`); err != nil {
		t.Fatal(err)
	}
	current.stop()
	artifact := createMarkedRestoreArtifact(t)

	result, err := NewRestorer().Restore(
		context.Background(), RestoreOptions{
			DataDirectory: dataDirectory, DatabasePath: databasePath,
			ArtifactPath: artifact, Confirmation: RestoreConfirmation,
			ReplaceUnhealthy: true,
		},
	)
	if err != nil || result.CurrentState != CurrentHealthy ||
		result.RollbackName != "siftail.db.rollback" ||
		result.PreservedName != "" || result.Validate() != nil {
		t.Fatalf("restore = %#v, %v", result, err)
	}
	assertRestoredState(t, databasePath, TypeFull)
	if rollback, err := Verify(
		context.Background(), databasePath+".rollback",
	); err != nil || rollback.Type != TypeFull {
		t.Fatalf("rollback verification = %#v, %v", rollback, err)
	}
	assertNoUnhealthyDirectory(t, dataDirectory)
}

func TestRestoreReplaceUnhealthyFailureAfterInstallKeepsPreservedFiles(
	t *testing.T,
) {
	for _, fixture := range []currentFixture{
		unhealthyCurrentFixtures[0], unhealthyCurrentFixtures[2],
	} {
		t.Run(fixture.name, func(t *testing.T) {
			dataDirectory := t.TempDir()
			databasePath := filepath.Join(dataDirectory, "siftail.db")
			fixture.setup(t, databasePath)
			mainBefore, mainErr := os.ReadFile(databasePath)
			artifact := createMarkedRestoreArtifact(t)
			restorer := NewRestorer()
			restorer.hooks.afterInstall = func() error {
				return errors.New("injected interruption")
			}

			result, err := restorer.Restore(
				context.Background(), RestoreOptions{
					DataDirectory: dataDirectory, DatabasePath: databasePath,
					ArtifactPath: artifact, Confirmation: RestoreConfirmation,
					ReplaceUnhealthy: true,
				},
			)
			if err == nil || err.Error() != RestoreErrFailedNoDatabase ||
				result.CurrentState != fixture.state {
				t.Fatalf("faulted restore = %#v, %v", result, err)
			}
			for _, suffix := range databaseFileSuffixes {
				if _, err := os.Lstat(
					databasePath + suffix,
				); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("installed file %q remains: %v", suffix, err)
				}
			}
			if _, err := os.Stat(
				filepath.Join(dataDirectory, "restore-staging"),
			); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("restore staging remains: %v", err)
			}
			if mainErr != nil {
				if result.PreservedName != "" {
					t.Fatalf("preserved nothing as %q", result.PreservedName)
				}
				return
			}
			moved, err := os.ReadFile(filepath.Join(
				dataDirectory, result.PreservedName, "siftail.db",
			))
			if err != nil || !bytes.Equal(moved, mainBefore) {
				t.Fatalf("preserved main file after failure: %v", err)
			}
		})
	}
}

func createMarkedRestoreArtifact(t *testing.T) string {
	t.Helper()
	source := newBackupFixture(t)
	seedFullBackupState(t, source.db.Writer())
	if _, err := source.db.Writer().Exec(`
		INSERT INTO settings(key,value_json,updated_at_us)
		VALUES('artifact_only','{"artifact":true}',2)
	`); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(t.TempDir(), "artifact.sqlite")
	if _, err := source.service.CreateFull(
		context.Background(), artifact, nil,
	); err != nil {
		t.Fatal(err)
	}
	return artifact
}

func writePrivateFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func snapshotDirectory(t *testing.T, directory string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	contents := make(map[string]string, len(entries))
	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		if entry.IsDir() {
			contents[path] = "<dir>"
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		contents[path] = string(data)
	}
	return contents
}

func assertNoUnhealthyDirectory(t *testing.T, directory string) {
	t.Helper()
	matches, err := filepath.Glob(
		filepath.Join(directory, "siftail.db.unhealthy-*"),
	)
	if err != nil || len(matches) != 0 {
		t.Fatalf("unexpected unhealthy preservation: %v, %v", matches, err)
	}
}
