package logs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
)

const (
	defaultLiveSubscribers       = 16
	defaultLiveMessages          = 256
	defaultLiveBytes       int64 = 2 << 20
	defaultBrokerCommands        = 256
	defaultBrokerEvents          = 10_000
	defaultBrokerBytes     int64 = 16 << 20
	maxLiveFilterValues          = 256
)

var (
	ErrLiveBrokerBusy    = errors.New("live broker command queue is full")
	ErrLiveBrokerStopped = errors.New("live broker is stopped")
	ErrLiveLimit         = errors.New("live subscription limit reached")
	ErrLiveOverflow      = errors.New("live subscription overflow")
	ErrLiveUnsubscribed  = errors.New("live subscription closed")
	ErrInvalidLiveFilter = errors.New("invalid live filter")
)

// CommittedEvent is the persistence-to-Live boundary. Its database IDs are
// assigned within the transaction and become publishable only after commit.
type CommittedEvent struct {
	ID                  int64
	SourceID            int64
	ContainerInstanceID int64
	Event               CanonicalEvent
}

// LiveFilter contains exact canonical filters. Empty fields match every
// committed event. The broker copies and validates filters on subscription.
type LiveFilter struct {
	SourceIDs []int64
	Levels    []Level
	Streams   []Stream
}

// LiveMessage is one committed event delivered in assigned-ID order.
type LiveMessage struct {
	Event CommittedEvent
}

type LiveBrokerOptions struct {
	MaxSubscribers        int
	SubscriberMaxMessages int
	SubscriberMaxBytes    int64
	CommandQueue          int
	PublishQueueMaxEvents int
	PublishQueueMaxBytes  int64
}

func (o LiveBrokerOptions) withDefaults() LiveBrokerOptions {
	if o.MaxSubscribers <= 0 {
		o.MaxSubscribers = defaultLiveSubscribers
	}
	if o.SubscriberMaxMessages <= 0 {
		o.SubscriberMaxMessages = defaultLiveMessages
	}
	if o.SubscriberMaxBytes <= 0 {
		o.SubscriberMaxBytes = defaultLiveBytes
	}
	if o.CommandQueue <= 0 {
		o.CommandQueue = defaultBrokerCommands
	}
	if o.PublishQueueMaxEvents <= 0 {
		o.PublishQueueMaxEvents = defaultBrokerEvents
	}
	if o.PublishQueueMaxBytes <= 0 {
		o.PublishQueueMaxBytes = defaultBrokerBytes
	}
	return o
}

const (
	brokerNew uint32 = iota
	brokerRunning
	brokerStopping
	brokerStopped
)

type liveCommandKind uint8

const (
	livePublish liveCommandKind = iota
	liveSubscribe
	liveUnsubscribe
)

type liveCommand struct {
	kind         liveCommandKind
	events       []CommittedEvent
	filter       compiledLiveFilter
	subscription *LiveSubscription
	reply        chan subscribeResult
}

type subscribeResult struct {
	subscription *LiveSubscription
	err          error
}

type compiledLiveFilter struct {
	sources map[int64]struct{}
	levels  map[Level]struct{}
	streams map[Stream]struct{}
}

func (f compiledLiveFilter) matches(event CommittedEvent) bool {
	if len(f.sources) > 0 {
		if _, ok := f.sources[event.SourceID]; !ok {
			return false
		}
	}
	if len(f.levels) > 0 {
		if _, ok := f.levels[event.Event.Level]; !ok {
			return false
		}
	}
	if len(f.streams) > 0 {
		if _, ok := f.streams[event.Event.Stream]; !ok {
			return false
		}
	}
	return true
}

// LiveBroker owns subscription state in one lifecycle-managed goroutine.
// Publication only attempts bounded in-memory admission and never waits for a
// subscriber or the broker worker.
type LiveBroker struct {
	options  LiveBrokerOptions
	commands chan liveCommand
	ready    chan struct{}
	stopping chan struct{}
	lost     chan struct{}
	done     chan struct{}

	state     atomic.Uint32
	lossEpoch atomic.Uint64
	nextID    uint64

	lifecycleMu  sync.RWMutex
	queueMu      sync.Mutex
	queuedEvents int
	queuedBytes  int64
	stopOnce     sync.Once
	readyOnce    sync.Once
	doneOnce     sync.Once
}

func NewLiveBroker(options LiveBrokerOptions) *LiveBroker {
	options = options.withDefaults()
	return &LiveBroker{
		options:  options,
		commands: make(chan liveCommand, options.CommandQueue),
		ready:    make(chan struct{}),
		stopping: make(chan struct{}),
		lost:     make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
}

func (b *LiveBroker) Ready() <-chan struct{} { return b.ready }
func (b *LiveBroker) Done() <-chan struct{}  { return b.done }

// Run starts the broker worker. Stop rejects new work, drains every command
// accepted before the stop boundary, closes all subscriptions, and lets Run
// return deterministically.
func (b *LiveBroker) Run(ctx context.Context) error {
	if b == nil {
		return ErrLiveBrokerStopped
	}
	b.lifecycleMu.Lock()
	if !b.state.CompareAndSwap(brokerNew, brokerRunning) {
		b.lifecycleMu.Unlock()
		return errors.New("live broker already started")
	}
	b.readyOnce.Do(func() { close(b.ready) })
	b.lifecycleMu.Unlock()

	subscriptions := make(map[uint64]*LiveSubscription)
	var handledLossEpoch uint64
	for {
		handledLossEpoch = b.handlePublicationLoss(subscriptions, handledLossEpoch)
		select {
		case command := <-b.commands:
			b.handleCommand(subscriptions, command)
		case <-b.lost:
		case <-ctx.Done():
			b.beginStop()
			b.drain(subscriptions, &handledLossEpoch)
			b.finish(subscriptions)
			return nil
		case <-b.stopping:
			b.drain(subscriptions, &handledLossEpoch)
			b.finish(subscriptions)
			return nil
		}
	}
}

func (b *LiveBroker) handleCommand(subscriptions map[uint64]*LiveSubscription, command liveCommand) {
	switch command.kind {
	case livePublish:
		b.releasePublish(command.events)
		for _, event := range command.events {
			for id, subscription := range subscriptions {
				if !subscription.filter.matches(event) {
					continue
				}
				if !subscription.enqueue(LiveMessage{Event: event}, liveMessageBytes(event)) {
					delete(subscriptions, id)
				}
			}
		}
	case liveSubscribe:
		for id, subscription := range subscriptions {
			if subscription.closed() {
				delete(subscriptions, id)
			}
		}
		if len(subscriptions) >= b.options.MaxSubscribers {
			command.reply <- subscribeResult{err: ErrLiveLimit}
			return
		}
		b.nextID++
		subscription := newLiveSubscription(
			b, b.nextID, b.lossEpoch.Load(), command.filter, b.options,
		)
		subscriptions[subscription.id] = subscription
		command.reply <- subscribeResult{subscription: subscription}
	case liveUnsubscribe:
		if command.subscription != nil {
			delete(subscriptions, command.subscription.id)
			command.subscription.closeWith(ErrLiveUnsubscribed)
		}
	}
}

func (b *LiveBroker) drain(
	subscriptions map[uint64]*LiveSubscription,
	handledLossEpoch *uint64,
) {
	for {
		*handledLossEpoch = b.handlePublicationLoss(subscriptions, *handledLossEpoch)
		select {
		case command := <-b.commands:
			b.handleCommand(subscriptions, command)
		case <-b.lost:
		default:
			return
		}
	}
}

func (b *LiveBroker) handlePublicationLoss(
	subscriptions map[uint64]*LiveSubscription,
	handled uint64,
) uint64 {
	current := b.lossEpoch.Load()
	if current <= handled {
		return handled
	}
	for id, subscription := range subscriptions {
		if subscription.lossEpoch < current {
			subscription.closeWith(ErrLiveOverflow)
			delete(subscriptions, id)
		}
	}
	return current
}

func (b *LiveBroker) finish(subscriptions map[uint64]*LiveSubscription) {
	closeLiveSubscriptions(subscriptions, ErrLiveBrokerStopped)
	b.state.Store(brokerStopped)
	b.doneOnce.Do(func() { close(b.done) })
}

func closeLiveSubscriptions(subscriptions map[uint64]*LiveSubscription, err error) {
	for id, subscription := range subscriptions {
		subscription.closeWith(err)
		delete(subscriptions, id)
	}
}

// Stop is idempotent. A broker stopped before Run transitions directly to a
// completed state.
func (b *LiveBroker) Stop() {
	if b == nil {
		return
	}
	b.lifecycleMu.Lock()
	switch b.state.Load() {
	case brokerNew:
		b.state.Store(brokerStopped)
		b.readyOnce.Do(func() { close(b.ready) })
		b.doneOnce.Do(func() { close(b.done) })
	case brokerRunning:
		b.state.Store(brokerStopping)
		b.stopOnce.Do(func() { close(b.stopping) })
	}
	b.lifecycleMu.Unlock()
}

func (b *LiveBroker) beginStop() {
	b.lifecycleMu.Lock()
	if b.state.Load() == brokerRunning {
		b.state.Store(brokerStopping)
		b.stopOnce.Do(func() { close(b.stopping) })
	}
	b.lifecycleMu.Unlock()
}

// TryPublish admits a committed batch without waiting. False means the broker
// could not retain the batch; every current subscription is then closed with
// ErrLiveOverflow so the gap is explicit.
func (b *LiveBroker) TryPublish(events []CommittedEvent) bool {
	if b == nil || len(events) == 0 {
		return b != nil
	}
	eventBytes := int64(0)
	for _, event := range events {
		eventBytes += liveMessageBytes(event)
	}
	if len(events) > b.options.PublishQueueMaxEvents || eventBytes > b.options.PublishQueueMaxBytes {
		b.markPublishLost()
		return false
	}
	copied := append([]CommittedEvent(nil), events...)
	sort.Slice(copied, func(i, j int) bool { return copied[i].ID < copied[j].ID })

	b.lifecycleMu.RLock()
	defer b.lifecycleMu.RUnlock()
	if b.state.Load() != brokerRunning {
		return false
	}
	b.queueMu.Lock()
	if b.queuedEvents+len(copied) > b.options.PublishQueueMaxEvents ||
		b.queuedBytes+eventBytes > b.options.PublishQueueMaxBytes {
		b.queueMu.Unlock()
		b.markPublishLost()
		return false
	}
	command := liveCommand{kind: livePublish, events: copied}
	select {
	case b.commands <- command:
		b.queuedEvents += len(copied)
		b.queuedBytes += eventBytes
		b.queueMu.Unlock()
		return true
	default:
		b.queueMu.Unlock()
		b.markPublishLost()
		return false
	}
}

func (b *LiveBroker) releasePublish(events []CommittedEvent) {
	var bytes int64
	for _, event := range events {
		bytes += liveMessageBytes(event)
	}
	b.queueMu.Lock()
	b.queuedEvents -= len(events)
	b.queuedBytes -= bytes
	b.queueMu.Unlock()
}

func (b *LiveBroker) markPublishLost() {
	if b == nil || b.state.Load() != brokerRunning {
		return
	}
	b.lossEpoch.Add(1)
	select {
	case b.lost <- struct{}{}:
	default:
	}
}

// Subscribe registers an exact filter through the bounded broker command
// queue. Once the command is accepted, the method waits only for the broker's
// deterministic response.
func (b *LiveBroker) Subscribe(ctx context.Context, filter LiveFilter) (*LiveSubscription, error) {
	if b == nil {
		return nil, ErrLiveBrokerStopped
	}
	compiled, err := compileLiveFilter(filter)
	if err != nil {
		return nil, err
	}
	reply := make(chan subscribeResult, 1)
	command := liveCommand{kind: liveSubscribe, filter: compiled, reply: reply}
	b.lifecycleMu.RLock()
	if b.state.Load() != brokerRunning {
		b.lifecycleMu.RUnlock()
		return nil, ErrLiveBrokerStopped
	}
	select {
	case b.commands <- command:
		b.lifecycleMu.RUnlock()
	case <-ctx.Done():
		b.lifecycleMu.RUnlock()
		return nil, ctx.Err()
	default:
		b.lifecycleMu.RUnlock()
		return nil, ErrLiveBrokerBusy
	}
	result := <-reply
	return result.subscription, result.err
}

func (b *LiveBroker) unsubscribe(subscription *LiveSubscription) {
	if b == nil || subscription == nil {
		return
	}
	b.lifecycleMu.RLock()
	defer b.lifecycleMu.RUnlock()
	if b.state.Load() != brokerRunning {
		return
	}
	select {
	case b.commands <- liveCommand{kind: liveUnsubscribe, subscription: subscription}:
	default:
		// The subscription is already closed locally. The broker prunes closed
		// entries before enforcing its fixed subscriber limit.
	}
}

func compileLiveFilter(filter LiveFilter) (compiledLiveFilter, error) {
	if len(filter.SourceIDs) > maxLiveFilterValues ||
		len(filter.Levels) > maxLiveFilterValues ||
		len(filter.Streams) > maxLiveFilterValues {
		return compiledLiveFilter{}, fmt.Errorf("%w: too many values", ErrInvalidLiveFilter)
	}
	compiled := compiledLiveFilter{}
	for _, sourceID := range filter.SourceIDs {
		if sourceID <= 0 {
			return compiledLiveFilter{}, fmt.Errorf("%w: source ID", ErrInvalidLiveFilter)
		}
		if compiled.sources == nil {
			compiled.sources = make(map[int64]struct{}, len(filter.SourceIDs))
		}
		compiled.sources[sourceID] = struct{}{}
	}
	for _, level := range filter.Levels {
		switch level {
		case LevelTrace, LevelDebug, LevelInfo, LevelWarn, LevelError, LevelFatal, LevelUnknown:
		default:
			return compiledLiveFilter{}, fmt.Errorf("%w: level", ErrInvalidLiveFilter)
		}
		if compiled.levels == nil {
			compiled.levels = make(map[Level]struct{}, len(filter.Levels))
		}
		compiled.levels[level] = struct{}{}
	}
	for _, stream := range filter.Streams {
		switch stream {
		case StreamStdout, StreamStderr, StreamUnknown:
		default:
			return compiledLiveFilter{}, fmt.Errorf("%w: stream", ErrInvalidLiveFilter)
		}
		if compiled.streams == nil {
			compiled.streams = make(map[Stream]struct{}, len(filter.Streams))
		}
		compiled.streams[stream] = struct{}{}
	}
	return compiled, nil
}

func liveMessageBytes(event CommittedEvent) int64 {
	return event.Event.RetainedBytes() + 128
}

type queuedLiveMessage struct {
	message LiveMessage
	bytes   int64
}

// LiveSubscription exposes a bounded dequeue API. Consumers cannot bypass the
// byte accounting by reading an internal channel directly.
type LiveSubscription struct {
	broker *LiveBroker
	id     uint64
	// lossEpoch is the publication-loss generation observed at registration.
	// A prior broker overflow must not truncate a later subscription.
	lossEpoch uint64
	filter    compiledLiveFilter

	mu       sync.Mutex
	queue    []queuedLiveMessage
	head     int
	count    int
	bytes    int64
	maxBytes int64
	notify   chan struct{}
	done     chan struct{}
	err      error
	closedAt sync.Once
}

func newLiveSubscription(
	broker *LiveBroker,
	id uint64,
	lossEpoch uint64,
	filter compiledLiveFilter,
	options LiveBrokerOptions,
) *LiveSubscription {
	return &LiveSubscription{
		broker: broker, id: id, lossEpoch: lossEpoch, filter: filter,
		queue:    make([]queuedLiveMessage, options.SubscriberMaxMessages),
		maxBytes: options.SubscriberMaxBytes,
		notify:   make(chan struct{}, 1), done: make(chan struct{}),
	}
}

func (s *LiveSubscription) Done() <-chan struct{} { return s.done }

func (s *LiveSubscription) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *LiveSubscription) Next(ctx context.Context) (LiveMessage, error) {
	if s == nil {
		return LiveMessage{}, ErrLiveBrokerStopped
	}
	for {
		s.mu.Lock()
		if s.count > 0 {
			entry := s.queue[s.head]
			s.queue[s.head] = queuedLiveMessage{}
			s.head = (s.head + 1) % len(s.queue)
			s.count--
			s.bytes -= entry.bytes
			if s.count > 0 {
				s.signal()
			}
			s.mu.Unlock()
			return entry.message, nil
		}
		if s.err != nil {
			err := s.err
			s.mu.Unlock()
			return LiveMessage{}, err
		}
		s.mu.Unlock()

		select {
		case <-s.notify:
		case <-s.done:
		case <-ctx.Done():
			return LiveMessage{}, ctx.Err()
		}
	}
}

func (s *LiveSubscription) Close() {
	if s == nil {
		return
	}
	s.closeWith(ErrLiveUnsubscribed)
	s.broker.unsubscribe(s)
}

func (s *LiveSubscription) enqueue(message LiveMessage, bytes int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return false
	}
	if s.count == len(s.queue) || s.bytes+bytes > s.maxBytes {
		s.closeLocked(ErrLiveOverflow)
		return false
	}
	index := (s.head + s.count) % len(s.queue)
	s.queue[index] = queuedLiveMessage{message: message, bytes: bytes}
	s.count++
	s.bytes += bytes
	s.signal()
	return true
}

func (s *LiveSubscription) closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err != nil
}

func (s *LiveSubscription) closeWith(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked(err)
}

func (s *LiveSubscription) closeLocked(err error) {
	if s.err != nil {
		return
	}
	s.err = err
	for i := range s.queue {
		s.queue[i] = queuedLiveMessage{}
	}
	s.head, s.count, s.bytes = 0, 0, 0
	s.closedAt.Do(func() { close(s.done) })
	s.signal()
}

func (s *LiveSubscription) signal() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}
