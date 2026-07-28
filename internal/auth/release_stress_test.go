package auth

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/logs"
)

func TestConcurrentSessionLookupAndHistoryReads(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	seedBrowserHistory(t, fixture)
	cookie := loginBrowserCookie(t, fixture)
	query, err := logs.ParseHistoryQuery(baseHistoryValues(), time.Now())
	if err != nil {
		t.Fatal(err)
	}

	const workers = 16
	const iterations = 50
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for iteration := 0; iteration < iterations; iteration++ {
				if _, err := fixture.sessions.Lookup(context.Background(), cookie.Value); err != nil {
					errs <- fmt.Errorf("session lookup: %w", err)
					return
				}
				page, err := fixture.browser.history.History(context.Background(), query)
				if err != nil {
					errs <- fmt.Errorf("History read: %w", err)
					return
				}
				if len(page.Events) != 3 {
					errs <- fmt.Errorf("History read returned %d events", len(page.Events))
					return
				}
			}
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
