package sources

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
)

func TestSourceCatalogIncludesLifecycleHierarchyAndRetainedFacts(t *testing.T) {
	db, store, now := sourceCatalogFixture(t)

	first, err := store.Catalog(context.Background(), CatalogQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !first.HasMore || first.NextAfter != 2 || len(first.Sources) != 2 {
		t.Fatalf("first page = %#v", first)
	}
	active := first.Sources[0]
	if active.ID != 1 || active.ServerID != 1 || active.ServerName != "Alpha" ||
		active.ServerHostname != "" || active.ProjectKey != "project-a" ||
		active.ProjectLabel != "Project A" || active.DisplayName() != "Public API" ||
		!active.Active || active.CleanupEligible || !active.HasRetainedEvents ||
		active.LatestRetainedAtUS == nil || *active.LatestRetainedAtUS != now.Add(-time.Minute).UnixMicro() ||
		!active.HasContainerHistory {
		t.Fatalf("active source = %#v", active)
	}
	inactive := first.Sources[1]
	if inactive.ID != 2 || inactive.ServerID != 1 || inactive.Active ||
		inactive.CleanupEligible || inactive.HasRetainedEvents ||
		inactive.HasContainerHistory || inactive.Alias != nil {
		t.Fatalf("inactive source = %#v", inactive)
	}

	second, err := store.Catalog(context.Background(), CatalogQuery{
		AfterID: first.NextAfter, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.HasMore || len(second.Sources) != 1 ||
		second.Sources[0].ServerID != 2 || second.Sources[0].ServerName != "Beta" ||
		!second.Sources[0].CleanupEligible {
		t.Fatalf("second page = %#v", second)
	}

	var sourceServer int64
	if err := db.Reader().QueryRow(`SELECT server_id FROM sources WHERE id=3`).Scan(&sourceServer); err != nil {
		t.Fatal(err)
	}
	if sourceServer != second.Sources[0].ServerID {
		t.Fatal("catalog crossed the source's trusted Server relationship")
	}
}

func TestSourceDetailKeepsContainersAsBoundedObservations(t *testing.T) {
	db, store, _ := sourceCatalogFixture(t)
	detail, err := store.SourceDetail(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ContainersTruncated || len(detail.Containers) != 2 {
		t.Fatalf("containers = %#v", detail)
	}
	got := []string{
		detail.Containers[0].ContainerID + "/" + detail.Containers[0].ContainerName,
		detail.Containers[1].ContainerID + "/" + detail.Containers[1].ContainerName,
	}
	if want := []string{"/api-current", "old-id/"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("container observations = %#v, want %#v", got, want)
	}
	if !detail.Containers[0].Active || detail.Containers[1].Active {
		t.Fatalf("container lifecycle = %#v", detail.Containers)
	}
	tx, err := db.Writer().Begin()
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index <= MaxDetailContainers; index++ {
		if _, err := tx.Exec(`INSERT INTO container_instances(
			id,source_id,container_name,first_seen_at_us,last_seen_at_us
		) VALUES (?,2,?,1,?)`, index+10, fmt.Sprintf("worker-%03d", index), index+10); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	bounded, err := store.SourceDetail(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if !bounded.ContainersTruncated || len(bounded.Containers) != MaxDetailContainers {
		t.Fatalf("bounded containers = %d, truncated=%t",
			len(bounded.Containers), bounded.ContainersTruncated)
	}
	if _, err := store.SourceDetail(context.Background(), 404); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("missing source error = %v", err)
	}
}

func TestSourceCatalogBoundsCancellationAndQueryPlan(t *testing.T) {
	db, store, _ := sourceCatalogFixture(t)
	for _, query := range []CatalogQuery{
		{AfterID: -1}, {Limit: -1}, {Limit: MaxCatalogLimit + 1},
	} {
		if _, err := store.Catalog(context.Background(), query); err == nil {
			t.Fatalf("query %#v accepted", query)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Catalog(ctx, CatalogQuery{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled catalog error = %v", err)
	}
	if _, err := store.SourceDetail(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled detail error = %v", err)
	}

	rows, err := db.Reader().QueryContext(context.Background(),
		"EXPLAIN QUERY PLAN "+catalogSourceSQL+`
		WHERE source.id > ?
		ORDER BY source.id
		LIMIT ?`, 0, DefaultCatalogLimit+1)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"SEARCH source USING INTEGER PRIMARY KEY (rowid>?)",
		"SEARCH server USING INTEGER PRIMARY KEY (rowid=?)",
		"log_events_source_time_idx",
		"SEARCH container USING COVERING INDEX",
	} {
		if !strings.Contains(plan.String(), want) {
			t.Fatalf("catalog plan missing %q:\n%s", want, plan.String())
		}
	}
}

func sourceCatalogFixture(t *testing.T) (*database.DB, *Store, time.Time) {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	tx, err := db.Writer().Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO servers(id,name,hostname,created_at_us)
			VALUES (1,'Alpha',NULL,1),(2,'Beta','beta.example',1)`,
		`INSERT INTO sources(
			id,server_id,project_key,environment_key,application_key,service_key,
			project_label,environment_label,application_label,service_label,alias,
			first_seen_at_us,last_seen_at_us
		) VALUES
			(1,1,'project-a','production','api','web',
			 'Project A','Production','API','Web','Public API',1,?),
			(2,1,'project-a','production','worker','jobs',
			 'Project A','Production','Worker','Jobs',NULL,1,?),
			(3,2,'project-b','staging','api','web',
			 'Project B','Staging','API','Web',NULL,1,?)`,
		`INSERT INTO container_instances(
			id,source_id,container_id,container_name,first_seen_at_us,last_seen_at_us
		) VALUES
			(1,1,NULL,'api-current',1,?),
			(2,1,'old-id',NULL,1,?)`,
		`INSERT INTO log_events(
			id,event_at_us,received_at_us,source_id,container_instance_id,
			stream,level_normalized,message_raw,message_text
		) VALUES (1,?,?,1,1,'stdout','info',x'6f6b','ok')`,
	} {
		var args []any
		switch {
		case strings.Contains(statement, "INSERT INTO sources"):
			args = []any{
				now.Add(-time.Hour).UnixMicro(),
				now.Add(-25 * time.Hour).UnixMicro(),
				now.Add(-91 * 24 * time.Hour).UnixMicro(),
			}
		case strings.Contains(statement, "INSERT INTO container_instances"):
			args = []any{
				now.Add(-time.Hour).UnixMicro(),
				now.Add(-48 * time.Hour).UnixMicro(),
			}
		case strings.Contains(statement, "INSERT INTO log_events"):
			eventAt := now.Add(-time.Minute).UnixMicro()
			args = []any{eventAt, eventAt}
		}
		if _, err := tx.Exec(statement, args...); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db.Reader())
	store.now = func() time.Time { return now }
	return db, store, now
}
