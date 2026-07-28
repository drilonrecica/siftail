package auth

import (
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
	session, _ := BrowserSessionFromContext(r.Context())
	values := cloneURLValues(r.URL.Query())
	query, err := logs.ParseHistoryQuery(values, b.now())
	if err != nil {
		b.renderHistoryShellError(w, session.CSRFToken, http.StatusBadRequest,
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
		b.renderHistoryShellError(w, session.CSRFToken, status, message, requestID)
		return
	}
	if err := b.ui.Shell(w, http.StatusOK, webui.ShellView{
		CSRFToken: session.CSRFToken,
		Mode:      "history",
		History:   view,
	}); err != nil {
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
	appendPage := hasAppend
	values.Del("append")
	query, err := logs.ParseHistoryQuery(values, b.now())
	if err != nil || (appendPage && query.Cursor == "") {
		view := webui.HistoryView{
			CanonicalURL: "/logs",
			Error:        "Check the time range and filters.",
		}
		historyErrorHeaders(w)
		_ = b.ui.HistoryError(w, http.StatusBadRequest, view)
		return
	}
	view, err := b.historyView(r, query, appendPage)
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
	view := buildHistoryView(query, page, catalog)
	if appendPage {
		noun := "events"
		if len(view.Rows) == 1 {
			noun = "event"
		}
		view.Announcement = fmt.Sprintf("%d additional %s loaded.", len(view.Rows), noun)
	}
	return view, nil
}

func buildHistoryView(
	query logs.HistoryQuery,
	page logs.HistoryPage,
	catalog logs.SourceCatalog,
) webui.HistoryView {
	from := time.UnixMicro(query.FromUS).UTC()
	to := time.UnixMicro(query.ToUS).UTC()
	view := webui.HistoryView{
		CanonicalURL: historyURL(query),
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
	switch {
	case len(catalog.Servers) == 0:
		view.EmptyTitle = "No log sources have been discovered yet"
		view.EmptyMessage = "Create a server token and connect a Coolify or Fluent Bit log drain."
	case historyHasFilters(query):
		view.EmptyTitle = "No logs matched these filters"
		view.EmptyMessage = "Try a longer time range or remove a message filter."
	default:
		view.EmptyTitle = "No logs arrived during this time range"
		view.EmptyMessage = "Try the last 24 hours or select another source."
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
	if page.HasMore {
		next := query
		next.Cursor = page.NextCursor
		values := next.CanonicalValues(true)
		values.Set("append", "1")
		view.NextURL = "/logs/rows?" + values.Encode()
	}
	return view
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
	csrfToken string,
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
	if err := b.ui.Shell(w, status, webui.ShellView{
		CSRFToken: csrfToken,
		Mode:      "history",
		History:   view,
	}); err != nil {
		http.Error(w, "Logs are temporarily unavailable.", http.StatusInternalServerError)
	}
}

func historyHTTPError(r *http.Request, err error) (int, string, string) {
	if errors.Is(err, logs.ErrInvalidHistoryCursor) {
		return http.StatusBadRequest, "The page cursor is invalid for these filters.", ""
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
