package auth

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/drilonrecica/siftail/internal/logs"
	webui "github.com/drilonrecica/siftail/internal/web/ui"
)

type livePageQuery struct {
	SourceID int64
	Levels   []logs.Level
	Streams  []logs.Stream
	Contains string
}

func (b *Browser) livePage(w http.ResponseWriter, r *http.Request) {
	session, _ := BrowserSessionFromContext(r.Context())
	query, err := parseLivePageQuery(r.URL.Query())
	if err != nil {
		http.Error(w, "Check the Live filters.", http.StatusBadRequest)
		return
	}
	if b.history == nil {
		http.Error(w, "Logs are temporarily unavailable.", http.StatusServiceUnavailable)
		return
	}
	sources, err := b.history.LiveSources(r.Context())
	if err != nil {
		http.Error(w, "Logs are temporarily unavailable.", http.StatusServiceUnavailable)
		return
	}
	if err := b.ui.Shell(w, http.StatusOK, webui.ShellView{
		CSRFToken: session.CSRFToken,
		Mode:      "live",
		Live:      buildLiveView(query, sources),
	}); err != nil {
		http.Error(w, "Logs are temporarily unavailable.", http.StatusInternalServerError)
	}
}

func parseLivePageQuery(values url.Values) (livePageQuery, error) {
	allowed := map[string]struct{}{
		"mode": {}, "source": {}, "levels": {}, "streams": {}, "contains": {},
	}
	for key, entries := range values {
		if _, ok := allowed[key]; !ok {
			return livePageQuery{}, fmt.Errorf("unknown Live parameter %q", key)
		}
		if len(entries) != 1 {
			return livePageQuery{}, fmt.Errorf("Live parameter %q must occur once", key)
		}
	}
	if values.Get("mode") != "live" {
		return livePageQuery{}, errors.New("mode must be live")
	}
	var query livePageQuery
	if source := values.Get("source"); source != "" {
		parsed, err := strconv.ParseInt(source, 10, 64)
		if err != nil || parsed <= 0 {
			return livePageQuery{}, errors.New("source must be a positive integer")
		}
		query.SourceID = parsed
	}
	var err error
	if query.Levels, err = parseLivePageLevels(values.Get("levels")); err != nil {
		return livePageQuery{}, err
	}
	if query.Streams, err = parseLivePageStreams(values.Get("streams")); err != nil {
		return livePageQuery{}, err
	}
	query.Contains = values.Get("contains")
	if !utf8.ValidString(query.Contains) || len(query.Contains) > logs.MaxTextFilterBytes ||
		strings.ContainsRune(query.Contains, 0) {
		return livePageQuery{}, errors.New("contains is invalid")
	}
	return query, nil
}

func parseLivePageLevels(value string) ([]logs.Level, error) {
	return parseLivePageSet(value, []logs.Level{
		logs.LevelTrace, logs.LevelDebug, logs.LevelInfo, logs.LevelWarn,
		logs.LevelError, logs.LevelFatal, logs.LevelUnknown,
	}, "level")
}

func parseLivePageStreams(value string) ([]logs.Stream, error) {
	return parseLivePageSet(value, []logs.Stream{
		logs.StreamStdout, logs.StreamStderr, logs.StreamUnknown,
	}, "stream")
}

func parseLivePageSet[T ~string](value string, order []T, name string) ([]T, error) {
	if value == "" {
		return nil, nil
	}
	positions := make(map[T]int, len(order))
	for index, item := range order {
		positions[item] = index
	}
	seen := make(map[T]struct{})
	for _, raw := range strings.Split(value, ",") {
		item := T(raw)
		if _, ok := positions[item]; !ok || raw == "" {
			return nil, fmt.Errorf("%ss contains an invalid %s", name, name)
		}
		seen[item] = struct{}{}
	}
	result := make([]T, 0, len(seen))
	for item := range seen {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return positions[result[i]] < positions[result[j]] })
	return result, nil
}

func buildLiveView(query livePageQuery, sources []logs.LiveSourceOption) webui.LiveView {
	view := webui.LiveView{
		CanonicalURL:  livePageURL(query),
		StreamURL:     liveStreamURL(query),
		HistoryURL:    "/logs",
		SourceSummary: "All sources",
		Levels:        levelChoices(query.Levels),
		Streams:       streamChoices(query.Streams),
		LevelsValue:   joinLevels(query.Levels),
		StreamsValue:  joinStreams(query.Streams),
		Contains:      query.Contains,
	}
	for _, source := range sources {
		selected := source.ID == query.SourceID
		view.Sources = append(view.Sources, webui.SelectOption{
			Value: strconv.FormatInt(source.ID, 10),
			Label: source.Label, Selected: selected,
		})
		if selected {
			view.SourceSummary = source.Label
		}
	}
	return view
}

func livePageURL(query livePageQuery) string {
	values := url.Values{"mode": {"live"}}
	if query.SourceID > 0 {
		values.Set("source", strconv.FormatInt(query.SourceID, 10))
	}
	if levels := joinLevels(query.Levels); levels != "" {
		values.Set("levels", levels)
	}
	if streams := joinStreams(query.Streams); streams != "" {
		values.Set("streams", streams)
	}
	if query.Contains != "" {
		values.Set("contains", query.Contains)
	}
	return "/logs?" + values.Encode()
}

func liveStreamURL(query livePageQuery) string {
	values := url.Values{}
	if query.SourceID > 0 {
		values.Set("source", strconv.FormatInt(query.SourceID, 10))
	}
	for _, level := range query.Levels {
		values.Add("level", string(level))
	}
	for _, stream := range query.Streams {
		values.Add("stream", string(stream))
	}
	if query.Contains != "" {
		values.Set("contains", query.Contains)
	}
	if encoded := values.Encode(); encoded != "" {
		return "/logs/live/stream?" + encoded
	}
	return "/logs/live/stream"
}
