package logs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestLiveBrokerPublishesInIDOrderAndFilters(t *testing.T) {
	broker := startLiveBroker(t, LiveBrokerOptions{})
	all := subscribeLive(t, broker, LiveFilter{})
	filtered := subscribeLive(t, broker, LiveFilter{
		SourceIDs: []int64{2},
		Levels:    []Level{LevelError},
		Streams:   []Stream{StreamStderr},
		Contains:  "THIRD",
	})

	events := []CommittedEvent{
		liveTestEvent(3, 2, LevelError, StreamStderr, "third"),
		liveTestEvent(1, 1, LevelInfo, StreamStdout, "first"),
		liveTestEvent(2, 2, LevelError, StreamStderr, "second"),
	}
	if !broker.TryPublish(events) {
		t.Fatal("ordered batch was not accepted")
	}

	for index, wantID := range []int64{1, 2, 3} {
		message := nextLive(t, all)
		if message.Event.ID != wantID {
			t.Fatalf("message %d ID = %d, want %d", index, message.Event.ID, wantID)
		}
	}
	message := nextLive(t, filtered)
	if message.Event.ID != 3 {
		t.Fatalf("filtered ID = %d, want 3", message.Event.ID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := filtered.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unexpected filtered message: %v", err)
	}

	// The broker owns its copies of the batch and filter slices.
	events[0].ID = 99
}

func TestLiveBrokerPublishesSourceScopedControls(t *testing.T) {
	broker := startLiveBroker(t, LiveBrokerOptions{})
	sourceOne := subscribeLive(t, broker, LiveFilter{SourceIDs: []int64{1}})
	sourceTwo := subscribeLive(t, broker, LiveFilter{SourceIDs: []int64{2}})
	all := subscribeLive(t, broker, LiveFilter{})

	control := LiveControl{Type: LiveControlSourcePurged, SourceID: 2}
	if !broker.TryPublishControl(control) {
		t.Fatal("control was not accepted")
	}
	for _, subscription := range []*LiveSubscription{sourceTwo, all} {
		message := nextLive(t, subscription)
		if message.Type != LiveMessageControl || message.Control != control {
			t.Fatalf("control message = %#v", message)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := sourceOne.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unaffected source received control: %v", err)
	}
	if broker.TryPublishControl(LiveControl{Type: "invented", SourceID: 2}) {
		t.Fatal("invalid control was accepted")
	}
}

func TestLiveBrokerSlowSubscriberOverflowsWithoutBlockingPublish(t *testing.T) {
	broker := startLiveBroker(t, LiveBrokerOptions{
		SubscriberMaxMessages: 2,
		SubscriberMaxBytes:    1 << 20,
	})
	subscription := subscribeLive(t, broker, LiveFilter{})

	started := time.Now()
	if !broker.TryPublish([]CommittedEvent{
		liveTestEvent(1, 1, LevelInfo, StreamStdout, "one"),
		liveTestEvent(2, 1, LevelInfo, StreamStdout, "two"),
		liveTestEvent(3, 1, LevelInfo, StreamStdout, "three"),
	}) {
		t.Fatal("publish queue unexpectedly rejected batch")
	}
	if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
		t.Fatalf("publish waited on slow subscriber for %v", elapsed)
	}
	waitLiveDone(t, subscription)
	if !errors.Is(subscription.Err(), ErrLiveOverflow) {
		t.Fatalf("subscription error = %v, want overflow", subscription.Err())
	}
	if _, err := subscription.Next(context.Background()); !errors.Is(err, ErrLiveOverflow) {
		t.Fatalf("Next error = %v, want overflow", err)
	}
}

func TestLiveBrokerEnforcesSubscriberByteAndConnectionBounds(t *testing.T) {
	broker := startLiveBroker(t, LiveBrokerOptions{
		MaxSubscribers:        1,
		SubscriberMaxMessages: 8,
		SubscriberMaxBytes:    256,
	})
	first := subscribeLive(t, broker, LiveFilter{})
	if _, err := broker.Subscribe(context.Background(), LiveFilter{}); !errors.Is(err, ErrLiveLimit) {
		t.Fatalf("second subscription error = %v, want limit", err)
	}

	if !broker.TryPublish([]CommittedEvent{
		liveTestEvent(1, 1, LevelInfo, StreamStdout, string(make([]byte, 512))),
	}) {
		t.Fatal("broker publication queue unexpectedly rejected event")
	}
	waitLiveDone(t, first)
	if !errors.Is(first.Err(), ErrLiveOverflow) {
		t.Fatalf("byte-limit error = %v, want overflow", first.Err())
	}

	replacement := subscribeLive(t, broker, LiveFilter{})
	replacement.Close()
	waitLiveDone(t, replacement)
	if !errors.Is(replacement.Err(), ErrLiveUnsubscribed) {
		t.Fatalf("close error = %v, want unsubscribed", replacement.Err())
	}
	_ = subscribeLive(t, broker, LiveFilter{})
}

func TestLiveBrokerPublicationCapacityFailureIsExplicit(t *testing.T) {
	broker := startLiveBroker(t, LiveBrokerOptions{
		PublishQueueMaxEvents: 1,
		PublishQueueMaxBytes:  1 << 20,
	})
	subscription := subscribeLive(t, broker, LiveFilter{})
	if broker.TryPublish([]CommittedEvent{
		liveTestEvent(1, 1, LevelInfo, StreamStdout, "one"),
		liveTestEvent(2, 1, LevelInfo, StreamStdout, "two"),
	}) {
		t.Fatal("oversized publication was accepted")
	}
	waitLiveDone(t, subscription)
	if !errors.Is(subscription.Err(), ErrLiveOverflow) {
		t.Fatalf("publication-loss error = %v, want overflow", subscription.Err())
	}

	replacement := subscribeLive(t, broker, LiveFilter{})
	if !broker.TryPublish([]CommittedEvent{
		liveTestEvent(3, 1, LevelInfo, StreamStdout, "after-overflow"),
	}) {
		t.Fatal("publication after overflow was rejected")
	}
	if got := nextLive(t, replacement).Event.ID; got != 3 {
		t.Fatalf("post-overflow event ID = %d", got)
	}
}

func TestLiveBrokerShutdownAndPublishAfterStop(t *testing.T) {
	broker := NewLiveBroker(LiveBrokerOptions{})
	runDone := make(chan error, 1)
	go func() { runDone <- broker.Run(context.Background()) }()
	<-broker.Ready()
	subscription := subscribeLive(t, broker, LiveFilter{})

	broker.Stop()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("broker did not stop")
	}
	waitLiveDone(t, subscription)
	if !errors.Is(subscription.Err(), ErrLiveBrokerStopped) {
		t.Fatalf("shutdown error = %v, want stopped", subscription.Err())
	}
	if broker.TryPublish([]CommittedEvent{liveTestEvent(1, 1, LevelInfo, StreamStdout, "late")}) {
		t.Fatal("publish after stop was accepted")
	}
	if _, err := broker.Subscribe(context.Background(), LiveFilter{}); !errors.Is(err, ErrLiveBrokerStopped) {
		t.Fatalf("subscribe after stop error = %v", err)
	}

	broker.Stop()
	select {
	case <-broker.Done():
	default:
		t.Fatal("Done was not closed")
	}
}

func TestLiveBrokerConcurrentSubscribePublishAndClose(t *testing.T) {
	broker := startLiveBroker(t, LiveBrokerOptions{
		MaxSubscribers:        16,
		SubscriberMaxMessages: 256,
		SubscriberMaxBytes:    2 << 20,
		CommandQueue:          256,
	})
	const workers = 12
	const publications = 200

	var subscribers sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		subscribers.Add(1)
		go func(worker int) {
			defer subscribers.Done()
			for attempt := 0; attempt < 30; attempt++ {
				subscription, err := broker.Subscribe(context.Background(), LiveFilter{
					Levels: []Level{LevelInfo},
				})
				if errors.Is(err, ErrLiveBrokerBusy) || errors.Is(err, ErrLiveLimit) {
					continue
				}
				if err != nil {
					t.Errorf("worker %d subscribe: %v", worker, err)
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
				_, _ = subscription.Next(ctx)
				cancel()
				subscription.Close()
			}
		}(worker)
	}
	for id := 1; id <= publications; id++ {
		_ = broker.TryPublish([]CommittedEvent{
			liveTestEvent(int64(id), 1, LevelInfo, StreamStdout, fmt.Sprintf("event-%d", id)),
		})
	}
	subscribers.Wait()

	final := subscribeLive(t, broker, LiveFilter{})
	if !broker.TryPublish([]CommittedEvent{
		liveTestEvent(publications+1, 1, LevelInfo, StreamStdout, "final"),
	}) {
		t.Fatal("final publication was not accepted")
	}
	if got := nextLive(t, final).Event.ID; got != publications+1 {
		t.Fatalf("final event ID = %d", got)
	}
}

func TestLiveBrokerRejectsInvalidFilters(t *testing.T) {
	broker := startLiveBroker(t, LiveBrokerOptions{})
	tests := []LiveFilter{
		{SourceIDs: []int64{0}},
		{Levels: []Level{"notice"}},
		{Streams: []Stream{"pipe"}},
	}
	for _, filter := range tests {
		if _, err := broker.Subscribe(context.Background(), filter); !errors.Is(err, ErrInvalidLiveFilter) {
			t.Fatalf("filter %#v error = %v", filter, err)
		}
	}
}

func startLiveBroker(t *testing.T, options LiveBrokerOptions) *LiveBroker {
	t.Helper()
	broker := NewLiveBroker(options)
	runDone := make(chan error, 1)
	go func() { runDone <- broker.Run(context.Background()) }()
	select {
	case <-broker.Ready():
	case <-time.After(time.Second):
		t.Fatal("broker did not start")
	}
	t.Cleanup(func() {
		broker.Stop()
		select {
		case err := <-runDone:
			if err != nil {
				t.Errorf("broker shutdown: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("broker did not stop")
		}
	})
	return broker
}

func subscribeLive(t *testing.T, broker *LiveBroker, filter LiveFilter) *LiveSubscription {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		subscription, err := broker.Subscribe(context.Background(), filter)
		if !errors.Is(err, ErrLiveBrokerBusy) {
			if err != nil {
				t.Fatal(err)
			}
			return subscription
		}
		if time.Now().After(deadline) {
			t.Fatal("broker remained busy")
		}
	}
}

func nextLive(t *testing.T, subscription *LiveSubscription) LiveMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	message, err := subscription.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func waitLiveDone(t *testing.T, subscription *LiveSubscription) {
	t.Helper()
	select {
	case <-subscription.Done():
	case <-time.After(time.Second):
		t.Fatal("subscription did not close")
	}
}

func liveTestEvent(
	id int64,
	sourceID int64,
	level Level,
	stream Stream,
	message string,
) CommittedEvent {
	return CommittedEvent{
		ID: id, SourceID: sourceID,
		Event: CanonicalEvent{
			Source: SourceIdentity{ServerID: 1},
			Level:  level, Stream: stream,
			MessageRaw: []byte(message), MessageText: message,
		},
	}
}
