package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/drilonrecica/siftail/internal/backup"
	"github.com/drilonrecica/siftail/internal/config"
)

func runRestoreCommand(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	confirmation := flags.String("confirm", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 1 {
		return errors.New(
			"usage: siftail restore --confirm RESTORE <backup-path>",
		)
	}
	if *confirmation != backup.RestoreConfirmation {
		return errors.New(
			"restore requires --confirm RESTORE",
		)
	}
	cfg, err := config.Parse()
	if err != nil {
		return fmt.Errorf("configuration invalid: %w", err)
	}
	socket := filepath.Join(cfg.DataDir, "siftail-control.sock")
	active, err := controlSocketActive(socket)
	if err != nil {
		return err
	}
	if active {
		return errors.New(
			"restore requires the Siftail server to be stopped",
		)
	}
	result, err := backup.NewRestorer().Restore(
		context.Background(), backup.RestoreOptions{
			DataDirectory: cfg.DataDir,
			DatabasePath:  cfg.DatabasePath,
			ArtifactPath:  flags.Arg(0),
			Confirmation:  *confirmation,
		},
	)
	if err != nil {
		return safeRestoreError(err)
	}
	fmt.Fprintln(stdout, "restore complete")
	fmt.Fprintf(stdout, "type: %s\n", result.Type)
	fmt.Fprintf(stdout, "artifact: %s\n", result.ArtifactName)
	fmt.Fprintf(stdout, "rollback: %s\n", result.RollbackName)
	fmt.Fprintf(stdout, "schema: %d\n", result.SchemaAfter)
	fmt.Fprintf(stdout, "migrated: %t\n", result.Migrated)
	fmt.Fprintln(stdout, "fresh login required: true")
	return nil
}

func safeRestoreError(err error) error {
	switch err.Error() {
	case "restore requires the Siftail server to be stopped",
		"restore requires exact RESTORE confirmation",
		"restore artifact verification failed",
		"current database is not a compatible rollback source",
		"restore has insufficient destination capacity",
		"an incomplete restore staging directory requires recovery",
		"restore completed but staging cleanup requires manual recovery",
		"restore failed and rollback requires manual recovery":
		return err
	default:
		return errors.New(
			"restore failed; the active database was preserved or recovered",
		)
	}
}
