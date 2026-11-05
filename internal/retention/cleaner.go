package retention

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
	"github.com/drilonrecica/siftail/internal/logs"
)

const (
	DefaultCleanupInterval = time.Hour
	defaultDeleteChunk     = 10_000
	// defaultVacuumPages bounds the pages one reclamation cycle releases
	// (ARCHITECTURE.md §23.5).
	defaultVacuumPages = 8_192
	// defaultVacuumChunkPages bounds the pages one writer turn releases. One
	// chunk is one incremental-vacuum transaction (one WAL commit); 1,024
	// pages held the writer for 72–189 ms on a SATA SSD
	// (docs/performance/retention.md).
	defaultVacuumChunkPages = 1_024
	// Container instances are small rows; the chunk bounds how many
	// NOT EXISTS probes one writer turn performs.
	defaultContainerPruneChunk = 1_000
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
	ContainersPruned  int64
}

type CleanerOptions struct {
	DeleteChunk int
	// VacuumPages caps the pages released per reclamation cycle and
	// VacuumChunkPages the pages released per coordinator operation.
	VacuumPages      int
	VacuumChunkPages int
	Now              func() time.Time
	AfterDelete      func(int64)
	// MeasureFootprint defaults to ActiveFootprint over the cleaner's reader
	// pool and database path.
	MeasureFootprint func(context.Context) (int64, error)
	// ContainerLimit is the per-server container-instance cap the writer
	// enforces; it defaults to logs.MaxContainerInstancesPerServer.
	ContainerLimit      int
	ContainerPruneChunk int
}

type Cleaner struct {
	db               *sql.DB
	coordinator      database.MaintenanceCoordinator
	path             string
	settings         *Store
	deleteChunk      int
	vacuumPages      int
	vacuumChunk      int
	now              func() time.Time
	afterDelete      func(int64)
	measureFootprint func(context.Context) (int64, error)
	containerLimit   int
	containerChunk   int
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
	if options.VacuumChunkPages <= 0 {
		options.VacuumChunkPages = defaultVacuumChunkPages
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.MeasureFootprint == nil {
		options.MeasureFootprint = func(ctx context.Context) (int64, error) {
			return ActiveFootprint(ctx, db, path)
		}
	}
	if options.ContainerLimit <= 0 {
		options.ContainerLimit = logs.MaxContainerInstancesPerServer
	}
	if options.ContainerPruneChunk <= 0 {
		options.ContainerPruneChunk = defaultContainerPruneChunk
	}
	return &Cleaner{
		db: db, coordinator: coordinator, path: path, settings: settings,
		deleteChunk: options.DeleteChunk, vacuumPages: options.VacuumPages,
		vacuumChunk: options.VacuumChunkPages,
		now:         options.Now, afterDelete: options.AfterDelete,
		measureFootprint: options.MeasureFootprint,
		containerLimit:   options.ContainerLimit,
		containerChunk:   options.ContainerPruneChunk,
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
	cutoff := AgeCutoffUS(c.now(), settings.AgeDays)
	if err := c.deleteEvents(ctx, settings, cutoff, &result); err != nil {
		return result, err
	}
	// Event deletion is what leaves container instances unreferenced, so
	// pruning follows it in the same cycle.
	pruned, err := c.pruneContainers(ctx, cutoff)
	result.ContainersPruned = pruned
	return result, err
}

func (c *Cleaner) deleteEvents(ctx context.Context, settings Settings, cutoff int64, result *CleanupResult) error {
	var err error
	for {
		deleted, err := c.deleteOldest(ctx, &cutoff)
		if err != nil {
			return err
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
			return err
		}
		result.CheckpointBusy = busy
	}

	result.FootprintBefore, err = c.measureFootprint(ctx)
	if err != nil {
		return err
	}
	trigger := settings.MaxDatabaseBytes * 95 / 100
	target := settings.MaxDatabaseBytes * 90 / 100
	result.FootprintAfter = result.FootprintBefore
	if result.FootprintBefore < trigger {
		result.SizeTargetReached = true
		return nil
	}
	result.SizeTriggered = true
	if result.CheckpointBusy {
		return nil
	}
	for result.FootprintAfter > target {
		deleted, err := c.deleteOldest(ctx, nil)
		if err != nil {
			return err
		}
		if deleted == 0 {
			result.EventsExhausted = true
			break
		}
		result.SizeDeleted += deleted
		c.notifyDeleted(deleted)
		busy, err := c.reclaim(ctx)
		if err != nil {
			return err
		}
		if busy {
			result.CheckpointBusy = true
			break
		}
		result.FootprintAfter, err = c.measureFootprint(ctx)
		if err != nil {
			return err
		}
	}
	result.SizeTargetReached = result.FootprintAfter <= target
	return nil
}

// pruneContainers is the bounded container-instance cleanup of ADR 0006 and
// DOMAIN.md §14. An instance a retained event still references is never
// deleted; the log_events foreign key (no cascade) enforces that as well.
//
//  1. Unreferenced instances last seen before the age cutoff, oldest first.
//  2. Per Server above its prune target, its oldest unreferenced instances by
//     (last_seen_at_us, id) until it is back at the target. The writer rejects
//     any batch that would exceed the cap, so a Server can never hold more than
//     the cap; the target therefore sits one tenth below it, leaving room for
//     new containers until the next cycle.
//
// Each chunk is one coordinator transaction, so ingestion interleaves.
func (c *Cleaner) pruneContainers(ctx context.Context, cutoff int64) (int64, error) {
	var total int64
	for {
		deleted, err := c.pruneChunk(ctx, func(tx *sql.Tx) (sql.Result, error) {
			return tx.ExecContext(ctx, `DELETE FROM container_instances WHERE id IN (
				SELECT c.id FROM container_instances AS c
				WHERE c.last_seen_at_us < ?
				AND NOT EXISTS (
					SELECT 1 FROM log_events AS e WHERE e.container_instance_id = c.id
				)
				ORDER BY c.last_seen_at_us, c.id LIMIT ?)`, cutoff, c.containerChunk)
		})
		total += deleted
		if err != nil {
			return total, err
		}
		if deleted < int64(c.containerChunk) {
			break
		}
	}

	target := c.containerLimit - c.containerLimit/10
	servers, err := c.serversAboveContainerTarget(ctx, target)
	if err != nil {
		return total, err
	}
	for _, serverID := range servers {
		for {
			deleted, err := c.pruneChunk(ctx, func(tx *sql.Tx) (sql.Result, error) {
				var count int
				if err := tx.QueryRowContext(ctx, `SELECT count(*)
					FROM container_instances AS c JOIN sources AS s ON s.id = c.source_id
					WHERE s.server_id = ?`, serverID).Scan(&count); err != nil {
					return nil, err
				}
				excess := min(count-target, c.containerChunk)
				if excess <= 0 {
					return nil, nil
				}
				return tx.ExecContext(ctx, `DELETE FROM container_instances WHERE id IN (
					SELECT c.id FROM container_instances AS c
					JOIN sources AS s ON s.id = c.source_id
					WHERE s.server_id = ?
					AND NOT EXISTS (
						SELECT 1 FROM log_events AS e WHERE e.container_instance_id = c.id
					)
					ORDER BY c.last_seen_at_us, c.id LIMIT ?)`, serverID, excess)
			})
			total += deleted
			if err != nil {
				return total, err
			}
			// Zero means the Server is at its target or every remaining
			// instance still has retained events.
			if deleted == 0 {
				break
			}
		}
	}
	return total, nil
}

// pruneChunk runs one bounded deletion in a writer transaction. A nil result
// means there was nothing to delete.
func (c *Cleaner) pruneChunk(ctx context.Context, remove func(*sql.Tx) (sql.Result, error)) (int64, error) {
	var deleted int64
	err := c.coordinator.Do(ctx, func(tx *sql.Tx) error {
		result, err := remove(tx)
		if err != nil {
			return database.Classify("prune container instances", err)
		}
		if result == nil {
			return nil
		}
		deleted, err = result.RowsAffected()
		if err != nil {
			return database.Classify("read container prune result", err)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("container instance pruning: %w", err)
	}
	return deleted, nil
}

// serversAboveContainerTarget reads candidates from the reader pool; every
// chunk recounts inside its writer transaction, so a stale list only costs a
// no-op transaction.
func (c *Cleaner) serversAboveContainerTarget(ctx context.Context, target int) ([]int64, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT s.server_id
		FROM container_instances AS c JOIN sources AS s ON s.id = c.source_id
		GROUP BY s.server_id HAVING count(*) > ?
		ORDER BY s.server_id`, target)
	if err != nil {
		return nil, fmt.Errorf("container instance pruning: %w",
			database.Classify("list servers above container target", err))
	}
	defer rows.Close()
	var servers []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("container instance pruning: %w",
				database.Classify("scan server above container target", err))
		}
		servers = append(servers, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("container instance pruning: %w",
			database.Classify("list servers above container target", err))
	}
	return servers, nil
}

func (c *Cleaner) deleteOldest(ctx context.Context, cutoff *int64) (int64, error) {
	var deleted int64
	err := c.coordinator.Do(ctx, func(tx *sql.Tx) error {
		query := `DELETE FROM log_events WHERE id IN (
			SELECT id FROM log_events`
		var args []any
		if cutoff != nil {
			query += ` WHERE received_at_us < ?`
			args = append(args, *cutoff)
		}
		query += ` ORDER BY received_at_us, id LIMIT ?)`
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

// reclaim is one bounded reclamation cycle (ARCHITECTURE.md §23.5): a passive
// checkpoint preflight, incremental vacuum in chunks, and a truncate checkpoint.
// Every step is its own coordinator operation, so ingestion commits queued
// behind one step run before the next. It reports whether a reader kept a
// checkpoint busy.
func (c *Cleaner) reclaim(ctx context.Context) (bool, error) {
	busy, err := c.checkpoint(ctx, "PASSIVE")
	if err != nil || busy {
		return busy, err
	}
	if err := c.vacuum(ctx); err != nil {
		return false, err
	}
	return c.checkpoint(ctx, "TRUNCATE")
}

func (c *Cleaner) checkpoint(ctx context.Context, mode string) (bool, error) {
	var busy bool
	err := c.coordinator.DoMaintenance(ctx, func(db *sql.DB) error {
		var err error
		busy, err = retentionCheckpoint(db, mode)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("retention reclamation: %w", err)
	}
	return busy, nil
}

// vacuum releases at most vacuumPages free pages, vacuumChunk per writer turn,
// and stops early once the freelist is empty.
func (c *Cleaner) vacuum(ctx context.Context) error {
	for remaining := c.vacuumPages; remaining > 0; {
		pages := min(remaining, c.vacuumChunk)
		var released int
		err := c.coordinator.DoMaintenance(ctx, func(db *sql.DB) error {
			var err error
			released, err = incrementalVacuum(db, pages)
			return err
		})
		if err != nil {
			return fmt.Errorf("retention reclamation: %w", err)
		}
		if released < pages {
			return nil
		}
		remaining -= released
	}
	return nil
}

// incrementalVacuum releases up to pages free pages in one autocommit
// transaction and returns how many it released. The pragma moves one page per
// result row, so the rows must be drained: Exec steps once and would release a
// single page.
func incrementalVacuum(db *sql.DB, pages int) (int, error) {
	if pages <= 0 {
		// incremental_vacuum(0) would release the whole freelist.
		return 0, errors.New("invalid incremental-vacuum page limit")
	}
	var before, after int
	if err := db.QueryRow("PRAGMA freelist_count").Scan(&before); err != nil {
		return 0, database.Classify("inspect retention freelist", err)
	}
	if before == 0 {
		return 0, nil
	}
	rows, err := db.Query("PRAGMA incremental_vacuum(" + strconv.Itoa(pages) + ")")
	if err != nil {
		return 0, database.Classify("incremental retention vacuum", err)
	}
	for rows.Next() {
	}
	err = rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return 0, database.Classify("incremental retention vacuum", err)
	}
	if err := db.QueryRow("PRAGMA freelist_count").Scan(&after); err != nil {
		return 0, database.Classify("inspect retention freelist", err)
	}
	return max(before-after, 0), nil
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

// ActiveFootprint is the size-retention measure (DOMAIN.md §16.6): the main
// database's in-use pages, (page_count − freelist_count) × page_size, plus the
// WAL and shared-memory files. Free pages are not counted because SQLite
// reuses them before it grows the file; counting them would make size
// retention delete events to pay for space that is already free. The three
// page values come from one statement and therefore one read snapshot.
func ActiveFootprint(ctx context.Context, db *sql.DB, databasePath string) (int64, error) {
	if db == nil {
		return 0, errors.New("database is unavailable")
	}
	if databasePath == "" {
		return 0, errors.New("database path is empty")
	}
	var pages, free, pageSize int64
	if err := db.QueryRowContext(ctx, `SELECT p.page_count, f.freelist_count, s.page_size
		FROM pragma_page_count() AS p, pragma_freelist_count() AS f,
			pragma_page_size() AS s`).Scan(&pages, &free, &pageSize); err != nil {
		return 0, fmt.Errorf("measure active SQLite footprint: %w",
			database.Classify("read page counts", err))
	}
	total := max(pages-free, 0) * pageSize
	for _, path := range []string{databasePath + "-wal", databasePath + "-shm"} {
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
