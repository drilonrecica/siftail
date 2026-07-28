package logs

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseHistoryQueryDefaultsAndPresets(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 34, 56, 123456000, time.FixedZone("test", 2*60*60))
	defaultQuery, err := ParseHistoryQuery(url.Values{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := defaultQuery.ToUS, now.UTC().UnixMicro(); got != want {
		t.Errorf("default to = %d, want %d", got, want)
	}
	if got := time.Duration(defaultQuery.ToUS-defaultQuery.FromUS) * time.Microsecond; got != time.Hour {
		t.Errorf("default range = %s", got)
	}
	if defaultQuery.Direction != DirectionOlder || defaultQuery.Limit != DefaultHistoryLimit {
		t.Errorf("defaults = direction %q limit %d", defaultQuery.Direction, defaultQuery.Limit)
	}

	for preset, want := range map[string]time.Duration{
		"15m": 15 * time.Minute,
		"1h":  time.Hour,
		"6h":  6 * time.Hour,
		"24h": 24 * time.Hour,
		"7d":  7 * 24 * time.Hour,
	} {
		t.Run(preset, func(t *testing.T) {
			query, err := ParseHistoryQuery(url.Values{"preset": {preset}}, now)
			if err != nil {
				t.Fatal(err)
			}
			if got := time.Duration(query.ToUS-query.FromUS) * time.Microsecond; got != want {
				t.Errorf("range = %s, want %s", got, want)
			}
			canonical := query.CanonicalValues(false)
			if canonical.Get("preset") != "" || canonical.Get("from") == "" || canonical.Get("to") == "" {
				t.Fatalf("preset was not resolved in canonical values: %v", canonical)
			}
		})
	}
}

func TestHistoryQueryCanonicalRoundTrip(t *testing.T) {
	values := url.Values{
		"mode":        {"history"},
		"from":        {"2026-07-01T00:00:00Z"},
		"to":          {"2026-07-02T00:00:00Z"},
		"server":      {"7"},
		"project":     {"alpha"},
		"environment": {"production"},
		"application": {"api"},
		"service":     {"worker"},
		"container":   {"9"},
		"levels":      {"error,debug,error"},
		"streams":     {"stderr,stdout,stderr"},
		"contains":    {"timeout_100%"},
		"excludes":    {`health\check`},
		"request_id":  {"request-1"},
		"logger":      {"http"},
		"http_method": {"POST"},
		"http_status": {"503"},
		"error_type":  {"temporary"},
		"direction":   {"newer"},
		"limit":       {"500"},
		"cursor":      {"opaque.cursor"},
	}
	query, err := ParseHistoryQuery(values, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := query.Levels, []Level{LevelDebug, LevelError}; !reflect.DeepEqual(got, want) {
		t.Errorf("levels = %#v, want %#v", got, want)
	}
	if got, want := query.Streams, []Stream{StreamStdout, StreamStderr}; !reflect.DeepEqual(got, want) {
		t.Errorf("streams = %#v, want %#v", got, want)
	}
	encoded := query.CanonicalQuery()
	parsedValues, err := url.ParseQuery(encoded)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := ParseHistoryQuery(parsedValues, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(roundTrip, query) {
		t.Fatalf("round trip:\n got %#v\nwant %#v", roundTrip, query)
	}
	if strings.Contains(encoded, "preset=") || strings.ContainsAny(encoded, "\r\n") {
		t.Errorf("unsafe canonical query: %q", encoded)
	}
	if strings.Contains(encoded, "token") || strings.Contains(encoded, "csrf") ||
		strings.Contains(encoded, "password") {
		t.Errorf("canonical query exposed forbidden state: %q", encoded)
	}
}

func TestParseHistoryQueryRangeBoundaries(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	exact := url.Values{
		"from": {from.Format(time.RFC3339)},
		"to":   {from.Add(MaxHistoryRange).Format(time.RFC3339)},
	}
	if _, err := ParseHistoryQuery(exact, time.Time{}); err != nil {
		t.Fatalf("exact maximum rejected: %v", err)
	}
	exact.Set("to", from.Add(MaxHistoryRange+time.Microsecond).Format(time.RFC3339Nano))
	if _, err := ParseHistoryQuery(exact, time.Time{}); err == nil {
		t.Fatal("range over maximum accepted")
	}
}

func TestParseHistoryQueryRejectsMalformedValues(t *testing.T) {
	validRange := url.Values{
		"from": {"2026-07-01T00:00:00Z"},
		"to":   {"2026-07-02T00:00:00Z"},
	}
	cases := map[string]url.Values{
		"unknown":           {"wat": {"1"}},
		"repeated":          {"limit": {"1", "2"}},
		"mode":              {"mode": {"live"}},
		"mixed range":       {"preset": {"1h"}, "from": {"2026-07-01T00:00:00Z"}},
		"unknown preset":    {"preset": {"2h"}},
		"one endpoint":      {"from": {"2026-07-01T00:00:00Z"}},
		"reversed":          {"from": {"2026-07-02T00:00:00Z"}, "to": {"2026-07-01T00:00:00Z"}},
		"non-UTC":           {"from": {"2026-07-01T00:00:00+01:00"}, "to": {"2026-07-02T00:00:00+01:00"}},
		"server zero":       mergeValues(validRange, "server", "0"),
		"container text":    mergeValues(validRange, "container", "x"),
		"bad level":         mergeValues(validRange, "levels", "notice"),
		"empty level":       mergeValues(validRange, "levels", "info,"),
		"bad stream":        mergeValues(validRange, "streams", "system"),
		"bad direction":     mergeValues(validRange, "direction", "sideways"),
		"limit zero":        mergeValues(validRange, "limit", "0"),
		"limit high":        mergeValues(validRange, "limit", "501"),
		"status low":        mergeValues(validRange, "http_status", "99"),
		"control exact":     mergeValues(validRange, "logger", "safe\nunsafe"),
		"contains too long": mergeValues(validRange, "contains", strings.Repeat("é", 257)),
		"cursor newline":    mergeValues(validRange, "cursor", "opaque\ncursor"),
	}
	for name, values := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseHistoryQuery(values, time.Time{}); err == nil {
				t.Fatalf("accepted values: %v", values)
			}
		})
	}
}

func TestParseHistoryQueryTextByteBoundary(t *testing.T) {
	values := url.Values{
		"from":     {"2026-07-01T00:00:00Z"},
		"to":       {"2026-07-02T00:00:00Z"},
		"contains": {strings.Repeat("é", 256)},
	}
	if _, err := ParseHistoryQuery(values, time.Time{}); err != nil {
		t.Fatalf("512-byte search rejected: %v", err)
	}
}

func mergeValues(base url.Values, key, value string) url.Values {
	merged := make(url.Values, len(base)+1)
	for name, entries := range base {
		merged[name] = append([]string(nil), entries...)
	}
	merged.Set(key, value)
	return merged
}

func FuzzParseHistoryQuery(f *testing.F) {
	for _, seed := range []string{
		"",
		"preset=1h",
		"from=2026-07-01T00%3A00%3A00Z&to=2026-07-02T00%3A00%3A00Z",
		"levels=debug%2Cerror&contains=%25_%5C",
	} {
		f.Add(seed)
	}
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	f.Fuzz(func(t *testing.T, encoded string) {
		if len(encoded) > 4096 {
			t.Skip()
		}
		values, err := url.ParseQuery(encoded)
		if err == nil {
			_, _ = ParseHistoryQuery(values, now)
		}
	})
}
