package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/drilonrecica/siftail/internal/sessions"
	webui "github.com/drilonrecica/siftail/internal/web/ui"
)

const csrfPurpose = "siftail-browser-csrf-v1"

type BrowserConfig struct {
	PublicURL         string
	TrustedProxyCIDRs []string
}

type Browser struct {
	administrators *Store
	sessions       *sessions.Store
	publicURL      string
	proxies        proxyTrust
	throttle       *loginThrottle
	ui             *webui.Renderer
}

func NewBrowser(administrators *Store, sessionStore *sessions.Store, config BrowserConfig) *Browser {
	return &Browser{
		administrators: administrators,
		sessions:       sessionStore,
		publicURL:      strings.TrimSuffix(config.PublicURL, "/"),
		proxies:        newProxyTrust(config.TrustedProxyCIDRs),
		throttle:       newLoginThrottle(),
		ui:             webui.New(),
	}
}

type browserContextKey struct{}

type BrowserSession struct {
	Session   sessions.Session
	Token     string
	CSRFToken string
}

func BrowserSessionFromContext(ctx context.Context) (BrowserSession, bool) {
	value, ok := ctx.Value(browserContextKey{}).(BrowserSession)
	return value, ok
}

func CSRFToken(token string) string {
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte(csrfPurpose))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (b *Browser) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /login", b.loginPage)
	mux.HandleFunc("POST /session", b.login)
	mux.HandleFunc("/assets/", b.ui.Asset)
	mux.Handle("POST /session/logout", b.Protect(http.HandlerFunc(b.logout)))
	mux.Handle("GET /logs", b.Protect(http.HandlerFunc(b.logsPlaceholder)))
}

func (b *Browser) loginPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if cookie, err := r.Cookie(sessions.CookieName); err == nil {
		if _, err := b.sessions.Lookup(r.Context(), cookie.Value); err == nil {
			http.Redirect(w, r, "/logs", http.StatusSeeOther)
			return
		}
	}
	exists, err := b.administrators.Exists(r.Context())
	if err != nil {
		http.Error(w, "Sign-in is temporarily unavailable.", http.StatusServiceUnavailable)
		return
	}
	data := webui.LoginView{
		AdministratorMissing: !exists,
		ReturnPath:           safeReturnPath(r.URL.Query().Get("return")),
		Expired:              r.URL.Query().Get("expired") == "1",
	}
	if err := b.ui.Login(w, http.StatusOK, data); err != nil {
		http.Error(w, "Sign-in is temporarily unavailable.", http.StatusInternalServerError)
	}
}

func (b *Browser) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !formContentType(r) || !b.validRequestOrigin(r) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid sign-in request.", http.StatusBadRequest)
		return
	}
	username := r.PostForm.Get("username")
	password := []byte(r.PostForm.Get("password"))
	clientKey := clientThrottleKey(b.proxies.clientIdentity(r))
	accountKey := accountThrottleKey(username)
	if delay := b.throttle.Check(clientKey, accountKey); delay > 0 {
		b.writeRateLimitedLogin(w, r, delay)
		return
	}
	administrator, matched, err := b.administrators.Verify(r.Context(), username, password)
	if err != nil {
		http.Error(w, "Sign-in is temporarily unavailable.", http.StatusServiceUnavailable)
		return
	}
	if !matched {
		if delay := b.throttle.Failure(clientKey, accountKey); delay > 0 {
			b.writeRateLimitedLogin(w, r, delay)
			return
		}
		b.writeLoginFailure(w, r, http.StatusUnauthorized,
			"Sign-in failed. Check your credentials and try again.")
		return
	}
	issued, err := b.sessions.Issue(r.Context(), administrator.ID, summarizeUserAgent(r.UserAgent()), b.proxies.clientIdentity(r))
	if err != nil {
		http.Error(w, "Sign-in is temporarily unavailable.", http.StatusServiceUnavailable)
		return
	}
	b.throttle.Success(clientKey, accountKey)
	cookie, err := sessions.NewCookie(issued.Token, issued.Session.ExpiresAt, b.publicURL)
	if err != nil {
		_ = b.sessions.Revoke(r.Context(), issued.Token)
		http.Error(w, "Sign-in is temporarily unavailable.", http.StatusServiceUnavailable)
		return
	}
	http.SetCookie(w, cookie)
	returnPath := safeReturnPath(r.PostForm.Get("return"))
	if returnPath == "" {
		returnPath = "/logs"
	}
	http.Redirect(w, r, returnPath, http.StatusSeeOther)
}

func (b *Browser) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessions.CookieName)
		if err != nil {
			b.requireLogin(w, r, false)
			return
		}
		session, err := b.sessions.Lookup(r.Context(), cookie.Value)
		if err != nil {
			b.requireLogin(w, r, true)
			return
		}
		browserSession := BrowserSession{
			Session: session, Token: cookie.Value, CSRFToken: CSRFToken(cookie.Value),
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if !formContentType(r) || !b.validRequestOrigin(r) {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
			if err := r.ParseForm(); err != nil {
				http.Error(w, "Invalid request.", http.StatusBadRequest)
				return
			}
			supplied := r.Header.Get("X-CSRF-Token")
			if supplied == "" {
				supplied = r.PostForm.Get("csrf_token")
			}
			if subtle.ConstantTimeCompare([]byte(supplied), []byte(browserSession.CSRFToken)) != 1 {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
		}
		ctx := context.WithValue(r.Context(), browserContextKey{}, browserSession)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (b *Browser) logout(w http.ResponseWriter, r *http.Request) {
	session, ok := BrowserSessionFromContext(r.Context())
	if !ok {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if err := b.sessions.Revoke(r.Context(), session.Token); err != nil {
		http.Error(w, "Sign-out failed.", http.StatusServiceUnavailable)
		return
	}
	cookie, _ := sessions.NewCookie("deleted", time.Unix(1, 0), b.publicURL)
	cookie.MaxAge = -1
	http.SetCookie(w, cookie)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (b *Browser) logsPlaceholder(w http.ResponseWriter, r *http.Request) {
	session, _ := BrowserSessionFromContext(r.Context())
	if err := b.ui.Shell(w, http.StatusOK, webui.ShellView{CSRFToken: session.CSRFToken}); err != nil {
		http.Error(w, "Logs are temporarily unavailable.", http.StatusInternalServerError)
	}
}

func (b *Browser) requireLogin(w http.ResponseWriter, r *http.Request, expired bool) {
	target := "/login?return=" + url.QueryEscape(safeRequestPath(r))
	if expired {
		target += "&expired=1"
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", target)
		http.Error(w, "Authentication required.", http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (b *Browser) validRequestOrigin(r *http.Request) bool {
	allowed := b.allowedOrigin(r)
	if allowed == "" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		return subtle.ConstantTimeCompare([]byte(origin), []byte(allowed)) == 1
	}
	referer := r.Header.Get("Referer")
	parsed, err := url.Parse(referer)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(parsed.Scheme+"://"+parsed.Host), []byte(allowed)) == 1
}

func (b *Browser) allowedOrigin(r *http.Request) string {
	if b.publicURL != "" {
		parsed, err := url.Parse(b.publicURL)
		if err == nil && parsed.Scheme != "" && parsed.Host != "" {
			return parsed.Scheme + "://" + parsed.Host
		}
		return ""
	}
	return b.proxies.requestOrigin(r)
}

func formContentType(r *http.Request) bool {
	mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		return false
	}
	for key, value := range parameters {
		if !strings.EqualFold(key, "charset") || !strings.EqualFold(value, "utf-8") {
			return false
		}
	}
	return true
}

func safeRequestPath(r *http.Request) string {
	path := r.URL.RequestURI()
	if len(path) > 2048 || safeReturnPath(path) == "" {
		return "/logs"
	}
	return path
}

func safeReturnPath(value string) string {
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" ||
		!strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") ||
		len(value) > 2048 || strings.ContainsAny(value, "\r\n") {
		return ""
	}
	return value
}

func summarizeUserAgent(value string) string {
	value = strings.Map(func(char rune) rune {
		if char < 0x20 || char == 0x7f {
			return -1
		}
		return char
	}, value)
	if len(value) > 256 {
		value = value[:256]
		for !utf8.ValidString(value) {
			_, size := utf8.DecodeLastRuneInString(value)
			if size == 0 {
				return ""
			}
			value = value[:len(value)-size]
		}
	}
	return value
}

func (b *Browser) writeRateLimitedLogin(
	w http.ResponseWriter,
	r *http.Request,
	delay time.Duration,
) {
	seconds := int((delay + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	b.writeLoginFailure(w, r, http.StatusTooManyRequests,
		"Too many attempts. Try again shortly.")
}

func (b *Browser) writeLoginFailure(
	w http.ResponseWriter,
	r *http.Request,
	status int,
	message string,
) {
	view := webui.LoginView{
		ReturnPath: safeReturnPath(r.PostForm.Get("return")),
		Error:      message,
	}
	if err := b.ui.Login(w, status, view); err != nil {
		http.Error(w, "Sign-in is temporarily unavailable.", http.StatusInternalServerError)
	}
}

func SecurityHeaders(publicURL string) func(http.Handler) http.Handler {
	https := strings.HasPrefix(strings.ToLower(publicURL), "https://")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
			w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
			w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
			w.Header().Set("X-Frame-Options", "DENY")
			if https {
				w.Header().Set("Strict-Transport-Security", "max-age=31536000")
			}
			next.ServeHTTP(w, r)
		})
	}
}
