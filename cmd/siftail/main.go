package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/drilonrecica/siftail/internal/app"
	"github.com/drilonrecica/siftail/internal/config"
	"github.com/drilonrecica/siftail/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	switch cmd {
	case "version":
		runVersion()
	case "serve":
		runServe()
	case "config":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "siftail: missing config subcommand")
			printUsage()
			os.Exit(1)
		}
		sub := os.Args[2]
		switch sub {
		case "validate":
			runConfigValidate()
		default:
			fmt.Fprintf(os.Stderr, "siftail: unknown config subcommand %q\n\n", sub)
			printUsage()
			os.Exit(1)
		}
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "siftail: unknown command %q\n\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func runVersion() {
	fmt.Printf("siftail version %s\n", version.Version)
	fmt.Printf("commit: %s\n", version.Commit)
	fmt.Printf("build date: %s\n", version.BuildDate)
	fmt.Printf("go version: %s\n", version.GoVersion())
}

func runConfigValidate() {
	cfg, err := config.Parse()
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration invalid: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("configuration valid")
	fmt.Printf("data dir: %s\n", cfg.DataDir)
	fmt.Printf("ui addr: %s\n", cfg.UIAddr)
	fmt.Printf("ingest addr: %s\n", cfg.IngestAddr)
}

func runServe() {
	cfg, err := config.Parse()
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration invalid: %v\n", err)
		os.Exit(1)
	}

	logger, err := config.ConfigureLogger(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger setup failed: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	application := app.New(cfg, logger)
	if err := application.Run(ctx); err != nil {
		logger.Error("application error", "error", err)
		os.Exit(1)
	}

	logger.Info("shutdown complete")
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "Usage: siftail <command>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Commands:")
	fmt.Fprintln(os.Stderr, "  version         Print version information")
	fmt.Fprintln(os.Stderr, "  serve           Start the Siftail server")
	fmt.Fprintln(os.Stderr, "  config validate Validate process configuration without opening the database")
}
