package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCheckReadinessRequiresExactHealthyResponse(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		ok     bool
	}{
		{name: "healthy", status: http.StatusOK, body: "ok", ok: true},
		{name: "not ready", status: http.StatusServiceUnavailable, body: "not ready\n"},
		{name: "unexpected body", status: http.StatusOK, body: "healthy\n"},
		{name: "trailing body", status: http.StatusOK, body: "ok\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(test.status)
					_, _ = w.Write([]byte(test.body))
				},
			))
			defer server.Close()
			address := strings.TrimPrefix(server.URL, "http://")
			err := checkReadiness(context.Background(), address)
			if test.ok && err != nil {
				t.Fatal(err)
			}
			if !test.ok && err == nil {
				t.Fatal("unhealthy endpoint passed")
			}
		})
	}
}

func TestCheckReadinessRefusesRedirectAndCancellation(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok\n"))
		},
	))
	defer target.Close()
	redirect := httptest.NewServer(http.RedirectHandler(
		target.URL, http.StatusTemporaryRedirect,
	))
	defer redirect.Close()
	if err := checkReadiness(
		context.Background(), strings.TrimPrefix(redirect.URL, "http://"),
	); err == nil {
		t.Fatal("redirected healthcheck passed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := checkReadiness(ctx, "127.0.0.1:1"); err == nil ||
		!errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("canceled healthcheck = %v", err)
	}

	timeout, stop := context.WithTimeout(context.Background(), time.Nanosecond)
	defer stop()
	<-timeout.Done()
	if err := checkReadiness(timeout, "127.0.0.1:1"); err == nil {
		t.Fatal("expired healthcheck passed")
	}
}

func TestRunHealthcheckRejectsInvalidConfigurationWithoutDetail(t *testing.T) {
	clearSiftailEnv(t)
	t.Setenv("SIFTAIL_UI_ADDR", "invalid")
	if err := runHealthcheck(); err == nil ||
		err.Error() != "configuration is unavailable" {
		t.Fatalf("invalid configuration healthcheck = %v", err)
	}
}
