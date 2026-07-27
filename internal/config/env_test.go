package config

import (
	"strings"
	"testing"
)

func TestReadStringFallback(t *testing.T) {
	clearEnv(t)
	v, err := readString("SIFTAIL_TEST_VALUE", "", "fallback")
	if err != nil {
		t.Fatalf("readString failed: %v", err)
	}
	if v != "fallback" {
		t.Errorf("value = %q, want fallback", v)
	}
}

func TestReadStringFromEnv(t *testing.T) {
	clearEnv(t)
	setEnv(t, "SIFTAIL_TEST_VALUE", "from-env")

	v, err := readString("SIFTAIL_TEST_VALUE", "", "fallback")
	if err != nil {
		t.Fatalf("readString failed: %v", err)
	}
	if v != "from-env" {
		t.Errorf("value = %q, want from-env", v)
	}
}

func TestReadStringFromFile(t *testing.T) {
	clearEnv(t)
	file := writeTempFile(t, "from-file\n")
	setEnv(t, "SIFTAIL_TEST_VALUE_FILE", file)

	v, err := readString("SIFTAIL_TEST_VALUE", "SIFTAIL_TEST_VALUE_FILE", "fallback")
	if err != nil {
		t.Fatalf("readString failed: %v", err)
	}
	if v != "from-file" {
		t.Errorf("value = %q, want from-file", v)
	}
}

func TestReadStringConflicts(t *testing.T) {
	clearEnv(t)
	file := writeTempFile(t, "from-file\n")
	setEnv(t, "SIFTAIL_TEST_VALUE", "from-env")
	setEnv(t, "SIFTAIL_TEST_VALUE_FILE", file)

	_, err := readString("SIFTAIL_TEST_VALUE", "SIFTAIL_TEST_VALUE_FILE", "fallback")
	if err == nil {
		t.Fatal("expected error when both direct and _FILE are set")
	}
	if !strings.Contains(err.Error(), "SIFTAIL_TEST_VALUE") || !strings.Contains(err.Error(), "SIFTAIL_TEST_VALUE_FILE") {
		t.Errorf("error does not mention both variables: %v", err)
	}
}

func TestReadStringMissingFile(t *testing.T) {
	clearEnv(t)
	setEnv(t, "SIFTAIL_TEST_VALUE_FILE", "/nonexistent/path")

	_, err := readString("SIFTAIL_TEST_VALUE", "SIFTAIL_TEST_VALUE_FILE", "fallback")
	if err == nil {
		t.Fatal("expected error for missing _FILE")
	}
}

func TestReadStringTrimsNewline(t *testing.T) {
	clearEnv(t)
	file := writeTempFile(t, "secret-value\r\n")
	setEnv(t, "SIFTAIL_TEST_VALUE_FILE", file)

	v, err := readString("SIFTAIL_TEST_VALUE", "SIFTAIL_TEST_VALUE_FILE", "fallback")
	if err != nil {
		t.Fatalf("readString failed: %v", err)
	}
	if v != "secret-value" {
		t.Errorf("value = %q, want secret-value", v)
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
