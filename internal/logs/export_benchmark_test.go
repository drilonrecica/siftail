package logs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"github.com/drilonrecica/siftail/internal/database"
)

func BenchmarkExportTimeToFirstByte(b *testing.B) {
	db := newExportBenchmarkDatabase(b, 10_000)
	store := NewExportStore(db.Reader(), ExportLimits{})
	query := exportBenchmarkQuery()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		writer := &firstByteWriter{}
		result, err := store.Export(
			context.Background(), query, ExportFormatNDJSON, writer,
		)
		if err == nil || writer.bytes == 0 || result.Rows != 0 {
			b.Fatalf("first byte = %#v, %v, %d", result, err, writer.bytes)
		}
	}
}

func BenchmarkExportThroughput(b *testing.B) {
	for _, concurrentWrite := range []bool{false, true} {
		name := "read_only"
		if concurrentWrite {
			name = "with_committed_write"
		}
		b.Run(name, func(b *testing.B) {
			db := newExportBenchmarkDatabase(b, 10_000)
			store := NewExportStore(db.Reader(), ExportLimits{})
			query := exportBenchmarkQuery()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				var writeDone chan error
				if concurrentWrite {
					writeDone = make(chan error, 1)
					go func() {
						_, err := db.Writer().Exec(`INSERT INTO log_events(
							event_at_us,received_at_us,source_id,stream,
							level_normalized,message_raw,message_text
						) VALUES(1,1,1,'stdout','info',
							cast('concurrent' AS BLOB),'concurrent')`)
						writeDone <- err
					}()
				}
				result, err := store.Export(
					context.Background(), query,
					ExportFormatNDJSON, io.Discard,
				)
				if err != nil {
					b.Fatal(err)
				}
				if concurrentWrite {
					if err := <-writeDone; err != nil {
						b.Fatal(err)
					}
				}
				b.SetBytes(result.Bytes)
			}
		})
	}
}

type firstByteWriter struct {
	bytes int
}

func (w *firstByteWriter) Write(value []byte) (int, error) {
	w.bytes += len(value)
	return 0, errors.New("stop after first byte boundary")
}

func newExportBenchmarkDatabase(
	b *testing.B,
	eventCount int,
) *database.DB {
	b.Helper()
	db, err := database.Open(
		context.Background(), filepath.Join(b.TempDir(), "siftail.db"),
	)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := db.Close(); err != nil {
			b.Error(err)
		}
	})
	if err := seedExportBenchmark(db.Writer(), eventCount); err != nil {
		b.Fatal(err)
	}
	return db
}

func seedExportBenchmark(db *sql.DB, eventCount int) error {
	if eventCount < 1 {
		return errors.New("event count must be positive")
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		INSERT INTO servers(id,name,created_at_us)
		VALUES(1,'Benchmark',1);
		INSERT INTO sources(
			id,server_id,project_key,environment_key,application_key,service_key,
			project_label,environment_label,application_label,service_label,
			first_seen_at_us,last_seen_at_us
		) VALUES(1,1,'project','production','api','web',
			'Project','Production','API','Web',1,2)
	`); err != nil {
		return err
	}
	query := fmt.Sprintf(`WITH RECURSIVE sequence(n) AS (
		VALUES(1)
		UNION ALL SELECT n+1 FROM sequence WHERE n<%d
	)
	INSERT INTO log_events(
		id,event_at_us,received_at_us,source_id,stream,level_normalized,
		message_raw,message_text,attributes_json,logger,request_id
	)
	SELECT n,1700000000000000+n,1700000000000000+n,1,
		CASE WHEN n%%3=0 THEN 'stderr' ELSE 'stdout' END,
		CASE WHEN n%%10=0 THEN 'error' ELSE 'info' END,
		zeroblob(256),printf('event %%d %%s',n,
			'abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz'),
		'{"benchmark":true}','benchmark',printf('request-%%d',n)
	FROM sequence`, eventCount)
	if _, err := tx.Exec(query); err != nil {
		return err
	}
	return tx.Commit()
}

func exportBenchmarkQuery() HistoryQuery {
	return HistoryQuery{
		FromUS: 1699999999000000, ToUS: 1700000001000000,
		Direction: DirectionOlder, Limit: DefaultHistoryLimit,
	}
}
