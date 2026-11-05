package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// secretSettings is the registry of process settings declared secret
// (ADR 0010 §17). Only a name listed here has a SIFTAIL_X_FILE variant; any
// other *_FILE name is an unknown reserved variable. Version 0.5.0 declares
// none. Tests register a name temporarily to exercise the mechanism so the
// first real secret inherits it unchanged.
var secretSettings []string

// maxSecretFileBytes caps a secret file (ADR 0010 §19).
const maxSecretFileBytes = 64 * 1024

// SecretSource says where a secret value came from. It is the only fact about
// a secret that sanitized configuration and diagnostics may show.
type SecretSource string

const (
	SecretFromEnvironment SecretSource = "environment"
	SecretFromFile        SecretSource = "file"
)

// Secret holds a secret setting's value. Every formatting path (fmt verbs,
// JSON, text marshaling, slog) yields "[redacted]"; only Reveal returns the
// value, for the one component that uses it (ADR 0010 §22). The value sits
// behind a closure because fmt does not call String on a Secret reached
// through an unexported field, such as Config's: reflection then prints the
// fields, and a func field prints only as an address.
type Secret struct {
	value  func() string
	source SecretSource
}

func newSecret(value string, source SecretSource) Secret {
	return Secret{value: func() string { return value }, source: source}
}

// Reveal returns the secret value. Call it only where the value is used.
func (s Secret) Reveal() string {
	if s.value == nil {
		return ""
	}
	return s.value()
}

// Source reports whether the value came from the environment or a file.
func (s Secret) Source() SecretSource { return s.source }

func (s Secret) String() string {
	if s.source == "" {
		return ""
	}
	return "[redacted]"
}

func (s Secret) GoString() string { return s.String() }

func (s Secret) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

func (s Secret) LogValue() slog.Value { return slog.StringValue(s.String()) }

// redacted returns a copy without the value, keeping only its source.
func (s Secret) redacted() Secret { return Secret{source: s.source} }

// readSecret reads the secret setting name from either name or name_FILE,
// enforcing the ADR 0010 contract: no precedence, empty values are invalid,
// the file is an absolute path to a regular, not group- or world-writable
// file of at most 64 KiB of UTF-8 without NUL, trailing CR/LF are removed and
// nothing else, and an interior line break is an error. Errors name the
// variables and the path but never the content or its length. present is
// false when neither variable is set.
func readSecret(name string) (secret Secret, present bool, err error) {
	fileName := name + "_FILE"
	direct, hasDirect := os.LookupEnv(name)
	path, hasFile := os.LookupEnv(fileName)

	switch {
	case hasDirect && hasFile:
		return Secret{}, false, fmt.Errorf("%s and %s are both set; set only one of them", name, fileName)
	case hasDirect:
		if err := validateSecretValue(direct); err != nil {
			return Secret{}, false, fmt.Errorf("%s %w", name, err)
		}
		return newSecret(direct, SecretFromEnvironment), true, nil
	case hasFile:
		value, err := readSecretFile(path)
		if err != nil {
			return Secret{}, false, fmt.Errorf("%s (%q): %w", fileName, path, err)
		}
		if err := validateSecretValue(value); err != nil {
			return Secret{}, false, fmt.Errorf("%s (%q): file %w", fileName, path, err)
		}
		return newSecret(value, SecretFromFile), true, nil
	}
	return Secret{}, false, nil
}

// readSecretFile returns the file's content with all trailing CR and LF bytes
// removed. The file is checked by path first, so a FIFO or device is refused
// before open can block, and again on the opened descriptor, so the checked
// file is the one read.
func readSecretFile(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("must be an absolute path")
	}
	info, err := os.Stat(path) // follows symlinks, as secret mounts require
	if err != nil {
		return "", fmt.Errorf("cannot read file: %w", pathErrorCause(err))
	}
	if err := checkSecretFileInfo(info); err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("cannot read file: %w", pathErrorCause(err))
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return "", fmt.Errorf("cannot read file: %w", pathErrorCause(err))
	}
	if err := checkSecretFileInfo(info); err != nil {
		return "", err
	}
	content, err := io.ReadAll(io.LimitReader(file, maxSecretFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("cannot read file: %w", pathErrorCause(err))
	}
	if len(content) > maxSecretFileBytes {
		return "", errors.New("file exceeds 64 KiB")
	}
	if !utf8.Valid(content) {
		return "", errors.New("file is not valid UTF-8")
	}
	return strings.TrimRight(string(content), "\r\n"), nil
}

func checkSecretFileInfo(info fs.FileInfo) error {
	if !info.Mode().IsRegular() {
		return errors.New("must name a regular file")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("file must not be group- or world-writable; use mode 0400 or 0440")
	}
	if info.Size() > maxSecretFileBytes {
		return errors.New("file exceeds 64 KiB")
	}
	return nil
}

// validateSecretValue applies the rules shared by both forms: a secret is one
// non-empty line of UTF-8 without NUL. Messages follow the variable name.
func validateSecretValue(value string) error {
	switch {
	case value == "":
		return errors.New("is empty")
	case strings.ContainsRune(value, 0):
		return errors.New("contains a NUL byte")
	case strings.ContainsAny(value, "\r\n"):
		return errors.New("contains a line break; a secret must be one line")
	case !utf8.ValidString(value):
		return errors.New("is not valid UTF-8")
	}
	return nil
}

// pathErrorCause drops the path an *fs.PathError repeats, since the caller
// already names it.
func pathErrorCause(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}
