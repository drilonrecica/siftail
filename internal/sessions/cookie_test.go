package sessions

import (
	"net/http"
	"testing"
	"time"
)

func TestCookiePolicyFollowsPublicOrigin(t *testing.T) {
	expires := time.Now().Add(AbsoluteLifetime)
	for _, test := range []struct {
		publicURL string
		name      string
		secure    bool
	}{
		{"http://127.0.0.1:8080", CookieName, false},
		{"http://logs.example.test", CookieName, false},
		{"https://logs.example.test", SecureCookieName, true},
		{"https://logs.example.test:8443/", SecureCookieName, true},
	} {
		policy, err := NewCookiePolicy(test.publicURL)
		if err != nil {
			t.Fatal(err)
		}
		if policy.Name() != test.name || policy.Secure() != test.secure {
			t.Fatalf("%s policy = %q secure=%v", test.publicURL, policy.Name(), policy.Secure())
		}
		cookie, err := policy.New("opaque-token", expires)
		if err != nil {
			t.Fatal(err)
		}
		// A __Host- cookie is only accepted with Secure, Path=/ and no Domain.
		if cookie.Name != test.name || cookie.Value != "opaque-token" ||
			cookie.Path != "/" || cookie.Domain != "" || !cookie.HttpOnly ||
			cookie.Secure != test.secure || cookie.SameSite != http.SameSiteStrictMode ||
			!cookie.Expires.Equal(expires.UTC()) || cookie.MaxAge <= 0 {
			t.Fatalf("cookie = %#v", cookie)
		}
		header := cookie.String()
		if test.secure && header[:len(SecureCookieName)] != SecureCookieName {
			t.Fatalf("Set-Cookie = %q", header)
		}
	}
	for _, invalid := range []string{"", "ftp://invalid", "https://", "logs.example.test"} {
		if _, err := NewCookiePolicy(invalid); err == nil {
			t.Fatalf("invalid public URL %q accepted", invalid)
		}
	}
	policy, _ := NewCookiePolicy("https://logs.example.test")
	if _, err := policy.New("", expires); err == nil {
		t.Fatal("empty token accepted")
	}
}

func TestExpiredCookiesClearSessionAndLegacyName(t *testing.T) {
	secure, _ := NewCookiePolicy("https://logs.example.test")
	cookies := secure.Expired()
	if len(cookies) != 2 || cookies[0].Name != SecureCookieName ||
		cookies[1].Name != CookieName {
		t.Fatalf("https expired cookies = %#v", cookies)
	}
	for _, cookie := range cookies {
		if cookie.MaxAge >= 0 || cookie.Value != "" || !cookie.Secure ||
			cookie.Path != "/" || cookie.Domain != "" {
			t.Fatalf("expired cookie = %#v", cookie)
		}
	}
	plain, _ := NewCookiePolicy("http://127.0.0.1:8080")
	cookies = plain.Expired()
	if len(cookies) != 1 || cookies[0].Name != CookieName || cookies[0].Secure ||
		cookies[0].MaxAge >= 0 {
		t.Fatalf("http expired cookies = %#v", cookies)
	}
}
