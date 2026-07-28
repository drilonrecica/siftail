package retention

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
)

const (
	DefaultCleanupInterval = time.Hour
	defaultDeleteChunk     = 10_000
	defaultVacuumPages     = 8_192
)

type CleanupResult struct {
	AgeDeleted        int64
	SizeDeleted       int64
	FootprintBefore   int64
	FootprintAfter    int64
	SizeTriggered     bool
	SizeTargetReached bool
	EventsExhausted   bool
	CheckpointBusy    bool
}

type CleanerOptions struct {
	DeleteChunk      int
	VacuumPages      int
	Now              func() time.Time
	AfterDelete      func(int64)
	MeasureFootprint func(string) (int64, error)
}

type Cleaner struct {
	db               *sql.DB
	coordinator      database.MaintenanceCoordinator
	path             string
	settings         *Store
	deleteChunk      int
	vacuumPages      int
	now              func() time.Time
	afterDelete      func(int64)
	measureFootprint func(string) (int64, error)
}

func NewCleaner(
	db *sql.DB,
	coordinator database.MaintenanceCoordinator,
	path string,
	settings *Store,
	options CleanerOptions,
) *Cleaner {
	if options.DeleteChunk <= 0 {
		options.DeleteChunk = defaultDeleteChunk
	}
	if options.VacuumPages <= 0 {
		options.VacuumPages = defaultVacuumPages
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.MeasureFootprint == nil {
		options.MeasureFootprint = ActiveFootprint
	}
	return &Cleaner{
		db: db, coordinator: coordinator, path: path, settings: settings,
		deleteChunk: options.DeleteChunk, vacuumPages: options.VacuumPages,
		now: options.Now, afterDelete: options.AfterDelete,
		measureFootprint: options.MeasureFootprint,
	}
}

func (c *Cleaner) RunOnce(ctx context.Context) (CleanupResult, error) {
	if c == nil || c.db == nil || c.coordinator == nil || c.settings == nil ||
		c.path == "" {
		return CleanupResult{}, errors.New("retention cleaner is unavailable")
	}
	settings, err := c.settings.Load(ctx)
	if err != nil {
		return CleanupResult{}, err
	}
	result := CleanupResult{}
	cutoff := c.now().UTC().AddDate(0, 0, -settings.AgeDays).UnixMicro()
	for {
		deleted, err := c.deleteOldest(ctx, &cutoff)
		if err != nil {
			return result, err
		}
		result.AgeDeleted += deleted
		c.notifyDeleted(deleted)
		if deleted < int64(c.deleteChunk) {
			break
		}
	}
	if result.AgeDeleted > 0 {
		busy, err := c.reclaim(ctx)
		if err != nil {
			return result, err
		}
		result.CheckpointBusy = busy
	}

	result.FootprintBefore, err = c.measureFootprint(c.path)
	if err != nil {
		return result, err
	}
	trigger := settings.MaxDatabaseBytes * 95 / 100
	target := settings.MaxDatabaseBytes * 90 / 100
	result.FootprintAfter = result.FootprintBefore
	if result.FootprintBefore < trigger {
		result.SizeTargetReached = true
		return result, nil
	}
	result.SizeTriggered = true
	if result.CheckpointBusy {
		return result, nil
	}
	for result.FootprintAfter > target {
		deleted, err := c.deleteOldest(ctx, nil)
		if err != nil {
			return result, err
		}
		if deleted == 0 {
			result.EventsExhausted = true
			break
		}
		result.SizeDeleted += deleted
		c.notifyDeleted(deleted)
		busy, err := c.reclaim(ctx)
		if err != nil {
			return result, err
		}
		if busy {
			result.CheckpointBusy = true
			break
		}
		result.FootprintAfter, err = c.measureFootprint(c.path)
		if err != nil {
			return result, err
		}
	}
	result.SizeTargetReached = result.FootprintAfter <= target
	return result, nil
}

func (c *Cleaner) deleteOldest(ctx context.Context, cutoff *int64) (int64, error) {
	var deleted int64
	err := c.coordinator.Do(ctx, func(tx *sql.Tx) error {
		query := `DELETE FROM log_events WHERE id IN (
			SELECT id FROM log_events`
		var args []any
		if cutoff != nil {
			query += ` WHERE retention_at_us < ?`
			args = append(args, *cutoff)
		}
		query += ` ORDER BY retention_at_us, id LIMIT ?)`
		args = append(args, c.deleteChunk)
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return database.Classify("delete retained application events", err)
		}
		deleted, err = result.RowsAffected()
		if err != nil {
			return database.Classify("read retention deletion result", err)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("retention deletion: %w", err)
	}
	return deleted, nil
}

func (c *Cleaner) reclaim(ctx context.Context) (bool, error) {
	var checkpointBusy bool
	err := c.coordinator.DoMaintenance(ctx, func(db *sql.DB) error {
		busy, err := retentionCheckpoint(db, "PASSIVE")
		if err != nil {
			return err
		}
		if busy {
			checkpointBusy = true
			return nil
		}
		if err := incrementalVacuum(db, c.vacuumPages); err != nil {
			return err
		}
		busy, err = retentionCheckpoint(db, "TRUNCATE")
		if err != nil {
			return err
		}
		checkpointBusy = busy
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("retention reclamation: %w", err)
	}
	return checkpointBusy, nil
}

func incrementalVacuum(db *sql.DB, maximumPages int) error {
	if maximumPages <= 0 {
		return errors.New("invalid incremental-vacuum page limit")
	}
	var freePages int
	if err := db.QueryRow("PRAGMA freelist_count").Scan(&freePages); err != nil {
		return database.Classify("inspect retention freelist", err)
	}
	if freePages < maximumPages {
		maximumPages = freePages
	}
	for page := 0; page < maximumPages; page++ {
		if _, err := db.Exec("PRAGMA incremental_vacuum(1)"); err != nil {
			return database.Classify("incremental retention vacuum", err)
		}
	}
	return nil
}

func retentionCheckpoint(db *sql.DB, mode string) (bool, error) {
	if mode != "PASSIVE" && mode != "TRUNCATE" {
		return false, errors.New("invalid retention checkpoint mode")
	}
	var busy, logFrames, checkpointed int
	query := "PRAGMA wal_checkpoint(" + mode + ")"
	if err := db.QueryRow(query).
		Scan(&busy, &logFrames, &checkpointed); err != nil {
		return false, database.Classify("retention WAL checkpoint", err)
	}
	return busy != 0, nil
}

func (c *Cleaner) notifyDeleted(deleted int64) {
	if deleted > 0 && c.afterDelete != nil {
		c.afterDelete(deleted)
	}
}

func ActiveFootprint(databasePath string) (int64, error) {
	if databasePath == "" {
		return 0, errors.New("database path is empty")
	}
	var total int64
	for _, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm"} {
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("measure active SQLite footprint: %w", err)
		}
		if !info.Mode().IsRegular() {
			return 0, errors.New("active SQLite footprint contains a non-regular file")
		}
		total += info.Size()
	}
	return total, nil
}
