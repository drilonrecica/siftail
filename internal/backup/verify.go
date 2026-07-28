package backup

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
)

var requiredFullTables = []string{
	"administrators",
	"container_instances",
	"ingestion_tokens",
	"log_events",
	"schema_migrations",
	"security_audit_events",
	"servers",
	"sessions",
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
	if formatVersion != FormatVersion || result.Type != TypeFull ||
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
	for _, table := range requiredFullTables {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema
			WHERE type='table' AND name=?`, table).Scan(&count); err != nil ||
			count != 1 {
			return Result{}, errors.New("backup required tables are incomplete")
		}
	}
	var sessions int
	if err := db.QueryRowContext(
		ctx, "SELECT count(*) FROM sessions",
	).Scan(&sessions); err != nil || sessions != 0 {
		return Result{}, errors.New("backup contains browser sessions")
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
