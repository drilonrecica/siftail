package sources

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const purgeChunkEvents = 10_000

var ErrInvalidAlias = errors.New("invalid source alias")

type PurgeResult struct {
	SourceID  int64
	Watermark int64
	Deleted   int64
	Removed   bool
}

func (s *Store) SetAlias(ctx context.Context, sourceID int64, alias string) error {
	if s == nil || s.mutator == nil {
		return errors.New("source store is unavailable")
	}
	if sourceID <= 0 {
		return ErrSourceNotFound
	}
	alias = strings.TrimSpace(alias)
	if err := validText(alias, 128, true); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAlias, err)
	}
	return s.mutator.Do(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`UPDATE sources SET alias = nullif(?, '') WHERE id = ?`, alias, sourceID)
		if err != nil {
			return fmt.Errorf("update source alias: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read source alias result: %w", err)
		}
		if affected == 0 {
			return ErrSourceNotFound
		}
		return nil
	})
}

func (s *Store) ClearLogs(ctx context.Context, sourceID int64) (PurgeResult, error) {
	watermark, err := s.captureSourceWatermark(ctx, sourceID)
	if err != nil {
		return PurgeResult{}, err
	}
	deleted, err := s.deleteSourceEvents(ctx, sourceID, watermark)
	if err != nil {
		return PurgeResult{}, err
	}
	return PurgeResult{
		SourceID: sourceID, Watermark: watermark, Deleted: deleted,
	}, nil
}

func (s *Store) RemoveSource(ctx context.Context, sourceID int64) (PurgeResult, error) {
	watermark, err := s.captureSourceWatermark(ctx, sourceID)
	if err != nil {
		return PurgeResult{}, err
	}
	deleted, err := s.deleteSourceEvents(ctx, sourceID, watermark)
	if err != nil {
		return PurgeResult{}, err
	}
	result := PurgeResult{
		SourceID: sourceID, Watermark: watermark, Deleted: deleted,
	}
	err = s.mutator.Do(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM container_instances
			WHERE source_id = ?
			AND NOT EXISTS (
				SELECT 1 FROM log_events AS event
				WHERE event.container_instance_id = container_instances.id
			)`, sourceID); err != nil {
			return fmt.Errorf("delete source container observations: %w", err)
		}
		var remaining int
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM log_events WHERE source_id = ? LIMIT 1)`,
			sourceID,
		).Scan(&remaining); err != nil {
			return fmt.Errorf("check source events after watermark: %w", err)
		}
		if remaining != 0 {
			update, err := tx.ExecContext(ctx,
				`UPDATE sources SET alias = NULL WHERE id = ?`, sourceID)
			if err != nil {
				return fmt.Errorf("remove source alias: %w", err)
			}
			affected, err := update.RowsAffected()
			if err != nil {
				return fmt.Errorf("read source alias removal: %w", err)
			}
			if affected == 0 {
				return ErrSourceNotFound
			}
			return nil
		}
		deletion, err := tx.ExecContext(ctx, `DELETE FROM sources WHERE id = ?`, sourceID)
		if err != nil {
			return fmt.Errorf("delete source metadata: %w", err)
		}
		affected, err := deletion.RowsAffected()
		if err != nil {
			return fmt.Errorf("read source deletion: %w", err)
		}
		if affected == 0 {
			return ErrSourceNotFound
		}
		result.Removed = true
		return nil
	})
	if err != nil {
		return PurgeResult{}, err
	}
	return result, nil
}

func (s *Store) captureSourceWatermark(ctx context.Context, sourceID int64) (int64, error) {
	if s == nil || s.mutator == nil {
		return 0, errors.New("source store is unavailable")
	}
	if sourceID <= 0 {
		return 0, ErrSourceNotFound
	}
	var watermark int64
	err := s.mutator.Do(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT coalesce(max(event.id), 0)
			FROM sources AS source
			LEFT JOIN log_events AS event ON event.source_id = source.id
			WHERE source.id = ?
			GROUP BY source.id`, sourceID).Scan(&watermark)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSourceNotFound
		}
		if err != nil {
			return fmt.Errorf("capture source purge watermark: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return watermark, nil
}

func (s *Store) deleteSourceEvents(
	ctx context.Context,
	sourceID, watermark int64,
) (int64, error) {
	var total int64
	for {
		var deleted int64
		err := s.mutator.Do(ctx, func(tx *sql.Tx) error {
			result, err := tx.ExecContext(ctx, `DELETE FROM log_events
				WHERE id IN (
					SELECT id FROM log_events
					WHERE source_id = ? AND id <= ?
					ORDER BY id
					LIMIT ?
				)`, sourceID, watermark, purgeChunkEvents)
			if err != nil {
				return fmt.Errorf("delete source events: %w", err)
			}
			deleted, err = result.RowsAffected()
			if err != nil {
				return fmt.Errorf("read source purge result: %w", err)
			}
			return nil
		})
		if err != nil {
			return 0, err
		}
		total += deleted
		if deleted < purgeChunkEvents {
			return total, nil
		}
	}
}
