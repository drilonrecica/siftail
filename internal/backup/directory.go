package backup

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

// MaxFileNameLength bounds a browser-submitted backup file name.
const MaxFileNameLength = 128

// ErrInvalidFileName reports a browser-submitted backup name that is not a
// plain file name.
var ErrInvalidFileName = errors.New("backup file name is invalid")

// ErrDirectoryUnavailable reports that the configured backup directory cannot
// be created, resolved, or used.
var ErrDirectoryUnavailable = errors.New("backup directory is unavailable")

// Directory confines browser-initiated backup operations to the one
// operator-configured directory, SIFTAIL_BACKUP_DIR (audit SEC-11). The
// browser submits only a plain file name, so a stolen session cannot write a
// database copy to an arbitrary path or probe the filesystem. The CLI does not
// use Directory and keeps arbitrary server-side paths over the owner-only
// control socket.
type Directory struct {
	path string
}

// NewDirectory accepts an absolute backup directory path.
func NewDirectory(path string) (Directory, error) {
	if path == "" || !filepath.IsAbs(path) {
		return Directory{}, errors.New("backup directory must be an absolute path")
	}
	return Directory{path: filepath.Clean(path)}, nil
}

// Configured reports whether d was built by NewDirectory.
func (d Directory) Configured() bool {
	return d.path != ""
}

// OutputPath creates the backup directory with mode 0700 when it is missing
// and returns the destination for a new artifact named name. Creation still
// refuses an existing file or symbolic link at that path.
func (d Directory) OutputPath(name string) (string, error) {
	if !ValidFileName(name) {
		return "", ErrInvalidFileName
	}
	if !d.Configured() {
		return "", ErrDirectoryUnavailable
	}
	if err := os.MkdirAll(d.path, 0o700); err != nil {
		return "", ErrDirectoryUnavailable
	}
	return d.join(name)
}

// ArtifactPath returns the path of an existing artifact named name inside the
// backup directory. Verification itself refuses symbolic links and anything
// other than a regular owner-private file.
func (d Directory) ArtifactPath(name string) (string, error) {
	if !ValidFileName(name) {
		return "", ErrInvalidFileName
	}
	if !d.Configured() {
		return "", ErrDirectoryUnavailable
	}
	return d.join(name)
}

// join resolves the configured directory itself (which the operator may point
// at through a symbolic link) and appends the separator-free name, so the
// result is always a direct child of the real directory.
func (d Directory) join(name string) (string, error) {
	resolved, err := filepath.EvalSymlinks(d.path)
	if err != nil {
		return "", ErrDirectoryUnavailable
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", ErrDirectoryUnavailable
	}
	return filepath.Join(resolved, name), nil
}

// ValidFileName accepts 1–128 ASCII letters, digits, '.', '_' and '-',
// starting with a letter or digit. It therefore rejects separators, "." and
// "..", hidden files (including Siftail's own staging files), leading
// dashes, whitespace, control characters, and non-ASCII text.
func ValidFileName(name string) bool {
	if name == "" || len(name) > MaxFileNameLength {
		return false
	}
	for index := 0; index < len(name); index++ {
		character := name[index]
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9':
		case index > 0 &&
			(character == '.' || character == '_' || character == '-'):
		default:
			return false
		}
	}
	return true
}

// GeneratedFileName names a browser backup when the administrator leaves the
// name empty, for example siftail-full-20261105T143012Z.sqlite.
func GeneratedFileName(backupType string, at time.Time) string {
	kind := TypeFull
	if backupType == TypeConfiguration {
		kind = TypeConfiguration
	}
	return "siftail-" + kind + "-" + at.UTC().Format("20060102T150405Z") +
		".sqlite"
}
