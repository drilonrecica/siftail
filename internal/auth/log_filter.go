package auth

import (
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/drilonrecica/siftail/internal/logs"
)

// logFilter is the one filter model History and Live share (DESIGN.md §8.1,
// ADR 0011 D1): the source scope cascade, levels, streams, and the Contains
// text. Both modes serialise it with the same parameter names, so switching
// modes carries the investigation across. Controls only one mode supports
// (History's time range, Does not contain, and structured filters) are not
// part of it.
type logFilter struct {
	Scope    logs.SourceScope
	Levels   []logs.Level
	Streams  []logs.Stream
	Contains string
}

func historyLogFilter(query logs.HistoryQuery) logFilter {
	return logFilter{
		Scope:  query.Scope(),
		Levels: query.Levels, Streams: query.Streams, Contains: query.Contains,
	}
}

// set writes the filter's non-empty parameters into values.
func (f logFilter) set(values url.Values) {
	f.Scope.Set(values)
	if levels := joinLevels(f.Levels); levels != "" {
		values.Set("levels", levels)
	}
	if streams := joinStreams(f.Streams); streams != "" {
		values.Set("streams", streams)
	}
	if f.Contains != "" {
		values.Set("contains", f.Contains)
	}
}

// liveURL is the Live page for this filter.
func (f logFilter) liveURL() string {
	values := url.Values{"mode": {"live"}}
	f.set(values)
	return "/logs?" + values.Encode()
}

// historyURL is History for this filter over the default range; the History
// handler resolves the missing range to the last hour and redirects to the
// absolute URL (DESIGN.md §10.1, §10.2).
func (f logFilter) historyURL() string {
	values := url.Values{"mode": {"history"}}
	f.set(values)
	return "/logs?" + values.Encode()
}

// surroundingWindow is the half-width of "Show events around this time"
// (DESIGN.md §13.1, ADR 0011 D17).
const surroundingWindow = time.Minute

// surroundingLimit loads the largest History page so the highlighted event
// lands in the first page unless the window is very busy.
const surroundingLimit = logs.MaxHistoryLimit

// surroundingURL opens History at eventAt ± one minute within scope, with
// eventID highlighted. Level, stream, and text filters are deliberately
// left out: the point is to see what else happened around the event.
func surroundingURL(scope logs.SourceScope, eventAt time.Time, eventID int64) string {
	eventAt = eventAt.UTC()
	values := url.Values{
		"mode":      {"history"},
		"from":      {eventAt.Add(-surroundingWindow).Format(time.RFC3339Nano)},
		"to":        {eventAt.Add(surroundingWindow).Format(time.RFC3339Nano)},
		"direction": {string(logs.DirectionOlder)},
		"limit":     {strconv.Itoa(surroundingLimit)},
	}
	scope.Set(values)
	values.Set(highlightParameter, strconv.FormatInt(eventID, 10))
	return "/logs?" + values.Encode()
}

// highlightParameter names the History event to mark and scroll to. It is
// view state only: it never reaches logs.HistoryQuery, so cursors, exports,
// and the query fingerprint ignore it.
const highlightParameter = "highlight"

// takeHighlight removes the highlight parameter from values and returns the
// event ID it names, or zero.
func takeHighlight(values url.Values) (int64, error) {
	entries, ok := values[highlightParameter]
	if !ok {
		return 0, nil
	}
	values.Del(highlightParameter)
	if len(entries) != 1 {
		return 0, fmt.Errorf("%s must occur once", highlightParameter)
	}
	id, err := strconv.ParseInt(entries[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", highlightParameter)
	}
	return id, nil
}

// withHighlight appends the highlight parameter to a URL that already has a
// query string.
func withHighlight(target string, id int64) string {
	if id <= 0 || target == "" {
		return target
	}
	return target + "&" + highlightParameter + "=" + strconv.FormatInt(id, 10)
}
