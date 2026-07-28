package database

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMaintenanceLockSerializesAndRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	first, err := AcquireMaintenanceLock(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireMaintenanceLock(directory); !errors.Is(
		err, ErrMaintenanceActive,
	) {
		t.Fatalf("parallel lock = %v", err)
	}
	info, err := os.Stat(MaintenanceLockPath(directory))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("lock permissions = %#v, %v", info, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireMaintenanceLock(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}

	symlinkDirectory := t.TempDir()
	target := filepath.Join(symlinkDirectory, "target")
	if err := os.WriteFile(target, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		target, MaintenanceLockPath(symlinkDirectory),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireMaintenanceLock(symlinkDirectory); err == nil {
		t.Fatal("symlink maintenance lock accepted")
	}
}
