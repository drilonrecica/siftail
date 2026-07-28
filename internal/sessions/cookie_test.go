package sessions

import (
	"net/http"
	"testing"
	"time"
)

func TestCookieAttributesFollowPublicURL(t *testing.T) {
	expires := time.Now().Add(AbsoluteLifetime)
	for _, test := range []struct {
		publicURL string
		secure    bool
	}{
		{"", false},
		{"http://logs.example.test", false},
		{"https://logs.example.test", true},
	} {
		cookie, err := NewCookie("opaque-token", expires, test.publicURL)
		if err != nil {
			t.Fatal(err)
		}
		if cookie.Name != CookieName || cookie.Value != "opaque-token" ||
			cookie.Path != "/" || !cookie.HttpOnly || cookie.Secure != test.secure ||
			cookie.SameSite != http.SameSiteStrictMode || !cookie.Expires.Equal(expires.UTC()) ||
			cookie.MaxAge <= 0 {
			t.Fatalf("cookie = %#v", cookie)
		}
	}
	if _, err := NewCookie("opaque-token", time.Now(), "ftp://invalid"); err == nil {
		t.Fatal("invalid public URL accepted")
	}
}
