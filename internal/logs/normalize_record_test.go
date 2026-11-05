package logs

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseRecordSplitsMembersLikeUnmarshal(t *testing.T) {
	bodies := []string{
		`{"log":"plain","n":1}`,
		"{ \"a\" : \"spaced\" ,\n\t\"b\":\t[1, {\"c\": null}] , \"d\" : -1.5e3 }",
		`{"escaped":"line\nquote\" back\\ \u00e9","unicode":"żółw ✓","t":true,"f":false,"z":null}`,
		`{"nested":{"deep":{"deeper":[{"x":"y"}]}},"empty":{},"list":[]}`,
		`{"date":"2026-07-27T20:14:55.123456Z","tag":"coolify.application"}`,
		`{}`,
	}
	for _, body := range bodies {
		record, err := ParseRecord([]byte(body), maxJSONDepth)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		var want map[string]json.RawMessage
		if err := json.Unmarshal([]byte(body), &want); err != nil {
			t.Fatal(err)
		}
		if len(record.Fields) != len(want) {
			t.Fatalf("%s: fields = %q, want %q", body, record.Fields, want)
		}
		for key, value := range want {
			if !bytes.Equal(record.Fields[key], value) {
				t.Fatalf("%s: %s = %q, want %q", body, key, record.Fields[key], value)
			}
		}
		if !bytes.Equal(record.Timestamp, want["date"]) || !bytes.Equal(record.Raw, []byte(body)) {
			t.Fatalf("%s: timestamp/raw = %q/%q", body, record.Timestamp, record.Raw)
		}
	}
}

func TestParseRecordRejectsWhatValidateJSONRejects(t *testing.T) {
	// The object is depth 0 and its member depth 1, so maxJSONDepth+1 nested
	// arrays put the innermost one just past the limit.
	deep := `{"a":` + strings.Repeat(`[`, maxJSONDepth+1) + strings.Repeat(`]`, maxJSONDepth+1) + `}`
	if err := ValidateJSON([]byte(deep), maxJSONDepth); err == nil {
		t.Fatal("fixture is within ValidateJSON's depth limit")
	}
	for name, body := range map[string]string{
		"duplicate key":        `{"a":1,"a":2}`,
		"nested duplicate key": `{"a":{"b":1,"b":2}}`,
		"too deep":             deep,
		"trailing data":        `{"a":1} {"b":2}`,
		"trailing garbage":     `{"a":1}x`,
		"array":                `[{"a":1}]`,
		"null":                 `null`,
		"string":               `"text"`,
		"truncated":            `{"a":"open`,
		"invalid UTF-8":        "{\"a\":\"\xff\"}",
		"bad key":              `{1:2}`,
	} {
		if _, err := ParseRecord([]byte(body), maxJSONDepth); err == nil {
			t.Errorf("%s: ParseRecord accepted %q", name, body)
		}
	}
	// The depth bound is the smaller of the caller's and the canonical one.
	shallow := `{"a":{"b":{"c":1}}}`
	if _, err := ParseRecord([]byte(shallow), 2); err == nil {
		t.Fatal("caller depth limit ignored")
	}
	if _, err := ParseRecord([]byte(deep), maxJSONDepth+10); err == nil {
		t.Fatal("canonical depth limit ignored")
	}
}

// A record from ParseRecord skips the second validation and the defensive
// copy of its fields; the canonical event must not change because of it.
func TestParsedRecordNormalizesLikeUnparsedRecord(t *testing.T) {
	received := time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC)
	bodies := []string{
		`{"date":"2026-07-27T20:14:55.123456Z","log":"ERROR: failed\nstack","stream":"stderr","container_id":"c1","request_id":"r1","customer_id":842}`,
		`{"log":"{\"level\":\"warn\",\"msg\":\"inner\",\"duration_ms\":12.5,\"k\":[1,2]}","service":"api"}`,
		`{"timestamp":1785196800.25,"message":"top","msg":"aside","level":"INFO","extra":{"a":1}}`,
		`{"level":"error","application":"app","http_status":500,"customer":"x"}`,
		`{"log":{"nested":"object","message":"from object"},"tag":"t"}`,
		`{"log":12345}`,
	}
	for _, body := range bodies {
		parsed, err := ParseRecord([]byte(body), maxJSONDepth)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Normalize(parsed, TrustedServer{ID: 3}, received)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		want, err := Normalize(testRecord(body), TrustedServer{ID: 3}, received)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s:\nparsed   %#v\nunparsed %#v", body, got, want)
		}
	}
}

func TestTopLevelMessageIsRawTextAndTransportKeysStayOut(t *testing.T) {
	received := time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC)
	envelope := `"date":"2026-07-27T20:14:55Z","tag":"coolify.application",` +
		`"container_id":"c1","container_name":"web-1","source":"stdout",` +
		`"coolify.app_name":"api","coolify.project_name":"shop",` +
		`"coolify.environment_name":"production","coolify.server_ip":"10.0.0.7"`
	tests := []struct {
		name       string
		body       string
		raw        string
		text       string
		attributes string
	}{
		{
			name:       "message",
			body:       `{` + envelope + `,"message":"checkout failed","msg":"aside","customer":1}`,
			raw:        "checkout failed",
			text:       "checkout failed",
			attributes: `{"coolify.server_ip":"10.0.0.7","customer":1,"msg":"aside"}`,
		},
		{
			name:       "msg",
			body:       `{` + envelope + `,"msg":"short form","customer":1}`,
			raw:        "short form",
			text:       "short form",
			attributes: `{"coolify.server_ip":"10.0.0.7","customer":1}`,
		},
		{
			name:       "message holding a JSON object",
			body:       `{` + envelope + `,"message":"{\"msg\":\"inner\",\"k\":2}"}`,
			raw:        `{"msg":"inner","k":2}`,
			text:       "inner",
			attributes: `{"coolify.server_ip":"10.0.0.7","k":2}`,
		},
		{
			name:       "log keeps a top-level message as an attribute",
			body:       `{` + envelope + `,"log":"line","message":"aside"}`,
			raw:        "line",
			text:       "line",
			attributes: `{"coolify.server_ip":"10.0.0.7","message":"aside"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record, err := ParseRecord([]byte(tt.body), maxJSONDepth)
			if err != nil {
				t.Fatal(err)
			}
			event, err := Normalize(record, TrustedServer{ID: 1}, received)
			if err != nil {
				t.Fatal(err)
			}
			if string(event.MessageRaw) != tt.raw || event.MessageText != tt.text ||
				string(event.Attributes) != tt.attributes {
				t.Fatalf("raw %q text %q attributes %s", event.MessageRaw, event.MessageText, event.Attributes)
			}
			if event.Source.Project != "shop" || event.Source.Service != "api" ||
				event.Container == nil || event.Container.ID != "c1" || event.Stream != StreamStdout {
				t.Fatalf("envelope not normalized: %#v %#v", event.Source, event.Container)
			}
		})
	}

	// Without log, message, or msg the record is its own payload. Its raw
	// text is kept, but its envelope does not reappear as attributes. The
	// producer-reported Server IP is not envelope that Siftail represents, so
	// it stays an attribute, as on every other path.
	body := `{` + envelope + `,"level":"warn","customer":1}`
	record, err := ParseRecord([]byte(body), maxJSONDepth)
	if err != nil {
		t.Fatal(err)
	}
	event, err := Normalize(record, TrustedServer{ID: 1}, received)
	if err != nil {
		t.Fatal(err)
	}
	if string(event.MessageRaw) != body || event.MessageText != body ||
		event.Level != LevelWarn ||
		string(event.Attributes) != `{"coolify.server_ip":"10.0.0.7","customer":1}` {
		t.Fatalf("record payload: raw %q text %q level %s attributes %s",
			event.MessageRaw, event.MessageText, event.Level, event.Attributes)
	}
}

func TestJSONStringMatchesUnmarshal(t *testing.T) {
	for _, raw := range []string{
		`"plain"`, `""`, `"with \"quote\""`, `"tab\tescape"`, `"\u00e9t\u00e9"`,
		`"żółw"`, `"a/b"`, `123`, `null`, `true`, `{"a":1}`, `["x"]`, `"unterminated`,
		`"`, "\"raw\ttab\"", "\"\xff\"", `"trailing" `,
	} {
		got, ok := jsonString(json.RawMessage(raw))
		var want string
		wantOK := json.Unmarshal([]byte(raw), &want) == nil
		if ok != wantOK || got != want {
			t.Errorf("jsonString(%q) = %q,%v; Unmarshal = %q,%v", raw, got, ok, want, wantOK)
		}
	}
}
