// Package audit owns immutable, bounded security audit storage.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/drilonrecica/siftail/internal/database"
)

const (
	MaxRecords           = 100_000
	DefaultRetentionDays = 365
	MaximumRetentionDays = 365
	DefaultPageLimit     = 100
	MaxPageLimit         = 200
	MaxCleanupChunk      = 1_000
	maxMetadataFields    = 12
	maxMetadataValue     = 256
)

var (
	ErrInvalidEvent      = errors.New("invalid security audit event")
	ErrInvalidQuery      = errors.New("invalid security audit query")
	ErrInvalidCleanup    = errors.New("invalid security audit cleanup")
	ErrCapacityInvariant = errors.New("security audit storage exceeds its repair bound")
)

type Category string

const (
	CategoryAuthentication          Category = "authentication"
	CategorySession                 Category = "session"
	CategoryAdministratorCredential Category = "administrator_credential"
	CategoryIngestionToken          Category = "ingestion_token"
	CategorySourceAdministration    Category = "source_administration"
	CategoryRetentionSettings       Category = "retention_settings"
	CategoryBackupRestore           Category = "backup_restore"
	CategoryExport                  Category = "export"
	CategoryProxyAuthConfiguration  Category = "proxy_auth_configuration"
	CategoryDestructiveOperation    Category = "destructive_operation"
)

type Outcome string

const (
	OutcomeSucceeded Outcome = "succeeded"
	OutcomeFailed    Outcome = "failed"
	OutcomeRejected  Outcome = "rejected"
	OutcomeCanceled  Outcome = "canceled"
)

type ActorType string

const (
	ActorAdministrator   ActorType = "administrator"
	ActorUnauthenticated ActorType = "unauthenticated"
	ActorLocalOperator   ActorType = "local_operator"
	ActorSystem          ActorType = "system"
)

const (
	MetadataActorName          = "actor_name"
	MetadataClientAddress      = "client_address"
	MetadataServerName         = "server_name"
	MetadataSourceName         = "source_name"
	MetadataTokenName          = "token_name"
	MetadataTokenFingerprint   = "token_fingerprint"
	MetadataAffectedCount      = "affected_count"
	MetadataSessionCount       = "session_count"
	MetadataRetentionAgeDays   = "retention_age_days"
	MetadataMaximumDatabaseGiB = "maximum_database_gib"
	MetadataBackupType         = "backup_type"
	MetadataBackupName         = "backup_name"
	MetadataExportFormat       = "export_format"
	MetadataResultCategory     = "result_category"
	MetadataSettingName        = "setting_name"
	MetadataPreviousValue      = "previous_value"
	MetadataCurrentValue       = "current_value"
)

var allowedMetadata = map[string]struct{}{
	MetadataActorName: {}, MetadataClientAddress: {}, MetadataServerName: {},
	MetadataSourceName: {}, MetadataTokenName: {}, MetadataTokenFingerprint: {},
	MetadataAffectedCount: {}, MetadataSessionCount: {},
	MetadataRetentionAgeDays: {}, MetadataMaximumDatabaseGiB: {},
	MetadataBackupType: {}, MetadataBackupName: {}, MetadataExportFormat: {},
	MetadataResultCategory: {}, MetadataSettingName: {},
	MetadataPreviousValue: {}, MetadataCurrentValue: {},
}

type Metadata map[string]string

type Input struct {
	OccurredAt      time.Time
	Category        Category
	Action          string
	Outcome         Outcome
	ActorType       ActorType
	AdministratorID *int64
	ServerID        *int64
	SourceID        *int64
	Metadata        Metadata
	RequestID       string
}

type Event struct {
	ID              int64
	OccurredAt      time.Time
	Category        Category
	Action          string
	Outcome         Outcome
	ActorType       ActorType
	AdministratorID *int64
	ServerID        *int64
	SourceID        *int64
	Metadata        Metadata
	RequestID       string
}

type Query struct {
	Category           Category
	Action             string
	Outcome            Outcome
	FromOccurredAtUS   int64
	ToOccurredAtUS     int64
	BeforeOccurredAtUS int64
	BeforeID           int64
	Limit              int
}

type Page struct {
	Events                 []Event
	HasMore                bool
	NextBeforeOccurredAtUS int64
	NextBeforeID           int64
}

type CleanupResult struct {
	AgeDeleted int64
	CapDeleted int64
}

type Store struct {
	db      *sql.DB
	mutator database.MutationCoordinator
	now     func() time.Time
}

func NewStore(db *sql.DB, mutator database.MutationCoordinator) *Store {
	return &Store{db: db, mutator: mutator, now: time.Now}
}

func (s *Store) Record(ctx context.Context, input Input) (Event, error) {
	if s == nil || s.mutator == nil {
		return Event{}, errors.New("security audit mutation is unavailable")
	}
	var event Event
	err := s.mutator.Do(ctx, func(tx *sql.Tx) error {
		var err error
		event, err = RecordTx(context.WithoutCancel(ctx), tx, input)
		return err
	})
	if err != nil {
		return Event{}, err
	}
	return event, nil
}

// RecordTx validates and inserts an event in a caller-owned mutation
// transaction. Privileged features use this to make success attribution atomic
// with the action it describes.
func RecordTx(ctx context.Context, tx *sql.Tx, input Input) (Event, error) {
	metadataJSON, err := validateAndEncode(input)
	if err != nil {
		return Event{}, err
	}
	if tx == nil {
		return Event{}, errors.New("security audit transaction is unavailable")
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO security_audit_events(
		occurred_at_us, category, action, outcome, actor_type,
		administrator_id, server_id, source_id, safe_metadata_json, request_id
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, nullif(?, ''))`,
		input.OccurredAt.UTC().UnixMicro(), input.Category, input.Action,
		input.Outcome, input.ActorType, input.AdministratorID, input.ServerID,
		input.SourceID, metadataJSON, input.RequestID)
	if err != nil {
		return Event{}, database.Classify("insert security audit event", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Event{}, database.Classify("read security audit event ID", err)
	}
	if err := enforceCap(ctx, tx); err != nil {
		return Event{}, err
	}
	return eventFromInput(id, input), nil
}

func (s *Store) List(ctx context.Context, query Query) (Page, error) {
	if s == nil || s.db == nil {
		return Page{}, errors.New("security audit storage is unavailable")
	}
	if err := validateQuery(&query); err != nil {
		return Page{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, occurred_at_us, category, action, outcome, actor_type,
		administrator_id, server_id, source_id, safe_metadata_json,
		coalesce(request_id, '')
		FROM security_audit_events
		WHERE (? = '' OR category = ?)
		AND (? = '' OR action = ?)
		AND (? = '' OR outcome = ?)
		AND (? = 0 OR occurred_at_us >= ?)
		AND (? = 0 OR occurred_at_us < ?)
		AND (? = 0 OR occurred_at_us < ?
			OR (occurred_at_us = ? AND id < ?))
		ORDER BY occurred_at_us DESC, id DESC
		LIMIT ?`,
		query.Category, query.Category, query.Action, query.Action,
		query.Outcome, query.Outcome,
		query.FromOccurredAtUS, query.FromOccurredAtUS,
		query.ToOccurredAtUS, query.ToOccurredAtUS,
		query.BeforeOccurredAtUS, query.BeforeOccurredAtUS,
		query.BeforeOccurredAtUS, query.BeforeID, query.Limit+1)
	if err != nil {
		return Page{}, database.Classify("list security audit events", err)
	}
	defer rows.Close()

	page := Page{Events: make([]Event, 0, query.Limit)}
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return Page{}, err
		}
		page.Events = append(page.Events, event)
	}
	if err := rows.Err(); err != nil {
		return Page{}, database.Classify("iterate security audit events", err)
	}
	if len(page.Events) > query.Limit {
		page.HasMore = true
		page.Events = page.Events[:query.Limit]
	}
	if page.HasMore {
		last := page.Events[len(page.Events)-1]
		page.NextBeforeOccurredAtUS = last.OccurredAt.UnixMicro()
		page.NextBeforeID = last.ID
	}
	return page, nil
}

func (s *Store) Cleanup(
	ctx context.Context,
	retentionDays, limit int,
) (CleanupResult, error) {
	if retentionDays < 1 || retentionDays > MaximumRetentionDays ||
		limit < 1 || limit > MaxCleanupChunk {
		return CleanupResult{}, ErrInvalidCleanup
	}
	if s == nil || s.mutator == nil {
		return CleanupResult{}, errors.New("security audit cleanup is unavailable")
	}
	cutoff := s.now().UTC().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	var cleanup CleanupResult
	err := s.mutator.Do(ctx, func(tx *sql.Tx) error {
		mutationCtx := context.WithoutCancel(ctx)
		result, err := tx.ExecContext(mutationCtx, `DELETE FROM security_audit_events
			WHERE id IN (
				SELECT id FROM security_audit_events
				WHERE occurred_at_us < ?
				ORDER BY occurred_at_us ASC, id ASC
				LIMIT ?
			)`, cutoff.UnixMicro(), limit)
		if err != nil {
			return database.Classify("delete expired security audit events", err)
		}
		cleanup.AgeDeleted, err = result.RowsAffected()
		if err != nil {
			return database.Classify("read expired security audit deletion", err)
		}
		remaining := int64(limit) - cleanup.AgeDeleted
		if remaining == 0 {
			return nil
		}
		var count int64
		if err := tx.QueryRowContext(mutationCtx,
			"SELECT count(*) FROM security_audit_events").Scan(&count); err != nil {
			return database.Classify("count security audit events", err)
		}
		excess := count - MaxRecords
		if excess <= 0 {
			return nil
		}
		if excess > remaining {
			excess = remaining
		}
		result, err = tx.ExecContext(mutationCtx, `DELETE FROM security_audit_events
			WHERE id IN (
				SELECT id FROM security_audit_events
				ORDER BY occurred_at_us ASC, id ASC
				LIMIT ?
			)`, excess)
		if err != nil {
			return database.Classify("delete excess security audit events", err)
		}
		cleanup.CapDeleted, err = result.RowsAffected()
		if err != nil {
			return database.Classify("read excess security audit deletion", err)
		}
		return nil
	})
	if err != nil {
		return CleanupResult{}, err
	}
	return cleanup, nil
}

func enforceCap(ctx context.Context, tx *sql.Tx) error {
	var count int
	if err := tx.QueryRowContext(ctx,
		"SELECT count(*) FROM security_audit_events").Scan(&count); err != nil {
		return database.Classify("count security audit capacity", err)
	}
	excess := count - MaxRecords
	if excess <= 0 {
		return nil
	}
	if excess > MaxCleanupChunk {
		return ErrCapacityInvariant
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM security_audit_events
		WHERE id IN (
			SELECT id FROM security_audit_events
			ORDER BY occurred_at_us ASC, id ASC
			LIMIT ?
		)`, excess); err != nil {
		return database.Classify("enforce security audit capacity", err)
	}
	return nil
}

func validateAndEncode(input Input) (string, error) {
	if input.OccurredAt.IsZero() || input.OccurredAt.Year() < 1 ||
		input.OccurredAt.Year() > 9999 || !validCategory(input.Category) ||
		!validAction(input.Action) || !validOutcome(input.Outcome) ||
		!validActor(input.ActorType) || !validOptionalID(input.AdministratorID) ||
		!validOptionalID(input.ServerID) || !validOptionalID(input.SourceID) ||
		!validText(input.RequestID, 128, true) {
		return "", ErrInvalidEvent
	}
	if input.ActorType == ActorAdministrator && input.AdministratorID == nil {
		return "", ErrInvalidEvent
	}
	if input.ActorType != ActorAdministrator && input.AdministratorID != nil {
		return "", ErrInvalidEvent
	}
	if len(input.Metadata) > maxMetadataFields {
		return "", ErrInvalidEvent
	}
	metadata := make(Metadata, len(input.Metadata))
	for key, value := range input.Metadata {
		if _, ok := allowedMetadata[key]; !ok ||
			!validMetadataValue(key, value) {
			return "", ErrInvalidEvent
		}
		metadata[key] = value
	}
	if metadata == nil {
		metadata = Metadata{}
	}
	encoded, err := json.Marshal(metadata)
	if err != nil || len(encoded) > 2048 {
		return "", ErrInvalidEvent
	}
	return string(encoded), nil
}

func validMetadataValue(key, value string) bool {
	if !validText(value, maxMetadataValue, false) {
		return false
	}
	switch key {
	case MetadataAffectedCount, MetadataSessionCount:
		number, err := strconv.ParseUint(value, 10, 64)
		return err == nil && strconv.FormatUint(number, 10) == value
	case MetadataRetentionAgeDays, MetadataMaximumDatabaseGiB:
		number, err := strconv.ParseUint(value, 10, 64)
		return err == nil && number > 0 && strconv.FormatUint(number, 10) == value
	case MetadataTokenFingerprint:
		if len(value) < 8 || len(value) > 32 {
			return false
		}
		for index := range len(value) {
			char := value[index]
			if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
				return false
			}
		}
		return true
	case MetadataBackupName:
		return value != "." && value != ".." &&
			!strings.ContainsAny(value, `/\`)
	case MetadataBackupType:
		return value == "full" || value == "configuration"
	case MetadataExportFormat:
		return value == "text" || value == "ndjson"
	case MetadataResultCategory, MetadataSettingName:
		return validAction(value)
	default:
		return true
	}
}

func validateQuery(query *Query) error {
	if query.Category != "" && !validCategory(query.Category) {
		return ErrInvalidQuery
	}
	if query.Outcome != "" && !validOutcome(query.Outcome) {
		return ErrInvalidQuery
	}
	if query.Action != "" && !validAction(query.Action) {
		return ErrInvalidQuery
	}
	if (query.FromOccurredAtUS == 0) != (query.ToOccurredAtUS == 0) ||
		query.FromOccurredAtUS < 0 || query.ToOccurredAtUS < 0 ||
		(query.FromOccurredAtUS != 0 &&
			(query.FromOccurredAtUS >= query.ToOccurredAtUS ||
				query.ToOccurredAtUS-query.FromOccurredAtUS >
					int64(366*24*time.Hour/time.Microsecond))) {
		return ErrInvalidQuery
	}
	if (query.BeforeOccurredAtUS == 0) != (query.BeforeID == 0) ||
		query.BeforeOccurredAtUS < 0 || query.BeforeID < 0 {
		return ErrInvalidQuery
	}
	if query.Limit == 0 {
		query.Limit = DefaultPageLimit
	}
	if query.Limit < 1 || query.Limit > MaxPageLimit {
		return ErrInvalidQuery
	}
	return nil
}

func scanEvent(rows *sql.Rows) (Event, error) {
	var event Event
	var occurredAtUS int64
	var administratorID, serverID, sourceID sql.NullInt64
	var metadataJSON string
	if err := rows.Scan(
		&event.ID, &occurredAtUS, &event.Category, &event.Action,
		&event.Outcome, &event.ActorType, &administratorID, &serverID,
		&sourceID, &metadataJSON, &event.RequestID,
	); err != nil {
		return Event{}, database.Classify("scan security audit event", err)
	}
	event.OccurredAt = time.UnixMicro(occurredAtUS).UTC()
	event.AdministratorID = optionalID(administratorID)
	event.ServerID = optionalID(serverID)
	event.SourceID = optionalID(sourceID)
	if err := json.Unmarshal([]byte(metadataJSON), &event.Metadata); err != nil {
		return Event{}, errors.New("decode security audit metadata")
	}
	input := Input{
		OccurredAt: event.OccurredAt, Category: event.Category,
		Action: event.Action, Outcome: event.Outcome, ActorType: event.ActorType,
		AdministratorID: event.AdministratorID, ServerID: event.ServerID,
		SourceID: event.SourceID, Metadata: event.Metadata,
		RequestID: event.RequestID,
	}
	if _, err := validateAndEncode(input); err != nil {
		return Event{}, errors.New("stored security audit event is invalid")
	}
	return event, nil
}

func eventFromInput(id int64, input Input) Event {
	metadata := make(Metadata, len(input.Metadata))
	for key, value := range input.Metadata {
		metadata[key] = value
	}
	if metadata == nil {
		metadata = Metadata{}
	}
	return Event{
		ID: id, OccurredAt: input.OccurredAt.UTC(), Category: input.Category,
		Action: input.Action, Outcome: input.Outcome, ActorType: input.ActorType,
		AdministratorID: cloneID(input.AdministratorID),
		ServerID:        cloneID(input.ServerID), SourceID: cloneID(input.SourceID),
		Metadata: metadata, RequestID: input.RequestID,
	}
}

func validCategory(category Category) bool {
	switch category {
	case CategoryAuthentication, CategorySession, CategoryAdministratorCredential,
		CategoryIngestionToken, CategorySourceAdministration,
		CategoryRetentionSettings, CategoryBackupRestore, CategoryExport,
		CategoryProxyAuthConfiguration, CategoryDestructiveOperation:
		return true
	default:
		return false
	}
}

func validOutcome(outcome Outcome) bool {
	switch outcome {
	case OutcomeSucceeded, OutcomeFailed, OutcomeRejected, OutcomeCanceled:
		return true
	default:
		return false
	}
}

func validActor(actor ActorType) bool {
	switch actor {
	case ActorAdministrator, ActorUnauthenticated, ActorLocalOperator, ActorSystem:
		return true
	default:
		return false
	}
}

func validAction(action string) bool {
	if len(action) < 1 || len(action) > 128 ||
		action[0] < 'a' || action[0] > 'z' {
		return false
	}
	for index := 1; index < len(action); index++ {
		char := action[index]
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') ||
			char == '_' || char == '.' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func validOptionalID(id *int64) bool {
	return id == nil || *id > 0
}

func validText(value string, maxBytes int, optional bool) bool {
	if value == "" {
		return optional
	}
	if !utf8.ValidString(value) || len(value) > maxBytes {
		return false
	}
	return strings.IndexFunc(value, func(char rune) bool {
		return unicode.IsControl(char)
	}) < 0
}

func optionalID(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	id := value.Int64
	return &id
}

func cloneID(value *int64) *int64 {
	if value == nil {
		return nil
	}
	id := *value
	return &id
}
