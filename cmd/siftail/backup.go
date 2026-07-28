package main

import (
	"context"
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
	if len(args) > 0 && args[0] == "verify" {
		if len(args) != 2 || args[1] == "" {
			return errors.New("usage: siftail backup verify <path>")
		}
		return runBackupVerify(args[1], stdout)
	}
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	output := flags.String("output", "", "")
	configurationOnly := flags.Bool("configuration-only", false, "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 ||
		*output == "" {
		return errors.New("usage: siftail backup [--configuration-only] --output <path>")
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
		return errors.New("backup creation requires the Siftail server to be active")
	}
	endpoint := "/backup/full"
	if *configurationOnly {
		endpoint = "/backup/configuration"
	}
	result, err := runActiveBackupOperation(
		socket, endpoint,
		struct {
			OutputPath string `json:"output_path"`
		}{OutputPath: *output},
		"backup",
	)
	if err != nil {
		return err
	}
	printBackupResult(stdout, result)
	return nil
}

func runBackupVerify(path string, stdout io.Writer) error {
	cfg, err := config.Parse()
	if err != nil {
		return fmt.Errorf("configuration invalid: %w", err)
	}
	socket := filepath.Join(cfg.DataDir, "siftail-control.sock")
	active, err := controlSocketActive(socket)
	if err != nil {
		return err
	}
	var result backup.Result
	if active {
		result, err = runActiveBackupOperation(
			socket, "/backup/verify",
			struct {
				ArtifactPath string `json:"artifact_path"`
			}{ArtifactPath: path},
			"backup verification",
		)
	} else {
		result, err = backup.Verify(context.Background(), path)
		if err != nil {
			err = errors.New("backup verification failed")
		}
	}
	if err != nil {
		return err
	}
	printBackupResult(stdout, result)
	return nil
}

func runActiveBackupOperation(
	socket string,
	endpoint string,
	input any,
	name string,
) (backup.Result, error) {
	var started backup.Status
	if err := controlRequest(
		http.MethodPost, socket, endpoint, input, &started,
	); err != nil {
		return backup.Result{}, fmt.Errorf("%s could not be started", name)
	}
	if err := started.Validate(); err != nil ||
		started.State != backup.StateRunning {
		return backup.Result{}, fmt.Errorf(
			"%s returned an invalid start result", name,
		)
	}
	for {
		time.Sleep(250 * time.Millisecond)
		var status backup.Status
		if err := controlRequest(
			http.MethodGet, socket, "/backup", nil, &status,
		); err != nil {
			return backup.Result{}, fmt.Errorf("%s status request failed", name)
		}
		if err := status.Validate(); err != nil || status.ID != started.ID {
			return backup.Result{}, fmt.Errorf(
				"%s returned an invalid status", name,
			)
		}
		switch status.State {
		case backup.StateRunning:
			continue
		case backup.StateSucceeded:
			return *status.Result, nil
		case backup.StateCanceled:
			return backup.Result{}, fmt.Errorf("%s canceled", name)
		case backup.StateFailed:
			return backup.Result{}, fmt.Errorf(
				"%s failed: %s", name, status.Category,
			)
		default:
			return backup.Result{}, fmt.Errorf(
				"%s returned an invalid status", name,
			)
		}
	}
}

func printBackupResult(stdout io.Writer, result backup.Result) {
	fmt.Fprintln(stdout, "backup verified")
	fmt.Fprintf(stdout, "type: %s\n", result.Type)
	fmt.Fprintf(stdout, "artifact: %s\n", result.Name)
	fmt.Fprintf(stdout, "bytes: %d\n", result.Bytes)
	fmt.Fprintf(stdout, "sha256: %s\n", result.SHA256)
}
