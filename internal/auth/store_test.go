package auth

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
)

func TestAdministratorCreateVerifyResetAndSingleAccount(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewStore(db.Writer())
	store.now = func() time.Time { return time.Unix(10, 0) }

	created, err := store.Create(context.Background(), "Admin", []byte("first-password"))
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != 1 || created.Username != "Admin" ||
		created.CreatedAtUS != time.Unix(10, 0).UnixMicro() {
		t.Fatalf("administrator = %#v", created)
	}
	if _, err := store.Create(context.Background(), "Other", []byte("other-password")); !errors.Is(err, ErrAdministratorExists) {
		t.Fatalf("second administrator error = %v", err)
	}
	administrator, matched, err := store.Verify(context.Background(), "Admin", []byte("first-password"))
	if err != nil || !matched || administrator.ID != 1 {
		t.Fatalf("verify = %#v/%v/%v", administrator, matched, err)
	}
	if _, matched, err := store.Verify(context.Background(), "Missing", []byte("first-password")); err != nil || matched {
		t.Fatalf("unknown username matched=%v err=%v", matched, err)
	}

	store.now = func() time.Time { return time.Unix(20, 0) }
	if err := store.ResetPassword(context.Background(), []byte("second-password")); err != nil {
		t.Fatal(err)
	}
	if _, matched, _ := store.Verify(context.Background(), "Admin", []byte("first-password")); matched {
		t.Fatal("old password remained valid")
	}
	if _, matched, err := store.Verify(context.Background(), "Admin", []byte("second-password")); err != nil || !matched {
		t.Fatalf("new password matched=%v err=%v", matched, err)
	}
	var changed int64
	if err := db.Reader().QueryRow("SELECT password_changed_at_us FROM administrators WHERE id=1").Scan(&changed); err != nil ||
		changed != time.Unix(20, 0).UnixMicro() {
		t.Fatalf("password changed at = %d, err=%v", changed, err)
	}
}

func TestAdministratorResetWithoutAccountRollsBack(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewStore(db.Writer())
	if err := store.ResetPassword(context.Background(), []byte("missing-admin")); !errors.Is(err, ErrAdministratorNotFound) {
		t.Fatalf("reset error = %v", err)
	}
	var count int
	if err := db.Reader().QueryRow("SELECT count(*) FROM administrators").Scan(&count); err != nil || count != 0 {
		t.Fatalf("administrator rows = %d, err=%v", count, err)
	}
}

func TestConcurrentAdministratorCreationEnforcesOne(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	coordinator := database.NewCoordinator(db.Writer())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	<-coordinator.Ready()
	defer func() {
		coordinator.Close()
		cancel()
		<-done
	}()
	store := NewCoordinatedStore(db.Reader(), coordinator)
	var successes int
	var lock sync.Mutex
	var group sync.WaitGroup
	for i := 0; i < 4; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := store.Create(context.Background(), "Admin", []byte("concurrent-password")); err == nil {
				lock.Lock()
				successes++
				lock.Unlock()
			} else if !errors.Is(err, ErrAdministratorExists) {
				t.Errorf("create error = %v", err)
			}
		}()
	}
	group.Wait()
	if successes != 1 {
		t.Fatalf("successful creations = %d", successes)
	}
}
