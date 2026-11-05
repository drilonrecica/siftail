package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/audit"
	"github.com/drilonrecica/siftail/internal/backup"
)

func TestBrowserBackupIsProtectedStartsAndShowsVerifiedOutcome(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)
	handler := fixture.handler()

	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated,
		httptest.NewRequest(http.MethodGet, "/backup", nil))
	if unauthenticated.Code != http.StatusSeeOther {
		t.Fatalf("unauthenticated backup = %d", unauthenticated.Code)
	}

	pageRequest := httptest.NewRequest(http.MethodGet, "/backup", nil)
	pageRequest.AddCookie(cookie)
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, pageRequest)
	body := page.Body.String()
	if page.Code != http.StatusOK ||
		page.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(body, `aria-current="page">Backup</a>`) ||
		!strings.Contains(body, "Browser sessions are always excluded") ||
		!strings.Contains(body, "Configuration-only backup") ||
		!strings.Contains(body, "replaces the database") ||
		!strings.Contains(body, "not a merge") ||
		!strings.Contains(body, "Verification is read-only") ||
		!strings.Contains(body, "restore --confirm RESTORE") ||
		!strings.Contains(body, "siftail.db.rollback") ||
		!strings.Contains(body, "Never copy only the live main database file") {
		t.Fatalf("backup page = %d %#v %q", page.Code, page.Header(), body)
	}

	backupDirectory := fixture.backupDir
	output := filepath.Join(backupDirectory, "private-server-path-full.sqlite")
	form := url.Values{
		"csrf_token": {CSRFToken(cookie.Value)},
		"file_name":  {"private-server-path-full.sqlite"},
	}
	start := httptest.NewRequest(
		http.MethodPost, "/backup/full", strings.NewReader(form.Encode()),
	)
	start.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	start.Header.Set("Origin", "https://logs.example.test")
	start.AddCookie(cookie)
	startResponse := httptest.NewRecorder()
	handler.ServeHTTP(startResponse, start)
	if startResponse.Code != http.StatusSeeOther ||
		startResponse.Header().Get("Location") != "/backup" {
		t.Fatalf("backup start = %d %q %q",
			startResponse.Code, startResponse.Header().Get("Location"),
			startResponse.Body.String())
	}
	waitBrowserBackup(t, fixture, backup.StateSucceeded)

	completedRequest := httptest.NewRequest(http.MethodGet, "/backup", nil)
	completedRequest.AddCookie(cookie)
	completed := httptest.NewRecorder()
	handler.ServeHTTP(completed, completedRequest)
	body = completed.Body.String()
	if completed.Code != http.StatusOK ||
		!strings.Contains(body, "Backup verified.") ||
		!strings.Contains(body, "private-server-path-full.sqlite") ||
		!strings.Contains(body, "SHA-256") ||
		strings.Contains(body, backupDirectory) {
		t.Fatalf("completed backup = %d %q", completed.Code, body)
	}
	if info, err := os.Lstat(output); err != nil ||
		!info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("browser backup artifact = %v %v", info, err)
	}

	auditPage, err := fixture.browser.audit.List(context.Background(), audit.Query{
		Action: "backup.full", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(auditPage.Events) != 1 ||
		auditPage.Events[0].ActorType != audit.ActorAdministrator ||
		auditPage.Events[0].AdministratorID == nil ||
		auditPage.Events[0].Outcome != audit.OutcomeSucceeded {
		t.Fatalf("browser backup audit = %#v", auditPage.Events)
	}

	// An empty name asks the server to generate one inside the directory.
	form = url.Values{
		"csrf_token": {CSRFToken(cookie.Value)},
		"file_name":  {""},
	}
	start = httptest.NewRequest(
		http.MethodPost, "/backup/configuration",
		strings.NewReader(form.Encode()),
	)
	start.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	start.Header.Set("Origin", "https://logs.example.test")
	start.AddCookie(cookie)
	startResponse = httptest.NewRecorder()
	handler.ServeHTTP(startResponse, start)
	if startResponse.Code != http.StatusSeeOther {
		t.Fatalf("configuration backup start = %d %q",
			startResponse.Code, startResponse.Body.String())
	}
	configurationStatus := waitBrowserBackup(
		t, fixture, backup.StateSucceeded,
	)
	if configurationStatus.Result == nil ||
		configurationStatus.Result.Type != backup.TypeConfiguration {
		t.Fatalf("configuration status = %#v", configurationStatus)
	}
	configurationName := configurationStatus.Result.Name
	if !strings.HasPrefix(configurationName, "siftail-configuration-") ||
		!strings.HasSuffix(configurationName, "Z.sqlite") {
		t.Fatalf("generated configuration name = %q", configurationName)
	}
	if _, err := os.Lstat(
		filepath.Join(backupDirectory, configurationName),
	); err != nil {
		t.Fatalf("generated configuration artifact: %v", err)
	}

	form = url.Values{
		"csrf_token":    {CSRFToken(cookie.Value)},
		"artifact_name": {configurationName},
	}
	start = httptest.NewRequest(
		http.MethodPost, "/backup/verify", strings.NewReader(form.Encode()),
	)
	start.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	start.Header.Set("Origin", "https://logs.example.test")
	start.AddCookie(cookie)
	startResponse = httptest.NewRecorder()
	handler.ServeHTTP(startResponse, start)
	if startResponse.Code != http.StatusSeeOther {
		t.Fatalf("verification start = %d %q",
			startResponse.Code, startResponse.Body.String())
	}
	verificationStatus := waitBrowserBackup(
		t, fixture, backup.StateSucceeded,
	)
	if verificationStatus.Operation != backup.OperationVerify ||
		verificationStatus.Result == nil ||
		verificationStatus.Result.Type != backup.TypeConfiguration {
		t.Fatalf("verification status = %#v", verificationStatus)
	}
}

func TestBrowserBackupRequiresCSRFOriginAndPlainName(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)
	for _, test := range []struct {
		name   string
		route  string
		field  string
		origin string
		csrf   string
		path   string
		status int
	}{
		{
			"full origin", "/backup/full", "file_name",
			"https://attacker.test", CSRFToken(cookie.Value), "x.sqlite", 403,
		},
		{
			"configuration csrf", "/backup/configuration", "file_name",
			"https://logs.example.test", "wrong", "x.sqlite", 403,
		},
		{
			"verification origin", "/backup/verify", "artifact_name",
			"https://attacker.test", CSRFToken(cookie.Value), "x.sqlite", 403,
		},
		{
			"bounded path", "/backup/full", "file_name",
			"https://logs.example.test", CSRFToken(cookie.Value), "bad\npath", 400,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			form := url.Values{
				"csrf_token": {test.csrf}, test.field: {test.path},
			}
			request := httptest.NewRequest(
				http.MethodPost, test.route,
				strings.NewReader(form.Encode()),
			)
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("Origin", test.origin)
			request.AddCookie(cookie)
			response := httptest.NewRecorder()
			fixture.handler().ServeHTTP(response, request)
			if response.Code != test.status ||
				strings.Contains(response.Body.String(), test.path) {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
		})
	}
}

func postBrowserBackup(
	t *testing.T,
	fixture *browserFixture,
	cookie *http.Cookie,
	route string,
	field string,
	value string,
) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"csrf_token": {CSRFToken(cookie.Value)}, field: {value}}
	request := httptest.NewRequest(
		http.MethodPost, route, strings.NewReader(form.Encode()),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://logs.example.test")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, request)
	return response
}

// TestBrowserBackupRefusesPathsAndTraversal covers audit SEC-11: the browser
// names only a plain file inside SIFTAIL_BACKUP_DIR, so absolute paths,
// traversal, separators, hidden staging names, and the retired path fields
// are refused before any job starts or any file is touched.
func TestBrowserBackupRefusesPathsAndTraversal(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)
	outside := t.TempDir()
	for _, value := range []string{
		filepath.Join(outside, "absolute.sqlite"),
		"../escape.sqlite",
		"../../" + filepath.Base(outside) + "/escape.sqlite",
		"..",
		".",
		"sub/escape.sqlite",
		`sub\escape.sqlite`,
		".siftail-full.sqlite.partial-000000",
		"-dash.sqlite",
		"space name.sqlite",
		strings.Repeat("a", backup.MaxFileNameLength+1),
	} {
		for _, target := range []struct{ route, field string }{
			{"/backup/full", "file_name"},
			{"/backup/configuration", "file_name"},
			{"/backup/verify", "artifact_name"},
		} {
			response := postBrowserBackup(
				t, fixture, cookie, target.route, target.field, value,
			)
			body := response.Body.String()
			if response.Code != http.StatusBadRequest ||
				!strings.Contains(body, "Paths are not accepted.") ||
				(len(value) > 3 && strings.Contains(body, value)) {
				t.Fatalf("%s %q = %d %q", target.route, value, response.Code, body)
			}
		}
	}
	for _, retired := range []struct{ route, field string }{
		{"/backup/full", "output_path"},
		{"/backup/configuration", "output_path"},
		{"/backup/verify", "artifact_path"},
	} {
		response := postBrowserBackup(
			t, fixture, cookie, retired.route, retired.field,
			filepath.Join(outside, "retired.sqlite"),
		)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("retired %s field = %d", retired.field, response.Code)
		}
	}
	if status := fixture.browser.backups.Snapshot(); status.State != backup.StateIdle {
		t.Fatalf("refused request started a job: %#v", status)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("outside directory = %v %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(
		filepath.Dir(fixture.backupDir), "escape.sqlite",
	)); err == nil {
		t.Fatal("traversal wrote beside the backup directory")
	}
}

// TestBrowserBackupRefusesSymlinksOutOfDirectory plants symbolic links inside
// SIFTAIL_BACKUP_DIR that point elsewhere. Creation never follows a link at
// the final name and verification never reads through one.
func TestBrowserBackupRefusesSymlinksOutOfDirectory(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)
	outside := t.TempDir()

	// A valid artifact outside the directory, as the CLI may still create.
	outsideArtifact := filepath.Join(outside, "outside-full.sqlite")
	if _, err := fixture.browser.backups.Start(
		context.Background(), outsideArtifact,
	); err != nil {
		t.Fatal(err)
	}
	waitBrowserBackup(t, fixture, backup.StateSucceeded)

	if err := os.MkdirAll(fixture.backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	danglingTarget := filepath.Join(outside, "dangling-target.sqlite")
	if err := os.Symlink(
		danglingTarget, filepath.Join(fixture.backupDir, "dangling.sqlite"),
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		outsideArtifact, filepath.Join(fixture.backupDir, "linked.sqlite"),
	); err != nil {
		t.Fatal(err)
	}
	outsideBefore, err := os.ReadFile(outsideArtifact)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"dangling.sqlite", "linked.sqlite"} {
		response := postBrowserBackup(
			t, fixture, cookie, "/backup/full", "file_name", name,
		)
		if response.Code != http.StatusSeeOther {
			t.Fatalf("full backup over symlink %q = %d %q",
				name, response.Code, response.Body.String())
		}
		status := waitBrowserBackupDone(t, fixture)
		if status.State != backup.StateFailed {
			t.Fatalf("full backup over symlink %q = %#v", name, status)
		}
	}
	failures, err := fixture.browser.audit.List(context.Background(), audit.Query{
		Action: "backup.full", Outcome: audit.OutcomeFailed, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(failures.Events) != 2 {
		t.Fatalf("symlink backup failure audit = %#v", failures.Events)
	}
	for _, event := range failures.Events {
		if event.Metadata[audit.MetadataResultCategory] != "invalid_output" {
			t.Fatalf("symlink backup failure category = %#v", event.Metadata)
		}
	}
	if _, err := os.Lstat(danglingTarget); !os.IsNotExist(err) {
		t.Fatalf("backup followed a dangling symlink out of the directory: %v", err)
	}
	outsideAfter, err := os.ReadFile(outsideArtifact)
	if err != nil || string(outsideAfter) != string(outsideBefore) {
		t.Fatalf("backup changed the symlink target: %v", err)
	}

	for _, name := range []string{"linked.sqlite", "dangling.sqlite", "missing.sqlite"} {
		response := postBrowserBackup(
			t, fixture, cookie, "/backup/verify", "artifact_name", name,
		)
		if response.Code != http.StatusSeeOther {
			t.Fatalf("verify %q = %d %q", name, response.Code, response.Body.String())
		}
		status := waitBrowserBackupDone(t, fixture)
		if status.State != backup.StateFailed ||
			status.Operation != backup.OperationVerify {
			t.Fatalf("verify %q through symlink = %#v", name, status)
		}
	}
	for _, name := range []string{"dangling.sqlite", "linked.sqlite"} {
		info, err := os.Lstat(filepath.Join(fixture.backupDir, name))
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("planted symlink %q was replaced: %v %v", name, info, err)
		}
	}
}

func TestBrowserBackupReportsUnavailableDirectory(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)
	// A regular file where the directory should be cannot hold backups.
	if err := os.WriteFile(fixture.backupDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct{ route, field string }{
		{"/backup/full", "file_name"},
		{"/backup/verify", "artifact_name"},
	} {
		response := postBrowserBackup(
			t, fixture, cookie, target.route, target.field, "x.sqlite",
		)
		body := response.Body.String()
		if response.Code != http.StatusServiceUnavailable ||
			!strings.Contains(body, "SIFTAIL_BACKUP_DIR") ||
			strings.Contains(body, fixture.backupDir) {
			t.Fatalf("%s = %d %q", target.route, response.Code, body)
		}
	}
	if status := fixture.browser.backups.Snapshot(); status.State != backup.StateIdle {
		t.Fatalf("unavailable directory started a job: %#v", status)
	}
}

func waitBrowserBackupDone(t *testing.T, fixture *browserFixture) backup.Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status := fixture.browser.backups.Snapshot()
		if status.State != backup.StateRunning {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("backup status = %#v", status)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitBrowserBackup(
	t *testing.T,
	fixture *browserFixture,
	want string,
) backup.Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status := fixture.browser.backups.Snapshot()
		if status.State == want {
			return status
		}
		if status.State == backup.StateFailed ||
			status.State == backup.StateCanceled ||
			time.Now().After(deadline) {
			t.Fatalf("backup status = %#v", status)
		}
		time.Sleep(time.Millisecond)
	}
}
