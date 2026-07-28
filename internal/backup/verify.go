package backup

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
)

var baseBackupTables = []string{
	"container_instances",
	"ingestion_tokens",
	"log_events",
	"schema_migrations",
	"servers",
	"settings",
	"siftail_backup_metadata",
	"sources",
}

func Verify(ctx context.Context, path string) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("backup verification context is nil")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return Result{}, errors.New("backup artifact is unavailable")
	}
	if info.Mode().Perm()&0077 != 0 {
		return Result{}, errors.New("backup artifact permissions are unsafe")
	}
	db, err := sql.Open("sqlite3", sqlitePath(path, "ro")+"&_query_only=1")
	if err != nil {
		return Result{}, errors.New("open backup artifact")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return Result{}, errors.New("read backup artifact")
	}
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil ||
		integrity != "ok" {
		return Result{}, errors.New("backup integrity check failed")
	}
	var result Result
	var createdAtUS int64
	var formatVersion, complete int
	var metadataRows int
	if err := db.QueryRowContext(
		ctx, "SELECT count(*) FROM siftail_backup_metadata",
	).Scan(&metadataRows); err != nil || metadataRows != 1 {
		return Result{}, errors.New("backup metadata is invalid")
	}
	if err := db.QueryRowContext(ctx, `SELECT
		format_version,backup_type,created_at_us,source_schema_version,complete
		FROM siftail_backup_metadata`).Scan(
		&formatVersion, &result.Type, &createdAtUS,
		&result.SchemaVersion, &complete,
	); err != nil {
		return Result{}, errors.New("backup metadata is invalid")
	}
	if formatVersion != FormatVersion ||
		(result.Type != TypeFull && result.Type != TypeConfiguration) ||
		complete != 1 || result.SchemaVersion < 1 ||
		result.SchemaVersion > database.MaxSchemaVersion {
		return Result{}, errors.New("backup metadata is incompatible")
	}
	var schemaVersion int
	if err := db.QueryRowContext(
		ctx, "SELECT coalesce(max(version),0) FROM schema_migrations",
	).Scan(&schemaVersion); err != nil || schemaVersion != result.SchemaVersion {
		return Result{}, errors.New("backup schema metadata is inconsistent")
	}
	requiredTables := append([]string(nil), baseBackupTables...)
	if result.SchemaVersion >= 2 {
		requiredTables = append(requiredTables, "administrators")
	}
	if result.SchemaVersion >= 3 {
		requiredTables = append(requiredTables, "sessions")
	}
	if result.SchemaVersion >= 4 {
		requiredTables = append(requiredTables, "security_audit_events")
	}
	for _, table := range requiredTables {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema
			WHERE type='table' AND name=?`, table).Scan(&count); err != nil ||
			count != 1 {
			return Result{}, errors.New("backup required tables are incomplete")
		}
	}
	if result.SchemaVersion >= 3 {
		var sessions int
		if err := db.QueryRowContext(
			ctx, "SELECT count(*) FROM sessions",
		).Scan(&sessions); err != nil || sessions != 0 {
			return Result{}, errors.New("backup contains browser sessions")
		}
	}
	if result.Type == TypeConfiguration {
		excludedTables := []string{"log_events", "container_instances"}
		if result.SchemaVersion >= 4 {
			excludedTables = append(
				excludedTables, "security_audit_events",
			)
		}
		for _, table := range excludedTables {
			var count int
			if err := db.QueryRowContext(
				ctx, "SELECT count(*) FROM "+table,
			).Scan(&count); err != nil || count != 0 {
				return Result{}, errors.New(
					"configuration backup contains excluded history",
				)
			}
		}
	}
	result.CreatedAt = time.UnixMicro(createdAtUS).UTC()
	result.Name = info.Name()
	result.SHA256, result.Bytes, err = checksum(path)
	if err != nil {
		return Result{}, err
	}
	if err := result.Validate(); err != nil {
		return Result{}, err
	}
	return result, nil
}
