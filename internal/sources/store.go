// Package sources owns Server and ingestion-token administration.
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
)

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db, now: time.Now}
}

type Server struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Hostname    string `json:"hostname,omitempty"`
	CreatedAtUS int64  `json:"created_at_us"`
}

type AuthenticatedServer struct {
	ID       int64
	Name     string
	Hostname string
}

func (s *Store) CreateServer(ctx context.Context, name, hostname string) (Server, error) {
	name = strings.TrimSpace(name)
	hostname = strings.TrimSpace(hostname)
	if err := validText(name, 128, false); err != nil {
		return Server{}, fmt.Errorf("server name: %w", err)
	}
	if err := validText(hostname, 255, true); err != nil {
		return Server{}, fmt.Errorf("server hostname: %w", err)
	}
	created := s.now().UnixMicro()
	result, err := s.db.ExecContext(ctx,
		"INSERT INTO servers(name, hostname, created_at_us) VALUES (?, nullif(?, ''), ?)",
		name, hostname, created)
	if err != nil {
		return Server{}, fmt.Errorf("create server: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Server{}, fmt.Errorf("read server ID: %w", err)
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
		return CreatedToken{}, errors.New("server ID must be positive")
	}
	name = strings.TrimSpace(name)
	if err := validText(name, 128, false); err != nil {
		return CreatedToken{}, fmt.Errorf("token name: %w", err)
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return CreatedToken{}, errors.New("generate token")
	}
	plaintext := "sft_" + base64.RawURLEncoding.EncodeToString(random)
	hash := sha256.Sum256([]byte(plaintext))
	fingerprint := hex.EncodeToString(hash[:6])
	result, err := s.db.ExecContext(ctx, `INSERT INTO ingestion_tokens(
		server_id, name, token_hash, fingerprint, created_at_us
	) VALUES (?, ?, ?, ?, ?)`, serverID, name, hash[:], fingerprint, s.now().UnixMicro())
	if err != nil {
		return CreatedToken{}, fmt.Errorf("create token: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return CreatedToken{}, fmt.Errorf("read token ID: %w", err)
	}
	return CreatedToken{
		ID: id, ServerID: serverID, Name: name,
		Token: plaintext, Fingerprint: fingerprint,
	}, nil
}

func (s *Store) RevokeToken(ctx context.Context, id int64) error {
	if id <= 0 {
		return errors.New("token ID must be positive")
	}
	result, err := s.db.ExecContext(ctx,
		"UPDATE ingestion_tokens SET revoked_at_us=? WHERE id=? AND revoked_at_us IS NULL",
		s.now().UnixMicro(), id)
	if err != nil {
		return fmt.Errorf("revoke token: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("revoke token: %w", err)
	}
	if affected == 0 {
		return errors.New("active token not found")
	}
	return nil
}

// VerifyToken always performs at least one constant-time hash comparison,
// including malformed tokens and lookup misses.
func (s *Store) VerifyToken(ctx context.Context, plaintext string) (AuthenticatedServer, error) {
	hash := sha256.Sum256([]byte(plaintext))
	fingerprint := hex.EncodeToString(hash[:6])
	rows, err := s.db.QueryContext(ctx, `SELECT
		t.token_hash, s.id, s.name, coalesce(s.hostname, '')
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
		var candidate AuthenticatedServer
		if err := rows.Scan(&stored, &candidate.ID, &candidate.Name, &candidate.Hostname); err != nil {
			return AuthenticatedServer{}, fmt.Errorf("verify token")
		}
		compared = true
		if len(stored) == sha256.Size && subtle.ConstantTimeCompare(hash[:], stored) == 1 {
			authenticated = candidate
			matched = true
		}
	}
	if err := rows.Err(); err != nil {
		return AuthenticatedServer{}, fmt.Errorf("verify token")
	}
	if !compared {
		_ = subtle.ConstantTimeCompare(hash[:], dummy[:])
	}
	if !matched || !strings.HasPrefix(plaintext, "sft_") {
		return AuthenticatedServer{}, errors.New("invalid token")
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
