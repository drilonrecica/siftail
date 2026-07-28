package audit

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
)

func BenchmarkAuditRecordAtCapacity(b *testing.B) {
	db, store, stop := auditBenchmarkFixture(b)
	defer stop()
	seedAuditBenchmark(b, db, MaxRecords)
	input := validInput()
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		input.OccurredAt = input.OccurredAt.Add(time.Microsecond)
		if _, err := store.Record(context.Background(), input); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAuditList100K(b *testing.B) {
	db, store, stop := auditBenchmarkFixture(b)
	defer stop()
	seedAuditBenchmark(b, db, MaxRecords)
	for _, benchmark := range []struct {
		name  string
		query Query
	}{
		{name: "newest", query: Query{Limit: 100}},
		{
			name:  "selective_oldest_category",
			query: Query{Category: CategoryExport, Limit: 100},
		},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			b.ReportAllocs()
			for index := 0; index < b.N; index++ {
				page, err := store.List(context.Background(), benchmark.query)
				if err != nil {
					b.Fatal(err)
				}
				if len(page.Events) == 0 {
					b.Fatal("audit benchmark returned no events")
				}
			}
		})
	}
}

func auditBenchmarkFixture(
	b *testing.B,
) (*database.DB, *Store, func()) {
	b.Helper()
	db, err := database.Open(context.Background(), b.TempDir()+"/siftail.db")
	if err != nil {
		b.Fatal(err)
	}
	coordinator := database.NewCoordinator(db.Writer())
	done := make(chan error, 1)
	go func() {
		done <- coordinator.Run(context.Background())
	}()
	<-coordinator.Ready()
	stop := func() {
		coordinator.Close()
		if err := <-done; err != nil {
			b.Errorf("coordinator shutdown: %v", err)
		}
		if err := db.Close(); err != nil {
			b.Errorf("database close: %v", err)
		}
	}
	return db, NewStore(db.Reader(), coordinator), stop
}

func seedAuditBenchmark(b *testing.B, db *database.DB, count int) {
	b.Helper()
	base := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC).UnixMicro()
	statement := fmt.Sprintf(`WITH RECURSIVE n(value) AS (
		SELECT 1 UNION ALL SELECT value+1 FROM n WHERE value < %d
	)
	INSERT INTO security_audit_events(
		id,occurred_at_us,category,action,outcome,actor_type,safe_metadata_json
	)
	SELECT value, ? + value,
		CASE WHEN value = 1 THEN 'export' ELSE 'authentication' END,
		CASE WHEN value = 1 THEN 'history.export' ELSE 'sign_in' END,
		'succeeded','system','{}'
	FROM n`, count)
	if _, err := db.Writer().Exec(statement, base); err != nil {
		b.Fatal(err)
	}
}
