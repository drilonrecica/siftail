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
	"github.com/drilonrecica/siftail/internal/sources"
	"github.com/drilonrecica/siftail/internal/web"
	webui "github.com/drilonrecica/siftail/internal/web/ui"
)

func (b *Browser) sourcesPage(w http.ResponseWriter, r *http.Request) {
	session, _ := BrowserSessionFromContext(r.Context())
	query, notice, err := parseSourceCatalogQuery(r.URL.Query())
	if err != nil {
		b.renderSourcesError(w, session.CSRFToken, http.StatusBadRequest,
			"Check the source page parameters.", "")
		return
	}
	if b.sources == nil {
		b.renderSourcesError(w, session.CSRFToken, http.StatusServiceUnavailable,
			"Sources are temporarily unavailable.", web.RequestIDFromContext(r.Context()))
		return
	}
	page, err := b.sources.Catalog(r.Context(), query)
	if err != nil {
		b.renderSourcesError(w, session.CSRFToken, http.StatusServiceUnavailable,
			"Sources could not be read because the database is temporarily unavailable.",
			web.RequestIDFromContext(r.Context()))
		return
	}
	if err := b.ui.Shell(w, http.StatusOK, webui.ShellView{
		CSRFToken: session.CSRFToken,
		Mode:      "sources",
		Sources:   buildSourcesView(query, page, notice),
	}); err != nil {
		http.Error(w, "Sources are temporarily unavailable.", http.StatusInternalServerError)
	}
}

func (b *Browser) sourceDetailPage(w http.ResponseWriter, r *http.Request) {
	session, _ := BrowserSessionFromContext(r.Context())
	id, idErr := sourcePathID(r)
	notice, noticeErr := sourceDetailNotice(r.URL.Query())
	if idErr != nil || noticeErr != nil {
		b.renderSourcesError(w, session.CSRFToken, http.StatusBadRequest,
			"Check the source identifier.", "")
		return
	}
	b.renderSourceDetail(w, r, session.CSRFToken, id, notice,
		http.StatusOK, "", "", "")
}

func (b *Browser) sourceAlias(w http.ResponseWriter, r *http.Request) {
	session, _ := BrowserSessionFromContext(r.Context())
	id, err := sourcePathID(r)
	alias, formErr := sourceMutationValue(r, "alias")
	if err != nil || formErr != nil {
		b.renderSourcesError(w, session.CSRFToken, http.StatusBadRequest,
			"Check the source alias request.", "")
		return
	}
	if b.sources == nil {
		b.renderSourcesError(w, session.CSRFToken, http.StatusServiceUnavailable,
			"Sources are temporarily unavailable.", web.RequestIDFromContext(r.Context()))
		return
	}
	if err := b.sources.SetAlias(r.Context(), id, alias); err != nil {
		if errors.Is(err, sources.ErrInvalidAlias) {
			b.renderSourceDetail(w, r, session.CSRFToken, id, "",
				http.StatusUnprocessableEntity,
				"Use 1–128 UTF-8 bytes without control characters, or leave the alias empty.",
				"", "")
			return
		}
		b.renderSourceMutationFailure(w, r, session.CSRFToken, err)
		return
	}
	notice := "alias-updated"
	if strings.TrimSpace(alias) == "" {
		notice = "alias-removed"
	}
	http.Redirect(w, r, fmt.Sprintf("/sources/%d?notice=%s", id, notice),
		http.StatusSeeOther)
}

func (b *Browser) sourceClearLogs(w http.ResponseWriter, r *http.Request) {
	session, _ := BrowserSessionFromContext(r.Context())
	id, err := sourcePathID(r)
	confirmation, formErr := sourceMutationValue(r, "confirmation")
	if err != nil || formErr != nil {
		b.renderSourcesError(w, session.CSRFToken, http.StatusBadRequest,
			"Check the clear-logs request.", "")
		return
	}
	detail, err := b.readSourceForMutation(r, id)
	if err != nil {
		b.renderSourceMutationFailure(w, r, session.CSRFToken, err)
		return
	}
	if confirmation != detail.Source.DisplayName() {
		b.renderSourceDetailView(w, session.CSRFToken, detail, "",
			http.StatusUnprocessableEntity, "",
			"Type the displayed source name exactly to clear retained logs.", "")
		return
	}
	if _, err := b.sources.ClearLogs(r.Context(), id); err != nil {
		b.renderSourceMutationFailure(w, r, session.CSRFToken, err)
		return
	}
	if b.live != nil {
		b.live.TryPublishControl(logs.LiveControl{
			Type: logs.LiveControlSourcePurged, SourceID: id,
		})
	}
	http.Redirect(w, r, fmt.Sprintf("/sources/%d?notice=logs-cleared", id),
		http.StatusSeeOther)
}

func (b *Browser) sourceRemove(w http.ResponseWriter, r *http.Request) {
	session, _ := BrowserSessionFromContext(r.Context())
	id, err := sourcePathID(r)
	confirmation, formErr := sourceMutationValue(r, "confirmation")
	if err != nil || formErr != nil {
		b.renderSourcesError(w, session.CSRFToken, http.StatusBadRequest,
			"Check the remove-source request.", "")
		return
	}
	detail, err := b.readSourceForMutation(r, id)
	if err != nil {
		b.renderSourceMutationFailure(w, r, session.CSRFToken, err)
		return
	}
	if confirmation != "remove "+detail.Source.DisplayName() {
		b.renderSourceDetailView(w, session.CSRFToken, detail, "",
			http.StatusUnprocessableEntity, "", "",
			"Type the complete removal phrase exactly.")
		return
	}
	result, err := b.sources.RemoveSource(r.Context(), id)
	if err != nil {
		b.renderSourceMutationFailure(w, r, session.CSRFToken, err)
		return
	}
	controlType := logs.LiveControlSourcePurged
	if result.Removed {
		controlType = logs.LiveControlSourceRemoved
	}
	if b.live != nil {
		b.live.TryPublishControl(logs.LiveControl{Type: controlType, SourceID: id})
	}
	if result.Removed {
		http.Redirect(w, r, "/sources?notice=source-removed", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/sources/%d?notice=new-events-remain", id),
		http.StatusSeeOther)
}

func (b *Browser) readSourceForMutation(
	r *http.Request,
	id int64,
) (sources.SourceDetail, error) {
	if b.sources == nil {
		return sources.SourceDetail{}, errors.New("source store is unavailable")
	}
	return b.sources.SourceDetail(r.Context(), id)
}

func (b *Browser) renderSourceMutationFailure(
	w http.ResponseWriter,
	r *http.Request,
	csrfToken string,
	err error,
) {
	if errors.Is(err, sources.ErrSourceNotFound) {
		b.renderSourcesError(w, csrfToken, http.StatusNotFound, "Source not found.", "")
		return
	}
	b.renderSourcesError(w, csrfToken, http.StatusServiceUnavailable,
		"The source operation could not be completed because the database is temporarily unavailable.",
		web.RequestIDFromContext(r.Context()))
}

func (b *Browser) renderSourceDetail(
	w http.ResponseWriter,
	r *http.Request,
	csrfToken string,
	id int64,
	notice string,
	status int,
	aliasError, clearError, removeError string,
) {
	if b.sources == nil {
		b.renderSourcesError(w, csrfToken, http.StatusServiceUnavailable,
			"Sources are temporarily unavailable.", web.RequestIDFromContext(r.Context()))
		return
	}
	detail, err := b.sources.SourceDetail(r.Context(), id)
	if errors.Is(err, sources.ErrSourceNotFound) {
		b.renderSourcesError(w, csrfToken, http.StatusNotFound, "Source not found.", "")
		return
	}
	if err != nil {
		b.renderSourcesError(w, csrfToken, http.StatusServiceUnavailable,
			"Source details could not be read because the database is temporarily unavailable.",
			web.RequestIDFromContext(r.Context()))
		return
	}
	b.renderSourceDetailView(w, csrfToken, detail, notice, status,
		aliasError, clearError, removeError)
}

func (b *Browser) renderSourceDetailView(
	w http.ResponseWriter,
	csrfToken string,
	detail sources.SourceDetail,
	notice string,
	status int,
	aliasError, clearError, removeError string,
) {
	view := buildSourceDetailView(detail, csrfToken, notice)
	view.AliasError = aliasError
	view.ClearError = clearError
	view.RemoveError = removeError
	if err := b.ui.Shell(w, status, webui.ShellView{
		CSRFToken: csrfToken,
		Mode:      "source-detail",
		Source:    view,
	}); err != nil {
		http.Error(w, "Sources are temporarily unavailable.", http.StatusInternalServerError)
	}
}

func sourceMutationValue(r *http.Request, field string) (string, error) {
	for key, values := range r.PostForm {
		if key != "csrf_token" && key != field {
			return "", fmt.Errorf("unknown source form field %q", key)
		}
		if len(values) != 1 {
			return "", fmt.Errorf("source form field %q must occur once", key)
		}
	}
	if len(r.PostForm["csrf_token"]) != 1 || len(r.PostForm[field]) != 1 {
		return "", errors.New("required source form field is missing")
	}
	return r.PostForm.Get(field), nil
}

func sourcePathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid source identifier")
	}
	return id, nil
}

func parseSourceCatalogQuery(values url.Values) (sources.CatalogQuery, string, error) {
	for key := range values {
		if key != "after" && key != "limit" && key != "notice" {
			return sources.CatalogQuery{}, "", fmt.Errorf("unknown source parameter %q", key)
		}
	}
	query := sources.CatalogQuery{Limit: sources.DefaultCatalogLimit}
	if entries := values["after"]; len(entries) > 1 {
		return sources.CatalogQuery{}, "", errors.New("after must occur once")
	} else if len(entries) == 1 {
		after, err := strconv.ParseInt(entries[0], 10, 64)
		if err != nil || after <= 0 {
			return sources.CatalogQuery{}, "", errors.New("after must be a positive integer")
		}
		query.AfterID = after
	}
	if entries := values["limit"]; len(entries) > 1 {
		return sources.CatalogQuery{}, "", errors.New("limit must occur once")
	} else if len(entries) == 1 {
		limit, err := strconv.Atoi(entries[0])
		if err != nil || limit < 1 || limit > sources.MaxCatalogLimit {
			return sources.CatalogQuery{}, "", errors.New("invalid source limit")
		}
		query.Limit = limit
	}
	notice := ""
	if entries := values["notice"]; len(entries) > 1 {
		return sources.CatalogQuery{}, "", errors.New("notice must occur once")
	} else if len(entries) == 1 {
		if entries[0] != "source-removed" {
			return sources.CatalogQuery{}, "", errors.New("invalid source notice")
		}
		notice = "Source removed. An active sender may discover it again."
	}
	return query, notice, nil
}

func sourceDetailNotice(values url.Values) (string, error) {
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 || len(values["notice"]) != 1 {
		return "", errors.New("invalid source notice")
	}
	switch values.Get("notice") {
	case "alias-updated":
		return "Source alias updated. Stable identity and stored events were unchanged.", nil
	case "alias-removed":
		return "Source alias removed. Stable identity and stored events were unchanged.", nil
	case "logs-cleared":
		return "Retained logs were cleared. The source, alias, and container observations remain.", nil
	case "new-events-remain":
		return "Events committed after removal started remain. The alias and unreferenced container observations were removed.", nil
	default:
		return "", errors.New("invalid source notice")
	}
}

func buildSourcesView(
	query sources.CatalogQuery,
	page sources.CatalogPage,
	notice string,
) webui.SourcesView {
	view := webui.SourcesView{
		LoadedCount: len(page.Sources),
		Rows:        make([]webui.SourceRowView, len(page.Sources)),
		Notice:      notice,
	}
	for index, source := range page.Sources {
		view.Rows[index] = sourceRowView(source)
	}
	if page.HasMore {
		values := url.Values{
			"after": {strconv.FormatInt(page.NextAfter, 10)},
			"limit": {strconv.Itoa(query.Limit)},
		}
		view.NextURL = "/sources?" + values.Encode()
	}
	return view
}

func sourceRowView(source sources.CatalogSource) webui.SourceRowView {
	status := "Inactive"
	if source.Active {
		status = "Active"
	}
	retained := "No retained logs"
	if source.HasRetainedEvents {
		retained = "Retained logs"
	}
	return webui.SourceRowView{
		ID:          source.ID,
		DetailURL:   fmt.Sprintf("/sources/%d", source.ID),
		DisplayName: source.DisplayName(),
		Server:      source.ServerName,
		Project:     source.ProjectLabel,
		Environment: source.EnvironmentLabel,
		Application: source.ApplicationLabel,
		Service:     source.ServiceLabel,
		Alias:       source.Alias != nil,
		Status:      status,
		Active:      source.Active,
		FirstSeen:   formatSourceTime(source.FirstSeenAtUS),
		LastSeen:    formatSourceTime(source.LastSeenAtUS),
		Retained:    retained,
	}
}

func buildSourceDetailView(
	detail sources.SourceDetail,
	csrfToken, notice string,
) webui.SourceDetailView {
	source := detail.Source
	view := webui.SourceDetailView{
		SourceRowView:       sourceRowView(source),
		CSRFToken:           csrfToken,
		Notice:              notice,
		ServerHostname:      source.ServerHostname,
		ProjectKey:          source.ProjectKey,
		EnvironmentKey:      source.EnvironmentKey,
		ApplicationKey:      source.ApplicationKey,
		ServiceKey:          source.ServiceKey,
		CleanupEligible:     source.CleanupEligible,
		ContainersTruncated: detail.ContainersTruncated,
		LogsURL:             sourceLogsURL(source),
		Containers:          make([]webui.ContainerObservationView, len(detail.Containers)),
	}
	if source.Alias != nil {
		view.AliasValue = *source.Alias
	}
	for index, container := range detail.Containers {
		status := "Inactive"
		if container.Active {
			status = "Active"
		}
		identity := container.ContainerName
		if identity == "" {
			identity = container.ContainerID
		} else if container.ContainerID != "" {
			identity += " · " + container.ContainerID
		}
		view.Containers[index] = webui.ContainerObservationView{
			Identity:  identity,
			Status:    status,
			Active:    container.Active,
			FirstSeen: formatSourceTime(container.FirstSeenAtUS),
			LastSeen:  formatSourceTime(container.LastSeenAtUS),
		}
	}
	return view
}

func sourceLogsURL(source sources.CatalogSource) string {
	values := url.Values{
		"mode":        {"history"},
		"server":      {strconv.FormatInt(source.ServerID, 10)},
		"project":     {source.ProjectKey},
		"environment": {source.EnvironmentKey},
		"application": {source.ApplicationKey},
		"service":     {source.ServiceKey},
	}
	return "/logs?" + values.Encode()
}

func formatSourceTime(value int64) string {
	return time.UnixMicro(value).UTC().Format("02 Jan 2006 15:04:05 UTC")
}

func (b *Browser) renderSourcesError(
	w http.ResponseWriter,
	csrfToken string,
	status int,
	message, requestID string,
) {
	if err := b.ui.Shell(w, status, webui.ShellView{
		CSRFToken: csrfToken,
		Mode:      "sources",
		Sources: webui.SourcesView{
			Error: message, ErrorRequestID: requestID,
		},
	}); err != nil {
		http.Error(w, "Sources are temporarily unavailable.", http.StatusInternalServerError)
	}
}
