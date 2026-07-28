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
