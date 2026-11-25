package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The fixture follows ARCHITECTURE.md §33.1: uneven sources, plain text,
// JSON attributes, stack traces, repeated messages, varied sizes, several
// levels, and a time range that spans old and recent events. Every value is
// a pure function of the event index, so a tier is reproducible and the
// harness can name events (a request ID, an equal-time block) without
// reading the database.

// Event density is constant: fixtureSpan covers fixtureEvents, so the
// 100,000-event tier spans about three hours and the 10,000,000-event tier
// thirteen days, inside the default 14-day retention age.
const (
	fixtureEvents = 10_000_000
	fixtureSpan   = 13 * 24 * time.Hour
	// Every fixtureBurstEvery events, fixtureBurstSize consecutive events
	// share one timestamp, more than one 200-row History page.
	fixtureBurstEvery = 50_000
	fixtureBurstSize  = 300
	// fixtureBlock events in a row go to one Server, as one Fluent Bit
	// flush from one host would.
	fixtureBlock = 100
	// fixtureNeedle occurs in one event in 1,000 for substring search.
	fixtureNeedle = "checkout_failed"
)

type fixtureSource struct {
	project, environment, application, service string
	weight                                     int
}

type fixtureServer struct {
	name    string
	weight  int
	sources []fixtureSource
}

// fixtureServers carry 60/30/10 percent of blocks; inside each, sources are
// weighted unevenly, so the smallest source receives 1.5% of all events and
// the largest 24%.
var fixtureServers = []fixtureServer{
	{name: "edge-1", weight: 60, sources: []fixtureSource{
		{"shop", "production", "storefront", "web", 40},
		{"shop", "production", "storefront", "worker", 15},
		{"shop", "production", "api", "web", 25},
		{"shop", "production", "checkout", "web", 12},
		{"shop", "staging", "storefront", "web", 8},
	}},
	{name: "edge-2", weight: 30, sources: []fixtureSource{
		{"crm", "production", "backend", "web", 50},
		{"crm", "production", "backend", "cron", 10},
		{"crm", "production", "frontend", "web", 30},
		{"crm", "staging", "backend", "web", 10},
	}},
	{name: "edge-3", weight: 10, sources: []fixtureSource{
		{"infra", "production", "proxy", "traefik", 60},
		{"infra", "production", "db", "postgres", 25},
		{"infra", "production", "queue", "redis", 15},
	}},
}

type fixture struct {
	startUS int64
	stepUS  int64
}

func newFixture(newest time.Time) fixture {
	step := fixtureSpan.Microseconds() / fixtureEvents
	return fixture{
		startUS: newest.Add(-fixtureSpan).Truncate(time.Second).UnixMicro(),
		stepUS:  step,
	}
}

// splitmix64 is a small deterministic mixer; the fixture needs repeatable
// variety, not cryptographic randomness.
func splitmix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

func weighted[T any](items []T, weight func(T) int, roll uint64) int {
	total := 0
	for _, item := range items {
		total += weight(item)
	}
	pick := int(roll % uint64(total))
	for index, item := range items {
		pick -= weight(item)
		if pick < 0 {
			return index
		}
	}
	return len(items) - 1
}

// serverIndex returns the zero-based Server of the block holding event i.
func (f fixture) serverIndex(i int64) int {
	return weighted(fixtureServers, func(s fixtureServer) int { return s.weight },
		splitmix64(uint64(i/fixtureBlock)^0x5eed))
}

func (f fixture) source(i int64, server int) fixtureSource {
	sources := fixtureServers[server].sources
	return sources[weighted(sources, func(s fixtureSource) int { return s.weight },
		splitmix64(uint64(i)^0xa11ce))]
}

// timestampUS is non-decreasing in i; a burst shares the timestamp of its
// first event.
func (f fixture) timestampUS(i int64) int64 {
	offset := i % fixtureBurstEvery
	if offset >= fixtureBurstSize {
		offset = 0
	}
	return f.startUS + (i-offset)*f.stepUS
}

// burstStart returns the first event index of the newest complete burst
// below limit.
func burstStart(limit int64) int64 {
	start := (limit - fixtureBurstSize) / fixtureBurstEvery * fixtureBurstEvery
	return start
}

type eventKind int

const (
	kindPlain eventKind = iota
	kindJSON
	kindRepeated
	kindStack
)

func (f fixture) kind(i int64) eventKind {
	switch roll := splitmix64(uint64(i)) % 1000; {
	case roll < 20:
		return kindStack
	case roll < 100:
		return kindRepeated
	case roll < 350:
		return kindJSON
	default:
		return kindPlain
	}
}

func requestID(i int64) string { return fmt.Sprintf("req-%012x", splitmix64(uint64(i))>>16) }

// jsonRequestEvent returns the nearest index at or below i whose event
// carries a request ID.
func (f fixture) jsonRequestEvent(i int64) int64 {
	for f.kind(i) != kindJSON {
		i--
	}
	return i
}

var filler = strings.Repeat("lorem ipsum dolor sit amet consectetur adipiscing elit ", 40)

var routes = []string{"/api/v1/items", "/api/v1/orders", "/checkout", "/login", "/api/v1/search", "/static/app.js"}

// appendRecord writes event i as one NDJSON line of the Coolify/Fluent Bit
// shape: transport metadata at the top level and the application line in
// "log".
func (f fixture) appendRecord(buffer *bytes.Buffer, i int64, server int) {
	record := f.record(i, server)
	record["timestamp"] = time.UnixMicro(f.timestampUS(i)).UTC().Format(time.RFC3339Nano)
	encoded, _ := json.Marshal(record)
	buffer.Write(encoded)
	buffer.WriteByte('\n')
}

// record returns event i without its timestamp, which the bench derives
// from the index and the soak from the clock.
func (f fixture) record(i int64, server int) map[string]any {
	roll := splitmix64(uint64(i) ^ 0xfeed)
	source := f.source(i, server)
	stream := "stdout"
	if roll%4 == 0 {
		stream = "stderr"
	}
	needle := ""
	if (roll>>8)%1000 == 0 {
		needle = " " + fixtureNeedle
	}
	route := routes[(roll>>16)%uint64(len(routes))]
	var message string
	switch f.kind(i) {
	case kindStack:
		frames := 8 + int((roll>>24)%32)
		var trace strings.Builder
		fmt.Fprintf(&trace, "ERROR unhandled exception in %s%s: index out of range [%d]", route, needle, roll%97)
		for frame := range frames {
			fmt.Fprintf(&trace, "\n    at app.handler%d (/srv/app/internal/handler%d.go:%d)", frame, frame%9, 40+frame*7)
		}
		message = trace.String()
	case kindRepeated:
		message = "GET /healthz 200 0.4ms"
	case kindJSON:
		levels := []string{"info", "info", "info", "info", "info", "debug", "warn", "error"}
		structured := map[string]any{
			"level":       levels[(roll>>32)%uint64(len(levels))],
			"msg":         "request handled" + needle,
			"request_id":  requestID(i),
			"route":       route,
			"user_id":     (roll >> 12) % 50_000,
			"duration_ms": float64((roll>>40)%5000) / 10,
		}
		if structured["level"] == "error" {
			structured["error_type"] = "UpstreamTimeout"
		}
		encoded, _ := json.Marshal(structured)
		message = string(encoded)
	default:
		level := "INFO"
		switch (roll >> 32) % 20 {
		case 0, 1:
			level = "WARN"
		case 2:
			level = "ERROR"
		case 3, 4, 5:
			level = "DEBUG"
		}
		// Sizes skew small: most lines are 60-200 bytes, a few reach
		// about 1,000.
		size := 60 + int((roll>>44)%140)
		if (roll>>52)%10 == 0 {
			size += int((roll >> 20) % 800)
		}
		head := fmt.Sprintf("%s %s %s status=%d bytes=%d%s ", level, "GET", route, 200+int((roll>>4)%4)*100, roll%65536, needle)
		if len(head) < size {
			head += filler[:size-len(head)]
		}
		message = head
	}
	return map[string]any{
		"project":        source.project,
		"environment":    source.environment,
		"application":    source.application,
		"service":        source.service,
		"container_name": source.application + "-" + source.service + "-1",
		"stream":         stream,
		"log":            message,
	}
}
