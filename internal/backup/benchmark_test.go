package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/drilonrecica/siftail/internal/audit"
	"github.com/drilonrecica/siftail/internal/database"
)

func BenchmarkCreateFullBackup(b *testing.B) {
	sourcePath := filepath.Join(b.TempDir(), "siftail.db")
	db, err := database.Open(context.Background(), sourcePath)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Writer().Exec(`
		INSERT INTO servers(id,name,created_at_us) VALUES(1,'server',1);
		INSERT INTO sources(
			id,server_id,project_key,environment_key,application_key,service_key,
			project_label,environment_label,application_label,service_label,
			first_seen_at_us,last_seen_at_us
		) VALUES(1,1,'p','e','a','s','p','e','a','s',1,1);
	`); err != nil {
		b.Fatal(err)
	}
	tx, err := db.Writer().Begin()
	if err != nil {
		b.Fatal(err)
	}
	statement, err := tx.Prepare(`INSERT INTO log_events(
		event_at_us,received_at_us,source_id,stream,level_normalized,
		message_raw,message_text,attributes_json
	) VALUES(?,?,1,'stdout','info',?,?, '{}')`)
	if err != nil {
		b.Fatal(err)
	}
	message := make([]byte, 256)
	for index := 0; index < 10_000; index++ {
		if _, err := statement.Exec(
			index+1, index+1, message, string(message),
		); err != nil {
			b.Fatal(err)
		}
	}
	statement.Close()
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	coordinator := database.NewCoordinator(db.Writer())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	<-coordinator.Ready()
	defer func() {
		coordinator.Close()
		cancel()
		if err := <-done; err != nil {
			b.Error(err)
		}
	}()
	service := NewService(db, sourcePath, audit.NewStore(db.Reader(), coordinator))
	outputDirectory := b.TempDir()

	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		output := filepath.Join(
			outputDirectory, fmt.Sprintf("backup-%d.sqlite", index),
		)
		result, err := service.CreateFull(context.Background(), output, nil)
		if err != nil {
			b.Fatal(err)
		}
		b.SetBytes(result.Bytes)
		if err := os.Remove(output); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCreateConfigurationBackup(b *testing.B) {
	sourcePath := filepath.Join(b.TempDir(), "siftail.db")
	db, err := database.Open(context.Background(), sourcePath)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Writer().Exec(`
		INSERT INTO administrators(
			id,username,password_hash,created_at_us,password_changed_at_us
		) VALUES(1,'admin',printf('%064d',0),1,1);
		INSERT INTO servers(id,name,hostname,created_at_us)
		VALUES(1,'server','server.example.test',1);
		INSERT INTO ingestion_tokens(
			id,server_id,name,token_hash,fingerprint,created_at_us
		) VALUES(1,1,'primary',randomblob(32),'fingerprint',1);
		INSERT INTO sources(
			id,server_id,project_key,environment_key,application_key,service_key,
			project_label,environment_label,application_label,service_label,
			alias,first_seen_at_us,last_seen_at_us
		) VALUES(1,1,'p','e','a','s','p','e','a','s','API',1,1);
	`); err != nil {
		b.Fatal(err)
	}
	tx, err := db.Writer().Begin()
	if err != nil {
		b.Fatal(err)
	}
	statement, err := tx.Prepare(`INSERT INTO log_events(
		event_at_us,received_at_us,source_id,stream,level_normalized,
		message_raw,message_text,attributes_json
	) VALUES(?,?,1,'stdout','info',?,?, '{}')`)
	if err != nil {
		b.Fatal(err)
	}
	message := make([]byte, 256)
	for index := 0; index < 10_000; index++ {
		if _, err := statement.Exec(
			index+1, index+1, message, string(message),
		); err != nil {
			b.Fatal(err)
		}
	}
	statement.Close()
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	coordinator := database.NewCoordinator(db.Writer())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	<-coordinator.Ready()
	defer func() {
		coordinator.Close()
		cancel()
		if err := <-done; err != nil {
			b.Error(err)
		}
	}()
	service := NewService(db, sourcePath, audit.NewStore(db.Reader(), coordinator))
	outputDirectory := b.TempDir()

	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		output := filepath.Join(
			outputDirectory, fmt.Sprintf("configuration-%d.sqlite", index),
		)
		result, err := service.CreateConfiguration(
			context.Background(), output, nil,
		)
		if err != nil {
			b.Fatal(err)
		}
		b.SetBytes(result.Bytes)
		if err := os.Remove(output); err != nil {
			b.Fatal(err)
		}
	}
}
