package logs

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drilonrecica/siftail/internal/database"
)

// History benchmark fixture: events arrive one millisecond apart (the
// sustained 1,000 events/second target), round-robin across four sources on
// two Servers, with messages of 80 to 399 bytes. Run with TMPDIR on a real
// disk (AGENTS.md §17.2); docs/performance/history-queries.md records results.
const (
	historyBenchmarkBaseUS = int64(1_700_000_000_000_000)
	historyBenchmarkStepUS = int64(1_000)
)

func BenchmarkHistoryStore100K(b *testing.B) {
	benchmarkHistoryStore(b, 100_000)
}

func BenchmarkHistoryStore1M(b *testing.B) {
	benchmarkHistoryStore(b, 1_000_000)
}

// BenchmarkHistoryStore10M is the pre-public 10-million-event plan and
// latency review (docs/performance/0.5.0-container.md). Seeding takes several
// minutes and about 5 GB of TMPDIR, so it runs only when
// SIFTAIL_BENCH_10M=1.
func BenchmarkHistoryStore10M(b *testing.B) {
	if os.Getenv("SIFTAIL_BENCH_10M") != "1" {
		b.Skip("set SIFTAIL_BENCH_10M=1 to seed and query 10,000,000 events")
	}
	benchmarkHistoryStore(b, 10_000_000)
}

func benchmarkHistoryStore(b *testing.B, eventCount int) {
	db, err := database.Open(context.Background(), filepath.Join(b.TempDir(), "siftail.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	if err := seedHistoryBenchmark(db.Writer(), eventCount); err != nil {
		b.Fatal(err)
	}
	codec, err := newCursorCodec([]byte("01234567890123456789012345678901"))
	if err != nil {
		b.Fatal(err)
	}
	store := NewHistoryStore(db.Reader(), codec)
	base := HistoryQuery{
		FromUS:    historyBenchmarkBaseUS,
		ToUS:      historyBenchmarkBaseUS + int64(eventCount+1)*historyBenchmarkStepUS,
		Direction: DirectionOlder,
		Limit:     200,
	}
	source := func(query HistoryQuery) HistoryQuery {
		query.ServerID = 1
		query.Project = "project"
		query.Environment = "production"
		query.Application = "api"
		query.Service = "web"
		return query
	}
	serverOnly := func(query HistoryQuery) HistoryQuery {
		query.ServerID = 1
		return query
	}
	// A deep cursor continues from a row 90% of the way to the oldest event,
	// as if the operator had paged nearly to the end of the range.
	deep := func(query HistoryQuery) HistoryQuery {
		n := int64(eventCount / 10)
		for !historyBenchmarkMatches(query, n) {
			n--
		}
		cursor, err := codec.Encode(query, historyBenchmarkBaseUS+n*historyBenchmarkStepUS, n)
		if err != nil {
			b.Fatal(err)
		}
		query.Cursor = cursor
		return query
	}
	// The equal-time case continues from the middle of the block of
	// historyBenchmarkEqualRows events that share one timestamp, so the
	// cursor's ID tie-break alone decides the page.
	equalTime := func(query HistoryQuery) HistoryQuery {
		at := historyBenchmarkEqualAtUS(eventCount)
		query.FromUS, query.ToUS = at, at+1
		cursor, err := codec.Encode(query, at,
			int64(eventCount)+historyBenchmarkEqualRows/2)
		if err != nil {
			b.Fatal(err)
		}
		query.Cursor = cursor
		return query
	}
	cases := []struct {
		name  string
		query HistoryQuery
	}{
		{"unfiltered", base},
		{"source_level", func() HistoryQuery {
			query := source(base)
			query.Levels = []Level{LevelError}
			return query
		}()},
		{"literal", func() HistoryQuery {
			query := base
			query.Contains = "needle"
			return query
		}()},
		{"server_only", serverOnly(base)},
		{"server_two_levels", func() HistoryQuery {
			query := serverOnly(base)
			query.Levels = []Level{LevelWarn, LevelError}
			return query
		}()},
		{"deep_unfiltered", deep(base)},
		{"deep_source", deep(source(base))},
		{"deep_server_only", deep(serverOnly(base))},
		{"request_id", func() HistoryQuery {
			query := base
			query.RequestID = fmt.Sprintf("req-%d", historyBenchmarkRequestN(eventCount))
			return query
		}()},
		{"equal_time", equalTime(base)},
	}
	for _, test := range cases {
		b.Run(test.name, func(b *testing.B) {
			var cursor *HistoryCursor
			if test.query.Cursor != "" {
				decoded, err := codec.Decode(test.query, test.query.Cursor)
				if err != nil {
					b.Fatal(err)
				}
				cursor = &decoded
			}
			statement, arguments := buildHistorySQL(test.query, cursor)
			plan, err := historyQueryPlan(db.Reader(), statement, arguments)
			if err != nil {
				b.Fatal(err)
			}
			b.Logf("rows=%d plan=%s", eventCount, strings.Join(plan, " | "))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				page, err := store.History(context.Background(), test.query)
				if err != nil {
					b.Fatal(err)
				}
				if len(page.Events) == 0 {
					b.Fatal("benchmark query returned no events")
				}
			}
		})
	}
	b.Run("detail", func(b *testing.B) {
		id := int64(eventCount / 2)
		plan, err := historyQueryPlan(db.Reader(),
			historyEventColumns+"\nWHERE e.id = ?", []any{id})
		if err != nil {
			b.Fatal(err)
		}
		b.Logf("rows=%d plan=%s", eventCount, strings.Join(plan, " | "))
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if _, err := store.Event(context.Background(), id); err != nil {
				b.Fatal(err)
			}
		}
	})
	// Export streams the newest minute of events, 60,000 rows (plus the
	// equal-time block when it falls inside), within the 100,000-row cap.
	b.Run("export_one_minute", func(b *testing.B) {
		query := base
		query.ToUS = historyBenchmarkBaseUS + int64(eventCount)*historyBenchmarkStepUS
		query.FromUS = query.ToUS - 60_000_000
		filter, arguments := buildHistoryFilterSQL(query, nil)
		plan, err := historyQueryPlan(db.Reader(), historyEventColumns+
			"\nWHERE "+filter+"\nORDER BY e.event_at_us DESC, e.id DESC\nLIMIT ?",
			append(arguments, 100_001))
		if err != nil {
			b.Fatal(err)
		}
		b.Logf("rows=%d plan=%s", eventCount, strings.Join(plan, " | "))
		want := 60_000
		if at := historyBenchmarkEqualAtUS(eventCount); at > query.FromUS && at < query.ToUS {
			want += historyBenchmarkEqualRows
		}
		exports := NewExportStore(db.Reader(), ExportLimits{})
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			result, err := exports.Export(context.Background(), query,
				ExportFormatNDJSON, io.Discard)
			if err != nil {
				b.Fatal(err)
			}
			if result.Rows != want {
				b.Fatalf("export rows = %d, want %d", result.Rows, want)
			}
		}
	})
}

// historyBenchmarkEqualRows events share one timestamp, the one of the
// middle event, so equal-time pagination is measured at every size.
const historyBenchmarkEqualRows = 300

func historyBenchmarkEqualAtUS(eventCount int) int64 {
	return historyBenchmarkBaseUS + int64(eventCount/2)*historyBenchmarkStepUS
}

// historyBenchmarkRequestN names a seeded event that carries a request ID
// (every seventh does) near the middle of the range.
func historyBenchmarkRequestN(eventCount int) int {
	return eventCount / 2 / 7 * 7
}

// historyBenchmarkMatches mirrors the seeded distribution for the hierarchy
// filters the benchmark uses, so a deep cursor names a row of its own query.
func historyBenchmarkMatches(query HistoryQuery, n int64) bool {
	source := n%4 + 1
	switch {
	case query.Service != "":
		return source == 1
	case query.ServerID == 1:
		return source <= 2
	default:
		return true
	}
}

func seedHistoryBenchmark(db *sql.DB, eventCount int) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`INSERT INTO servers(id,name,created_at_us)
		VALUES (1,'Benchmark',1),(2,'Neighbour',1)`); err != nil {
		return rollback(err)
	}
	if _, err := tx.Exec(`INSERT INTO sources(
		id,server_id,project_key,environment_key,application_key,service_key,
		project_label,environment_label,application_label,service_label,
		first_seen_at_us,last_seen_at_us
	) VALUES
		(1,1,'project','production','api','web','Project','Production','API','Web',1,2),
		(2,1,'project','production','worker','jobs','Project','Production','Worker','Jobs',1,2),
		(3,2,'shop','production','storefront','web','Shop','Production','Storefront','Web',1,2),
		(4,2,'shop','staging','storefront','web','Shop','Staging','Storefront','Web',1,2)`); err != nil {
		return rollback(err)
	}
	// Messages are a timestamped access-log-like prefix padded to 80-399
	// bytes; one in 1,000 contains the literal "needle".
	statement := fmt.Sprintf(`WITH RECURSIVE sequence(n) AS (
		VALUES(1)
		UNION ALL
		SELECT n + 1 FROM sequence WHERE n < %d
	), messages(n, text) AS (
		SELECT n, substr(
			printf('%%s request %%d GET /api/v1/items/%%d status=%%d duration_ms=%%d ',
				CASE WHEN n %% 1000 = 0 THEN 'needle' ELSE 'handled' END,
				n, n %% 9973, CASE WHEN n %% 10 = 0 THEN 503 ELSE 200 END, n %% 487) ||
			replace(hex(zeroblob(200)), '00', 'lorem ipsum '),
			1, 80 + (n * 37) %% 320)
		FROM sequence
	)
	INSERT INTO log_events(
		id,event_at_us,received_at_us,source_id,stream,level_normalized,
		message_raw,message_text,request_id
	)
	SELECT
		n,
		%d + n * %d,
		%d + n * %d,
		n %% 4 + 1,
		CASE WHEN n %% 3 = 0 THEN 'stderr' ELSE 'stdout' END,
		CASE WHEN n %% 10 = 0 THEN 'error' WHEN n %% 10 = 1 THEN 'warn' ELSE 'info' END,
		cast(text AS BLOB),
		text,
		CASE WHEN n %% 7 = 0 THEN 'req-' || n END
	FROM messages`, eventCount,
		historyBenchmarkBaseUS, historyBenchmarkStepUS,
		historyBenchmarkBaseUS, historyBenchmarkStepUS)
	if _, err := tx.Exec(statement); err != nil {
		return rollback(err)
	}
	// One block of events shares the middle event's timestamp; IDs follow
	// the main sequence so the block is the newest rows at that time.
	if _, err := tx.Exec(fmt.Sprintf(`WITH RECURSIVE sequence(n) AS (
		VALUES(1)
		UNION ALL
		SELECT n + 1 FROM sequence WHERE n < %d
	)
	INSERT INTO log_events(
		id,event_at_us,received_at_us,source_id,stream,level_normalized,
		message_raw,message_text
	)
	SELECT %d + n, %d, %d, 1, 'stdout', 'info',
		cast('equal-time event' AS BLOB), 'equal-time event'
	FROM sequence`, historyBenchmarkEqualRows, eventCount,
		historyBenchmarkEqualAtUS(eventCount),
		historyBenchmarkEqualAtUS(eventCount))); err != nil {
		return rollback(err)
	}
	return tx.Commit()
}
