package logs

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestHistoryOrdersAndPaginatesEqualTimestamps(t *testing.T) {
	fixture := newHistoryFixture(t)
	query := fixture.query()
	query.Limit = 1

	var ids []int64
	for {
		page, err := fixture.store.History(context.Background(), query)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Events) != 1 {
			t.Fatalf("page events = %d", len(page.Events))
		}
		ids = append(ids, page.Events[0].ID)
		if !page.HasMore {
			if page.NextCursor != "" {
				t.Fatal("terminal page returned a cursor")
			}
			break
		}
		if page.NextCursor == "" {
			t.Fatal("nonterminal page omitted cursor")
		}
		query.Cursor = page.NextCursor
	}
	if want := []int64{3, 2, 1}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("older IDs = %v, want %v", ids, want)
	}

	newer := fixture.query()
	newer.Direction = DirectionNewer
	newer.Limit = 1
	cursor, err := fixture.codec.Encode(newer, 1_000_000, 1)
	if err != nil {
		t.Fatal(err)
	}
	newer.Cursor = cursor
	first, err := fixture.store.History(context.Background(), newer)
	if err != nil {
		t.Fatal(err)
	}
	if got := historyIDs(first.Events); !reflect.DeepEqual(got, []int64{2}) || !first.HasMore {
		t.Fatalf("first newer page = %v, more=%v", got, first.HasMore)
	}
	newer.Cursor = first.NextCursor
	second, err := fixture.store.History(context.Background(), newer)
	if err != nil {
		t.Fatal(err)
	}
	if got := historyIDs(second.Events); !reflect.DeepEqual(got, []int64{3}) || second.HasMore {
		t.Fatalf("second newer page = %v, more=%v", got, second.HasMore)
	}
}

func TestHistoryComposesEveryFilter(t *testing.T) {
	fixture := newHistoryFixture(t)
	status := int64(503)
	query := fixture.query()
	query.ServerID = 1
	query.Project = "project"
	query.Environment = "production"
	query.Application = "api"
	query.Service = "web"
	query.ContainerID = 1
	query.Levels = []Level{LevelDebug, LevelError}
	query.Streams = []Stream{StreamStdout, StreamStderr}
	query.Contains = `%_\`
	query.Excludes = "health"
	query.RequestID = "request-1"
	query.Logger = "http"
	query.HTTPMethod = "POST"
	query.HTTPStatus = &status
	query.ErrorType = "temporary"

	page, err := fixture.store.History(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if got := historyIDs(page.Events); !reflect.DeepEqual(got, []int64{1}) {
		t.Fatalf("filtered IDs = %v", got)
	}
	event := page.Events[0]
	if event.ContainerID == nil || *event.ContainerID != "container-1" ||
		event.OriginalLevel == nil || *event.OriginalLevel != "ERROR" ||
		string(event.AttributesJSON) != `{"nested":{"safe":true}}` ||
		event.DurationMS == nil || *event.DurationMS != 12.5 {
		t.Fatalf("optional fields were not scanned: %#v", event)
	}
}

func TestHistoryLiteralSearchUsesASCIIFoldingOnly(t *testing.T) {
	fixture := newHistoryFixture(t)
	cases := map[string]struct {
		contains string
		want     []int64
	}{
		"ASCII fold":        {"error", []int64{1}},
		"wildcards literal": {`100%_\`, []int64{1}},
		"matching UTF-8":    {"café", []int64{2, 1}},
		"distinct UTF-8":    {"CAFÉ", []int64{}},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			query := fixture.query()
			query.Contains = test.contains
			page, err := fixture.store.History(context.Background(), query)
			if err != nil {
				t.Fatal(err)
			}
			if got := historyIDs(page.Events); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("IDs = %v, want %v", got, test.want)
			}
		})
	}
}

func TestHistoryOptionalFieldsAndEventLookup(t *testing.T) {
	fixture := newHistoryFixture(t)
	query := fixture.query()
	query.RequestID = "absent"
	page, err := fixture.store.History(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 0 {
		t.Fatal("NULL request ID matched an exact filter")
	}

	event, err := fixture.store.Event(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if event.ContainerInstanceID != nil || event.ContainerID != nil ||
		event.Logger != nil || event.HTTPStatus != nil || event.AttributesJSON != nil {
		t.Fatalf("NULL fields were not preserved: %#v", event)
	}
	if string(event.MessageRaw) != `{"log":"plain café"}` {
		t.Fatalf("raw payload = %q", event.MessageRaw)
	}
	if _, err := fixture.store.Event(context.Background(), 999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing event error = %v", err)
	}
}

func TestHistoryCatalogCascadesAndIncludesInactiveSources(t *testing.T) {
	fixture := newHistoryFixture(t)
	all, err := fixture.store.Catalog(context.Background(), SourceScope{})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(all.Servers); got != 2 {
		t.Fatalf("servers = %d", got)
	}
	if !containsSourceOption(all.Projects, "inactive-project") {
		t.Fatalf("inactive source missing from projects: %#v", all.Projects)
	}

	scoped, err := fixture.store.Catalog(context.Background(), SourceScope{
		ServerID: 1, Project: "project", Environment: "production", Application: "api",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sourceOptionValues(scoped.Services), []string{"web"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("services = %v, want %v", got, want)
	}
	if len(scoped.Containers) != 1 || scoped.Containers[0].ID != 1 ||
		scoped.Containers[0].Label != "api-1" {
		t.Fatalf("containers = %#v", scoped.Containers)
	}
}

func TestHistoryRejectsInvalidCursorAndHonorsCancellation(t *testing.T) {
	fixture := newHistoryFixture(t)
	query := fixture.query()
	query.Cursor = `<script type="text/html">`
	if _, err := fixture.store.History(context.Background(), query); !errors.Is(err, ErrInvalidHistoryCursor) {
		t.Fatalf("hostile cursor error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	query.Cursor = ""
	if _, err := fixture.store.History(ctx, query); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled query error = %v", err)
	}
	if _, err := fixture.store.Catalog(ctx, SourceScope{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled catalog error = %v", err)
	}
}

func TestHistoryQueryPlansUseExistingTimeIndexes(t *testing.T) {
	fixture := newHistoryFixture(t)
	cases := map[string]struct {
		query     HistoryQuery
		wantIndex string
	}{
		"unfiltered": {fixture.query(), "log_events_time_idx"},
		"source level": {
			func() HistoryQuery {
				query := fixture.query()
				query.ServerID = 1
				query.Project = "project"
				query.Environment = "production"
				query.Application = "api"
				query.Service = "web"
				query.Levels = []Level{LevelError}
				return query
			}(),
			"log_events_source_time_idx",
		},
		"container": {
			func() HistoryQuery {
				query := fixture.query()
				query.ContainerID = 1
				return query
			}(),
			"log_events_container_time_idx",
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			statement, arguments := buildHistorySQL(test.query, nil)
			details, err := historyQueryPlan(fixture.store.db, statement, arguments)
			if err != nil {
				t.Fatal(err)
			}
			t.Log(strings.Join(details, "\n"))
			if !strings.Contains(strings.Join(details, "\n"), test.wantIndex) {
				t.Fatalf("plan did not use %s:\n%s", test.wantIndex, strings.Join(details, "\n"))
			}
		})
	}
}

func historyQueryPlan(db *sql.DB, statement string, arguments []any) ([]string, error) {
	rows, err := db.QueryContext(
		context.Background(), "EXPLAIN QUERY PLAN "+statement, arguments...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			return nil, err
		}
		details = append(details, detail)
	}
	return details, rows.Err()
}

type historyFixture struct {
	store *Store
	codec *CursorCodec
}

func newHistoryFixture(t *testing.T) historyFixture {
	t.Helper()
	db, coordinator := cursorTestDatabase(t)
	codec, err := LoadCursorCodec(context.Background(), db.Reader(), coordinator)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Do(context.Background(), func(tx *sql.Tx) error {
		statements := []string{
			`INSERT INTO servers(id,name,created_at_us) VALUES
				(1,'Primary',1),(2,'Archive',1)`,
			`INSERT INTO sources(
				id,server_id,project_key,environment_key,application_key,service_key,
				project_label,environment_label,application_label,service_label,
				first_seen_at_us,last_seen_at_us
			) VALUES
				(1,1,'project','production','api','web','Project','Production','API','Web',1,3),
				(2,1,'project','production','worker','jobs','Project','Production','Worker','Jobs',1,3),
				(3,2,'inactive-project','old','retired','service','Inactive','Old','Retired','Service',1,1)`,
			`INSERT INTO container_instances(
				id,source_id,container_id,container_name,first_seen_at_us,last_seen_at_us
			) VALUES (1,1,'container-1','api-1',1,3)`,
			`INSERT INTO log_events(
				id,event_at_us,received_at_us,source_id,container_instance_id,
				stream,level_normalized,level_original,message_raw,message_text,
				attributes_json,source_event_id,logger,request_id,error_type,
				http_method,http_path,http_status,duration_ms
			) VALUES (
				1,1000000,1100000,1,1,
				'stderr','error','ERROR',cast('{"log":"Error 100%_\ Café"}' AS BLOB),'Error 100%_\ Café',
				'{"nested":{"safe":true}}','event-1','http','request-1','temporary',
				'POST','/items',503,12.5
			)`,
			`INSERT INTO log_events(
				id,event_at_us,received_at_us,source_id,container_instance_id,
				stream,level_normalized,level_original,message_raw,message_text,
				attributes_json,source_event_id,logger,request_id,error_type,
				http_method,http_path,http_status,duration_ms
			) VALUES (
				2,1000000,1200000,2,NULL,
				'stdout','info',NULL,cast('{"log":"plain café"}' AS BLOB),'plain café',
				NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL
			)`,
			`INSERT INTO log_events(
				id,event_at_us,received_at_us,source_id,container_instance_id,
				stream,level_normalized,level_original,message_raw,message_text,
				attributes_json,source_event_id,logger,request_id,error_type,
				http_method,http_path,http_status,duration_ms
			) VALUES (
				3,2000000,2100000,1,1,
				'stdout','warn',NULL,cast('{"log":"HEALTH timeout"}' AS BLOB),'HEALTH timeout',
				NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL
			)`,
		}
		for _, statement := range statements {
			if _, err := tx.Exec(statement); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return historyFixture{store: NewHistoryStore(db.Reader(), codec), codec: codec}
}

func (historyFixture) query() HistoryQuery {
	return HistoryQuery{
		FromUS:    0,
		ToUS:      3_000_000,
		Direction: DirectionOlder,
		Limit:     200,
	}
}

func historyIDs(events []HistoryEvent) []int64 {
	ids := make([]int64, len(events))
	for index, event := range events {
		ids[index] = event.ID
	}
	return ids
}

func containsSourceOption(options []SourceOption, value string) bool {
	for _, option := range options {
		if option.Value == value {
			return true
		}
	}
	return false
}

func sourceOptionValues(options []SourceOption) []string {
	values := make([]string, len(options))
	for index, option := range options {
		values[index] = option.Value
	}
	return values
}
