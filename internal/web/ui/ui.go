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
)

//go:embed templates/*.html assets/* licenses/*
var files embed.FS

type Renderer struct {
	templates *template.Template
}

type LoginView struct {
	AdministratorMissing bool
	ReturnPath           string
	Expired              bool
	Error                string
}

type ShellView struct {
	CSRFToken string
	Mode      string
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

type HistoryView struct {
	CanonicalURL   string
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
	LoadedCount    int
	LoadedLabel    string
	HasMore        bool
	NextURL        string
	Error          string
	ErrorRequestID string
	Announcement   string
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

type SourcesView struct {
	Rows           []SourceRowView
	LoadedCount    int
	NextURL        string
	Notice         string
	Error          string
	ErrorRequestID string
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
	CSRFToken      string
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
	AcceptedEvents    string
	LastIngest        string
	LastDatabaseError string
	DatabaseCheck     string
	DatabaseCheckAt   string
	StorageWarning    string
	CheckNotice       string
	CheckFailed       bool
	CSRFToken         string
	Diagnostics       []StatusDiagnosticView
	Error             string
	ErrorRequestID    string
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

func New() *Renderer {
	return &Renderer{templates: template.Must(template.ParseFS(files, "templates/*.html"))}
}

func (r *Renderer) Login(w http.ResponseWriter, status int, view LoginView) error {
	return r.render(w, status, "login.html", view)
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
		"app.css":            "text/css; charset=utf-8",
		"app.js":             "text/javascript; charset=utf-8",
		"live.js":            "text/javascript; charset=utf-8",
		"tokens.js":          "text/javascript; charset=utf-8",
		"favicon.svg":        "image/svg+xml",
		"htmx-2.0.10.min.js": "text/javascript; charset=utf-8",
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
	if name == "htmx-2.0.10.min.js" {
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
