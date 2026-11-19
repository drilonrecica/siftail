package ui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// TestPageMetaNamesEntitiesInTitleAndHeading checks DESIGN.md §6.4: one h1
// per page, detail pages named after the entity in both the h1 and the
// <title>, the ADR 0011 D5 context lines, and no eyebrow (D15).
func TestPageMetaNamesEntitiesInTitleAndHeading(t *testing.T) {
	renderer := New()
	cases := []struct {
		name    string
		view    ShellView
		title   string
		heading string
		context string
	}{
		{"history", ShellView{Mode: "history", History: HistoryView{
			SourceSummary: AllSourcesSummary, RangeSummary: "Last hour"}},
			"History", "Logs", "All sources · Last hour"},
		{"live", ShellView{Mode: "live", Live: LiveView{SourceSummary: "nextup/api"}},
			"Live", "Logs", "nextup/api"},
		{"sources", ShellView{Mode: "sources"},
			"Sources", "Sources", "Services that have sent logs"},
		{"source detail", ShellView{Mode: "source-detail", Source: SourceDetailView{
			SourceRowView: SourceRowView{DisplayName: "api/web", Server: "prod",
				Project: "nextup", Environment: "production"}}},
			"api/web", "api/web", "prod · nextup / production"},
		{"servers", ShellView{Mode: "servers"},
			"Servers", "Servers", "Each server sends logs with its own token"},
		{"server detail", ShellView{Mode: "server-detail", Server: ServerDetailView{
			ServerRowView: ServerRowView{Name: "prod-eu", Hostname: "eu.example.test"}}},
			"prod-eu", "prod-eu", "eu.example.test"},
		{"token", ShellView{Mode: "token-created", Token: OneTimeTokenView{ServerName: "prod-eu"}},
			"New token for prod-eu", "prod-eu", "New ingestion token"},
		{"settings", ShellView{Mode: "settings"}, "Settings", "Settings", "Retention"},
		{"status", ShellView{Mode: "status", Status: StatusView{Severity: "Attention"}},
			"Status", "Status", "Attention"},
		{"audit", ShellView{Mode: "audit"},
			"Audit", "Audit", "Sign-ins and administrative actions"},
		{"backup", ShellView{Mode: "backup"}, "Backup", "Backup", "Database backups"},
	}
	h1 := regexp.MustCompile(`<h1[ >]`)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			if err := renderer.Shell(response, http.StatusOK, tc.view); err != nil {
				t.Fatal(err)
			}
			body := response.Body.String()
			for _, want := range []string{
				"<title>" + tc.title + " · Siftail</title>",
				"<h1>" + tc.heading + "</h1>",
				`<p class="workspace-context">` + tc.context + "</p>",
			} {
				if !strings.Contains(body, want) {
					t.Errorf("missing %q", want)
				}
			}
			if count := len(h1.FindAllString(body, -1)); count != 1 {
				t.Errorf("h1 count = %d, want 1", count)
			}
			if strings.Contains(body, "eyebrow") {
				t.Error("page still renders an eyebrow label")
			}
		})
	}
}

// TestLiveEmptyStateFollowsScopeAndLinksToHistory checks DESIGN.md §24.4.
func TestLiveEmptyStateFollowsScopeAndLinksToHistory(t *testing.T) {
	renderer := New()
	for _, tc := range []struct{ summary, want string }{
		{AllSourcesSummary, "Waiting for events from all sources"},
		{"nextup/api", "Waiting for events from nextup/api"},
	} {
		response := httptest.NewRecorder()
		if err := renderer.Shell(response, http.StatusOK, ShellView{Mode: "live", Live: LiveView{
			SourceSummary: tc.summary, HistoryURL: "/logs?mode=history&levels=error",
		}}); err != nil {
			t.Fatal(err)
		}
		body := response.Body.String()
		if !strings.Contains(body, "<h2>"+tc.want+"</h2>") ||
			!strings.Contains(body, `<a class="button" href="/logs?mode=history&amp;levels=error" data-live-empty-history>Look in History</a>`) {
			t.Errorf("Live empty state for %q lacks scope phrase or History link", tc.summary)
		}
	}
}

// TestHeaderCollapsesIntoOneMenu checks the markup DESIGN.md §6.2 needs:
// the System menu carries the primary destinations, so CSS can collapse the
// header into one menu at narrow widths without JavaScript.
func TestHeaderCollapsesIntoOneMenu(t *testing.T) {
	response := httptest.NewRecorder()
	if err := New().Shell(response, http.StatusOK, ShellView{Mode: "sources"}); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	start := strings.Index(body, `<details class="system-menu"`)
	end := strings.Index(body, `</details>`)
	if start < 0 || end < start {
		t.Fatal("system menu not found")
	}
	menu := body[start:end]
	for _, want := range []string{
		`<span class="system-menu-label">System <svg`,
		`<span class="system-menu-label-narrow"><svg class="icon" aria-hidden="true" focusable="false"><use href="#icon-menu"/></svg> Menu</span>`,
		`data-menu-primary`,
		`<a href="/logs">Logs</a>`,
		`<a href="/sources" aria-current="page">Sources</a>`,
		`<a href="/servers">Servers</a>`,
		`<a href="/status">Status</a>`,
	} {
		if !strings.Contains(menu, want) {
			t.Errorf("system menu missing %q", want)
		}
	}
}

// TestTemplatesUseOperatorCopy enforces DESIGN.md §36.2-§36.3 on every
// template: none of the implementation words, "Server" only as a label or
// heading start, and no heading inside a notice.
func TestTemplatesUseOperatorCopy(t *testing.T) {
	banned := regexp.MustCompile(`(?i)\b(committed|canonical|bounded|deliberate|synthetic|trusted|` +
		`sanitized|immutable|appliance|artifacts?|receipt|stable identity|normali[sz]ed)\b`)
	comment := regexp.MustCompile(`(?s)\{\{/\*.*?\*/\}\}`)
	tag := regexp.MustCompile(`(?s)<[^>]*>|\{\{[^}]*\}\}`)
	midSentenceServer := regexp.MustCompile(`[a-z,] Servers?\b`)
	noticeHeading := regexp.MustCompile(`class="notice[^"]*"[^>]*>\s*<h[1-6]`)
	names, err := fs.Glob(files, "templates/*.html")
	if err != nil || len(names) == 0 {
		t.Fatalf("templates = %v, err=%v", names, err)
	}
	for _, name := range names {
		raw, err := files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		if noticeHeading.MatchString(source) {
			t.Errorf("%s puts a heading inside a notice", name)
		}
		// Only the text an operator reads: comments, tags, attributes, and
		// template actions are removed first.
		text := tag.ReplaceAllString(comment.ReplaceAllString(source, " "), " ")
		if found := banned.FindAllString(text, -1); len(found) > 0 {
			t.Errorf("%s uses implementation words %q", name, found)
		}
		if found := midSentenceServer.FindAllString(text, -1); len(found) > 0 {
			t.Errorf("%s capitalises server mid-sentence: %q", name, found)
		}
	}
}
