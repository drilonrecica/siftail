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

	"github.com/drilonrecica/siftail/internal/audit"
	"github.com/drilonrecica/siftail/internal/backup"
	"github.com/drilonrecica/siftail/internal/database"
	"github.com/drilonrecica/siftail/internal/ingest"
	"github.com/drilonrecica/siftail/internal/logs"
	"github.com/drilonrecica/siftail/internal/retention"
	"github.com/drilonrecica/siftail/internal/sessions"
	"github.com/drilonrecica/siftail/internal/sources"
	statusstate "github.com/drilonrecica/siftail/internal/status"
	"github.com/drilonrecica/siftail/internal/web"
	webui "github.com/drilonrecica/siftail/internal/web/ui"
)

const csrfPurpose = "siftail-browser-csrf-v1"

type BrowserConfig struct {
	PublicURL         string
	IngestPublicURL   string
	TrustedProxyCIDRs []string
	HistoryStore      *logs.Store
	SourceStore       *sources.Store
	RetentionStore    *retention.Store
	StatusStore       *statusstate.Store
	AuditStore        *audit.Store
	BackupManager     *backup.Manager
	DatabaseChecker   *database.ActiveChecker
	LiveBroker        *logs.LiveBroker
	LiveHeartbeat     time.Duration
	LiveSessionCheck  time.Duration
	LiveWriteTimeout  time.Duration
	GuideTester       *ingest.GuideTester
}

type Browser struct {
	administrators   *Store
	sessions         *sessions.Store
	publicURL        string
	ingestPublicURL  string
	proxies          proxyTrust
	throttle         *loginThrottle
	ui               *webui.Renderer
	history          *logs.Store
	sources          *sources.Store
	retention        *retention.Store
	status           *statusstate.Store
	audit            *audit.Store
	backups          *backup.Manager
	databaseChecker  *database.ActiveChecker
	live             *logs.LiveBroker
	liveHeartbeat    time.Duration
	liveSessionCheck time.Duration
	liveWriteTimeout time.Duration
	guideTester      *ingest.GuideTester
	now              func() time.Time
}

func NewBrowser(administrators *Store, sessionStore *sessions.Store, config BrowserConfig) *Browser {
	if config.LiveHeartbeat <= 0 {
		config.LiveHeartbeat = 15 * time.Second
	}
	if config.LiveSessionCheck <= 0 {
		config.LiveSessionCheck = 5 * time.Second
	}
	if config.LiveWriteTimeout <= 0 {
		config.LiveWriteTimeout = 5 * time.Second
	}
	return &Browser{
		administrators:   administrators,
		sessions:         sessionStore,
		publicURL:        strings.TrimSuffix(config.PublicURL, "/"),
		ingestPublicURL:  config.IngestPublicURL,
		proxies:          newProxyTrust(config.TrustedProxyCIDRs),
		throttle:         newLoginThrottle(),
		ui:               webui.New(),
		history:          config.HistoryStore,
		sources:          config.SourceStore,
		retention:        config.RetentionStore,
		status:           config.StatusStore,
		audit:            config.AuditStore,
		backups:          config.BackupManager,
		databaseChecker:  config.DatabaseChecker,
		live:             config.LiveBroker,
		liveHeartbeat:    config.LiveHeartbeat,
		liveSessionCheck: config.LiveSessionCheck,
		liveWriteTimeout: config.LiveWriteTimeout,
		guideTester:      config.GuideTester,
		now:              time.Now,
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
	mux.Handle("GET /logs", b.Protect(http.HandlerFunc(b.historyPage)))
	mux.Handle("GET /logs/rows", b.Protect(http.HandlerFunc(b.historyRows)))
	mux.Handle("GET /logs/events/{id}", b.Protect(http.HandlerFunc(b.eventDetail)))
	mux.Handle("GET /logs/live/stream", b.Protect(http.HandlerFunc(b.liveStream)))
	mux.Handle("GET /sources", b.Protect(http.HandlerFunc(b.sourcesPage)))
	mux.Handle("GET /sources/{id}", b.Protect(http.HandlerFunc(b.sourceDetailPage)))
	mux.Handle("POST /sources/{id}/alias", b.Protect(http.HandlerFunc(b.sourceAlias)))
	mux.Handle("POST /sources/{id}/clear-logs", b.Protect(http.HandlerFunc(b.sourceClearLogs)))
	mux.Handle("POST /sources/{id}/remove", b.Protect(http.HandlerFunc(b.sourceRemove)))
	mux.Handle("GET /servers", b.Protect(http.HandlerFunc(b.serversPage)))
	mux.Handle("POST /servers", b.Protect(http.HandlerFunc(b.serverCreate)))
	mux.Handle("GET /servers/{id}", b.Protect(http.HandlerFunc(b.serverDetailPage)))
	mux.Handle("POST /servers/{id}/tokens", b.Protect(http.HandlerFunc(b.tokenCreate)))
	mux.Handle("POST /servers/{id}/test-ingestion", b.Protect(http.HandlerFunc(b.guidedIngestionTest)))
	mux.Handle("POST /tokens/{id}/revoke", b.Protect(http.HandlerFunc(b.tokenRevoke)))
	mux.Handle("GET /settings", b.Protect(http.HandlerFunc(b.settingsPage)))
	mux.Handle("POST /settings/retention", b.Protect(http.HandlerFunc(b.retentionSettingsSave)))
	mux.Handle("GET /status", b.Protect(http.HandlerFunc(b.statusPage)))
	mux.Handle("POST /status/database-check", b.Protect(http.HandlerFunc(b.databaseCheck)))
	mux.Handle("GET /audit", b.Protect(http.HandlerFunc(b.auditPage)))
	mux.Handle("GET /backup", b.Protect(http.HandlerFunc(b.backupPage)))
	mux.Handle("GET /backup/region", b.Protect(http.HandlerFunc(b.backupRegion)))
	mux.Handle("POST /backup/full", b.Protect(http.HandlerFunc(b.backupStart)))
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
		if err := b.recordLoginAudit(r, audit.OutcomeRejected); err != nil {
			http.Error(w, "Sign-in is temporarily unavailable.", http.StatusServiceUnavailable)
			return
		}
		b.writeRateLimitedLogin(w, r, delay)
		return
	}
	administrator, matched, err := b.administrators.Verify(r.Context(), username, password)
	if err != nil {
		http.Error(w, "Sign-in is temporarily unavailable.", http.StatusServiceUnavailable)
		return
	}
	if !matched {
		if err := b.recordLoginAudit(r, audit.OutcomeRejected); err != nil {
			http.Error(w, "Sign-in is temporarily unavailable.", http.StatusServiceUnavailable)
			return
		}
		if delay := b.throttle.Failure(clientKey, accountKey); delay > 0 {
			b.writeRateLimitedLogin(w, r, delay)
			return
		}
		b.writeLoginFailure(w, r, http.StatusUnauthorized,
			"Sign-in failed. Check your credentials and try again.")
		return
	}
	administratorID := administrator.ID
	loginCtx := audit.ContextWithAttribution(r.Context(), audit.Attribution{
		ActorType: audit.ActorAdministrator, AdministratorID: &administratorID,
		RequestID: web.RequestIDFromContext(r.Context()),
	})
	issued, err := b.sessions.Issue(
		loginCtx, administrator.ID, summarizeUserAgent(r.UserAgent()),
		b.proxies.clientIdentity(r),
	)
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
		administratorID := session.AdministratorID
		ctx := audit.ContextWithAttribution(r.Context(), audit.Attribution{
			ActorType: audit.ActorAdministrator, AdministratorID: &administratorID,
			RequestID: web.RequestIDFromContext(r.Context()),
		})
		ctx = context.WithValue(ctx, browserContextKey{}, browserSession)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (b *Browser) recordLoginAudit(r *http.Request, outcome audit.Outcome) error {
	if b.audit == nil {
		return nil
	}
	metadata := audit.Metadata{}
	if client := b.proxies.clientIdentity(r); client != "" {
		metadata[audit.MetadataClientAddress] = client
	}
	_, err := b.audit.Record(r.Context(), audit.Input{
		OccurredAt: b.now(), Category: audit.CategoryAuthentication,
		Action: "sign_in", Outcome: outcome,
		ActorType: audit.ActorUnauthenticated, Metadata: metadata,
		RequestID: web.RequestIDFromContext(r.Context()),
	})
	return err
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
			// no-referrer makes Chromium serialize native form POST origins as
			// "null"; same-origin suppresses cross-origin referrers while
			// preserving exact local Origin/Referer validation.
			w.Header().Set("Referrer-Policy", "same-origin")
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
