package main

import (
	"fmt"
	"os"

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

func printUsage() {
	fmt.Fprintln(os.Stderr, "Usage: siftail <command>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Commands:")
	fmt.Fprintln(os.Stderr, "  version   Print version information")
}
