package ui

import (
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestLoginTemplateStatesAccessibilityAndEscaping(t *testing.T) {
	renderer := New()
	cases := []struct {
		name string
		view LoginView
		want []string
	}{
		{
			name: "ordinary",
			view: LoginView{ReturnPath: `/logs?contains="><script>alert(1)</script>`},
			want: []string{
				`<label for="username">Username</label>`,
				`autocomplete="username"`, `autofocus`, `autocomplete="current-password"`,
				`Lost access? Reset the administrator password with the Siftail CLI on the host.`,
			},
		},
		{
			name: "missing administrator",
			view: LoginView{AdministratorMissing: true},
			want: []string{
				`No administrator is configured`,
				`siftail admin create --username &lt;name&gt;`,
			},
		},
		{
			name: "expired and error",
			view: LoginView{Expired: true, Error: `<script>alert("unsafe")</script>`},
			want: []string{
				`Your session expired. Sign in again to continue.`,
				`id="login-error"`, `role="alert"`, `aria-describedby="login-error"`,
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			if err := renderer.Login(response, http.StatusOK, test.view); err != nil {
				t.Fatal(err)
			}
			body := response.Body.String()
			for _, want := range test.want {
				if !strings.Contains(body, want) {
					t.Errorf("body missing %q", want)
				}
			}
			if strings.Contains(body, `<script>alert`) ||
				strings.Contains(body, `"><script>`) {
				t.Fatalf("template emitted unescaped input: %s", body)
			}
			if !strings.Contains(body, `class="skip-link"`) ||
				!strings.Contains(body, `<main id="main-content"`) {
				t.Fatal("skip-link landmarks missing")
			}
		})
	}
}

func TestShellTemplateUsesLocalCSPCompatibleAssets(t *testing.T) {
	renderer := New()
	response := httptest.NewRecorder()
	if err := renderer.Shell(response, http.StatusOK, ShellView{
		CSRFToken: `"><script>alert(1)</script>`,
	}); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	for _, pattern := range []string{
		`href="/assets/app\.[0-9a-f]{12}\.css"`,
		`src="/assets/htmx-2\.0\.11\.min\.[0-9a-f]{12}\.js"`,
		`src="/assets/app\.[0-9a-f]{12}\.js"`,
		`src="/assets/live\.[0-9a-f]{12}\.js"`,
		`src="/assets/tokens\.[0-9a-f]{12}\.js"`,
	} {
		if !regexp.MustCompile(pattern).MatchString(body) {
			t.Errorf("shell missing hashed asset %s", pattern)
		}
	}
	for _, want := range []string{
		`"includeIndicatorStyles":false`,
		`hx-history="false"`,
		`name="htmx-config"`,
		`"[45].."`,
		`class="skip-link"`,
		`aria-label="Primary"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("shell missing %q", want)
		}
	}
	for _, forbidden := range []string{
		`<script>`, `<style`, `https://`, `http://`, `"><script>`,
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("shell contains CSP-incompatible or unsafe %q", forbidden)
		}
	}
}

func TestHistoryExportDialogIsExplicitBoundedAndEscaped(t *testing.T) {
	renderer := New()
	response := httptest.NewRecorder()
	if err := renderer.Shell(response, http.StatusOK, ShellView{
		CSRFToken: "shell-csrf",
		Mode:      "history",
		History: HistoryView{
			CanonicalURL:  "/logs?from=from&amp;to=to",
			ExportURL:     `/logs/export?contains=%3Cscript%3E`,
			CSRFToken:     "shell-csrf",
			From:          "2026-07-28T00:00:00Z",
			To:            "2026-07-28T01:00:00Z",
			RangeSummary:  "one hour",
			SourceSummary: `<script>source</script>`,
			ExportFilters: []DetailField{{
				Label: "Contains", Value: `<script>payload</script>`,
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	for _, want := range []string{
		`data-open-export`,
		`data-export-dialog`,
		`method="post"`,
		`action="/logs/export?contains=%3Cscript%3E"`,
		`Text (siftail-text-v1, .txt)`,
		`NDJSON (schema 1, .ndjson)`,
		`100,000 events`,
		`256 MiB`,
		`31-day range`,
		`two minutes`,
		`name="confirmation"`,
		`required`,
		`every matching event`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("History export dialog missing %q", want)
		}
	}
	// The full page owns exactly three server-rendered CSRF tokens
	// (preferences, sign-out, and the export form's own, so the export works
	// without JavaScript); filter fragments carry none (SFT-046).
	if strings.Contains(body, `<script>source`) ||
		strings.Contains(body, `<script>payload`) ||
		strings.Count(body, `name="csrf_token" value="shell-csrf"`) != 3 {
		t.Fatalf("History export dialog exposed unsafe content or fragment CSRF: %s", body)
	}
}

func TestSettingsShellRendersBoundedAccessibleRetentionForm(t *testing.T) {
	renderer := New()
	response := httptest.NewRecorder()
	if err := renderer.Shell(response, http.StatusUnprocessableEntity, ShellView{
		CSRFToken: "csrf",
		Mode:      "settings",
		Settings: SettingsView{
			CSRFToken:      "csrf",
			RetentionDays:  `<script>alert("unsafe")</script>`,
			MaxDatabaseGiB: "0",
			RetentionError: "Retention error",
			DatabaseError:  "Database error",
		},
	}); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	for _, want := range []string{
		`aria-current="page">Settings</a>`,
		`action="/settings/retention"`,
		`min="1" max="3650" step="1"`,
		`min="1" max="1024" step="1"`,
		`aria-invalid="true"`,
		`id="retention-days-error"`,
		`id="maximum-database-error"`,
		`Siftail does not claim forensic erasure.`,
		`the audit history`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Settings shell missing %q", want)
		}
	}
	if strings.Contains(body, `<script>alert`) {
		t.Fatal("Settings shell emitted unsafe field content")
	}
}

func TestStatusShellRendersSanitizedTextualOperationalState(t *testing.T) {
	renderer := New()
	response := httptest.NewRecorder()
	view := StatusView{
		Severity: "Degraded", Version: "dev · unknown", Uptime: "1h2m3s",
		Architecture: "linux/amd64", UIReady: "ready", IngestionReady: "not ready",
		DatabaseSize: "2.0 MiB", WALSize: "1.0 MiB", SHMSize: "32.0 KiB",
		DatabaseLimit: "4.00 GiB", DatabaseUsage: 1,
		OldestEvent: "old", NewestEvent: "new", RetentionAge: "14 days",
		LastCleanup: "now", LastCleanupResult: "10 age-deleted",
		EventsToday: "12", RecentRate: "3 events/min (last 60 seconds)",
		QueuedEvents: "1", QueuedBytes: "2.0 KiB", RejectedBatches: "4",
		AcceptedEvents: "12", LastIngest: "now", LastDatabaseError: "storage_full",
		Diagnostics: []StatusDiagnosticView{{
			Time: "now", Category: "storage_full",
			Summary: `<script>private-payload</script>`,
		}},
	}
	if err := renderer.Shell(response, http.StatusOK, ShellView{
		Mode: "status", Status: view,
	}); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	for _, want := range []string{
		`aria-current="page">Status</a>`,
		`data-severity="Degraded"`,
		`<progress id="database-usage" max="100" value="1">`,
		`Runtime`, `Storage`, `Ingestion`, `Diagnostics`,
		`&lt;script&gt;private-payload&lt;/script&gt;`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Status shell missing %q", want)
		}
	}
	if strings.Contains(body, `<script>private-payload`) ||
		strings.Contains(body, "canvas") ||
		// The only SVG is the icon sprite and its icons (DESIGN.md §31).
		strings.Contains(strings.NewReplacer(`<svg class="icon-sprite"`, "", `<svg class="icon"`, "").Replace(body), "<svg") {
		t.Fatal("Status shell emitted unsafe content or dashboard chart")
	}
}

func TestLiveShellRendersExplicitEscapedBoundedWorkspace(t *testing.T) {
	renderer := New()
	response := httptest.NewRecorder()
	view := LiveView{
		CanonicalURL:  `/logs?mode=live&amp;contains=%3Cunsafe%3E`,
		StreamURL:     `/logs/live/stream?contains=%3Cunsafe%3E`,
		HistoryURL:    "/logs",
		Zone:          "Europe/Berlin",
		SourceSummary: `API <unsafe>`,
		Servers: []SelectOption{{
			Value: "1", Label: `API <unsafe>`, Selected: true,
		}},
		Levels:       []FilterChoice{{ID: "level-error", Value: "error", Label: "error", Checked: true}},
		Streams:      []FilterChoice{{ID: "stream-stderr", Value: "stderr", Label: "stderr", Checked: true}},
		LevelsValue:  "error",
		StreamsValue: "stderr",
		Contains:     `<script>alert(1)</script>`,
	}
	if err := renderer.Shell(response, http.StatusOK, ShellView{Mode: "live", Live: view}); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	for _, want := range []string{
		`data-live-workspace`, `data-stream-url=`,
		`aria-current="page">Live<`, `data-live-pause`, `data-live-newest`,
		`data-live-clear`, `>Clear view<`, `data-live-reconnect`,
		`data-live-pending hidden`,
		`aria-label="Live log events, times in Europe/Berlin"`,
		`<span class="tail-zone">Times in Europe/Berlin</span>`,
		`API &lt;unsafe&gt;`, `&lt;script&gt;alert(1)&lt;/script&gt;`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Live shell missing %q", want)
		}
	}
	if strings.Contains(body, `<script>alert(1)</script>`) ||
		strings.Contains(body, "Clear logs") {
		t.Fatal("Live shell emitted unsafe content or destructive copy")
	}
}

func TestSourceCatalogShellEscapesHierarchyAndMarksLifecycle(t *testing.T) {
	renderer := New()
	response := httptest.NewRecorder()
	view := SourcesView{
		LoadedCount: 1,
		NextURL:     `/sources?after=1&limit=100`,
		Rows: []SourceRowView{{
			ID: 1, DetailURL: "/sources/1", DisplayName: `<script>source</script>`,
			Server: "Production", Project: "Project", Environment: "Environment",
			Application: "API", Service: "Web", Alias: true,
			Status: "Inactive", LastSeen: "28 Jul 2026", Retained: "Retained logs",
		}},
	}
	if err := renderer.Shell(response, http.StatusOK, ShellView{
		Mode: "sources", Sources: view,
	}); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	for _, want := range []string{
		`<title>Sources · Siftail</title>`,
		`aria-current="page">Sources</a>`,
		`&lt;script&gt;source&lt;/script&gt;`,
		`<span class="alias-indicator">Alias</span>`,
		`href="/sources?after=1&amp;limit=100"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("source shell missing %q", want)
		}
	}
	if strings.Contains(body, `<script>source</script>`) {
		t.Fatal("source shell emitted unescaped metadata")
	}
}

func TestSourceDetailMutationFormsKeepActionsDistinctAndEscaped(t *testing.T) {
	renderer := New()
	response := httptest.NewRecorder()
	view := SourceDetailView{
		SourceRowView: SourceRowView{
			ID: 9, DisplayName: `<img src=x onerror=alert(1)>`,
			Server: "Production", Project: "Project", Environment: "Production",
			Application: "API", Service: "Web", Status: "Inactive",
		},
		CSRFToken:  `csrf"><script>`,
		AliasValue: `<Alias>`,
		ClearError: "Type the displayed source name exactly to clear retained logs.",
	}
	if err := renderer.Shell(response, http.StatusUnprocessableEntity, ShellView{
		Mode: "source-detail", Source: view,
	}); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	for _, want := range []string{
		`action="/sources/9/alias"`,
		`Aliases change display names only. Original source metadata remains unchanged.`,
		`action="/sources/9/clear-logs"`,
		`The source, its alias, and its container history remain.`,
		`action="/sources/9/remove"`,
		`a server that still sends logs for this service creates it again`,
		`This is not secure forensic erasure.`,
		`id="clear-confirmation"`,
		`aria-describedby="clear-error" autofocus`,
		`&lt;img src=x onerror=alert(1)&gt;`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("source mutation shell missing %q", want)
		}
	}
	if strings.Contains(body, `<img src=x`) || strings.Contains(body, `<script>`) {
		t.Fatal("source mutation shell emitted hostile metadata or CSRF")
	}
}

func TestOneTimeTokenTemplateContainsNoSecretURLOrUnsafeMarkup(t *testing.T) {
	renderer := New()
	response := httptest.NewRecorder()
	secret := `sft_secret"><script>alert(1)</script>`
	if err := renderer.Shell(response, http.StatusCreated, ShellView{
		Mode: "token-created",
		Token: OneTimeTokenView{
			ServerID: 3, ServerName: `Production <unsafe>`, TokenName: "rotation",
			Fingerprint: "abcdef123456", Token: secret, DoneURL: "/servers/3",
			TestURL:          "/servers/3/test-ingestion",
			TokenPlaceholder: "__SIFTAIL_INGEST_TOKEN__",
			GuideAvailable:   true,
			Endpoint:         "https://ingest.example.test/api/v1/ingest",
			CoolifyConfig:    "Header Authorization Bearer __SIFTAIL_INGEST_TOKEN__",
			GenericConfig:    "Header Authorization Bearer __SIFTAIL_INGEST_TOKEN__",
			CurlCommand:      "curl __SIFTAIL_INGEST_TOKEN__",
			SourcePreview:    "siftail-test / setup / guided-ingestion / probe",
		},
	}); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	for _, want := range []string{
		`data-one-time-token`, `data-done-url="/servers/3"`,
		`data-token-secret type="password"`,
		`data-token-toggle`, `data-token-copy`,
		`data-material-copy="coolify"`, `data-material-copy="generic"`,
		`data-material-copy="curl"`, `data-guide-test`,
		`action="/servers/3/test-ingestion"`,
		`tabindex="0" aria-label="Coolify configuration"`,
		`__SIFTAIL_INGEST_TOKEN__`,
		`Test connection`,
		`stores only a hash and cannot show it again`,
		`href="/servers/3">Done</a>`,
		`Production &lt;unsafe&gt;`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("one-time token shell missing %q", want)
		}
	}
	if strings.Contains(body, `<script>alert`) ||
		strings.Contains(body, `/servers/3?`) ||
		strings.Count(body, `sft_secret`) != 1 {
		t.Fatal("one-time token shell emitted unsafe markup or secret-bearing URL")
	}
}

func TestHistoryFragmentsRenderFocusedEscapedState(t *testing.T) {
	renderer := New()
	view := HistoryView{
		CanonicalURL:  `/logs?contains=%3Cscript%3E`,
		From:          "2026-07-28T00:00:00Z",
		To:            "2026-07-28T01:00:00Z",
		FromText:      "28 Jul 2026 02:00:00 Europe/Berlin",
		ToText:        "28 Jul 2026 03:00:00 Europe/Berlin",
		Zone:          "Europe/Berlin",
		RangeSummary:  "28 Jul 2026 02:00–28 Jul 2026 03:00 Europe/Berlin",
		SourceSummary: `API <unsafe>`,
		Servers:       []SelectOption{{Value: "1", Label: `Server <one>`, Selected: true}},
		Levels:        []FilterChoice{{ID: "level-error", Value: "error", Label: "error", Checked: true}},
		Streams:       []FilterChoice{{ID: "stream-stderr", Value: "stderr", Label: "stderr", Checked: true}},
		LevelsValue:   "error",
		StreamsValue:  "stderr",
		Contains:      `<script>alert(1)</script>`,
		Rows: []HistoryRowView{{
			ID: 7, DetailID: "event-detail-7", DetailURL: "/logs/events/7",
			TimestampUTC: "2026-07-28T00:30:00Z",
			Timestamp:    "02:30:00.000", Level: "error", Stream: "stderr",
			Source: `API <unsafe>`, Message: `<img src=x onerror=alert(1)>`,
			ToggleLabel: RowToggleLabel("error", `API <unsafe>`, `<img src=x onerror=alert(1)>`),
		}},
		LoadedCount: 1,
		HasMore:     true,
		NextURL:     "/logs/rows?cursor=opaque&amp;append=1",
	}
	response := httptest.NewRecorder()
	if err := renderer.HistoryRegion(response, http.StatusOK, view); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	for _, want := range []string{
		`id="history-region"`,
		`hx-target="#history-results"`,
		`hx-sync="this:replace"`,
		`placeholder="Contains message text" data-text-filter hx-preserve>`,
		`<div id="history-filter-error" class="history-fragment-error"`,
		`<span class="paging-label">Load older</span><span class="htmx-indicator">Loading…</span>`,
		`<div id="history-older-error" class="history-fragment-error"`,
		`<div id="history-newer-error" class="history-fragment-error"`,
		`delay:400ms`,
		`name="levels" value="error" checked`,
		`API &lt;unsafe&gt;`,
		`&lt;img src=x onerror=alert(1)&gt;`,
		`id="load-older"`,
		`hx-disabled-elt="this"`,
		`aria-controls="event-detail-7"`,
		`hx-get="/logs/events/7"`,
		`id="history-rows" class="log-list" role="list" aria-label="History log events, times in Europe/Berlin"`,
		`<time datetime="2026-07-28T00:30:00Z">02:30:00.000</time>`,
		`<span class="tail-zone">Times in Europe/Berlin</span>`,
		`28 Jul 2026 02:00–28 Jul 2026 03:00 Europe/Berlin`,
		`<time datetime="2026-07-28T00:00:00Z">28 Jul 2026 02:00:00 Europe/Berlin</time>`,
		`From (<span data-range-zone>UTC</span>)`,
		`class="log-row level-error" role="listitem" data-event-id="7" tabindex="-1"`,
		`tabindex="-1" aria-label="Details: ERR API &lt;unsafe&gt;, &lt;img src=x onerror=alert(1)&gt;"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("History fragment missing %q", want)
		}
	}
	if strings.Contains(body, `<img src=x`) || strings.Contains(body, `<script>alert`) {
		t.Fatal("History fragment emitted unescaped event/filter data")
	}
	if strings.Contains(body, "for event 7") || strings.Contains(body, "<article") {
		t.Fatal("History rows must be listitems with content-named toggles")
	}

	view.Announcement = "1 additional event loaded."
	appendResponse := httptest.NewRecorder()
	if err := renderer.HistoryAppend(appendResponse, http.StatusOK, view); err != nil {
		t.Fatal(err)
	}
	appendBody := appendResponse.Body.String()
	if strings.Contains(appendBody, "<form") ||
		!strings.Contains(appendBody, `<div hx-swap-oob="beforeend:#history-rows">`) ||
		!strings.Contains(appendBody, `id="history-pagination"`) {
		t.Fatalf("append fragment replaced the wrong boundary: %s", appendBody)
	}
}

func TestEventDetailTemplateEscapesPayloadAndExposesSafeControls(t *testing.T) {
	renderer := New()
	response := httptest.NewRecorder()
	view := EventDetailView{
		ID: 4, DetailID: "event-detail-4", FullURL: "/logs/events/4?full=1",
		Message: `<script>alert(1)</script>` + "\nnext", MessageBytes: 32,
		MessageTruncated: true,
		SourceFields:     []DetailField{{Label: "Application", Value: `API <unsafe>`}},
		TimingFields:     []DetailField{{Label: "Event time", Value: "2026-07-28T00:00:00Z"}},
		SeverityFields:   []DetailField{{Label: "Level", Value: "error"}},
		CommonFields:     []DetailField{{Label: "HTTP path", Value: `/<img src=x>`}},
		Attributes:       `{"a":"<b>"}`, AttributesBytes: 11,
		Raw: `<raw>&`, RawBytes: 6,
	}
	if err := renderer.EventDetail(response, http.StatusOK, view); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	for _, want := range []string{
		`tabindex="-1"`, `data-collapse-detail`, `data-copy-target="event-message-4"`,
		`hx-get="/logs/events/4?full=1"`, `API &lt;unsafe&gt;`,
		`&lt;script&gt;alert(1)&lt;/script&gt;`, `&lt;raw&gt;&amp;`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("detail missing %q", want)
		}
	}
	if strings.Contains(body, "<script>alert") || strings.Contains(body, "<img src=x>") {
		t.Fatal("detail emitted unescaped log content")
	}
}

func TestEmbeddedAssetsTypesIntegrityAndSafety(t *testing.T) {
	renderer := New()
	cases := map[string]string{
		"app.css":                            "text/css; charset=utf-8",
		"tokens.css":                         "text/css; charset=utf-8",
		"app.js":                             "text/javascript; charset=utf-8",
		"live.js":                            "text/javascript; charset=utf-8",
		"tokens.js":                          "text/javascript; charset=utf-8",
		"favicon.svg":                        "image/svg+xml",
		"htmx-2.0.11.min.js":                 "text/javascript; charset=utf-8",
		"jetbrains-mono-400-latin.woff2":     "font/woff2",
		"jetbrains-mono-400-latin-ext.woff2": "font/woff2",
		"jetbrains-mono-700-latin.woff2":     "font/woff2",
		"jetbrains-mono-700-latin-ext.woff2": "font/woff2",
	}
	for name, contentType := range cases {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/assets/"+name, nil)
			response := httptest.NewRecorder()
			renderer.Asset(response, request)
			if response.Code != http.StatusOK ||
				response.Header().Get("Content-Type") != contentType {
				t.Fatalf("asset response = %d %#v", response.Code, response.Header())
			}
			if cache := response.Header().Get("Cache-Control"); cache != "no-cache" {
				t.Fatalf("plain asset cache = %q", cache)
			}
			etag := response.Header().Get("ETag")
			if !strings.HasPrefix(etag, `"`) || strings.HasPrefix(etag, "W/") {
				t.Fatalf("plain asset ETag = %q, want strong", etag)
			}
			revalidate := httptest.NewRequest(http.MethodGet, "/assets/"+name, nil)
			revalidate.Header.Set("If-None-Match", etag)
			notModified := httptest.NewRecorder()
			renderer.Asset(notModified, revalidate)
			if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 {
				t.Fatalf("revalidation = %d with %d bytes", notModified.Code, notModified.Body.Len())
			}
			if response.Body.Len() == 0 {
				t.Fatal("asset body is empty")
			}
		})
	}

	htmx, err := files.ReadFile("assets/htmx-2.0.11.min.js")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha512.Sum384(htmx)
	const wantSHA384 = "d8e6adcd0cb51fe65dfc822b8ebd5372e0c6a8b5de1e16e8a00c8963529d40c2a7af82d9db6937d4604b7582879958e0"
	if got := hex.EncodeToString(sum[:]); got != wantSHA384 {
		t.Fatalf("HTMX sha384 = %s", got)
	}
	license, err := files.ReadFile("licenses/HTMX-0BSD.txt")
	if err != nil || !strings.Contains(string(license), "Zero-Clause BSD") {
		t.Fatalf("HTMX license missing or invalid: %v", err)
	}
	// What the scripts and stylesheets do is proven by the Playwright suite
	// (tests/browser), not by searching their source text (audit SEC-15):
	// hostile payloads stay text in History, Live and details; row caps,
	// pending caps, Back/Forward refetches, clipboard and one-time token
	// handling, Live control events, themes, reduced motion, focus
	// indicators and narrow layouts are all exercised in a browser.
}

func TestHashedAssetsAreImmutableAndGzipped(t *testing.T) {
	renderer := New()
	hashedName := regexp.MustCompile(`^[a-z0-9.-]+\.[0-9a-f]{12}\.(css|js|svg|woff2)$`)
	for plain, a := range renderer.assets.byName {
		if !hashedName.MatchString(a.hashedName) {
			t.Fatalf("%s hashed name = %q", plain, a.hashedName)
		}
		request := httptest.NewRequest(http.MethodGet, "/assets/"+a.hashedName, nil)
		request.Header.Set("Accept-Encoding", "gzip, br")
		response := httptest.NewRecorder()
		renderer.Asset(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", a.hashedName, response.Code)
		}
		if cache := response.Header().Get("Cache-Control"); cache != "public, max-age=31536000, immutable" {
			t.Fatalf("%s cache = %q", a.hashedName, cache)
		}
		wantGzip := !strings.HasSuffix(plain, ".woff2")
		gotGzip := response.Header().Get("Content-Encoding") == "gzip"
		if plain == "favicon.svg" {
			wantGzip = gotGzip // 411 bytes may not shrink; either is valid
		}
		if gotGzip != wantGzip {
			t.Fatalf("%s gzip = %v, want %v", plain, gotGzip, wantGzip)
		}
		if gotGzip {
			zr, err := gzip.NewReader(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := io.ReadAll(zr)
			if err != nil || !bytes.Equal(decoded, a.body) {
				t.Fatalf("%s gzip body mismatch: %v", plain, err)
			}
			if !strings.HasSuffix(response.Header().Get("ETag"), `-gzip"`) {
				t.Fatalf("gzip ETag = %q", response.Header().Get("ETag"))
			}
		}
		if plain != "favicon.svg" && !strings.HasSuffix(plain, ".woff2") &&
			response.Header().Get("Vary") != "Accept-Encoding" {
			t.Fatalf("%s Vary = %q", plain, response.Header().Get("Vary"))
		}
	}
	// Stylesheets must reference the fonts by their hashed, immutable names.
	tokens := renderer.assets.byName["tokens.css"].body
	if bytes.Contains(tokens, []byte(`"/assets/jetbrains-mono-400-latin.woff2"`)) ||
		!regexp.MustCompile(`/assets/jetbrains-mono-400-latin\.[0-9a-f]{12}\.woff2`).Match(tokens) {
		t.Fatal("tokens.css does not reference hashed font names")
	}
	// No identity request gets a gzip body.
	identity := httptest.NewRecorder()
	renderer.Asset(identity, httptest.NewRequest(http.MethodGet, "/assets/"+renderer.assets.byName["app.css"].hashedName, nil))
	if identity.Header().Get("Content-Encoding") != "" {
		t.Fatal("identity request received gzip")
	}
}

func TestAssetHandlerRejectsUnknownAndMutation(t *testing.T) {
	renderer := New()
	unknown := httptest.NewRecorder()
	renderer.Asset(unknown, httptest.NewRequest(http.MethodGet, "/assets/secret", nil))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown status = %d", unknown.Code)
	}
	post := httptest.NewRecorder()
	renderer.Asset(post, httptest.NewRequest(http.MethodPost, "/assets/app.css", nil))
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", post.Code)
	}
	head := httptest.NewRecorder()
	renderer.Asset(head, httptest.NewRequest(http.MethodHead, "/assets/app.css", nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD response = %d %d bytes", head.Code, head.Body.Len())
	}
}

func TestSharedComponentPartials(t *testing.T) {
	renderer := New()

	severity := &bytes.Buffer{}
	if err := renderer.templates.ExecuteTemplate(severity, "severity.html", "error"); err != nil {
		t.Fatal(err)
	}
	if severity.String() != `<span class="severity severity-error">ERR</span>` {
		t.Fatalf("severity partial = %q", severity.String())
	}

	for level, code := range map[string]string{
		"trace": "TRC", "debug": "DBG", "info": "INF", "warn": "WRN",
		"error": "ERR", "fatal": "FTL", "unknown": "UNK", "bogus": "UNK",
	} {
		if got := SeverityCode(level); got != code {
			t.Errorf("SeverityCode(%q) = %q, want %q", level, got, code)
		}
	}

	empty := &bytes.Buffer{}
	if err := renderer.templates.ExecuteTemplate(empty, "empty-state.html", EmptyStateView{
		Title:   "No log sources yet",
		Message: `<script>x</script>`,
		Actions: []ActionLink{{Label: "Create server", URL: "/servers"}},
	}); err != nil {
		t.Fatal(err)
	}
	body := empty.String()
	if !strings.Contains(body, "No log sources yet") ||
		!strings.Contains(body, `href="/servers"`) ||
		!strings.Contains(body, "Create server") ||
		strings.Contains(body, "<script>") {
		t.Fatalf("empty-state partial = %q", body)
	}
}

func TestRowToggleLabelNamesRowsFromContent(t *testing.T) {
	long := strings.Repeat("é", 70)
	for _, test := range []struct {
		level, source, message, want string
	}{
		{"error", "api/web", "Database connection failed", "Details: ERR api/web, Database connection failed"},
		{"warn", "api/web", "  first line\nsecond line", "Details: WRN api/web, first line…"},
		{"fatal", "worker", long, "Details: FTL worker, " + strings.Repeat("é", 60) + "…"},
		{"bogus", "worker", "", "Details: UNK worker, empty message"},
		{"info", "api/web", "already cut…", "Details: INF api/web, already cut…"},
	} {
		if got := RowToggleLabel(test.level, test.source, test.message); got != test.want {
			t.Errorf("RowToggleLabel(%q, %q, %q) = %q, want %q",
				test.level, test.source, test.message, got, test.want)
		}
	}
}
