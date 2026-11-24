package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"time"
)

// queryResult is one PRODUCT.md §15.4 query at one tier: the first (cold)
// request, then warm repetitions.
type queryResult struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	FirstMS float64   `json:"first_ms"`
	WarmMS  []float64 `json:"warm_ms"`
	Median  float64   `json:"warm_median_ms"`
	Min     float64   `json:"warm_min_ms"`
	Max     float64   `json:"warm_max_ms"`
	Rows    int       `json:"rows"`
	Bytes   int64     `json:"bytes,omitempty"`
	Check   string    `json:"check"`
}

type tierContext struct {
	events int64
	newest time.Time
	oldest time.Time
}

func (f fixture) tier(events int64) tierContext {
	return tierContext{
		events: events,
		newest: time.UnixMicro(f.timestampUS(events - 1)).UTC(),
		oldest: time.UnixMicro(f.timestampUS(0)).UTC(),
	}
}

func rangeValues(from, to time.Time, extra url.Values) url.Values {
	values := url.Values{}
	for key, entries := range extra {
		values[key] = entries
	}
	values.Set("from", from.UTC().Format(time.RFC3339Nano))
	values.Set("to", to.UTC().Format(time.RFC3339Nano))
	return values
}

// last returns the range of the newest d of a tier, ending just after its
// newest event.
func (t tierContext) last(d time.Duration, extra url.Values) url.Values {
	to := t.newest.Add(time.Second)
	return rangeValues(to.Add(-d), to, extra)
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	if len(sorted)%2 == 1 {
		return sorted[len(sorted)/2]
	}
	return (sorted[len(sorted)/2-1] + sorted[len(sorted)/2]) / 2
}

func finishQuery(result queryResult) queryResult {
	result.Median = median(result.WarmMS)
	if len(result.WarmMS) > 0 {
		result.Min, result.Max = result.WarmMS[0], result.WarmMS[0]
		for _, value := range result.WarmMS {
			result.Min = min(result.Min, value)
			result.Max = max(result.Max, value)
		}
	}
	return result
}

var (
	largestService = url.Values{"server": {"1"}, "project": {"shop"}, "environment": {"production"}, "application": {"storefront"}, "service": {"web"}}
	smallService   = url.Values{"server": {"3"}, "project": {"infra"}, "environment": {"production"}, "application": {"queue"}, "service": {"redis"}}
	partialApp     = url.Values{"server": {"1"}, "project": {"shop"}, "environment": {"production"}, "application": {"storefront"}}
	checkoutApp    = url.Values{"server": {"1"}, "project": {"shop"}, "environment": {"production"}, "application": {"checkout"}}
)

// runQueries measures every query once cold and then repetitions times.
func runQueries(ctx context.Context, b *browser, f fixture, t tierContext, repetitions int) ([]queryResult, error) {
	var results []queryResult
	pageCase := func(name string, values url.Values, check func(page) string) error {
		path := "/logs/rows?" + values.Encode()
		result := queryResult{Name: name, Path: path}
		for attempt := 0; attempt <= repetitions; attempt++ {
			p, err := b.history(ctx, path)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if attempt == 0 {
				result.FirstMS = ms(p.elapsed)
				result.Rows = p.rows
				result.Check = check(p)
			} else {
				result.WarmMS = append(result.WarmMS, ms(p.elapsed))
			}
		}
		results = append(results, finishQuery(result))
		return nil
	}
	fullPage := func(p page) string {
		if p.rows == 200 {
			return "ok: full page"
		}
		return fmt.Sprintf("partial page: %d rows", p.rows)
	}
	anyRows := func(p page) string {
		if p.rows > 0 {
			return fmt.Sprintf("ok: %d rows", p.rows)
		}
		return "no rows"
	}

	day := 24 * time.Hour
	week := 7 * day
	if err := pageCase("newest_unfiltered_24h", t.last(day, nil), fullPage); err != nil {
		return nil, err
	}
	if err := pageCase("one_service_newest_24h", t.last(day, largestService), fullPage); err != nil {
		return nil, err
	}
	if err := pageCase("small_service_newest_7d", t.last(week, smallService), fullPage); err != nil {
		return nil, err
	}
	if err := pageCase("partial_application_7d", t.last(week, partialApp), fullPage); err != nil {
		return nil, err
	}
	if err := pageCase("errors_last_hour", t.last(time.Hour, url.Values{"levels": {"error,fatal"}}), anyRows); err != nil {
		return nil, err
	}
	if err := pageCase("substring_one_application_24h", t.last(day, mergeValues(checkoutApp, url.Values{"contains": {fixtureNeedle}})), anyRows); err != nil {
		return nil, err
	}
	if err := pageCase("substring_unfiltered_7d", t.last(week, url.Values{"contains": {fixtureNeedle}}), anyRows); err != nil {
		return nil, err
	}
	target := f.jsonRequestEvent(t.events / 2)
	if err := pageCase("request_id_31d", t.last(31*day, url.Values{"request_id": {requestID(target)}}), func(p page) string {
		if p.rows == 1 {
			return "ok: exactly one event"
		}
		return fmt.Sprintf("unexpected: %d rows", p.rows)
	}); err != nil {
		return nil, err
	}

	// Cursor pagination: follow Load older 25 times from the newest
	// unfiltered page; page 1 and page 26 should cost the same.
	{
		first, err := b.history(ctx, "/logs/rows?"+t.last(day, nil).Encode())
		if err != nil {
			return nil, err
		}
		next := first.nextURL
		seen := map[string]bool{}
		for _, id := range first.ids {
			seen[id] = true
		}
		duplicates := 0
		result := queryResult{Name: "deep_cursor_page_26", Path: "Load older ×25 from newest_unfiltered_24h"}
		pageTimes := []float64{}
		for range 25 {
			if next == "" {
				break
			}
			p, err := b.history(ctx, next)
			if err != nil {
				return nil, err
			}
			for _, id := range p.ids {
				if seen[id] {
					duplicates++
				}
				seen[id] = true
			}
			pageTimes = append(pageTimes, ms(p.elapsed))
			next = p.nextURL
			result.Rows = p.rows
		}
		if next == "" {
			return nil, fmt.Errorf("deep cursor: pagination ended early after %d pages", len(pageTimes))
		}
		result.FirstMS = ms(first.elapsed)
		for attempt := 0; attempt < repetitions; attempt++ {
			p, err := b.history(ctx, next)
			if err != nil {
				return nil, err
			}
			result.WarmMS = append(result.WarmMS, ms(p.elapsed))
		}
		result.Check = fmt.Sprintf("ok: %d distinct rows over 26 pages, %d duplicates, pages 2-26 median %.1f ms", len(seen), duplicates, median(pageTimes))
		if duplicates > 0 || len(seen) != 26*200 {
			result.Check = "FAILED: " + result.Check
		}
		results = append(results, finishQuery(result))
	}

	// Equal-time pagination: a 300-event burst shares one timestamp, so the
	// second page continues by ID alone.
	{
		start := burstStart(t.events)
		at := time.UnixMicro(f.timestampUS(start)).UTC()
		values := rangeValues(at.Add(-time.Millisecond), at.Add(time.Millisecond), nil)
		path := "/logs/rows?" + values.Encode()
		result := queryResult{Name: "equal_time_two_pages", Path: path}
		for attempt := 0; attempt <= repetitions; attempt++ {
			first, err := b.history(ctx, path)
			if err != nil {
				return nil, err
			}
			if first.nextURL == "" {
				return nil, fmt.Errorf("equal time: no second page (%d rows)", first.rows)
			}
			second, err := b.history(ctx, first.nextURL)
			if err != nil {
				return nil, err
			}
			elapsed := ms(first.elapsed) + ms(second.elapsed)
			if attempt == 0 {
				result.FirstMS = elapsed
				seen := map[string]bool{}
				for _, id := range append(first.ids, second.ids...) {
					seen[id] = true
				}
				result.Rows = first.rows + second.rows
				result.Check = fmt.Sprintf("ok: %d distinct of %d rows, second page more=%t", len(seen), result.Rows, second.nextURL != "")
				if len(seen) != fixtureBurstSize || result.Rows != fixtureBurstSize || second.nextURL != "" {
					result.Check = "FAILED: " + result.Check
				}
			} else {
				result.WarmMS = append(result.WarmMS, elapsed)
			}
		}
		results = append(results, finishQuery(result))
	}

	// Event detail from the newest page.
	{
		first, err := b.history(ctx, "/logs/rows?"+t.last(day, nil).Encode())
		if err != nil || first.detailURL == "" {
			return nil, fmt.Errorf("detail: no detail URL: %v", err)
		}
		result := queryResult{Name: "event_detail", Path: first.detailURL}
		for attempt := 0; attempt <= repetitions; attempt++ {
			status, body, elapsed, err := b.do(ctx, http.MethodGet, first.detailURL, nil)
			if err != nil || status != http.StatusOK {
				return nil, fmt.Errorf("detail: HTTP %d: %v", status, err)
			}
			if attempt == 0 {
				result.FirstMS = ms(elapsed)
				result.Bytes = int64(len(body))
				result.Check = "ok: HTTP 200"
			} else {
				result.WarmMS = append(result.WarmMS, ms(elapsed))
			}
		}
		results = append(results, finishQuery(result))
	}

	// Export: every event of the newest hour as NDJSON (about 32,000 at
	// this fixture's density), three times; exports are serialized.
	{
		values := t.last(time.Hour, nil)
		result := queryResult{Name: "export_ndjson_1h", Path: "POST /logs/export?" + values.Encode()}
		for attempt := 0; attempt <= min(repetitions, 2); attempt++ {
			status, bytes, lines, elapsed, err := b.export(ctx, values)
			if err != nil || status != http.StatusOK {
				return nil, fmt.Errorf("export: HTTP %d: %v", status, err)
			}
			if attempt == 0 {
				result.FirstMS = ms(elapsed)
				result.Rows = lines
				result.Bytes = bytes
				result.Check = fmt.Sprintf("ok: %d lines", lines)
			} else {
				result.WarmMS = append(result.WarmMS, ms(elapsed))
			}
		}
		results = append(results, finishQuery(result))
	}

	// Source listing and the full History page (catalog plus rows).
	for _, simple := range []struct{ name, path string }{
		{"source_listing", "/sources"},
		{"history_page_full", "/logs?" + t.last(time.Hour, nil).Encode()},
	} {
		result := queryResult{Name: simple.name, Path: simple.path}
		for attempt := 0; attempt <= repetitions; attempt++ {
			status, body, elapsed, err := b.do(ctx, http.MethodGet, simple.path, nil)
			if err != nil || status != http.StatusOK {
				return nil, fmt.Errorf("%s: HTTP %d: %v", simple.name, status, err)
			}
			if attempt == 0 {
				result.FirstMS = ms(elapsed)
				result.Bytes = int64(len(body))
				result.Check = "ok: HTTP 200"
			} else {
				result.WarmMS = append(result.WarmMS, ms(elapsed))
			}
		}
		results = append(results, finishQuery(result))
	}
	return results, nil
}

func mergeValues(sets ...url.Values) url.Values {
	merged := url.Values{}
	for _, set := range sets {
		for key, entries := range set {
			merged[key] = entries
		}
	}
	return merged
}
