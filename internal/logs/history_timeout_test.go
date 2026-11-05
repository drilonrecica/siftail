package logs

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestHistoryReadsCarryTheInteractiveQueryBound(t *testing.T) {
	fixture := newHistoryFixture(t)
	if fixture.store.queryTimeout != DefaultHistoryQueryTimeout {
		t.Fatalf("default History bound = %s", fixture.store.queryTimeout)
	}
	for _, requested := range []time.Duration{0, -time.Second, time.Hour} {
		if got := fixture.store.WithQueryTimeout(requested).queryTimeout; got != DefaultHistoryQueryTimeout {
			t.Fatalf("WithQueryTimeout(%s) = %s; it may only tighten the bound", requested, got)
		}
	}

	fixture.store.WithQueryTimeout(time.Nanosecond)
	if _, err := fixture.store.History(context.Background(), fixture.query()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("History past its bound = %v", err)
	}
	if _, err := fixture.store.Catalog(context.Background(), SourceScope{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Catalog past its bound = %v", err)
	}
	if _, err := fixture.store.Event(context.Background(), 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Event past its bound = %v", err)
	}
	if _, err := fixture.store.LiveSources(context.Background()); err == nil {
		t.Fatal("LiveSources past its bound succeeded")
	}

	fixture.store.WithQueryTimeout(DefaultHistoryQueryTimeout)
	if _, err := fixture.store.History(context.Background(), fixture.query()); err != nil {
		t.Fatalf("History within its bound = %v", err)
	}
}
