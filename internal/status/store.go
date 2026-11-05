package status

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/drilonrecica/siftail/internal/ingest"
	"github.com/drilonrecica/siftail/internal/retention"
)

// ConflictDisplayLimit bounds the per-Server conflicting-duplicate rows Status
// resolves and shows; the total covers every Server.
const ConflictDisplayLimit = 20

// LateDisplayLimit bounds the per-Server late-event rows Status resolves and
// shows; the total covers every Server.
const LateDisplayLimit = 20

// ServerConflictView is one per-Server conflicting-duplicate counter with the
// Server's current name, or an empty name when the Server was removed.
type ServerConflictView struct {
	ServerConflicts
	ServerName string
}

// ServerLateView is one per-Server late-event counter with the Server's
// current name, or an empty name when the Server was removed.
type ServerLateView struct {
	ServerLateEvents
	ServerName string
}

type OperationalSnapshot struct {
	State Snapshot
	// Conflicts holds at most ConflictDisplayLimit most recent per-Server
	// counters from State.Conflicts, with Server names.
	Conflicts []ServerConflictView
	// Late holds at most LateDisplayLimit most recent per-Server late-event
	// counters from State.Late, with Server names.
	Late          []ServerLateView
	Queue         ingest.AdmissionStats
	Retention     retention.Settings
	DatabaseBytes int64
	WALBytes      int64
	SHMBytes      int64
	EventsToday   int64
	OldestEventUS *int64
	NewestEventUS *int64
	SchemaVersion int
	SQLiteVersion string
}

type Store struct {
	db        *sql.DB
	path      string
	admission *ingest.Admission
	retention *retention.Store
	state     *State
	now       func() time.Time
}

const statusEventBoundsSQL = `SELECT
	(SELECT event_at_us FROM log_events ORDER BY event_at_us, id LIMIT 1),
	(SELECT event_at_us FROM log_events ORDER BY event_at_us DESC, id DESC LIMIT 1),
	(SELECT coalesce(max(version),0) FROM schema_migrations),
	sqlite_version()`

func NewStore(
	db *sql.DB,
	path string,
	admission *ingest.Admission,
	retentionStore *retention.Store,
	state *State,
) *Store {
	return &Store{
		db: db, path: path, admission: admission, retention: retentionStore,
		state: state, now: time.Now,
	}
}

// SetAdmission completes startup composition before HTTP listeners open.
func (s *Store) SetAdmission(admission *ingest.Admission) {
	s.admission = admission
}

// HealthSeverity exposes the in-memory header severity without touching the
// database; it returns "" when state is unavailable.
func (s *Store) HealthSeverity() string {
	if s == nil || s.state == nil {
		return ""
	}
	return s.state.HealthSeverity()
}

func (s *Store) RecordDiagnostic(input DiagnosticInput) error {
	if s == nil || s.state == nil {
		return errors.New("status storage is unavailable")
	}
	return s.state.RecordDiagnostic(input)
}

func (s *Store) Read(ctx context.Context) (OperationalSnapshot, error) {
	if s == nil || s.db == nil || s.path == "" || s.retention == nil ||
		s.state == nil {
		return OperationalSnapshot{}, errors.New("status storage is unavailable")
	}
	now := s.now().UTC()
	snapshot := OperationalSnapshot{State: s.state.Snapshot(now)}
	if s.admission != nil {
		snapshot.Queue = s.admission.Stats()
	}
	settings, err := s.retention.Load(ctx)
	if err != nil {
		return OperationalSnapshot{}, fmt.Errorf("read status retention policy: %w", err)
	}
	snapshot.Retention = settings
	snapshot.DatabaseBytes, err = regularFileSize(s.path)
	if err != nil {
		return OperationalSnapshot{}, err
	}
	snapshot.WALBytes, err = optionalRegularFileSize(s.path + "-wal")
	if err != nil {
		return OperationalSnapshot{}, err
	}
	snapshot.SHMBytes, err = optionalRegularFileSize(s.path + "-shm")
	if err != nil {
		return OperationalSnapshot{}, err
	}
	var oldest, newest sql.NullInt64
	if err := s.db.QueryRowContext(ctx, statusEventBoundsSQL).
		Scan(
			&oldest, &newest, &snapshot.SchemaVersion,
			&snapshot.SQLiteVersion,
		); err != nil {
		return OperationalSnapshot{}, fmt.Errorf("read sanitized event status: %w", err)
	}
	snapshot.Conflicts, snapshot.Late, err = s.counterViews(
		ctx, snapshot.State.Conflicts, snapshot.State.Late,
	)
	if err != nil {
		return OperationalSnapshot{}, err
	}
	snapshot.EventsToday = int64(snapshot.State.EventsToday)
	if oldest.Valid {
		value := oldest.Int64
		snapshot.OldestEventUS = &value
	}
	if newest.Valid {
		value := newest.Int64
		snapshot.NewestEventUS = &value
	}
	return snapshot, nil
}

// counterViews resolves Server names for the displayed per-Server counters
// with one primary-key lookup over a bounded ID set. Names are
// administrator-set labels, never payload content.
func (s *Store) counterViews(
	ctx context.Context,
	conflicts []ServerConflicts,
	late []ServerLateEvents,
) ([]ServerConflictView, []ServerLateView, error) {
	if len(conflicts) > ConflictDisplayLimit {
		conflicts = conflicts[:ConflictDisplayLimit]
	}
	if len(late) > LateDisplayLimit {
		late = late[:LateDisplayLimit]
	}
	if len(conflicts) == 0 && len(late) == 0 {
		return nil, nil, nil
	}
	ids := make([]string, 0, len(conflicts)+len(late))
	for _, conflict := range conflicts {
		ids = append(ids, strconv.FormatInt(conflict.ServerID, 10))
	}
	for _, counter := range late {
		ids = append(ids, strconv.FormatInt(counter.ServerID, 10))
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name FROM servers
		WHERE id IN (SELECT value FROM json_each(?))`, "["+strings.Join(ids, ",")+"]")
	if err != nil {
		return nil, nil, fmt.Errorf("read counted Servers: %w", err)
	}
	defer rows.Close()
	names := make(map[int64]string, len(ids))
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, nil, fmt.Errorf("scan counted Server: %w", err)
		}
		names[id] = name
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("read counted Servers: %w", err)
	}
	var conflictViews []ServerConflictView
	for _, conflict := range conflicts {
		conflictViews = append(conflictViews, ServerConflictView{
			ServerConflicts: conflict, ServerName: names[conflict.ServerID],
		})
	}
	var lateViews []ServerLateView
	for _, counter := range late {
		lateViews = append(lateViews, ServerLateView{
			ServerLateEvents: counter, ServerName: names[counter.ServerID],
		})
	}
	return conflictViews, lateViews, nil
}

func regularFileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("read status storage size: %w", err)
	}
	if !info.Mode().IsRegular() {
		return 0, errors.New("status storage path is not a regular file")
	}
	return info.Size(), nil
}

func optionalRegularFileSize(path string) (int64, error) {
	size, err := regularFileSize(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	return size, err
}
