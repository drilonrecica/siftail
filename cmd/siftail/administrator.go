package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/drilonrecica/siftail/internal/auth"
	"golang.org/x/term"
)

func runAdministratorCommand(args []string, stdin io.Reader, stdout, prompt io.Writer) error {
	if len(args) == 0 {
		return errors.New("missing admin subcommand")
	}
	switch args[0] {
	case "create":
		flags := flag.NewFlagSet("admin create", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		username := flags.String("username", "", "")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
			return errors.New("usage: siftail admin create --username <name>")
		}
		if err := auth.ValidateUsername(*username); err != nil {
			return err
		}
		password, err := readConfirmedPassword(stdin, prompt)
		if err != nil {
			return err
		}
		var administrator auth.Administrator
		if err := runAdminOperation(http.MethodPost, "/administrator", map[string]any{
			"username": *username, "password": string(password),
		}, &administrator); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "administrator created: %s\n", administrator.Username)
		return nil
	case "reset-password":
		flags := flag.NewFlagSet("admin reset-password", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
			return errors.New("usage: siftail admin reset-password")
		}
		password, err := readConfirmedPassword(stdin, prompt)
		if err != nil {
			return err
		}
		var result struct{}
		if err := runAdminOperation(http.MethodPost, "/administrator/reset-password", map[string]any{
			"password": string(password),
		}, &result); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "administrator password reset")
		return nil
	default:
		return fmt.Errorf("unknown admin subcommand %q", args[0])
	}
}

func readConfirmedPassword(stdin io.Reader, prompt io.Writer) ([]byte, error) {
	if input, ok := stdin.(*os.File); ok && term.IsTerminal(int(input.Fd())) {
		fmt.Fprint(prompt, "Password: ")
		first, err := term.ReadPassword(int(input.Fd()))
		fmt.Fprintln(prompt)
		if err != nil {
			return nil, errors.New("read password")
		}
		fmt.Fprint(prompt, "Confirm password: ")
		second, err := term.ReadPassword(int(input.Fd()))
		fmt.Fprintln(prompt)
		if err != nil {
			return nil, errors.New("read password confirmation")
		}
		return confirmedPassword(first, second)
	}

	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 1024), 1025)
	lines := make([][]byte, 0, 2)
	for scanner.Scan() {
		if len(lines) == 2 {
			return nil, errors.New("password input must contain exactly two lines")
		}
		line := bytes.TrimSuffix(scanner.Bytes(), []byte{'\r'})
		lines = append(lines, append([]byte(nil), line...))
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.New("password input exceeds the supported size")
	}
	if len(lines) != 2 {
		return nil, errors.New("password input must contain password and confirmation lines")
	}
	return confirmedPassword(lines[0], lines[1])
}

func confirmedPassword(first, second []byte) ([]byte, error) {
	if !bytes.Equal(first, second) {
		return nil, errors.New("password confirmation does not match")
	}
	if err := auth.ValidatePassword(first); err != nil {
		return nil, err
	}
	return append([]byte(nil), first...), nil
}
