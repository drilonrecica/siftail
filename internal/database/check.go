package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type CheckReport struct {
	Mode               string `json:"mode"`
	SchemaVersion      int    `json:"schema_version"`
	SupportedSchema    int    `json:"supported_schema"`
	SQLiteVersion      string `json:"sqlite_version"`
	Compatible         bool   `json:"compatible"`
	Integrity          string `json:"integrity"`
	JournalMode        string `json:"journal_mode"`
	Synchronous        string `json:"synchronous"`
	ForeignKeys        bool   `json:"foreign_keys"`
	AutoVacuum         string `json:"auto_vacuum"`
	Writable           bool   `json:"writable"`
	WritabilitySource  string `json:"writability_source"`
	Checkpoint         string `json:"checkpoint"`
	WALFrames          int    `json:"wal_frames"`
	CheckpointedFrames int    `json:"checkpointed_frames"`
	DatabaseBytes      int64  `json:"database_bytes"`
	WALBytes           int64  `json:"wal_bytes"`
	SHMBytes           int64  `json:"shm_bytes"`
	PageCount          int64  `json:"page_count"`
	FreePages          int64  `json:"free_pages"`
}

type ActiveChecker struct {
	db          *DB
	path        string
	coordinator MaintenanceCoordinator
	writable    func() bool
	mu          sync.RWMutex
	last        CheckResult
}

type CheckResult struct {
	At            time.Time   `json:"at"`
	Report        CheckReport `json:"report"`
	ErrorCategory string      `json:"error_category,omitempty"`
}

func NewActiveChecker(
	db *DB,
	path string,
	coordinator MaintenanceCoordinator,
	writable func() bool,
) *ActiveChecker {
	return &ActiveChecker{
		db: db, path: path, coordinator: coordinator, writable: writable,
	}
}

func (c *ActiveChecker) Check(ctx context.Context) (CheckReport, error) {
	if c == nil || c.db == nil || c.coordinator == nil || c.writable == nil {
		return CheckReport{}, errors.New("active database check is unavailable")
	}
	report, err := c.db.CheckActive(ctx, c.path, c.coordinator, c.writable())
	result := CheckResult{At: time.Now().UTC(), Report: report}
	if err != nil {
		result.ErrorCategory = categoryName(err)
	}
	c.mu.Lock()
	c.last = result
	c.mu.Unlock()
	return report, err
}

func (c *ActiveChecker) Last() (CheckResult, bool) {
	if c == nil {
		return CheckResult{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.last.At.IsZero() {
		return CheckResult{}, false
	}
	return c.last, true
}

// CheckActive performs the active-safe database inspection. Read checks use
// the bounded read pool. The passive checkpoint is ordered with every writer
// mutation by the maintenance coordinator.
func (d *DB) CheckActive(
	ctx context.Context,
	path string,
	coordinator MaintenanceCoordinator,
	writable bool,
) (CheckReport, error) {
	if d == nil || d.reader == nil || coordinator == nil {
		return CheckReport{}, errors.New("active database check is unavailable")
	}
	report, err := collectCheck(ctx, d.reader, path, false)
	report.Writable = writable
	report.WritabilitySource = "operational_state"
	if err != nil {
		return report, err
	}
	err = coordinator.DoMaintenance(ctx, func(writer *sql.DB) error {
		var busy int
		if err := writer.QueryRowContext(
			context.WithoutCancel(ctx), "PRAGMA wal_checkpoint(PASSIVE)",
		).Scan(&busy, &report.WALFrames, &report.CheckpointedFrames); err != nil {
			return classify("database check WAL checkpoint", err)
		}
		if busy != 0 || report.CheckpointedFrames < report.WALFrames {
			report.Checkpoint = "busy"
			return &CategoryError{
				Category: CategoryBusy, Operation: "database check WAL checkpoint",
			}
		}
		report.Checkpoint = "completed"
		return nil
	})
	return report, err
}

// CheckPath opens an existing stopped-server database read-only. It never
// creates, migrates, checkpoints, or rewrites the database.
func CheckPath(ctx context.Context, path string, full bool) (CheckReport, error) {
	info, err := os.Stat(path)
	if err != nil {
		return CheckReport{}, fmt.Errorf("inspect database file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return CheckReport{}, errors.New("database is not a regular file")
	}
	query := url.Values{
		"mode":          {"ro"},
		"_query_only":   {"on"},
		"_busy_timeout": {"5000"},
		"_foreign_keys": {"on"},
		"_synchronous":  {"FULL"},
	}
	db, err := sql.Open("sqlite3",
		"file:"+filepath.ToSlash(path)+"?"+query.Encode())
	if err != nil {
		return CheckReport{}, classify("open database check", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return CheckReport{}, classify("connect database check", err)
	}
	report, err := collectCheck(ctx, db, path, full)
	report.Writable = stoppedFileWritable(path, info.Mode()) &&
		directoryModeWritable(filepath.Dir(path))
	report.WritabilitySource = "filesystem_access"
	report.Checkpoint = "not_run_read_only"
	return report, err
}

func collectCheck(
	ctx context.Context,
	db *sql.DB,
	path string,
	full bool,
) (CheckReport, error) {
	report := CheckReport{
		Mode: "quick", SupportedSchema: MaxSchemaVersion,
		Checkpoint: "not_run",
	}
	if full {
		report.Mode = "full"
	}
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema
		WHERE type='table' AND name='schema_migrations'`).Scan(&exists); err != nil {
		return report, classify("inspect database check schema", err)
	}
	if exists != 0 {
		if err := db.QueryRowContext(ctx,
			"SELECT coalesce(max(version),0) FROM schema_migrations",
		).Scan(&report.SchemaVersion); err != nil {
			return report, classify("read database check schema", err)
		}
	}
	report.Compatible = report.SchemaVersion <= report.SupportedSchema
	if !report.Compatible {
		return report, &SchemaTooNewError{
			Actual: report.SchemaVersion, Supported: report.SupportedSchema,
		}
	}
	integrityQuery := "PRAGMA quick_check"
	if full {
		integrityQuery = "PRAGMA integrity_check"
	}
	if err := integrityCheck(ctx, db, integrityQuery); err != nil {
		report.Integrity = categoryName(err)
		return report, err
	}
	report.Integrity = "ok"
	var foreignKeys int
	var autoVacuum int
	if err := db.QueryRowContext(ctx, `SELECT
		sqlite_version(),
		(SELECT journal_mode FROM pragma_journal_mode),
		(SELECT synchronous FROM pragma_synchronous),
		(SELECT foreign_keys FROM pragma_foreign_keys),
		(SELECT auto_vacuum FROM pragma_auto_vacuum),
		(SELECT page_count FROM pragma_page_count),
		(SELECT freelist_count FROM pragma_freelist_count)`).
		Scan(
			&report.SQLiteVersion, &report.JournalMode,
			&report.Synchronous, &foreignKeys,
			&autoVacuum, &report.PageCount, &report.FreePages,
		); err != nil {
		return report, classify("read database check settings", err)
	}
	report.JournalMode = strings.ToLower(report.JournalMode)
	report.ForeignKeys = foreignKeys == 1
	report.AutoVacuum = map[int]string{
		0: "none", 1: "full", 2: "incremental",
	}[autoVacuum]
	var err error
	report.DatabaseBytes, err = checkRegularFileSize(path, false)
	if err != nil {
		return report, err
	}
	report.WALBytes, err = checkRegularFileSize(path+"-wal", true)
	if err != nil {
		return report, err
	}
	report.SHMBytes, err = checkRegularFileSize(path+"-shm", true)
	if err != nil {
		return report, err
	}
	return report, nil
}

func checkRegularFileSize(path string, optional bool) (int64, error) {
	info, err := os.Stat(path)
	if optional && errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("inspect database storage file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return 0, errors.New("database storage entry is not a regular file")
	}
	return info.Size(), nil
}

func fileModeWritable(mode os.FileMode) bool {
	return mode.Perm()&0222 != 0
}

func stoppedFileWritable(path string, mode os.FileMode) bool {
	if !fileModeWritable(mode) {
		return false
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	return file.Close() == nil
}

func directoryModeWritable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir() && info.Mode().Perm()&0222 != 0
}

func categoryName(err error) string {
	var category *CategoryError
	if errors.As(err, &category) {
		return string(category.Category)
	}
	var tooNew *SchemaTooNewError
	if errors.As(err, &tooNew) {
		return "incompatible"
	}
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	return "internal"
}

func (r CheckReport) Validate() error {
	if r.Mode != "quick" && r.Mode != "full" {
		return errors.New("invalid database check mode")
	}
	if r.SchemaVersion < 0 || r.SupportedSchema != MaxSchemaVersion ||
		r.DatabaseBytes < 0 || r.WALBytes < 0 || r.SHMBytes < 0 ||
		r.PageCount < 0 || r.FreePages < 0 ||
		r.FreePages > r.PageCount || r.WALFrames < 0 ||
		r.CheckpointedFrames < 0 || r.CheckpointedFrames > r.WALFrames {
		return errors.New("invalid database check result")
	}
	for _, value := range []string{
		r.SQLiteVersion, r.Integrity, r.JournalMode, r.Synchronous, r.AutoVacuum,
		r.WritabilitySource, r.Checkpoint,
	} {
		if value == "" || len(value) > 64 {
			return errors.New("invalid database check result")
		}
	}
	return nil
}

func (r CheckReport) SummaryLines() []string {
	return []string{
		"mode: " + r.Mode,
		"schema: " + strconv.Itoa(r.SchemaVersion) + "/" +
			strconv.Itoa(r.SupportedSchema),
		"sqlite: " + r.SQLiteVersion,
		"compatible: " + strconv.FormatBool(r.Compatible),
		"integrity: " + r.Integrity,
		"writable: " + strconv.FormatBool(r.Writable) +
			" (" + r.WritabilitySource + ")",
		"journal: " + r.JournalMode,
		"synchronous: " + r.Synchronous,
		"foreign keys: " + strconv.FormatBool(r.ForeignKeys),
		"auto vacuum: " + r.AutoVacuum,
		"checkpoint: " + r.Checkpoint,
		"wal frames: " + strconv.Itoa(r.CheckpointedFrames) + "/" +
			strconv.Itoa(r.WALFrames),
		"database bytes: " + strconv.FormatInt(r.DatabaseBytes, 10),
		"wal bytes: " + strconv.FormatInt(r.WALBytes, 10),
		"shared-memory bytes: " + strconv.FormatInt(r.SHMBytes, 10),
		"pages: " + strconv.FormatInt(r.PageCount, 10),
		"free pages: " + strconv.FormatInt(r.FreePages, 10),
	}
}
