// Package retention owns application-log retention settings and cleanup.
package retention

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/drilonrecica/siftail/internal/audit"
	"github.com/drilonrecica/siftail/internal/database"
)

const (
	DefaultAgeDays                = 14
	MinimumAgeDays                = 1
	MaximumAgeDays                = 3_650
	DefaultMaxDatabaseGiB         = 4
	MinimumMaxDatabaseGiB         = 1
	MaximumMaxDatabaseGiB         = 1_024
	applicationRetentionKey       = "application_retention"
	gibBytes                int64 = 1 << 30
)

var (
	ErrInvalidAgeLimit  = errors.New("invalid application-log retention age")
	ErrInvalidSizeLimit = errors.New("invalid maximum database size")
	ErrInvalidStored    = errors.New("stored application-log retention settings are invalid")
)

type Settings struct {
	AgeDays          int   `json:"age_days"`
	MaxDatabaseBytes int64 `json:"max_database_bytes"`
	UpdatedAtUS      int64 `json:"-"`
}

type Input struct {
	AgeDays        int
	MaxDatabaseGiB int
}

type Store struct {
	db      *sql.DB
	mutator database.MutationCoordinator
	now     func() time.Time
}

func NewStore(db *sql.DB, mutator database.MutationCoordinator) *Store {
	return &Store{db: db, mutator: mutator, now: time.Now}
}

func Defaults() Settings {
	return Settings{
		AgeDays:          DefaultAgeDays,
		MaxDatabaseBytes: int64(DefaultMaxDatabaseGiB) * gibBytes,
	}
}

func (s Settings) MaxDatabaseGiB() int {
	return int(s.MaxDatabaseBytes / gibBytes)
}

func Validate(input Input) error {
	if input.AgeDays < MinimumAgeDays || input.AgeDays > MaximumAgeDays {
		return ErrInvalidAgeLimit
	}
	if input.MaxDatabaseGiB < MinimumMaxDatabaseGiB ||
		input.MaxDatabaseGiB > MaximumMaxDatabaseGiB {
		return ErrInvalidSizeLimit
	}
	return nil
}

func (s *Store) Load(ctx context.Context) (Settings, error) {
	if s == nil || s.db == nil {
		return Settings{}, errors.New("retention settings storage is unavailable")
	}
	var valueJSON string
	var updatedAtUS int64
	err := s.db.QueryRowContext(ctx, `SELECT value_json, updated_at_us
		FROM settings WHERE key = ?`, applicationRetentionKey).
		Scan(&valueJSON, &updatedAtUS)
	if errors.Is(err, sql.ErrNoRows) {
		return Defaults(), nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("load application-log retention settings: %w", err)
	}
	settings, err := decodeSettings(valueJSON)
	if err != nil {
		return Settings{}, err
	}
	settings.UpdatedAtUS = updatedAtUS
	return settings, nil
}

func (s *Store) Save(ctx context.Context, input Input) (Settings, error) {
	if err := Validate(input); err != nil {
		return Settings{}, err
	}
	if s == nil || s.mutator == nil {
		return Settings{}, errors.New("retention settings mutation is unavailable")
	}
	settings := Settings{
		AgeDays:          input.AgeDays,
		MaxDatabaseBytes: int64(input.MaxDatabaseGiB) * gibBytes,
		UpdatedAtUS:      s.now().UTC().UnixMicro(),
	}
	valueJSON, err := json.Marshal(struct {
		AgeDays          int   `json:"age_days"`
		MaxDatabaseBytes int64 `json:"max_database_bytes"`
	}{
		AgeDays: settings.AgeDays, MaxDatabaseBytes: settings.MaxDatabaseBytes,
	})
	if err != nil {
		return Settings{}, fmt.Errorf("encode application-log retention settings: %w", err)
	}
	if err := s.mutator.Do(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value_json, updated_at_us)
			VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET
				value_json = excluded.value_json,
				updated_at_us = excluded.updated_at_us`,
			applicationRetentionKey, string(valueJSON), settings.UpdatedAtUS,
		)
		if err != nil {
			return database.Classify("save application-log retention settings", err)
		}
		auditInput := audit.InputFromContext(
			ctx, audit.CategoryRetentionSettings, "retention.update",
			audit.OutcomeSucceeded,
			audit.Metadata{
				audit.MetadataRetentionAgeDays: strconv.Itoa(input.AgeDays),
				audit.MetadataMaximumDatabaseGiB: strconv.Itoa(
					input.MaxDatabaseGiB,
				),
			},
		)
		auditInput.OccurredAt = time.UnixMicro(settings.UpdatedAtUS)
		_, err = audit.RecordTx(context.WithoutCancel(ctx), tx, auditInput)
		return err
	}); err != nil {
		return Settings{}, fmt.Errorf("save application-log retention settings: %w", err)
	}
	return settings, nil
}

func decodeSettings(valueJSON string) (Settings, error) {
	var stored struct {
		AgeDays          int   `json:"age_days"`
		MaxDatabaseBytes int64 `json:"max_database_bytes"`
	}
	decoder := json.NewDecoder(bytes.NewBufferString(valueJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return Settings{}, ErrInvalidStored
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return Settings{}, ErrInvalidStored
	}
	if stored.MaxDatabaseBytes%gibBytes != 0 {
		return Settings{}, ErrInvalidStored
	}
	input := Input{
		AgeDays: stored.AgeDays, MaxDatabaseGiB: int(stored.MaxDatabaseBytes / gibBytes),
	}
	if Validate(input) != nil {
		return Settings{}, ErrInvalidStored
	}
	return Settings{
		AgeDays: stored.AgeDays, MaxDatabaseBytes: stored.MaxDatabaseBytes,
	}, nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("unexpected trailing JSON value")
	}
	return err
}
