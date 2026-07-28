package status

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/drilonrecica/siftail/internal/ingest"
	"github.com/drilonrecica/siftail/internal/retention"
)

type OperationalSnapshot struct {
	State         Snapshot
	Queue         ingest.AdmissionStats
	Retention     retention.Settings
	DatabaseBytes int64
	WALBytes      int64
	SHMBytes      int64
	EventsToday   int64
	OldestEventUS *int64
	NewestEventUS *int64
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
	(SELECT event_at_us FROM log_events ORDER BY event_at_us DESC, id DESC LIMIT 1)`

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
		Scan(&oldest, &newest); err != nil {
		return OperationalSnapshot{}, fmt.Errorf("read sanitized event status: %w", err)
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
