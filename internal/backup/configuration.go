package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/drilonrecica/siftail/internal/database"
)

type configurationTable struct {
	name    string
	count   string
	selectQ string
	insertQ string
}

var configurationTables = []configurationTable{
	{
		name:  "administrators",
		count: "SELECT count(*) FROM administrators",
		selectQ: `SELECT id,username,password_hash,created_at_us,
			password_changed_at_us,disabled_at_us FROM administrators ORDER BY id`,
		insertQ: `INSERT INTO administrators(
			id,username,password_hash,created_at_us,
			password_changed_at_us,disabled_at_us
		) VALUES(?,?,?,?,?,?)`,
	},
	{
		name:  "servers",
		count: "SELECT count(*) FROM servers",
		selectQ: `SELECT id,name,hostname,created_at_us
			FROM servers ORDER BY id`,
		insertQ: `INSERT INTO servers(id,name,hostname,created_at_us)
			VALUES(?,?,?,?)`,
	},
	{
		name:  "ingestion_tokens",
		count: "SELECT count(*) FROM ingestion_tokens",
		selectQ: `SELECT id,server_id,name,token_hash,fingerprint,created_at_us,
			last_used_at_us,revoked_at_us FROM ingestion_tokens ORDER BY id`,
		insertQ: `INSERT INTO ingestion_tokens(
			id,server_id,name,token_hash,fingerprint,created_at_us,
			last_used_at_us,revoked_at_us
		) VALUES(?,?,?,?,?,?,?,?)`,
	},
	{
		name:  "settings",
		count: "SELECT count(*) FROM settings",
		selectQ: `SELECT key,value_json,updated_at_us
			FROM settings ORDER BY key`,
		insertQ: `INSERT INTO settings(key,value_json,updated_at_us)
			VALUES(?,?,?)`,
	},
	{
		name:  "sources",
		count: "SELECT count(*) FROM sources",
		selectQ: `SELECT id,server_id,project_key,environment_key,application_key,
			service_key,project_label,environment_label,application_label,
			service_label,alias,first_seen_at_us,last_seen_at_us
			FROM sources ORDER BY id`,
		insertQ: `INSERT INTO sources(
			id,server_id,project_key,environment_key,application_key,
			service_key,project_label,environment_label,application_label,
			service_label,alias,first_seen_at_us,last_seen_at_us
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
	},
}

func createConfigurationSnapshot(
	ctx context.Context,
	source *sql.DB,
	destinationPath string,
	progress func(Progress),
) error {
	if ctx == nil || source == nil || destinationPath == "" {
		return errors.New("configuration backup snapshot is unavailable")
	}
	sourceTx, err := source.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return database.Classify("begin configuration backup snapshot", err)
	}
	defer sourceTx.Rollback()
	total, err := configurationRowCount(ctx, sourceTx)
	if err != nil {
		return err
	}

	destination, err := database.Open(ctx, destinationPath)
	if err != nil {
		return fmt.Errorf("initialize configuration backup: %w", err)
	}
	if err := destination.Close(); err != nil {
		return database.Classify("close initialized configuration backup", err)
	}
	output, err := sql.Open("sqlite3", sqlitePath(destinationPath, "rw"))
	if err != nil {
		return errors.New("open configuration backup output")
	}
	output.SetMaxOpenConns(1)
	output.SetMaxIdleConns(1)
	defer func() { _ = output.Close() }()
	if _, err := output.ExecContext(ctx, `
		PRAGMA busy_timeout=5000;
		PRAGMA synchronous=FULL;
		PRAGMA foreign_keys=ON;
		PRAGMA secure_delete=ON;
		PRAGMA journal_mode=DELETE;
	`); err != nil {
		return database.Classify("configure configuration backup output", err)
	}
	outputTx, err := output.BeginTx(ctx, nil)
	if err != nil {
		return database.Classify("begin configuration backup output", err)
	}
	defer outputTx.Rollback()
	completed := 0
	for _, table := range configurationTables {
		copied, err := copyConfigurationTable(
			ctx, sourceTx, outputTx, table, completed, total, progress,
		)
		if err != nil {
			return err
		}
		completed += copied
	}
	if err := outputTx.Commit(); err != nil {
		return database.Classify("commit configuration backup rows", err)
	}
	if err := output.Close(); err != nil {
		return database.Classify("close configuration backup output", err)
	}
	if err := sourceTx.Commit(); err != nil {
		return database.Classify("finish configuration backup snapshot", err)
	}
	if progress != nil {
		progress(Progress{Completed: total, Total: total, Unit: "rows"})
	}
	return nil
}

func configurationRowCount(ctx context.Context, source *sql.Tx) (int, error) {
	total := 0
	for _, table := range configurationTables {
		var count int
		if err := source.QueryRowContext(ctx, table.count).Scan(&count); err != nil {
			return 0, database.Classify(
				"count configuration backup "+table.name, err,
			)
		}
		if count < 0 || total > int(^uint(0)>>1)-count {
			return 0, errors.New("configuration backup row count is unsupported")
		}
		total += count
	}
	return total, nil
}

func copyConfigurationTable(
	ctx context.Context,
	source *sql.Tx,
	destination *sql.Tx,
	table configurationTable,
	completed int,
	total int,
	progress func(Progress),
) (int, error) {
	rows, err := source.QueryContext(ctx, table.selectQ)
	if err != nil {
		return 0, database.Classify(
			"read configuration backup "+table.name, err,
		)
	}
	defer rows.Close()
	columnTypes, err := rows.ColumnTypes()
	if err != nil || len(columnTypes) == 0 {
		return 0, errors.New("inspect configuration backup columns")
	}
	statement, err := destination.PrepareContext(ctx, table.insertQ)
	if err != nil {
		return 0, database.Classify(
			"prepare configuration backup "+table.name, err,
		)
	}
	defer statement.Close()
	copied := 0
	for rows.Next() {
		values := make([]any, len(columnTypes))
		destinations := make([]any, len(columnTypes))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return copied, database.Classify(
				"scan configuration backup "+table.name, err,
			)
		}
		if _, err := statement.ExecContext(ctx, values...); err != nil {
			return copied, database.Classify(
				"write configuration backup "+table.name, err,
			)
		}
		copied++
		if progress != nil {
			progress(Progress{
				Completed: completed + copied, Total: total, Unit: "rows",
			})
		}
	}
	if err := rows.Err(); err != nil {
		return copied, database.Classify(
			"iterate configuration backup "+table.name, err,
		)
	}
	return copied, nil
}
