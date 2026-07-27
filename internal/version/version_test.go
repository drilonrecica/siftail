package version

import "testing"

func TestGoVersion_NotEmpty(t *testing.T) {
	v := GoVersion()
	if v == "" {
		t.Fatal("GoVersion returned empty string")
	}
}

func TestVersionDefaults(t *testing.T) {
	if Version == "" {
		t.Fatal("Version variable is empty")
	}
	if Commit == "" {
		t.Fatal("Commit variable is empty")
	}
	if BuildDate == "" {
		t.Fatal("BuildDate variable is empty")
	}
}
