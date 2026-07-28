package database

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestInitialMigrationAndNoOpReopen(t *testing.T) {
	db := openTestDB(t)
	assertSchemaVersion(t, db.Writer(), 4)
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
	assertSchemaVersion(t, db.Writer(), 4)
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
	assertSchemaVersion(t, db, 4)
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
		tooNew.Actual != 4 || tooNew.Supported != 2 {
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
	assertSchemaVersion(t, db, 4)
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
	assertSchemaVersion(t, db, 4)
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
		tooNew.Actual != 4 || tooNew.Supported != 3 {
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
		{"SELECT id FROM log_events WHERE request_id='r'", "log_events_request_id_idx"},
		{"SELECT id FROM log_events ORDER BY retention_at_us, id LIMIT 10", "log_events_retention_idx"},
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
