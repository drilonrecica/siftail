package database

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

const maintenanceLockName = ".siftail-maintenance.lock"

var ErrMaintenanceActive = errors.New("Siftail database is in use")

// MaintenanceLock prevents the server and destructive stopped-server
// maintenance commands from owning the database at the same time.
type MaintenanceLock struct {
	file *os.File
}

func MaintenanceLockPath(dataDirectory string) string {
	return filepath.Join(dataDirectory, maintenanceLockName)
}

func AcquireMaintenanceLock(dataDirectory string) (*MaintenanceLock, error) {
	if dataDirectory == "" {
		return nil, errors.New("maintenance lock directory is empty")
	}
	path := MaintenanceLockPath(dataDirectory)
	fd, err := syscall.Open(
		path,
		syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW,
		0600,
	)
	if err != nil {
		return nil, errors.New("open maintenance lock")
	}
	file := os.NewFile(uintptr(fd), path)
	fail := func(err error) (*MaintenanceLock, error) {
		_ = file.Close()
		return nil, err
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return fail(errors.New("inspect maintenance lock"))
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return fail(errors.New("maintenance lock is not a regular file"))
	}
	if err := syscall.Fchmod(fd, 0600); err != nil {
		return fail(errors.New("secure maintenance lock"))
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) ||
			errors.Is(err, syscall.EAGAIN) {
			return fail(ErrMaintenanceActive)
		}
		return fail(errors.New("acquire maintenance lock"))
	}
	return &MaintenanceLock{file: file}, nil
}

func (l *MaintenanceLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	var errs []error
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		errs = append(errs, fmt.Errorf("release maintenance lock: %w", err))
	}
	if err := file.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close maintenance lock: %w", err))
	}
	return errors.Join(errs...)
}
