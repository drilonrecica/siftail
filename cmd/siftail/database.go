package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/drilonrecica/siftail/internal/config"
	"github.com/drilonrecica/siftail/internal/database"
	statusstate "github.com/drilonrecica/siftail/internal/status"
)

func runDatabaseCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("missing database subcommand")
	}
	if args[0] != "check" {
		return fmt.Errorf("unknown database subcommand %q", args[0])
	}
	flags := flag.NewFlagSet("database check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	full := flags.Bool("full", false, "")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return errors.New("usage: siftail database check [--full]")
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
	var report database.CheckReport
	if active {
		if *full {
			return errors.New("full database check requires the Siftail server to be stopped")
		}
		if err := controlRequest(
			http.MethodGet, socket, "/database/check", nil, &report,
		); err != nil {
			return errors.New("active database check failed")
		}
	} else {
		report, err = database.CheckPath(context.Background(), cfg.DatabasePath, *full)
		if err != nil {
			return safeDatabaseCheckError(err)
		}
		if active, err := controlSocketActive(socket); err != nil {
			return err
		} else if active {
			return errors.New("server became active; retry the database check")
		}
	}
	if err := report.Validate(); err != nil {
		return errors.New("database check returned an invalid result")
	}
	for _, line := range report.SummaryLines() {
		fmt.Fprintln(stdout, line)
	}
	return nil
}

func runDiagnosticsCommand(args []string, stdout io.Writer) error {
	if len(args) != 0 {
		return errors.New("usage: siftail diagnostics")
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
	if !active {
		return errors.New("diagnostics require the Siftail server to be active")
	}
	var diagnostics []statusstate.Diagnostic
	if err := controlRequest(
		http.MethodGet, socket, "/diagnostics", nil, &diagnostics,
	); err != nil {
		return errors.New("active diagnostics request failed")
	}
	if len(diagnostics) > 100 {
		return errors.New("diagnostics returned an invalid result")
	}
	for _, diagnostic := range diagnostics {
		if err := diagnostic.Validate(); err != nil {
			return errors.New("diagnostics returned an invalid result")
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s",
			diagnostic.At.UTC().Format(time.RFC3339Nano),
			diagnostic.Severity, diagnostic.Component,
			diagnostic.Category, diagnostic.Summary,
		)
		if diagnostic.RequestID != "" {
			fmt.Fprintf(stdout, "\trequest_id=%s", diagnostic.RequestID)
		}
		if diagnostic.RecoveredAt != nil {
			fmt.Fprintf(stdout, "\trecovered_at=%s",
				diagnostic.RecoveredAt.UTC().Format(time.RFC3339Nano))
		}
		fmt.Fprintln(stdout)
	}
	return nil
}

func controlSocketActive(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("cannot inspect control socket")
	}
	if info.Mode()&os.ModeSocket == 0 {
		return false, errors.New("control path exists but is not a socket")
	}
	return true, nil
}

func safeDatabaseCheckError(err error) error {
	var tooNew *database.SchemaTooNewError
	if errors.As(err, &tooNew) {
		return fmt.Errorf(
			"database schema %d is newer than supported schema %d",
			tooNew.Actual, tooNew.Supported,
		)
	}
	var category *database.CategoryError
	if errors.As(err, &category) {
		return fmt.Errorf("database check failed: %s", category.Category)
	}
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return errors.New("database check canceled")
	}
	return errors.New("database check failed: unavailable")
}
