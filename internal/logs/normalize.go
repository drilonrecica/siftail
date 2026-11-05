// Package logs defines canonical application-log values and normalization.
package logs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxIdentityBytes  = 128
	maxLevelBytes     = 64
	maxEventIDBytes   = 255
	maxCommonBytes    = 255
	maxMessageBytes   = 1 << 20
	maxAttributesSize = 256 << 10
	maxJSONDepth      = 32
)

type Stream string

const (
	StreamStdout  Stream = "stdout"
	StreamStderr  Stream = "stderr"
	StreamUnknown Stream = "unknown"
)

type Level string

const (
	LevelTrace   Level = "trace"
	LevelDebug   Level = "debug"
	LevelInfo    Level = "info"
	LevelWarn    Level = "warn"
	LevelError   Level = "error"
	LevelFatal   Level = "fatal"
	LevelUnknown Level = "unknown"
)

// ReceivedRecord is the typed decoder-to-normalizer boundary. Values retain
// their JSON representation; transport maps do not pass beyond Normalize.
type ReceivedRecord struct {
	Timestamp json.RawMessage
	Fields    map[string]json.RawMessage
	Raw       []byte
	// parsed marks a record built by ParseRecord: Raw is already validated
	// and Fields belongs to the record, so Normalize neither validates Raw
	// again nor copies Fields before consuming it.
	parsed bool
}

// ParseRecord validates one received JSON object and splits its top-level
// members in the same token pass. It rejects malformed JSON, invalid UTF-8,
// duplicate keys at any level, trailing data, a value that is not an object,
// and nesting deeper than maxDepth or the canonical limit, whichever is
// smaller. Field values are slices of raw, which must not change afterwards.
// The Fluent Bit record timestamp is the "date" member.
func ParseRecord(raw []byte, maxDepth int) (ReceivedRecord, error) {
	fields, err := parseObject(raw, min(maxDepth, maxJSONDepth))
	if err != nil {
		return ReceivedRecord{}, err
	}
	return ReceivedRecord{Timestamp: fields["date"], Fields: fields, Raw: raw, parsed: true}, nil
}

type SourceIdentity struct {
	ServerID     int64
	Project      string
	Environment  string
	Application  string
	Service      string
	ProjectLabel string
	EnvLabel     string
	AppLabel     string
	ServiceLabel string
}

// MaxContainerInstancesPerServer bounds retained container instances per
// Server (DOMAIN.md §14). Ingestion rejects a batch that would exceed it;
// retention prunes unreferenced instances so only instances with retained
// events can reach it.
const MaxContainerInstancesPerServer = 100_000

type ContainerIdentity struct {
	ID   string
	Name string
}

type CommonFields struct {
	Logger     string
	RequestID  string
	ErrorType  string
	HTTPMethod string
	HTTPPath   string
	HTTPStatus *int64
	DurationMS *float64
}

type CanonicalEvent struct {
	EventAtUS int64
	// EventAtDefaulted is true exactly when neither a source timestamp nor a
	// Fluent Bit record timestamp was present and EventAtUS is the receive
	// time (DOMAIN.md §5.2 step 3, ADR 0007).
	EventAtDefaulted bool
	ReceivedAtUS     int64
	Source           SourceIdentity
	Container        *ContainerIdentity
	Stream           Stream
	Level            Level
	OriginalLevel    string
	MessageRaw       []byte
	MessageText      string
	Attributes       []byte
	SourceEventID    string
	Common           CommonFields
}

type TrustedServer struct {
	ID int64
}

var ErrLimit = errors.New("normalization limit exceeded")

// Normalize converts one decoded record into a fully bounded canonical event.
func Normalize(record ReceivedRecord, server TrustedServer, receivedAt time.Time) (CanonicalEvent, error) {
	if server.ID <= 0 {
		return CanonicalEvent{}, fmt.Errorf("trusted server identity is invalid")
	}
	fields := record.Fields
	if !record.parsed {
		if !utf8.Valid(record.Raw) {
			return CanonicalEvent{}, fmt.Errorf("record is not valid UTF-8")
		}
		if len(record.Raw) > 0 {
			if err := ValidateJSON(record.Raw, maxJSONDepth); err != nil {
				return CanonicalEvent{}, err
			}
		}
		// Normalization deletes the members it consumes; a caller's map
		// is left as it was.
		fields = cloneFields(record.Fields)
	}
	eventAt, eventAtDefaulted, err := normalizeTimestamp(fields, record.Timestamp, receivedAt)
	if err != nil {
		return CanonicalEvent{}, err
	}
	source, container, err := normalizeSource(fields, server.ID)
	if err != nil {
		return CanonicalEvent{}, err
	}
	messageRaw, messageText, structured, err := normalizeMessage(fields, record.Raw, record.parsed)
	if err != nil {
		return CanonicalEvent{}, err
	}
	originalLevel, level, err := normalizeLevel(fields, structured, messageText)
	if err != nil {
		return CanonicalEvent{}, err
	}
	stream := normalizeStream(firstString(fields, "stream", "source"))
	eventID, err := takeBoundedString(fields, maxEventIDBytes, "source_event_id")
	if err != nil {
		return CanonicalEvent{}, fmt.Errorf("source event ID: %w", err)
	}
	common, err := normalizeCommon(fields, structured)
	if err != nil {
		return CanonicalEvent{}, err
	}
	attributes, err := canonicalAttributes(fields, structured)
	if err != nil {
		return CanonicalEvent{}, err
	}

	event := CanonicalEvent{
		EventAtUS:        eventAt,
		EventAtDefaulted: eventAtDefaulted,
		ReceivedAtUS:     receivedAt.UnixMicro(),
		Source:           source,
		Container:        container,
		Stream:           stream,
		Level:            level,
		OriginalLevel:    originalLevel,
		MessageRaw:       messageRaw,
		MessageText:      messageText,
		Attributes:       attributes,
		SourceEventID:    eventID,
		Common:           common,
	}
	if event.RetainedBytes() > int64(maxMessageBytes+maxAttributesSize+4096) {
		return CanonicalEvent{}, ErrLimit
	}
	return event, nil
}

func cloneFields(input map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

// normalizeTimestamp applies the DOMAIN.md §5.2 precedence and reports whether
// the receive time was used because no timestamp was supplied.
func normalizeTimestamp(fields map[string]json.RawMessage, fluent json.RawMessage, received time.Time) (int64, bool, error) {
	var raw json.RawMessage
	for _, key := range []string{"timestamp", "@timestamp", "time"} {
		if value, ok := fields[key]; ok {
			raw = value
			delete(fields, key)
			break
		}
	}
	if len(raw) == 0 {
		raw = fluent
	}
	delete(fields, "date")
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return received.UnixMicro(), true, nil
	}
	timestamp, err := parseTimestamp(raw)
	if err != nil {
		return 0, false, fmt.Errorf("invalid supplied timestamp")
	}
	return timestamp.UnixMicro(), false, nil
}

func parseTimestamp(raw json.RawMessage) (time.Time, error) {
	if text, ok := jsonString(raw); ok {
		value, err := time.Parse(time.RFC3339Nano, text)
		if err != nil || value.Year() < 1 || value.Year() > 9999 {
			return time.Time{}, errors.New("invalid timestamp")
		}
		return value, nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return time.Time{}, err
	}
	seconds, err := strconv.ParseFloat(string(number), 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return time.Time{}, errors.New("invalid timestamp")
	}
	whole, fraction := math.Modf(seconds)
	value := time.Unix(int64(whole), int64(fraction*1e9)).UTC()
	if value.Year() < 1 || value.Year() > 9999 {
		return time.Time{}, errors.New("timestamp out of range")
	}
	return value, nil
}

func normalizeSource(fields map[string]json.RawMessage, serverID int64) (SourceIdentity, *ContainerIdentity, error) {
	project := firstString(fields, "coolify_project_name", "coolify.project_name", "project_name", "com.docker.compose.project", "project")
	environment := firstString(fields, "coolify_environment_name", "coolify.environment_name", "environment_name", "environment", "env")
	application := firstString(fields, "coolify_application_name", "application_name", "application", "app")
	service := firstString(fields, "coolify_service_name", "coolify.app_name", "service_name", "com.docker.compose.service", "service")
	containerID := firstString(fields, "container_id", "container.id")
	containerName := firstString(fields, "container_name", "container.name")

	if project == "" {
		project = "default-project"
	}
	if environment == "" {
		environment = "default-environment"
	}
	if application == "" {
		application = firstNonempty(firstString(fields, "compose_project"), service, containerName, "unknown-application")
	}
	if service == "" {
		service = firstNonempty(stableContainerName(containerName), "default")
	}
	values := []*string{&project, &environment, &application, &service}
	for _, value := range values {
		normalized, err := identityPart(*value)
		if err != nil {
			return SourceIdentity{}, nil, err
		}
		*value = normalized
	}

	var container *ContainerIdentity
	if containerID != "" || containerName != "" {
		if err := validateBoundedText(containerID, 255, true); err != nil {
			return SourceIdentity{}, nil, fmt.Errorf("container ID: %w", err)
		}
		if err := validateBoundedText(containerName, 255, true); err != nil {
			return SourceIdentity{}, nil, fmt.Errorf("container name: %w", err)
		}
		container = &ContainerIdentity{ID: containerID, Name: containerName}
	}
	return SourceIdentity{
		ServerID: serverID, Project: project, Environment: environment,
		Application: application, Service: service, ProjectLabel: project,
		EnvLabel: environment, AppLabel: application, ServiceLabel: service,
	}, container, nil
}

func identityPart(value string) (string, error) {
	value = strings.Trim(value, " \t\r\n")
	if err := validateBoundedText(value, maxIdentityBytes, false); err != nil {
		return "", fmt.Errorf("invalid source identity: %w", err)
	}
	return value, nil
}

func stableContainerName(value string) string {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, "-")
	if len(parts) > 1 {
		if _, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			return strings.Join(parts[:len(parts)-1], "-")
		}
	}
	return value
}

// normalizeMessage selects the application payload (DOMAIN.md §5.10, §5.11,
// ARCHITECTURE.md §12.4). A record member "log", else "message", else "msg"
// is the payload and keeps its exact text; the record's other members stay
// record fields. A record with none of them is itself the payload. Only a
// payload that is itself a JSON object, or a string holding one, yields
// structured application fields. validated reports that recordRaw, and so
// every member of fields, has already passed ValidateJSON.
func normalizeMessage(fields map[string]json.RawMessage, recordRaw []byte, validated bool) ([]byte, string, map[string]json.RawMessage, error) {
	var payload json.RawMessage
	for _, key := range []string{"log", "message", "msg"} {
		if value, ok := fields[key]; ok {
			payload = value
			delete(fields, key)
			break
		}
	}
	if payload == nil {
		// The whole record is the payload. Its members are already the
		// record fields, so the record is not decoded a second time as
		// structured content; that would also copy transport keys into
		// the attributes.
		raw := recordRaw
		if len(raw) == 0 {
			raw = []byte("{}")
		}
		if !validated {
			if err := ValidateJSON(raw, maxJSONDepth); err != nil {
				return nil, "", nil, err
			}
		}
		if len(raw) > maxMessageBytes {
			return nil, "", nil, ErrLimit
		}
		return append([]byte(nil), raw...), compactJSON(raw), nil, nil
	}

	if text, ok := jsonString(payload); ok {
		if !utf8.ValidString(text) || len(text) > maxMessageBytes {
			return nil, "", nil, ErrLimit
		}
		raw := []byte(text)
		if looksLikeObject(raw) {
			if object, err := parseObject(raw, maxJSONDepth); err == nil {
				return raw, structuredMessage(object, raw), object, nil
			}
		}
		return raw, text, nil, nil
	}

	if !validated {
		if err := ValidateJSON(payload, maxJSONDepth); err != nil {
			return nil, "", nil, err
		}
	}
	if len(payload) > maxMessageBytes {
		return nil, "", nil, ErrLimit
	}
	raw := append([]byte(nil), payload...)
	if looksLikeObject(raw) {
		if object, err := parseObject(raw, maxJSONDepth); err == nil {
			return raw, structuredMessage(object, raw), object, nil
		}
	}
	return raw, compactJSON(raw), nil, nil
}

// structuredMessage is the searchable text of a structured payload: its own
// message field, or its compact JSON when it has none (DOMAIN.md §5.11).
func structuredMessage(object map[string]json.RawMessage, raw []byte) string {
	if message := firstString(object, "message", "msg", "log"); message != "" {
		return message
	}
	return compactJSON(raw)
}

func looksLikeObject(raw []byte) bool {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	return len(trimmed) > 0 && trimmed[0] == '{'
}

func normalizeLevel(fields, structured map[string]json.RawMessage, message string) (string, Level, error) {
	original := firstString(fields, "level", "severity", "log_level")
	if original == "" {
		original = firstString(structured, "level", "severity", "log_level")
	}
	if original != "" {
		original = strings.TrimSpace(original)
		if err := validateBoundedText(original, maxLevelBytes, false); err != nil {
			return "", "", fmt.Errorf("level: %w", err)
		}
		return original, mapLevel(original), nil
	}
	return "", inferLevel(message), nil
}

func mapLevel(value string) Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "trace":
		return LevelTrace
	case "debug", "dbg":
		return LevelDebug
	case "info", "information", "notice":
		return LevelInfo
	case "warn", "warning":
		return LevelWarn
	case "error", "err", "severe":
		return LevelError
	case "fatal", "critical", "crit", "panic", "emerg", "alert":
		return LevelFatal
	default:
		return LevelUnknown
	}
}

func inferLevel(message string) Level {
	value := strings.TrimLeft(message, " \t\r\n")
	if strings.HasPrefix(value, "[") {
		if end := strings.IndexByte(value, ']'); end > 0 {
			token := value[1:end]
			rest := value[end+1:]
			if rest == "" || isLevelDelimiter(rest[0]) {
				return mapLevel(token)
			}
		}
	}
	end := 0
	for end < len(value) && ((value[end] >= 'A' && value[end] <= 'Z') || (value[end] >= 'a' && value[end] <= 'z')) {
		end++
	}
	if end > 0 && (end == len(value) || isLevelDelimiter(value[end])) {
		return mapLevel(value[:end])
	}
	return LevelUnknown
}

func isLevelDelimiter(value byte) bool {
	return value == ':' || value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

func normalizeStream(value string) Stream {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "stdout":
		return StreamStdout
	case "stderr":
		return StreamStderr
	default:
		return StreamUnknown
	}
}

func normalizeCommon(fields, structured map[string]json.RawMessage) (CommonFields, error) {
	all := []map[string]json.RawMessage{structured, fields}
	text := func(keys ...string) (string, error) {
		for _, source := range all {
			value := firstString(source, keys...)
			if value != "" {
				if err := validateBoundedText(value, maxCommonBytes, false); err != nil {
					return "", err
				}
				return value, nil
			}
		}
		return "", nil
	}
	var result CommonFields
	var err error
	if result.Logger, err = text("logger", "logger_name"); err != nil {
		return result, err
	}
	if result.RequestID, err = text("request_id", "requestId", "trace_id"); err != nil {
		return result, err
	}
	if result.ErrorType, err = text("error_type", "error.type"); err != nil {
		return result, err
	}
	if result.HTTPMethod, err = text("http_method", "method"); err != nil {
		return result, err
	}
	if result.HTTPPath, err = text("http_path", "path"); err != nil {
		return result, err
	}
	result.HTTPStatus = firstInt(all, "http_status", "status")
	result.DurationMS = firstFloat(all, "duration_ms")
	return result, nil
}

func canonicalAttributes(fields, structured map[string]json.RawMessage) ([]byte, error) {
	attributes := make(map[string]json.RawMessage)
	for key, value := range fields {
		if isTransportField(key) || isCommonField(key) {
			continue
		}
		attributes[key] = value
	}
	for key, value := range structured {
		if isCommonField(key) || key == "message" || key == "msg" || key == "log" || key == "level" || key == "severity" {
			continue
		}
		attributes[key] = value
	}
	if len(attributes) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(attributes)
	if err != nil {
		return nil, fmt.Errorf("encode attributes")
	}
	if len(encoded) > maxAttributesSize {
		return nil, ErrLimit
	}
	return encoded, nil
}

// isTransportField reports the record members that are transport envelope
// already represented canonically (time, stream, source and container
// identity) or Fluent Bit routing (tag); they are excluded from attributes
// (DOMAIN.md §5.12).
func isTransportField(key string) bool {
	switch key {
	case "date", "timestamp", "@timestamp", "time", "tag", "stream", "source",
		"source_event_id", "container_id", "container.id",
		"container_name", "container.name", "project", "project_name",
		"coolify_project_name", "coolify.project_name",
		"environment", "environment_name", "env",
		"coolify_environment_name", "coolify.environment_name",
		"application", "application_name", "app",
		"coolify_application_name", "service", "service_name",
		"coolify_service_name", "coolify.app_name", "com.docker.compose.project",
		"com.docker.compose.service", "compose_project":
		return true
	default:
		return false
	}
}

func isCommonField(key string) bool {
	switch key {
	case "logger", "logger_name", "request_id", "requestId", "trace_id",
		"error_type", "error.type", "http_method", "method", "http_path",
		"path", "http_status", "status", "duration_ms":
		return true
	default:
		return false
	}
}

func firstString(fields map[string]json.RawMessage, keys ...string) string {
	if fields == nil {
		return ""
	}
	for _, key := range keys {
		if raw, ok := fields[key]; ok {
			if value, ok := jsonString(raw); ok {
				delete(fields, key)
				return value
			}
		}
	}
	return ""
}

// jsonString decodes raw when it is a JSON string or null. A plain ASCII string, the
// common case for field values, is converted directly; anything with escapes
// or non-ASCII bytes goes through encoding/json, as does every malformed
// value, so the result always matches json.Unmarshal.
func jsonString(raw json.RawMessage) (string, bool) {
	if string(raw) == "null" {
		// json.Unmarshal accepts null into a string and leaves it empty;
		// callers rely on that to consume a null member.
		return "", true
	}
	if len(raw) < 2 || raw[0] != '"' {
		return "", false
	}
	if inner := raw[1 : len(raw)-1]; raw[len(raw)-1] == '"' && plainASCII(inner) {
		return string(inner), true
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

// plainASCII reports whether value can appear verbatim between JSON quotes
// with no decoding: printable ASCII without a quote or backslash.
func plainASCII(value []byte) bool {
	for _, char := range value {
		if char < 0x20 || char >= 0x7f || char == '"' || char == '\\' {
			return false
		}
	}
	return true
}

func takeBoundedString(fields map[string]json.RawMessage, max int, keys ...string) (string, error) {
	value := firstString(fields, keys...)
	if value == "" {
		return "", nil
	}
	if err := validateBoundedText(value, max, false); err != nil {
		return "", err
	}
	return value, nil
}

func validateBoundedText(value string, max int, emptyOK bool) error {
	if !utf8.ValidString(value) {
		return errors.New("invalid UTF-8")
	}
	if value == "" && emptyOK {
		return nil
	}
	if value == "" || len([]byte(value)) > max {
		return ErrLimit
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return errors.New("control character")
		}
	}
	return nil
}

func firstInt(sources []map[string]json.RawMessage, keys ...string) *int64 {
	for _, source := range sources {
		for _, key := range keys {
			if raw, ok := source[key]; ok {
				var value int64
				if json.Unmarshal(raw, &value) == nil {
					delete(source, key)
					return &value
				}
			}
		}
	}
	return nil
}

func firstFloat(sources []map[string]json.RawMessage, keys ...string) *float64 {
	for _, source := range sources {
		for _, key := range keys {
			if raw, ok := source[key]; ok {
				var value float64
				if json.Unmarshal(raw, &value) == nil && !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 {
					delete(source, key)
					return &value
				}
			}
		}
	}
	return nil
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// parseObject validates raw exactly as ValidateJSON does and, in the same
// token pass, returns its top-level members when it is an object. Each value
// is the member's own bytes within raw.
func parseObject(raw []byte, maxDepth int) (map[string]json.RawMessage, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("invalid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	object := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, errors.New("invalid JSON object")
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("invalid JSON object key")
		}
		if _, duplicate := object[key]; duplicate {
			return nil, errors.New("duplicate JSON key")
		}
		// The offset after the key precedes the colon; the offset after
		// the value is its last byte.
		start := decoder.InputOffset()
		if err := validateValue(decoder, 1, maxDepth); err != nil {
			return nil, err
		}
		object[key] = bytes.Trim(raw[start:decoder.InputOffset()], " \t\r\n:")
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return nil, errors.New("invalid JSON object")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("trailing JSON data")
	}
	return object, nil
}

func compactJSON(raw []byte) string {
	var output bytes.Buffer
	if json.Compact(&output, raw) == nil {
		return output.String()
	}
	return string(raw)
}

// ValidateJSON rejects malformed JSON, duplicate object keys at every level,
// trailing values, invalid UTF-8, and nesting deeper than maxDepth.
func ValidateJSON(raw []byte, maxDepth int) error {
	if !utf8.Valid(raw) {
		return errors.New("invalid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := validateValue(decoder, 0, maxDepth); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}

func validateValue(decoder *json.Decoder, depth, maxDepth int) error {
	if depth > maxDepth {
		return errors.New("JSON nesting limit exceeded")
	}
	token, err := decoder.Token()
	if err != nil {
		return errors.New("invalid JSON")
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return errors.New("invalid JSON object")
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate JSON key")
			}
			seen[key] = struct{}{}
			if err := validateValue(decoder, depth+1, maxDepth); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
	case '[':
		for decoder.More() {
			if err := validateValue(decoder, depth+1, maxDepth); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("invalid JSON array")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}

func (e CanonicalEvent) RetainedBytes() int64 {
	total := len(e.MessageRaw) + len(e.MessageText) + len(e.Attributes) +
		len(e.OriginalLevel) + len(e.SourceEventID) + len(e.Source.Project) +
		len(e.Source.Environment) + len(e.Source.Application) + len(e.Source.Service) +
		len(e.Common.Logger) + len(e.Common.RequestID) + len(e.Common.ErrorType) +
		len(e.Common.HTTPMethod) + len(e.Common.HTTPPath)
	if e.Container != nil {
		total += len(e.Container.ID) + len(e.Container.Name)
	}
	return int64(total)
}

// CanonicalEqual compares the content used by stable source-event-ID
// idempotency (DOMAIN.md §8.1, ADR 0007). Receive time and database IDs are
// excluded. Event time is compared only when neither side defaulted it to
// receive time, because a defaulted time is Siftail's observation, not
// producer content.
func CanonicalEqual(a, b CanonicalEvent) bool {
	eventTimeEqual := a.EventAtDefaulted || b.EventAtDefaulted || a.EventAtUS == b.EventAtUS
	return eventTimeEqual &&
		sourceIdentityEqual(a.Source, b.Source) &&
		containerEqual(a.Container, b.Container) &&
		a.Stream == b.Stream && a.Level == b.Level &&
		a.OriginalLevel == b.OriginalLevel &&
		bytes.Equal(a.MessageRaw, b.MessageRaw) &&
		a.MessageText == b.MessageText &&
		bytes.Equal(a.Attributes, b.Attributes) &&
		a.SourceEventID == b.SourceEventID &&
		commonEqual(a.Common, b.Common)
}

func sourceIdentityEqual(a, b SourceIdentity) bool {
	return a.ServerID == b.ServerID &&
		a.Project == b.Project &&
		a.Environment == b.Environment &&
		a.Application == b.Application &&
		a.Service == b.Service
}

// containerEqual compares container identity (ADR 0006, ADR 0007): by
// container ID when both carry one, by name only when neither does. The name of
// an instance with an ID is a mutable label, so a rename is not a difference;
// one side with an ID and the other without is.
func containerEqual(a, b *ContainerIdentity) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.ID != "" || b.ID != "" {
		return a.ID == b.ID
	}
	return a.Name == b.Name
}

func commonEqual(a, b CommonFields) bool {
	if a.Logger != b.Logger || a.RequestID != b.RequestID ||
		a.ErrorType != b.ErrorType || a.HTTPMethod != b.HTTPMethod ||
		a.HTTPPath != b.HTTPPath {
		return false
	}
	return pointerEqual(a.HTTPStatus, b.HTTPStatus) && pointerEqual(a.DurationMS, b.DurationMS)
}

func pointerEqual[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
