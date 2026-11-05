package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
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
	socket := controlSocketPath(cfg.DataDir)
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
	socket := controlSocketPath(cfg.DataDir)
	if err := requireActiveControlSocket(
		socket, "diagnostics require the Siftail server to be active",
	); err != nil {
		return err
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

const controlSocketName = "siftail-control.sock"

// controlProbeTimeout bounds the liveness probe. A listening server accepts
// a Unix connection from its backlog without application work, so only a
// wedged or overloaded process gets near this.
const controlProbeTimeout = 2 * time.Second

var errDatabaseBusy = errors.New(
	"the Siftail database is in use by a starting server or a running restore; retry the command",
)

type controlSocketState int

const (
	controlSocketAbsent controlSocketState = iota
	controlSocketLive
	controlSocketStale
)

func controlSocketPath(dataDirectory string) string {
	return filepath.Join(dataDirectory, controlSocketName)
}

// probeControlSocket decides server liveness by connecting to the control
// socket. A socket file nobody accepts on (left behind by SIGKILL or a
// crash) is stale, not live.
func probeControlSocket(path string) (controlSocketState, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return controlSocketAbsent, nil
	}
	if err != nil {
		return controlSocketAbsent, errors.New("cannot inspect control socket")
	}
	if info.Mode()&os.ModeSocket == 0 {
		return controlSocketAbsent, errors.New("control path exists but is not a socket")
	}
	conn, err := net.DialTimeout("unix", path, controlProbeTimeout)
	if err == nil {
		_ = conn.Close()
		return controlSocketLive, nil
	}
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return controlSocketStale, nil
	case errors.Is(err, syscall.ENOENT):
		return controlSocketAbsent, nil
	case errors.Is(err, os.ErrDeadlineExceeded):
		return controlSocketAbsent, errors.New(
			"control socket did not accept a connection; the server may be overloaded, retry the command",
		)
	default:
		return controlSocketAbsent, errors.New("cannot connect to control socket")
	}
}

// controlSocketActive reports whether a server is listening. A stale socket
// counts as inactive, so stopped-server commands keep working after SIGKILL.
func controlSocketActive(path string) (bool, error) {
	state, err := probeControlSocket(path)
	return state == controlSocketLive, err
}

// requireActiveControlSocket is for commands that only work through a
// running server; a stale socket is reported with its recovery.
func requireActiveControlSocket(path, requirement string) error {
	state, err := probeControlSocket(path)
	if err != nil {
		return err
	}
	switch state {
	case controlSocketLive:
		return nil
	case controlSocketStale:
		return fmt.Errorf("%s; %s", requirement, staleSocketMessage(path))
	default:
		return errors.New(requirement)
	}
}

func staleSocketMessage(path string) string {
	return fmt.Sprintf(
		"stale control socket (no Siftail server is listening): "+
			"starting the server replaces it, or remove it with: rm %s",
		path,
	)
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
