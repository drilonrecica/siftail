package sessions

import (
	"errors"
	"net/http"
	"net/url"
	"time"
)

const (
	// CookieName is the session cookie on an http public origin, which exists
	// for loopback and private access only.
	CookieName = "siftail_session"
	// SecureCookieName is the session cookie on an https public origin. The
	// __Host- prefix makes browsers refuse it unless it is Secure, has
	// Path=/, and has no Domain, so no sibling host can plant or shadow it.
	SecureCookieName = "__Host-siftail_session"
)

// CookiePolicy derives the session cookie's name and attributes from the
// configured public origin alone, never from Host or forwarded headers
// (ADR 0010).
type CookiePolicy struct {
	secure bool
}

// NewCookiePolicy parses the configured SIFTAIL_PUBLIC_URL. Only http and
// https origins with a host are accepted.
func NewCookiePolicy(publicURL string) (CookiePolicy, error) {
	parsed, err := url.Parse(publicURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return CookiePolicy{}, errors.New("public URL is invalid")
	}
	return CookiePolicy{secure: parsed.Scheme == "https"}, nil
}

// Secure reports whether the public origin is https.
func (p CookiePolicy) Secure() bool { return p.secure }

// Name is the session cookie name for this origin.
func (p CookiePolicy) Name() string {
	if p.secure {
		return SecureCookieName
	}
	return CookieName
}

// New returns the session cookie for token, expiring at expires.
func (p CookiePolicy) New(token string, expires time.Time) (*http.Cookie, error) {
	if token == "" {
		return nil, errors.New("session token is empty")
	}
	maxAge := int(time.Until(expires).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	return &http.Cookie{
		Name: p.Name(), Value: token, Path: "/", Expires: expires.UTC(),
		MaxAge: maxAge, HttpOnly: true, Secure: p.secure, SameSite: http.SameSiteStrictMode,
	}, nil
}

// Expired returns cookies that delete the session cookie and, on an https
// origin, the legacy cookie (see ExpiredLegacy).
func (p CookiePolicy) Expired() []*http.Cookie {
	cookies := []*http.Cookie{p.expired(p.Name())}
	if legacy := p.ExpiredLegacy(); legacy != nil {
		cookies = append(cookies, legacy)
	}
	return cookies
}

// ExpiredLegacy deletes the unprefixed cookie an earlier version set on an
// https origin, so the browser stops sending a token the server now ignores.
// It is nil on an http origin, where that name is still the session cookie.
func (p CookiePolicy) ExpiredLegacy() *http.Cookie {
	if !p.secure {
		return nil
	}
	return p.expired(CookieName)
}

func (p CookiePolicy) expired(name string) *http.Cookie {
	return &http.Cookie{
		Name: name, Value: "", Path: "/", Expires: time.Unix(1, 0).UTC(),
		MaxAge: -1, HttpOnly: true, Secure: p.secure, SameSite: http.SameSiteStrictMode,
	}
}
