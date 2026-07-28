package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"github.com/drilonrecica/siftail/internal/backup"
	"github.com/drilonrecica/siftail/internal/config"
)

func runBackupCommand(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	output := flags.String("output", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 ||
		*output == "" {
		return errors.New("usage: siftail backup --output <path>")
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
		return errors.New("full backup requires the Siftail server to be active")
	}
	var started backup.Status
	if err := controlRequest(
		http.MethodPost, socket, "/backup/full",
		struct {
			OutputPath string `json:"output_path"`
		}{OutputPath: *output},
		&started,
	); err != nil {
		return errors.New("full backup could not be started")
	}
	if err := started.Validate(); err != nil ||
		started.State != backup.StateRunning {
		return errors.New("full backup returned an invalid start result")
	}
	for {
		time.Sleep(250 * time.Millisecond)
		var status backup.Status
		if err := controlRequest(
			http.MethodGet, socket, "/backup", nil, &status,
		); err != nil {
			return errors.New("full backup status request failed")
		}
		if err := status.Validate(); err != nil || status.ID != started.ID {
			return errors.New("full backup returned an invalid status")
		}
		switch status.State {
		case backup.StateRunning:
			continue
		case backup.StateSucceeded:
			fmt.Fprintln(stdout, "backup verified")
			fmt.Fprintf(stdout, "type: %s\n", status.Result.Type)
			fmt.Fprintf(stdout, "artifact: %s\n", status.Result.Name)
			fmt.Fprintf(stdout, "bytes: %d\n", status.Result.Bytes)
			fmt.Fprintf(stdout, "sha256: %s\n", status.Result.SHA256)
			return nil
		case backup.StateCanceled:
			return errors.New("full backup canceled")
		case backup.StateFailed:
			return fmt.Errorf("full backup failed: %s", status.Category)
		default:
			return errors.New("full backup returned an invalid status")
		}
	}
}
