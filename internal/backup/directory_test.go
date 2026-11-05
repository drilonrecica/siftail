package backup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidFileNameAcceptsOnlyPlainNames(t *testing.T) {
	for _, name := range []string{
		"siftail-full.sqlite", "a", "A1", "backup_2026-11-05.sqlite",
		"x.y.z", "9", "name..sqlite", strings.Repeat("a", MaxFileNameLength),
	} {
		if !ValidFileName(name) {
			t.Errorf("ValidFileName(%q) = false", name)
		}
	}
	for _, name := range []string{
		"", ".", "..", "../siftail.db", "../../etc/passwd", "/etc/passwd",
		"/tmp/x.sqlite", "a/b", `a\b`, "./x", ".hidden", ".x.partial-00",
		"-flag", "_x", "with space", "tab\tname", "new\nline", "nul\x00",
		"ünicode", "x\u009b", "x:y", "x*", "~root", "$HOME",
		strings.Repeat("a", MaxFileNameLength+1),
	} {
		if ValidFileName(name) {
			t.Errorf("ValidFileName(%q) = true", name)
		}
	}
}

func TestGeneratedFileNameIsValidAndTyped(t *testing.T) {
	at := time.Date(2026, 11, 5, 14, 30, 12, 0, time.FixedZone("x", 3600))
	full := GeneratedFileName(TypeFull, at)
	configuration := GeneratedFileName(TypeConfiguration, at)
	if full != "siftail-full-20261105T133012Z.sqlite" ||
		configuration != "siftail-configuration-20261105T133012Z.sqlite" ||
		!ValidFileName(full) || !ValidFileName(configuration) {
		t.Fatalf("generated = %q %q", full, configuration)
	}
}

func TestNewDirectoryRequiresAbsolutePath(t *testing.T) {
	for _, path := range []string{"", "backups", "./backups", "../backups"} {
		if _, err := NewDirectory(path); err == nil {
			t.Errorf("NewDirectory(%q) accepted", path)
		}
	}
	var zero Directory
	if zero.Configured() {
		t.Fatal("zero directory is configured")
	}
	if _, err := zero.OutputPath("x.sqlite"); !errors.Is(err, ErrDirectoryUnavailable) {
		t.Fatalf("zero OutputPath error = %v", err)
	}
	if _, err := zero.ArtifactPath("x.sqlite"); !errors.Is(err, ErrDirectoryUnavailable) {
		t.Fatalf("zero ArtifactPath error = %v", err)
	}
}

func TestDirectoryOutputPathCreatesPrivateDirectoryAndStaysInside(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "backups")
	directory, err := NewDirectory(path)
	if err != nil {
		t.Fatal(err)
	}
	output, err := directory.OutputPath("siftail-full.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if output != filepath.Join(path, "siftail-full.sqlite") {
		t.Fatalf("output = %q", output)
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("backup directory = %v %v", info, err)
	}
	for _, name := range []string{
		"../escape.sqlite", "/tmp/escape.sqlite", "sub/escape.sqlite", "..",
	} {
		if _, err := directory.OutputPath(name); !errors.Is(err, ErrInvalidFileName) {
			t.Errorf("OutputPath(%q) error = %v", name, err)
		}
		if _, err := directory.ArtifactPath(name); !errors.Is(err, ErrInvalidFileName) {
			t.Errorf("ArtifactPath(%q) error = %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "nested", "escape.sqlite")); err == nil {
		t.Fatal("traversal created a file outside the backup directory")
	}
}

func TestDirectoryResolvesConfiguredSymlinkAndRefusesFileDirectory(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	directory, err := NewDirectory(link)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := directory.ArtifactPath("x.sqlite")
	if err != nil || artifact != filepath.Join(real, "x.sqlite") {
		t.Fatalf("artifact = %q %v", artifact, err)
	}

	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	notDirectory, err := NewDirectory(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := notDirectory.OutputPath("x.sqlite"); !errors.Is(err, ErrDirectoryUnavailable) {
		t.Fatalf("file OutputPath error = %v", err)
	}
	if _, err := notDirectory.ArtifactPath("x.sqlite"); !errors.Is(err, ErrDirectoryUnavailable) {
		t.Fatalf("file ArtifactPath error = %v", err)
	}
	missing, err := NewDirectory(filepath.Join(root, "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := missing.ArtifactPath("x.sqlite"); !errors.Is(err, ErrDirectoryUnavailable) {
		t.Fatalf("missing ArtifactPath error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "missing")); err == nil {
		t.Fatal("verification created the backup directory")
	}
}
