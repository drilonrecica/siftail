package auth

import (
	"net/http"
	"strings"

	webui "github.com/drilonrecica/siftail/internal/web/ui"
)

// prefsCookieName stores the non-secret interface preferences so the server
// can render theme and density attributes before first paint.
const prefsCookieName = "siftail_prefs"

const prefsMaxAge = 365 * 24 * 60 * 60

func validTheme(value string) bool {
	return value == "system" || value == "dark" || value == "light"
}

func validDensity(value string) bool {
	return value == "compact" || value == "comfortable"
}

func validShortcuts(value string) bool {
	return value == "on" || value == "off"
}

// preferences reads the interface preferences from the request cookie,
// falling back to the documented defaults for absent or invalid values. The
// cookie is "theme.density.shortcuts.timezone" (DESIGN.md §19.5); a value
// with fewer fields defaults the missing ones, so cookies written before a
// field existed keep working.
func preferences(r *http.Request) webui.PrefsView {
	prefs := webui.PrefsView{Theme: "system", Density: "compact", Shortcuts: "on"}
	cookie, err := r.Cookie(prefsCookieName)
	if err != nil {
		return prefs
	}
	fields := strings.SplitN(cookie.Value, ".", 4)
	if validTheme(fields[0]) {
		prefs.Theme = fields[0]
	}
	if len(fields) > 1 && validDensity(fields[1]) {
		prefs.Density = fields[1]
	}
	if len(fields) > 2 && validShortcuts(fields[2]) {
		prefs.Shortcuts = fields[2]
	}
	return prefs
}

// shellBase fills the shell fields shared by every authenticated page render:
// CSRF token, mode, current request path, interface preferences, header
// health, and the pending one-time flash message (which it consumes).
func (b *Browser) shellBase(w http.ResponseWriter, r *http.Request, mode string) webui.ShellView {
	session, _ := BrowserSessionFromContext(r.Context())
	// Only the path is reflected into the page; query strings stay out of
	// rendered HTML so unexpected parameters can never leak through it.
	path := r.URL.Path
	if safeReturnPath(path) == "" {
		path = "/logs"
	}
	return webui.ShellView{
		CSRFToken: session.CSRFToken,
		Mode:      mode,
		Path:      path,
		Prefs:     preferences(r),
		Health:    b.status.HealthSeverity(),
		Flash:     b.takeFlash(w, r),
	}
}

func (b *Browser) preferencesSecure() bool {
	return b.cookies.Secure()
}

// preferencesSave persists validated theme, density, and keyboard-shortcut
// values in the preference cookie and returns to the submitting page. A form
// rendered before the shortcuts field existed omits it; that keeps the
// current value instead of failing. Protect has already verified the
// session, origin, and CSRF token.
func (b *Browser) preferencesSave(w http.ResponseWriter, r *http.Request) {
	theme := r.PostForm.Get("theme")
	density := r.PostForm.Get("density")
	shortcuts := r.PostForm.Get("shortcuts")
	if shortcuts == "" {
		shortcuts = preferences(r).Shortcuts
	}
	if !validTheme(theme) || !validDensity(density) || !validShortcuts(shortcuts) {
		http.Error(w, "Invalid preference value.", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     prefsCookieName,
		Value:    theme + "." + density + "." + shortcuts,
		Path:     "/",
		MaxAge:   prefsMaxAge,
		HttpOnly: true,
		Secure:   b.preferencesSecure(),
		SameSite: http.SameSiteLaxMode,
	})
	target := safeReturnPath(r.PostForm.Get("return"))
	if target == "" {
		target = "/logs"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
