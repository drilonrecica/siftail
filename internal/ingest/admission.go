package ingest

import (
	"context"
	"errors"
	"sync"

	"github.com/drilonrecica/siftail/internal/logs"
)

var ErrQueueClosed = errors.New("ingestion queue is closed")
var ErrAdmissionClosed = errors.New("ingestion admission is closed")

type AdmissionLimits struct {
	MaxDecoders       int
	ResidentMaxEvents int64
	ResidentMaxBytes  int64
	QueueMaxEvents    int64
	QueueMaxBytes     int64
}

// Admission owns the decoder semaphore and both aggregate and queue-subset
// resident ledgers.
type Admission struct {
	decoders chan struct{}
	limits   AdmissionLimits
	mu       sync.Mutex
	resident ledger
	queued   ledger
	closed   chan struct{}
	once     sync.Once
}

type ledger struct {
	events int64
	bytes  int64
}

func NewAdmission(limits AdmissionLimits) *Admission {
	return &Admission{
		decoders: make(chan struct{}, limits.MaxDecoders),
		limits:   limits,
		closed:   make(chan struct{}),
	}
}

func (a *Admission) beginDecode(ctx context.Context) (*residentLease, error) {
	select {
	case <-a.closed:
		return nil, ErrAdmissionClosed
	default:
	}
	select {
	case a.decoders <- struct{}{}:
		select {
		case <-a.closed:
			<-a.decoders
			return nil, ErrAdmissionClosed
		default:
		}
		return &residentLease{admission: a, decoderHeld: true}, nil
	case <-a.closed:
		return nil, ErrAdmissionClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close rejects new decoders. Existing decoder leases remain valid and release
// their accounting through the ordinary ownership path.
func (a *Admission) Close() {
	a.once.Do(func() { close(a.closed) })
}

type residentLease struct {
	admission   *Admission
	mu          sync.Mutex
	events      int64
	bytes       int64
	decoderHeld bool
	queued      bool
	released    bool
}

func (l *residentLease) add(events, bytes int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return errors.New("resident lease already released")
	}
	l.admission.mu.Lock()
	defer l.admission.mu.Unlock()
	nextEvents := l.admission.resident.events + events
	nextBytes := l.admission.resident.bytes + bytes
	if nextEvents > l.admission.limits.ResidentMaxEvents ||
		nextBytes > l.admission.limits.ResidentMaxBytes {
		return &Error{Category: CategoryUnavailable}
	}
	l.admission.resident.events = nextEvents
	l.admission.resident.bytes = nextBytes
	l.events += events
	l.bytes += bytes
	return nil
}

func (l *residentLease) decodingDone() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releaseDecoderLocked()
}

func (l *residentLease) releaseDecoderLocked() {
	if l.decoderHeld {
		<-l.admission.decoders
		l.decoderHeld = false
	}
}

func (l *residentLease) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return
	}
	a := l.admission
	a.mu.Lock()
	a.resident.events -= l.events
	a.resident.bytes -= l.bytes
	if l.queued {
		a.queued.events -= l.events
		a.queued.bytes -= l.bytes
	}
	a.mu.Unlock()
	l.releaseDecoderLocked()
	l.released = true
}

type WriteBatch struct {
	Events                []logs.CanonicalEvent
	ApproxBytes           int64
	Result                chan error
	RequestID             string
	AuthenticatedServerID int64

	lease *residentLease
	once  sync.Once
}

func NewWriteBatch(decoded DecodedBatch, requestID string, serverID int64) *WriteBatch {
	return &WriteBatch{
		Events: decoded.Events, ApproxBytes: decoded.ApproxBytes,
		Result: make(chan error, 1), RequestID: requestID,
		AuthenticatedServerID: serverID, lease: decoded.lease,
	}
}

// Complete delivers a buffered result and releases all capacity exactly once.
func (b *WriteBatch) Complete(err error) {
	b.once.Do(func() {
		if b.lease != nil {
			b.lease.release()
		}
		b.Result <- err
	})
}

type Queue struct {
	admission *Admission
	batches   chan *WriteBatch
	mu        sync.Mutex
	closed    bool
}

func NewQueue(admission *Admission) *Queue {
	capacity := int(admission.limits.QueueMaxEvents)
	if capacity < 1 {
		capacity = 1
	}
	return &Queue{admission: admission, batches: make(chan *WriteBatch, capacity)}
}

// Enqueue transfers an already-resident complete batch to the queue without
// double counting. It never blocks.
func (q *Queue) Enqueue(batch *WriteBatch) error {
	if batch == nil || batch.lease == nil || len(batch.Events) == 0 {
		return &Error{Category: CategoryUnavailable}
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		batch.Complete(ErrQueueClosed)
		return ErrQueueClosed
	}

	lease := batch.lease
	lease.mu.Lock()
	if lease.released || lease.queued {
		lease.mu.Unlock()
		q.mu.Unlock()
		return &Error{Category: CategoryUnavailable}
	}
	a := q.admission
	a.mu.Lock()
	if a.queued.events+lease.events > a.limits.QueueMaxEvents ||
		a.queued.bytes+lease.bytes > a.limits.QueueMaxBytes {
		a.mu.Unlock()
		lease.releaseDecoderLocked()
		lease.mu.Unlock()
		q.mu.Unlock()
		err := &Error{Category: CategoryUnavailable}
		batch.Complete(err)
		return err
	}
	select {
	case q.batches <- batch:
		a.queued.events += lease.events
		a.queued.bytes += lease.bytes
		lease.queued = true
		lease.releaseDecoderLocked()
		a.mu.Unlock()
		lease.mu.Unlock()
		q.mu.Unlock()
		return nil
	default:
		a.mu.Unlock()
		lease.releaseDecoderLocked()
		lease.mu.Unlock()
		q.mu.Unlock()
		err := &Error{Category: CategoryUnavailable}
		batch.Complete(err)
		return err
	}
}

func (q *Queue) Dequeue(ctx context.Context) (*WriteBatch, error) {
	select {
	case batch, ok := <-q.batches:
		if !ok {
			return nil, ErrQueueClosed
		}
		return batch, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (q *Queue) Close() {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		close(q.batches)
	}
	q.mu.Unlock()
}

// Drain completes every remaining batch with err. Deletion does not wait on
// disconnected handlers because Result is buffered.
func (q *Queue) Drain(err error) {
	q.Close()
	for batch := range q.batches {
		batch.Complete(err)
	}
}

type AdmissionStats struct {
	Decoders       int
	ResidentEvents int64
	ResidentBytes  int64
	QueuedEvents   int64
	QueuedBytes    int64
}

func (a *Admission) Stats() AdmissionStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	return AdmissionStats{
		Decoders: len(a.decoders), ResidentEvents: a.resident.events,
		ResidentBytes: a.resident.bytes, QueuedEvents: a.queued.events,
		QueuedBytes: a.queued.bytes,
	}
}
