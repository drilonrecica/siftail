package database

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"sync"
	"time"
)

// snapshotProgressInterval is how often SnapshotInto samples the staged file
// size while VACUUM INTO runs. Tests shorten it.
var snapshotProgressInterval = 250 * time.Millisecond

// SnapshotProgress reports staged bytes written against the source's logical
// size (in-use pages × page size) when the snapshot began. VACUUM INTO omits
// free pages, so Written approaches but need not reach Total until the final
// report, which sets Written to Total.
type SnapshotProgress struct {
	Written int64
	Total   int64
}

// SnapshotInto copies one consistent read snapshot of the database at
// sourcePath into destinationPath with VACUUM INTO on a dedicated connection
// (ADR 0009). The connection is opened read-write without create because
// SQLite opens the VACUUM INTO output with the source connection's flags, so a
// read-only or query_only connection cannot create it; the statement only
// reads the source.
// Concurrent commits never restart it: they are wholly absent from the copy.
// The destination must already exist as an exclusively created, empty regular
// file. SQLite does not fsync VACUUM INTO output, and this function never
// exposes the destination as a complete artifact; callers finalize, sync,
// verify, and publish it. Cancellation interrupts the statement and leaves a
// partial destination for the caller to remove.
func SnapshotInto(
	ctx context.Context,
	sourcePath string,
	destinationPath string,
	progress func(SnapshotProgress),
) error {
	if ctx == nil {
		return errors.New("snapshot context is nil")
	}
	if sourcePath == "" || destinationPath == "" {
		return errors.New("snapshot is unavailable")
	}
	info, err := os.Lstat(destinationPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		return errors.New("snapshot destination must be an empty regular file")
	}
	if _, err := os.Stat(sourcePath); err != nil {
		return errors.New("snapshot source is unavailable")
	}
	source, err := sql.Open("sqlite3", snapshotSourceDSN(sourcePath))
	if err != nil {
		return classify("open snapshot source", err)
	}
	source.SetMaxOpenConns(1)
	source.SetMaxIdleConns(0)
	defer source.Close()
	conn, err := source.Conn(ctx)
	if err != nil {
		return classify("reserve snapshot source connection", err)
	}
	defer conn.Close()

	total, err := logicalSize(ctx, conn)
	if err != nil {
		return err
	}
	report := func(written int64) {
		if progress == nil {
			return
		}
		if written > total {
			written = total
		}
		progress(SnapshotProgress{Written: written, Total: total})
	}
	report(0)

	stopSampling := make(chan struct{})
	var sampling sync.WaitGroup
	if progress != nil {
		sampling.Add(1)
		go func() {
			defer sampling.Done()
			ticker := time.NewTicker(snapshotProgressInterval)
			defer ticker.Stop()
			for {
				select {
				case <-stopSampling:
					return
				case <-ticker.C:
					if info, err := os.Stat(destinationPath); err == nil {
						report(info.Size())
					}
				}
			}
		}()
	}
	// The driver interrupts a running statement on cancellation, but an
	// interrupt issued before the statement starts is lost, so the context is
	// checked on both sides of the statement.
	if err = ctx.Err(); err == nil {
		_, err = conn.ExecContext(ctx, "VACUUM INTO ?", destinationPath)
	}
	close(stopSampling)
	sampling.Wait()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if err != nil {
		return classify("copy database snapshot", err)
	}
	report(total)
	return nil
}

func logicalSize(ctx context.Context, conn *sql.Conn) (int64, error) {
	var pageCount, freelistCount, pageSize int64
	if err := conn.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pageCount); err != nil {
		return 0, classify("measure snapshot source pages", err)
	}
	if err := conn.QueryRowContext(
		ctx, "PRAGMA freelist_count",
	).Scan(&freelistCount); err != nil {
		return 0, classify("measure snapshot source free pages", err)
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return 0, classify("measure snapshot source page size", err)
	}
	used := pageCount - freelistCount
	if used < 0 || pageSize <= 0 || used > int64(^uint64(0)>>1)/pageSize {
		return 0, errors.New("snapshot source size is unsupported")
	}
	return used * pageSize, nil
}

func snapshotSourceDSN(path string) string {
	return fileURI(path, url.Values{
		"mode":          {"rw"},
		"_busy_timeout": {"5000"},
	})
}
