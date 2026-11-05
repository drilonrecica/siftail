package database

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

func TestInitialMigrationAndNoOpReopen(t *testing.T) {
	db := openTestDB(t)
	assertSchemaVersion(t, db.Writer(), MaxSchemaVersion)
	for _, table := range []string{"settings", "servers", "ingestion_tokens", "sources", "container_instances", "log_events", "administrators", "sessions", "security_audit_events"} {
		var found int
		if err := db.Writer().QueryRow("SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?", table).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if found != 1 {
			t.Errorf("missing table %s", table)
		}
	}
	if err := Migrate(context.Background(), db.Writer()); err != nil {
		t.Fatal(err)
	}
	assertSchemaVersion(t, db.Writer(), MaxSchemaVersion)
}

func TestAdministratorMigrationPreservesPreviousSchemaData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "previous.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(context.Background(), db, migrations[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO servers(id,name,created_at_us) VALUES(7,'preserved',1)"); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(context.Background(), db, migrations); err != nil {
		t.Fatal(err)
	}
	assertSchemaVersion(t, db, MaxSchemaVersion)
	var name string
	if err := db.QueryRow("SELECT name FROM servers WHERE id=7").Scan(&name); err != nil || name != "preserved" {
		t.Fatalf("preserved server = %q, err=%v", name, err)
	}
	var found int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='administrators'").Scan(&found); err != nil || found != 1 {
		t.Fatalf("administrator table = %d, err=%v", found, err)
	}
	if err := IntegrityCheck(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var tooNew *SchemaTooNewError
	if err := checkSchemaCompatible(context.Background(), db, 2); !errors.As(err, &tooNew) ||
		tooNew.Actual != MaxSchemaVersion || tooNew.Supported != 2 {
		t.Fatalf("older compatibility error = %#v", err)
	}
}

func TestSessionMigrationPreservesAdministrator(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema-two.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(context.Background(), db, migrations[:2]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO administrators(
		id,username,password_hash,created_at_us,password_changed_at_us
	) VALUES(1,'Admin',?,1,1)`, strings.Repeat("h", 64)); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(context.Background(), db, migrations); err != nil {
		t.Fatal(err)
	}
	assertSchemaVersion(t, db, MaxSchemaVersion)
	var username string
	if err := db.QueryRow("SELECT username FROM administrators WHERE id=1").Scan(&username); err != nil ||
		username != "Admin" {
		t.Fatalf("administrator = %q, err=%v", username, err)
	}
	var sessions int
	if err := db.QueryRow("SELECT count(*) FROM sessions").Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("sessions = %d, err=%v", sessions, err)
	}
}

func TestSecurityAuditMigrationPreservesSchemaThreeData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema-three.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(context.Background(), db, migrations[:3]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO administrators(
			id,username,password_hash,created_at_us,password_changed_at_us
		) VALUES(1,'Admin',?,1,1);
		INSERT INTO servers(id,name,created_at_us) VALUES(7,'preserved',1);
		INSERT INTO sources(
			id,server_id,project_key,environment_key,application_key,service_key,
			project_label,environment_label,application_label,service_label,
			first_seen_at_us,last_seen_at_us
		) VALUES(8,7,'p','e','a','s','p','e','a','s',1,1);
		INSERT INTO log_events(
			id,event_at_us,received_at_us,source_id,stream,level_normalized,
			message_raw,message_text
		) VALUES(9,10,11,8,'stdout','info',x'707265736572766564','preserved')`,
		strings.Repeat("h", 64)); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(context.Background(), db, migrations); err != nil {
		t.Fatal(err)
	}
	assertSchemaVersion(t, db, MaxSchemaVersion)
	var administrator, server, message string
	if err := db.QueryRow("SELECT username FROM administrators WHERE id=1").
		Scan(&administrator); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT name FROM servers WHERE id=7").
		Scan(&server); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT message_text FROM log_events WHERE id=9").
		Scan(&message); err != nil {
		t.Fatal(err)
	}
	if administrator != "Admin" || server != "preserved" || message != "preserved" {
		t.Fatalf("preserved values = %q, %q, %q", administrator, server, message)
	}
	var auditCount int
	if err := db.QueryRow("SELECT count(*) FROM security_audit_events").
		Scan(&auditCount); err != nil || auditCount != 0 {
		t.Fatalf("audit count = %d, err=%v", auditCount, err)
	}
	if err := IntegrityCheck(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var tooNew *SchemaTooNewError
	if err := checkSchemaCompatible(context.Background(), db, 3); !errors.As(err, &tooNew) ||
		tooNew.Actual != MaxSchemaVersion || tooNew.Supported != 3 {
		t.Fatalf("older compatibility error = %#v", err)
	}
}

func TestSecurityAuditSchemaConstraintsAndOrdering(t *testing.T) {
	db := openTestDB(t)
	insert := `INSERT INTO security_audit_events(
		occurred_at_us,category,action,outcome,actor_type,
		administrator_id,server_id,source_id,safe_metadata_json,request_id
	) VALUES(?,?,?,?,?,?,?,?,?,?)`
	if _, err := db.Writer().Exec(insert, 2, "authentication", "sign_in",
		"succeeded", "administrator", 1, nil, nil, `{}`, "request-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec(insert, 2, "session", "session.revoke",
		"failed", "system", nil, nil, nil, `{}`, nil); err != nil {
		t.Fatal(err)
	}
	for name, arguments := range map[string][]any{
		"timestamp": {0, "authentication", "sign_in", "succeeded",
			"system", nil, nil, nil, `{}`, nil},
		"category": {1, "unknown", "sign_in", "succeeded",
			"system", nil, nil, nil, `{}`, nil},
		"action": {1, "authentication", "", "succeeded",
			"system", nil, nil, nil, `{}`, nil},
		"outcome": {1, "authentication", "sign_in", "unknown",
			"system", nil, nil, nil, `{}`, nil},
		"actor": {1, "authentication", "sign_in", "succeeded",
			"unknown", nil, nil, nil, `{}`, nil},
		"identifier": {1, "authentication", "sign_in", "succeeded",
			"system", nil, 0, nil, `{}`, nil},
		"metadata-array": {1, "authentication", "sign_in", "succeeded",
			"system", nil, nil, nil, `[]`, nil},
		"metadata-size": {1, "authentication", "sign_in", "succeeded",
			"system", nil, nil, nil,
			`{"value":"` + strings.Repeat("x", 2048) + `"}`, nil},
		"request-size": {1, "authentication", "sign_in", "succeeded",
			"system", nil, nil, nil, `{}`, strings.Repeat("x", 129)},
	} {
		if _, err := db.Writer().Exec(insert, arguments...); err == nil {
			t.Errorf("%s constraint accepted", name)
		}
	}
	if _, err := db.Writer().Exec(
		"UPDATE security_audit_events SET outcome='failed' WHERE id=1",
	); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("immutable update error = %v", err)
	}
	rows, err := db.Writer().Query(`SELECT id FROM security_audit_events
		ORDER BY occurred_at_us DESC, id DESC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 2 || ids[0] != 2 || ids[1] != 1 {
		t.Fatalf("audit order = %v", ids)
	}
}

func TestMigrationRollback(t *testing.T) {
	db := openTestDB(t)
	_, err := db.Writer().Exec("DROP TABLE schema_migrations")
	if err != nil {
		t.Fatal(err)
	}
	bad := []migration{{version: 1, name: "0001_bad.sql", sql: "CREATE TABLE should_rollback(id INTEGER); THIS IS NOT SQL"}}
	if err := applyMigrations(context.Background(), db.Writer(), bad); err == nil {
		t.Fatal("invalid migration succeeded")
	}
	var found int
	if err := db.Writer().QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='should_rollback'").Scan(&found); err != nil {
		t.Fatal(err)
	}
	if found != 0 {
		t.Fatal("failed migration left a table behind")
	}
	assertSchemaVersion(t, db.Writer(), 0)
}

func TestMigrationListMustBeContiguous(t *testing.T) {
	files := fstest.MapFS{
		"migrations/0001_one.sql": {Data: []byte("SELECT 1")},
		"migrations/0003_gap.sql": {Data: []byte("SELECT 1")},
	}
	if _, err := loadMigrations(files); err == nil {
		t.Fatal("migration gap accepted")
	}
}

func TestInitialSchemaConstraintsAndOrdering(t *testing.T) {
	db := openTestDB(t)
	writer := db.Writer()
	result, err := writer.Exec("INSERT INTO servers(name, hostname, created_at_us) VALUES ('Prod', 'prod.example', 1)")
	if err != nil {
		t.Fatal(err)
	}
	serverID, _ := result.LastInsertId()
	result, err = writer.Exec(`INSERT INTO sources(
		server_id, project_key, environment_key, application_key, service_key,
		project_label, environment_label, application_label, service_label,
		first_seen_at_us, last_seen_at_us
	) VALUES (?, 'Project', 'production', 'App', 'api', 'Project', 'production', 'App', 'api', 1, 2)`, serverID)
	if err != nil {
		t.Fatal(err)
	}
	sourceID, _ := result.LastInsertId()
	container, err := writer.Exec(`INSERT INTO container_instances(
		source_id, container_id, container_name, first_seen_at_us, last_seen_at_us
	) VALUES (?, 'abc', 'app-1', 1, 2)`, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	containerID, _ := container.LastInsertId()

	insert := `INSERT INTO log_events(
		event_at_us, received_at_us, source_id, container_instance_id, stream,
		level_normalized, message_raw, message_text, attributes_json, source_event_id, request_id
	) VALUES (?, ?, ?, ?, 'stdout', 'info', ?, ?, '{"extra":true}', ?, ?)`
	if _, err := writer.Exec(insert, 20, 10, sourceID, containerID, []byte("later"), "later", "evt-2", "req-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(insert, 10, 30, sourceID, containerID, []byte("earlier"), "earlier", "evt-1", "req-1"); err != nil {
		t.Fatal(err)
	}
	var retention int64
	if err := writer.QueryRow("SELECT retention_at_us FROM log_events WHERE source_event_id='evt-2'").Scan(&retention); err != nil || retention != 10 {
		t.Fatalf("retention = %d, err=%v", retention, err)
	}

	rows, err := writer.Query("SELECT message_text FROM log_events ORDER BY event_at_us DESC, id DESC")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		got = append(got, value)
	}
	if len(got) != 2 || got[0] != "later" || got[1] != "earlier" {
		t.Fatalf("order = %v", got)
	}

	if _, err := writer.Exec("UPDATE log_events SET message_text='changed'"); err == nil {
		t.Fatal("immutable event updated")
	}
	if _, err := writer.Exec(insert, 40, 40, sourceID, containerID, []byte("duplicate"), "duplicate", "evt-1", nil); err == nil {
		t.Fatal("duplicate source event ID accepted")
	}
	if _, err := writer.Exec(insert, 40, 40, sourceID, containerID, []byte("bad"), "bad", "evt-3", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec("INSERT INTO log_events(event_at_us,received_at_us,source_id,stream,level_normalized,message_raw,message_text,attributes_json) VALUES(1,1,?,'pipe','info',x'00','x','[]')", sourceID); err == nil {
		t.Fatal("invalid stream/attributes accepted")
	}
	if err := IntegrityCheck(context.Background(), writer); err != nil {
		t.Fatal(err)
	}
}

func TestContainerMustBelongToEventSource(t *testing.T) {
	db := openTestDB(t)
	w := db.Writer()
	_, err := w.Exec(`PRAGMA foreign_keys=ON;
		INSERT INTO servers(id,name,created_at_us) VALUES(1,'s',1);
		INSERT INTO sources(id,server_id,project_key,environment_key,application_key,service_key,project_label,environment_label,application_label,service_label,first_seen_at_us,last_seen_at_us)
		VALUES(1,1,'p','e','a','x','p','e','a','x',1,1),(2,1,'p','e','a','y','p','e','a','y',1,1);
		INSERT INTO container_instances(id,source_id,container_id,first_seen_at_us,last_seen_at_us) VALUES(1,1,'c',1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.Exec("INSERT INTO log_events(event_at_us,received_at_us,source_id,container_instance_id,stream,level_normalized,message_raw,message_text) VALUES(1,1,2,1,'unknown','unknown',x'00','x')")
	var category *CategoryError
	if err == nil || !errors.As(classify("insert", err), &category) || category.Category != CategoryConstraint {
		t.Fatalf("cross-source container error = %v", err)
	}
}

func TestCriticalIndexesAreUsed(t *testing.T) {
	db := openTestDB(t)
	tests := []struct {
		query string
		index string
	}{
		{"SELECT id FROM log_events ORDER BY event_at_us DESC, id DESC LIMIT 10", "log_events_time_idx"},
		{"SELECT id FROM log_events WHERE source_id=1 ORDER BY event_at_us DESC, id DESC LIMIT 10", "log_events_source_time_idx"},
		{"SELECT id FROM log_events WHERE source_id=1 AND level_normalized='error' ORDER BY event_at_us DESC, id DESC LIMIT 10", "log_events_source_level_time_idx"},
		{"SELECT id FROM log_events WHERE request_id='r' ORDER BY event_at_us DESC, id DESC LIMIT 10", "log_events_request_time_idx"},
		{"SELECT id FROM log_events WHERE received_at_us < 100 ORDER BY received_at_us, id LIMIT 10", "log_events_received_idx"},
		{"SELECT id FROM log_events ORDER BY received_at_us, id LIMIT 10", "log_events_received_idx"},
		{"SELECT id FROM container_instances WHERE source_id=1 AND container_id='c' ORDER BY last_seen_at_us DESC, id DESC LIMIT 1", "container_instances_source_container_id_idx"},
		{"SELECT id FROM container_instances WHERE source_id=1 AND container_id IS NULL AND container_name='n' ORDER BY last_seen_at_us DESC, id DESC LIMIT 1", "container_instances_source_name_idx"},
	}
	for _, tt := range tests {
		var id, parent, unused int
		var detail string
		if err := db.Writer().QueryRow("EXPLAIN QUERY PLAN "+tt.query).Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if !contains(detail, tt.index) {
			t.Errorf("%s plan %q does not use %s", tt.query, detail, tt.index)
		}
	}
}

func TestContainerIdentityMigrationPreservesSchemaFourInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema-4.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if !migrations[4].foreignKeysOff || migrations[0].foreignKeysOff {
		t.Fatal("only migration 0005 runs with foreign keys disabled")
	}
	if err := applyMigrations(context.Background(), db, migrations[:4]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO servers(id,name,created_at_us) VALUES(1,'s',1);
		INSERT INTO sources(id,server_id,project_key,environment_key,application_key,service_key,project_label,environment_label,application_label,service_label,first_seen_at_us,last_seen_at_us)
		VALUES(1,1,'p','e','a','x','p','e','a','x',1,1);
		INSERT INTO container_instances(id,source_id,container_id,container_name,first_seen_at_us,last_seen_at_us) VALUES
			(7,1,'id-a','web-1',1,5),(8,1,NULL,'worker',2,6),(9,1,'id-c',NULL,3,7);
		INSERT INTO log_events(id,event_at_us,received_at_us,source_id,container_instance_id,stream,level_normalized,message_raw,message_text)
		VALUES(20,1,1,1,7,'stdout','info',x'61','a'),(21,1,1,1,8,'stdout','info',x'62','b')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO container_instances(source_id,container_id,container_name,first_seen_at_us,last_seen_at_us)
		VALUES(1,'id-new','web-1',8,8)`); err == nil {
		t.Fatal("schema 4 accepted a reused container name")
	}

	if err := applyMigrations(context.Background(), db, migrations); err != nil {
		t.Fatal(err)
	}
	assertSchemaVersion(t, db, MaxSchemaVersion)

	rows, err := db.Query(`SELECT id, source_id, coalesce(container_id,'-'), coalesce(container_name,'-'),
		first_seen_at_us, last_seen_at_us FROM container_instances ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var id, source, first, last int64
		var containerID, name string
		if err := rows.Scan(&id, &source, &containerID, &name, &first, &last); err != nil {
			t.Fatal(err)
		}
		got = append(got, strings.Join([]string{
			strconv.FormatInt(id, 10), strconv.FormatInt(source, 10), containerID, name,
			strconv.FormatInt(first, 10), strconv.FormatInt(last, 10),
		}, "|"))
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	want := []string{"7|1|id-a|web-1|1|5", "8|1|-|worker|2|6", "9|1|id-c|-|3|7"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("preserved container instances = %v, want %v", got, want)
	}
	var joined int
	if err := db.QueryRow(`SELECT count(*) FROM log_events e
		JOIN container_instances c ON c.id=e.container_instance_id AND c.source_id=e.source_id`).Scan(&joined); err != nil || joined != 2 {
		t.Fatalf("events still referencing preserved instances = %d, err=%v", joined, err)
	}

	var enabled int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("foreign keys after migration = %d, err=%v", enabled, err)
	}
	if _, err := db.Exec(`INSERT INTO container_instances(source_id,container_id,container_name,first_seen_at_us,last_seen_at_us)
		VALUES(1,'id-new','web-1',8,8),(1,'id-a','web-1-renamed-twin',8,8),(1,NULL,'worker',8,8)`); err != nil {
		t.Fatalf("producer metadata is still a uniqueness constraint: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO container_instances(source_id,first_seen_at_us,last_seen_at_us) VALUES(1,1,1)`); err == nil {
		t.Fatal("container instance without ID or name accepted")
	}
	if _, err := db.Exec(`INSERT INTO log_events(event_at_us,received_at_us,source_id,container_instance_id,stream,level_normalized,message_raw,message_text)
		VALUES(1,1,1,999,'stdout','info',x'63','c')`); err == nil {
		t.Fatal("event referencing a missing container instance accepted")
	}
	if _, err := db.Exec("DELETE FROM container_instances WHERE id=7"); err == nil {
		t.Fatal("referenced container instance deleted")
	}
	var uniques int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_index_list('container_instances') WHERE "unique"=1`).Scan(&uniques); err != nil || uniques != 1 {
		t.Fatalf("unique indexes on container_instances = %d (want only UNIQUE (id, source_id)), err=%v", uniques, err)
	}
	if err := IntegrityCheck(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	// The rebuilt table keeps ON DELETE CASCADE from its source.
	if _, err := db.Exec("DELETE FROM sources WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := db.QueryRow("SELECT count(*) FROM container_instances").Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("container instances after source cascade = %d, err=%v", remaining, err)
	}
}

func TestForeignKeysOffMigrationRefusesDanglingReference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dangling.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	migrations := []migration{
		{version: 1, name: "0001_parent.sql", sql: `CREATE TABLE parent(id INTEGER PRIMARY KEY) STRICT;
			CREATE TABLE child(id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parent(id)) STRICT;
			INSERT INTO parent VALUES(1); INSERT INTO child VALUES(1,1);`},
		{version: 2, name: "0002_dangling.sql", foreignKeysOff: true, sql: `CREATE TABLE parent_new(id INTEGER PRIMARY KEY) STRICT;
			DROP TABLE parent; ALTER TABLE parent_new RENAME TO parent;`},
	}
	err = applyMigrations(context.Background(), db, migrations)
	if err == nil || !strings.Contains(err.Error(), "foreign-key violation") {
		t.Fatalf("dangling-reference migration error = %v", err)
	}
	assertSchemaVersion(t, db, 1)
	var parents int
	if err := db.QueryRow("SELECT count(*) FROM parent WHERE id=1").Scan(&parents); err != nil || parents != 1 {
		t.Fatalf("rolled-back parent rows = %d, err=%v", parents, err)
	}
	var enabled int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("foreign keys after failed migration = %d, err=%v", enabled, err)
	}
}

func TestForeignKeysOffDirectiveMustBeFirstLine(t *testing.T) {
	files := fstest.MapFS{
		"migrations/0001_one.sql": {Data: []byte("-- siftail:foreign-keys-off\nSELECT 1")},
		"migrations/0002_two.sql": {Data: []byte("SELECT 1;\n-- siftail:foreign-keys-off\n")},
	}
	migrations, err := loadMigrations(files)
	if err != nil {
		t.Fatal(err)
	}
	if !migrations[0].foreignKeysOff || migrations[1].foreignKeysOff {
		t.Fatalf("directive parsing = %t %t", migrations[0].foreignKeysOff, migrations[1].foreignKeysOff)
	}
}

func assertSchemaVersion(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow("SELECT coalesce(max(version),0) FROM schema_migrations").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("schema version = %d, want %d", got, want)
	}
}

func contains(value, part string) bool {
	for i := 0; i+len(part) <= len(value); i++ {
		if value[i:i+len(part)] == part {
			return true
		}
	}
	return false
}

func TestEventTimeDefaultedMigrationBackfillsOnlyStableIDRowsWithEqualTimes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema-5.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if migrations[5].name != "0006_event_time_defaulted.sql" || migrations[5].foreignKeysOff {
		t.Fatalf("migration 6 = %q (foreign keys off %v)", migrations[5].name, migrations[5].foreignKeysOff)
	}
	if err := applyMigrations(context.Background(), db, migrations[:5]); err != nil {
		t.Fatal(err)
	}
	// 30: stable ID, equal times (defaulted in practice); 31: stable ID,
	// supplied time; 32: no stable ID, equal times (never read by dedup).
	if _, err := db.Exec(`INSERT INTO servers(id,name,created_at_us) VALUES(1,'s',1);
		INSERT INTO sources(id,server_id,project_key,environment_key,application_key,service_key,project_label,environment_label,application_label,service_label,first_seen_at_us,last_seen_at_us)
		VALUES(1,1,'p','e','a','x','p','e','a','x',1,1);
		INSERT INTO log_events(id,event_at_us,received_at_us,source_id,stream,level_normalized,message_raw,message_text,source_event_id)
		VALUES(30,500,500,1,'stdout','info',x'61','a','id-a'),
			(31,400,500,1,'stdout','info',x'62','b','id-b'),
			(32,500,500,1,'stdout','info',x'63','c',NULL)`); err != nil {
		t.Fatal(err)
	}

	if err := applyMigrations(context.Background(), db, migrations); err != nil {
		t.Fatal(err)
	}
	assertSchemaVersion(t, db, MaxSchemaVersion)
	rows, err := db.Query(`SELECT id, event_at_us, received_at_us, event_at_defaulted,
		message_text FROM log_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var id, eventAt, receivedAt, defaulted int64
		var message string
		if err := rows.Scan(&id, &eventAt, &receivedAt, &defaulted, &message); err != nil {
			t.Fatal(err)
		}
		got = append(got, strings.Join([]string{
			strconv.FormatInt(id, 10), strconv.FormatInt(eventAt, 10),
			strconv.FormatInt(receivedAt, 10), strconv.FormatInt(defaulted, 10), message,
		}, "|"))
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	want := []string{"30|500|500|1|a", "31|400|500|0|b", "32|500|500|0|c"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("migrated events = %v, want %v", got, want)
	}
	if _, err := db.Exec(`INSERT INTO log_events(event_at_us,event_at_defaulted,received_at_us,source_id,stream,level_normalized,message_raw,message_text)
		VALUES(1,2,1,1,'stdout','info',x'64','d')`); err == nil {
		t.Fatal("event_at_defaulted accepted a non-boolean value")
	}
	var defaultValue int
	if _, err := db.Exec(`INSERT INTO log_events(id,event_at_us,received_at_us,source_id,stream,level_normalized,message_raw,message_text)
		VALUES(33,1,1,1,'stdout','info',x'65','e')`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT event_at_defaulted FROM log_events WHERE id=33").Scan(&defaultValue); err != nil || defaultValue != 0 {
		t.Fatalf("default event_at_defaulted = %d, err=%v", defaultValue, err)
	}
	// The backfill recreated the immutability trigger.
	if _, err := db.Exec("UPDATE log_events SET event_at_defaulted=0 WHERE id=30"); err == nil {
		t.Fatal("migrated event lost immutability")
	}
	if err := IntegrityCheck(context.Background(), db); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionByReceiptMigrationReplacesRetentionIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema-6.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if migrations[6].name != "0007_retention_by_receipt.sql" || migrations[6].foreignKeysOff {
		t.Fatalf("migration 7 = %q (foreign keys off %v)", migrations[6].name, migrations[6].foreignKeysOff)
	}
	if err := applyMigrations(context.Background(), db, migrations[:6]); err != nil {
		t.Fatal(err)
	}
	// 40: late (event far before receipt); 41: future-skewed; 42: equal.
	if _, err := db.Exec(`INSERT INTO servers(id,name,created_at_us) VALUES(1,'s',1);
		INSERT INTO sources(id,server_id,project_key,environment_key,application_key,service_key,project_label,environment_label,application_label,service_label,first_seen_at_us,last_seen_at_us)
		VALUES(1,1,'p','e','a','x','p','e','a','x',1,1);
		INSERT INTO log_events(id,event_at_us,received_at_us,source_id,stream,level_normalized,message_raw,message_text)
		VALUES(40,100,900,1,'stdout','info',x'61','a'),
			(41,9000,500,1,'stdout','info',x'62','b'),
			(42,700,700,1,'stdout','info',x'63','c')`); err != nil {
		t.Fatal(err)
	}

	if err := applyMigrations(context.Background(), db, migrations); err != nil {
		t.Fatal(err)
	}
	assertSchemaVersion(t, db, MaxSchemaVersion)
	var indexes []string
	rows, err := db.Query(`SELECT name FROM sqlite_schema
		WHERE type='index' AND tbl_name='log_events' AND name IN
		('log_events_received_idx','log_events_retention_idx') ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		indexes = append(indexes, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(indexes, ",") != "log_events_received_idx" {
		t.Fatalf("retention indexes after migration = %v", indexes)
	}
	// The deprecated generated column stays and keeps its old meaning.
	var retentionAt int64
	if err := db.QueryRow("SELECT retention_at_us FROM log_events WHERE id=40").Scan(&retentionAt); err != nil || retentionAt != 100 {
		t.Fatalf("deprecated retention_at_us = %d, err=%v", retentionAt, err)
	}
	// Deletion order is receipt order, independent of claimed event time.
	order, err := db.Query("SELECT id FROM log_events ORDER BY received_at_us, id")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for order.Next() {
		var id int64
		if err := order.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got = append(got, strconv.FormatInt(id, 10))
	}
	if err := order.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "41,42,40" {
		t.Fatalf("receipt order = %v", got)
	}
	if err := IntegrityCheck(context.Background(), db); err != nil {
		t.Fatal(err)
	}
}

func TestRequestTimeIndexMigrationOrdersRequestLookups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema-7.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if migrations[7].name != "0008_request_time_index.sql" || migrations[7].foreignKeysOff {
		t.Fatalf("migration 8 = %q (foreign keys off %v)", migrations[7].name, migrations[7].foreignKeysOff)
	}
	if err := applyMigrations(context.Background(), db, migrations[:7]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO servers(id,name,created_at_us) VALUES(1,'s',1);
		INSERT INTO sources(id,server_id,project_key,environment_key,application_key,service_key,project_label,environment_label,application_label,service_label,first_seen_at_us,last_seen_at_us)
		VALUES(1,1,'p','e','a','x','p','e','a','x',1,1);
		INSERT INTO log_events(id,event_at_us,received_at_us,source_id,stream,level_normalized,message_raw,message_text,request_id)
		VALUES(50,300,300,1,'stdout','info',x'61','a','r-1'),
			(51,100,100,1,'stdout','info',x'62','b','r-1'),
			(52,300,300,1,'stdout','info',x'63','c','r-1'),
			(53,200,200,1,'stdout','info',x'64','d','r-2'),
			(54,400,400,1,'stdout','info',x'65','e',NULL)`); err != nil {
		t.Fatal(err)
	}

	if err := applyMigrations(context.Background(), db, migrations); err != nil {
		t.Fatal(err)
	}
	assertSchemaVersion(t, db, MaxSchemaVersion)
	var current, legacy int
	if err := db.QueryRow(`SELECT
		(SELECT count(*) FROM sqlite_schema WHERE type='index' AND name='log_events_request_time_idx'),
		(SELECT count(*) FROM sqlite_schema WHERE type='index' AND name='log_events_request_id_idx')`).
		Scan(&current, &legacy); err != nil {
		t.Fatal(err)
	}
	if current != 1 || legacy != 0 {
		t.Fatalf("request indexes: current=%d legacy=%d", current, legacy)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM log_events").Scan(&count); err != nil || count != 5 {
		t.Fatalf("events after migration = %d, err=%v", count, err)
	}

	const lookup = `SELECT id FROM log_events INDEXED BY log_events_request_time_idx
		WHERE request_id = ? AND event_at_us >= 0 AND event_at_us < 1000
		ORDER BY event_at_us DESC, id DESC LIMIT 10`
	rows, err := db.Query("EXPLAIN QUERY PLAN "+lookup, "r-1")
	if err != nil {
		t.Fatal(err)
	}
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(plan, "\n"); strings.Contains(joined, "TEMP B-TREE") {
		t.Fatalf("request lookup sorts:\n%s", joined)
	}
	ordered, err := db.Query(lookup, "r-1")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for ordered.Next() {
		var id int64
		if err := ordered.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got = append(got, strconv.FormatInt(id, 10))
	}
	if err := ordered.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "52,50,51" {
		t.Fatalf("request order = %v", got)
	}
}

func TestUnauthenticatedAuditCounterMigrationSeedsAndTracksRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema-9.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if migrations[9].name != "0010_security_audit_unauthenticated.sql" ||
		migrations[9].foreignKeysOff {
		t.Fatalf("migration 10 = %q (foreign keys off %v)",
			migrations[9].name, migrations[9].foreignKeysOff)
	}
	if err := applyMigrations(context.Background(), db, migrations[:9]); err != nil {
		t.Fatal(err)
	}
	// Schema-9 history: three per-attempt sign-in rejections and two
	// privileged rows.
	if _, err := db.Exec(`INSERT INTO security_audit_events(
			id,occurred_at_us,category,action,outcome,actor_type,
			administrator_id,safe_metadata_json
		) VALUES
			(1,10,'authentication','sign_in','rejected','unauthenticated',NULL,'{}'),
			(2,20,'authentication','sign_in','succeeded','administrator',1,'{}'),
			(3,30,'authentication','sign_in','rejected','unauthenticated',NULL,'{}'),
			(4,40,'session','session.revoke_all','succeeded','local_operator',NULL,'{}'),
			(5,50,'authentication','sign_in','rejected','unauthenticated',NULL,'{}')`); err != nil {
		t.Fatal(err)
	}

	if err := applyMigrations(context.Background(), db, migrations); err != nil {
		t.Fatal(err)
	}
	assertSchemaVersion(t, db, MaxSchemaVersion)
	assertUnauthenticatedAuditCount := func(want int) {
		t.Helper()
		var total, unauthenticated, stored, storedTotal int
		if err := db.QueryRow(`SELECT
			(SELECT count(*) FROM security_audit_events),
			(SELECT count(*) FROM security_audit_events
				WHERE actor_type = 'unauthenticated'),
			(SELECT row_count FROM security_audit_unauthenticated_count WHERE id = 1),
			(SELECT row_count FROM security_audit_event_count WHERE id = 1)`).
			Scan(&total, &unauthenticated, &stored, &storedTotal); err != nil {
			t.Fatal(err)
		}
		if unauthenticated != want || stored != want || storedTotal != total {
			t.Fatalf("unauthenticated rows=%d counter=%d want %d; total rows=%d counter=%d",
				unauthenticated, stored, want, total, storedTotal)
		}
	}
	assertUnauthenticatedAuditCount(3)

	if _, err := db.Exec(`INSERT INTO security_audit_events(
			occurred_at_us,category,action,outcome,actor_type,safe_metadata_json
		) VALUES
			(60,'authentication','sign_in.rejected_summary','rejected','unauthenticated','{}'),
			(70,'session','session.cleanup','succeeded','system','{}')`); err != nil {
		t.Fatal(err)
	}
	assertUnauthenticatedAuditCount(4)
	if _, err := db.Exec(`DELETE FROM security_audit_events WHERE id IN (1, 2)`); err != nil {
		t.Fatal(err)
	}
	assertUnauthenticatedAuditCount(3)

	const oldest = `SELECT id FROM security_audit_events
		WHERE actor_type = 'unauthenticated'
		ORDER BY occurred_at_us ASC, id ASC LIMIT ?`
	var plan []string
	rows, err := db.Query("EXPLAIN QUERY PLAN "+oldest, 1)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan, "\n")
	if !strings.Contains(joined, "security_audit_events_unauthenticated_idx") ||
		strings.Contains(joined, "TEMP B-TREE") {
		t.Fatalf("oldest unauthenticated lookup plan:\n%s", joined)
	}
	var first int64
	if err := db.QueryRow(oldest, 1).Scan(&first); err != nil || first != 3 {
		t.Fatalf("oldest unauthenticated row = %d, err=%v", first, err)
	}

	if _, err := db.Exec("DELETE FROM security_audit_events"); err != nil {
		t.Fatal(err)
	}
	assertUnauthenticatedAuditCount(0)
	if err := IntegrityCheck(context.Background(), db); err != nil {
		t.Fatal(err)
	}
}
