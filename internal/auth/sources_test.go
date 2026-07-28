package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSourcesPagesAreProtectedBoundedAndEscaped(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	seedBrowserSources(t, fixture)
	cookie := loginBrowserCookie(t, fixture)

	unauthenticated := httptest.NewRecorder()
	fixture.handler().ServeHTTP(unauthenticated,
		httptest.NewRequest(http.MethodGet, "/sources", nil))
	if unauthenticated.Code != http.StatusSeeOther ||
		!strings.HasPrefix(unauthenticated.Header().Get("Location"), "/login?") {
		t.Fatalf("unauthenticated sources = %d %#v", unauthenticated.Code, unauthenticated.Header())
	}

	request := httptest.NewRequest(http.MethodGet, "/sources?limit=2", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, request)
	body := response.Body.String()
	for _, want := range []string{
		`aria-current="page">Sources</a>`,
		`Public &lt;API&gt;`,
		`<span class="alias-indicator">Alias</span>`,
		`Alpha`, `Project A / Production`, `Inactive`,
		`href="/sources?after=2&amp;limit=2"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("source page missing %q", want)
		}
	}
	if response.Code != http.StatusOK || strings.Contains(body, `<script>`) {
		t.Fatalf("source page = %d %q", response.Code, body)
	}

	next := httptest.NewRequest(http.MethodGet, "/sources?after=2&limit=2", nil)
	next.AddCookie(cookie)
	nextResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(nextResponse, next)
	if nextResponse.Code != http.StatusOK ||
		!strings.Contains(nextResponse.Body.String(), "Project B") ||
		strings.Contains(nextResponse.Body.String(), "Next sources") {
		t.Fatalf("next source page = %d %q", nextResponse.Code, nextResponse.Body.String())
	}
}

func TestSourceDetailShowsStableIdentityAndContainerObservations(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	seedBrowserSources(t, fixture)
	cookie := loginBrowserCookie(t, fixture)

	request := httptest.NewRequest(http.MethodGet, "/sources/1", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, request)
	body := response.Body.String()
	for _, want := range []string{
		`Public &lt;API&gt;`,
		`Stable identity`,
		`<code>project-a</code>`,
		`Container observations`,
		`api-current`,
		`Containers are ephemeral observations`,
		`application=api`,
		`service=web`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("source detail missing %q", want)
		}
	}
	if response.Code != http.StatusOK || strings.Contains(body, `<script>`) {
		t.Fatalf("source detail = %d %q", response.Code, body)
	}

	for _, target := range []string{
		"/sources?unknown=1", "/sources?after=0", "/sources?limit=201",
		"/sources?limit=1&limit=2", "/sources/not-a-number", "/sources/404",
		"/sources/1?unexpected=1",
	} {
		bad := httptest.NewRequest(http.MethodGet, target, nil)
		bad.AddCookie(cookie)
		badResponse := httptest.NewRecorder()
		fixture.handler().ServeHTTP(badResponse, bad)
		want := http.StatusBadRequest
		if target == "/sources/404" {
			want = http.StatusNotFound
		}
		if badResponse.Code != want {
			t.Errorf("%s = %d, want %d", target, badResponse.Code, want)
		}
	}
}

func seedBrowserSources(t *testing.T, fixture *browserFixture) {
	t.Helper()
	now := time.Now().UTC()
	tx, err := fixture.db.Writer().Begin()
	if err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO servers(id,name,created_at_us) VALUES (1,'Alpha',1),(2,'Beta',1)`, nil},
		{`INSERT INTO sources(
			id,server_id,project_key,environment_key,application_key,service_key,
			project_label,environment_label,application_label,service_label,alias,
			first_seen_at_us,last_seen_at_us
		) VALUES
			(1,1,'project-a','production','api','web',
			 'Project A','Production','API','Web','Public <API>',1,?),
			(2,1,'project-a','production','worker','jobs',
			 'Project A','Production','Worker','Jobs',NULL,1,?),
			(3,2,'project-b','staging','api','web',
			 'Project B','Staging','API','Web',NULL,1,?)`,
			[]any{
				now.Add(-time.Hour).UnixMicro(),
				now.Add(-25 * time.Hour).UnixMicro(),
				now.Add(-91 * 24 * time.Hour).UnixMicro(),
			}},
		{`INSERT INTO container_instances(
			id,source_id,container_name,first_seen_at_us,last_seen_at_us
		) VALUES (1,1,'api-current',1,?)`, []any{now.Add(-time.Hour).UnixMicro()}},
		{`INSERT INTO log_events(
			id,event_at_us,received_at_us,source_id,container_instance_id,
			stream,level_normalized,message_raw,message_text
		) VALUES (1,?,?,1,1,'stdout','info',x'6f6b','ok')`,
			[]any{now.UnixMicro(), now.UnixMicro()}},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement.query, statement.args...); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
