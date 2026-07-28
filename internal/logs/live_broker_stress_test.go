package logs

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestLiveBrokerDefaultResourceLedger(t *testing.T) {
	options := NewLiveBroker(LiveBrokerOptions{}).options
	if options.MaxSubscribers != 16 ||
		options.SubscriberMaxMessages != 256 ||
		options.SubscriberMaxBytes != 2<<20 ||
		options.CommandQueue != 256 ||
		options.PublishQueueMaxEvents != 10_000 ||
		options.PublishQueueMaxBytes != 16<<20 {
		t.Fatalf("default Live resource ledger = %#v", options)
	}
}

func TestLiveBrokerConcurrentStressDrainsOwnedResources(t *testing.T) {
	broker := NewLiveBroker(LiveBrokerOptions{})
	runDone := make(chan error, 1)
	go func() { runDone <- broker.Run(context.Background()) }()
	select {
	case <-broker.Ready():
	case <-time.After(time.Second):
		t.Fatal("broker did not start")
	}

	const (
		publishers        = 8
		publicationsEach  = 500
		churners          = 8
		subscriptionsEach = 100
	)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for publisher := 0; publisher < publishers; publisher++ {
		workers.Add(1)
		go func(publisher int) {
			defer workers.Done()
			<-start
			for publication := 0; publication < publicationsEach; publication++ {
				id := int64(publisher*publicationsEach + publication + 1)
				broker.TryPublish([]CommittedEvent{
					liveTestEvent(id, id%4+1, LevelInfo, StreamStdout,
						fmt.Sprintf("publisher-%d-event-%d", publisher, publication)),
				})
			}
		}(publisher)
	}
	for churner := 0; churner < churners; churner++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for completed := 0; completed < subscriptionsEach; {
				subscription, err := broker.Subscribe(context.Background(), LiveFilter{
					Levels: []Level{LevelInfo},
				})
				switch {
				case errors.Is(err, ErrLiveBrokerBusy), errors.Is(err, ErrLiveLimit):
					runtime.Gosched()
					continue
				case err != nil:
					t.Errorf("subscribe: %v", err)
					return
				}
				readContext, cancel := context.WithTimeout(context.Background(), time.Millisecond)
				_, _ = subscription.Next(readContext)
				cancel()
				subscription.Close()
				completed++
			}
		}()
	}
	close(start)

	workersDone := make(chan struct{})
	go func() {
		workers.Wait()
		close(workersDone)
	}()
	select {
	case <-workersDone:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent publishers or subscribers stalled")
	}

	final := subscribeLive(t, broker, LiveFilter{})
	if !broker.TryPublish([]CommittedEvent{
		liveTestEvent(10_000, 1, LevelInfo, StreamStdout, "final"),
	}) {
		t.Fatal("broker did not accept publication after stress")
	}
	if got := nextLive(t, final).Event.ID; got != 10_000 {
		t.Fatalf("final event ID = %d", got)
	}
	final.Close()

	broker.Stop()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("broker shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("broker worker leaked after shutdown")
	}
	broker.queueMu.Lock()
	queuedEvents, queuedBytes := broker.queuedEvents, broker.queuedBytes
	broker.queueMu.Unlock()
	if queuedEvents != 0 || queuedBytes != 0 {
		t.Fatalf("broker ledger after shutdown = %d events, %d bytes", queuedEvents, queuedBytes)
	}
}

func BenchmarkLiveBrokerPublish16Subscribers(b *testing.B) {
	broker := NewLiveBroker(LiveBrokerOptions{})
	runDone := make(chan error, 1)
	go func() { runDone <- broker.Run(context.Background()) }()
	<-broker.Ready()

	const subscriberCount = 16
	subscriptions := make([]*LiveSubscription, 0, subscriberCount)
	for index := 0; index < subscriberCount; index++ {
		subscription, err := broker.Subscribe(context.Background(), LiveFilter{})
		if err != nil {
			b.Fatal(err)
		}
		subscriptions = append(subscriptions, subscription)
	}
	event := liveTestEvent(1, 1, LevelInfo, StreamStdout,
		"representative live message with enough content to exercise fan-out")

	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		event.ID = int64(index + 1)
		if !broker.TryPublish([]CommittedEvent{event}) {
			b.Fatal("publication rejected")
		}
		for _, subscription := range subscriptions {
			message, err := subscription.Next(context.Background())
			if err != nil {
				b.Fatal(err)
			}
			if message.Event.ID != event.ID {
				b.Fatalf("event ID = %d, want %d", message.Event.ID, event.ID)
			}
		}
	}
	b.StopTimer()
	b.ReportMetric(subscriberCount, "deliveries/op")

	for _, subscription := range subscriptions {
		subscription.Close()
	}
	broker.Stop()
	if err := <-runDone; err != nil {
		b.Fatal(err)
	}
}
