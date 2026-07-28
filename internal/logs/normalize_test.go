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
		name    string
		body    string
		date    string
		want    time.Time
		wantErr bool
	}{
		{"application over fluent", `{"timestamp":"2025-01-01T00:00:00Z","log":"x"}`, `"2024-01-01T00:00:00Z"`, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), false},
		{"fluent date", `{"log":"x"}`, `"2024-01-01T00:00:00Z"`, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), false},
		{"absent fallback", `{"log":"x"}`, "", received, false},
		{"invalid supplied", `{"timestamp":"yesterday","log":"x"}`, "", time.Time{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := testRecord(tt.body)
			record.Timestamp = json.RawMessage(tt.date)
			event, err := Normalize(record, TrustedServer{ID: 1}, received)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if !tt.wantErr && event.EventAtUS != tt.want.UnixMicro() {
				t.Fatalf("event time = %d", event.EventAtUS)
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
