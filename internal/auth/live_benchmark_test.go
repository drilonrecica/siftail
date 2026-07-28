package auth

import (
	"net/http"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/logs"
)

func BenchmarkLiveSSEFrame(b *testing.B) {
	browser := &Browser{liveWriteTimeout: 5 * time.Second}
	event := authLiveEvent(1, 1, "error", "stderr",
		"representative live message with structured attributes and common fields")
	writer := &discardLiveWriter{header: make(http.Header)}

	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		event.ID = int64(index + 1)
		if err := browser.writeLiveMessage(writer, liveLogMessage(event)); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if b.N > 0 {
		b.ReportMetric(float64(writer.written)/float64(b.N), "wire-bytes/op")
	}
}

func liveLogMessage(event logs.CommittedEvent) logs.LiveMessage {
	return logs.LiveMessage{Type: logs.LiveMessageLog, Event: event}
}

type discardLiveWriter struct {
	header  http.Header
	written int64
}

func (w *discardLiveWriter) Header() http.Header { return w.header }
func (w *discardLiveWriter) WriteHeader(int)     {}
func (w *discardLiveWriter) Write(payload []byte) (int, error) {
	w.written += int64(len(payload))
	return len(payload), nil
}
func (w *discardLiveWriter) Flush() {}
func (w *discardLiveWriter) SetWriteDeadline(time.Time) error {
	return nil
}

var _ http.Flusher = (*discardLiveWriter)(nil)
