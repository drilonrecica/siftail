package auth

import (
	"net/http"

	webui "github.com/drilonrecica/siftail/internal/web/ui"
)

// flashCookieName carries a one-time post-action message code across the
// redirect after a state change (DESIGN.md §26). Only the code travels in
// the cookie; the copy lives in flashMessages, so a tampered cookie can at
// worst select a different fixed message.
const flashCookieName = "siftail_flash"

type flashMessage struct {
	Kind string
	Text string
}

var flashMessages = map[string]flashMessage{
	"retention-saved": {Kind: "success", Text: "Retention settings saved."},
	"alias-updated": {Kind: "success",
		Text: "Source alias updated. Stable identity and stored events were unchanged."},
	"alias-removed": {Kind: "success",
		Text: "Source alias removed. Stable identity and stored events were unchanged."},
	"logs-cleared": {Kind: "success",
		Text: "Retained logs were cleared. The source, alias, and container observations remain."},
	"source-removed": {Kind: "success",
		Text: "Source removed. An active sender may discover it again."},
	"new-events-remain": {Kind: "warning",
		Text: "Events committed after removal started remain. The alias and unreferenced container observations were removed."},
	"token-revoked": {Kind: "success",
		Text: "Token revoked. New ingestion requests using it fail immediately."},
	"database-check-complete": {Kind: "success", Text: "Database check completed."},
	"other-sessions-signed-out": {Kind: "success",
		Text: "Other sessions signed out. This browser stays signed in."},
	"no-other-sessions": {Kind: "success",
		Text: "No other sessions were active. This browser stays signed in."},
	"database-check-failed": {Kind: "warning",
		Text: "Database check did not complete successfully."},
}

// setFlash stores the one-time message code before a post-action redirect.
func (b *Browser) setFlash(w http.ResponseWriter, code string) {
	if _, ok := flashMessages[code]; !ok {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    code,
		Path:     "/",
		MaxAge:   60,
		HttpOnly: true,
		Secure:   b.preferencesSecure(),
		SameSite: http.SameSiteLaxMode,
	})
}

// takeFlash consumes the pending flash message: it returns the mapped copy
// and expires the cookie so a reload or shared link never re-shows it.
func (b *Browser) takeFlash(w http.ResponseWriter, r *http.Request) webui.FlashView {
	cookie, err := r.Cookie(flashCookieName)
	if err != nil {
		return webui.FlashView{}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   b.preferencesSecure(),
		SameSite: http.SameSiteLaxMode,
	})
	message, ok := flashMessages[cookie.Value]
	if !ok {
		return webui.FlashView{}
	}
	return webui.FlashView{Kind: message.Kind, Text: message.Text}
}
