package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testSecretName is registered only by tests: version 0.5.0 declares no secret
// setting, so the mechanism is exercised through a test-only registration
// (ADR 0010 §17).
const (
	testSecretName     = "SIFTAIL_TEST_SECRET"
	testSecretFileName = "SIFTAIL_TEST_SECRET_FILE"
	secretMarker       = "s3cr3t-marker"
)

func registerTestSecret(t *testing.T) {
	t.Helper()
	previous := secretSettings
	secretSettings = append([]string(nil), previous...)
	secretSettings = append(secretSettings, testSecretName)
	t.Cleanup(func() { secretSettings = previous })
}

func TestProductionDeclaresNoSecretSetting(t *testing.T) {
	if len(secretSettings) != 0 {
		t.Fatalf("secretSettings = %v; 0.5.0 declares none (ADR 0010 §17)", secretSettings)
	}
}

func writeSecretFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Chmod after writing so the process umask cannot mask the mode under test.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	return path
}

func assertNoSecretInError(t *testing.T, err error) {
	t.Helper()
	if strings.Contains(err.Error(), secretMarker) {
		t.Fatalf("error leaked secret content: %v", err)
	}
}

func TestReadSecretAbsent(t *testing.T) {
	clearEnv(t)
	_, present, err := readSecret(testSecretName)
	if err != nil || present {
		t.Fatalf("readSecret absent = %v, %v; want not present, nil", present, err)
	}
}

func TestReadSecretDirect(t *testing.T) {
	clearEnv(t)
	setEnv(t, testSecretName, " "+secretMarker+"\t")
	secret, present, err := readSecret(testSecretName)
	if err != nil || !present {
		t.Fatalf("readSecret = %v, %v", present, err)
	}
	if secret.Reveal() != " "+secretMarker+"\t" || secret.Source() != SecretFromEnvironment {
		t.Fatalf("secret = %q from %q", secret.Reveal(), secret.Source())
	}
}

func TestReadSecretFileTrimsOnlyTrailingCRLF(t *testing.T) {
	for name, content := range map[string]string{
		"lf":       secretMarker + "\n",
		"crlf":     secretMarker + "\r\n",
		"cr":       secretMarker + "\r",
		"repeated": secretMarker + "\r\n\n\r\n",
		"none":     secretMarker,
	} {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			setEnv(t, testSecretFileName, writeSecretFile(t, content, 0o400))
			secret, present, err := readSecret(testSecretName)
			if err != nil || !present {
				t.Fatalf("readSecret = %v, %v", present, err)
			}
			if secret.Reveal() != secretMarker || secret.Source() != SecretFromFile {
				t.Fatalf("secret = %q from %q", secret.Reveal(), secret.Source())
			}
		})
	}

	clearEnv(t)
	setEnv(t, testSecretFileName, writeSecretFile(t, "  "+secretMarker+" \t\n", 0o400))
	secret, _, err := readSecret(testSecretName)
	if err != nil {
		t.Fatalf("readSecret: %v", err)
	}
	if secret.Reveal() != "  "+secretMarker+" \t" {
		t.Fatalf("spaces and tabs must be kept, got %q", secret.Reveal())
	}
}

func TestReadSecretFileFollowsSymlink(t *testing.T) {
	clearEnv(t)
	target := writeSecretFile(t, secretMarker+"\n", 0o444)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	setEnv(t, testSecretFileName, link)
	secret, _, err := readSecret(testSecretName)
	if err != nil || secret.Reveal() != secretMarker {
		t.Fatalf("readSecret through symlink = %q, %v", secret.Reveal(), err)
	}
}

// Docker and Kubernetes mount secrets 0444/0644; readable modes are accepted.
func TestReadSecretFileAcceptsReadableModes(t *testing.T) {
	for _, mode := range []os.FileMode{0o400, 0o440, 0o444, 0o600, 0o644} {
		t.Run(mode.String(), func(t *testing.T) {
			clearEnv(t)
			setEnv(t, testSecretFileName, writeSecretFile(t, secretMarker, mode))
			if _, _, err := readSecret(testSecretName); err != nil {
				t.Fatalf("mode %v refused: %v", mode, err)
			}
		})
	}
}

func TestReadSecretFileRejectsWritableByOthers(t *testing.T) {
	for _, mode := range []os.FileMode{0o620, 0o602, 0o666} {
		t.Run(mode.String(), func(t *testing.T) {
			clearEnv(t)
			setEnv(t, testSecretFileName, writeSecretFile(t, secretMarker, mode))
			_, _, err := readSecret(testSecretName)
			if err == nil || !strings.Contains(err.Error(), "group- or world-writable") {
				t.Fatalf("mode %v error = %v", mode, err)
			}
			assertNoSecretInError(t, err)
		})
	}
}

func TestReadSecretConflict(t *testing.T) {
	file := writeSecretFile(t, secretMarker, 0o400)
	for name, direct := range map[string]string{"both set": secretMarker, "direct empty": ""} {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			setEnv(t, testSecretName, direct)
			setEnv(t, testSecretFileName, file)
			_, _, err := readSecret(testSecretName)
			if err == nil {
				t.Fatal("expected conflict error")
			}
			if !strings.Contains(err.Error(), testSecretName+" and "+testSecretFileName) {
				t.Fatalf("error does not name both variables: %v", err)
			}
			assertNoSecretInError(t, err)
		})
	}

	clearEnv(t)
	setEnv(t, testSecretName, secretMarker)
	setEnv(t, testSecretFileName, "")
	if _, _, err := readSecret(testSecretName); err == nil {
		t.Fatal("expected conflict error with an empty _FILE variable")
	}
}

func TestReadSecretInvalidValues(t *testing.T) {
	tooLarge := strings.Repeat("a", maxSecretFileBytes+1)
	fileCases := map[string]struct {
		content string
		want    string
	}{
		"empty":             {"", "is empty"},
		"only line breaks":  {"\r\n\n", "is empty"},
		"interior LF":       {secretMarker + "\nsecond", "line break"},
		"interior CR":       {secretMarker + "\rsecond", "line break"},
		"NUL":               {secretMarker + "\x00", "NUL"},
		"invalid UTF-8":     {secretMarker + "\xff", "UTF-8"},
		"larger than limit": {tooLarge, "exceeds 64 KiB"},
	}
	for name, tc := range fileCases {
		t.Run("file "+name, func(t *testing.T) {
			clearEnv(t)
			path := writeSecretFile(t, tc.content, 0o400)
			setEnv(t, testSecretFileName, path)
			_, _, err := readSecret(testSecretName)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), testSecretFileName) || !strings.Contains(err.Error(), path) {
				t.Fatalf("error does not name variable and path: %v", err)
			}
			assertNoSecretInError(t, err)
			if strings.Contains(err.Error(), fmt.Sprint(maxSecretFileBytes+1)) {
				t.Fatalf("error disclosed content length: %v", err)
			}
		})
	}

	exactLimit := strings.Repeat("a", maxSecretFileBytes)
	clearEnv(t)
	setEnv(t, testSecretFileName, writeSecretFile(t, exactLimit, 0o400))
	if _, _, err := readSecret(testSecretName); err != nil {
		t.Fatalf("64 KiB file refused: %v", err)
	}

	for name, value := range map[string]string{
		"empty": "", "interior LF": secretMarker + "\nx", "trailing LF": secretMarker + "\n",
	} {
		t.Run("direct "+name, func(t *testing.T) {
			clearEnv(t)
			setEnv(t, testSecretName, value)
			_, _, err := readSecret(testSecretName)
			if err == nil || !strings.HasPrefix(err.Error(), testSecretName+" ") {
				t.Fatalf("error = %v", err)
			}
			assertNoSecretInError(t, err)
		})
	}
}

func TestReadSecretFilePathErrors(t *testing.T) {
	dir := t.TempDir()
	for name, tc := range map[string]struct{ path, want string }{
		"missing":   {filepath.Join(dir, "missing"), "cannot read file"},
		"relative":  {"run/secrets/siftail", "absolute path"},
		"empty":     {"", "absolute path"},
		"directory": {dir, "regular file"},
	} {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			setEnv(t, testSecretFileName, tc.path)
			_, _, err := readSecret(testSecretName)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), testSecretFileName) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestParseRegisteredSecretAndSanitized(t *testing.T) {
	registerTestSecret(t)
	clearEnv(t)
	setEnv(t, testSecretFileName, writeSecretFile(t, secretMarker+"\n", 0o440))

	cfg, err := Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	secret, ok := cfg.Secret(testSecretName)
	if !ok || secret.Reveal() != secretMarker {
		t.Fatalf("Secret = %q, %v", secret.Reveal(), ok)
	}
	if got := cfg.SecretSources(); len(got) != 1 || got[testSecretName] != SecretFromFile {
		t.Fatalf("SecretSources = %v", got)
	}

	sanitized := cfg.Sanitized()
	redacted, ok := sanitized.Secret(testSecretName)
	if !ok || redacted.Reveal() != "" || redacted.Source() != SecretFromFile || redacted.String() != "[redacted]" {
		t.Fatalf("sanitized secret = %q from %q (%s)", redacted.Reveal(), redacted.Source(), redacted)
	}
	if original, _ := cfg.Secret(testSecretName); original.Reveal() != secretMarker {
		t.Fatal("Sanitized mutated the original configuration")
	}

	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("config", "cfg", cfg, "secret", secret)
	encoded, _ := json.Marshal(map[string]any{"secret": secret, "cfg": cfg})
	for _, rendered := range []string{
		fmt.Sprintf("%v %+v %#v %s", cfg, cfg, cfg, secret),
		fmt.Sprintf("%v %+v %#v", secret, secret, secret),
		logs.String(), string(encoded),
	} {
		if strings.Contains(rendered, secretMarker) {
			t.Fatalf("secret leaked in %q", rendered)
		}
	}
}

func TestParseRegisteredSecretFromEnvironmentAndErrors(t *testing.T) {
	registerTestSecret(t)

	clearEnv(t)
	setEnv(t, testSecretName, secretMarker)
	cfg, err := Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := cfg.SecretSources()[testSecretName]; got != SecretFromEnvironment {
		t.Fatalf("source = %q", got)
	}

	clearEnv(t)
	cfg, err = Parse()
	if err != nil {
		t.Fatalf("Parse without secret: %v", err)
	}
	if _, ok := cfg.Secret(testSecretName); ok || len(cfg.SecretSources()) != 0 {
		t.Fatal("unset secret reported as present")
	}

	clearEnv(t)
	setEnv(t, testSecretName, secretMarker)
	setEnv(t, testSecretFileName, writeSecretFile(t, secretMarker, 0o400))
	if _, err := Parse(); err == nil || !strings.Contains(err.Error(), "both set") {
		t.Fatalf("Parse conflict error = %v", err)
	}
	clearEnv(t)
	setEnv(t, testSecretFileName, filepath.Join(t.TempDir(), "missing"))
	if _, err := Parse(); err == nil {
		t.Fatal("Parse accepted a missing secret file")
	}
}
