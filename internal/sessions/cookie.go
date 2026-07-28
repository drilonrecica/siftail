package sessions

import (
	"errors"
	"net/http"
	"net/url"
	"time"
)

const CookieName = "siftail_session"

func NewCookie(token string, expires time.Time, publicURL string) (*http.Cookie, error) {
	if token == "" {
		return nil, errors.New("session token is empty")
	}
	secure := false
	if publicURL != "" {
		parsed, err := url.Parse(publicURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return nil, errors.New("public URL is invalid")
		}
		secure = parsed.Scheme == "https"
	}
	maxAge := int(time.Until(expires).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	return &http.Cookie{
		Name: CookieName, Value: token, Path: "/", Expires: expires.UTC(),
		MaxAge: maxAge, HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode,
	}, nil
}
