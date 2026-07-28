// Package status owns bounded, sanitized operational state and health views.
package status

import (
	"sync"
	"time"

	"github.com/drilonrecica/siftail/internal/ingest"
	"github.com/drilonrecica/siftail/internal/retention"
)

const diagnosticCapacity = 100

type Diagnostic struct {
	At       time.Time
	Category string
	Summary  string
}

type Snapshot struct {
	StartedAt            time.Time
	WriterReady          bool
	DatabaseWritable     bool
	Degraded             bool
	DegradedCategory     string
	AcceptedBatches      uint64
	AcceptedEvents       uint64
	EventsToday          uint64
	RejectedBatches      uint64
	RecentEvents         uint64
	LastSuccessfulIngest *time.Time
	LastDatabaseError    *Diagnostic
	LastCleanup          *time.Time
	LastCleanupResult    retention.CleanupResult
	Diagnostics          []Diagnostic
}

type rateBucket struct {
	second int64
	events uint64
}

type State struct {
	mu sync.Mutex

	startedAt         time.Time
	writerReady       bool
	databaseWritable  bool
	databaseCategory  string
	retentionDegraded bool
	acceptedBatches   uint64
	acceptedEvents    uint64
	dailyDay          int64
	dailyEvents       uint64
	rejectedBatches   uint64
	lastSuccess       time.Time
	lastDatabase      Diagnostic
	lastCleanup       time.Time
	cleanupResult     retention.CleanupResult
	rate              [60]rateBucket
	diagnostics       [diagnosticCapacity]Diagnostic
	diagnosticStart   int
	diagnosticCount   int
}

func NewState(startedAt time.Time) *State {
	return &State{
		startedAt: startedAt.UTC(), databaseWritable: true,
	}
}

func (s *State) SetWriterReady(ready bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.writerReady = ready
	s.mu.Unlock()
}

func (s *State) Ready(shuttingDown bool) bool {
	if s == nil || shuttingDown {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writerReady && s.databaseWritable && s.databaseCategory == "" &&
		!s.retentionDegraded
}

func (s *State) RecordIngestAccepted(events int, at time.Time) {
	if s == nil || events <= 0 {
		return
	}
	at = at.UTC()
	s.mu.Lock()
	s.acceptedBatches++
	s.acceptedEvents += uint64(events)
	day := at.Unix() / int64(24*time.Hour/time.Second)
	if s.dailyDay != day {
		s.dailyDay = day
		s.dailyEvents = 0
	}
	s.dailyEvents += uint64(events)
	s.databaseWritable = true
	s.databaseCategory = ""
	s.lastSuccess = at
	second := at.Unix()
	index := int(second % int64(len(s.rate)))
	if s.rate[index].second != second {
		s.rate[index] = rateBucket{second: second}
	}
	s.rate[index].events += uint64(events)
	s.mu.Unlock()
}

func (s *State) RecordIngestRejected(
	category ingest.ErrorCategory,
	databaseFailure bool,
	at time.Time,
) {
	if s == nil {
		return
	}
	at = at.UTC()
	s.mu.Lock()
	s.rejectedBatches++
	if databaseFailure {
		s.databaseWritable = false
		s.databaseCategory = string(category)
		diagnostic := Diagnostic{
			At: at, Category: string(category),
			Summary: "Ingestion could not commit because storage was unavailable.",
		}
		s.lastDatabase = diagnostic
		s.appendDiagnosticLocked(diagnostic)
	}
	s.mu.Unlock()
}

func (s *State) RecordCleanup(result retention.CleanupResult, at time.Time) {
	if s == nil {
		return
	}
	at = at.UTC()
	s.mu.Lock()
	s.lastCleanup = at
	s.cleanupResult = result
	s.retentionDegraded = result.SizeTriggered && !result.SizeTargetReached &&
		result.EventsExhausted
	s.mu.Unlock()
}

func (s *State) RecordCleanupError(at time.Time) {
	if s == nil {
		return
	}
	diagnostic := Diagnostic{
		At: at.UTC(), Category: "retention_cleanup",
		Summary: "Retention cleanup did not complete.",
	}
	s.mu.Lock()
	s.appendDiagnosticLocked(diagnostic)
	s.mu.Unlock()
}

func (s *State) Snapshot(now time.Time) Snapshot {
	if s == nil {
		return Snapshot{}
	}
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := Snapshot{
		StartedAt: s.startedAt, WriterReady: s.writerReady,
		DatabaseWritable: s.databaseWritable,
		Degraded:         s.databaseCategory != "" || s.retentionDegraded,
		DegradedCategory: s.databaseCategory,
		AcceptedBatches:  s.acceptedBatches, AcceptedEvents: s.acceptedEvents,
		EventsToday:       s.dailyEvents,
		RejectedBatches:   s.rejectedBatches,
		LastCleanupResult: s.cleanupResult,
	}
	if snapshot.DegradedCategory == "" && s.retentionDegraded {
		snapshot.DegradedCategory = "retention_target_unmet"
	}
	cutoff := now.Unix() - int64(len(s.rate)) + 1
	for _, bucket := range s.rate {
		if bucket.second >= cutoff && bucket.second <= now.Unix() {
			snapshot.RecentEvents += bucket.events
		}
	}
	if !s.lastSuccess.IsZero() {
		value := s.lastSuccess
		snapshot.LastSuccessfulIngest = &value
	}
	if !s.lastDatabase.At.IsZero() {
		value := s.lastDatabase
		snapshot.LastDatabaseError = &value
	}
	if !s.lastCleanup.IsZero() {
		value := s.lastCleanup
		snapshot.LastCleanup = &value
	}
	snapshot.Diagnostics = make([]Diagnostic, s.diagnosticCount)
	for index := 0; index < s.diagnosticCount; index++ {
		position := (s.diagnosticStart + s.diagnosticCount - 1 - index) %
			diagnosticCapacity
		snapshot.Diagnostics[index] = s.diagnostics[position]
	}
	return snapshot
}

func (s *State) appendDiagnosticLocked(diagnostic Diagnostic) {
	if s.diagnosticCount < diagnosticCapacity {
		position := (s.diagnosticStart + s.diagnosticCount) % diagnosticCapacity
		s.diagnostics[position] = diagnostic
		s.diagnosticCount++
		return
	}
	s.diagnostics[s.diagnosticStart] = diagnostic
	s.diagnosticStart = (s.diagnosticStart + 1) % diagnosticCapacity
}
