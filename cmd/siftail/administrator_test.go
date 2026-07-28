package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drilonrecica/siftail/internal/auth"
	"github.com/drilonrecica/siftail/internal/database"
)

func TestAdministratorCLIOfflineCreateAndResetWithoutSecretOutput(t *testing.T) {
	clearSiftailEnv(t)
	dataDir := t.TempDir()
	t.Setenv("SIFTAIL_DATA_DIR", dataDir)
	const first = "offline-first-password"
	const second = "offline-second-password"

	var stdout, stderr bytes.Buffer
	if err := runAdministratorCommand(
		[]string{"create", "--username", "Admin"},
		strings.NewReader(first+"\n"+first+"\n"), &stdout, &stderr,
	); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String()+stderr.String(), first) {
		t.Fatal("administrator creation output leaked password")
	}
	stdout.Reset()
	stderr.Reset()
	if err := runAdministratorCommand(
		[]string{"reset-password"},
		strings.NewReader(second+"\n"+second+"\n"), &stdout, &stderr,
	); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String()+stderr.String(), second) {
		t.Fatal("administrator reset output leaked password")
	}

	db, err := database.Open(context.Background(), filepath.Join(dataDir, "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, matched, err := auth.NewStore(db.Reader()).Verify(
		context.Background(), "Admin", []byte(second),
	); err != nil || !matched {
		t.Fatalf("reset password matched=%v err=%v", matched, err)
	}
}

func TestAdministratorCLIRejectsArgumentsMismatchAndExtraInput(t *testing.T) {
	tests := []struct {
		args  []string
		input string
	}{
		{[]string{"create", "--username", "Admin", "--password", "secret"}, ""},
		{[]string{"create", "--username", " admin"}, "valid-password\nvalid-password\n"},
		{[]string{"create", "--username", "Admin"}, "one-password\ntwo-password\n"},
		{[]string{"create", "--username", "Admin"}, "valid-password\nvalid-password\nextra\n"},
	}
	for _, test := range tests {
		if err := runAdministratorCommand(
			test.args, strings.NewReader(test.input), &bytes.Buffer{}, &bytes.Buffer{},
		); err == nil {
			t.Fatalf("args/input accepted: %v", test.args)
		}
	}
}

func TestReadConfirmedPasswordPreservesSpacesAndHandlesCRLF(t *testing.T) {
	password, err := readConfirmedPassword(
		strings.NewReader(" spaces stay \r\n spaces stay \r\n"), &bytes.Buffer{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(password) != " spaces stay " {
		t.Fatalf("password = %q", password)
	}
}
