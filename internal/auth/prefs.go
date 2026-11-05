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

// preferences reads the interface preferences from the request cookie,
// falling back to the documented defaults for absent or invalid values.
func preferences(r *http.Request) webui.PrefsView {
	prefs := webui.PrefsView{Theme: "system", Density: "compact"}
	cookie, err := r.Cookie(prefsCookieName)
	if err != nil {
		return prefs
	}
	theme, density, ok := strings.Cut(cookie.Value, ".")
	if !ok {
		return prefs
	}
	if validTheme(theme) {
		prefs.Theme = theme
	}
	if validDensity(density) {
		prefs.Density = density
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

// preferencesSave persists validated theme and density values in the
// preference cookie and returns to the submitting page. Protect has already
// verified the session, origin, and CSRF token.
func (b *Browser) preferencesSave(w http.ResponseWriter, r *http.Request) {
	theme := r.PostForm.Get("theme")
	density := r.PostForm.Get("density")
	if !validTheme(theme) || !validDensity(density) {
		http.Error(w, "Invalid preference value.", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     prefsCookieName,
		Value:    theme + "." + density,
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
