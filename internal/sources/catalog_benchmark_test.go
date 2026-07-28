package sources

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/drilonrecica/siftail/internal/database"
)

func BenchmarkSourceCatalog10000(b *testing.B) {
	db, err := database.Open(context.Background(), filepath.Join(b.TempDir(), "siftail.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Writer().Begin()
	if err != nil {
		b.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO servers(id,name,created_at_us)
		VALUES (1,'Benchmark',1)`); err != nil {
		b.Fatal(err)
	}
	statement, err := tx.Prepare(`INSERT INTO sources(
		id,server_id,project_key,environment_key,application_key,service_key,
		project_label,environment_label,application_label,service_label,
		first_seen_at_us,last_seen_at_us
	) VALUES (?,1,?,?,?,?,?,?,?,?,1,1)`)
	if err != nil {
		b.Fatal(err)
	}
	for id := 1; id <= 10_000; id++ {
		project := fmt.Sprintf("project-%04d", id%100)
		application := fmt.Sprintf("application-%05d", id)
		if _, err := statement.Exec(
			id, project, "production", application, "web",
			project, "Production", application, "Web",
		); err != nil {
			b.Fatal(err)
		}
	}
	if err := statement.Close(); err != nil {
		b.Fatal(err)
	}
	containerStatement, err := tx.Prepare(`INSERT INTO container_instances(
		id,source_id,container_name,first_seen_at_us,last_seen_at_us
	) VALUES (?,?,?,1,1)`)
	if err != nil {
		b.Fatal(err)
	}
	eventStatement, err := tx.Prepare(`INSERT INTO log_events(
		id,event_at_us,received_at_us,source_id,container_instance_id,
		stream,level_normalized,message_raw,message_text
	) VALUES (?,?,?,?,?,'stdout','info',x'62656e63686d61726b','benchmark')`)
	if err != nil {
		b.Fatal(err)
	}
	for id := 10; id <= 10_000; id += 10 {
		if _, err := containerStatement.Exec(
			id, id, fmt.Sprintf("container-%05d", id),
		); err != nil {
			b.Fatal(err)
		}
		if _, err := eventStatement.Exec(id, id, id, id, id); err != nil {
			b.Fatal(err)
		}
	}
	if err := containerStatement.Close(); err != nil {
		b.Fatal(err)
	}
	if err := eventStatement.Close(); err != nil {
		b.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	store := NewStore(db.Reader())

	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		after := int64((index * DefaultCatalogLimit) % 9_800)
		page, err := store.Catalog(context.Background(), CatalogQuery{
			AfterID: after, Limit: DefaultCatalogLimit,
		})
		if err != nil {
			b.Fatal(err)
		}
		if len(page.Sources) != DefaultCatalogLimit || !page.HasMore {
			b.Fatalf("page after %d = %#v", after, page)
		}
	}
}
