package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/drilonrecica/siftail/internal/app"
	"github.com/drilonrecica/siftail/internal/config"
	"github.com/drilonrecica/siftail/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 1
	}

	cmd := args[0]
	switch cmd {
	case "version":
		runVersion(stdout)
		return 0
	case "serve":
		if err := runServe(); err != nil {
			fmt.Fprintf(stderr, "siftail: %v\n", err)
			return 1
		}
		return 0
	case "config":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "siftail: missing config subcommand")
			printUsage(stderr)
			return 1
		}
		sub := args[1]
		switch sub {
		case "validate":
			if err := runConfigValidate(stdout); err != nil {
				fmt.Fprintf(stderr, "configuration invalid: %v\n", err)
				return 1
			}
			return 0
		default:
			fmt.Fprintf(stderr, "siftail: unknown config subcommand %q\n\n", sub)
			printUsage(stderr)
			return 1
		}
	case "server":
		if err := runServerCommand(args[1:], stdout); err != nil {
			fmt.Fprintf(stderr, "siftail: %v\n", err)
			return 1
		}
		return 0
	case "token":
		if err := runTokenCommand(args[1:], stdout); err != nil {
			fmt.Fprintf(stderr, "siftail: %v\n", err)
			return 1
		}
		return 0
	case "admin":
		if err := runAdministratorCommand(args[1:], os.Stdin, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "siftail: %v\n", err)
			return 1
		}
		return 0
	case "sessions":
		if err := runSessionCommand(args[1:], stdout); err != nil {
			fmt.Fprintf(stderr, "siftail: %v\n", err)
			return 1
		}
		return 0
	case "database":
		if err := runDatabaseCommand(args[1:], stdout); err != nil {
			fmt.Fprintf(stderr, "siftail: %v\n", err)
			return 1
		}
		return 0
	case "diagnostics":
		if err := runDiagnosticsCommand(args[1:], stdout); err != nil {
			fmt.Fprintf(stderr, "siftail: %v\n", err)
			return 1
		}
		return 0
	case "backup":
		if err := runBackupCommand(args[1:], stdout); err != nil {
			fmt.Fprintf(stderr, "siftail: %v\n", err)
			return 1
		}
		return 0
	case "restore":
		if err := runRestoreCommand(args[1:], stdout); err != nil {
			fmt.Fprintf(stderr, "siftail: %v\n", err)
			return 1
		}
		return 0
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "siftail: unknown command %q\n\n", cmd)
		printUsage(stderr)
		return 1
	}
}

func runVersion(stdout io.Writer) {
	fmt.Fprintf(stdout, "siftail version %s\n", version.Version)
	fmt.Fprintf(stdout, "commit: %s\n", version.Commit)
	fmt.Fprintf(stdout, "build date: %s\n", version.BuildDate)
	fmt.Fprintf(stdout, "go version: %s\n", version.GoVersion())
}

func runConfigValidate(stdout io.Writer) error {
	cfg, err := config.Parse()
	if err != nil {
		return err
	}
	if err := cfg.IsWritableDataDir(); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "configuration valid")
	fmt.Fprintf(stdout, "data dir: %s\n", cfg.DataDir)
	fmt.Fprintf(stdout, "ui addr: %s\n", cfg.UIAddr)
	fmt.Fprintf(stdout, "ingest addr: %s\n", cfg.IngestAddr)
	return nil
}

func runServe() error {
	cfg, err := config.Parse()
	if err != nil {
		return fmt.Errorf("configuration invalid: %w", err)
	}

	logger, err := config.ConfigureLogger(cfg)
	if err != nil {
		return fmt.Errorf("logger setup failed: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	application := app.New(cfg, logger)
	if err := application.Run(ctx); err != nil {
		logger.Error("application stopped", "error_category", "critical_component")
		return fmt.Errorf("application stopped unexpectedly")
	}

	logger.Info("shutdown complete")
	return nil
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: siftail <command>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  version         Print version information")
	fmt.Fprintln(w, "  serve           Start the Siftail server")
	fmt.Fprintln(w, "  config validate Validate process configuration without opening the database")
	fmt.Fprintln(w, "  server create    Create a trusted Server")
	fmt.Fprintln(w, "  server list      List trusted Servers")
	fmt.Fprintln(w, "  token create     Create a one-time token or generated ingestion material")
	fmt.Fprintln(w, "  token revoke     Revoke an ingestion token")
	fmt.Fprintln(w, "  admin create      Create the single administrator")
	fmt.Fprintln(w, "  admin reset-password Reset the administrator password")
	fmt.Fprintln(w, "  sessions revoke-all Revoke every administrator session")
	fmt.Fprintln(w, "  database check [--full] Run a bounded database integrity check")
	fmt.Fprintln(w, "  diagnostics      Print the latest sanitized operational diagnostics")
	fmt.Fprintln(w, "  backup [--configuration-only] --output <path> Create and verify a backup")
	fmt.Fprintln(w, "  backup verify <path> Verify a backup without applying changes")
	fmt.Fprintln(w, "  restore --confirm RESTORE <path> Replace the stopped-server database")
}
