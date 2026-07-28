package logs

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DefaultHistoryLimit = 200
	MaxHistoryLimit     = 500
	MaxHistoryRange     = 31 * 24 * time.Hour
	MaxTextFilterBytes  = 512
)

type Direction string

const (
	DirectionOlder Direction = "older"
	DirectionNewer Direction = "newer"
)

type HistoryQuery struct {
	FromUS      int64
	ToUS        int64
	ServerID    int64
	Project     string
	Environment string
	Application string
	Service     string
	ContainerID int64
	Levels      []Level
	Streams     []Stream
	Contains    string
	Excludes    string
	RequestID   string
	Logger      string
	HTTPMethod  string
	HTTPStatus  *int64
	ErrorType   string
	Direction   Direction
	Limit       int
	Cursor      string
}

var historyParameters = map[string]struct{}{
	"mode": {}, "preset": {}, "from": {}, "to": {}, "server": {},
	"project": {}, "environment": {}, "application": {}, "service": {},
	"container": {}, "levels": {}, "streams": {}, "contains": {},
	"excludes": {}, "request_id": {}, "logger": {}, "http_method": {},
	"http_status": {}, "error_type": {}, "direction": {}, "limit": {},
	"cursor": {},
}

func ParseHistoryQuery(values url.Values, now time.Time) (HistoryQuery, error) {
	for key, entries := range values {
		if _, ok := historyParameters[key]; !ok {
			return HistoryQuery{}, fmt.Errorf("unknown history parameter %q", key)
		}
		if len(entries) != 1 {
			return HistoryQuery{}, fmt.Errorf("history parameter %q must occur once", key)
		}
	}
	if mode := values.Get("mode"); mode != "" && mode != "history" {
		return HistoryQuery{}, errors.New("mode must be history")
	}

	query := HistoryQuery{Direction: DirectionOlder, Limit: DefaultHistoryLimit}
	if err := parseHistoryRange(values, now, &query); err != nil {
		return HistoryQuery{}, err
	}
	var err error
	if query.ServerID, err = positiveOptionalInt(values.Get("server"), "server"); err != nil {
		return HistoryQuery{}, err
	}
	if query.ContainerID, err = positiveOptionalInt(values.Get("container"), "container"); err != nil {
		return HistoryQuery{}, err
	}
	for name, target := range map[string]*string{
		"project": &query.Project, "environment": &query.Environment,
		"application": &query.Application, "service": &query.Service,
	} {
		*target = values.Get(name)
		if err := validateQueryText(*target, 128, true); err != nil {
			return HistoryQuery{}, fmt.Errorf("%s: %w", name, err)
		}
	}
	query.Levels, err = parseLevels(values.Get("levels"))
	if err != nil {
		return HistoryQuery{}, err
	}
	query.Streams, err = parseStreams(values.Get("streams"))
	if err != nil {
		return HistoryQuery{}, err
	}
	query.Contains = values.Get("contains")
	query.Excludes = values.Get("excludes")
	for name, value := range map[string]string{
		"contains": query.Contains, "excludes": query.Excludes,
	} {
		if !utf8.ValidString(value) || len([]byte(value)) > MaxTextFilterBytes ||
			strings.ContainsRune(value, 0) {
			return HistoryQuery{}, fmt.Errorf("%s must be valid UTF-8 of at most %d bytes", name, MaxTextFilterBytes)
		}
	}
	for name, target := range map[string]*string{
		"request_id": &query.RequestID, "logger": &query.Logger,
		"http_method": &query.HTTPMethod, "error_type": &query.ErrorType,
	} {
		*target = values.Get(name)
		limit := 255
		if name == "http_method" {
			limit = 32
		}
		if err := validateQueryText(*target, limit, true); err != nil {
			return HistoryQuery{}, fmt.Errorf("%s: %w", name, err)
		}
	}
	if status := values.Get("http_status"); status != "" {
		parsed, err := strconv.ParseInt(status, 10, 64)
		if err != nil || parsed < 100 || parsed > 999 {
			return HistoryQuery{}, errors.New("http_status must be between 100 and 999")
		}
		query.HTTPStatus = &parsed
	}
	if direction := values.Get("direction"); direction != "" {
		query.Direction = Direction(direction)
	}
	if query.Direction != DirectionOlder && query.Direction != DirectionNewer {
		return HistoryQuery{}, errors.New("direction must be older or newer")
	}
	if limit := values.Get("limit"); limit != "" {
		parsed, err := strconv.Atoi(limit)
		if err != nil || parsed < 1 || parsed > MaxHistoryLimit {
			return HistoryQuery{}, fmt.Errorf("limit must be between 1 and %d", MaxHistoryLimit)
		}
		query.Limit = parsed
	}
	query.Cursor = values.Get("cursor")
	if len(query.Cursor) > 1024 || strings.ContainsAny(query.Cursor, "\r\n") {
		return HistoryQuery{}, errors.New("cursor is invalid")
	}
	return query, nil
}

func parseHistoryRange(values url.Values, now time.Time, query *HistoryQuery) error {
	preset := values.Get("preset")
	fromValue, toValue := values.Get("from"), values.Get("to")
	if preset != "" && (fromValue != "" || toValue != "") {
		return errors.New("preset cannot be combined with from or to")
	}
	now = now.UTC()
	if preset != "" {
		durations := map[string]time.Duration{
			"15m": 15 * time.Minute, "1h": time.Hour, "6h": 6 * time.Hour,
			"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour,
		}
		duration, ok := durations[preset]
		if !ok {
			return errors.New("preset must be 15m, 1h, 6h, 24h, or 7d")
		}
		query.ToUS = now.UnixMicro()
		query.FromUS = now.Add(-duration).UnixMicro()
		return nil
	}
	if fromValue == "" && toValue == "" {
		query.ToUS = now.UnixMicro()
		query.FromUS = now.Add(-time.Hour).UnixMicro()
		return nil
	}
	if fromValue == "" || toValue == "" {
		return errors.New("from and to must be provided together")
	}
	from, err := parseUTCTime(fromValue)
	if err != nil {
		return fmt.Errorf("from: %w", err)
	}
	to, err := parseUTCTime(toValue)
	if err != nil {
		return fmt.Errorf("to: %w", err)
	}
	if !from.Before(to) {
		return errors.New("from must be earlier than to")
	}
	if to.Sub(from) > MaxHistoryRange {
		return errors.New("history range cannot exceed 31 days")
	}
	query.FromUS, query.ToUS = from.UnixMicro(), to.UnixMicro()
	return nil
}

func parseUTCTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, errors.New("must be an absolute RFC3339 timestamp")
	}
	_, offset := parsed.Zone()
	if offset != 0 {
		return time.Time{}, errors.New("must use UTC")
	}
	return parsed.UTC(), nil
}

func positiveOptionalInt(value, name string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return parsed, nil
}

func parseLevels(value string) ([]Level, error) {
	if value == "" {
		return nil, nil
	}
	order := map[Level]int{
		LevelTrace: 0, LevelDebug: 1, LevelInfo: 2, LevelWarn: 3,
		LevelError: 4, LevelFatal: 5, LevelUnknown: 6,
	}
	seen := make(map[Level]struct{})
	for _, item := range strings.Split(value, ",") {
		level := Level(item)
		if _, ok := order[level]; !ok || item == "" {
			return nil, errors.New("levels contains an invalid level")
		}
		seen[level] = struct{}{}
	}
	levels := make([]Level, 0, len(seen))
	for level := range seen {
		levels = append(levels, level)
	}
	sort.Slice(levels, func(i, j int) bool { return order[levels[i]] < order[levels[j]] })
	return levels, nil
}

func parseStreams(value string) ([]Stream, error) {
	if value == "" {
		return nil, nil
	}
	order := map[Stream]int{StreamStdout: 0, StreamStderr: 1, StreamUnknown: 2}
	seen := make(map[Stream]struct{})
	for _, item := range strings.Split(value, ",") {
		stream := Stream(item)
		if _, ok := order[stream]; !ok || item == "" {
			return nil, errors.New("streams contains an invalid stream")
		}
		seen[stream] = struct{}{}
	}
	streams := make([]Stream, 0, len(seen))
	for stream := range seen {
		streams = append(streams, stream)
	}
	sort.Slice(streams, func(i, j int) bool { return order[streams[i]] < order[streams[j]] })
	return streams, nil
}

func validateQueryText(value string, max int, controls bool) error {
	if value == "" {
		return nil
	}
	if !utf8.ValidString(value) || len([]byte(value)) > max {
		return fmt.Errorf("must be valid UTF-8 of at most %d bytes", max)
	}
	if controls {
		for _, char := range value {
			if char < 0x20 || char == 0x7f {
				return errors.New("control characters are not allowed")
			}
		}
	}
	return nil
}

func (q HistoryQuery) CanonicalValues(includeCursor bool) url.Values {
	values := url.Values{
		"mode":      {"history"},
		"from":      {time.UnixMicro(q.FromUS).UTC().Format(time.RFC3339Nano)},
		"to":        {time.UnixMicro(q.ToUS).UTC().Format(time.RFC3339Nano)},
		"direction": {string(q.Direction)},
		"limit":     {strconv.Itoa(q.Limit)},
	}
	addInt := func(name string, value int64) {
		if value > 0 {
			values.Set(name, strconv.FormatInt(value, 10))
		}
	}
	addText := func(name, value string) {
		if value != "" {
			values.Set(name, value)
		}
	}
	addInt("server", q.ServerID)
	addText("project", q.Project)
	addText("environment", q.Environment)
	addText("application", q.Application)
	addText("service", q.Service)
	addInt("container", q.ContainerID)
	if len(q.Levels) > 0 {
		items := make([]string, len(q.Levels))
		for index, level := range q.Levels {
			items[index] = string(level)
		}
		values.Set("levels", strings.Join(items, ","))
	}
	if len(q.Streams) > 0 {
		items := make([]string, len(q.Streams))
		for index, stream := range q.Streams {
			items[index] = string(stream)
		}
		values.Set("streams", strings.Join(items, ","))
	}
	addText("contains", q.Contains)
	addText("excludes", q.Excludes)
	addText("request_id", q.RequestID)
	addText("logger", q.Logger)
	addText("http_method", q.HTTPMethod)
	if q.HTTPStatus != nil {
		values.Set("http_status", strconv.FormatInt(*q.HTTPStatus, 10))
	}
	addText("error_type", q.ErrorType)
	if includeCursor && q.Cursor != "" {
		values.Set("cursor", q.Cursor)
	}
	return values
}

func (q HistoryQuery) CanonicalQuery() string {
	return q.CanonicalValues(true).Encode()
}
