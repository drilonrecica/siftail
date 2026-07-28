package auth

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/drilonrecica/siftail/internal/audit"
	"github.com/drilonrecica/siftail/internal/web"
	webui "github.com/drilonrecica/siftail/internal/web/ui"
)

const auditTimeLayout = "2006-01-02T15:04:05Z"

func (b *Browser) auditPage(w http.ResponseWriter, r *http.Request) {
	session, _ := BrowserSessionFromContext(r.Context())
	query, redirectURL, err := parseAuditPageQuery(r.URL.Query(), b.now())
	if err != nil {
		b.renderAudit(w, session.CSRFToken, http.StatusBadRequest, webui.AuditView{
			Error: "Check the Audit time range and filters.",
		})
		return
	}
	if redirectURL != "" {
		http.Redirect(w, r, redirectURL, http.StatusSeeOther)
		return
	}
	if b.audit == nil {
		b.renderAudit(w, session.CSRFToken, http.StatusServiceUnavailable, webui.AuditView{
			Error:          "Security audit history is temporarily unavailable.",
			ErrorRequestID: web.RequestIDFromContext(r.Context()),
		})
		return
	}
	page, err := b.audit.List(r.Context(), query)
	if err != nil {
		b.renderAudit(w, session.CSRFToken, http.StatusServiceUnavailable, webui.AuditView{
			Error:          "Security audit history could not be read because local storage is temporarily unavailable.",
			ErrorRequestID: web.RequestIDFromContext(r.Context()),
		})
		return
	}
	view := buildAuditView(query, page)
	b.renderAudit(w, session.CSRFToken, http.StatusOK, view)
}

func (b *Browser) renderAudit(
	w http.ResponseWriter,
	csrfToken string,
	status int,
	view webui.AuditView,
) {
	if err := b.ui.Shell(w, status, webui.ShellView{
		CSRFToken: csrfToken, Mode: "audit", Audit: view,
	}); err != nil {
		http.Error(w, "Security audit history is temporarily unavailable.",
			http.StatusInternalServerError)
	}
}

func parseAuditPageQuery(
	values url.Values,
	now time.Time,
) (audit.Query, string, error) {
	allowed := map[string]bool{
		"from": true, "to": true, "category": true, "action": true,
		"outcome": true, "before_at": true, "before_id": true,
	}
	for key, entries := range values {
		if !allowed[key] || len(entries) != 1 {
			return audit.Query{}, "", audit.ErrInvalidQuery
		}
	}
	if len(values) == 0 {
		// The query format has second precision while audit events have
		// microsecond precision. Round the exclusive end up so actions from
		// the current second are visible on the default page.
		to := now.UTC().Truncate(time.Second).Add(time.Second)
		from := to.Add(-30 * 24 * time.Hour)
		defaults := url.Values{
			"from": {from.Format(auditTimeLayout)},
			"to":   {to.Format(auditTimeLayout)},
		}
		return audit.Query{}, "/audit?" + defaults.Encode(), nil
	}
	from, err := time.Parse(auditTimeLayout, values.Get("from"))
	if err != nil {
		return audit.Query{}, "", audit.ErrInvalidQuery
	}
	to, err := time.Parse(auditTimeLayout, values.Get("to"))
	if err != nil {
		return audit.Query{}, "", audit.ErrInvalidQuery
	}
	query := audit.Query{
		Category:         audit.Category(values.Get("category")),
		Action:           values.Get("action"),
		Outcome:          audit.Outcome(values.Get("outcome")),
		FromOccurredAtUS: from.UnixMicro(), ToOccurredAtUS: to.UnixMicro(),
		Limit: audit.DefaultPageLimit,
	}
	if beforeAt := values.Get("before_at"); beforeAt != "" {
		query.BeforeOccurredAtUS, err = strconv.ParseInt(beforeAt, 10, 64)
		if err != nil {
			return audit.Query{}, "", audit.ErrInvalidQuery
		}
	}
	if beforeID := values.Get("before_id"); beforeID != "" {
		query.BeforeID, err = strconv.ParseInt(beforeID, 10, 64)
		if err != nil {
			return audit.Query{}, "", audit.ErrInvalidQuery
		}
	}
	// Store validation remains authoritative for enums, cursors, and range.
	if _, err := bAuditValidateQuery(query); err != nil {
		return audit.Query{}, "", err
	}
	return query, "", nil
}

func bAuditValidateQuery(query audit.Query) (audit.Query, error) {
	// Validation uses the concrete store without issuing SQL.
	if query.FromOccurredAtUS <= 0 || query.ToOccurredAtUS <= 0 ||
		query.FromOccurredAtUS >= query.ToOccurredAtUS ||
		query.ToOccurredAtUS-query.FromOccurredAtUS >
			int64(366*24*time.Hour/time.Microsecond) {
		return audit.Query{}, audit.ErrInvalidQuery
	}
	for _, category := range auditCategoryOptions() {
		if query.Category == "" || query.Category == audit.Category(category.Value) {
			goto categoryValid
		}
	}
	return audit.Query{}, audit.ErrInvalidQuery
categoryValid:
	for _, outcome := range auditOutcomeOptions() {
		if query.Outcome == "" || query.Outcome == audit.Outcome(outcome.Value) {
			goto outcomeValid
		}
	}
	return audit.Query{}, audit.ErrInvalidQuery
outcomeValid:
	if query.Action != "" {
		if len(query.Action) > 128 {
			return audit.Query{}, audit.ErrInvalidQuery
		}
		for index, char := range query.Action {
			if (index == 0 && (char < 'a' || char > 'z')) ||
				(index > 0 && !((char >= 'a' && char <= 'z') ||
					(char >= '0' && char <= '9') || char == '_' ||
					char == '.' || char == '-')) {
				return audit.Query{}, audit.ErrInvalidQuery
			}
		}
	}
	if (query.BeforeOccurredAtUS == 0) != (query.BeforeID == 0) ||
		query.BeforeOccurredAtUS < 0 || query.BeforeID < 0 {
		return audit.Query{}, audit.ErrInvalidQuery
	}
	return query, nil
}

func buildAuditView(query audit.Query, page audit.Page) webui.AuditView {
	view := webui.AuditView{
		From:       queryTime(query.FromOccurredAtUS),
		To:         queryTime(query.ToOccurredAtUS),
		Action:     query.Action,
		Categories: auditCategoryOptions(),
		Outcomes:   auditOutcomeOptions(),
		Rows:       make([]webui.AuditRowView, len(page.Events)),
	}
	selectAuditOption(view.Categories, string(query.Category))
	selectAuditOption(view.Outcomes, string(query.Outcome))
	for index, event := range page.Events {
		view.Rows[index] = webui.AuditRowView{
			Time: formatStatusTime(event.OccurredAt), Category: string(event.Category),
			Action: event.Action, Outcome: string(event.Outcome),
			Actor: auditActorLabel(event), Summary: auditSummary(event),
			RequestID: event.RequestID,
		}
	}
	if page.HasMore {
		values := url.Values{
			"from": {view.From}, "to": {view.To},
			"before_at": {strconv.FormatInt(page.NextBeforeOccurredAtUS, 10)},
			"before_id": {strconv.FormatInt(page.NextBeforeID, 10)},
		}
		if query.Category != "" {
			values.Set("category", string(query.Category))
		}
		if query.Action != "" {
			values.Set("action", query.Action)
		}
		if query.Outcome != "" {
			values.Set("outcome", string(query.Outcome))
		}
		view.NextURL = "/audit?" + values.Encode()
	}
	return view
}

func auditActorLabel(event audit.Event) string {
	label := strings.ReplaceAll(string(event.ActorType), "_", " ")
	if event.AdministratorID != nil {
		label += " #" + strconv.FormatInt(*event.AdministratorID, 10)
	}
	return label
}

func auditSummary(event audit.Event) string {
	parts := make([]string, 0, len(event.Metadata)+2)
	if event.ServerID != nil {
		parts = append(parts, "Server #"+strconv.FormatInt(*event.ServerID, 10))
	}
	if event.SourceID != nil {
		parts = append(parts, "Source #"+strconv.FormatInt(*event.SourceID, 10))
	}
	keys := make([]string, 0, len(event.Metadata))
	for key := range event.Metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts = append(parts,
			strings.ReplaceAll(key, "_", " ")+"="+event.Metadata[key])
	}
	if len(parts) == 0 {
		return "No additional context"
	}
	return strings.Join(parts, " · ")
}

func auditCategoryOptions() []webui.SelectOption {
	return []webui.SelectOption{
		{Value: "", Label: "All categories"},
		{Value: string(audit.CategoryAuthentication), Label: "Authentication"},
		{Value: string(audit.CategorySession), Label: "Sessions"},
		{Value: string(audit.CategoryAdministratorCredential), Label: "Administrator credentials"},
		{Value: string(audit.CategoryIngestionToken), Label: "Servers and ingestion tokens"},
		{Value: string(audit.CategorySourceAdministration), Label: "Source administration"},
		{Value: string(audit.CategoryRetentionSettings), Label: "Retention settings"},
		{Value: string(audit.CategoryBackupRestore), Label: "Backup and restore"},
		{Value: string(audit.CategoryExport), Label: "Export"},
		{Value: string(audit.CategoryProxyAuthConfiguration), Label: "Proxy authentication"},
		{Value: string(audit.CategoryDestructiveOperation), Label: "Destructive operations"},
	}
}

func auditOutcomeOptions() []webui.SelectOption {
	return []webui.SelectOption{
		{Value: "", Label: "All outcomes"},
		{Value: string(audit.OutcomeSucceeded), Label: "Succeeded"},
		{Value: string(audit.OutcomeFailed), Label: "Failed"},
		{Value: string(audit.OutcomeRejected), Label: "Rejected"},
		{Value: string(audit.OutcomeCanceled), Label: "Canceled"},
	}
}

func selectAuditOption(options []webui.SelectOption, selected string) {
	for index := range options {
		options[index].Selected = options[index].Value == selected
	}
}

func queryTime(value int64) string {
	return time.UnixMicro(value).UTC().Format(auditTimeLayout)
}
