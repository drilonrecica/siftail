package logs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const MaxCatalogOptions = 10_000

var ErrCatalogTooLarge = errors.New("historical source catalog exceeds its bounded limit")

type HistoryEvent struct {
	ID                  int64
	EventAtUS           int64
	ReceivedAtUS        int64
	SourceID            int64
	ServerID            int64
	ServerName          string
	ProjectKey          string
	EnvironmentKey      string
	ApplicationKey      string
	ServiceKey          string
	ProjectLabel        string
	EnvironmentLabel    string
	ApplicationLabel    string
	ServiceLabel        string
	SourceAlias         *string
	ContainerInstanceID *int64
	ContainerID         *string
	ContainerName       *string
	Stream              Stream
	Level               Level
	OriginalLevel       *string
	MessageRaw          []byte
	MessageText         string
	MessageBytes        int64
	AttributesJSON      []byte
	SourceEventID       *string
	Logger              *string
	RequestID           *string
	ErrorType           *string
	HTTPMethod          *string
	HTTPPath            *string
	HTTPStatus          *int64
	DurationMS          *float64
}

type HistoryPage struct {
	Events     []HistoryEvent
	NextCursor string
	HasMore    bool
}

type SourceScope struct {
	ServerID    int64
	Project     string
	Environment string
	Application string
	Service     string
}

type ServerOption struct {
	ID   int64
	Name string
}

type SourceOption struct {
	Value string
	Label string
}

type ContainerOption struct {
	ID       int64
	SourceID int64
	Value    string
	Label    string
}

type SourceCatalog struct {
	Servers      []ServerOption
	Projects     []SourceOption
	Environments []SourceOption
	Applications []SourceOption
	Services     []SourceOption
	Containers   []ContainerOption
}

type LiveSourceOption struct {
	ID    int64
	Label string
}

const historyEventColumns = `SELECT
	e.id, e.event_at_us, e.received_at_us,
	e.source_id, s.server_id, server.name,
	s.project_key, s.environment_key, s.application_key, s.service_key,
	s.project_label, s.environment_label, s.application_label, s.service_label, s.alias,
	e.container_instance_id, container.container_id, container.container_name,
	e.stream, e.level_normalized, e.level_original,
	e.message_raw, e.message_text, e.attributes_json, e.source_event_id,
	e.logger, e.request_id, e.error_type, e.http_method, e.http_path,
	e.http_status, e.duration_ms
	FROM log_events AS e
	JOIN sources AS s ON s.id = e.source_id
	JOIN servers AS server ON server.id = s.server_id
	LEFT JOIN container_instances AS container ON container.id = e.container_instance_id`

const historyPageColumns = `SELECT
	e.id, e.event_at_us, e.received_at_us,
	e.source_id, s.server_id, server.name,
	s.project_key, s.environment_key, s.application_key, s.service_key,
	s.project_label, s.environment_label, s.application_label, s.service_label, s.alias,
	e.container_instance_id, container.container_id, container.container_name,
	e.stream, e.level_normalized,
	substr(e.message_text, 1, 2048), length(cast(e.message_text AS BLOB))
	FROM log_events AS e
	JOIN sources AS s ON s.id = e.source_id
	JOIN servers AS server ON server.id = s.server_id
	LEFT JOIN container_instances AS container ON container.id = e.container_instance_id`

// NewHistoryStore adds authenticated cursor support to the read-only log
// store. The ordinary NewStore constructor remains suitable for bounded
// cursor-free verification reads.
func NewHistoryStore(db *sql.DB, codec *CursorCodec) *Store {
	return &Store{db: db, cursorCodec: codec}
}

// History returns one bounded page in canonical descending order. It fetches
// limit+1 rows to detect continuation and never performs COUNT(*).
func (s *Store) History(ctx context.Context, query HistoryQuery) (HistoryPage, error) {
	if s == nil || s.db == nil {
		return HistoryPage{}, errors.New("history store is unavailable")
	}
	normalized, err := ParseHistoryQuery(query.CanonicalValues(true), unixMicroTime(query.ToUS))
	if err != nil {
		return HistoryPage{}, fmt.Errorf("invalid history query: %w", err)
	}
	query = normalized

	var cursor *HistoryCursor
	if query.Cursor != "" {
		if s.cursorCodec == nil {
			return HistoryPage{}, ErrInvalidHistoryCursor
		}
		decoded, err := s.cursorCodec.Decode(query, query.Cursor)
		if err != nil {
			return HistoryPage{}, err
		}
		cursor = &decoded
	}
	statement, arguments := buildHistorySQL(query, cursor)
	rows, err := s.db.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return HistoryPage{}, historyReadError(ctx, "query historical events", err)
	}
	defer rows.Close()

	events := make([]HistoryEvent, 0, query.Limit+1)
	for rows.Next() {
		event, err := scanHistoryPageEvent(rows)
		if err != nil {
			return HistoryPage{}, fmt.Errorf("scan historical event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return HistoryPage{}, historyReadError(ctx, "query historical events", err)
	}
	hasMore := len(events) > query.Limit
	if hasMore {
		events = events[:query.Limit]
	}
	if cursor != nil && query.Direction == DirectionNewer {
		slices.Reverse(events)
	}
	page := HistoryPage{Events: events, HasMore: hasMore}
	if hasMore {
		if s.cursorCodec == nil {
			return HistoryPage{}, errors.New("history cursor codec is unavailable")
		}
		boundary := events[len(events)-1]
		if query.Direction == DirectionNewer {
			boundary = events[0]
		}
		page.NextCursor, err = s.cursorCodec.Encode(query, boundary.EventAtUS, boundary.ID)
		if err != nil {
			return HistoryPage{}, fmt.Errorf("encode history cursor: %w", err)
		}
	}
	return page, nil
}

func buildHistorySQL(query HistoryQuery, cursor *HistoryCursor) (string, []any) {
	filter, arguments := buildHistoryFilterSQL(query, cursor)
	order := "DESC"
	if cursor != nil && query.Direction == DirectionNewer {
		order = "ASC"
	}
	arguments = append(arguments, query.Limit+1)
	return historyPageColumns + "\nWHERE " + filter +
		"\nORDER BY e.event_at_us " + order + ", e.id " + order +
		"\nLIMIT ?", arguments
}

func buildHistoryFilterSQL(
	query HistoryQuery,
	cursor *HistoryCursor,
) (string, []any) {
	clauses := []string{"e.event_at_us >= ?", "e.event_at_us < ?"}
	arguments := []any{query.FromUS, query.ToUS}
	addExact := func(column string, value any, present bool) {
		if present {
			clauses = append(clauses, column+" = ?")
			arguments = append(arguments, value)
		}
	}
	addExact("s.server_id", query.ServerID, query.ServerID > 0)
	addExact("s.project_key", query.Project, query.Project != "")
	addExact("s.environment_key", query.Environment, query.Environment != "")
	addExact("s.application_key", query.Application, query.Application != "")
	addExact("s.service_key", query.Service, query.Service != "")
	addExact("e.container_instance_id", query.ContainerID, query.ContainerID > 0)
	addSet := func(column string, values []string) {
		if len(values) == 0 {
			return
		}
		placeholders := make([]string, len(values))
		for index, value := range values {
			placeholders[index] = "?"
			arguments = append(arguments, value)
		}
		clauses = append(clauses, column+" IN ("+strings.Join(placeholders, ",")+")")
	}
	levels := make([]string, len(query.Levels))
	for index, level := range query.Levels {
		levels[index] = string(level)
	}
	addSet("e.level_normalized", levels)
	streams := make([]string, len(query.Streams))
	for index, stream := range query.Streams {
		streams[index] = string(stream)
	}
	addSet("e.stream", streams)
	if query.Contains != "" {
		clauses = append(clauses, "instr(lower(e.message_text), lower(?)) > 0")
		arguments = append(arguments, query.Contains)
	}
	if query.Excludes != "" {
		clauses = append(clauses, "instr(lower(e.message_text), lower(?)) = 0")
		arguments = append(arguments, query.Excludes)
	}
	addExact("e.request_id", query.RequestID, query.RequestID != "")
	addExact("e.logger", query.Logger, query.Logger != "")
	addExact("e.http_method", query.HTTPMethod, query.HTTPMethod != "")
	if query.HTTPStatus != nil {
		addExact("e.http_status", *query.HTTPStatus, true)
	}
	addExact("e.error_type", query.ErrorType, query.ErrorType != "")
	if cursor != nil {
		operator := "<"
		if query.Direction == DirectionNewer {
			operator = ">"
		}
		clauses = append(clauses,
			"(e.event_at_us "+operator+" ? OR (e.event_at_us = ? AND e.id "+operator+" ?))",
		)
		arguments = append(arguments, cursor.EventAtUS, cursor.EventAtUS, cursor.ID)
	}
	return strings.Join(clauses, "\nAND "), arguments
}

func scanHistoryPageEvent(row rowScanner) (HistoryEvent, error) {
	var event HistoryEvent
	var alias, containerID, containerName sql.NullString
	var containerInstanceID sql.NullInt64
	if err := row.Scan(
		&event.ID, &event.EventAtUS, &event.ReceivedAtUS,
		&event.SourceID, &event.ServerID, &event.ServerName,
		&event.ProjectKey, &event.EnvironmentKey, &event.ApplicationKey, &event.ServiceKey,
		&event.ProjectLabel, &event.EnvironmentLabel, &event.ApplicationLabel, &event.ServiceLabel, &alias,
		&containerInstanceID, &containerID, &containerName,
		&event.Stream, &event.Level, &event.MessageText, &event.MessageBytes,
	); err != nil {
		return HistoryEvent{}, err
	}
	event.SourceAlias = nullableString(alias)
	event.ContainerInstanceID = nullableInt64(containerInstanceID)
	event.ContainerID = nullableString(containerID)
	event.ContainerName = nullableString(containerName)
	return event, nil
}

// Event returns one retained event with its complete bounded payload and source
// context. Deletion and an unknown ID have the same result.
func (s *Store) Event(ctx context.Context, id int64) (HistoryEvent, error) {
	if s == nil || s.db == nil || id <= 0 {
		return HistoryEvent{}, sql.ErrNoRows
	}
	event, err := scanHistoryEvent(s.db.QueryRowContext(ctx,
		historyEventColumns+"\nWHERE e.id = ?", id,
	))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return HistoryEvent{}, sql.ErrNoRows
		}
		return HistoryEvent{}, historyReadError(ctx, "get historical event", err)
	}
	return event, nil
}

// Catalog returns bounded cascading source and container options. Sources are
// intentionally not filtered by last-seen time, so inactive historical
// sources remain selectable while retained metadata exists.
func (s *Store) Catalog(ctx context.Context, scope SourceScope) (SourceCatalog, error) {
	if s == nil || s.db == nil {
		return SourceCatalog{}, errors.New("history store is unavailable")
	}
	if scope.ServerID < 0 {
		return SourceCatalog{}, errors.New("catalog server must be nonnegative")
	}
	for name, value := range map[string]string{
		"project": scope.Project, "environment": scope.Environment,
		"application": scope.Application, "service": scope.Service,
	} {
		if err := validateQueryText(value, 128, true); err != nil {
			return SourceCatalog{}, fmt.Errorf("catalog %s: %w", name, err)
		}
	}

	var catalog SourceCatalog
	serverRows, err := s.db.QueryContext(ctx, `SELECT id, name
		FROM servers
		ORDER BY name COLLATE BINARY, id
		LIMIT ?`, MaxCatalogOptions+1)
	if err != nil {
		return SourceCatalog{}, historyReadError(ctx, "list history servers", err)
	}
	for serverRows.Next() {
		var option ServerOption
		if err := serverRows.Scan(&option.ID, &option.Name); err != nil {
			serverRows.Close()
			return SourceCatalog{}, fmt.Errorf("scan history server option: %w", err)
		}
		catalog.Servers = append(catalog.Servers, option)
	}
	if err := serverRows.Err(); err != nil {
		serverRows.Close()
		return SourceCatalog{}, historyReadError(ctx, "list history servers", err)
	}
	serverRows.Close()
	if len(catalog.Servers) > MaxCatalogOptions {
		return SourceCatalog{}, ErrCatalogTooLarge
	}
	if catalog.Projects, err = s.catalogSourceOptions(ctx, scope, 0); err != nil {
		return SourceCatalog{}, err
	}
	if catalog.Environments, err = s.catalogSourceOptions(ctx, scope, 1); err != nil {
		return SourceCatalog{}, err
	}
	if catalog.Applications, err = s.catalogSourceOptions(ctx, scope, 2); err != nil {
		return SourceCatalog{}, err
	}
	if catalog.Services, err = s.catalogSourceOptions(ctx, scope, 3); err != nil {
		return SourceCatalog{}, err
	}
	if catalog.Containers, err = s.catalogContainerOptions(ctx, scope); err != nil {
		return SourceCatalog{}, err
	}
	return catalog, nil
}

// LiveSources returns the bounded stable-source choices used only by the Live
// workspace, avoiding extra catalog work on every History query.
func (s *Store) LiveSources(ctx context.Context) ([]LiveSourceOption, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("history store is unavailable")
	}
	sourceRows, err := s.db.QueryContext(ctx, `SELECT s.id,
		CASE WHEN s.alias IS NOT NULL
			THEN s.alias || ' · ' || server.name
			ELSE server.name || ' / ' || s.project_label || ' / ' ||
				s.environment_label || ' / ' || s.application_label || ' / ' ||
				s.service_label
		END
		FROM sources AS s
		JOIN servers AS server ON server.id=s.server_id
		ORDER BY 2 COLLATE BINARY, s.id
		LIMIT ?`, MaxCatalogOptions+1)
	if err != nil {
		return nil, historyReadError(ctx, "list Live sources", err)
	}
	options := make([]LiveSourceOption, 0)
	for sourceRows.Next() {
		var option LiveSourceOption
		if err := sourceRows.Scan(&option.ID, &option.Label); err != nil {
			sourceRows.Close()
			return nil, fmt.Errorf("scan Live source option: %w", err)
		}
		options = append(options, option)
	}
	if err := sourceRows.Err(); err != nil {
		sourceRows.Close()
		return nil, historyReadError(ctx, "list Live sources", err)
	}
	sourceRows.Close()
	if len(options) > MaxCatalogOptions {
		return nil, ErrCatalogTooLarge
	}
	return options, nil
}

func (s *Store) catalogSourceOptions(
	ctx context.Context,
	scope SourceScope,
	level int,
) ([]SourceOption, error) {
	keys := []string{"project", "environment", "application", "service"}
	keyColumns := []string{"project_key", "environment_key", "application_key", "service_key"}
	labelColumns := []string{"project_label", "environment_label", "application_label", "service_label"}
	clauses, arguments := catalogScopeClauses(scope, level)
	statement := "SELECT DISTINCT s." + keyColumns[level] + ", s." + labelColumns[level] +
		"\nFROM sources AS s"
	if len(clauses) > 0 {
		statement += "\nWHERE " + strings.Join(clauses, "\nAND ")
	}
	statement += "\nORDER BY s." + labelColumns[level] + " COLLATE BINARY, s." +
		keyColumns[level] + " COLLATE BINARY\nLIMIT ?"
	arguments = append(arguments, MaxCatalogOptions+1)
	rows, err := s.db.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, historyReadError(ctx, "list history "+keys[level]+" options", err)
	}
	defer rows.Close()
	options := make([]SourceOption, 0)
	for rows.Next() {
		var option SourceOption
		if err := rows.Scan(&option.Value, &option.Label); err != nil {
			return nil, fmt.Errorf("scan history %s option: %w", keys[level], err)
		}
		options = append(options, option)
	}
	if err := rows.Err(); err != nil {
		return nil, historyReadError(ctx, "list history "+keys[level]+" options", err)
	}
	if len(options) > MaxCatalogOptions {
		return nil, ErrCatalogTooLarge
	}
	return options, nil
}

func (s *Store) catalogContainerOptions(
	ctx context.Context,
	scope SourceScope,
) ([]ContainerOption, error) {
	clauses, arguments := catalogScopeClauses(scope, 4)
	statement := `SELECT
		container.id, container.source_id,
		coalesce(container.container_id, container.container_name),
		coalesce(container.container_name, container.container_id)
		FROM container_instances AS container
		JOIN sources AS s ON s.id = container.source_id`
	if len(clauses) > 0 {
		statement += "\nWHERE " + strings.Join(clauses, "\nAND ")
	}
	statement += "\nORDER BY 4 COLLATE BINARY, container.id\nLIMIT ?"
	arguments = append(arguments, MaxCatalogOptions+1)
	rows, err := s.db.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, historyReadError(ctx, "list history container options", err)
	}
	defer rows.Close()
	options := make([]ContainerOption, 0)
	for rows.Next() {
		var option ContainerOption
		if err := rows.Scan(&option.ID, &option.SourceID, &option.Value, &option.Label); err != nil {
			return nil, fmt.Errorf("scan history container option: %w", err)
		}
		options = append(options, option)
	}
	if err := rows.Err(); err != nil {
		return nil, historyReadError(ctx, "list history container options", err)
	}
	if len(options) > MaxCatalogOptions {
		return nil, ErrCatalogTooLarge
	}
	return options, nil
}

func catalogScopeClauses(scope SourceScope, depth int) ([]string, []any) {
	var clauses []string
	var arguments []any
	if scope.ServerID > 0 {
		clauses = append(clauses, "s.server_id = ?")
		arguments = append(arguments, scope.ServerID)
	}
	values := []string{scope.Project, scope.Environment, scope.Application, scope.Service}
	columns := []string{"s.project_key", "s.environment_key", "s.application_key", "s.service_key"}
	for index := 0; index < depth && index < len(values); index++ {
		if values[index] != "" {
			clauses = append(clauses, columns[index]+" = ?")
			arguments = append(arguments, values[index])
		}
	}
	return clauses, arguments
}

type rowScanner interface {
	Scan(...any) error
}

func scanHistoryEvent(row rowScanner) (HistoryEvent, error) {
	var event HistoryEvent
	var alias, containerID, containerName sql.NullString
	var containerInstanceID sql.NullInt64
	var originalLevel, attributes, sourceEventID sql.NullString
	var logger, requestID, errorType, httpMethod, httpPath sql.NullString
	var httpStatus sql.NullInt64
	var durationMS sql.NullFloat64
	if err := row.Scan(
		&event.ID, &event.EventAtUS, &event.ReceivedAtUS,
		&event.SourceID, &event.ServerID, &event.ServerName,
		&event.ProjectKey, &event.EnvironmentKey, &event.ApplicationKey, &event.ServiceKey,
		&event.ProjectLabel, &event.EnvironmentLabel, &event.ApplicationLabel, &event.ServiceLabel, &alias,
		&containerInstanceID, &containerID, &containerName,
		&event.Stream, &event.Level, &originalLevel,
		&event.MessageRaw, &event.MessageText, &attributes, &sourceEventID,
		&logger, &requestID, &errorType, &httpMethod, &httpPath,
		&httpStatus, &durationMS,
	); err != nil {
		return HistoryEvent{}, err
	}
	event.SourceAlias = nullableString(alias)
	event.ContainerInstanceID = nullableInt64(containerInstanceID)
	event.ContainerID = nullableString(containerID)
	event.ContainerName = nullableString(containerName)
	event.OriginalLevel = nullableString(originalLevel)
	if attributes.Valid {
		event.AttributesJSON = []byte(attributes.String)
	}
	event.SourceEventID = nullableString(sourceEventID)
	event.Logger = nullableString(logger)
	event.RequestID = nullableString(requestID)
	event.ErrorType = nullableString(errorType)
	event.HTTPMethod = nullableString(httpMethod)
	event.HTTPPath = nullableString(httpPath)
	event.HTTPStatus = nullableInt64(httpStatus)
	event.DurationMS = nullableFloat64(durationMS)
	return event, nil
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func nullableInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

func nullableFloat64(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	return &value.Float64
}

func historyReadError(ctx context.Context, operation string, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return fmt.Errorf("%s: %w", operation, contextErr)
	}
	return fmt.Errorf("%s: database read failed: %w", operation, err)
}

func unixMicroTime(value int64) (now time.Time) {
	return time.UnixMicro(value).UTC()
}
