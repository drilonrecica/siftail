package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// browser is one signed-in administrator session driving the same routes
// the HTMX interface uses.
type browser struct {
	base   string
	client *http.Client
	csrf   string
}

var (
	csrfPattern      = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)
	rowPattern       = regexp.MustCompile(`class="log-row `)
	detailPattern    = regexp.MustCompile(`class="event-toggle"[^>]*?hx-get="([^"]+)"`)
	loadOlderPattern = regexp.MustCompile(`id="load-older"[^>]*?hx-get="([^"]+)"`)
	eventIDPattern   = regexp.MustCompile(`data-event-id="(\d+)"`)
)

func newBrowser(base, username, password string) (*browser, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	b := &browser{base: base, client: &http.Client{
		Jar:     jar,
		Timeout: 5 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
	form := url.Values{"username": {username}, "password": {password}, "return": {"/logs"}}
	status, _, _, err := b.do(context.Background(), http.MethodPost, "/session", form)
	if err != nil {
		return nil, err
	}
	if status != http.StatusSeeOther {
		return nil, fmt.Errorf("sign-in returned HTTP %d", status)
	}
	status, body, _, err := b.do(context.Background(), http.MethodGet, "/settings", nil)
	if err != nil || status != http.StatusOK {
		return nil, fmt.Errorf("settings page: HTTP %d: %v", status, err)
	}
	match := csrfPattern.FindStringSubmatch(body)
	if match == nil {
		return nil, errors.New("no CSRF token on the settings page")
	}
	b.csrf = html.UnescapeString(match[1])
	return b, nil
}

func (b *browser) do(ctx context.Context, method, path string, form url.Values) (int, string, time.Duration, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	request, err := http.NewRequestWithContext(ctx, method, b.base+path, body)
	if err != nil {
		return 0, "", 0, err
	}
	request.Header.Set("Origin", b.base)
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if strings.HasPrefix(path, "/logs/rows") || strings.HasPrefix(path, "/logs/events/") {
		request.Header.Set("HX-Request", "true")
	}
	started := time.Now()
	response, err := b.client.Do(request)
	if err != nil {
		return 0, "", time.Since(started), err
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	return response.StatusCode, string(content), time.Since(started), err
}

// page is one History rows response.
type page struct {
	status    int
	rows      int
	elapsed   time.Duration
	nextURL   string
	detailURL string
	ids       []string
}

func (b *browser) history(ctx context.Context, path string) (page, error) {
	status, body, elapsed, err := b.do(ctx, http.MethodGet, path, nil)
	result := page{status: status, elapsed: elapsed, rows: len(rowPattern.FindAllStringIndex(body, -1))}
	if err != nil {
		return result, err
	}
	if status != http.StatusOK {
		return result, fmt.Errorf("GET %s: HTTP %d", path, status)
	}
	if match := loadOlderPattern.FindStringSubmatch(body); match != nil {
		result.nextURL = html.UnescapeString(match[1])
	}
	if match := detailPattern.FindStringSubmatch(body); match != nil {
		result.detailURL = html.UnescapeString(match[1])
	}
	for _, match := range eventIDPattern.FindAllStringSubmatch(body, -1) {
		result.ids = append(result.ids, match[1])
	}
	return result, nil
}

// export posts the History export form and reads the whole file.
func (b *browser) export(ctx context.Context, query url.Values) (int, int64, int, time.Duration, error) {
	form := url.Values{
		"csrf_token":   {b.csrf},
		"format":       {"ndjson"},
		"confirmation": {"export matching history"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		b.base+"/logs/export?"+query.Encode(), strings.NewReader(form.Encode()))
	if err != nil {
		return 0, 0, 0, 0, err
	}
	request.Header.Set("Origin", b.base)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	started := time.Now()
	response, err := b.client.Do(request)
	if err != nil {
		return 0, 0, 0, time.Since(started), err
	}
	defer response.Body.Close()
	var bytes int64
	lines := 0
	reader := bufio.NewReaderSize(response.Body, 1<<16)
	for {
		line, err := reader.ReadSlice('\n')
		bytes += int64(len(line))
		if len(line) > 0 && line[len(line)-1] == '\n' {
			lines++
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return response.StatusCode, bytes, lines, time.Since(started), err
		}
	}
	return response.StatusCode, bytes, lines, time.Since(started), nil
}

// live holds one Live stream open and counts the frames it receives until
// ctx ends.
func (b *browser) live(ctx context.Context) (frames int64, err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, b.base+"/logs/live/stream", nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Origin", b.base)
	client := *b.client
	client.Timeout = 0
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("live stream: HTTP %d", response.StatusCode)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 1<<20), 1<<24)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data:") {
			frames++
		}
	}
	if ctx.Err() != nil {
		return frames, nil
	}
	return frames, scanner.Err()
}

// saveRetention writes the retention settings through the Settings form.
func (b *browser) saveRetention(ctx context.Context, days, gib int) error {
	status, _, _, err := b.do(ctx, http.MethodPost, "/settings/retention", url.Values{
		"csrf_token":           {b.csrf},
		"retention_days":       {fmt.Sprint(days)},
		"maximum_database_gib": {fmt.Sprint(gib)},
	})
	if err != nil {
		return err
	}
	if status != http.StatusSeeOther && status != http.StatusOK {
		return fmt.Errorf("retention settings: HTTP %d", status)
	}
	return nil
}

var statusFieldPattern = regexp.MustCompile(`<dt>([^<]+)</dt><dd>([^<]*)</dd>`)

// status returns the Status page's term/definition pairs.
func (b *browser) status(ctx context.Context) (map[string]string, error) {
	status, body, _, err := b.do(ctx, http.MethodGet, "/status", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("status page: HTTP %d", status)
	}
	fields := map[string]string{}
	for _, match := range statusFieldPattern.FindAllStringSubmatch(body, -1) {
		fields[html.UnescapeString(match[1])] = html.UnescapeString(strings.TrimSpace(match[2]))
	}
	return fields, nil
}
