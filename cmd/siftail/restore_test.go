package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/app"
	"github.com/drilonrecica/siftail/internal/audit"
	"github.com/drilonrecica/siftail/internal/backup"
	"github.com/drilonrecica/siftail/internal/config"
	"github.com/drilonrecica/siftail/internal/database"
)

func TestRestoreCLIRequiresStoppedServerConfirmationAndStaysSecretFree(
	t *testing.T,
) {
	clearSiftailEnv(t)
	dataDirectory := t.TempDir()
	t.Setenv("SIFTAIL_DATA_DIR", dataDirectory)
	currentPath := filepath.Join(dataDirectory, "siftail.db")
	current, err := database.Open(context.Background(), currentPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedRestoreCLIState(current.Writer(), "current"); err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	artifact := createRestoreCLIArtifact(t)

	var stdout, stderr bytes.Buffer
	if code := run(
		[]string{"restore", artifact}, &stdout, &stderr,
	); code == 0 || !strings.Contains(stderr.String(), "--confirm RESTORE") {
		t.Fatalf("unconfirmed restore = %d %q %q",
			code, stdout.String(), stderr.String())
	}

	cfg, err := config.Parse()
	if err != nil {
		t.Fatal(err)
	}
	cfg.UIAddr = freeTCPAddr(t)
	cfg.IngestAddr = freeTCPAddr(t)
	cfg.LogLevel = "error"
	cfg.ShutdownTimeout = time.Second
	logger, err := config.ConfigureLogger(cfg)
	if err != nil {
		t.Fatal(err)
	}
	application := app.New(cfg, logger)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	waitForHTTP(t, "http://"+cfg.UIAddr+"/health/live", nil, &bytes.Buffer{})
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{
		"restore", "--confirm", "RESTORE", artifact,
	}, &stdout, &stderr); code == 0 ||
		!strings.Contains(stderr.String(), "server to be stopped") {
		t.Fatalf("active restore = %d %q %q",
			code, stdout.String(), stderr.String())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{
		"restore", "--confirm", "RESTORE", artifact,
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("restore = %d %q %q", code, stdout.String(), stderr.String())
	}
	for _, expected := range []string{
		"restore complete", "type: full",
		"artifact: private-artifact.sqlite",
		"rollback: siftail.db.rollback", "schema: 4",
		"fresh login required: true",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("restore output lacks %q: %q", expected, stdout.String())
		}
	}
	if strings.Contains(stdout.String()+stderr.String(),
		filepath.Dir(artifact)) {
		t.Fatalf("restore exposed path: %q %q",
			stdout.String(), stderr.String())
	}
	restarted := app.New(cfg, logger)
	restartCtx, restartCancel := context.WithCancel(context.Background())
	restartDone := make(chan error, 1)
	go func() { restartDone <- restarted.Run(restartCtx) }()
	waitForHTTP(
		t, "http://"+cfg.UIAddr+"/health/ready", nil, &bytes.Buffer{},
	)
	restartCancel()
	if err := <-restartDone; err != nil {
		t.Fatal(err)
	}

	corrupt := filepath.Join(t.TempDir(), "private-corrupt.sqlite")
	if err := os.WriteFile(
		corrupt, []byte("private-payload"), 0600,
	); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{
		"restore", "--confirm", "RESTORE", corrupt,
	}, &stdout, &stderr); code == 0 ||
		strings.Contains(stdout.String()+stderr.String(), corrupt) ||
		strings.Contains(stdout.String()+stderr.String(), "private-payload") {
		t.Fatalf("corrupt restore = %d %q %q",
			code, stdout.String(), stderr.String())
	}
}

func TestRestoreCLIInStoppedServerSubprocess(t *testing.T) {
	clearSiftailEnv(t)
	dataDirectory := t.TempDir()
	currentPath := filepath.Join(dataDirectory, "siftail.db")
	current, err := database.Open(context.Background(), currentPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedRestoreCLIState(current.Writer(), "current"); err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	artifact := createRestoreCLIArtifact(t)
	command := exec.Command(
		os.Args[0], "-test.run=^TestRestoreCLIProcessHelper$",
	)
	command.Env = append(
		withoutSiftailEnv(os.Environ()),
		"GO_WANT_SIFTAIL_RESTORE_HELPER=1",
		"SIFTAIL_DATA_DIR="+dataDirectory,
		"RESTORE_TEST_ARTIFACT="+artifact,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("restore subprocess: %v\n%s", err, output)
	}
	db, err := database.Open(context.Background(), currentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	if err := db.Reader().QueryRow(
		"SELECT name FROM servers WHERE id=1",
	).Scan(&name); err != nil || name != "artifact" {
		t.Fatalf("subprocess restored server = %q, %v", name, err)
	}
}

func TestRestoreCLIProcessHelper(t *testing.T) {
	if os.Getenv("GO_WANT_SIFTAIL_RESTORE_HELPER") != "1" {
		return
	}
	artifact := os.Getenv("RESTORE_TEST_ARTIFACT")
	var stdout, stderr bytes.Buffer
	if code := run([]string{
		"restore", "--confirm", "RESTORE", artifact,
	}, &stdout, &stderr); code != 0 ||
		!strings.Contains(stdout.String(), "restore complete") {
		t.Fatalf("restore helper = %d %q %q",
			code, stdout.String(), stderr.String())
	}
}

func createRestoreCLIArtifact(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "source.db")
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedRestoreCLIState(db.Writer(), "artifact"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	coordinator := database.NewCoordinator(db.Writer())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	<-coordinator.Ready()
	service := backup.NewService(
		db, path, audit.NewStore(db.Reader(), coordinator),
	)
	artifact := filepath.Join(t.TempDir(), "private-artifact.sqlite")
	if _, err := service.CreateFull(
		context.Background(), artifact, nil,
	); err != nil {
		t.Fatal(err)
	}
	coordinator.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return artifact
}

func seedRestoreCLIState(db *sql.DB, marker string) error {
	_, err := db.Exec(`
		INSERT INTO administrators(
			id,username,password_hash,created_at_us,password_changed_at_us
		) VALUES(1,'admin',
			'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			1,1);
		INSERT INTO servers(id,name,created_at_us) VALUES(1,?,1);
		INSERT INTO sources(
			id,server_id,project_key,environment_key,application_key,service_key,
			project_label,environment_label,application_label,service_label,
			first_seen_at_us,last_seen_at_us
		) VALUES(1,1,'p','e','a','s','p','e','a','s',1,1);
		INSERT INTO log_events(
			id,event_at_us,received_at_us,source_id,stream,level_normalized,
			message_raw,message_text,attributes_json
		) VALUES(1,1,1,1,'stdout','info',?,?, '{}')`,
		marker, []byte(marker), marker,
	)
	return err
}
