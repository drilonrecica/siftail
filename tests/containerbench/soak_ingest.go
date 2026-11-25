package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// soakBatch is one ingestion request of the soak and everything the final
// verification needs to know about it. Every event carries the stable
// source event ID "soak-<seq>", so the database copy can be joined back to
// the batch that sent it.
type soakBatch struct {
	From, To int64 // event sequence numbers [From, To)
	Server   int   // zero-based fixture Server; its token sends the batch
	Gen      int   // container generation: a redeploy renames containers
	Release  int   // >0: events belong to the new "release-<n>" source
	EventUS  int64 // event time of the first event
	Equal    bool  // every event shares EventUS
	Flood    bool  // saturation request, sent once without retry

	Attempts  int
	FirstSend time.Time
	// LastSend is when the acknowledged attempt (or the last attempt) began.
	// Siftail stamps received_at_us after it, which bounds retention.
	LastSend time.Time
	Acked    time.Time // zero unless a 204 arrived
	// Ambiguous: an attempt ended without an HTTP status, so it may have
	// committed. Rejected: the only attempt got a non-204 status.
	Ambiguous bool
	Rejected  bool
	Statuses  map[string]int
}

func (b *soakBatch) events() int64 { return b.To - b.From }

type minuteStats struct {
	latencies []time.Duration
	statuses  map[string]int
	acked     int64
}

// soakIngester sends the soak's continuous ingestion at a target rate that
// the scheduler changes, retrying rejected batches with Fluent Bit-like
// backoff, and records the outcome of every batch.
type soakIngester struct {
	url     string
	client  *http.Client
	fixture fixture
	started time.Time

	rateBits atomic.Uint64 // float64 events/s
	gen      atomic.Int64
	next     atomic.Int64 // next sequence number

	tokenMu sync.RWMutex
	tokens  []string
	tokenID []int64

	mu       sync.Mutex
	batches  []*soakBatch
	minutes  map[int]*minuteStats
	unexpect []string // unexpected outcomes, e.g. 401 with a current token
}

func newSoakIngester(url string, f fixture, tokens []string, ids []int64) *soakIngester {
	in := &soakIngester{
		url: url, client: newHTTPClient(2 * time.Minute), fixture: f,
		started: time.Now(), tokens: tokens, tokenID: ids, minutes: map[int]*minuteStats{},
	}
	return in
}

func (in *soakIngester) setRate(rate float64) { in.rateBits.Store(math.Float64bits(rate)) }
func (in *soakIngester) rate() float64        { return math.Float64frombits(in.rateBits.Load()) }

func (in *soakIngester) token(server int) (string, int64) {
	in.tokenMu.RLock()
	defer in.tokenMu.RUnlock()
	return in.tokens[server], in.tokenID[server]
}

func (in *soakIngester) setToken(server int, token string, id int64) {
	in.tokenMu.Lock()
	defer in.tokenMu.Unlock()
	in.tokens[server], in.tokenID[server] = token, id
}

// allocate reserves n sequence numbers, always a multiple of fixtureBlock
// so a batch is one fixture Server block.
func (in *soakIngester) allocate(blocks int64) int64 {
	return in.next.Add(blocks*fixtureBlock) - blocks*fixtureBlock
}

func (in *soakIngester) newBatch(from, to int64, flood bool) *soakBatch {
	now := time.Now()
	b := &soakBatch{
		From: from, To: to, Server: in.fixture.serverIndex(from), Gen: int(in.gen.Load()),
		EventUS: now.UnixMicro(), Equal: (from/fixtureBlock)%50 == 0, Flood: flood,
		Statuses: map[string]int{},
	}
	in.mu.Lock()
	in.batches = append(in.batches, b)
	in.mu.Unlock()
	return b
}

// payload renders the batch. It is a pure function of the batch fields, so
// a replay is byte-identical; conflict changes every message instead.
func (in *soakIngester) payload(b *soakBatch, conflict bool) []byte {
	var buffer bytes.Buffer
	for i := b.From; i < b.To; i++ {
		record := in.fixture.record(i, b.Server)
		at := b.EventUS
		if !b.Equal {
			at += i - b.From
		}
		record["timestamp"] = time.UnixMicro(at).UTC().Format(time.RFC3339Nano)
		record["source_event_id"] = "soak-" + strconv.FormatInt(i, 10)
		if b.Release > 0 {
			record["project"], record["environment"] = "shop", "production"
			record["application"], record["service"] = "storefront", fmt.Sprintf("release-%d", b.Release)
		}
		record["container_name"] = fmt.Sprintf("%s-%s-%d", record["application"], record["service"], b.Gen+1)
		if conflict {
			record["log"] = fmt.Sprint(record["log"]) + " (conflicting replay)"
		}
		encoded, _ := json.Marshal(record)
		buffer.Write(encoded)
		buffer.WriteByte('\n')
	}
	return buffer.Bytes()
}

func (in *soakIngester) post(ctx context.Context, token string, payload []byte) (string, time.Duration) {
	started := time.Now()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, in.url, bytes.NewReader(payload))
	if err != nil {
		return "transport", 0
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/x-ndjson")
	response, err := in.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return "cancelled", time.Since(started)
		}
		return "transport", time.Since(started)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	return strconv.Itoa(response.StatusCode), time.Since(started)
}

func (in *soakIngester) record(status string, latency time.Duration, acked int64) {
	in.mu.Lock()
	defer in.mu.Unlock()
	minute := int(time.Since(in.started) / time.Minute)
	stats := in.minutes[minute]
	if stats == nil {
		stats = &minuteStats{statuses: map[string]int{}}
		in.minutes[minute] = stats
	}
	stats.statuses[status]++
	if status == "204" {
		stats.latencies = append(stats.latencies, latency)
		stats.acked += acked
	}
}

func (in *soakIngester) unexpected(format string, args ...any) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if len(in.unexpect) < 200 {
		in.unexpect = append(in.unexpect, time.Now().UTC().Format(time.RFC3339)+" "+fmt.Sprintf(format, args...))
	}
}

// send delivers one batch. With retry it keeps trying until a 204 or until
// ctx ends, as Fluent Bit's unlimited retry would; a 401 re-reads the
// Server's token, which the rotation scenario replaces.
func (in *soakIngester) send(ctx context.Context, b *soakBatch, retry bool) {
	payload := in.payload(b, false)
	backoff := 250 * time.Millisecond
	for ctx.Err() == nil {
		token, _ := in.token(b.Server)
		attemptStart := time.Now()
		status, latency := in.post(ctx, token, payload)
		in.mu.Lock()
		b.Attempts++
		if b.FirstSend.IsZero() {
			b.FirstSend = attemptStart
		}
		b.LastSend = attemptStart
		b.Statuses[status]++
		switch status {
		case "204":
			b.Acked = time.Now()
		case "transport", "cancelled":
			b.Ambiguous = true
		}
		in.mu.Unlock()
		in.record(status, latency, b.events())
		if status == "204" {
			return
		}
		if status == "401" {
			if current, _ := in.token(b.Server); current != token {
				continue // rotated while in flight
			}
			in.unexpected("batch %d: 401 with the current token of Server %d", b.From, b.Server+1)
		}
		if !retry {
			in.mu.Lock()
			if status != "transport" && status != "cancelled" {
				b.Rejected = true
			}
			in.mu.Unlock()
			return
		}
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 4*time.Second)
	}
}

// run dispatches batches at the current rate until stop is closed, then
// lets the workers finish their retries until drain ends.
func (in *soakIngester) run(stop <-chan struct{}, drain context.Context, workers int) {
	jobs := make(chan *soakBatch)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := range jobs {
				in.send(drain, b, true)
			}
		}()
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	credit := 0.0
	last := time.Now()
loop:
	for {
		select {
		case <-stop:
			break loop
		case now := <-ticker.C:
			rate := in.rate()
			credit += rate * now.Sub(last).Seconds()
			last = now
			// A blocked dispatcher (every worker retrying) is Fluent Bit's
			// bounded buffer: it catches up by at most 60 s of input.
			credit = min(credit, max(rate*60, fixtureBlock))
			for credit >= fixtureBlock {
				from := in.allocate(1)
				b := in.newBatch(from, from+fixtureBlock, false)
				select {
				case jobs <- b:
				case <-stop:
					in.forget(b)
					break loop
				}
				credit -= fixtureBlock
			}
		}
	}
	close(jobs)
	wg.Wait()
}

// forget drops a batch that was allocated but never sent.
func (in *soakIngester) forget(b *soakBatch) {
	in.mu.Lock()
	defer in.mu.Unlock()
	for index := len(in.batches) - 1; index >= 0; index-- {
		if in.batches[index] == b {
			in.batches = append(in.batches[:index], in.batches[index+1:]...)
			return
		}
	}
}

// flood offers far more than the writer commits, once per request and
// without retry, to observe bounded rejection at queue saturation.
func (in *soakIngester) flood(ctx context.Context, workers int) (sent, acked, rejected int) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				// Ten blocks per request; one Server's token sends them all.
				from := in.allocate(10)
				b := in.newBatch(from, from+10*fixtureBlock, true)
				in.send(ctx, b, false)
				mu.Lock()
				sent++
				in.mu.Lock()
				if !b.Acked.IsZero() {
					acked++
				} else if b.Rejected {
					rejected++
				}
				in.mu.Unlock()
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return sent, acked, rejected
}

// replay resends acknowledged batches, byte-identical or with conflicting
// content, and reports the statuses.
func (in *soakIngester) replay(ctx context.Context, batches []*soakBatch, conflict bool) map[string]int {
	statuses := map[string]int{}
	for _, b := range batches {
		token, _ := in.token(b.Server)
		status, latency := in.post(ctx, token, in.payload(b, conflict))
		in.record(status, latency, 0)
		statuses[status]++
	}
	return statuses
}

// recentAcked returns up to n acknowledged non-flood batches acknowledged
// within the last window, newest first.
func (in *soakIngester) recentAcked(n int, window time.Duration) []*soakBatch {
	in.mu.Lock()
	defer in.mu.Unlock()
	var picked []*soakBatch
	cutoff := time.Now().Add(-window)
	for index := len(in.batches) - 1; index >= 0 && len(picked) < n; index-- {
		b := in.batches[index]
		if b.Flood || b.Release > 0 || b.Acked.IsZero() || b.Acked.Before(cutoff) {
			continue
		}
		picked = append(picked, b)
	}
	return picked
}

// sendRelease sends one tracked batch for a source that did not exist
// before: a new service appearing after a deployment.
func (in *soakIngester) sendRelease(ctx context.Context, release int) *soakBatch {
	from := in.allocate(1)
	b := in.newBatch(from, from+fixtureBlock, false)
	in.mu.Lock()
	b.Release, b.Server = release, 0
	in.mu.Unlock()
	in.send(ctx, b, true)
	return b
}

type minuteSummary struct {
	Minute    int            `json:"minute"`
	Acked     int64          `json:"events_acknowledged"`
	Statuses  map[string]int `json:"status_counts"`
	LatencyMS percentiles    `json:"commit_latency_ms"`
}

func (in *soakIngester) minuteSummaries() []minuteSummary {
	in.mu.Lock()
	defer in.mu.Unlock()
	last := 0
	for minute := range in.minutes {
		last = max(last, minute)
	}
	var out []minuteSummary
	for minute := 0; minute <= last; minute++ {
		stats := in.minutes[minute]
		if stats == nil {
			continue
		}
		out = append(out, minuteSummary{Minute: minute, Acked: stats.acked, Statuses: stats.statuses, LatencyMS: summarize(stats.latencies)})
	}
	return out
}

func (in *soakIngester) allLatencies(from, to time.Duration) percentiles {
	in.mu.Lock()
	defer in.mu.Unlock()
	var all []time.Duration
	for minute, stats := range in.minutes {
		at := time.Duration(minute) * time.Minute
		if at >= from && at < to {
			all = append(all, stats.latencies...)
		}
	}
	return summarize(all)
}

// latenciesWhere summarizes the commit latencies of the minutes keep
// accepts, given each minute's start.
func (in *soakIngester) latenciesWhere(keep func(time.Time) bool) percentiles {
	in.mu.Lock()
	defer in.mu.Unlock()
	var all []time.Duration
	for minute, stats := range in.minutes {
		if keep(in.started.Add(time.Duration(minute) * time.Minute)) {
			all = append(all, stats.latencies...)
		}
	}
	return summarize(all)
}
