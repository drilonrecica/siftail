// Package sessions owns opaque administrator browser sessions.
package sessions

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/drilonrecica/siftail/internal/audit"
	"github.com/drilonrecica/siftail/internal/database"
)

const (
	AbsoluteLifetime = 14 * 24 * time.Hour
	IdleLifetime     = 7 * 24 * time.Hour
	TouchInterval    = 5 * time.Minute
	CleanupGrace     = 7 * 24 * time.Hour
	MaxActive        = 64
)

var (
	ErrInvalidSession  = errors.New("invalid session")
	ErrSessionNotFound = errors.New("session not found")
)

type Store struct {
	db      *sql.DB
	mutator database.MutationCoordinator
	now     func() time.Time
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db, mutator: directMutator{db: db}, now: time.Now}
}

func NewCoordinatedStore(db *sql.DB, coordinator database.MutationCoordinator) *Store {
	return &Store{db: db, mutator: coordinator, now: time.Now}
}

type directMutator struct{ db *sql.DB }

func (m directMutator) Do(ctx context.Context, run func(*sql.Tx) error) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin session mutation: %w", err)
	}
	if err := run(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session mutation: %w", err)
	}
	return nil
}

type Session struct {
	ID               int64
	AdministratorID  int64
	CreatedAt        time.Time
	LastUsedAt       time.Time
	ExpiresAt        time.Time
	RevokedAt        *time.Time
	UserAgentSummary string
	ClientIdentity   string
}

type Issued struct {
	Session Session
	Token   string
}

func (s *Store) Issue(
	ctx context.Context,
	administratorID int64,
	userAgentSummary, clientIdentitySummary string,
) (Issued, error) {
	if administratorID <= 0 {
		return Issued{}, errors.New("administrator ID must be positive")
	}
	if err := validateSummary(userAgentSummary, 256); err != nil {
		return Issued{}, fmt.Errorf("user agent summary: %w", err)
	}
	if err := validateSummary(clientIdentitySummary, 128); err != nil {
		return Issued{}, fmt.Errorf("client identity summary: %w", err)
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return Issued{}, errors.New("generate session token")
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	hash := sha256.Sum256([]byte(token))
	now := s.now().UTC()
	expires := now.Add(AbsoluteLifetime)
	session := Session{
		AdministratorID: administratorID, CreatedAt: now, LastUsedAt: now,
		ExpiresAt: expires, UserAgentSummary: userAgentSummary,
		ClientIdentity: clientIdentitySummary,
	}

	err := s.mutator.Do(ctx, func(tx *sql.Tx) error {
		mutationCtx := context.WithoutCancel(ctx)
		var enabled int
		if err := tx.QueryRowContext(mutationCtx, `SELECT EXISTS(
			SELECT 1 FROM administrators WHERE id=? AND disabled_at_us IS NULL
		)`, administratorID).Scan(&enabled); err != nil {
			return database.Classify("verify session administrator", err)
		}
		if enabled != 1 {
			return ErrInvalidSession
		}
		var active int
		if err := tx.QueryRowContext(mutationCtx, `SELECT count(*) FROM sessions
			WHERE administrator_id=? AND revoked_at_us IS NULL
			AND expires_at_us>? AND last_used_at_us>?`,
			administratorID, now.UnixMicro(), now.Add(-IdleLifetime).UnixMicro(),
		).Scan(&active); err != nil {
			return database.Classify("count active sessions", err)
		}
		if active >= MaxActive {
			var evictedID int64
			if err := tx.QueryRowContext(mutationCtx, `SELECT id FROM sessions
				WHERE administrator_id=? AND revoked_at_us IS NULL
				AND expires_at_us>? AND last_used_at_us>?
				ORDER BY last_used_at_us ASC, created_at_us ASC, id ASC LIMIT 1`,
				administratorID, now.UnixMicro(), now.Add(-IdleLifetime).UnixMicro(),
			).Scan(&evictedID); err != nil {
				return database.Classify("select session eviction", err)
			}
			if _, err := tx.ExecContext(mutationCtx,
				"UPDATE sessions SET revoked_at_us=? WHERE id=? AND revoked_at_us IS NULL",
				now.UnixMicro(), evictedID); err != nil {
				return database.Classify("evict active session", err)
			}
		}
		result, err := tx.ExecContext(mutationCtx, `INSERT INTO sessions(
			administrator_id, token_hash, created_at_us, last_used_at_us,
			expires_at_us, user_agent_summary, client_identity_summary
		) VALUES (?, ?, ?, ?, ?, nullif(?, ''), nullif(?, ''))`,
			administratorID, hash[:], now.UnixMicro(), now.UnixMicro(),
			expires.UnixMicro(), userAgentSummary, clientIdentitySummary)
		if err != nil {
			return database.Classify("issue session", err)
		}
		session.ID, err = result.LastInsertId()
		if err != nil {
			return database.Classify("read session ID", err)
		}
		var auditMetadata audit.Metadata
		if clientIdentitySummary != "" {
			auditMetadata = audit.Metadata{
				audit.MetadataClientAddress: clientIdentitySummary,
			}
		}
		auditInput := audit.InputFromContext(
			ctx, audit.CategoryAuthentication, "sign_in",
			audit.OutcomeSucceeded, auditMetadata,
		)
		auditInput.OccurredAt = now
		_, err = audit.RecordTx(mutationCtx, tx, auditInput)
		return err
	})
	if err != nil {
		return Issued{}, err
	}
	return Issued{Session: session, Token: token}, nil
}

func (s *Store) Lookup(ctx context.Context, token string) (Session, error) {
	hash := sha256.Sum256([]byte(token))
	var session Session
	var created, lastUsed, expires int64
	var revoked sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT
		se.id, se.administrator_id, se.created_at_us, se.last_used_at_us,
		se.expires_at_us, se.revoked_at_us, coalesce(se.user_agent_summary, ''),
		coalesce(se.client_identity_summary, '')
		FROM sessions se
		JOIN administrators a ON a.id=se.administrator_id
		WHERE se.token_hash=? AND a.disabled_at_us IS NULL`,
		hash[:]).Scan(
		&session.ID, &session.AdministratorID, &created, &lastUsed, &expires,
		&revoked, &session.UserAgentSummary, &session.ClientIdentity,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrInvalidSession
	}
	if err != nil {
		return Session{}, fmt.Errorf("lookup session")
	}
	session.CreatedAt = time.UnixMicro(created).UTC()
	session.LastUsedAt = time.UnixMicro(lastUsed).UTC()
	session.ExpiresAt = time.UnixMicro(expires).UTC()
	if revoked.Valid {
		value := time.UnixMicro(revoked.Int64).UTC()
		session.RevokedAt = &value
	}
	now := s.now().UTC()
	if session.RevokedAt != nil || !now.Before(session.ExpiresAt) ||
		!session.LastUsedAt.After(now.Add(-IdleLifetime)) {
		return Session{}, ErrInvalidSession
	}
	if now.Sub(session.LastUsedAt) < TouchInterval {
		return session, nil
	}
	err = s.mutator.Do(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(context.WithoutCancel(ctx), `UPDATE sessions
			SET last_used_at_us=?
			WHERE id=? AND revoked_at_us IS NULL AND expires_at_us>?
			AND last_used_at_us>? AND last_used_at_us<=?`,
			now.UnixMicro(), session.ID, now.UnixMicro(),
			now.Add(-IdleLifetime).UnixMicro(), now.Add(-TouchInterval).UnixMicro())
		if err != nil {
			return database.Classify("touch session", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return database.Classify("read session touch", err)
		}
		if affected != 1 {
			return ErrInvalidSession
		}
		return nil
	})
	if err != nil {
		return Session{}, err
	}
	session.LastUsedAt = now
	return session, nil
}

func (s *Store) Revoke(ctx context.Context, token string) error {
	hash := sha256.Sum256([]byte(token))
	now := s.now().UTC().UnixMicro()
	return s.mutator.Do(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(context.WithoutCancel(ctx),
			"UPDATE sessions SET revoked_at_us=? WHERE token_hash=? AND revoked_at_us IS NULL",
			now, hash[:])
		if err != nil {
			return database.Classify("revoke session", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return database.Classify("read session revocation", err)
		}
		if affected != 1 {
			return ErrSessionNotFound
		}
		auditInput := audit.InputFromContext(
			ctx, audit.CategorySession, "session.revoke",
			audit.OutcomeSucceeded, nil,
		)
		auditInput.OccurredAt = time.UnixMicro(now)
		_, err = audit.RecordTx(context.WithoutCancel(ctx), tx, auditInput)
		return err
	})
}

func (s *Store) RevokeAll(ctx context.Context, administratorID int64) (int64, error) {
	now := s.now().UTC().UnixMicro()
	var affected int64
	err := s.mutator.Do(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(context.WithoutCancel(ctx), `UPDATE sessions
			SET revoked_at_us=? WHERE administrator_id=? AND revoked_at_us IS NULL`,
			now, administratorID)
		if err != nil {
			return database.Classify("revoke all sessions", err)
		}
		affected, err = result.RowsAffected()
		if err != nil {
			return database.Classify("read session revocations", err)
		}
		auditInput := audit.InputFromContext(
			ctx, audit.CategorySession, "session.revoke_all",
			audit.OutcomeSucceeded,
			audit.Metadata{
				audit.MetadataSessionCount: strconv.FormatInt(affected, 10),
			},
		)
		auditInput.OccurredAt = time.UnixMicro(now)
		_, err = audit.RecordTx(context.WithoutCancel(ctx), tx, auditInput)
		return err
	})
	return affected, err
}

func (s *Store) Cleanup(ctx context.Context, limit int) (int64, error) {
	if limit < 1 || limit > 1000 {
		return 0, errors.New("session cleanup limit must be between 1 and 1000")
	}
	now := s.now().UTC()
	cutoff := now.Add(-CleanupGrace).UnixMicro()
	idleCutoff := now.Add(-IdleLifetime - CleanupGrace).UnixMicro()
	var affected int64
	err := s.mutator.Do(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(context.WithoutCancel(ctx), `DELETE FROM sessions WHERE id IN (
			SELECT id FROM sessions
			WHERE (revoked_at_us IS NOT NULL AND revoked_at_us<=?)
			OR expires_at_us<=? OR last_used_at_us<=?
			ORDER BY id LIMIT ?
		)`, cutoff, cutoff, idleCutoff, limit)
		if err != nil {
			return database.Classify("clean sessions", err)
		}
		affected, err = result.RowsAffected()
		if err != nil {
			return database.Classify("read session cleanup", err)
		}
		return nil
	})
	return affected, err
}

func validateSummary(value string, max int) error {
	if value == "" {
		return nil
	}
	if !utf8.ValidString(value) || len([]byte(value)) > max {
		return errors.New("invalid UTF-8 or length")
	}
	if strings.IndexFunc(value, func(char rune) bool { return char < 0x20 || char == 0x7f }) >= 0 {
		return errors.New("control characters are not allowed")
	}
	return nil
}
