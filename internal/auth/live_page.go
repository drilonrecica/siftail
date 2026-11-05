package auth

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/drilonrecica/siftail/internal/logs"
	"github.com/drilonrecica/siftail/internal/web"
	webui "github.com/drilonrecica/siftail/internal/web/ui"
)

func (b *Browser) livePage(w http.ResponseWriter, r *http.Request) {
	filter, err := parseLivePageQuery(r.URL.Query())
	if err != nil {
		b.renderLiveShellError(w, r, http.StatusBadRequest, webui.LiveView{
			CanonicalURL: logFilter{}.liveURL(), HistoryURL: logFilter{}.historyURL(),
			Error: "Check the Live filters.",
		})
		return
	}
	var catalog logs.SourceCatalog
	if b.history != nil {
		catalog, err = b.history.Catalog(r.Context(), filter.Scope)
	}
	if b.history == nil || err != nil {
		b.renderLiveShellError(w, r, http.StatusServiceUnavailable, webui.LiveView{
			CanonicalURL: filter.liveURL(), HistoryURL: filter.historyURL(),
			Error: "Live could not start because the database is temporarily " +
				"unavailable. Existing filters were preserved.",
			ErrorRequestID: web.RequestIDFromContext(r.Context()),
		})
		return
	}
	shell := b.shellBase(w, r, "live")
	shell.Live = buildLiveView(filter, catalog)
	shell.Live.Zone = shell.Prefs.Timezone
	if err := b.ui.Shell(w, http.StatusOK, shell); err != nil {
		http.Error(w, "Logs are temporarily unavailable.", http.StatusInternalServerError)
	}
}

// renderLiveShellError renders a Live failure inside the application shell,
// like History's shell error, so navigation and the mode switch remain
// (audit UX-21). The plain-text response is only the fallback when the shell
// itself cannot render.
func (b *Browser) renderLiveShellError(
	w http.ResponseWriter,
	r *http.Request,
	status int,
	view webui.LiveView,
) {
	view.SourceSummary = "Live"
	shell := b.shellBase(w, r, "live")
	view.Zone = shell.Prefs.Timezone
	shell.Live = view
	if err := b.ui.Shell(w, status, shell); err != nil {
		http.Error(w, view.Error, status)
	}
}

// parseLivePageQuery reads the shared filter model (logFilter) from a Live
// page URL. It accepts exactly the History names for scope, levels,
// streams, and contains, so a History URL's filters open Live unchanged.
func parseLivePageQuery(values url.Values) (logFilter, error) {
	allowed := map[string]struct{}{
		"mode": {}, "server": {}, "project": {}, "environment": {},
		"application": {}, "service": {}, "levels": {}, "streams": {}, "contains": {},
	}
	for key, entries := range values {
		if _, ok := allowed[key]; !ok {
			return logFilter{}, fmt.Errorf("unknown Live parameter %q", key)
		}
		if len(entries) != 1 {
			return logFilter{}, fmt.Errorf("Live parameter %q must occur once", key)
		}
	}
	if values.Get("mode") != "live" {
		return logFilter{}, errors.New("mode must be live")
	}
	var filter logFilter
	var err error
	if filter.Scope, err = logs.ParseSourceScope(values); err != nil {
		return logFilter{}, err
	}
	if filter.Levels, err = parseLivePageLevels(values.Get("levels")); err != nil {
		return logFilter{}, err
	}
	if filter.Streams, err = parseLivePageStreams(values.Get("streams")); err != nil {
		return logFilter{}, err
	}
	filter.Contains = values.Get("contains")
	if !utf8.ValidString(filter.Contains) || len(filter.Contains) > logs.MaxTextFilterBytes ||
		strings.ContainsRune(filter.Contains, 0) {
		return logFilter{}, errors.New("contains is invalid")
	}
	return filter, nil
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

func buildLiveView(filter logFilter, catalog logs.SourceCatalog) webui.LiveView {
	scope := filter.Scope
	return webui.LiveView{
		CanonicalURL: filter.liveURL(),
		StreamURL:    liveStreamURL(filter),
		HistoryURL:   filter.historyURL(),
		SourceSummary: sourceSummary(logs.HistoryQuery{
			ServerID: scope.ServerID, Project: scope.Project,
			Environment: scope.Environment, Application: scope.Application,
			Service: scope.Service,
		}, catalog),
		Servers:      serverOptions(catalog.Servers, scope.ServerID),
		Projects:     sourceOptions(catalog.Projects, scope.Project),
		Environments: sourceOptions(catalog.Environments, scope.Environment),
		Applications: sourceOptions(catalog.Applications, scope.Application),
		Services:     sourceOptions(catalog.Services, scope.Service),
		Levels:       levelChoices(filter.Levels),
		Streams:      streamChoices(filter.Streams),
		LevelsValue:  joinLevels(filter.Levels),
		StreamsValue: joinStreams(filter.Streams),
		Contains:     filter.Contains,
	}
}

// liveStreamURL is the SSE subscription for the filter. The stream API
// takes repeated level and stream entries (parseLiveFilter).
func liveStreamURL(filter logFilter) string {
	values := url.Values{}
	filter.Scope.Set(values)
	for _, level := range filter.Levels {
		values.Add("level", string(level))
	}
	for _, stream := range filter.Streams {
		values.Add("stream", string(stream))
	}
	if filter.Contains != "" {
		values.Set("contains", filter.Contains)
	}
	if encoded := values.Encode(); encoded != "" {
		return "/logs/live/stream?" + encoded
	}
	return "/logs/live/stream"
}
