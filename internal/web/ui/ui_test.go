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
		`hx-history="false"`,
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

func TestEmbeddedAssetsTypesIntegrityAndSafety(t *testing.T) {
	renderer := New()
	cases := map[string]string{
		"app.css":            "text/css; charset=utf-8",
		"app.js":             "text/javascript; charset=utf-8",
		"favicon.svg":        "image/svg+xml",
		"htmx-2.0.10.min.js": "text/javascript; charset=utf-8",
	}
	for name, contentType := range cases {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/assets/"+name, nil)
			response := httptest.NewRecorder()
			renderer.Asset(response, request)
			if response.Code != http.StatusOK ||
				response.Header().Get("Content-Type") != contentType ||
				!strings.Contains(response.Header().Get("Cache-Control"), "immutable") {
				t.Fatalf("asset response = %d %#v", response.Code, response.Header())
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
		!strings.Contains(string(appJS), "historyCacheSize = 0") {
		t.Fatal("application JavaScript violates DOM/history constraints")
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
