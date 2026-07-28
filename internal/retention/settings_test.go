package retention

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
)

func TestSettingsDefaultsPersistenceAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, coordinator, stop := settingsTestDatabase(t, path)
	store := NewStore(db.Reader(), coordinator)

	defaults, err := store.Load(context.Background())
	if err != nil || defaults != Defaults() {
		t.Fatalf("defaults = %#v, err=%v", defaults, err)
	}
	var rows int
	if err := db.Reader().QueryRow(
		"SELECT count(*) FROM settings WHERE key=?", applicationRetentionKey,
	).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("default rows = %d, err=%v", rows, err)
	}

	store.now = func() time.Time { return time.UnixMicro(1234) }
	saved, err := store.Save(context.Background(), Input{
		AgeDays: 30, MaxDatabaseGiB: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.AgeDays != 30 || saved.MaxDatabaseGiB() != 8 || saved.UpdatedAtUS != 1234 {
		t.Fatalf("saved = %#v", saved)
	}
	stop()

	reopened, reopenedCoordinator, stopReopened := settingsTestDatabase(t, path)
	defer stopReopened()
	loaded, err := NewStore(reopened.Reader(), reopenedCoordinator).Load(context.Background())
	if err != nil || loaded != saved {
		t.Fatalf("restarted settings = %#v, want %#v, err=%v", loaded, saved, err)
	}
}

func TestSettingsValidationDoesNotChangePriorValues(t *testing.T) {
	db, coordinator, stop := settingsTestDatabase(t, filepath.Join(t.TempDir(), "siftail.db"))
	defer stop()
	store := NewStore(db.Reader(), coordinator)
	prior, err := store.Save(context.Background(), Input{
		AgeDays: 7, MaxDatabaseGiB: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		input Input
		err   error
	}{
		{Input{AgeDays: 0, MaxDatabaseGiB: 2}, ErrInvalidAgeLimit},
		{Input{AgeDays: MaximumAgeDays + 1, MaxDatabaseGiB: 2}, ErrInvalidAgeLimit},
		{Input{AgeDays: 7, MaxDatabaseGiB: 0}, ErrInvalidSizeLimit},
		{Input{AgeDays: 7, MaxDatabaseGiB: MaximumMaxDatabaseGiB + 1}, ErrInvalidSizeLimit},
	}
	for _, test := range tests {
		if _, err := store.Save(context.Background(), test.input); !errors.Is(err, test.err) {
			t.Errorf("Save(%#v) error = %v, want %v", test.input, err, test.err)
		}
		loaded, err := store.Load(context.Background())
		if err != nil || loaded != prior {
			t.Fatalf("settings changed after invalid input: %#v, err=%v", loaded, err)
		}
	}
}

func TestSettingsRollbackAndStoredValidation(t *testing.T) {
	db, coordinator, stop := settingsTestDatabase(t, filepath.Join(t.TempDir(), "siftail.db"))
	defer stop()
	store := NewStore(db.Reader(), coordinator)
	prior, err := store.Save(context.Background(), Input{
		AgeDays: 14, MaxDatabaseGiB: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec(`CREATE TRIGGER reject_retention_update
		BEFORE UPDATE ON settings WHEN new.key='application_retention'
		BEGIN SELECT raise(ABORT, 'rejected test update'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(context.Background(), Input{
		AgeDays: 30, MaxDatabaseGiB: 8,
	}); err == nil {
		t.Fatal("triggered update succeeded")
	}
	loaded, err := store.Load(context.Background())
	if err != nil || loaded != prior {
		t.Fatalf("settings changed after rollback: %#v, err=%v", loaded, err)
	}
	if _, err := db.Writer().Exec("DROP TRIGGER reject_retention_update"); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Writer().Exec(`UPDATE settings SET value_json=? WHERE key=?`,
		`{"age_days":14,"max_database_bytes":4294967296,"unexpected":true}`,
		applicationRetentionKey,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background()); !errors.Is(err, ErrInvalidStored) {
		t.Fatalf("corrupt settings error = %v", err)
	}
	if _, err := decodeSettings(
		`{"age_days":14,"max_database_bytes":4294967296} {}`,
	); !errors.Is(err, ErrInvalidStored) {
		t.Fatalf("trailing settings JSON error = %v", err)
	}
}

func TestSettingsConcurrentUpdatesRemainAtomic(t *testing.T) {
	db, coordinator, stop := settingsTestDatabase(t, filepath.Join(t.TempDir(), "siftail.db"))
	defer stop()
	store := NewStore(db.Reader(), coordinator)

	var workers sync.WaitGroup
	for index := 1; index <= 24; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			if _, err := store.Save(context.Background(), Input{
				AgeDays: index, MaxDatabaseGiB: index,
			}); err != nil {
				t.Errorf("save %d: %v", index, err)
			}
		}(index)
	}
	workers.Wait()
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AgeDays != loaded.MaxDatabaseGiB() ||
		loaded.AgeDays < 1 || loaded.AgeDays > 24 {
		t.Fatalf("torn concurrent settings = %#v", loaded)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Save(ctx, Input{
		AgeDays: 40, MaxDatabaseGiB: 40,
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled save error = %v", err)
	}
}

func settingsTestDatabase(
	t *testing.T,
	path string,
) (*database.DB, *database.Coordinator, func()) {
	t.Helper()
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	coordinator := database.NewCoordinator(db.Writer())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	<-coordinator.Ready()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			coordinator.Close()
			cancel()
			<-done
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Cleanup(stop)
	return db, coordinator, stop
}
