package ingest

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/logs"
)

func TestJSONDecoderAcceptedFluentBitShapes(t *testing.T) {
	received := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		mediaType string
		body      string
		gzip      bool
		want      int
	}{
		{
			"json array", "application/json",
			`[{"date":"2026-07-27T20:14:55.123456Z","log":"first","tag":"coolify.app","application":"app"},{"log":"second","stream":"stderr"}]`,
			false, 2,
		},
		{
			"json object", "application/json",
			`{"log":"one\n  two","service":"worker"}`,
			false, 1,
		},
		{
			"structured object", "application/json",
			`{"level":"ERROR","message":"failed","request_id":"r1","extra":true}`,
			false, 1,
		},
		{
			"json lines gzip", "application/x-ndjson",
			"{\"log\":\"first\"}\n\n{\"log\":\"second\"}\n",
			true, 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.body)
			if tt.gzip {
				body = gzipBytes(t, body)
			}
			decoder := testJSONDecoder(int64(len(body)+16), 1<<20, 1<<16, 10)
			batch, err := decoder.Decode(context.Background(), DecodeRequest{
				Body: bytes.NewReader(body), MediaType: tt.mediaType, Gzip: tt.gzip,
				ReceivedAt: received, Server: logs.TrustedServer{ID: 9},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(batch.Events) != tt.want || batch.ApproxBytes <= 0 {
				t.Fatalf("batch = %#v", batch)
			}
			for _, event := range batch.Events {
				if event.Source.ServerID != 9 {
					t.Fatal("payload replaced trusted Server")
				}
			}
			if tt.name == "json object" && string(batch.Events[0].MessageRaw) != "one\n  two" {
				t.Fatalf("multiline raw = %q", batch.Events[0].MessageRaw)
			}
			if tt.name == "structured object" && string(batch.Events[0].MessageRaw) != tt.body {
				t.Fatalf("structured raw = %q", batch.Events[0].MessageRaw)
			}
		})
	}
}

func TestOfficialFormatAndCoolifyFixtures(t *testing.T) {
	fixtures := []struct {
		path      string
		mediaType string
	}{
		{"testdata/fluent-bit-json-array.json", "application/json"},
		{"testdata/fluent-bit-json-lines.ndjson", "application/x-ndjson"},
	}
	for _, fixture := range fixtures {
		body, err := os.ReadFile(fixture.path)
		if err != nil {
			t.Fatal(err)
		}
		decoder := testJSONDecoder(1<<20, 1<<20, 1<<16, 10)
		batch, err := decoder.Decode(context.Background(), DecodeRequest{
			Body: bytes.NewReader(body), MediaType: fixture.mediaType,
			ReceivedAt: time.Unix(1, 0), Server: logs.TrustedServer{ID: 1},
		})
		if err != nil || len(batch.Events) != 2 {
			t.Fatalf("%s: events=%d err=%v", fixture.path, len(batch.Events), err)
		}
		if batch.Events[0].Source.Service == "default" {
			t.Fatalf("%s: Coolify service alias was not normalized", fixture.path)
		}
	}
}

func TestJSONDecoderRejectsWholeInvalidRequest(t *testing.T) {
	tests := []struct {
		name      string
		mediaType string
		body      string
		category  ErrorCategory
	}{
		{"empty json", "application/json", "", CategoryBadRequest},
		{"empty array", "application/json", "[]", CategoryBadRequest},
		{"scalar", "application/json", `"text"`, CategoryBadRequest},
		{"array scalar", "application/json", `[{"log":"ok"},2]`, CategoryBadRequest},
		{"trailing", "application/json", `{"log":"ok"} false`, CategoryBadRequest},
		{"malformed last", "application/x-ndjson", "{\"log\":\"ok\"}\n{\"log\":", CategoryBadRequest},
		{"duplicate top", "application/json", `{"log":"x","log":"y"}`, CategoryBadRequest},
		{"duplicate nested", "application/json", `{"log":"x","a":{"same":1,"same":2}}`, CategoryBadRequest},
		{"bad timestamp", "application/json", `{"timestamp":"yesterday","log":"x"}`, CategoryBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decoder := testJSONDecoder(1<<20, 1<<20, 1<<16, 10)
			batch, err := decoder.Decode(context.Background(), DecodeRequest{
				Body: bytes.NewBufferString(tt.body), MediaType: tt.mediaType,
				ReceivedAt: time.Unix(1, 0), Server: logs.TrustedServer{ID: 1},
			})
			if err == nil || len(batch.Events) != 0 {
				t.Fatalf("batch/error = %#v / %v", batch, err)
			}
			var ingestErr *Error
			if !errors.As(err, &ingestErr) || ingestErr.Category != tt.category {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func TestJSONDecoderIndependentLimits(t *testing.T) {
	tests := []struct {
		name       string
		body       []byte
		gzip       bool
		compressed int64
		decomp     int64
		event      int64
		events     int
	}{
		{"compressed", []byte(`{"log":"12345"}`), false, 5, 100, 100, 10},
		{"decompressed gzip bomb", gzipBytes(t, []byte(`{"log":"`+strings.Repeat("x", 4096)+`"}`)), true, 1000, 128, 8192, 10},
		{"single event", []byte(`{"log":"` + strings.Repeat("x", 128) + `"}`), false, 1000, 1000, 64, 10},
		{"event count", []byte("{\"log\":\"1\"}\n{\"log\":\"2\"}\n"), false, 1000, 1000, 100, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decoder := testJSONDecoder(tt.compressed, tt.decomp, tt.event, tt.events)
			mediaType := "application/json"
			if tt.name == "event count" {
				mediaType = "application/x-ndjson"
			}
			_, err := decoder.Decode(context.Background(), DecodeRequest{
				Body: bytes.NewReader(tt.body), MediaType: mediaType, Gzip: tt.gzip,
				ReceivedAt: time.Unix(1, 0), Server: logs.TrustedServer{ID: 1},
			})
			var ingestErr *Error
			if !errors.As(err, &ingestErr) || ingestErr.Category != CategoryTooLarge {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func TestJSONDecoderBoundarySizes(t *testing.T) {
	body := []byte(`{"log":"x"}`)
	decoder := testJSONDecoder(int64(len(body)), int64(len(body)), int64(len(body)), 1)
	batch, err := decoder.Decode(context.Background(), DecodeRequest{
		Body: bytes.NewReader(body), MediaType: "application/json",
		ReceivedAt: time.Unix(1, 0), Server: logs.TrustedServer{ID: 1},
	})
	if err != nil || len(batch.Events) != 1 {
		t.Fatalf("boundary batch/error = %#v / %v", batch, err)
	}
}

func FuzzJSONDecoder(f *testing.F) {
	f.Add("application/json", `{"log":"hello"}`)
	f.Add("application/x-ndjson", "{\"log\":\"one\"}\n{\"log\":\"two\"}\n")
	f.Fuzz(func(t *testing.T, mediaType, body string) {
		if mediaType != "application/json" && mediaType != "application/x-ndjson" {
			return
		}
		decoder := testJSONDecoder(4096, 4096, 1024, 16)
		_, _ = decoder.Decode(context.Background(), DecodeRequest{
			Body: bytes.NewBufferString(body), MediaType: mediaType,
			ReceivedAt: time.Unix(1, 0), Server: logs.TrustedServer{ID: 1},
		})
	})
}

func testJSONDecoder(compressed, decompressed, event int64, events int) *JSONDecoder {
	return NewJSONDecoder(DecoderLimits{
		MaxCompressedBytes: compressed, MaxDecompressedBytes: decompressed,
		MaxEventBytes: event, MaxEvents: events, MaxJSONDepth: 32,
	})
}

func gzipBytes(t *testing.T, raw []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := gzip.NewWriter(&output)
	if _, err := writer.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
