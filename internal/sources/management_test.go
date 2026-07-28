package sources

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/drilonrecica/siftail/internal/database"
)

func TestServerManagementPagesExposeOnlyBoundedNonsecretMetadata(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewStore(db.Writer())
	first, err := store.CreateServer(context.Background(), "Alpha", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateServer(context.Background(), "Beta", "beta.example")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateServer(context.Background(), "Gamma", ""); err != nil {
		t.Fatal(err)
	}
	active, err := store.CreateToken(context.Background(), first.ID, "primary")
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := store.CreateToken(context.Background(), first.ID, "old")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeToken(context.Background(), revoked.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec(`INSERT INTO sources(
		server_id,project_key,environment_key,application_key,service_key,
		project_label,environment_label,application_label,service_label,
		first_seen_at_us,last_seen_at_us
	) VALUES (1,'project','production','api','web',
		'Project','Production','API','Web',10,20)`); err != nil {
		t.Fatal(err)
	}

	page, err := store.ServerPage(context.Background(), ServerPageQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !page.HasMore || page.NextAfter != second.ID || len(page.Servers) != 2 ||
		page.Servers[0].SourceCount != 1 || page.Servers[0].ActiveTokenCount != 1 ||
		page.Servers[0].LastEventAtUS == nil || *page.Servers[0].LastEventAtUS != 20 {
		t.Fatalf("Server page = %#v", page)
	}
	detail, err := store.ServerManagementDetail(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Tokens) != 2 || detail.Tokens[0].Name != "old" ||
		detail.Tokens[0].RevokedAtUS == nil || detail.Tokens[1].Name != "primary" ||
		detail.Tokens[1].RevokedAtUS != nil {
		t.Fatalf("Server detail = %#v", detail)
	}
	for _, token := range detail.Tokens {
		if token.Fingerprint == active.Token || token.Name == active.Token {
			t.Fatal("management metadata exposed token plaintext")
		}
	}
	metadata, err := store.TokenMetadata(context.Background(), active.ID)
	if err != nil || metadata.ServerID != first.ID || metadata.Fingerprint != active.Fingerprint {
		t.Fatalf("token metadata = %#v, err=%v", metadata, err)
	}
}

func TestServerManagementValidationCancellationAndCoordinatorRaces(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	coordinator := database.NewCoordinator(db.Writer())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(context.Background()) }()
	<-coordinator.Ready()
	defer func() {
		coordinator.Close()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}()
	store := NewCoordinatedStore(db.Reader(), coordinator)
	server, err := store.CreateServer(context.Background(), "Concurrent", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []ServerPageQuery{{AfterID: -1}, {Limit: -1}, {Limit: 201}} {
		if _, err := store.ServerPage(context.Background(), query); err == nil {
			t.Fatalf("query %#v accepted", query)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.ServerPage(ctx, ServerPageQuery{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Server page error = %v", err)
	}

	const workers = 16
	var group sync.WaitGroup
	created := make(chan CreatedToken, workers)
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			token, err := store.CreateToken(context.Background(), server.ID,
				"token-"+string(rune('a'+index)))
			if err != nil {
				t.Errorf("create token %d: %v", index, err)
				return
			}
			created <- token
		}(index)
	}
	group.Wait()
	close(created)
	var revoke sync.WaitGroup
	for token := range created {
		token := token
		revoke.Add(1)
		go func() {
			defer revoke.Done()
			if err := store.RevokeToken(context.Background(), token.ID); err != nil {
				t.Errorf("revoke token %d: %v", token.ID, err)
			}
		}()
	}
	revoke.Wait()
	detail, err := store.ServerManagementDetail(context.Background(), server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Tokens) != workers || detail.Server.ActiveTokenCount != 0 {
		t.Fatalf("concurrent token detail = %#v", detail)
	}
}
