// Package ui owns Siftail's embedded server-rendered templates and local
// browser assets.
package ui

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"path"
	"strings"
)

//go:embed templates/*.html assets/* licenses/*
var files embed.FS

type Renderer struct {
	templates *template.Template
}

// PrefsView carries the non-secret interface preferences rendered as
// data-theme, data-density, and data-shortcuts attributes before first paint.
type PrefsView struct {
	Theme     string
	Density   string
	Shortcuts string
}

type LoginView struct {
	AdministratorMissing bool
	ReturnPath           string
	Expired              bool
	Error                string
	Prefs                PrefsView
}

// SessionUnavailableView is the 503 page Protect renders when the database
// cannot answer a session lookup. The session itself is untouched, so the
// page offers a retry rather than sign-in (audit SEC-09).
type SessionUnavailableView struct {
	Message   string
	RequestID string
	RetryPath string
	Prefs     PrefsView
}

// FlashView is the one-time post-action notice the shell renders below the
// header (DESIGN.md §26).
type FlashView struct {
	Kind string
	Text string
}

type ShellView struct {
	CSRFToken string
	Mode      string
	Path      string
	Prefs     PrefsView
	Health    string
	Flash     FlashView
	History   HistoryView
	Live      LiveView
	Sources   SourcesView
	Source    SourceDetailView
	Servers   ServersView
	Server    ServerDetailView
	Token     OneTimeTokenView
	Settings  SettingsView
	Status    StatusView
	Audit     AuditView
	Backup    BackupView
}

// PageMeta describes the per-page shell chrome so templates never branch on
// Mode for titles, eyebrows, headings, or navigation state.
type PageMeta struct {
	Title      string
	Eyebrow    string
	Heading    string
	Context    string
	NavSection string
	// KbScope names the keyboard shortcut scope the page declares on <main>;
	// only "logs" carries single-letter shortcuts (DESIGN.md §32.1).
	KbScope string
}

func pageMeta(title, eyebrow, heading, context, navSection string) PageMeta {
	meta := PageMeta{
		Title:      title,
		Eyebrow:    eyebrow,
		Heading:    heading,
		Context:    context,
		NavSection: navSection,
	}
	if navSection == "logs" {
		meta.KbScope = "logs"
	}
	return meta
}

// PageMeta derives the shell chrome for the active Mode. It is the single
// authority for page titles, eyebrows, headings, context lines, and the
// active navigation section.
func (v ShellView) PageMeta() PageMeta {
	switch v.Mode {
	case "live":
		return pageMeta("Logs", "Live", "Logs", v.Live.SourceSummary, "logs")
	case "sources":
		return pageMeta("Sources", "Source catalog", "Sources", "Stable sources discovered from committed logs", "sources")
	case "source-detail":
		return pageMeta("Sources", "Source catalog", "Sources", v.Source.Server+" · "+v.Source.Project+" / "+v.Source.Environment, "sources")
	case "servers":
		return pageMeta("Servers", "Ingestion administration", "Servers", "Trusted identities for authenticated ingestion", "servers")
	case "server-detail":
		return pageMeta("Servers", "Ingestion administration", "Servers", v.Server.Name, "servers")
	case "token-created":
		return pageMeta("Servers", "Ingestion administration", "Servers", v.Token.ServerName+" · token shown once", "servers")
	case "settings":
		return pageMeta("Settings", "Runtime settings", "Settings", "Bounded local storage controls", "settings")
	case "status":
		return pageMeta("Status", "Appliance status", "Status", v.Status.Severity+" · sanitized local operational state", "status")
	case "audit":
		return pageMeta("Audit", "Security history", "Audit", "Immutable, bounded administrative security events", "audit")
	case "backup":
		return pageMeta("Backup", "Recovery preparation", "Backup", "Verified local SQLite recovery artifacts", "backup")
	default:
		return pageMeta("Logs", "History", "Logs", v.History.SourceSummary+" · "+v.History.RangeSummary, "logs")
	}
}

type SelectOption struct {
	Value    string
	Label    string
	Selected bool
}

type BackupView struct {
	CSRFToken      string
	State          string
	Operation      string
	BackupType     string
	StateLabel     string
	StartedAt      string
	CompletedAt    string
	TotalUnits     string
	CompletedUnits string
	Unit           string
	Name           string
	Size           string
	Checksum       string
	Error          string
}

type FilterChoice struct {
	ID      string
	Value   string
	Label   string
	Checked bool
}

type PresetLink struct {
	Label  string
	URL    string
	Active bool
}

type HistoryRowView struct {
	ID           int64
	DetailID     string
	DetailURL    string
	TimestampUTC string
	Timestamp    string
	ShowDate     bool
	Level        string
	Stream       string
	Source       string
	Message      string
	ToggleLabel  string
}

type DetailField struct {
	Label string
	Value string
}

type EventDetailView struct {
	ID                  int64
	DetailID            string
	FullURL             string
	Full                bool
	Message             string
	MessageBytes        int
	MessageTruncated    bool
	SourceFields        []DetailField
	TimingFields        []DetailField
	SeverityFields      []DetailField
	CommonFields        []DetailField
	Attributes          string
	AttributesBytes     int
	AttributesTruncated bool
	Raw                 string
	RawBytes            int
	RawTruncated        bool
}

type EventErrorView struct {
	Message   string
	RequestID string
}

// ModeSwitchView renders the shared History|Live segmented control. Because
// switching modes navigates, it uses links with aria-current, never tab
// roles (DESIGN.md §8.1).
type ModeSwitchView struct {
	HistoryURL string
	LiveURL    string
	Active     string
}

type HistoryView struct {
	CanonicalURL   string
	ExportURL      string
	From           string
	To             string
	RangeSummary   string
	SourceSummary  string
	Presets        []PresetLink
	Servers        []SelectOption
	Projects       []SelectOption
	Environments   []SelectOption
	Applications   []SelectOption
	Services       []SelectOption
	Containers     []SelectOption
	Levels         []FilterChoice
	Streams        []FilterChoice
	LevelsValue    string
	StreamsValue   string
	Contains       string
	Excludes       string
	RequestID      string
	Logger         string
	HTTPMethod     string
	HTTPStatus     string
	ErrorType      string
	Rows           []HistoryRowView
	EmptyTitle     string
	EmptyMessage   string
	EmptyActions   []ActionLink
	LoadedCount    int
	LoadedLabel    string
	HasMore        bool
	NextURL        string
	PrevURL        string
	GoLiveURL      string
	Error          string
	ErrorRequestID string
	Announcement   string
	ExportFilters  []DetailField
	ExportError    string
}

func (v HistoryView) ModeSwitch() ModeSwitchView {
	return ModeSwitchView{
		HistoryURL: v.CanonicalURL, LiveURL: "/logs?mode=live", Active: "history",
	}
}

func (v HistoryView) EmptyState() EmptyStateView {
	return EmptyStateView{
		Title: v.EmptyTitle, Message: v.EmptyMessage, Actions: v.EmptyActions,
	}
}

type LiveView struct {
	CanonicalURL  string
	StreamURL     string
	HistoryURL    string
	SourceSummary string
	Sources       []SelectOption
	Levels        []FilterChoice
	Streams       []FilterChoice
	LevelsValue   string
	StreamsValue  string
	Contains      string
}

func (v LiveView) ModeSwitch() ModeSwitchView {
	return ModeSwitchView{
		HistoryURL: v.HistoryURL, LiveURL: v.CanonicalURL, Active: "live",
	}
}

type SourcesView struct {
	Rows           []SourceRowView
	LoadedCount    int
	NextURL        string
	Notice         string
	Error          string
	ErrorRequestID string
}

func (v SourcesView) EmptyState() EmptyStateView {
	return EmptyStateView{
		Title: "No log sources yet",
		Message: "Create a server, generate its ingestion token, and point a " +
			"Coolify or Fluent Bit log drain at Siftail. Sources appear here " +
			"the moment the first event commits.",
		Actions: []ActionLink{{Label: "Create server", URL: "/servers"}},
	}
}

type SourceRowView struct {
	ID          int64
	DetailURL   string
	DisplayName string
	Server      string
	Project     string
	Environment string
	Application string
	Service     string
	Alias       bool
	Status      string
	Active      bool
	FirstSeen   string
	LastSeen    string
	Retained    string
}

type SourceDetailView struct {
	SourceRowView
	CSRFToken           string
	ServerHostname      string
	ProjectKey          string
	EnvironmentKey      string
	ApplicationKey      string
	ServiceKey          string
	CleanupEligible     bool
	Containers          []ContainerObservationView
	ContainersTruncated bool
	LogsURL             string
	Notice              string
	AliasValue          string
	AliasError          string
	ClearError          string
	RemoveError         string
}

type ContainerObservationView struct {
	Identity  string
	Status    string
	Active    bool
	FirstSeen string
	LastSeen  string
}

type ServersView struct {
	CSRFToken      string
	Rows           []ServerRowView
	NextURL        string
	Notice         string
	Error          string
	ErrorRequestID string
	ServerError    string
	Name           string
	Hostname       string
}

type ServerRowView struct {
	ID          int64
	DetailURL   string
	Name        string
	Hostname    string
	SourceCount int64
	TokenCount  int64
	LastEvent   string
}

type ServerDetailView struct {
	ServerRowView
	CSRFToken       string
	Notice          string
	Tokens          []TokenRowView
	TokensTruncated bool
	TokenError      string
	TokenName       string
	RevokeError     string
}

type TokenRowView struct {
	ID          int64
	Name        string
	Fingerprint string
	Created     string
	LastUsed    string
	Revoked     string
	Active      bool
}

type OneTimeTokenView struct {
	CSRFToken        string
	ServerID         int64
	ServerName       string
	TokenName        string
	Fingerprint      string
	Token            string
	DoneURL          string
	TestURL          string
	TokenPlaceholder string
	GuideAvailable   bool
	GuideError       string
	Endpoint         string
	CoolifyConfig    string
	GenericConfig    string
	CurlCommand      string
	SourcePreview    string
}

type SettingsView struct {
	CSRFToken string
	// OtherSessions is the number of other active administrator sessions;
	// SessionsKnown is false when that count could not be read.
	OtherSessions  int
	SessionsKnown  bool
	SessionsError  string
	SessionsReqID  string
	RetentionDays  string
	MaxDatabaseGiB string
	RetentionError string
	DatabaseError  string
	Notice         string
	Error          string
	ErrorRequestID string
}

type StatusView struct {
	Severity          string
	Version           string
	SchemaVersion     string
	SQLiteVersion     string
	Uptime            string
	Architecture      string
	UIReady           string
	IngestionReady    string
	DatabaseSize      string
	WALSize           string
	SHMSize           string
	DatabaseLimit     string
	DatabaseUsage     int
	OldestEvent       string
	NewestEvent       string
	RetentionAge      string
	LastCleanup       string
	LastCleanupResult string
	EventsToday       string
	RecentRate        string
	QueuedEvents      string
	QueuedBytes       string
	RejectedBatches   string
	ConflictsDropped  string
	// ConflictServers lists per-Server conflicting-duplicate counters (ADR
	// 0007), most recent first; ConflictServersMore names any not shown.
	ConflictServers     []StatusConflictView
	ConflictServersMore string
	LateEvents          string
	// LateServers lists per-Server late-event counters (ADR 0008), most
	// recent first; LateServersMore names any not shown.
	LateServers       []StatusLateView
	LateServersMore   string
	AcceptedEvents    string
	LastIngest        string
	LastDatabaseError string
	DatabaseCheck     string
	DatabaseCheckAt   string
	StorageWarning    string
	CSRFToken         string
	Diagnostics       []StatusDiagnosticView
	Error             string
	ErrorRequestID    string
}

type StatusConflictView struct {
	Server  string
	Dropped string
	LastAt  string
}

type StatusLateView struct {
	Server string
	Late   string
	LastAt string
}

type StatusDiagnosticView struct {
	Time        string
	Severity    string
	Component   string
	Category    string
	Summary     string
	RequestID   string
	RecoveredAt string
}

type AuditView struct {
	From           string
	To             string
	Action         string
	Categories     []SelectOption
	Outcomes       []SelectOption
	Rows           []AuditRowView
	NextURL        string
	Error          string
	ErrorRequestID string
}

type AuditRowView struct {
	Time      string
	Category  string
	Action    string
	Outcome   string
	Actor     string
	Summary   string
	RequestID string
}

// severityCodes maps canonical levels to the fixed-width three-letter codes
// every log surface renders (DESIGN.md §12.3). live.js carries the same map
// for browser-built rows; a test asserts the two stay identical.
var severityCodes = map[string]string{
	"trace":   "TRC",
	"debug":   "DBG",
	"info":    "INF",
	"warn":    "WRN",
	"error":   "ERR",
	"fatal":   "FTL",
	"unknown": "UNK",
}

// SeverityCode returns the fixed-width display code for a canonical level,
// falling back to the unknown code for anything unrecognized.
func SeverityCode(level string) string {
	if code, ok := severityCodes[level]; ok {
		return code
	}
	return "UNK"
}

// rowToggleMessageRunes bounds the message prefix in a row's detail-toggle
// name; live.js uses the same bound.
const rowToggleMessageRunes = 60

// RowToggleLabel names a log row's detail toggle from its content: severity
// code, source, and the first line of the message preview, never the event
// ID (DESIGN.md §32.3). live.js builds the same label for browser rows.
func RowToggleLabel(level, source, message string) string {
	prefix := message
	cut := false
	if line, _, found := strings.Cut(prefix, "\n"); found {
		prefix, cut = line, true
	}
	prefix = strings.TrimSpace(prefix)
	if runes := []rune(prefix); len(runes) > rowToggleMessageRunes {
		prefix, cut = strings.TrimSpace(string(runes[:rowToggleMessageRunes])), true
	}
	if cut && !strings.HasSuffix(prefix, "…") {
		prefix += "…"
	}
	if prefix == "" {
		prefix = "empty message"
	}
	return "Details: " + SeverityCode(level) + " " + source + ", " + prefix
}

// PaginationView renders the shared bidirectional pagination component.
type PaginationView struct {
	PrevURL   string
	PrevLabel string
	NextURL   string
	NextLabel string
	EndLabel  string
	Target    string
}

// ActionLink is a labeled navigation action, used by empty states.
type ActionLink struct {
	Label string
	URL   string
}

// EmptyStateView renders the shared empty-state component with real action
// controls (DESIGN.md §24).
type EmptyStateView struct {
	Title   string
	Message string
	Actions []ActionLink
}

func New() *Renderer {
	templates := template.New("").Funcs(template.FuncMap{
		"severityCode": SeverityCode,
	})
	return &Renderer{templates: template.Must(templates.ParseFS(files, "templates/*.html"))}
}

func (r *Renderer) Login(w http.ResponseWriter, status int, view LoginView) error {
	return r.render(w, status, "login.html", view)
}

func (r *Renderer) SessionUnavailable(w http.ResponseWriter, status int, view SessionUnavailableView) error {
	return r.render(w, status, "session-unavailable.html", view)
}

// SessionUnavailableNotice is the same notice as a fragment for htmx
// requests, which swap error responses into their target.
func (r *Renderer) SessionUnavailableNotice(w http.ResponseWriter, status int, view SessionUnavailableView) error {
	return r.render(w, status, "session-unavailable-notice.html", view)
}

func (r *Renderer) Shell(w http.ResponseWriter, status int, view ShellView) error {
	return r.render(w, status, "shell.html", view)
}

func (r *Renderer) Backup(w http.ResponseWriter, status int, view BackupView) error {
	return r.render(w, status, "backup.html", view)
}

func (r *Renderer) HistoryRegion(w http.ResponseWriter, status int, view HistoryView) error {
	return r.render(w, status, "history-region.html", view)
}

func (r *Renderer) HistoryAppend(w http.ResponseWriter, status int, view HistoryView) error {
	return r.render(w, status, "history-append.html", view)
}

func (r *Renderer) HistoryPrepend(w http.ResponseWriter, status int, view HistoryView) error {
	return r.render(w, status, "history-prepend.html", view)
}

func (r *Renderer) HistoryError(w http.ResponseWriter, status int, view HistoryView) error {
	return r.render(w, status, "history-error.html", view)
}

func (r *Renderer) EventDetail(w http.ResponseWriter, status int, view EventDetailView) error {
	return r.render(w, status, "event-detail.html", view)
}

func (r *Renderer) EventError(w http.ResponseWriter, status int, view EventErrorView) error {
	return r.render(w, status, "event-error.html", view)
}

func (r *Renderer) render(w http.ResponseWriter, status int, name string, view any) error {
	var output bytes.Buffer
	if err := r.templates.ExecuteTemplate(&output, name, view); err != nil {
		return fmt.Errorf("render %s: %w", name, err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, err := output.WriteTo(w)
	return err
}

func (r *Renderer) Asset(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := path.Base(request.URL.Path)
	contentTypes := map[string]string{
		"app.css":                        "text/css; charset=utf-8",
		"tokens.css":                     "text/css; charset=utf-8",
		"app.js":                         "text/javascript; charset=utf-8",
		"keyboard.js":                    "text/javascript; charset=utf-8",
		"palette.js":                     "text/javascript; charset=utf-8",
		"live.js":                        "text/javascript; charset=utf-8",
		"tokens.js":                      "text/javascript; charset=utf-8",
		"favicon.svg":                    "image/svg+xml",
		"htmx-2.0.11.min.js":             "text/javascript; charset=utf-8",
		"jetbrains-mono-latin.woff2":     "font/woff2",
		"jetbrains-mono-latin-ext.woff2": "font/woff2",
	}
	contentType, ok := contentTypes[name]
	if !ok {
		http.NotFound(w, request)
		return
	}
	content, err := files.ReadFile("assets/" + name)
	if err != nil {
		http.NotFound(w, request)
		return
	}
	w.Header().Set("Content-Type", contentType)
	if name == "htmx-2.0.11.min.js" || strings.HasSuffix(name, ".woff2") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
	w.WriteHeader(http.StatusOK)
	if request.Method == http.MethodGet {
		_, _ = w.Write(content)
	}
}
