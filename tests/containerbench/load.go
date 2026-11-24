package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

// loadSpec sends events [from, to) to the ingestion endpoint. Rate is the
// target events per second (0 sends as fast as the workers can). With retry,
// a rejected request (429, 503, or another 5xx) is resent after a short
// backoff, as Fluent Bit's retry would; without it, the rejection is counted
// and the batch dropped, which is how queue saturation is observed.
type loadSpec struct {
	name    string
	from    int64
	to      int64
	rate    float64
	batch   int
	workers int
	retry   bool
	// sameServer sends a whole batch for the Server of its first block.
	sameServer bool
}

type loadResult struct {
	Name            string         `json:"name"`
	Events          int64          `json:"events_acknowledged"`
	Requests        int64          `json:"requests_acknowledged"`
	TargetRate      float64        `json:"target_events_per_second"`
	BatchEvents     int            `json:"batch_events"`
	Workers         int            `json:"workers"`
	Seconds         float64        `json:"seconds"`
	AchievedRate    float64        `json:"achieved_events_per_second"`
	Statuses        map[string]int `json:"status_counts"`
	Retries         int64          `json:"retries"`
	DroppedBatches  int64          `json:"dropped_batches"`
	LatencyMS       percentiles    `json:"commit_latency_ms"`
	TransportErrors int64          `json:"transport_errors"`
}

type percentiles struct {
	Count int     `json:"count"`
	P50   float64 `json:"p50"`
	P95   float64 `json:"p95"`
	P99   float64 `json:"p99"`
	Max   float64 `json:"max"`
	Mean  float64 `json:"mean"`
}

func summarize(samples []time.Duration) percentiles {
	if len(samples) == 0 {
		return percentiles{}
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(a, b int) bool { return sorted[a] < sorted[b] })
	at := func(q float64) float64 {
		index := int(q*float64(len(sorted))+0.5) - 1
		if index < 0 {
			index = 0
		}
		if index >= len(sorted) {
			index = len(sorted) - 1
		}
		return ms(sorted[index])
	}
	var total time.Duration
	for _, sample := range sorted {
		total += sample
	}
	return percentiles{
		Count: len(sorted), P50: at(0.50), P95: at(0.95), P99: at(0.99),
		Max: ms(sorted[len(sorted)-1]), Mean: ms(total / time.Duration(len(sorted))),
	}
}

func ms(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}

type ingester struct {
	url     string
	tokens  []string // one per fixture Server, in fixtureServers order
	client  *http.Client
	fixture fixture
}

func (in *ingester) run(ctx context.Context, spec loadSpec) loadResult {
	type job struct {
		start, end int64
	}
	jobs := make(chan job)
	var (
		mu        sync.Mutex
		latencies []time.Duration
		statuses  = map[string]int{}
		result    = loadResult{
			Name: spec.name, TargetRate: spec.rate, BatchEvents: spec.batch,
			Workers: spec.workers,
		}
	)
	record := func(status string, latency time.Duration, acknowledged int64) {
		mu.Lock()
		defer mu.Unlock()
		statuses[status]++
		if status == "204" {
			latencies = append(latencies, latency)
			result.Events += acknowledged
			result.Requests++
		}
	}
	var wg sync.WaitGroup
	for range spec.workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var body bytes.Buffer
			for work := range jobs {
				body.Reset()
				server := in.fixture.serverIndex(work.start)
				for i := work.start; i < work.end; i++ {
					eventServer := server
					if !spec.sameServer {
						eventServer = in.fixture.serverIndex(i)
					}
					in.fixture.appendRecord(&body, i, eventServer)
				}
				payload := body.Bytes()
				for attempt := 0; ; attempt++ {
					started := time.Now()
					status := in.post(ctx, in.tokens[server], payload)
					elapsed := time.Since(started)
					record(status, elapsed, work.end-work.start)
					if status == "204" || ctx.Err() != nil {
						break
					}
					if !spec.retry || attempt >= 50 {
						mu.Lock()
						result.DroppedBatches++
						mu.Unlock()
						break
					}
					mu.Lock()
					result.Retries++
					mu.Unlock()
					select {
					case <-ctx.Done():
					case <-time.After(250 * time.Millisecond):
					}
				}
			}
		}()
	}
	started := time.Now()
	sent := int64(0)
	for start := spec.from; start < spec.to && ctx.Err() == nil; {
		end := start + int64(spec.batch)
		if !spec.sameServer {
			// A batch never crosses a Server block, so one token fits.
			if next := (start/fixtureBlock + 1) * fixtureBlock; end > next {
				end = next
			}
		}
		if end > spec.to {
			end = spec.to
		}
		if spec.rate > 0 {
			due := started.Add(time.Duration(float64(sent) / spec.rate * float64(time.Second)))
			if wait := time.Until(due); wait > 0 {
				select {
				case <-ctx.Done():
				case <-time.After(wait):
				}
			}
		}
		select {
		case jobs <- job{start, end}:
		case <-ctx.Done():
		}
		sent += end - start
		start = end
	}
	close(jobs)
	wg.Wait()
	result.Seconds = time.Since(started).Seconds()
	if result.Seconds > 0 {
		result.AchievedRate = float64(result.Events) / result.Seconds
	}
	result.Statuses = statuses
	result.LatencyMS = summarize(latencies)
	result.TransportErrors = int64(statuses["transport"])
	return result
}

func (in *ingester) post(ctx context.Context, token string, payload []byte) string {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, in.url, bytes.NewReader(payload))
	if err != nil {
		return "transport"
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/x-ndjson")
	response, err := in.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			// The phase ended while the request was in flight.
			return "cancelled"
		}
		return "transport"
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	return strconv.Itoa(response.StatusCode)
}

func (r loadResult) String() string {
	return fmt.Sprintf("%s: %d events in %.1fs = %.0f ev/s (target %.0f); p50 %.1f ms p95 %.1f ms p99 %.1f ms max %.1f ms; statuses %v; retries %d dropped %d",
		r.Name, r.Events, r.Seconds, r.AchievedRate, r.TargetRate,
		r.LatencyMS.P50, r.LatencyMS.P95, r.LatencyMS.P99, r.LatencyMS.Max,
		r.Statuses, r.Retries, r.DroppedBatches)
}
