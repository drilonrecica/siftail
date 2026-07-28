package database

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/mattn/go-sqlite3"
)

const DefaultBackupStepPages = 256

type BackupProgress struct {
	PageCount int
	Remaining int
}

// OnlineBackup copies one consistent snapshot through SQLite's online backup
// API. The destination must already exist as an exclusively created regular
// file and is never exposed as a complete artifact by this function.
func OnlineBackup(
	ctx context.Context,
	source *sql.DB,
	destinationPath string,
	stepPages int,
	progress func(BackupProgress),
) error {
	if ctx == nil {
		return errors.New("online backup context is nil")
	}
	if source == nil || destinationPath == "" {
		return errors.New("online backup is unavailable")
	}
	if stepPages <= 0 {
		stepPages = DefaultBackupStepPages
	}
	sourceConn, err := source.Conn(ctx)
	if err != nil {
		return classify("reserve backup source connection", err)
	}
	defer sourceConn.Close()

	destination, err := sql.Open("sqlite3", dsn(destinationPath, false))
	if err != nil {
		return classify("open backup destination", err)
	}
	destination.SetMaxOpenConns(1)
	destination.SetMaxIdleConns(1)
	defer destination.Close()
	if err := destination.PingContext(ctx); err != nil {
		return classify("connect backup destination", err)
	}
	destinationConn, err := destination.Conn(ctx)
	if err != nil {
		return classify("reserve backup destination connection", err)
	}
	defer destinationConn.Close()

	err = destinationConn.Raw(func(destinationDriver any) error {
		destinationSQLite, ok := destinationDriver.(*sqlite3.SQLiteConn)
		if !ok {
			return errors.New("backup destination is not SQLite")
		}
		return sourceConn.Raw(func(sourceDriver any) error {
			sourceSQLite, ok := sourceDriver.(*sqlite3.SQLiteConn)
			if !ok {
				return errors.New("backup source is not SQLite")
			}
			operation, err := destinationSQLite.Backup(
				"main", sourceSQLite, "main",
			)
			if err != nil {
				return classify("initialize online backup", err)
			}
			closed := false
			defer func() {
				if !closed {
					_ = operation.Close()
				}
			}()
			for {
				if err := ctx.Err(); err != nil {
					return err
				}
				done, err := operation.Step(stepPages)
				if err != nil {
					return classify("copy online backup pages", err)
				}
				if progress != nil {
					progress(BackupProgress{
						PageCount: operation.PageCount(),
						Remaining: operation.Remaining(),
					})
				}
				if done {
					if err := operation.Close(); err != nil {
						return classify("finish online backup", err)
					}
					closed = true
					return nil
				}
				timer := time.NewTimer(time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
		})
	})
	if err != nil {
		return err
	}
	return nil
}
