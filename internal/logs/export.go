package logs

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ExportSchemaVersion  = 1
	ExportFormatText     = "text"
	ExportFormatNDJSON   = "ndjson"
	DefaultExportRows    = 100_000
	DefaultExportBytes   = 256 << 20
	DefaultExportTimeout = 2 * time.Minute
)

var (
	ErrExportRowLimit  = errors.New("export exceeds the event limit")
	ErrExportByteLimit = errors.New("export exceeds the byte limit")
)

func ExportFailureCategory(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrExportRowLimit):
		return "row_limit"
	case errors.Is(err, ErrExportByteLimit):
		return "byte_limit"
	default:
		return "failed"
	}
}

type ExportLimits struct {
	MaxRows  int
	MaxBytes int64
	Timeout  time.Duration
}

type ExportAttempt struct {
	Format   string
	ServerID int64
	FromUS   int64
	ToUS     int64
	MaxRows  int
	MaxBytes int64
}

func (a ExportAttempt) Validate() error {
	if !validExportFormat(a.Format) || a.ServerID < 0 ||
		a.FromUS >= a.ToUS || a.MaxRows < 1 ||
		a.MaxRows > DefaultExportRows || a.MaxBytes < 1 ||
		a.MaxBytes > DefaultExportBytes {
		return errors.New("invalid export attempt metadata")
	}
	return nil
}

type ExportResult struct {
	Attempt ExportAttempt
	Rows    int
	Bytes   int64
}

func (r ExportResult) Validate() error {
	if err := r.Attempt.Validate(); err != nil || r.Rows < 0 ||
		r.Rows > r.Attempt.MaxRows || r.Bytes < 0 ||
		r.Bytes > r.Attempt.MaxBytes {
		return errors.New("invalid export result")
	}
	return nil
}

type ExportStore struct {
	db     *sql.DB
	limits ExportLimits
}

func NewExportStore(db *sql.DB, limits ExportLimits) *ExportStore {
	if limits.MaxRows <= 0 || limits.MaxRows > DefaultExportRows {
		limits.MaxRows = DefaultExportRows
	}
	if limits.MaxBytes <= 0 || limits.MaxBytes > DefaultExportBytes {
		limits.MaxBytes = DefaultExportBytes
	}
	if limits.Timeout <= 0 || limits.Timeout > DefaultExportTimeout {
		limits.Timeout = DefaultExportTimeout
	}
	return &ExportStore{db: db, limits: limits}
}

// Export streams the complete matching range in canonical History order.
// Pagination cursor, direction, and page size never narrow the export.
func (s *ExportStore) Export(
	ctx context.Context,
	query HistoryQuery,
	format string,
	output io.Writer,
) (ExportResult, error) {
	result := ExportResult{Attempt: ExportAttempt{
		Format: format, ServerID: query.ServerID,
		FromUS: query.FromUS, ToUS: query.ToUS,
	}}
	if s != nil {
		result.Attempt.MaxRows = s.limits.MaxRows
		result.Attempt.MaxBytes = s.limits.MaxBytes
	}
	if ctx == nil {
		return result, errors.New("export context is nil")
	}
	if s == nil || s.db == nil || output == nil {
		return result, errors.New("export store is unavailable")
	}
	if !validExportFormat(format) {
		return result, errors.New("unsupported export format")
	}
	normalized, err := ParseHistoryQuery(
		query.CanonicalValues(true), unixMicroTime(query.ToUS),
	)
	if err != nil {
		return result, fmt.Errorf("invalid export query: %w", err)
	}
	normalized.Cursor = ""
	normalized.Direction = DirectionOlder
	normalized.Limit = DefaultHistoryLimit
	result.Attempt.ServerID = normalized.ServerID
	result.Attempt.FromUS = normalized.FromUS
	result.Attempt.ToUS = normalized.ToUS

	exportContext, cancel := context.WithTimeout(ctx, s.limits.Timeout)
	defer cancel()
	filter, arguments := buildHistoryFilterSQL(normalized, nil)
	arguments = append(arguments, s.limits.MaxRows+1)
	rows, err := s.db.QueryContext(
		exportContext,
		historyEventColumns+"\nWHERE "+filter+
			"\nORDER BY e.event_at_us DESC, e.id DESC\nLIMIT ?",
		arguments...,
	)
	if err != nil {
		return result, exportReadError(exportContext, "query export events", err)
	}
	defer rows.Close()

	writer := &exportWriter{
		output: output, maximum: s.limits.MaxBytes,
	}
	if format == ExportFormatText {
		if err := writer.write([]byte(textExportHeader)); err != nil {
			result.Bytes = writer.written
			return result, err
		}
	}
	for rows.Next() {
		if err := exportContext.Err(); err != nil {
			result.Bytes = writer.written
			return result, err
		}
		event, err := scanHistoryEvent(rows)
		if err != nil {
			result.Bytes = writer.written
			return result, errors.New("scan export event")
		}
		if result.Rows >= s.limits.MaxRows {
			result.Bytes = writer.written
			return result, ErrExportRowLimit
		}
		encoded, err := encodeExportEvent(format, event)
		if err != nil {
			result.Bytes = writer.written
			return result, err
		}
		if err := writer.write(encoded); err != nil {
			result.Bytes = writer.written
			return result, err
		}
		result.Rows++
	}
	if err := rows.Err(); err != nil {
		result.Bytes = writer.written
		return result, exportReadError(
			exportContext, "iterate export events", err,
		)
	}
	result.Bytes = writer.written
	if err := result.Validate(); err != nil {
		return result, err
	}
	return result, nil
}

func validExportFormat(format string) bool {
	return format == ExportFormatText || format == ExportFormatNDJSON
}

type exportWriter struct {
	output  io.Writer
	maximum int64
	written int64
}

func (w *exportWriter) write(value []byte) error {
	if int64(len(value)) > w.maximum-w.written {
		return ErrExportByteLimit
	}
	written, err := w.output.Write(value)
	w.written += int64(written)
	if err != nil || written != len(value) {
		return errors.New("write export artifact")
	}
	return nil
}

func encodeExportEvent(format string, event HistoryEvent) ([]byte, error) {
	if err := validateExportEvent(event); err != nil {
		return nil, err
	}
	if format == ExportFormatText {
		return encodeTextExportEvent(event), nil
	}
	value := newNDJSONExportEvent(event)
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, errors.New("encode NDJSON export event")
	}
	return encoded.Bytes(), nil
}

func validateExportEvent(event HistoryEvent) error {
	for _, timestamp := range []int64{event.EventAtUS, event.ReceivedAtUS} {
		year := time.UnixMicro(timestamp).UTC().Year()
		if year < 1 || year > 9999 {
			return errors.New("export event timestamp is invalid")
		}
	}
	if !utf8.ValidString(event.MessageText) ||
		(event.AttributesJSON != nil && !json.Valid(event.AttributesJSON)) {
		return errors.New("export event content is invalid")
	}
	return nil
}

type ndjsonExportEvent struct {
	SchemaVersion       int             `json:"schema_version"`
	ID                  int64           `json:"id"`
	EventAt             string          `json:"event_at"`
	ReceivedAt          string          `json:"received_at"`
	SourceID            int64           `json:"source_id"`
	ServerID            int64           `json:"server_id"`
	ServerName          string          `json:"server_name"`
	ProjectKey          string          `json:"project_key"`
	EnvironmentKey      string          `json:"environment_key"`
	ApplicationKey      string          `json:"application_key"`
	ServiceKey          string          `json:"service_key"`
	ProjectLabel        string          `json:"project_label"`
	EnvironmentLabel    string          `json:"environment_label"`
	ApplicationLabel    string          `json:"application_label"`
	ServiceLabel        string          `json:"service_label"`
	SourceAlias         *string         `json:"source_alias"`
	ContainerInstanceID *int64          `json:"container_instance_id"`
	ContainerID         *string         `json:"container_id"`
	ContainerName       *string         `json:"container_name"`
	Stream              Stream          `json:"stream"`
	Level               Level           `json:"level"`
	LevelOriginal       *string         `json:"level_original"`
	Message             string          `json:"message"`
	MessageRawBase64    string          `json:"message_raw_base64"`
	Attributes          json.RawMessage `json:"attributes"`
	SourceEventID       *string         `json:"source_event_id"`
	Logger              *string         `json:"logger"`
	RequestID           *string         `json:"request_id"`
	ErrorType           *string         `json:"error_type"`
	HTTPMethod          *string         `json:"http_method"`
	HTTPPath            *string         `json:"http_path"`
	HTTPStatus          *int64          `json:"http_status"`
	DurationMS          *float64        `json:"duration_ms"`
}

func newNDJSONExportEvent(event HistoryEvent) ndjsonExportEvent {
	return ndjsonExportEvent{
		SchemaVersion: ExportSchemaVersion, ID: event.ID,
		EventAt:    formatExportTime(event.EventAtUS),
		ReceivedAt: formatExportTime(event.ReceivedAtUS),
		SourceID:   event.SourceID, ServerID: event.ServerID,
		ServerName: event.ServerName, ProjectKey: event.ProjectKey,
		EnvironmentKey: event.EnvironmentKey,
		ApplicationKey: event.ApplicationKey, ServiceKey: event.ServiceKey,
		ProjectLabel:     event.ProjectLabel,
		EnvironmentLabel: event.EnvironmentLabel,
		ApplicationLabel: event.ApplicationLabel,
		ServiceLabel:     event.ServiceLabel, SourceAlias: event.SourceAlias,
		ContainerInstanceID: event.ContainerInstanceID,
		ContainerID:         event.ContainerID, ContainerName: event.ContainerName,
		Stream: event.Stream, Level: event.Level,
		LevelOriginal: event.OriginalLevel, Message: event.MessageText,
		MessageRawBase64: base64.StdEncoding.EncodeToString(event.MessageRaw),
		Attributes:       nullableJSON(event.AttributesJSON),
		SourceEventID:    event.SourceEventID, Logger: event.Logger,
		RequestID: event.RequestID, ErrorType: event.ErrorType,
		HTTPMethod: event.HTTPMethod, HTTPPath: event.HTTPPath,
		HTTPStatus: event.HTTPStatus, DurationMS: event.DurationMS,
	}
}

func nullableJSON(value []byte) json.RawMessage {
	if value == nil {
		return json.RawMessage("null")
	}
	return json.RawMessage(value)
}

func formatExportTime(value int64) string {
	return time.UnixMicro(value).UTC().Format(time.RFC3339Nano)
}

const textExportHeader = "# siftail-text-v1\n" +
	"id\tevent_at\treceived_at\tsource_id\tserver_id\tserver_name\t" +
	"project_key\tenvironment_key\tapplication_key\tservice_key\t" +
	"project_label\tenvironment_label\tapplication_label\tservice_label\t" +
	"source_alias\tcontainer_instance_id\tcontainer_id\tcontainer_name\t" +
	"stream\tlevel\tlevel_original\tmessage\tmessage_raw_base64\tattributes\t" +
	"source_event_id\tlogger\trequest_id\terror_type\thttp_method\thttp_path\t" +
	"http_status\tduration_ms\n"

func encodeTextExportEvent(event HistoryEvent) []byte {
	fields := []string{
		strconv.FormatInt(event.ID, 10),
		quoteExportString(formatExportTime(event.EventAtUS)),
		quoteExportString(formatExportTime(event.ReceivedAtUS)),
		strconv.FormatInt(event.SourceID, 10),
		strconv.FormatInt(event.ServerID, 10),
		quoteExportString(event.ServerName),
		quoteExportString(event.ProjectKey),
		quoteExportString(event.EnvironmentKey),
		quoteExportString(event.ApplicationKey),
		quoteExportString(event.ServiceKey),
		quoteExportString(event.ProjectLabel),
		quoteExportString(event.EnvironmentLabel),
		quoteExportString(event.ApplicationLabel),
		quoteExportString(event.ServiceLabel),
		quoteOptionalString(event.SourceAlias),
		formatOptionalInt(event.ContainerInstanceID),
		quoteOptionalString(event.ContainerID),
		quoteOptionalString(event.ContainerName),
		quoteExportString(string(event.Stream)),
		quoteExportString(string(event.Level)),
		quoteOptionalString(event.OriginalLevel),
		quoteExportString(event.MessageText),
		quoteExportString(base64.StdEncoding.EncodeToString(event.MessageRaw)),
		quoteOptionalBytes(event.AttributesJSON),
		quoteOptionalString(event.SourceEventID),
		quoteOptionalString(event.Logger),
		quoteOptionalString(event.RequestID),
		quoteOptionalString(event.ErrorType),
		quoteOptionalString(event.HTTPMethod),
		quoteOptionalString(event.HTTPPath),
		formatOptionalInt(event.HTTPStatus),
		formatOptionalFloat(event.DurationMS),
	}
	return []byte(strings.Join(fields, "\t") + "\n")
}

func quoteExportString(value string) string {
	return strconv.Quote(value)
}

func quoteOptionalString(value *string) string {
	if value == nil {
		return "null"
	}
	return quoteExportString(*value)
}

func quoteOptionalBytes(value []byte) string {
	if value == nil {
		return "null"
	}
	return quoteExportString(string(value))
}

func formatOptionalInt(value *int64) string {
	if value == nil {
		return "null"
	}
	return strconv.FormatInt(*value, 10)
}

func formatOptionalFloat(value *float64) string {
	if value == nil {
		return "null"
	}
	return strconv.FormatFloat(*value, 'g', -1, 64)
}

func exportReadError(ctx context.Context, operation string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return fmt.Errorf("%s: %w", operation, err)
}
