package backup

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
)

func TestManagerSerializesReportsAndCompletesBackup(t *testing.T) {
	fixture := newBackupFixture(t)
	seedFullBackupState(t, fixture.db.Writer())
	fixture.service.stepPages = 8
	manager := NewManager(fixture.service)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	<-manager.Ready()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})

	output := filepath.Join(t.TempDir(), "managed.sqlite")
	started, err := manager.Start(context.Background(), output)
	if err != nil || started.State != StateRunning || started.Validate() != nil {
		t.Fatalf("started = %#v, %v", started, err)
	}
	if _, err := manager.Start(
		context.Background(), filepath.Join(t.TempDir(), "second.sqlite"),
	); !errors.Is(err, ErrBackupInProgress) {
		t.Fatalf("parallel start = %v", err)
	}
	status := waitBackupStatus(t, manager, started.ID)
	if status.State != StateSucceeded || status.Result == nil ||
		status.CompletedUnits != status.TotalUnits || status.Validate() != nil {
		t.Fatalf("completed = %#v", status)
	}
}

func TestManagerCreatesConfigurationAndVerifiesTypedArtifact(t *testing.T) {
	fixture := newBackupFixture(t)
	seedFullBackupState(t, fixture.db.Writer())
	manager := NewManager(fixture.service)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	<-manager.Ready()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})

	output := filepath.Join(t.TempDir(), "configuration.sqlite")
	started, err := manager.StartConfiguration(context.Background(), output)
	if err != nil || started.Operation != OperationCreate ||
		started.BackupType != TypeConfiguration ||
		started.Validate() != nil {
		t.Fatalf("configuration start = %#v, %v", started, err)
	}
	created := waitBackupStatus(t, manager, started.ID)
	if created.State != StateSucceeded || created.Result == nil ||
		created.Result.Type != TypeConfiguration ||
		created.Unit != "rows" || created.Validate() != nil {
		t.Fatalf("configuration result = %#v", created)
	}

	started, err = manager.StartVerify(context.Background(), output)
	if err != nil || started.Operation != OperationVerify ||
		started.BackupType != "" || started.Validate() != nil {
		t.Fatalf("verification start = %#v, %v", started, err)
	}
	verified := waitBackupStatus(t, manager, started.ID)
	if verified.State != StateSucceeded || verified.Result == nil ||
		verified.Result.Type != TypeConfiguration ||
		verified.BackupType != TypeConfiguration ||
		verified.Operation != OperationVerify ||
		verified.Validate() != nil {
		t.Fatalf("verification result = %#v", verified)
	}
}

func TestManagerCancellationStopsOwnedBackupAndLeavesNoArtifact(t *testing.T) {
	fixture := newBackupFixture(t)
	copyStarted := make(chan struct{})
	fixture.service.copy = func(
		ctx context.Context,
		_ *sql.DB,
		_ string,
		_ int,
		progress func(database.BackupProgress),
	) error {
		progress(database.BackupProgress{PageCount: 10, Remaining: 6})
		close(copyStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	manager := NewManager(fixture.service)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	<-manager.Ready()
	output := filepath.Join(t.TempDir(), "canceled.sqlite")
	started, err := manager.Start(context.Background(), output)
	if err != nil {
		t.Fatal(err)
	}
	<-copyStarted
	running := manager.Snapshot()
	if running.TotalUnits != 10 || running.CompletedUnits != 4 {
		t.Fatalf("running progress = %#v", running)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	status := manager.Snapshot()
	if status.ID != started.ID || status.State != StateCanceled ||
		status.Category != "canceled" || status.Validate() != nil {
		t.Fatalf("canceled = %#v", status)
	}
}

func TestManagerRejectsUnavailableLifecycle(t *testing.T) {
	if _, err := (*Manager)(nil).Start(context.Background(), "x"); err == nil {
		t.Fatal("nil manager start succeeded")
	}
	if err := (*Manager)(nil).Run(context.Background()); err == nil {
		t.Fatal("nil manager run succeeded")
	}
	fixture := newBackupFixture(t)
	manager := NewManager(fixture.service)
	if _, err := manager.Start(context.Background(), "x"); err == nil {
		t.Fatal("manager accepted work before Run")
	}
	if err := manager.Run(nil); err == nil {
		t.Fatal("nil manager context succeeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	<-manager.Ready()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), "x"); err == nil {
		t.Fatal("closed manager accepted work")
	}
}

func waitBackupStatus(t *testing.T, manager *Manager, id uint64) Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status := manager.Snapshot()
		if status.ID == id && status.State != StateRunning {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("backup did not complete: %#v", status)
		}
		time.Sleep(time.Millisecond)
	}
}
