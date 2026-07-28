package auth

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/drilonrecica/siftail/internal/sources"
	"github.com/drilonrecica/siftail/internal/web"
	webui "github.com/drilonrecica/siftail/internal/web/ui"
)

func (b *Browser) sourcesPage(w http.ResponseWriter, r *http.Request) {
	session, _ := BrowserSessionFromContext(r.Context())
	query, err := parseSourceCatalogQuery(r.URL.Query())
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
		Sources:   buildSourcesView(query, page),
	}); err != nil {
		http.Error(w, "Sources are temporarily unavailable.", http.StatusInternalServerError)
	}
}

func (b *Browser) sourceDetailPage(w http.ResponseWriter, r *http.Request) {
	session, _ := BrowserSessionFromContext(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 || len(r.URL.Query()) != 0 {
		b.renderSourcesError(w, session.CSRFToken, http.StatusBadRequest,
			"Check the source identifier.", "")
		return
	}
	if b.sources == nil {
		b.renderSourcesError(w, session.CSRFToken, http.StatusServiceUnavailable,
			"Sources are temporarily unavailable.", web.RequestIDFromContext(r.Context()))
		return
	}
	detail, err := b.sources.SourceDetail(r.Context(), id)
	if errors.Is(err, sources.ErrSourceNotFound) {
		b.renderSourcesError(w, session.CSRFToken, http.StatusNotFound,
			"Source not found.", "")
		return
	}
	if err != nil {
		b.renderSourcesError(w, session.CSRFToken, http.StatusServiceUnavailable,
			"Source details could not be read because the database is temporarily unavailable.",
			web.RequestIDFromContext(r.Context()))
		return
	}
	if err := b.ui.Shell(w, http.StatusOK, webui.ShellView{
		CSRFToken: session.CSRFToken,
		Mode:      "source-detail",
		Source:    buildSourceDetailView(detail),
	}); err != nil {
		http.Error(w, "Sources are temporarily unavailable.", http.StatusInternalServerError)
	}
}

func parseSourceCatalogQuery(values url.Values) (sources.CatalogQuery, error) {
	for key := range values {
		if key != "after" && key != "limit" {
			return sources.CatalogQuery{}, fmt.Errorf("unknown source parameter %q", key)
		}
	}
	query := sources.CatalogQuery{Limit: sources.DefaultCatalogLimit}
	if entries := values["after"]; len(entries) > 1 {
		return sources.CatalogQuery{}, errors.New("after must occur once")
	} else if len(entries) == 1 {
		after, err := strconv.ParseInt(entries[0], 10, 64)
		if err != nil || after <= 0 {
			return sources.CatalogQuery{}, errors.New("after must be a positive integer")
		}
		query.AfterID = after
	}
	if entries := values["limit"]; len(entries) > 1 {
		return sources.CatalogQuery{}, errors.New("limit must occur once")
	} else if len(entries) == 1 {
		limit, err := strconv.Atoi(entries[0])
		if err != nil || limit < 1 || limit > sources.MaxCatalogLimit {
			return sources.CatalogQuery{}, errors.New("invalid source limit")
		}
		query.Limit = limit
	}
	return query, nil
}

func buildSourcesView(query sources.CatalogQuery, page sources.CatalogPage) webui.SourcesView {
	view := webui.SourcesView{
		LoadedCount: len(page.Sources),
		Rows:        make([]webui.SourceRowView, len(page.Sources)),
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

func buildSourceDetailView(detail sources.SourceDetail) webui.SourceDetailView {
	source := detail.Source
	view := webui.SourceDetailView{
		SourceRowView:       sourceRowView(source),
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
