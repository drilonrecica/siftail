// Package sources owns Server and ingestion-token administration plus the
// bounded read-only catalog of discovered stable sources.
package sources

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/drilonrecica/siftail/internal/audit"
)

type Store struct {
	db      *sql.DB
	mutator databaseMutator
	now     func() time.Time
}

var (
	ErrInvalidServerInput = errors.New("invalid Server input")
	ErrServerNameInUse    = errors.New("Server name is already in use")
	ErrInvalidTokenInput  = errors.New("invalid ingestion-token input")
	ErrInvalidToken       = errors.New("invalid ingestion token")
	ErrTokenNameInUse     = errors.New("ingestion-token name is already in use")
)

func NewStore(db *sql.DB) *Store {
	return &Store{db: db, mutator: directMutator{db: db}, now: time.Now}
}

// NewCoordinatedStore routes mutations through the active server's single
// database coordinator while retaining the ordinary read pool for lookups.
func NewCoordinatedStore(db *sql.DB, mutator databaseMutator) *Store {
	return &Store{db: db, mutator: mutator, now: time.Now}
}

type databaseMutator interface {
	Do(context.Context, func(*sql.Tx) error) error
}

type directMutator struct{ db *sql.DB }

func (m directMutator) Do(ctx context.Context, run func(*sql.Tx) error) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin source mutation: %w", err)
	}
	if err := run(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit source mutation: %w", err)
	}
	return nil
}

type Server struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Hostname    string `json:"hostname,omitempty"`
	CreatedAtUS int64  `json:"created_at_us"`
}

type AuthenticatedServer struct {
	TokenID  int64
	ID       int64
	Name     string
	Hostname string
}

func (s *Store) CreateServer(ctx context.Context, name, hostname string) (Server, error) {
	name = strings.TrimSpace(name)
	hostname = strings.TrimSpace(hostname)
	if err := validText(name, 128, false); err != nil {
		return Server{}, fmt.Errorf("%w: name: %v", ErrInvalidServerInput, err)
	}
	if err := validText(hostname, 255, true); err != nil {
		return Server{}, fmt.Errorf("%w: hostname: %v", ErrInvalidServerInput, err)
	}
	created := s.now().UnixMicro()
	var id int64
	err := s.mutator.Do(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM servers WHERE name = ? LIMIT 1)`, name,
		).Scan(&exists); err != nil {
			return fmt.Errorf("check Server name: %w", err)
		}
		if exists != 0 {
			return ErrServerNameInUse
		}
		result, err := tx.ExecContext(ctx,
			"INSERT INTO servers(name, hostname, created_at_us) VALUES (?, nullif(?, ''), ?)",
			name, hostname, created)
		if err != nil {
			return fmt.Errorf("create server: %w", err)
		}
		id, err = result.LastInsertId()
		if err != nil {
			return fmt.Errorf("read server ID: %w", err)
		}
		auditInput := audit.InputFromContext(
			ctx, audit.CategoryIngestionToken, "server.create",
			audit.OutcomeSucceeded,
			audit.Metadata{audit.MetadataServerName: name},
		)
		auditInput.OccurredAt = time.UnixMicro(created)
		auditInput.ServerID = &id
		_, err = audit.RecordTx(context.WithoutCancel(ctx), tx, auditInput)
		return err
	})
	if err != nil {
		return Server{}, err
	}
	return Server{ID: id, Name: name, Hostname: hostname, CreatedAtUS: created}, nil
}

func (s *Store) ListServers(ctx context.Context) ([]Server, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, name, coalesce(hostname, ''), created_at_us FROM servers ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	defer rows.Close()
	var servers []Server
	for rows.Next() {
		var server Server
		if err := rows.Scan(&server.ID, &server.Name, &server.Hostname, &server.CreatedAtUS); err != nil {
			return nil, fmt.Errorf("scan server: %w", err)
		}
		servers = append(servers, server)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	return servers, nil
}

type CreatedToken struct {
	ID          int64  `json:"id"`
	ServerID    int64  `json:"server_id"`
	Name        string `json:"name"`
	Token       string `json:"token"`
	Fingerprint string `json:"fingerprint"`
}

func (s *Store) CreateToken(ctx context.Context, serverID int64, name string) (CreatedToken, error) {
	if serverID <= 0 {
		return CreatedToken{}, ErrServerNotFound
	}
	name = strings.TrimSpace(name)
	if err := validText(name, 128, false); err != nil {
		return CreatedToken{}, fmt.Errorf("%w: name: %v", ErrInvalidTokenInput, err)
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return CreatedToken{}, errors.New("generate token")
	}
	plaintext := "sft_" + base64.RawURLEncoding.EncodeToString(random)
	hash := sha256.Sum256([]byte(plaintext))
	fingerprint := hex.EncodeToString(hash[:6])
	var id int64
	created := s.now().UnixMicro()
	err := s.mutator.Do(ctx, func(tx *sql.Tx) error {
		var serverExists, nameExists int
		if err := tx.QueryRowContext(ctx, `SELECT
			EXISTS(SELECT 1 FROM servers WHERE id = ? LIMIT 1),
			EXISTS(SELECT 1 FROM ingestion_tokens
				WHERE server_id = ? AND name = ? LIMIT 1)`,
			serverID, serverID, name,
		).Scan(&serverExists, &nameExists); err != nil {
			return fmt.Errorf("check ingestion-token ownership: %w", err)
		}
		if serverExists == 0 {
			return ErrServerNotFound
		}
		if nameExists != 0 {
			return ErrTokenNameInUse
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO ingestion_tokens(
			server_id, name, token_hash, fingerprint, created_at_us
		) VALUES (?, ?, ?, ?, ?)`, serverID, name, hash[:], fingerprint, created)
		if err != nil {
			return fmt.Errorf("create token: %w", err)
		}
		id, err = result.LastInsertId()
		if err != nil {
			return fmt.Errorf("read token ID: %w", err)
		}
		auditInput := audit.InputFromContext(
			ctx, audit.CategoryIngestionToken, "ingestion_token.create",
			audit.OutcomeSucceeded,
			audit.Metadata{
				audit.MetadataTokenName:        name,
				audit.MetadataTokenFingerprint: fingerprint,
			},
		)
		auditInput.OccurredAt = time.UnixMicro(created)
		auditInput.ServerID = &serverID
		_, err = audit.RecordTx(context.WithoutCancel(ctx), tx, auditInput)
		return err
	})
	if err != nil {
		return CreatedToken{}, err
	}
	return CreatedToken{
		ID: id, ServerID: serverID, Name: name,
		Token: plaintext, Fingerprint: fingerprint,
	}, nil
}

func (s *Store) RevokeToken(ctx context.Context, id int64) error {
	if id <= 0 {
		return ErrTokenNotFound
	}
	return s.mutator.Do(ctx, func(tx *sql.Tx) error {
		var serverID int64
		var name, fingerprint string
		if err := tx.QueryRowContext(ctx, `SELECT server_id,name,fingerprint
			FROM ingestion_tokens WHERE id=? AND revoked_at_us IS NULL`, id).
			Scan(&serverID, &name, &fingerprint); errors.Is(err, sql.ErrNoRows) {
			return ErrTokenNotFound
		} else if err != nil {
			return fmt.Errorf("read token for revocation: %w", err)
		}
		revokedAt := s.now().UnixMicro()
		result, err := tx.ExecContext(ctx,
			"UPDATE ingestion_tokens SET revoked_at_us=? WHERE id=? AND revoked_at_us IS NULL",
			revokedAt, id)
		if err != nil {
			return fmt.Errorf("revoke token: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("revoke token: %w", err)
		}
		if affected == 0 {
			return ErrTokenNotFound
		}
		auditInput := audit.InputFromContext(
			ctx, audit.CategoryIngestionToken, "ingestion_token.revoke",
			audit.OutcomeSucceeded,
			audit.Metadata{
				audit.MetadataTokenName:        name,
				audit.MetadataTokenFingerprint: fingerprint,
			},
		)
		auditInput.OccurredAt = time.UnixMicro(revokedAt)
		auditInput.ServerID = &serverID
		_, err = audit.RecordTx(context.WithoutCancel(ctx), tx, auditInput)
		return err
	})
}

// VerifyToken always performs at least one constant-time hash comparison,
// including malformed tokens and lookup misses.
func (s *Store) VerifyToken(ctx context.Context, plaintext string) (AuthenticatedServer, error) {
	hash := sha256.Sum256([]byte(plaintext))
	fingerprint := hex.EncodeToString(hash[:6])
	rows, err := s.db.QueryContext(ctx, `SELECT
		t.id, t.token_hash, s.id, s.name, coalesce(s.hostname, '')
		FROM ingestion_tokens t JOIN servers s ON s.id=t.server_id
		WHERE t.fingerprint=? AND t.revoked_at_us IS NULL
		ORDER BY t.id LIMIT 8`, fingerprint)
	if err != nil {
		return AuthenticatedServer{}, fmt.Errorf("verify token")
	}
	defer rows.Close()
	dummy := sha256.Sum256([]byte("siftail-dummy-token-comparison"))
	compared := false
	var authenticated AuthenticatedServer
	matched := false
	for rows.Next() {
		var stored []byte
		var tokenID int64
		var candidate AuthenticatedServer
		if err := rows.Scan(&tokenID, &stored, &candidate.ID, &candidate.Name, &candidate.Hostname); err != nil {
			return AuthenticatedServer{}, fmt.Errorf("verify token")
		}
		compared = true
		if len(stored) == sha256.Size && subtle.ConstantTimeCompare(hash[:], stored) == 1 {
			authenticated = candidate
			authenticated.TokenID = tokenID
			matched = true
		}
	}
	if err := rows.Err(); err != nil {
		return AuthenticatedServer{}, fmt.Errorf("verify token")
	}
	if err := rows.Close(); err != nil {
		return AuthenticatedServer{}, fmt.Errorf("verify token")
	}
	if !compared {
		_ = subtle.ConstantTimeCompare(hash[:], dummy[:])
	}
	if !matched || !strings.HasPrefix(plaintext, "sft_") {
		return AuthenticatedServer{}, ErrInvalidToken
	}
	return authenticated, nil
}

func validText(value string, max int, emptyOK bool) error {
	if value == "" && emptyOK {
		return nil
	}
	if value == "" || !utf8.ValidString(value) || len([]byte(value)) > max {
		return errors.New("invalid length or UTF-8")
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return errors.New("control characters are not allowed")
		}
	}
	return nil
}
