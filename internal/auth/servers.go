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

func (b *Browser) serversPage(w http.ResponseWriter, r *http.Request) {
	session, _ := BrowserSessionFromContext(r.Context())
	query, notice, err := parseServerPageQuery(r.URL.Query())
	if err != nil {
		b.renderServersError(w, session.CSRFToken, http.StatusBadRequest,
			"Check the Server page parameters.", "")
		return
	}
	if b.sources == nil {
		b.renderServersError(w, session.CSRFToken, http.StatusServiceUnavailable,
			"Servers are temporarily unavailable.", web.RequestIDFromContext(r.Context()))
		return
	}
	page, err := b.sources.ServerPage(r.Context(), query)
	if err != nil {
		b.renderServersError(w, session.CSRFToken, http.StatusServiceUnavailable,
			"Servers could not be read because the database is temporarily unavailable.",
			web.RequestIDFromContext(r.Context()))
		return
	}
	view := buildServersView(query, page, session.CSRFToken)
	view.Notice = notice
	b.renderServersView(w, session.CSRFToken, http.StatusOK, view)
}

func (b *Browser) serverDetailPage(w http.ResponseWriter, r *http.Request) {
	session, _ := BrowserSessionFromContext(r.Context())
	id, err := serverPathID(r)
	notice, noticeErr := serverDetailNotice(r.URL.Query())
	if err != nil || noticeErr != nil {
		b.renderServersError(w, session.CSRFToken, http.StatusBadRequest,
			"Check the Server identifier.", "")
		return
	}
	b.renderServerDetail(w, r, session.CSRFToken, id, notice,
		http.StatusOK, "", "", "")
}

func (b *Browser) serverCreate(w http.ResponseWriter, r *http.Request) {
	session, _ := BrowserSessionFromContext(r.Context())
	values, err := exactManagementForm(r, "name", "hostname")
	if err != nil {
		b.renderServersError(w, session.CSRFToken, http.StatusBadRequest,
			"Check the create-Server request.", "")
		return
	}
	if b.sources == nil {
		b.renderServersError(w, session.CSRFToken, http.StatusServiceUnavailable,
			"Servers are temporarily unavailable.", web.RequestIDFromContext(r.Context()))
		return
	}
	server, err := b.sources.CreateServer(r.Context(), values.Get("name"), values.Get("hostname"))
	if err != nil {
		if errors.Is(err, sources.ErrInvalidServerInput) ||
			errors.Is(err, sources.ErrServerNameInUse) {
			page, readErr := b.sources.ServerPage(r.Context(), sources.ServerPageQuery{})
			if readErr != nil {
				b.renderServersError(w, session.CSRFToken, http.StatusServiceUnavailable,
					"Servers are temporarily unavailable.", web.RequestIDFromContext(r.Context()))
				return
			}
			message := "Use a unique 1–128 byte UTF-8 display name and an optional hostname without control characters."
			if errors.Is(err, sources.ErrServerNameInUse) {
				message = "That Server name is already in use."
			}
			view := buildServersView(sources.ServerPageQuery{
				Limit: sources.DefaultServerPageLimit,
			}, page, session.CSRFToken)
			view.Name = values.Get("name")
			view.Hostname = values.Get("hostname")
			view.ServerError = message
			b.renderServersView(w, session.CSRFToken, http.StatusUnprocessableEntity, view)
			return
		}
		b.renderServersError(w, session.CSRFToken, http.StatusServiceUnavailable,
			"The Server could not be created because the database is temporarily unavailable.",
			web.RequestIDFromContext(r.Context()))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/servers/%d", server.ID), http.StatusSeeOther)
}

func (b *Browser) tokenCreate(w http.ResponseWriter, r *http.Request) {
	session, _ := BrowserSessionFromContext(r.Context())
	serverID, idErr := serverPathID(r)
	values, formErr := exactManagementForm(r, "name")
	if idErr != nil || formErr != nil {
		b.renderServersError(w, session.CSRFToken, http.StatusBadRequest,
			"Check the create-token request.", "")
		return
	}
	if b.sources == nil {
		b.renderServersError(w, session.CSRFToken, http.StatusServiceUnavailable,
			"Servers are temporarily unavailable.", web.RequestIDFromContext(r.Context()))
		return
	}
	created, err := b.sources.CreateToken(r.Context(), serverID, values.Get("name"))
	if err != nil {
		if errors.Is(err, sources.ErrInvalidTokenInput) ||
			errors.Is(err, sources.ErrTokenNameInUse) {
			message := "Use a unique 1–128 byte UTF-8 token name without control characters."
			if errors.Is(err, sources.ErrTokenNameInUse) {
				message = "That token name is already in use for this Server."
			}
			b.renderServerDetail(w, r, session.CSRFToken, serverID, "",
				http.StatusUnprocessableEntity, message, values.Get("name"), "")
			return
		}
		if errors.Is(err, sources.ErrServerNotFound) {
			b.renderServersError(w, session.CSRFToken, http.StatusNotFound,
				"Server not found.", "")
			return
		}
		b.renderServersError(w, session.CSRFToken, http.StatusServiceUnavailable,
			"The token could not be created because the database is temporarily unavailable.",
			web.RequestIDFromContext(r.Context()))
		return
	}
	detail, err := b.sources.ServerManagementDetail(r.Context(), serverID)
	if err != nil {
		// The plaintext must not be exposed through an error response or log.
		b.renderServersError(w, session.CSRFToken, http.StatusServiceUnavailable,
			"The token was created but its display page is unavailable. Create a replacement token.",
			web.RequestIDFromContext(r.Context()))
		return
	}
	if err := b.ui.Shell(w, http.StatusCreated, webui.ShellView{
		CSRFToken: session.CSRFToken,
		Mode:      "token-created",
		Token: webui.OneTimeTokenView{
			CSRFToken: session.CSRFToken, ServerID: serverID,
			ServerName: detail.Server.Name, TokenName: created.Name,
			Fingerprint: created.Fingerprint, Token: created.Token,
			DoneURL: fmt.Sprintf("/servers/%d", serverID),
		},
	}); err != nil {
		http.Error(w, "The token display page is unavailable. Create a replacement token.",
			http.StatusInternalServerError)
	}
}

func (b *Browser) tokenRevoke(w http.ResponseWriter, r *http.Request) {
	session, _ := BrowserSessionFromContext(r.Context())
	tokenID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	values, formErr := exactManagementForm(r, "confirmation")
	if err != nil || tokenID <= 0 || formErr != nil {
		b.renderServersError(w, session.CSRFToken, http.StatusBadRequest,
			"Check the revoke-token request.", "")
		return
	}
	if b.sources == nil {
		b.renderServersError(w, session.CSRFToken, http.StatusServiceUnavailable,
			"Servers are temporarily unavailable.", web.RequestIDFromContext(r.Context()))
		return
	}
	token, err := b.sources.TokenMetadata(r.Context(), tokenID)
	if errors.Is(err, sources.ErrTokenNotFound) {
		b.renderServersError(w, session.CSRFToken, http.StatusNotFound,
			"Ingestion token not found.", "")
		return
	}
	if err != nil {
		b.renderServersError(w, session.CSRFToken, http.StatusServiceUnavailable,
			"The token could not be read because the database is temporarily unavailable.",
			web.RequestIDFromContext(r.Context()))
		return
	}
	if token.RevokedAtUS != nil {
		http.Redirect(w, r, fmt.Sprintf("/servers/%d?notice=token-revoked", token.ServerID),
			http.StatusSeeOther)
		return
	}
	if values.Get("confirmation") != token.Name {
		b.renderServerDetail(w, r, session.CSRFToken, token.ServerID, "",
			http.StatusUnprocessableEntity, "", "",
			"Type the token name exactly. Revocation stops this token immediately.")
		return
	}
	if err := b.sources.RevokeToken(r.Context(), tokenID); err != nil {
		if errors.Is(err, sources.ErrTokenNotFound) {
			http.Redirect(w, r, fmt.Sprintf("/servers/%d?notice=token-revoked", token.ServerID),
				http.StatusSeeOther)
			return
		}
		b.renderServersError(w, session.CSRFToken, http.StatusServiceUnavailable,
			"The token could not be revoked because the database is temporarily unavailable.",
			web.RequestIDFromContext(r.Context()))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/servers/%d?notice=token-revoked", token.ServerID),
		http.StatusSeeOther)
}

func (b *Browser) renderServerDetail(
	w http.ResponseWriter,
	r *http.Request,
	csrfToken string,
	serverID int64,
	notice string,
	status int,
	tokenError, tokenName, revokeError string,
) {
	if b.sources == nil {
		b.renderServersError(w, csrfToken, http.StatusServiceUnavailable,
			"Servers are temporarily unavailable.", web.RequestIDFromContext(r.Context()))
		return
	}
	detail, err := b.sources.ServerManagementDetail(r.Context(), serverID)
	if errors.Is(err, sources.ErrServerNotFound) {
		b.renderServersError(w, csrfToken, http.StatusNotFound, "Server not found.", "")
		return
	}
	if err != nil {
		b.renderServersError(w, csrfToken, http.StatusServiceUnavailable,
			"Server details could not be read because the database is temporarily unavailable.",
			web.RequestIDFromContext(r.Context()))
		return
	}
	view := buildServerDetailView(detail, csrfToken)
	view.Notice = notice
	view.TokenError = tokenError
	view.TokenName = tokenName
	view.RevokeError = revokeError
	if err := b.ui.Shell(w, status, webui.ShellView{
		CSRFToken: csrfToken, Mode: "server-detail", Server: view,
	}); err != nil {
		http.Error(w, "Servers are temporarily unavailable.", http.StatusInternalServerError)
	}
}

func parseServerPageQuery(values url.Values) (sources.ServerPageQuery, string, error) {
	for key := range values {
		if key != "after" && key != "limit" && key != "notice" {
			return sources.ServerPageQuery{}, "", errors.New("unknown Server parameter")
		}
	}
	query := sources.ServerPageQuery{Limit: sources.DefaultServerPageLimit}
	if entries := values["after"]; len(entries) > 1 {
		return sources.ServerPageQuery{}, "", errors.New("after must occur once")
	} else if len(entries) == 1 {
		after, err := strconv.ParseInt(entries[0], 10, 64)
		if err != nil || after <= 0 {
			return sources.ServerPageQuery{}, "", errors.New("invalid Server cursor")
		}
		query.AfterID = after
	}
	if entries := values["limit"]; len(entries) > 1 {
		return sources.ServerPageQuery{}, "", errors.New("limit must occur once")
	} else if len(entries) == 1 {
		limit, err := strconv.Atoi(entries[0])
		if err != nil || limit < 1 || limit > sources.MaxServerPageLimit {
			return sources.ServerPageQuery{}, "", errors.New("invalid Server limit")
		}
		query.Limit = limit
	}
	notice := ""
	if entries := values["notice"]; len(entries) > 1 {
		return sources.ServerPageQuery{}, "", errors.New("notice must occur once")
	} else if len(entries) == 1 {
		if entries[0] != "server-created" {
			return sources.ServerPageQuery{}, "", errors.New("invalid Server notice")
		}
		notice = "Server created. Create an ingestion token before configuring a sender."
	}
	return query, notice, nil
}

func serverDetailNotice(values url.Values) (string, error) {
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 || len(values["notice"]) != 1 ||
		values.Get("notice") != "token-revoked" {
		return "", errors.New("invalid Server notice")
	}
	return "Token revoked. New ingestion requests using it fail immediately.", nil
}

func exactManagementForm(r *http.Request, fields ...string) (url.Values, error) {
	allowed := map[string]struct{}{"csrf_token": {}}
	for _, field := range fields {
		allowed[field] = struct{}{}
	}
	for key, values := range r.PostForm {
		if _, ok := allowed[key]; !ok || len(values) != 1 {
			return nil, errors.New("invalid management form")
		}
	}
	if len(r.PostForm["csrf_token"]) != 1 {
		return nil, errors.New("missing CSRF field")
	}
	for _, field := range fields {
		if len(r.PostForm[field]) != 1 {
			return nil, errors.New("missing management field")
		}
	}
	return r.PostForm, nil
}

func serverPathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid Server identifier")
	}
	return id, nil
}

func buildServersView(
	query sources.ServerPageQuery,
	page sources.ServerPage,
	csrfToken string,
) webui.ServersView {
	view := webui.ServersView{
		CSRFToken: csrfToken,
		Rows:      make([]webui.ServerRowView, len(page.Servers)),
	}
	for index, server := range page.Servers {
		view.Rows[index] = serverRowView(server)
	}
	if page.HasMore {
		values := url.Values{
			"after": {strconv.FormatInt(page.NextAfter, 10)},
			"limit": {strconv.Itoa(query.Limit)},
		}
		view.NextURL = "/servers?" + values.Encode()
	}
	return view
}

func serverRowView(server sources.ServerSummary) webui.ServerRowView {
	lastEvent := "No events received"
	if server.LastEventAtUS != nil {
		lastEvent = formatManagementTime(*server.LastEventAtUS)
	}
	return webui.ServerRowView{
		ID: server.ID, DetailURL: fmt.Sprintf("/servers/%d", server.ID),
		Name: server.Name, Hostname: server.Hostname,
		SourceCount: server.SourceCount, TokenCount: server.ActiveTokenCount,
		LastEvent: lastEvent,
	}
}

func buildServerDetailView(
	detail sources.ServerManagementDetail,
	csrfToken string,
) webui.ServerDetailView {
	view := webui.ServerDetailView{
		ServerRowView:   serverRowView(detail.Server),
		CSRFToken:       csrfToken,
		TokensTruncated: detail.TokensTruncated,
		Tokens:          make([]webui.TokenRowView, len(detail.Tokens)),
	}
	for index, token := range detail.Tokens {
		lastUsed := "Never"
		if token.LastUsedAtUS != nil {
			lastUsed = formatManagementTime(*token.LastUsedAtUS)
		}
		revoked := ""
		if token.RevokedAtUS != nil {
			revoked = formatManagementTime(*token.RevokedAtUS)
		}
		view.Tokens[index] = webui.TokenRowView{
			ID: token.ID, Name: token.Name, Fingerprint: token.Fingerprint,
			Created: formatManagementTime(token.CreatedAtUS), LastUsed: lastUsed,
			Revoked: revoked, Active: token.RevokedAtUS == nil,
		}
	}
	return view
}

func formatManagementTime(value int64) string {
	return time.UnixMicro(value).UTC().Format("02 Jan 2006 15:04:05 UTC")
}

func (b *Browser) renderServersView(
	w http.ResponseWriter,
	csrfToken string,
	status int,
	view webui.ServersView,
) {
	if err := b.ui.Shell(w, status, webui.ShellView{
		CSRFToken: csrfToken, Mode: "servers", Servers: view,
	}); err != nil {
		http.Error(w, "Servers are temporarily unavailable.", http.StatusInternalServerError)
	}
}

func (b *Browser) renderServersError(
	w http.ResponseWriter,
	csrfToken string,
	status int,
	message, requestID string,
) {
	b.renderServersView(w, csrfToken, status, webui.ServersView{
		Error: message, ErrorRequestID: requestID,
	})
}
