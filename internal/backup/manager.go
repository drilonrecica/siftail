package backup

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/drilonrecica/siftail/internal/audit"
	"github.com/drilonrecica/siftail/internal/database"
)

const (
	StateIdle      = "idle"
	StateRunning   = "running"
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
	StateCanceled  = "canceled"
)

var ErrBackupInProgress = errors.New("a backup is already in progress")

type Status struct {
	ID          uint64     `json:"id"`
	State       string     `json:"state"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	PageCount   int        `json:"page_count"`
	PagesCopied int        `json:"pages_copied"`
	Result      *Result    `json:"result,omitempty"`
	Category    string     `json:"category,omitempty"`
}

func (s Status) Validate() error {
	switch s.State {
	case StateIdle:
		if s.ID != 0 || s.StartedAt != nil || s.CompletedAt != nil ||
			s.Result != nil || s.Category != "" {
			return errors.New("invalid idle backup status")
		}
	case StateRunning:
		if s.ID == 0 || s.StartedAt == nil || s.CompletedAt != nil ||
			s.Result != nil || s.Category != "" {
			return errors.New("invalid running backup status")
		}
	case StateSucceeded:
		if s.ID == 0 || s.StartedAt == nil || s.CompletedAt == nil ||
			s.Result == nil || s.Category != "" ||
			s.Result.Validate() != nil {
			return errors.New("invalid successful backup status")
		}
	case StateFailed, StateCanceled:
		if s.ID == 0 || s.StartedAt == nil || s.CompletedAt == nil ||
			s.Result != nil || !validStatusCategory(s.Category) ||
			(s.State == StateCanceled && s.Category != "canceled") {
			return errors.New("invalid failed backup status")
		}
	default:
		return errors.New("invalid backup state")
	}
	if s.PageCount < 0 || s.PagesCopied < 0 ||
		(s.PageCount > 0 && s.PagesCopied > s.PageCount) {
		return errors.New("invalid backup progress")
	}
	if s.StartedAt != nil &&
		(s.StartedAt.IsZero() || s.StartedAt.Year() < 1 ||
			s.StartedAt.Year() > 9999) {
		return errors.New("invalid backup start time")
	}
	if s.CompletedAt != nil &&
		(s.CompletedAt.IsZero() || s.CompletedAt.Year() < 1 ||
			s.CompletedAt.Year() > 9999 || s.StartedAt == nil) {
		return errors.New("invalid backup completion time")
	}
	return nil
}

func validStatusCategory(category string) bool {
	switch category {
	case "invalid_output", "insufficient_space", "destination_unavailable",
		"storage_full", "busy", "corrupt", "io", "canceled",
		"unavailable", "verification_failed":
		return true
	default:
		return false
	}
}

type request struct {
	id          uint64
	outputPath  string
	attribution audit.Attribution
}

type Manager struct {
	service  *Service
	now      func() time.Time
	jobs     chan request
	observer func(Status)
	ready    chan struct{}

	mu      sync.Mutex
	status  Status
	started bool
	closed  bool
}

func (m *Manager) WithObserver(observer func(Status)) *Manager {
	m.observer = observer
	return m
}

func NewManager(service *Service) *Manager {
	return &Manager{
		service: service, now: time.Now, jobs: make(chan request, 1),
		ready: make(chan struct{}), status: Status{State: StateIdle},
	}
}

func (m *Manager) Ready() <-chan struct{} {
	if m == nil {
		return nil
	}
	return m.ready
}

func (m *Manager) Start(ctx context.Context, outputPath string) (Status, error) {
	if m == nil || m.service == nil {
		return Status{}, errors.New("backup manager is unavailable")
	}
	if ctx == nil {
		return Status{}, errors.New("backup start context is nil")
	}
	m.mu.Lock()
	if !m.started || m.closed {
		m.mu.Unlock()
		return Status{}, errors.New("backup manager is unavailable")
	}
	if m.status.State == StateRunning {
		m.mu.Unlock()
		return Status{}, ErrBackupInProgress
	}
	id := m.status.ID + 1
	if id == 0 {
		id = 1
	}
	startedAt := m.now().UTC()
	m.status = Status{
		ID: id, State: StateRunning, StartedAt: &startedAt,
	}
	status := cloneStatus(m.status)
	job := request{
		id: id, outputPath: outputPath,
		attribution: audit.AttributionFromContext(ctx),
	}
	m.jobs <- job
	m.mu.Unlock()
	return status, nil
}

func (m *Manager) Snapshot() Status {
	if m == nil {
		return Status{State: StateIdle}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneStatus(m.status)
}

func (m *Manager) Run(ctx context.Context) error {
	if m == nil || m.service == nil || m.jobs == nil {
		return errors.New("backup manager is unavailable")
	}
	if ctx == nil {
		return errors.New("backup manager context is nil")
	}
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return errors.New("backup manager already started")
	}
	m.started = true
	close(m.ready)
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.closed = true
		m.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			m.cancelRunning()
			return nil
		case job := <-m.jobs:
			jobCtx := audit.ContextWithAttribution(ctx, job.attribution)
			result, err := m.service.CreateFull(
				jobCtx, job.outputPath,
				func(progress database.BackupProgress) {
					m.recordProgress(job.id, progress)
				},
			)
			m.complete(job.id, result, err)
		}
	}
}

func (m *Manager) recordProgress(id uint64, progress database.BackupProgress) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.ID != id || m.status.State != StateRunning {
		return
	}
	m.status.PageCount = progress.PageCount
	m.status.PagesCopied = progress.PageCount - progress.Remaining
}

func (m *Manager) complete(id uint64, result Result, err error) {
	m.mu.Lock()
	if m.status.ID != id || m.status.State != StateRunning {
		m.mu.Unlock()
		return
	}
	completedAt := m.now().UTC()
	m.status.CompletedAt = &completedAt
	if err == nil {
		m.status.State = StateSucceeded
		m.status.Result = &result
		m.status.Category = ""
		if m.status.PageCount > 0 {
			m.status.PagesCopied = m.status.PageCount
		}
	} else {
		m.status.State = StateFailed
		m.status.Category = failureCategory(err)
		if errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			m.status.State = StateCanceled
			m.status.Category = "canceled"
		}
	}
	status := cloneStatus(m.status)
	m.mu.Unlock()
	if m.observer != nil {
		m.observer(status)
	}
}

func (m *Manager) cancelRunning() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.State != StateRunning {
		return
	}
	completedAt := m.now().UTC()
	m.status.State = StateCanceled
	m.status.CompletedAt = &completedAt
	m.status.Category = "canceled"
}

func cloneStatus(status Status) Status {
	clone := status
	if status.StartedAt != nil {
		value := *status.StartedAt
		clone.StartedAt = &value
	}
	if status.CompletedAt != nil {
		value := *status.CompletedAt
		clone.CompletedAt = &value
	}
	if status.Result != nil {
		value := *status.Result
		clone.Result = &value
	}
	return clone
}
