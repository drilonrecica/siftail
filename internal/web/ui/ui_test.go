package ui

import (
	"crypto/sha512"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
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
	for _, want := range []string{
		`href="/assets/app.css"`,
		`src="/assets/htmx-2.0.10.min.js"`,
		`src="/assets/app.js"`,
		`src="/assets/live.js"`,
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

func TestLiveShellRendersExplicitEscapedBoundedWorkspace(t *testing.T) {
	renderer := New()
	response := httptest.NewRecorder()
	view := LiveView{
		CanonicalURL:  `/logs?mode=live&amp;contains=%3Cunsafe%3E`,
		StreamURL:     `/logs/live/stream?contains=%3Cunsafe%3E`,
		HistoryURL:    "/logs",
		SourceSummary: `API <unsafe>`,
		Sources: []SelectOption{{
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
		`aria-selected="true" href=`, `data-live-pause`, `data-live-newest`,
		`data-live-clear`, `>Clear view<`, `data-live-reconnect`,
		`data-live-pending hidden`, `aria-label="Live log events"`,
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

func TestHistoryFragmentsRenderFocusedEscapedState(t *testing.T) {
	renderer := New()
	view := HistoryView{
		CanonicalURL:  `/logs?contains=%3Cscript%3E`,
		From:          "2026-07-28T00:00:00Z",
		To:            "2026-07-28T01:00:00Z",
		RangeSummary:  "28 Jul 2026 00:00–01:00 UTC",
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
			Timestamp:    "00:30:00.000 UTC", Level: "error", Stream: "stderr",
			Source: `API <unsafe>`, Message: `<img src=x onerror=alert(1)>`,
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
		`hx-target="#history-region"`,
		`delay:400ms`,
		`data-list-filter="levels" checked`,
		`API &lt;unsafe&gt;`,
		`&lt;img src=x onerror=alert(1)&gt;`,
		`id="load-older"`,
		`hx-disabled-elt="this"`,
		`aria-controls="event-detail-7"`,
		`hx-get="/logs/events/7"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("History fragment missing %q", want)
		}
	}
	if strings.Contains(body, `<img src=x`) || strings.Contains(body, `<script>alert`) {
		t.Fatal("History fragment emitted unescaped event/filter data")
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
		"app.css":            "text/css; charset=utf-8",
		"app.js":             "text/javascript; charset=utf-8",
		"live.js":            "text/javascript; charset=utf-8",
		"favicon.svg":        "image/svg+xml",
		"htmx-2.0.10.min.js": "text/javascript; charset=utf-8",
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
			cache := response.Header().Get("Cache-Control")
			if name == "htmx-2.0.10.min.js" && !strings.Contains(cache, "immutable") {
				t.Fatalf("versioned asset cache = %q", cache)
			}
			if name != "htmx-2.0.10.min.js" && cache != "no-cache" {
				t.Fatalf("mutable asset cache = %q", cache)
			}
			if response.Body.Len() == 0 {
				t.Fatal("asset body is empty")
			}
		})
	}

	htmx, err := files.ReadFile("assets/htmx-2.0.10.min.js")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha512.Sum384(htmx)
	const wantSHA384 = "1f94ab71fca01e602e4c366984c1ea0492dcdc586cb0a8c6ef0fc2782a4545e49fc015834caa64ccf3fc73e70bb0af95"
	if got := hex.EncodeToString(sum[:]); got != wantSHA384 {
		t.Fatalf("HTMX sha384 = %s", got)
	}
	license, err := files.ReadFile("licenses/HTMX-0BSD.txt")
	if err != nil || !strings.Contains(string(license), "Zero-Clause BSD") {
		t.Fatalf("HTMX license missing or invalid: %v", err)
	}
	appJS, err := files.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(appJS), "innerHTML") ||
		!strings.Contains(string(appJS), "historyCacheSize = 0") ||
		!strings.Contains(string(appJS), "rows.length > 1000") ||
		!strings.Contains(string(appJS), "navigator.clipboard.writeText(source.textContent)") ||
		!strings.Contains(string(appJS), "replaceChildren()") {
		t.Fatal("application JavaScript violates DOM/history constraints")
	}
	liveJS, err := files.ReadFile("assets/live.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{".innerHTML =", "insertAdjacentHTML", "document.write"} {
		if strings.Contains(string(liveJS), forbidden) {
			t.Fatalf("Live JavaScript contains unsafe DOM API %q", forbidden)
		}
	}
	for _, required := range []string{
		"const renderedLimit = 1000",
		"const pendingLimit = 2000",
		"message.textContent",
		"eventSource.close()",
		`window.addEventListener("pagehide"`,
	} {
		if !strings.Contains(string(liveJS), required) {
			t.Errorf("Live JavaScript missing lifecycle constraint %q", required)
		}
	}
	css, err := files.ReadFile("assets/app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`:root[data-theme="light"]`,
		`prefers-color-scheme: light`,
		`prefers-reduced-motion: reduce`,
		`@media (max-width: 32rem)`,
		`:focus-visible`,
	} {
		if !strings.Contains(string(css), want) {
			t.Errorf("CSS missing %q", want)
		}
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
