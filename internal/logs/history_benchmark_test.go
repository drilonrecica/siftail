package logs

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/drilonrecica/siftail/internal/database"
)

func BenchmarkHistoryStore100K(b *testing.B) {
	benchmarkHistoryStore(b, 100_000)
}

func BenchmarkHistoryStore1M(b *testing.B) {
	benchmarkHistoryStore(b, 1_000_000)
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
		FromUS:    1_700_000_000_000_000,
		ToUS:      1_700_000_000_000_000 + int64(eventCount) + 1,
		Direction: DirectionOlder,
		Limit:     200,
	}
	cases := map[string]HistoryQuery{
		"unfiltered": base,
		"source_level": func() HistoryQuery {
			query := base
			query.ServerID = 1
			query.Project = "project"
			query.Environment = "production"
			query.Application = "api"
			query.Service = "web"
			query.Levels = []Level{LevelError}
			return query
		}(),
		"literal": func() HistoryQuery {
			query := base
			query.Contains = "needle"
			return query
		}(),
	}
	for name, query := range cases {
		b.Run(name, func(b *testing.B) {
			statement, arguments := buildHistorySQL(query, nil)
			plan, err := historyQueryPlan(db.Reader(), statement, arguments)
			if err != nil {
				b.Fatal(err)
			}
			b.Logf("rows=%d plan=%v", eventCount, plan)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				page, err := store.History(context.Background(), query)
				if err != nil {
					b.Fatal(err)
				}
				if len(page.Events) == 0 {
					b.Fatal("benchmark query returned no events")
				}
			}
		})
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
		VALUES (1,'Benchmark',1)`); err != nil {
		return rollback(err)
	}
	if _, err := tx.Exec(`INSERT INTO sources(
		id,server_id,project_key,environment_key,application_key,service_key,
		project_label,environment_label,application_label,service_label,
		first_seen_at_us,last_seen_at_us
	) VALUES
		(1,1,'project','production','api','web','Project','Production','API','Web',1,2),
		(2,1,'project','production','worker','jobs','Project','Production','Worker','Jobs',1,2)`); err != nil {
		return rollback(err)
	}
	statement := fmt.Sprintf(`WITH RECURSIVE sequence(n) AS (
		VALUES(1)
		UNION ALL
		SELECT n + 1 FROM sequence WHERE n < %d
	)
	INSERT INTO log_events(
		id,event_at_us,received_at_us,source_id,stream,level_normalized,
		message_raw,message_text
	)
	SELECT
		n,
		1700000000000000 + n,
		1700000000000000 + n,
		CASE WHEN n %% 2 = 0 THEN 1 ELSE 2 END,
		CASE WHEN n %% 3 = 0 THEN 'stderr' ELSE 'stdout' END,
		CASE WHEN n %% 10 = 0 THEN 'error' ELSE 'info' END,
		cast(CASE WHEN n %% 1000 = 0 THEN 'needle event' ELSE 'ordinary event' END AS BLOB),
		CASE WHEN n %% 1000 = 0 THEN 'needle event' ELSE 'ordinary event' END
	FROM sequence`, eventCount)
	if _, err := tx.Exec(statement); err != nil {
		return rollback(err)
	}
	return tx.Commit()
}
