package logs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
)

func TestExportReusesAllHistoryFiltersAndCanonicalOrdering(t *testing.T) {
	db := newExportDatabase(t)
	status := int64(503)
	query := exportTestQuery()
	query.ServerID = 1
	query.Project = "project"
	query.Environment = "production"
	query.Application = "api"
	query.Service = "web"
	query.ContainerID = 1
	query.Levels = []Level{LevelError}
	query.Streams = []Stream{StreamStderr}
	query.Contains = "HOSTILE"
	query.Excludes = "absent"
	query.RequestID = "request-2"
	query.Logger = "worker"
	query.HTTPMethod = "POST"
	query.HTTPStatus = &status
	query.ErrorType = "timeout"
	query.Cursor = strings.Repeat("a", 10)
	query.Direction = DirectionNewer
	query.Limit = 1

	var output bytes.Buffer
	result, err := NewExportStore(db.Reader(), ExportLimits{}).Export(
		context.Background(), query, ExportFormatNDJSON, &output,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows != 1 || result.Attempt.ServerID != 1 ||
		result.Validate() != nil {
		t.Fatalf("result = %#v", result)
	}
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'})
	if len(lines) != 1 {
		t.Fatalf("NDJSON lines = %d: %q", len(lines), output.Bytes())
	}
	var event ndjsonExportEvent
	if err := json.Unmarshal(lines[0], &event); err != nil {
		t.Fatal(err)
	}
	if event.SchemaVersion != ExportSchemaVersion || event.ID != 2 ||
		event.EventAt != "2026-07-28T12:00:00.123456Z" ||
		event.ReceivedAt != "2026-07-28T12:00:01.654321Z" ||
		event.Message != "hostile </script>\nsecond\tline" ||
		event.LevelOriginal == nil || *event.LevelOriginal != "ERROR" ||
		event.HTTPPath == nil || *event.HTTPPath != "/v1/jobs" ||
		event.Attributes == nil ||
		string(event.Attributes) != `{"unknown":"preserved"}` ||
		event.MessageRawBase64 != base64.StdEncoding.EncodeToString(
			[]byte("raw\x00payload"),
		) {
		t.Fatalf("event = %#v attributes=%s", event, event.Attributes)
	}
	if bytes.Contains(lines[0], []byte{'\t'}) ||
		bytes.Count(output.Bytes(), []byte{'\n'}) != 1 {
		t.Fatalf("NDJSON framing was broken: %q", output.Bytes())
	}
}

func TestExportTextAndNDJSONSchemasPreserveOptionalAndMultilineData(
	t *testing.T,
) {
	db := newExportDatabase(t)
	store := NewExportStore(db.Reader(), ExportLimits{})
	for _, format := range []string{ExportFormatText, ExportFormatNDJSON} {
		t.Run(format, func(t *testing.T) {
			var output bytes.Buffer
			result, err := store.Export(
				context.Background(), exportTestQuery(), format, &output,
			)
			if err != nil || result.Rows != 3 ||
				result.Bytes != int64(output.Len()) {
				t.Fatalf("export = %#v, %v", result, err)
			}
			if format == ExportFormatText {
				lines := strings.Split(
					strings.TrimSuffix(output.String(), "\n"), "\n",
				)
				if len(lines) != 5 ||
					lines[0] != "# siftail-text-v1" ||
					!strings.Contains(lines[2], `"hostile </script>\nsecond\tline"`) ||
					!strings.Contains(lines[4], "\tnull\tnull\tnull\t") {
					t.Fatalf("text export = %q", output.String())
				}
			} else {
				lines := bytes.Split(
					bytes.TrimSpace(output.Bytes()), []byte{'\n'},
				)
				if len(lines) != 3 ||
					!bytes.Contains(lines[2], []byte(`"logger":null`)) ||
					!bytes.Contains(lines[2], []byte(`"attributes":null`)) {
					t.Fatalf("NDJSON export = %q", output.Bytes())
				}
				var ids []int64
				for _, line := range lines {
					var event struct {
						ID int64 `json:"id"`
					}
					if err := json.Unmarshal(line, &event); err != nil {
						t.Fatal(err)
					}
					ids = append(ids, event.ID)
				}
				if len(ids) != 3 || ids[0] != 2 ||
					ids[1] != 1 || ids[2] != 3 {
					t.Fatalf("canonical export order = %v", ids)
				}
			}
		})
	}
}

func TestExportLimitsCancellationTimeoutAndWriteFailure(t *testing.T) {
	db := newExportDatabase(t)
	query := exportTestQuery()
	rowLimited := NewExportStore(db.Reader(), ExportLimits{
		MaxRows: 1, MaxBytes: DefaultExportBytes, Timeout: time.Second,
	})
	var output bytes.Buffer
	result, err := rowLimited.Export(
		context.Background(), query, ExportFormatNDJSON, &output,
	)
	if !errors.Is(err, ErrExportRowLimit) || result.Rows != 1 ||
		result.Validate() != nil ||
		ExportFailureCategory(err) != "row_limit" {
		t.Fatalf("row limit = %#v, %v", result, err)
	}

	var complete bytes.Buffer
	completeResult, err := NewExportStore(
		db.Reader(), ExportLimits{},
	).Export(context.Background(), query, ExportFormatNDJSON, &complete)
	if err != nil {
		t.Fatal(err)
	}
	exact := NewExportStore(db.Reader(), ExportLimits{
		MaxRows: DefaultExportRows, MaxBytes: completeResult.Bytes,
		Timeout: time.Second,
	})
	output.Reset()
	if result, err := exact.Export(
		context.Background(), query, ExportFormatNDJSON, &output,
	); err != nil || result.Bytes != completeResult.Bytes {
		t.Fatalf("exact byte limit = %#v, %v", result, err)
	}
	tooSmall := NewExportStore(db.Reader(), ExportLimits{
		MaxRows: DefaultExportRows, MaxBytes: completeResult.Bytes - 1,
		Timeout: time.Second,
	})
	output.Reset()
	result, err = tooSmall.Export(
		context.Background(), query, ExportFormatNDJSON, &output,
	)
	if !errors.Is(err, ErrExportByteLimit) ||
		result.Bytes >= completeResult.Bytes || result.Validate() != nil ||
		ExportFailureCategory(err) != "byte_limit" {
		t.Fatalf("byte limit = %#v, %v", result, err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = NewExportStore(db.Reader(), ExportLimits{}).Export(
		canceled, query, ExportFormatNDJSON, io.Discard,
	)
	if !errors.Is(err, context.Canceled) || result.Rows != 0 {
		t.Fatalf("canceled export = %#v, %v", result, err)
	}
	result, err = NewExportStore(db.Reader(), ExportLimits{
		MaxRows: DefaultExportRows, MaxBytes: DefaultExportBytes,
		Timeout: time.Nanosecond,
	}).Export(context.Background(), query, ExportFormatNDJSON, io.Discard)
	if !errors.Is(err, context.DeadlineExceeded) || result.Rows != 0 {
		t.Fatalf("timed out export = %#v, %v", result, err)
	}
	result, err = NewExportStore(db.Reader(), ExportLimits{}).Export(
		context.Background(), query, ExportFormatText, failingExportWriter{},
	)
	if err == nil || strings.Contains(err.Error(), "hostile") ||
		result.Bytes != 0 || ExportFailureCategory(err) != "failed" {
		t.Fatalf("failed writer = %#v, %v", result, err)
	}

	invalidRange := query
	invalidRange.FromUS = invalidRange.ToUS -
		int64((MaxHistoryRange+time.Microsecond)/time.Microsecond)
	output.Reset()
	result, err = NewExportStore(db.Reader(), ExportLimits{}).Export(
		context.Background(), invalidRange, ExportFormatNDJSON, &output,
	)
	if err == nil || output.Len() != 0 || result.Rows != 0 {
		t.Fatalf("invalid range = %#v, %v, %q", result, err, output.Bytes())
	}
}

func TestExportAppliesSynchronousBackpressure(t *testing.T) {
	db := newExportDatabase(t)
	writer := &blockingExportWriter{
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	done := make(chan error, 1)
	go func() {
		_, err := NewExportStore(db.Reader(), ExportLimits{}).Export(
			context.Background(), exportTestQuery(),
			ExportFormatNDJSON, writer,
		)
		done <- err
	}()
	<-writer.entered
	select {
	case err := <-done:
		t.Fatalf("export bypassed writer backpressure: %v", err)
	default:
	}
	close(writer.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestExportAcceptsMaximumCanonicalEventWithoutUnboundedState(t *testing.T) {
	db := newExportDatabase(t)
	message := strings.Repeat("m", 1<<20)
	attributes := `{"value":"` + strings.Repeat("a", (256<<10)-20) + `"}`
	if len(attributes) > 256<<10 {
		t.Fatal("test attributes exceed canonical limit")
	}
	if _, err := db.Writer().Exec(`
		INSERT INTO log_events(
			id,event_at_us,received_at_us,source_id,stream,level_normalized,
			message_raw,message_text,attributes_json
		) VALUES(4,?,?,1,'stdout','info',?,?,?)`,
		time.Date(2026, 7, 28, 13, 0, 0, 0, time.UTC).UnixMicro(),
		time.Date(2026, 7, 28, 13, 0, 1, 0, time.UTC).UnixMicro(),
		[]byte(message), message, attributes,
	); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	result, err := NewExportStore(db.Reader(), ExportLimits{}).Export(
		context.Background(), exportTestQuery(),
		ExportFormatNDJSON, &output,
	)
	if err != nil || result.Rows != 4 || output.Len() <= len(message) {
		t.Fatalf("maximum event export = %#v, %v, bytes=%d",
			result, err, output.Len())
	}
}

type failingExportWriter struct{}

func (failingExportWriter) Write([]byte) (int, error) {
	return 0, errors.New("private writer detail")
}

type blockingExportWriter struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
	output  bytes.Buffer
}

func (w *blockingExportWriter) Write(value []byte) (int, error) {
	w.once.Do(func() {
		close(w.entered)
		<-w.release
	})
	return w.output.Write(value)
}

func newExportDatabase(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open(
		context.Background(), filepath.Join(t.TempDir(), "siftail.db"),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	eventAt := time.Date(
		2026, 7, 28, 12, 0, 0, 123456000, time.UTC,
	).UnixMicro()
	receivedAt := time.Date(
		2026, 7, 28, 12, 0, 1, 654321000, time.UTC,
	).UnixMicro()
	if _, err := db.Writer().Exec(`
		INSERT INTO servers(id,name,created_at_us)
		VALUES(1,'production',1),(2,'other',1);
		INSERT INTO sources(
			id,server_id,project_key,environment_key,application_key,service_key,
			project_label,environment_label,application_label,service_label,
			alias,first_seen_at_us,last_seen_at_us
		) VALUES
			(1,1,'project','production','api','web',
			 'Project','Production','API','Web','Primary API',1,1),
			(2,2,'other','test','worker','job',
			 'Other','Test','Worker','Job',NULL,1,1);
		INSERT INTO container_instances(
			id,source_id,container_id,container_name,first_seen_at_us,last_seen_at_us
		) VALUES(1,1,'container-id','api-1',1,1);
		INSERT INTO log_events(
			id,event_at_us,received_at_us,source_id,container_instance_id,
			stream,level_normalized,level_original,message_raw,message_text,
			attributes_json,source_event_id,logger,request_id,error_type,
			http_method,http_path,http_status,duration_ms
		) VALUES
			(1,?,?,1,1,'stderr','error','ERROR',X'726177',
			 'ordinary','{"one":1}','event-1','worker','request-1',
			 'timeout','POST','/v1/jobs',503,12.5),
			(2,?,?,1,1,'stderr','error','ERROR',?,
			 'hostile </script>
second	line','{
"unknown":"preserved"
}','event-2','worker','request-2',
			 'timeout','POST','/v1/jobs',503,12.5),
			(3,?,?,2,NULL,'stdout','unknown',NULL,X'00',
			 'optional',NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL)`,
		eventAt, receivedAt,
		eventAt, receivedAt, []byte("raw\x00payload"),
		eventAt-1, receivedAt,
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func exportTestQuery() HistoryQuery {
	return HistoryQuery{
		FromUS: time.Date(
			2026, 7, 28, 11, 0, 0, 0, time.UTC,
		).UnixMicro(),
		ToUS: time.Date(
			2026, 7, 29, 0, 0, 0, 0, time.UTC,
		).UnixMicro(),
		Direction: DirectionOlder, Limit: DefaultHistoryLimit,
	}
}
