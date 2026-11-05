package auth

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/drilonrecica/siftail/internal/audit"
)

// Rejected sign-ins are audited as one aggregated row per window (ADR 0010),
// never one row per attempt, so unauthenticated traffic cannot flood the
// single writer or the audit table.
const (
	signInAuditWindow = 5 * time.Minute
	// signInAuditMaxClients bounds the distinct client keys tracked per
	// window. Beyond it the count is reported as "1024+".
	signInAuditMaxClients = 1024
	// signInAuditMaxPending bounds closed windows kept while their write
	// fails, one hour of windows; older windows are dropped and reported.
	signInAuditMaxPending = 12
	signInAuditAction     = "sign_in.rejected_summary"
	maxClientSummaryBytes = 256
)

var errSignInAuditWindowsDropped = errors.New(
	"aggregated sign-in audit windows were dropped while storage was unavailable",
)

type signInRejectionReason int

const (
	// rejectionInvalidCredentials is a wrong password or unknown username.
	rejectionInvalidCredentials signInRejectionReason = iota
	// rejectionThrottled is a client or account key inside its retry delay.
	rejectionThrottled
	// rejectionInFlight is a second concurrent attempt on a key.
	rejectionInFlight
	// rejectionOverloaded is an Argon2 admission queue overflow.
	rejectionOverloaded
	rejectionReasonCount
)

type signInRejectionWindow struct {
	start     time.Time
	counts    [rejectionReasonCount]uint64
	clients   map[string]uint64
	untracked bool
}

// signInRejectionSummary is a closed window reduced to the values its audit
// row stores, so pending windows do not keep their client maps.
type signInRejectionSummary struct {
	start          time.Time
	counts         [rejectionReasonCount]uint64
	distinct       int
	distinctCapped bool
	topClient      string
	topCount       uint64
}

// signInRejections counts rejected sign-in attempts in memory. Recording an
// attempt never performs I/O; the owned writer drains closed windows.
type signInRejections struct {
	mu      sync.Mutex
	now     func() time.Time
	open    *signInRejectionWindow
	closed  []signInRejectionSummary
	dropped uint64
}

func newSignInRejections() *signInRejections {
	return &signInRejections{now: time.Now}
}

func (s *signInRejections) add(reason signInRejectionReason, clientKey string) {
	client := clientSummary(clientKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	start := s.now().UTC().Truncate(signInAuditWindow)
	if s.open != nil && !s.open.start.Equal(start) {
		s.closeOpenLocked()
	}
	if s.open == nil {
		s.open = &signInRejectionWindow{
			start: start, clients: make(map[string]uint64),
		}
	}
	window := s.open
	window.counts[reason]++
	if _, tracked := window.clients[client]; tracked ||
		len(window.clients) < signInAuditMaxClients {
		window.clients[client]++
	} else {
		window.untracked = true
	}
}

// take removes and returns every closed window, and the open window too when
// it has ended or final is set, plus how many windows were dropped since the
// last take.
func (s *signInRejections) take(final bool) ([]signInRejectionSummary, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open != nil && (final ||
		!s.now().Before(s.open.start.Add(signInAuditWindow))) {
		s.closeOpenLocked()
	}
	closed, dropped := s.closed, s.dropped
	s.closed, s.dropped = nil, 0
	return closed, dropped
}

// requeue returns windows whose write failed ahead of newer ones, within the
// pending bound.
func (s *signInRejections) requeue(failed []signInRejectionSummary) {
	s.mu.Lock()
	defer s.mu.Unlock()
	merged := append(append([]signInRejectionSummary(nil), failed...), s.closed...)
	if excess := len(merged) - signInAuditMaxPending; excess > 0 {
		s.dropped += uint64(excess)
		merged = merged[excess:]
	}
	s.closed = merged
}

// untilWindowEnd is how long the writer waits before the open window, or the
// window that would open now, has ended.
func (s *signInRejections) untilWindowEnd() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	start := now.Truncate(signInAuditWindow)
	if s.open != nil && s.open.start.Before(start) {
		return 0
	}
	return start.Add(signInAuditWindow).Sub(now)
}

func (s *signInRejections) closeOpenLocked() {
	window := s.open
	s.open = nil
	summary := signInRejectionSummary{
		start: window.start, counts: window.counts,
		distinct: len(window.clients), distinctCapped: window.untracked,
	}
	for client, count := range window.clients {
		if count > summary.topCount ||
			(count == summary.topCount && client < summary.topClient) {
			summary.topClient, summary.topCount = client, count
		}
	}
	if len(s.closed) >= signInAuditMaxPending {
		s.closed = s.closed[1:]
		s.dropped++
	}
	s.closed = append(s.closed, summary)
}

func (summary signInRejectionSummary) input() audit.Input {
	distinct := strconv.Itoa(summary.distinct)
	if summary.distinctCapped {
		distinct += "+"
	}
	metadata := audit.Metadata{
		audit.MetadataWindowStartedAt: summary.start.UTC().Format(audit.WindowTimeLayout),
		audit.MetadataInvalidCredentialsCount: strconv.FormatUint(
			summary.counts[rejectionInvalidCredentials], 10,
		),
		audit.MetadataThrottledCount: strconv.FormatUint(
			summary.counts[rejectionThrottled], 10,
		),
		audit.MetadataInFlightCount: strconv.FormatUint(
			summary.counts[rejectionInFlight], 10,
		),
		audit.MetadataOverloadedCount: strconv.FormatUint(
			summary.counts[rejectionOverloaded], 10,
		),
		audit.MetadataDistinctClientCount: distinct,
	}
	if summary.topClient != "" {
		metadata[audit.MetadataTopClient] = summary.topClient
		metadata[audit.MetadataTopClientCount] = strconv.FormatUint(summary.topCount, 10)
	}
	return audit.Input{
		OccurredAt: summary.start.Add(signInAuditWindow),
		Category:   audit.CategoryAuthentication, Action: signInAuditAction,
		Outcome: audit.OutcomeRejected, ActorType: audit.ActorUnauthenticated,
		Metadata: metadata,
	}
}

// clientSummary is the nonsecret client shown in an aggregate: the throttle
// key's address group, such as 192.0.2.7/32 or 2001:db8::/64.
func clientSummary(clientKey string) string {
	summary := strings.TrimPrefix(clientKey, "client:")
	if summary == "" || len(summary) > maxClientSummaryBytes ||
		!utf8.ValidString(summary) ||
		strings.IndexFunc(summary, unicode.IsControl) >= 0 {
		return "unknown"
	}
	return summary
}

// RunSignInAudit is the owned writer for aggregated sign-in rejections. It
// writes each window through the database coordinator once, when the window
// ends, and returns when ctx ends. The application root calls
// FlushSignInAudit after the UI server has drained, so the open window is
// kept on graceful shutdown; a crash loses at most the open window.
func (b *Browser) RunSignInAudit(ctx context.Context) error {
	if ctx == nil {
		return errors.New("sign-in audit context is nil")
	}
	timer := time.NewTimer(b.signInRejections.untilWindowEnd())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			_ = b.writeSignInAudit(ctx, false)
			timer.Reset(b.signInRejections.untilWindowEnd())
		}
	}
}

// FlushSignInAudit writes every pending window, including the open one.
func (b *Browser) FlushSignInAudit(ctx context.Context) error {
	return b.writeSignInAudit(ctx, true)
}

func (b *Browser) writeSignInAudit(ctx context.Context, final bool) error {
	summaries, dropped := b.signInRejections.take(final)
	var result error
	if dropped > 0 {
		result = errSignInAuditWindowsDropped
		b.reportSignInAuditError(result)
	}
	if b.audit == nil {
		return result
	}
	for index, summary := range summaries {
		if _, err := b.audit.Record(ctx, summary.input()); err != nil {
			b.signInRejections.requeue(summaries[index:])
			b.reportSignInAuditError(err)
			return errors.Join(result, err)
		}
	}
	return result
}

func (b *Browser) reportSignInAuditError(err error) {
	if b.signInAuditError != nil {
		b.signInAuditError(err)
	}
}
