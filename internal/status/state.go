// Package status owns bounded, sanitized operational state and health views.
package status

import (
	"errors"
	"sort"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/drilonrecica/siftail/internal/ingest"
	"github.com/drilonrecica/siftail/internal/retention"
)

const (
	diagnosticCapacity = 100
	// serverCounterCapacity bounds each set of per-Server ingestion counters;
	// the Server with the oldest last occurrence is evicted first. The
	// process-wide totals stay exact.
	serverCounterCapacity = 1024
	// conflictDiagnosticInterval limits the conflicting-duplicate diagnostic
	// to one entry per Server per minute (ADR 0007).
	conflictDiagnosticInterval = time.Minute
	// lateDiagnosticInterval limits the late-event diagnostic to one entry
	// per Server per hour (ADR 0008).
	lateDiagnosticInterval = time.Hour
)

// ServerConflicts is the process-local conflicting-duplicate counter of one
// Server (ADR 0007): records dropped because they reused a stable source event
// ID for different content, and when that last happened.
type ServerConflicts struct {
	ServerID int64
	Dropped  uint64
	LastAt   time.Time
}

// ServerLateEvents is the process-local late-event counter of one Server
// (ADR 0008): stored records whose event time was already older than the
// age-retention cutoff when received, and when that last happened.
type ServerLateEvents struct {
	ServerID int64
	Late     uint64
	LastAt   time.Time
}

type serverCounter struct {
	count          uint64
	lastAt         time.Time
	lastDiagnostic time.Time
}

// serverCounterSet is a bounded set of per-Server counters with an exact
// process-wide total and a per-Server diagnostic rate limit.
type serverCounterSet struct {
	total   uint64
	servers map[int64]*serverCounter
}

// record adds n occurrences for the Server and reports whether a diagnostic
// is due under the interval.
func (c *serverCounterSet) record(serverID int64, n int, at time.Time, interval time.Duration) bool {
	c.total += uint64(n)
	if c.servers == nil {
		c.servers = make(map[int64]*serverCounter)
	}
	counter, ok := c.servers[serverID]
	if !ok {
		if len(c.servers) >= serverCounterCapacity {
			c.evictOldest()
		}
		counter = &serverCounter{}
		c.servers[serverID] = counter
	}
	counter.count += uint64(n)
	if at.After(counter.lastAt) {
		counter.lastAt = at
	}
	if !counter.lastDiagnostic.IsZero() && at.Sub(counter.lastDiagnostic) < interval {
		return false
	}
	counter.lastDiagnostic = at
	return true
}

func (c *serverCounterSet) evictOldest() {
	var oldestID int64
	var oldest time.Time
	for id, counter := range c.servers {
		if oldestID == 0 || counter.lastAt.Before(oldest) ||
			(counter.lastAt.Equal(oldest) && id < oldestID) {
			oldestID, oldest = id, counter.lastAt
		}
	}
	delete(c.servers, oldestID)
}

type serverCount struct {
	serverID int64
	count    uint64
	lastAt   time.Time
}

// sorted lists the counters, most recent occurrence first.
func (c *serverCounterSet) sorted() []serverCount {
	if len(c.servers) == 0 {
		return nil
	}
	counts := make([]serverCount, 0, len(c.servers))
	for id, counter := range c.servers {
		counts = append(counts, serverCount{serverID: id, count: counter.count, lastAt: counter.lastAt})
	}
	sort.Slice(counts, func(i, j int) bool {
		a, b := counts[i], counts[j]
		if !a.lastAt.Equal(b.lastAt) {
			return a.lastAt.After(b.lastAt)
		}
		return a.serverID < b.serverID
	})
	return counts
}

type Diagnostic struct {
	At          time.Time  `json:"at"`
	Severity    string     `json:"severity"`
	Component   string     `json:"component"`
	Category    string     `json:"category"`
	Summary     string     `json:"summary"`
	RequestID   string     `json:"request_id,omitempty"`
	RecoveredAt *time.Time `json:"recovered_at,omitempty"`
}

type DiagnosticInput struct {
	At          time.Time
	Component   string
	Category    string
	RequestID   string
	RecoveredAt *time.Time
}

func (d Diagnostic) Validate() error {
	reconstructed, err := sanitizeDiagnostic(DiagnosticInput{
		At: d.At, Component: d.Component, Category: d.Category,
		RequestID: d.RequestID, RecoveredAt: d.RecoveredAt,
	})
	if err != nil || reconstructed.Severity != d.Severity ||
		reconstructed.Summary != d.Summary {
		return errors.New("invalid diagnostic event")
	}
	return nil
}

type Snapshot struct {
	StartedAt        time.Time
	WriterReady      bool
	DatabaseWritable bool
	Degraded         bool
	DegradedCategory string
	AcceptedBatches  uint64
	AcceptedEvents   uint64
	EventsToday      uint64
	RejectedBatches  uint64
	ConflictsDropped uint64
	// Conflicts lists per-Server counters, most recent occurrence first.
	Conflicts  []ServerConflicts
	LateEvents uint64
	// Late lists per-Server late-event counters, most recent occurrence
	// first.
	Late                 []ServerLateEvents
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
	conflicts         serverCounterSet
	late              serverCounterSet
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

// HealthSeverity reports the cheap in-memory severity for header display:
// "Healthy", "Degraded", or "Unavailable". Capacity-based Attention requires
// the full operational read and is deliberately not computed here.
func (s *State) HealthSeverity() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.databaseCategory != "" || s.retentionDegraded || !s.databaseWritable {
		return "Degraded"
	}
	if !s.writerReady {
		return "Unavailable"
	}
	return "Healthy"
}

func (s *State) DatabaseWritable() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.databaseWritable && s.databaseCategory == ""
}

func (s *State) IngestUnavailable() ingest.ErrorCategory {
	if s == nil {
		return ingest.CategoryUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retentionDegraded {
		return ingest.CategoryStorageFull
	}
	switch s.databaseCategory {
	case string(ingest.CategoryStorageFull):
		return ingest.CategoryStorageFull
	case "":
		return ""
	default:
		return ingest.CategoryUnavailable
	}
}

func (s *State) RecordDatabaseRecovered(at time.Time) {
	if s == nil {
		return
	}
	at = at.UTC()
	s.mu.Lock()
	if s.databaseCategory == "" {
		s.mu.Unlock()
		return
	}
	s.markDatabaseRecoveredLocked(at)
	s.databaseWritable = true
	s.databaseCategory = ""
	s.appendDiagnosticLocked(Diagnostic{
		At: at, Severity: "information", Component: "database",
		Category: "storage_recovered",
		Summary:  "Database writability recovered after a bounded probe.",
	})
	s.mu.Unlock()
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
	if s.databaseCategory != "" {
		s.markDatabaseRecoveredLocked(at)
	}
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
			At: at, Severity: "degraded", Component: "database",
			Category: string(category),
			Summary:  "Ingestion could not commit because storage was unavailable.",
		}
		s.lastDatabase = diagnostic
		s.appendDiagnosticLocked(diagnostic)
	} else if category == ingest.CategoryUnavailable {
		s.appendDiagnosticLocked(Diagnostic{
			At: at, Severity: "attention", Component: "ingestion",
			Category: "queue_saturated",
			Summary:  "Ingestion admission was temporarily saturated.",
		})
	} else if category == ingest.CategoryInternal {
		// Batch-scoped (audit DAT-03): visible, but readiness and the
		// database category stay as they are.
		s.appendDiagnosticLocked(Diagnostic{
			At: at, Severity: "attention", Component: "ingestion",
			Category: "batch_internal_error",
			Summary:  "An ingestion batch failed with an internal error and was not committed.",
		})
	}
	s.mu.Unlock()
}

// RecordIngestConflicts counts records dropped from a committed batch because
// they reused a stable source event ID for different content (ADR 0007). The
// diagnostic is rate-limited per Server and carries no Server, source, ID, or
// event field; attribution lives in the counter.
func (s *State) RecordIngestConflicts(serverID int64, dropped int, at time.Time) {
	if s == nil || dropped <= 0 || serverID <= 0 {
		return
	}
	at = at.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.conflicts.record(serverID, dropped, at, conflictDiagnosticInterval) {
		return
	}
	s.appendDiagnosticLocked(Diagnostic{
		At: at, Severity: "attention", Component: "ingestion",
		Category: "conflicting_duplicate",
		Summary:  conflictingDuplicateSummary,
	})
}

const conflictingDuplicateSummary = "Records that reused a stable source event ID for different content were dropped; see the per-Server count on Status."

// RecordIngestLate counts stored records of a committed batch whose event time
// was already older than the age-retention cutoff when received (ADR 0008).
// They are kept by receive time; the count points the operator at a clock or
// replay problem. The diagnostic is rate-limited per Server to one per hour
// and carries no Server, source, or event field.
func (s *State) RecordIngestLate(serverID int64, late int, at time.Time) {
	if s == nil || late <= 0 || serverID <= 0 {
		return
	}
	at = at.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.late.record(serverID, late, at, lateDiagnosticInterval) {
		return
	}
	s.appendDiagnosticLocked(Diagnostic{
		At: at, Severity: "attention", Component: "ingestion",
		Category: "late_event",
		Summary:  lateEventSummary,
	})
}

const lateEventSummary = "Events older than the retention age limit are arriving, which indicates a wrong clock or a replayed backlog; they are kept by receive time. See the per-Server count on Status."

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
		At: at.UTC(), Severity: "attention", Component: "retention",
		Category: "retention_cleanup",
		Summary:  "Retention cleanup did not complete.",
	}
	s.mu.Lock()
	s.appendDiagnosticLocked(diagnostic)
	s.mu.Unlock()
}

func (s *State) RecordDiagnostic(input DiagnosticInput) error {
	if s == nil {
		return errors.New("diagnostic state is unavailable")
	}
	diagnostic, err := sanitizeDiagnostic(input)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.appendDiagnosticLocked(diagnostic)
	s.mu.Unlock()
	return nil
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
		ConflictsDropped:  s.conflicts.total,
		LateEvents:        s.late.total,
		LastCleanupResult: s.cleanupResult,
	}
	for _, counter := range s.conflicts.sorted() {
		snapshot.Conflicts = append(snapshot.Conflicts, ServerConflicts{
			ServerID: counter.serverID, Dropped: counter.count, LastAt: counter.lastAt,
		})
	}
	for _, counter := range s.late.sorted() {
		snapshot.Late = append(snapshot.Late, ServerLateEvents{
			ServerID: counter.serverID, Late: counter.count, LastAt: counter.lastAt,
		})
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

func (s *State) markDatabaseRecoveredLocked(at time.Time) {
	recovered := at.UTC()
	if !s.lastDatabase.At.IsZero() && s.lastDatabase.RecoveredAt == nil &&
		!recovered.Before(s.lastDatabase.At) {
		s.lastDatabase.RecoveredAt = &recovered
	}
	for index := 0; index < s.diagnosticCount; index++ {
		position := (s.diagnosticStart + s.diagnosticCount - 1 - index) %
			diagnosticCapacity
		diagnostic := &s.diagnostics[position]
		if diagnostic.Component == "database" &&
			diagnostic.RecoveredAt == nil &&
			!recovered.Before(diagnostic.At) {
			diagnostic.RecoveredAt = &recovered
			return
		}
	}
}

func sanitizeDiagnostic(input DiagnosticInput) (Diagnostic, error) {
	if input.At.IsZero() || input.At.Year() < 1 || input.At.Year() > 9999 ||
		!validDiagnosticRequestID(input.RequestID) {
		return Diagnostic{}, errors.New("invalid diagnostic event")
	}
	severity := ""
	summary := ""
	switch input.Component + "/" + input.Category {
	case "database/storage_full", "database/unavailable":
		severity = "degraded"
		summary = "Ingestion could not commit because storage was unavailable."
	case "database/database_check_succeeded":
		severity = "information"
		summary = "The bounded database check completed successfully."
	case "database/database_check_failed":
		severity = "degraded"
		summary = "The bounded database check did not complete successfully."
	case "database/database_checkpoint_busy":
		severity = "attention"
		summary = "The passive WAL checkpoint was busy."
	case "database/storage_recovered":
		severity = "information"
		summary = "Database writability recovered after a bounded probe."
	case "backup/backup_succeeded":
		severity = "information"
		summary = "A backup completed and passed verification."
	case "backup/backup_failed":
		severity = "attention"
		summary = "A backup did not produce a verified artifact."
	case "retention/retention_cleanup":
		severity = "attention"
		summary = "Retention cleanup did not complete."
	case "audit/audit_cleanup":
		severity = "attention"
		summary = "Security audit cleanup did not complete."
	case "sessions/session_cleanup":
		severity = "attention"
		summary = "Session cleanup did not complete."
	case "ingestion/queue_saturated":
		severity = "attention"
		summary = "Ingestion admission was temporarily saturated."
	case "ingestion/batch_internal_error":
		severity = "attention"
		summary = "An ingestion batch failed with an internal error and was not committed."
	case "ingestion/conflicting_duplicate":
		severity = "attention"
		summary = conflictingDuplicateSummary
	case "ingestion/late_event":
		severity = "attention"
		summary = lateEventSummary
	case "ingestion/guided_test_rejected":
		severity = "information"
		summary = "A guided ingestion test request was rejected."
	default:
		return Diagnostic{}, errors.New("invalid diagnostic event")
	}
	var recoveredAt *time.Time
	if input.RecoveredAt != nil {
		recovered := input.RecoveredAt.UTC()
		if recovered.Before(input.At.UTC()) ||
			recovered.Year() < 1 || recovered.Year() > 9999 {
			return Diagnostic{}, errors.New("invalid diagnostic event")
		}
		recoveredAt = &recovered
	}
	return Diagnostic{
		At: input.At.UTC(), Severity: severity, Component: input.Component,
		Category: input.Category, Summary: summary, RequestID: input.RequestID,
		RecoveredAt: recoveredAt,
	}, nil
}

func validDiagnosticRequestID(value string) bool {
	if value == "" {
		return true
	}
	if !utf8.ValidString(value) || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
