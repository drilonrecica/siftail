package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/logs"
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

func TestSourceMutationsRequireProtectionConfirmationAndPublishControls(t *testing.T) {
	fixture, broker := newLiveBrowserFixture(t, logs.LiveBrokerOptions{})
	seedBrowserSources(t, fixture)
	cookie := loginBrowserCookie(t, fixture)

	noCSRF := sourceMutationRequest(t, fixture, cookie,
		"/sources/1/alias", url.Values{"alias": {"Unsafe"}}, false)
	noCSRFResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(noCSRFResponse, noCSRF)
	if noCSRFResponse.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d", noCSRFResponse.Code)
	}
	wrongOrigin := sourceMutationRequest(t, fixture, cookie,
		"/sources/1/alias", url.Values{"alias": {"Unsafe"}}, true)
	wrongOrigin.Header.Set("Origin", "https://attacker.example")
	wrongOriginResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(wrongOriginResponse, wrongOrigin)
	if wrongOriginResponse.Code != http.StatusForbidden {
		t.Fatalf("wrong Origin status = %d", wrongOriginResponse.Code)
	}
	invalidAlias := sourceMutationRequest(t, fixture, cookie,
		"/sources/1/alias", url.Values{"alias": {strings.Repeat("x", 129)}}, true)
	invalidAliasResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(invalidAliasResponse, invalidAlias)
	if invalidAliasResponse.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(invalidAliasResponse.Body.String(), `id="source-alias"`) ||
		!strings.Contains(invalidAliasResponse.Body.String(), "autofocus") {
		t.Fatalf("invalid alias = %d %q",
			invalidAliasResponse.Code, invalidAliasResponse.Body.String())
	}
	unknownField := sourceMutationRequest(t, fixture, cookie,
		"/sources/1/alias", url.Values{"alias": {"Unsafe"}, "role": {"admin"}}, true)
	unknownFieldResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(unknownFieldResponse, unknownField)
	if unknownFieldResponse.Code != http.StatusBadRequest {
		t.Fatalf("unknown alias field status = %d", unknownFieldResponse.Code)
	}

	alias := sourceMutationRequest(t, fixture, cookie,
		"/sources/1/alias", url.Values{"alias": {"Friendly API"}}, true)
	aliasResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(aliasResponse, alias)
	if aliasResponse.Code != http.StatusSeeOther ||
		aliasResponse.Header().Get("Location") != "/sources/1?notice=alias-updated" {
		t.Fatalf("alias response = %d %#v", aliasResponse.Code, aliasResponse.Header())
	}
	var sourceAlias string
	if err := fixture.db.Reader().QueryRow(`SELECT alias FROM sources WHERE id=1`).
		Scan(&sourceAlias); err != nil || sourceAlias != "Friendly API" {
		t.Fatalf("stored alias = %q, err=%v", sourceAlias, err)
	}

	badClear := sourceMutationRequest(t, fixture, cookie,
		"/sources/1/clear-logs", url.Values{"confirmation": {"API/Web"}}, true)
	badClearResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(badClearResponse, badClear)
	if badClearResponse.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(badClearResponse.Body.String(), "Type the displayed source name exactly") {
		t.Fatalf("bad clear = %d %q", badClearResponse.Code, badClearResponse.Body.String())
	}
	assertBrowserCount(t, fixture, `SELECT count(*) FROM log_events WHERE source_id=1`, 1)

	sourceOne := subscribeAuthLive(t, broker, logs.LiveFilter{SourceIDs: []int64{1}})
	clear := sourceMutationRequest(t, fixture, cookie,
		"/sources/1/clear-logs", url.Values{"confirmation": {"Friendly API"}}, true)
	clearResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(clearResponse, clear)
	if clearResponse.Code != http.StatusSeeOther ||
		clearResponse.Header().Get("Location") != "/sources/1?notice=logs-cleared" {
		t.Fatalf("clear response = %d %#v", clearResponse.Code, clearResponse.Header())
	}
	clearControl := nextAuthLive(t, sourceOne)
	if clearControl.Type != logs.LiveMessageControl ||
		clearControl.Control.Type != logs.LiveControlSourcePurged {
		t.Fatalf("clear control = %#v", clearControl)
	}
	assertBrowserCount(t, fixture, `SELECT count(*) FROM log_events WHERE source_id=1`, 0)
	assertBrowserCount(t, fixture, `SELECT count(*) FROM sources WHERE id=1 AND alias='Friendly API'`, 1)
	assertBrowserCount(t, fixture, `SELECT count(*) FROM container_instances WHERE source_id=1`, 1)

	badRemove := sourceMutationRequest(t, fixture, cookie,
		"/sources/2/remove", url.Values{"confirmation": {"Worker/Jobs"}}, true)
	badRemoveResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(badRemoveResponse, badRemove)
	if badRemoveResponse.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(badRemoveResponse.Body.String(), "complete removal phrase") {
		t.Fatalf("bad remove = %d %q", badRemoveResponse.Code, badRemoveResponse.Body.String())
	}
	sourceTwo := subscribeAuthLive(t, broker, logs.LiveFilter{SourceIDs: []int64{2}})
	remove := sourceMutationRequest(t, fixture, cookie,
		"/sources/2/remove", url.Values{"confirmation": {"remove Worker/Jobs"}}, true)
	removeResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(removeResponse, remove)
	if removeResponse.Code != http.StatusSeeOther ||
		removeResponse.Header().Get("Location") != "/sources?notice=source-removed" {
		t.Fatalf("remove response = %d %#v", removeResponse.Code, removeResponse.Header())
	}
	removeControl := nextAuthLive(t, sourceTwo)
	if removeControl.Type != logs.LiveMessageControl ||
		removeControl.Control.Type != logs.LiveControlSourceRemoved {
		t.Fatalf("remove control = %#v", removeControl)
	}
	assertBrowserCount(t, fixture, `SELECT count(*) FROM sources WHERE id=2`, 0)
	assertBrowserCount(t, fixture, `SELECT count(*) FROM servers WHERE id=1`, 1)
}

func sourceMutationRequest(
	t *testing.T,
	fixture *browserFixture,
	cookie *http.Cookie,
	target string,
	values url.Values,
	withCSRF bool,
) *http.Request {
	t.Helper()
	if withCSRF {
		values.Set("csrf_token", CSRFToken(cookie.Value))
	}
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", fixture.browser.publicURL)
	request.AddCookie(cookie)
	return request
}

func subscribeAuthLive(
	t *testing.T,
	broker *logs.LiveBroker,
	filter logs.LiveFilter,
) *logs.LiveSubscription {
	t.Helper()
	subscription, err := broker.Subscribe(context.Background(), filter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(subscription.Close)
	return subscription
}

func nextAuthLive(t *testing.T, subscription *logs.LiveSubscription) logs.LiveMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	message, err := subscription.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func assertBrowserCount(t *testing.T, fixture *browserFixture, query string, want int) {
	t.Helper()
	var got int
	if err := fixture.db.Reader().QueryRow(query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s = %d, want %d", query, got, want)
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
