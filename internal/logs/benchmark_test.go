package logs

import (
	"testing"
	"time"
)

func BenchmarkNormalize(b *testing.B) {
	record := testRecord(`{
		"timestamp":"2026-07-28T08:00:00.123456Z",
		"log":"ERROR: representative checkout failure\nstack line one\nstack line two",
		"stream":"stderr",
		"coolify_project_name":"billing",
		"environment":"production",
		"application":"checkout",
		"service":"api",
		"container_id":"container-1",
		"request_id":"request-1",
		"customer_id":842,
		"source_event_id":"event-1"
	}`)
	received := time.Date(2026, 7, 28, 8, 0, 1, 0, time.UTC)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Normalize(record, TrustedServer{ID: 1}, received); err != nil {
			b.Fatal(err)
		}
	}
}
