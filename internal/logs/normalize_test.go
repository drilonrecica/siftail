package logs

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNormalizeCanonicalEvent(t *testing.T) {
	received := time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC)
	record := testRecord(`{
		"date":"2026-07-27T20:14:55.123456Z",
		"log":"ERROR: checkout failed\nstack",
		"stream":"stderr",
		"coolify_project_name":"Billing",
		"environment":"production",
		"application":"checkout",
		"service":"api",
		"container_id":"abc",
		"request_id":"req-1",
		"customer_id":842,
		"source_event_id":"evt-1"
	}`)
	event, err := Normalize(record, TrustedServer{ID: 7}, received)
	if err != nil {
		t.Fatal(err)
	}
	wantTime, _ := time.Parse(time.RFC3339Nano, "2026-07-27T20:14:55.123456Z")
	if event.EventAtUS != wantTime.UnixMicro() || event.ReceivedAtUS != received.UnixMicro() {
		t.Fatalf("times = %d/%d", event.EventAtUS, event.ReceivedAtUS)
	}
	if event.Source.ServerID != 7 || event.Source.Project != "Billing" ||
		event.Source.Application != "checkout" || event.Source.Service != "api" {
		t.Fatalf("source = %#v", event.Source)
	}
	if event.Container == nil || event.Container.ID != "abc" {
		t.Fatalf("container = %#v", event.Container)
	}
	if event.Stream != StreamStderr || event.Level != LevelError || event.OriginalLevel != "" {
		t.Fatalf("stream/level = %s/%s/%q", event.Stream, event.Level, event.OriginalLevel)
	}
	if string(event.MessageRaw) != "ERROR: checkout failed\nstack" ||
		event.MessageText != string(event.MessageRaw) {
		t.Fatalf("message = %q / %q", event.MessageRaw, event.MessageText)
	}
	if event.Common.RequestID != "req-1" || event.SourceEventID != "evt-1" {
		t.Fatalf("common/id = %#v / %q", event.Common, event.SourceEventID)
	}
	if string(event.Attributes) != `{"customer_id":842}` {
		t.Fatalf("attributes = %s", event.Attributes)
	}
}

func TestNormalizeCoolifyFluentdAliases(t *testing.T) {
	event, err := Normalize(testRecord(`{
		"date":"2026-07-28T10:00:00.123456Z",
		"log":"request complete",
		"source":"stderr",
		"coolify.project_name":"storefront",
		"coolify.environment_name":"production",
		"coolify.app_name":"web",
		"container_id":"abc123",
		"container_name":"checkout-42",
		"coolify.server_ip":"192.0.2.10"
	}`), TrustedServer{ID: 17}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if event.Source.ServerID != 17 ||
		event.Source.Project != "storefront" ||
		event.Source.Environment != "production" ||
		event.Source.Application != "web" ||
		event.Source.Service != "web" {
		t.Fatalf("source = %#v", event.Source)
	}
	if event.Stream != StreamStderr {
		t.Fatalf("stream = %q", event.Stream)
	}
	if event.Container == nil ||
		event.Container.ID != "abc123" ||
		event.Container.Name != "checkout-42" {
		t.Fatalf("container = %#v", event.Container)
	}
	if strings.Contains(string(event.Attributes), `"source"`) ||
		!strings.Contains(string(event.Attributes), `"coolify.server_ip"`) {
		t.Fatalf("attributes = %s", event.Attributes)
	}
}

func TestTimestampPrecedenceAndFailure(t *testing.T) {
	received := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		body      string
		date      string
		want      time.Time
		defaulted bool
		wantErr   bool
	}{
		{"application over fluent", `{"timestamp":"2025-01-01T00:00:00Z","log":"x"}`, `"2024-01-01T00:00:00Z"`, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), false, false},
		{"fluent date", `{"log":"x"}`, `"2024-01-01T00:00:00Z"`, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), false, false},
		{"absent fallback", `{"log":"x"}`, "", received, true, false},
		{"null fallback", `{"timestamp":null,"log":"x"}`, "", received, true, false},
		// A null source timestamp is absent, so precedence step 2 still
		// applies before the receive time.
		{"null source uses fluent date", `{"time":null,"log":"x"}`, `"2024-01-01T00:00:00Z"`, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), false, false},
		// A supplied time equal to the receive time is producer content.
		{"supplied equals receive", `{"timestamp":"2026-07-28T00:00:00Z","log":"x"}`, "", received, false, false},
		{"invalid supplied", `{"timestamp":"yesterday","log":"x"}`, "", time.Time{}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := testRecord(tt.body)
			record.Timestamp = json.RawMessage(tt.date)
			event, err := Normalize(record, TrustedServer{ID: 1}, received)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if !tt.wantErr && (event.EventAtUS != tt.want.UnixMicro() ||
				event.EventAtDefaulted != tt.defaulted) {
				t.Fatalf("event time = %d defaulted=%v", event.EventAtUS, event.EventAtDefaulted)
			}
		})
	}
}

func TestStructuredLevelPrecedenceAndRawPreservation(t *testing.T) {
	payload := `{"level":"WARNING","message":"INFO later","request_id":"r","extra":{"b":2,"a":1}}`
	record := testRecord(`{"log":` + quote(payload) + `,"stream":"stdout"}`)
	event, err := Normalize(record, TrustedServer{ID: 1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if event.Level != LevelWarn || event.OriginalLevel != "WARNING" {
		t.Fatalf("level = %s / %q", event.Level, event.OriginalLevel)
	}
	if string(event.MessageRaw) != payload || event.MessageText != "INFO later" {
		t.Fatalf("raw/text = %q / %q", event.MessageRaw, event.MessageText)
	}
	if event.Common.RequestID != "r" || !strings.Contains(string(event.Attributes), `"extra"`) {
		t.Fatalf("common/attributes = %#v / %s", event.Common, event.Attributes)
	}
}

func TestLevelInferenceAnchoredAndIndependentOfStream(t *testing.T) {
	tests := []struct {
		message string
		stream  string
		want    Level
	}{
		{" [CRIT]: failed", "stdout", LevelFatal},
		{"WARN warning", "stderr", LevelWarn},
		{"operation has error later", "stderr", LevelUnknown},
		{"ERRORISH not a level", "stdout", LevelUnknown},
		{"plain", "stderr", LevelUnknown},
	}
	for _, tt := range tests {
		body := `{"log":` + quote(tt.message) + `,"stream":` + quote(tt.stream) + `}`
		event, err := Normalize(testRecord(body), TrustedServer{ID: 1}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if event.Level != tt.want {
			t.Errorf("%q => %s, want %s", tt.message, event.Level, tt.want)
		}
	}
}

func TestSourceIdentityExactFallbackAndContainerExclusion(t *testing.T) {
	lower, err := Normalize(testRecord(`{"log":"x","project":"App","container_name":"api-1"}`), TrustedServer{ID: 1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	upper, err := Normalize(testRecord(`{"log":"x","project":"app","container_name":"api-2"}`), TrustedServer{ID: 1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if lower.Source.Project == upper.Source.Project {
		t.Fatal("case distinction lost")
	}
	if lower.Source.Service != "api" || lower.Source.Application != "api-1" {
		t.Fatalf("fallback source = %#v", lower.Source)
	}
	if lower.Source != (SourceIdentity{
		ServerID: 1, Project: "App", Environment: "default-environment",
		Application: "api-1", Service: "api", ProjectLabel: "App",
		EnvLabel: "default-environment", AppLabel: "api-1", ServiceLabel: "api",
	}) {
		t.Fatalf("unexpected source = %#v", lower.Source)
	}
}

func TestNormalizationRejectsControlsLimitsAndDuplicateKeys(t *testing.T) {
	tests := []string{
		`{"log":"x","project":"bad\u0001key"}`,
		`{"log":"x","source_event_id":"` + strings.Repeat("x", 256) + `"}`,
		`{"log":"x","nested":{"same":1,"same":2}}`,
	}
	for _, body := range tests {
		if _, err := Normalize(testRecord(body), TrustedServer{ID: 1}, time.Now()); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}

func TestGenericIDIsNotSourceEventIdentity(t *testing.T) {
	event, err := Normalize(testRecord(`{"log":"x","id":"generic"}`), TrustedServer{ID: 1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if event.SourceEventID != "" || !strings.Contains(string(event.Attributes), `"id":"generic"`) {
		t.Fatalf("identity/attributes = %q / %s", event.SourceEventID, event.Attributes)
	}
}

func TestCanonicalEqualityExcludesReceiveTime(t *testing.T) {
	first, err := Normalize(testRecord(`{"timestamp":"2026-01-01T00:00:00Z","log":"x","source_event_id":"e"}`), TrustedServer{ID: 1}, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.ReceivedAtUS++
	if !CanonicalEqual(first, second) {
		t.Fatal("receive time affected canonical equality")
	}
	second.MessageText = "changed"
	if CanonicalEqual(first, second) {
		t.Fatal("content change did not affect canonical equality")
	}
}

func TestCanonicalEqualityIgnoresOnlyDefaultedEventTime(t *testing.T) {
	// A retry of a timestamp-less record arrives with a new receive time.
	first, err := Normalize(testRecord(`{"log":"x","source_event_id":"e"}`), TrustedServer{ID: 1}, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	retry, err := Normalize(testRecord(`{"log":"x","source_event_id":"e"}`), TrustedServer{ID: 1}, time.Unix(2, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !first.EventAtDefaulted || first.EventAtUS == retry.EventAtUS {
		t.Fatalf("defaulted times = %d/%d (%v)", first.EventAtUS, retry.EventAtUS, first.EventAtDefaulted)
	}
	if !CanonicalEqual(first, retry) {
		t.Fatal("defaulted event time made a retry a conflict")
	}
	// One side supplied, the other defaulted: the defaulted time carries no
	// producer content, so time is not compared.
	supplied, err := Normalize(testRecord(`{"timestamp":"2026-01-01T00:00:00Z","log":"x","source_event_id":"e"}`), TrustedServer{ID: 1}, time.Unix(3, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !CanonicalEqual(first, supplied) || !CanonicalEqual(supplied, first) {
		t.Fatal("event time compared although one side defaulted it")
	}
	// Both supplied: a different time is a conflict.
	later := supplied
	later.EventAtUS++
	if CanonicalEqual(supplied, later) {
		t.Fatal("different supplied event times compared equal")
	}
	// A defaulted retry still detects content changes.
	retry.MessageText = "changed"
	if CanonicalEqual(first, retry) {
		t.Fatal("content change hidden by defaulted event time")
	}
}

func TestCanonicalEqualityContainerIdentity(t *testing.T) {
	base, err := Normalize(testRecord(`{"timestamp":"2026-01-01T00:00:00Z","log":"x","source_event_id":"e"}`), TrustedServer{ID: 1}, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	with := func(container *ContainerIdentity) CanonicalEvent {
		event := base
		event.Container = container
		return event
	}
	tests := []struct {
		name  string
		a, b  *ContainerIdentity
		equal bool
	}{
		{"both absent", nil, nil, true},
		{"one absent", &ContainerIdentity{ID: "c1", Name: "web"}, nil, false},
		{"same ID renamed", &ContainerIdentity{ID: "c1", Name: "web"}, &ContainerIdentity{ID: "c1", Name: "web-renamed"}, true},
		{"different ID same name", &ContainerIdentity{ID: "c1", Name: "web"}, &ContainerIdentity{ID: "c2", Name: "web"}, false},
		{"names only equal", &ContainerIdentity{Name: "web"}, &ContainerIdentity{Name: "web"}, true},
		{"names only differ", &ContainerIdentity{Name: "web"}, &ContainerIdentity{Name: "worker"}, false},
		{"ID on one side only", &ContainerIdentity{ID: "c1", Name: "web"}, &ContainerIdentity{Name: "web"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanonicalEqual(with(tt.a), with(tt.b)); got != tt.equal {
				t.Fatalf("equal = %v, want %v", got, tt.equal)
			}
			if got := CanonicalEqual(with(tt.b), with(tt.a)); got != tt.equal {
				t.Fatalf("reversed equal = %v, want %v", got, tt.equal)
			}
		})
	}
}

func TestValidateJSONDepthAndDuplicates(t *testing.T) {
	if err := ValidateJSON([]byte(`{"a":{"b":1,"b":2}}`), 32); err == nil {
		t.Fatal("duplicate nested key accepted")
	}
	deep := strings.Repeat("[", 34) + "0" + strings.Repeat("]", 34)
	if err := ValidateJSON([]byte(deep), 32); err == nil {
		t.Fatal("excess depth accepted")
	}
}

func FuzzNormalize(f *testing.F) {
	f.Add(`{"log":"hello"}`)
	f.Add(`{"timestamp":"bad","log":"x"}`)
	f.Fuzz(func(t *testing.T, body string) {
		if !json.Valid([]byte(body)) {
			return
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(body), &fields) != nil || fields == nil {
			return
		}
		_, _ = Normalize(ReceivedRecord{Fields: fields, Raw: []byte(body)}, TrustedServer{ID: 1}, time.Unix(1, 0))
	})
}

func testRecord(body string) ReceivedRecord {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		panic(err)
	}
	record := ReceivedRecord{Fields: fields, Raw: []byte(body)}
	if date, ok := fields["date"]; ok {
		record.Timestamp = date
	}
	return record
}

func quote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// Common-field normalization consumes a member only when its value has the
// field's type. A member it did not consume is still application data and
// stays in attributes (DOMAIN.md §6 invariant 8, §11); a consumed member is
// removed from attributes, because its canonical column is the documented
// representation.
func TestUnconsumedCommonFieldsStayInAttributes(t *testing.T) {
	tests := []struct {
		name           string
		record         string
		wantRequestID  string
		wantStatus     *int64
		wantOriginal   string
		wantAttributes string
	}{
		{
			name: "ill-typed common and level members of a structured payload are kept",
			record: `{"log":` + quote(`{"message":"job finished","status":"completed","request_id":12345,"duration_ms":-1,"level":30,"keep":"yes"}`) +
				`,"stream":"stdout"}`,
			wantAttributes: `{"duration_ms":-1,"keep":"yes","level":30,"request_id":12345,"status":"completed"}`,
		},
		{
			name:           "ill-typed common member of the record itself is kept",
			record:         `{"log":"plain text","status":"ok","request_id":["a"]}`,
			wantAttributes: `{"request_id":["a"],"status":"ok"}`,
		},
		{
			name:           "the alias that was not consumed is kept",
			record:         `{"log":` + quote(`{"message":"m","request_id":"r1","trace_id":"t1"}`) + `}`,
			wantRequestID:  "r1",
			wantAttributes: `{"trace_id":"t1"}`,
		},
		{
			name:          "consumed members are removed from attributes",
			record:        `{"log":` + quote(`{"message":"m","level":"warn","request_id":"r","status":200,"duration_ms":1.5,"logger":"app"}`) + `}`,
			wantRequestID: "r",
			wantStatus:    int64Pointer(200),
			wantOriginal:  "warn",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, err := Normalize(testRecord(tt.record), TrustedServer{ID: 1}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if event.Common.RequestID != tt.wantRequestID {
				t.Fatalf("request_id = %q, want %q", event.Common.RequestID, tt.wantRequestID)
			}
			if (event.Common.HTTPStatus == nil) != (tt.wantStatus == nil) ||
				(tt.wantStatus != nil && *event.Common.HTTPStatus != *tt.wantStatus) {
				t.Fatalf("http_status = %v, want %v", event.Common.HTTPStatus, tt.wantStatus)
			}
			if event.OriginalLevel != tt.wantOriginal {
				t.Fatalf("original level = %q, want %q", event.OriginalLevel, tt.wantOriginal)
			}
			if string(event.Attributes) != tt.wantAttributes {
				t.Fatalf("attributes = %s, want %s", event.Attributes, tt.wantAttributes)
			}
		})
	}
}

func int64Pointer(value int64) *int64 { return &value }

// A payload member whose value is null is absent for payload selection
// (DOMAIN.md §5.10): the next payload field is used, and a record whose only
// payload member is null is itself the payload. The null member is still
// application data and stays in attributes.
func TestNullPayloadMemberIsAbsent(t *testing.T) {
	event, err := Normalize(testRecord(`{"log":null,"message":"real text","stream":"stdout"}`), TrustedServer{ID: 1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if string(event.MessageRaw) != "real text" || event.MessageText != "real text" ||
		string(event.Attributes) != `{"log":null}` {
		t.Fatalf("raw/text/attributes = %q / %q / %s", event.MessageRaw, event.MessageText, event.Attributes)
	}
	event, err = Normalize(testRecord(`{"log":null,"stream":"stdout"}`), TrustedServer{ID: 1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if string(event.MessageRaw) != `{"log":null,"stream":"stdout"}` ||
		event.MessageText != `{"log":null,"stream":"stdout"}` || string(event.Attributes) != `{"log":null}` {
		t.Fatalf("raw/text/attributes = %q / %q / %s", event.MessageRaw, event.MessageText, event.Attributes)
	}
}
