package auth

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/drilonrecica/siftail/internal/retention"
	"github.com/drilonrecica/siftail/internal/web"
	webui "github.com/drilonrecica/siftail/internal/web/ui"
)

func (b *Browser) settingsPage(w http.ResponseWriter, r *http.Request) {
	session, _ := BrowserSessionFromContext(r.Context())
	notice, err := settingsNotice(r.URL.Query())
	if err != nil {
		b.renderSettings(w, session.CSRFToken, http.StatusBadRequest, webui.SettingsView{
			Error: "Check the Settings page parameters.",
		})
		return
	}
	view, err := b.loadSettingsView(r, session.CSRFToken)
	if err != nil {
		b.renderSettings(w, session.CSRFToken, http.StatusServiceUnavailable, webui.SettingsView{
			Error:          "Retention settings could not be read because the database is temporarily unavailable.",
			ErrorRequestID: web.RequestIDFromContext(r.Context()),
		})
		return
	}
	view.Notice = notice
	b.renderSettings(w, session.CSRFToken, http.StatusOK, view)
}

func (b *Browser) retentionSettingsSave(w http.ResponseWriter, r *http.Request) {
	session, _ := BrowserSessionFromContext(r.Context())
	values, err := exactManagementForm(r, "retention_days", "maximum_database_gib")
	if err != nil {
		b.renderSettings(w, session.CSRFToken, http.StatusBadRequest, webui.SettingsView{
			Error: "Check the retention-settings request.",
		})
		return
	}
	view := webui.SettingsView{
		CSRFToken:      session.CSRFToken,
		RetentionDays:  values.Get("retention_days"),
		MaxDatabaseGiB: values.Get("maximum_database_gib"),
	}
	ageDays, ageErr := parseBoundedWholeNumber(
		view.RetentionDays, retention.MinimumAgeDays, retention.MaximumAgeDays,
	)
	maxDatabaseGiB, sizeErr := parseBoundedWholeNumber(
		view.MaxDatabaseGiB,
		retention.MinimumMaxDatabaseGiB,
		retention.MaximumMaxDatabaseGiB,
	)
	if ageErr != nil {
		view.RetentionError = "Retention must be a whole number from 1 to 3,650 days."
	}
	if sizeErr != nil {
		view.DatabaseError = "Maximum database size must be a whole number from 1 to 1,024 GiB."
	}
	if ageErr != nil || sizeErr != nil {
		b.renderSettings(w, session.CSRFToken, http.StatusUnprocessableEntity, view)
		return
	}
	if b.retention == nil {
		view.Error = "Retention settings are temporarily unavailable."
		view.ErrorRequestID = web.RequestIDFromContext(r.Context())
		b.renderSettings(w, session.CSRFToken, http.StatusServiceUnavailable, view)
		return
	}
	_, err = b.retention.Save(r.Context(), retention.Input{
		AgeDays: ageDays, MaxDatabaseGiB: maxDatabaseGiB,
	})
	if errors.Is(err, retention.ErrInvalidAgeLimit) {
		view.RetentionError = "Retention must be a whole number from 1 to 3,650 days."
		b.renderSettings(w, session.CSRFToken, http.StatusUnprocessableEntity, view)
		return
	}
	if errors.Is(err, retention.ErrInvalidSizeLimit) {
		view.DatabaseError = "Maximum database size must be a whole number from 1 to 1,024 GiB."
		b.renderSettings(w, session.CSRFToken, http.StatusUnprocessableEntity, view)
		return
	}
	if err != nil {
		view.Error = "Retention settings could not be saved because the database is temporarily unavailable."
		view.ErrorRequestID = web.RequestIDFromContext(r.Context())
		b.renderSettings(w, session.CSRFToken, http.StatusServiceUnavailable, view)
		return
	}
	http.Redirect(w, r, "/settings?notice=retention-saved", http.StatusSeeOther)
}

func (b *Browser) loadSettingsView(
	r *http.Request,
	csrfToken string,
) (webui.SettingsView, error) {
	if b.retention == nil {
		return webui.SettingsView{}, errors.New("retention settings are unavailable")
	}
	settings, err := b.retention.Load(r.Context())
	if err != nil {
		return webui.SettingsView{}, err
	}
	return webui.SettingsView{
		CSRFToken:      csrfToken,
		RetentionDays:  strconv.Itoa(settings.AgeDays),
		MaxDatabaseGiB: strconv.Itoa(settings.MaxDatabaseGiB()),
	}, nil
}

func (b *Browser) renderSettings(
	w http.ResponseWriter,
	csrfToken string,
	status int,
	view webui.SettingsView,
) {
	view.CSRFToken = csrfToken
	if err := b.ui.Shell(w, status, webui.ShellView{
		CSRFToken: csrfToken, Mode: "settings", Settings: view,
	}); err != nil {
		http.Error(w, "Retention settings are temporarily unavailable.",
			http.StatusInternalServerError)
	}
}

func settingsNotice(values url.Values) (string, error) {
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 || len(values["notice"]) != 1 ||
		values.Get("notice") != "retention-saved" {
		return "", errors.New("invalid Settings notice")
	}
	return "Retention settings saved.", nil
}

func parseBoundedWholeNumber(value string, minimum, maximum int) (int, error) {
	if value == "" || len(value) > 4 {
		return 0, errors.New("invalid whole number")
	}
	for index, character := range value {
		if character < '0' || character > '9' || (index == 0 && character == '0') {
			return 0, errors.New("invalid whole number")
		}
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, errors.New("whole number out of bounds")
	}
	return parsed, nil
}
