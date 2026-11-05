package config

import (
	"strings"
	"testing"
)

func TestReadStringFallback(t *testing.T) {
	clearEnv(t)
	if v := readString("SIFTAIL_TEST_VALUE", "fallback"); v != "fallback" {
		t.Errorf("value = %q, want fallback", v)
	}
}

func TestReadStringFromEnv(t *testing.T) {
	clearEnv(t)
	setEnv(t, "SIFTAIL_TEST_VALUE", "from-env")
	if v := readString("SIFTAIL_TEST_VALUE", "fallback"); v != "from-env" {
		t.Errorf("value = %q, want from-env", v)
	}
}

// Non-secret settings have no _FILE variant (ADR 0010 §17): the name is an
// unknown reserved variable and Parse fails instead of reading the file.
func TestParseRejectsFileVariantOfNonSecretSetting(t *testing.T) {
	clearEnv(t)
	setEnv(t, "SIFTAIL_PUBLIC_URL_FILE", writeTempFile(t, "https://logs.example.com\n"))

	_, err := Parse()
	if err == nil || !strings.Contains(err.Error(), "unknown environment variable(s): SIFTAIL_PUBLIC_URL_FILE") {
		t.Fatalf("Parse error = %v, want unknown SIFTAIL_PUBLIC_URL_FILE", err)
	}
}

func TestListUnknownVars(t *testing.T) {
	clearEnv(t)
	setEnv(t, "SIFTAIL_KNOWN", "x")
	setEnv(t, "SIFTAIL_UNKNOWN", "y")
	setEnv(t, "PATH", "/usr/bin")

	known := map[string]struct{}{
		"SIFTAIL_KNOWN": {},
	}
	unknown := listUnknownVars(known)
	if len(unknown) != 1 || unknown[0] != "SIFTAIL_UNKNOWN" {
		t.Errorf("unknown = %v", unknown)
	}
}
