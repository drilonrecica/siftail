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
	Tag       string
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
	EventAtUS     int64
	ReceivedAtUS  int64
	Source        SourceIdentity
	Container     *ContainerIdentity
	Stream        Stream
	Level         Level
	OriginalLevel string
	MessageRaw    []byte
	MessageText   string
	Attributes    []byte
	SourceEventID string
	Common        CommonFields
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
	if !utf8.Valid(record.Raw) {
		return CanonicalEvent{}, fmt.Errorf("record is not valid UTF-8")
	}
	if len(record.Raw) > 0 {
		if err := ValidateJSON(record.Raw, maxJSONDepth); err != nil {
			return CanonicalEvent{}, err
		}
	}

	fields := cloneFields(record.Fields)
	eventAt, err := normalizeTimestamp(fields, record.Timestamp, receivedAt)
	if err != nil {
		return CanonicalEvent{}, err
	}
	source, container, err := normalizeSource(fields, server.ID)
	if err != nil {
		return CanonicalEvent{}, err
	}
	messageRaw, messageText, structured, err := normalizeMessage(fields, record.Raw)
	if err != nil {
		return CanonicalEvent{}, err
	}
	originalLevel, level, err := normalizeLevel(fields, structured, messageText)
	if err != nil {
		return CanonicalEvent{}, err
	}
	stream := normalizeStream(takeString(fields, "stream"))
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
		EventAtUS:     eventAt,
		ReceivedAtUS:  receivedAt.UnixMicro(),
		Source:        source,
		Container:     container,
		Stream:        stream,
		Level:         level,
		OriginalLevel: originalLevel,
		MessageRaw:    messageRaw,
		MessageText:   messageText,
		Attributes:    attributes,
		SourceEventID: eventID,
		Common:        common,
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

func normalizeTimestamp(fields map[string]json.RawMessage, fluent json.RawMessage, received time.Time) (int64, error) {
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
		return received.UnixMicro(), nil
	}
	timestamp, err := parseTimestamp(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid supplied timestamp")
	}
	return timestamp.UnixMicro(), nil
}

func parseTimestamp(raw json.RawMessage) (time.Time, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
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
	project := firstString(fields, "coolify_project_name", "project_name", "com.docker.compose.project", "project")
	environment := firstString(fields, "coolify_environment_name", "environment_name", "environment", "env")
	application := firstString(fields, "coolify_application_name", "application_name", "application", "app")
	service := firstString(fields, "coolify_service_name", "service_name", "com.docker.compose.service", "service")
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

func normalizeMessage(fields map[string]json.RawMessage, recordRaw []byte) ([]byte, string, map[string]json.RawMessage, error) {
	var raw json.RawMessage
	for _, key := range []string{"log", "message", "msg"} {
		if value, ok := fields[key]; ok {
			raw = value
			delete(fields, key)
			break
		}
	}
	if len(raw) == 0 {
		raw = recordRaw
	}
	if len(raw) == 0 {
		raw = []byte("{}")
	}

	var text string
	if json.Unmarshal(raw, &text) == nil {
		if !utf8.ValidString(text) || len([]byte(text)) > maxMessageBytes {
			return nil, "", nil, ErrLimit
		}
		if object, err := decodeObject([]byte(text)); err == nil {
			message := firstString(object, "message", "msg", "log")
			if message == "" {
				message = compactJSON([]byte(text))
			}
			return []byte(text), message, object, nil
		}
		return []byte(text), text, nil, nil
	}

	if err := ValidateJSON(raw, maxJSONDepth); err != nil {
		return nil, "", nil, err
	}
	if len(raw) > maxMessageBytes {
		return nil, "", nil, ErrLimit
	}
	object, err := decodeObject(raw)
	if err != nil {
		return append([]byte(nil), raw...), compactJSON(raw), nil, nil
	}
	message := firstString(object, "message", "msg", "log")
	if message == "" {
		message = compactJSON(raw)
	}
	return append([]byte(nil), raw...), message, object, nil
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

func isTransportField(key string) bool {
	switch key {
	case "date", "timestamp", "@timestamp", "time", "tag", "stream",
		"source_event_id", "container_id", "container.id",
		"container_name", "container.name", "project", "project_name",
		"coolify_project_name", "environment", "environment_name", "env",
		"coolify_environment_name", "application", "application_name", "app",
		"coolify_application_name", "service", "service_name",
		"coolify_service_name", "com.docker.compose.project",
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
			var value string
			if json.Unmarshal(raw, &value) == nil {
				delete(fields, key)
				return value
			}
		}
	}
	return ""
}

func takeString(fields map[string]json.RawMessage, key string) string {
	return firstString(fields, key)
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

func decodeObject(raw []byte) (map[string]json.RawMessage, error) {
	if err := ValidateJSON(raw, maxJSONDepth); err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, errors.New("not an object")
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
// idempotency. Receive time and future database IDs are intentionally excluded.
func CanonicalEqual(a, b CanonicalEvent) bool {
	a.ReceivedAtUS, b.ReceivedAtUS = 0, 0
	return a.EventAtUS == b.EventAtUS &&
		a.Source == b.Source &&
		containerEqual(a.Container, b.Container) &&
		a.Stream == b.Stream && a.Level == b.Level &&
		a.OriginalLevel == b.OriginalLevel &&
		bytes.Equal(a.MessageRaw, b.MessageRaw) &&
		a.MessageText == b.MessageText &&
		bytes.Equal(a.Attributes, b.Attributes) &&
		a.SourceEventID == b.SourceEventID &&
		commonEqual(a.Common, b.Common)
}

func containerEqual(a, b *ContainerIdentity) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
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
