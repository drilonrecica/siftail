package database

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOnlineBackupStepsAndHonorsCancellation(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Writer().Exec(`
		CREATE TABLE backup_bulk(value BLOB);
		INSERT INTO backup_bulk(value) VALUES(zeroblob(2097152));
	`); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "partial.sqlite")
	file, err := os.OpenFile(
		destination, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var progressCalls int
	err = OnlineBackup(ctx, db.Reader(), destination, 1, func(progress BackupProgress) {
		progressCalls++
		if progress.PageCount <= 1 || progress.Remaining <= 0 {
			t.Errorf("unexpected first progress = %#v", progress)
		}
		cancel()
	})
	if !errors.Is(err, context.Canceled) || progressCalls != 1 {
		t.Fatalf("canceled backup = %v, progress calls=%d", err, progressCalls)
	}
	if info, err := os.Stat(destination); err != nil || info.Size() == 0 {
		t.Fatalf("partial destination = %#v, %v", info, err)
	}
}

func TestOnlineBackupRejectsUnavailableInputs(t *testing.T) {
	if err := OnlineBackup(
		nil, nil, "", 0, nil,
	); err == nil {
		t.Fatal("nil online backup succeeded")
	}
	if err := OnlineBackup(
		context.Background(), nil, "destination", 0, nil,
	); err == nil {
		t.Fatal("nil source online backup succeeded")
	}
}
