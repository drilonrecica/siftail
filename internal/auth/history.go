package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/drilonrecica/siftail/internal/logs"
	"github.com/drilonrecica/siftail/internal/web"
	webui "github.com/drilonrecica/siftail/internal/web/ui"
)

func (b *Browser) historyPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("mode") == "live" {
		b.livePage(w, r)
		return
	}
	values := cloneURLValues(r.URL.Query())
	query, err := logs.ParseHistoryQuery(values, b.now())
	if err != nil {
		b.renderHistoryShellError(w, r, http.StatusBadRequest,
			"Check the time range and filters.", "")
		return
	}
	if historyRangeNeedsResolution(values) {
		query.Cursor = ""
		http.Redirect(w, r, historyURL(query), http.StatusSeeOther)
		return
	}
	view, err := b.historyView(r, query, false)
	if err != nil {
		status, message, requestID := historyHTTPError(r, err)
		b.renderHistoryShellError(w, r, status, message, requestID)
		return
	}
	shell := b.shellBase(w, r, "history")
	shell.History = view
	if err := b.ui.Shell(w, http.StatusOK, shell); err != nil {
		http.Error(w, "Logs are temporarily unavailable.", http.StatusInternalServerError)
	}
}

func (b *Browser) historyRows(w http.ResponseWriter, r *http.Request) {
	values := cloneURLValues(r.URL.Query())
	appendValues, hasAppend := values["append"]
	if hasAppend && (len(appendValues) != 1 || appendValues[0] != "1") {
		historyErrorHeaders(w)
		_ = b.ui.HistoryError(w, http.StatusBadRequest, webui.HistoryView{
			Error: "Check the time range and filters.",
		})
		return
	}
	prependValues, hasPrepend := values["prepend"]
	if hasPrepend && (len(prependValues) != 1 || prependValues[0] != "1" || hasAppend) {
		historyErrorHeaders(w)
		_ = b.ui.HistoryError(w, http.StatusBadRequest, webui.HistoryView{
			Error: "Check the time range and filters.",
		})
		return
	}
	appendPage := hasAppend
	prependPage := hasPrepend
	values.Del("append")
	values.Del("prepend")
	query, err := logs.ParseHistoryQuery(values, b.now())
	if err != nil || (appendPage && query.Cursor == "") ||
		(prependPage && (query.Cursor == "" || query.Direction != logs.DirectionNewer)) {
		view := webui.HistoryView{
			CanonicalURL: "/logs",
			Error:        "Check the time range and filters.",
		}
		historyErrorHeaders(w)
		_ = b.ui.HistoryError(w, http.StatusBadRequest, view)
		return
	}
	view, err := b.historyView(r, query, appendPage || prependPage)
	if err != nil {
		status, message, requestID := historyHTTPError(r, err)
		view = webui.HistoryView{
			CanonicalURL:   historyURL(query),
			Error:          message,
			ErrorRequestID: requestID,
		}
		historyErrorHeaders(w)
		_ = b.ui.HistoryError(w, status, view)
		return
	}
	if prependPage {
		if err := b.ui.HistoryPrepend(w, http.StatusOK, view); err != nil {
			http.Error(w, "Logs are temporarily unavailable.", http.StatusInternalServerError)
		}
		return
	}
	if appendPage {
		if err := b.ui.HistoryAppend(w, http.StatusOK, view); err != nil {
			http.Error(w, "Logs are temporarily unavailable.", http.StatusInternalServerError)
		}
		return
	}
	w.Header().Set("HX-Push-Url", historyURL(query))
	if err := b.ui.HistoryRegion(w, http.StatusOK, view); err != nil {
		http.Error(w, "Logs are temporarily unavailable.", http.StatusInternalServerError)
	}
}

func historyErrorHeaders(w http.ResponseWriter) {
	w.Header().Set("HX-Retarget", "#history-update-status")
	w.Header().Set("HX-Reswap", "outerHTML")
}

func (b *Browser) historyView(
	r *http.Request,
	query logs.HistoryQuery,
	appendPage bool,
) (webui.HistoryView, error) {
	if b.history == nil {
		return webui.HistoryView{}, errors.New("history store unavailable")
	}
	page, err := b.history.History(r.Context(), query)
	if err != nil {
		return webui.HistoryView{}, err
	}
	catalog, err := b.history.Catalog(r.Context(), logs.SourceScope{
		ServerID: query.ServerID, Project: query.Project,
		Environment: query.Environment, Application: query.Application,
		Service: query.Service,
	})
	if err != nil {
		return webui.HistoryView{}, err
	}
	view := buildHistoryView(query, page, catalog, b.now())
	if appendPage {
		noun := "events"
		if len(view.Rows) == 1 {
			noun = "event"
		}
		if query.Direction == logs.DirectionNewer {
			if len(view.Rows) == 0 {
				view.Announcement = "No newer events yet."
			} else {
				view.Announcement = fmt.Sprintf("%d newer %s loaded.", len(view.Rows), noun)
			}
		} else {
			view.Announcement = fmt.Sprintf("%d additional %s loaded.", len(view.Rows), noun)
		}
	}
	return view, nil
}

func buildHistoryView(
	query logs.HistoryQuery,
	page logs.HistoryPage,
	catalog logs.SourceCatalog,
	now time.Time,
) webui.HistoryView {
	from := time.UnixMicro(query.FromUS).UTC()
	to := time.UnixMicro(query.ToUS).UTC()
	view := webui.HistoryView{
		CanonicalURL: historyURL(query),
		ExportURL:    "/logs/export?" + query.CanonicalQuery(),
		From:         from.Format(time.RFC3339Nano),
		To:           to.Format(time.RFC3339Nano),
		RangeSummary: from.Format("02 Jan 2006 15:04") + "–" +
			to.Format("02 Jan 2006 15:04 UTC"),
		SourceSummary: sourceSummary(query, catalog),
		Contains:      query.Contains,
		Excludes:      query.Excludes,
		RequestID:     query.RequestID,
		Logger:        query.Logger,
		HTTPMethod:    query.HTTPMethod,
		ErrorType:     query.ErrorType,
		LoadedCount:   len(page.Events),
		LoadedLabel:   "events",
		HasMore:       page.HasMore,
		LevelsValue:   joinLevels(query.Levels),
		StreamsValue:  joinStreams(query.Streams),
	}
	view.ExportFilters = historyExportFilters(query)
	if len(page.Events) == 1 {
		view.LoadedLabel = "event"
	}
	if query.HTTPStatus != nil {
		view.HTTPStatus = strconv.FormatInt(*query.HTTPStatus, 10)
	}
	view.Presets = presetLinks(query)
	view.Servers = serverOptions(catalog.Servers, query.ServerID)
	view.Projects = sourceOptions(catalog.Projects, query.Project)
	view.Environments = sourceOptions(catalog.Environments, query.Environment)
	view.Applications = sourceOptions(catalog.Applications, query.Application)
	view.Services = sourceOptions(catalog.Services, query.Service)
	view.Containers = containerOptions(catalog.Containers, query.ContainerID)
	view.Levels = levelChoices(query.Levels)
	view.Streams = streamChoices(query.Streams)
	// Every empty state offers its next step as a real control
	// (DESIGN.md §24), so first sign-in leads straight to the setup flow.
	switch {
	case len(catalog.Servers) == 0:
		view.EmptyTitle = "No log sources yet"
		view.EmptyMessage = "Create a server, generate its ingestion token, and " +
			"point a Coolify or Fluent Bit log drain at Siftail. Logs appear " +
			"here the moment the first event commits."
		view.EmptyActions = []webui.ActionLink{
			{Label: "Create server", URL: "/servers"},
		}
	case historyHasFilters(query):
		view.EmptyTitle = "No logs matched these filters"
		view.EmptyMessage = "Try a longer time range or remove a message filter."
		if query.Contains != "" || query.Excludes != "" {
			cleared := query
			cleared.Contains = ""
			cleared.Excludes = ""
			cleared.Cursor = ""
			view.EmptyActions = append(view.EmptyActions, webui.ActionLink{
				Label: "Clear text filters", URL: historyURL(cleared),
			})
		}
		for _, preset := range view.Presets {
			if preset.Label == "24h" && !preset.Active {
				view.EmptyActions = append(view.EmptyActions, webui.ActionLink{
					Label: "Last 24 hours", URL: preset.URL,
				})
			}
		}
	default:
		view.EmptyTitle = "No logs arrived during this time range"
		view.EmptyMessage = "Try a longer range or select another source."
		for _, preset := range view.Presets {
			if preset.Label == "24h" && !preset.Active {
				view.EmptyActions = append(view.EmptyActions, webui.ActionLink{
					Label: "Last 24 hours", URL: preset.URL,
				})
			}
		}
	}

	showDate := to.Sub(from) > 24*time.Hour
	view.Rows = make([]webui.HistoryRowView, len(page.Events))
	for index, event := range page.Events {
		eventTime := time.UnixMicro(event.EventAtUS).UTC()
		message := event.MessageText
		if event.MessageBytes > int64(len([]byte(message))) {
			message += "…"
		}
		source := event.ApplicationLabel + "/" + event.ServiceLabel
		if event.SourceAlias != nil {
			source = *event.SourceAlias
		}
		view.Rows[index] = webui.HistoryRowView{
			ID:           event.ID,
			DetailID:     fmt.Sprintf("event-detail-%d", event.ID),
			DetailURL:    fmt.Sprintf("/logs/events/%d", event.ID),
			TimestampUTC: eventTime.Format(time.RFC3339Nano),
			Timestamp:    eventTime.Format("15:04:05.000") + " UTC",
			ShowDate:     showDate,
			Level:        string(event.Level),
			Stream:       string(event.Stream),
			Source:       source,
			Message:      message,
		}
	}
	if page.HasMore && query.Direction == logs.DirectionOlder {
		next := query
		next.Cursor = page.NextCursor
		values := next.CanonicalValues(true)
		values.Set("append", "1")
		view.NextURL = "/logs/rows?" + values.Encode()
	}
	// The tail line's Load newer control (DESIGN.md §12.6). A newer-direction
	// page continues its own traversal while more rows exist, then keeps
	// polling from its newest row; an older-direction page polls from its
	// newest row. An empty poll reuses the same cursor.
	newerCursor := page.PrevCursor
	if query.Direction == logs.DirectionNewer {
		if page.HasMore {
			newerCursor = page.NextCursor
		} else if newerCursor == "" {
			newerCursor = query.Cursor
		}
	}
	if newerCursor != "" {
		newer := query
		newer.Direction = logs.DirectionNewer
		newer.Cursor = newerCursor
		values := newer.CanonicalValues(true)
		values.Set("prepend", "1")
		view.PrevURL = "/logs/rows?" + values.Encode()
	}
	// Offer the Go live jump when the queried range reaches the present.
	if time.UnixMicro(query.ToUS).After(now.Add(-5 * time.Minute)) {
		view.GoLiveURL = "/logs?mode=live"
	}
	return view
}

func historyExportFilters(query logs.HistoryQuery) []webui.DetailField {
	filters := make([]webui.DetailField, 0, 14)
	add := func(label, value string) {
		if value != "" {
			filters = append(filters, webui.DetailField{Label: label, Value: value})
		}
	}
	if query.ServerID > 0 {
		add("Server ID", strconv.FormatInt(query.ServerID, 10))
	}
	add("Project", query.Project)
	add("Environment", query.Environment)
	add("Application", query.Application)
	add("Service", query.Service)
	if query.ContainerID > 0 {
		add("Container instance ID", strconv.FormatInt(query.ContainerID, 10))
	}
	add("Levels", joinLevels(query.Levels))
	add("Streams", joinStreams(query.Streams))
	add("Contains", query.Contains)
	add("Does not contain", query.Excludes)
	add("Request ID", query.RequestID)
	add("Logger", query.Logger)
	add("HTTP method", query.HTTPMethod)
	if query.HTTPStatus != nil {
		add("HTTP status", strconv.FormatInt(*query.HTTPStatus, 10))
	}
	add("Error type", query.ErrorType)
	return filters
}

func historyHasFilters(query logs.HistoryQuery) bool {
	return query.ServerID > 0 || query.Project != "" || query.Environment != "" ||
		query.Application != "" || query.Service != "" || query.ContainerID > 0 ||
		len(query.Levels) > 0 || len(query.Streams) > 0 || query.Contains != "" ||
		query.Excludes != "" || query.RequestID != "" || query.Logger != "" ||
		query.HTTPMethod != "" || query.HTTPStatus != nil || query.ErrorType != ""
}

func (b *Browser) renderHistoryShellError(
	w http.ResponseWriter,
	r *http.Request,
	status int,
	message, requestID string,
) {
	view := webui.HistoryView{
		CanonicalURL:   "/logs",
		RangeSummary:   "Invalid query",
		SourceSummary:  "History",
		Error:          message,
		ErrorRequestID: requestID,
	}
	shell := b.shellBase(w, r, "history")
	shell.History = view
	if err := b.ui.Shell(w, status, shell); err != nil {
		http.Error(w, "Logs are temporarily unavailable.", http.StatusInternalServerError)
	}
}

// historyTimeoutMessage is the History shell error when a read exceeds
// logs.DefaultHistoryQueryTimeout (DESIGN.md §25).
const historyTimeoutMessage = "The search took too long and was stopped. " +
	"Narrow the time range or filters. Existing filters were preserved."

func historyHTTPError(r *http.Request, err error) (int, string, string) {
	if errors.Is(err, logs.ErrInvalidHistoryCursor) {
		return http.StatusBadRequest, "The page cursor is invalid for these filters.", ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusServiceUnavailable,
			historyTimeoutMessage,
			web.RequestIDFromContext(r.Context())
	}
	return http.StatusServiceUnavailable,
		"Logs could not be read because the database is temporarily unavailable. Existing filters were preserved.",
		web.RequestIDFromContext(r.Context())
}

func historyRangeNeedsResolution(values url.Values) bool {
	return values.Get("preset") != "" ||
		(values.Get("from") == "" && values.Get("to") == "")
}

func historyURL(query logs.HistoryQuery) string {
	return "/logs?" + query.CanonicalQuery()
}

func cloneURLValues(input url.Values) url.Values {
	output := make(url.Values, len(input))
	for key, values := range input {
		output[key] = append([]string(nil), values...)
	}
	return output
}

func presetLinks(query logs.HistoryQuery) []webui.PresetLink {
	presets := []struct {
		label    string
		value    string
		duration time.Duration
	}{
		{"15m", "15m", 15 * time.Minute},
		{"1h", "1h", time.Hour},
		{"6h", "6h", 6 * time.Hour},
		{"24h", "24h", 24 * time.Hour},
		{"7d", "7d", 7 * 24 * time.Hour},
	}
	links := make([]webui.PresetLink, len(presets))
	for index, preset := range presets {
		values := query.CanonicalValues(false)
		values.Del("from")
		values.Del("to")
		values.Set("preset", preset.value)
		links[index] = webui.PresetLink{
			Label: preset.label,
			URL:   "/logs?" + values.Encode(),
			Active: time.Duration(query.ToUS-query.FromUS)*time.Microsecond ==
				preset.duration,
		}
	}
	return links
}

func serverOptions(options []logs.ServerOption, selected int64) []webui.SelectOption {
	result := make([]webui.SelectOption, len(options))
	for index, option := range options {
		result[index] = webui.SelectOption{
			Value: strconv.FormatInt(option.ID, 10),
			Label: option.Name, Selected: option.ID == selected,
		}
	}
	return result
}

func sourceOptions(options []logs.SourceOption, selected string) []webui.SelectOption {
	result := make([]webui.SelectOption, len(options))
	for index, option := range options {
		result[index] = webui.SelectOption{
			Value: option.Value, Label: option.Label, Selected: option.Value == selected,
		}
	}
	return result
}

func containerOptions(options []logs.ContainerOption, selected int64) []webui.SelectOption {
	result := make([]webui.SelectOption, len(options))
	for index, option := range options {
		result[index] = webui.SelectOption{
			Value: strconv.FormatInt(option.ID, 10),
			Label: option.Label, Selected: option.ID == selected,
		}
	}
	return result
}

func levelChoices(selected []logs.Level) []webui.FilterChoice {
	all := []logs.Level{
		logs.LevelTrace, logs.LevelDebug, logs.LevelInfo, logs.LevelWarn,
		logs.LevelError, logs.LevelFatal, logs.LevelUnknown,
	}
	result := make([]webui.FilterChoice, len(all))
	for index, value := range all {
		result[index] = webui.FilterChoice{
			ID: "level-" + string(value), Value: string(value), Label: string(value),
			Checked: len(selected) == 0 || containsLevel(selected, value),
		}
	}
	return result
}

func streamChoices(selected []logs.Stream) []webui.FilterChoice {
	all := []logs.Stream{logs.StreamStdout, logs.StreamStderr, logs.StreamUnknown}
	result := make([]webui.FilterChoice, len(all))
	for index, value := range all {
		result[index] = webui.FilterChoice{
			ID: "stream-" + string(value), Value: string(value), Label: string(value),
			Checked: len(selected) == 0 || containsStream(selected, value),
		}
	}
	return result
}

func containsLevel(values []logs.Level, target logs.Level) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsStream(values []logs.Stream, target logs.Stream) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func joinLevels(values []logs.Level) string {
	items := make([]string, len(values))
	for index, value := range values {
		items[index] = string(value)
	}
	return strings.Join(items, ",")
}

func joinStreams(values []logs.Stream) string {
	items := make([]string, len(values))
	for index, value := range values {
		items[index] = string(value)
	}
	return strings.Join(items, ",")
}

func sourceSummary(query logs.HistoryQuery, catalog logs.SourceCatalog) string {
	var parts []string
	if query.ServerID > 0 {
		for _, server := range catalog.Servers {
			if server.ID == query.ServerID {
				parts = append(parts, server.Name)
				break
			}
		}
	}
	for _, value := range []string{
		query.Project, query.Environment, query.Application, query.Service,
	} {
		if value != "" {
			parts = append(parts, value)
		}
	}
	if len(parts) == 0 {
		return "All retained sources"
	}
	return strings.Join(parts, " / ")
}
