package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/drilonrecica/siftail/internal/audit"
	"github.com/drilonrecica/siftail/internal/database"
)

var (
	ErrAdministratorExists   = errors.New("administrator already exists")
	ErrAdministratorNotFound = errors.New("administrator not found")
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
		return fmt.Errorf("begin administrator mutation: %w", err)
	}
	if err := run(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit administrator mutation: %w", err)
	}
	return nil
}

type Administrator struct {
	ID                int64  `json:"id"`
	Username          string `json:"username"`
	CreatedAtUS       int64  `json:"created_at_us"`
	PasswordChangedUS int64  `json:"password_changed_at_us"`
}

func (s *Store) Create(ctx context.Context, username string, password []byte) (Administrator, error) {
	if err := ValidateUsername(username); err != nil {
		return Administrator{}, err
	}
	encoded, err := HashPassword(ctx, password)
	if err != nil {
		return Administrator{}, err
	}
	now := s.now().UnixMicro()
	administrator := Administrator{
		ID: 1, Username: username, CreatedAtUS: now, PasswordChangedUS: now,
	}
	err = s.mutator.Do(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.WithoutCancel(ctx), `INSERT INTO administrators(
			id, username, password_hash, created_at_us, password_changed_at_us
		) VALUES (1, ?, ?, ?, ?)`, username, encoded, now, now)
		if err != nil {
			var category *database.CategoryError
			classified := database.Classify("create administrator", err)
			if errors.As(classified, &category) && category.Category == database.CategoryConstraint {
				return ErrAdministratorExists
			}
			return classified
		}
		auditInput := audit.InputFromContext(
			ctx, audit.CategoryAdministratorCredential,
			"administrator.create", audit.OutcomeSucceeded,
			audit.Metadata{audit.MetadataActorName: username},
		)
		auditInput.OccurredAt = time.UnixMicro(now)
		_, err = audit.RecordTx(context.WithoutCancel(ctx), tx, auditInput)
		return err
	})
	if err != nil {
		return Administrator{}, err
	}
	return administrator, nil
}

func (s *Store) ResetPassword(ctx context.Context, password []byte) error {
	encoded, err := HashPassword(ctx, password)
	if err != nil {
		return err
	}
	changed := s.now().UnixMicro()
	return s.mutator.Do(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(context.WithoutCancel(ctx), `UPDATE administrators
			SET password_hash=?, password_changed_at_us=?
			WHERE id=1 AND disabled_at_us IS NULL`, encoded, changed)
		if err != nil {
			return database.Classify("reset administrator password", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return database.Classify("read administrator password reset", err)
		}
		if affected != 1 {
			return ErrAdministratorNotFound
		}
		revoked, err := tx.ExecContext(context.WithoutCancel(ctx), `UPDATE sessions
			SET revoked_at_us=? WHERE administrator_id=1 AND revoked_at_us IS NULL`,
			changed)
		if err != nil {
			return database.Classify("revoke sessions after password reset", err)
		}
		sessionCount, err := revoked.RowsAffected()
		if err != nil {
			return database.Classify("read password-reset session revocations", err)
		}
		auditInput := audit.InputFromContext(
			ctx, audit.CategoryAdministratorCredential,
			"administrator.password_reset", audit.OutcomeSucceeded,
			audit.Metadata{
				audit.MetadataSessionCount: strconv.FormatInt(sessionCount, 10),
			},
		)
		auditInput.OccurredAt = time.UnixMicro(changed)
		_, err = audit.RecordTx(context.WithoutCancel(ctx), tx, auditInput)
		return err
	})
}

// Verify performs one Argon2id operation for existing, unknown, malformed, and
// disabled usernames so account lookup does not bypass the expensive path.
func (s *Store) Verify(ctx context.Context, username string, password []byte) (Administrator, bool, error) {
	encoded := dummyPasswordHash
	var administrator Administrator
	if ValidateUsername(username) == nil {
		var disabled sql.NullInt64
		err := s.db.QueryRowContext(ctx, `SELECT
			id, username, password_hash, created_at_us, password_changed_at_us, disabled_at_us
			FROM administrators WHERE username=?`, username).Scan(
			&administrator.ID, &administrator.Username, &encoded,
			&administrator.CreatedAtUS, &administrator.PasswordChangedUS, &disabled,
		)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			_, _ = VerifyPassword(context.WithoutCancel(ctx), password, dummyPasswordHash)
			return Administrator{}, false, fmt.Errorf("verify administrator")
		}
		if err != nil || disabled.Valid {
			encoded = dummyPasswordHash
			administrator = Administrator{}
		}
	}
	matches, err := VerifyPassword(ctx, password, encoded)
	if err != nil {
		return Administrator{}, false, fmt.Errorf("verify administrator")
	}
	if !matches || administrator.ID == 0 {
		return Administrator{}, false, nil
	}
	return administrator, true, nil
}

func (s *Store) Exists(ctx context.Context) (bool, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM administrators WHERE id=1 AND disabled_at_us IS NULL)",
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("inspect administrator state: %w", err)
	}
	return exists == 1, nil
}
