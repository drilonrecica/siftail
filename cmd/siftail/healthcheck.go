package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/drilonrecica/siftail/internal/config"
)

const healthcheckTimeout = 5 * time.Second

func runHealthcheck() error {
	cfg, err := config.Parse()
	if err != nil {
		return errors.New("configuration is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), healthcheckTimeout)
	defer cancel()
	return checkReadiness(ctx, cfg.UIAddr)
}

func checkReadiness(ctx context.Context, address string) error {
	if ctx == nil {
		return errors.New("healthcheck context is unavailable")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return errors.New("healthcheck address is unavailable")
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::", "[::]":
		host = "::1"
	}
	endpoint := url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(host, port),
		Path:   "/health/ready",
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodGet, endpoint.String(), nil,
	)
	if err != nil {
		return errors.New("healthcheck request is unavailable")
	}
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:             nil,
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("readiness endpoint is unavailable")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 3))
	if err != nil || response.StatusCode != http.StatusOK ||
		string(body) != "ok" {
		return errors.New("readiness endpoint is unhealthy")
	}
	return nil
}
