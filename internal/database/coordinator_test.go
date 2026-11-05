package database

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCoordinatorSerializesAndCommitsMutations(t *testing.T) {
	db := openTestDB(t)
	coordinator, cancel, done := runTestCoordinator(t, db)
	if got := cap(coordinator.operations); got != 64 {
		t.Fatalf("operation capacity = %d", got)
	}
	defer func() {
		coordinator.Close()
		cancel()
		<-done
	}()

	var active atomic.Int64
	var maximum atomic.Int64
	var workers sync.WaitGroup
	for i := 0; i < 24; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			err := coordinator.Do(context.Background(), func(tx *sql.Tx) error {
				current := active.Add(1)
				for {
					seen := maximum.Load()
					if current <= seen || maximum.CompareAndSwap(seen, current) {
						break
					}
				}
				time.Sleep(time.Millisecond)
				_, err := tx.Exec("INSERT INTO settings(key,value_json,updated_at_us) VALUES(lower(hex(randomblob(8))),'{}',1)")
				active.Add(-1)
				return err
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	if got := maximum.Load(); got != 1 {
		t.Fatalf("maximum concurrent mutations = %d", got)
	}
	var count int
	if err := db.Reader().QueryRow("SELECT count(*) FROM settings").Scan(&count); err != nil || count != 24 {
		t.Fatalf("settings count = %d, err=%v", count, err)
	}
}

func TestCoordinatorRollsBackAndDrainsAcceptedWork(t *testing.T) {
	db := openTestDB(t)
	coordinator, cancel, done := runTestCoordinator(t, db)

	sentinel := errors.New("reject mutation")
	err := coordinator.Do(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO settings(key,value_json,updated_at_us) VALUES('rollback','{}',1)"); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("mutation error = %v", err)
	}
	var count int
	if err := db.Reader().QueryRow("SELECT count(*) FROM settings WHERE key='rollback'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("rolled-back rows = %d, err=%v", count, err)
	}

	coordinator.Close()
	if err := coordinator.Do(context.Background(), func(*sql.Tx) error { return nil }); !errors.Is(err, ErrCoordinatorClosed) {
		t.Fatalf("closed coordinator error = %v", err)
	}
	cancel()
	<-done
}

func TestCoordinatorSerializesBoundedMaintenanceWithMutations(t *testing.T) {
	db := openTestDB(t)
	coordinator, cancel, done := runTestCoordinator(t, db)
	defer func() {
		coordinator.Close()
		cancel()
		<-done
	}()

	if err := coordinator.Do(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO settings(key,value_json,updated_at_us)
			VALUES('before-maintenance','{}',1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.DoMaintenance(context.Background(), func(writer *sql.DB) error {
		var count int
		if err := writer.QueryRow(
			"SELECT count(*) FROM settings WHERE key='before-maintenance'",
		).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return errors.New("maintenance ran before prior mutation commit")
		}
		_, err := writer.Exec("PRAGMA incremental_vacuum(1)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.DoMaintenance(
		context.Background(), nil,
	); err == nil {
		t.Fatal("nil maintenance operation accepted")
	}
}

func runTestCoordinator(t *testing.T, db *DB) (*Coordinator, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	coordinator := NewCoordinator(db.Writer())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	select {
	case <-coordinator.Ready():
	case <-time.After(time.Second):
		t.Fatal("coordinator did not start")
	}
	return coordinator, cancel, done
}

// An operation that passed admission commits even when its caller gives up,
// so Do must report that commit rather than the caller's cancellation.
func TestCoordinatorReportsCommitForAdmittedCanceledOperation(t *testing.T) {
	db := openTestDB(t)
	coordinator, cancel, done := runTestCoordinator(t, db)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	// Unblock before shutdown on every path, or a failed assertion would
	// leave Run parked inside the blocking operation.
	defer func() {
		unblock()
		coordinator.Close()
		cancel()
		<-done
	}()

	blocking := make(chan error, 1)
	running := make(chan struct{})
	go func() {
		blocking <- coordinator.Do(context.Background(), func(*sql.Tx) error {
			close(running)
			<-release
			return nil
		})
	}()
	<-running
	ctx, cancelCaller := context.WithCancel(context.Background())
	admitted := make(chan error, 1)
	go func() {
		admitted <- coordinator.Do(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec("INSERT INTO settings(key,value_json,updated_at_us) VALUES('admitted','{}',1)")
			return err
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(coordinator.operations) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("second operation was not admitted")
		}
		time.Sleep(time.Millisecond)
	}
	cancelCaller()
	select {
	case err := <-admitted:
		t.Fatalf("admitted operation returned %v before it ran", err)
	case <-time.After(50 * time.Millisecond):
	}
	unblock()
	if err := <-blocking; err != nil {
		t.Fatal(err)
	}
	if err := <-admitted; err != nil {
		t.Fatalf("committed operation reported %v", err)
	}
	var count int
	if err := db.Reader().QueryRow("SELECT count(*) FROM settings WHERE key='admitted'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("admitted rows = %d, err=%v", count, err)
	}
}

// Close racing with submitters must leave no admitted operation unexecuted:
// every Do either runs its operation and returns its result, or is refused
// with ErrCoordinatorClosed, and none waits forever.
func TestCoordinatorCloseNeverStrandsAdmittedOperations(t *testing.T) {
	db := openTestDB(t)
	for iteration := 0; iteration < 50; iteration++ {
		coordinator, cancel, done := runTestCoordinator(t, db)
		var executed, succeeded atomic.Int64
		var submitters sync.WaitGroup
		for i := 0; i < 32; i++ {
			submitters.Add(1)
			go func() {
				defer submitters.Done()
				err := coordinator.Do(context.Background(), func(*sql.Tx) error {
					executed.Add(1)
					return nil
				})
				switch {
				case err == nil:
					succeeded.Add(1)
				case !errors.Is(err, ErrCoordinatorClosed):
					t.Error(err)
				}
			}()
		}
		if iteration%2 == 0 {
			coordinator.Close()
		} else {
			cancel()
		}
		finished := make(chan struct{})
		go func() { submitters.Wait(); close(finished) }()
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Fatalf("iteration %d: a submitter never received a result", iteration)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if executed.Load() != succeeded.Load() {
			t.Fatalf("iteration %d: executed %d operations, reported %d", iteration, executed.Load(), succeeded.Load())
		}
	}
}
