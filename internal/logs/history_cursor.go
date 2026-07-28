package logs

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
)

const (
	historyCursorKeySetting = "history_cursor_hmac_key"
	historyCursorVersion    = byte(1)
	historyCursorPayloadLen = 1 + 8 + 8 + 1 + sha256.Size
	maxHistoryCursorBytes   = 1024
)

var ErrInvalidHistoryCursor = errors.New("invalid history cursor")

// HistoryCursor is the verified keyset carried by an opaque History cursor.
type HistoryCursor struct {
	EventAtUS int64
	ID        int64
	Direction Direction
}

// CursorCodec authenticates History keysets with a local, persisted secret.
type CursorCodec struct {
	key [sha256.Size]byte
}

// LoadCursorCodec loads or creates the local History cursor key. Creation uses
// the active-server mutation coordinator and is safe when concurrent callers
// race to initialize an empty database.
func LoadCursorCodec(
	ctx context.Context,
	reader *sql.DB,
	coordinator database.MutationCoordinator,
) (*CursorCodec, error) {
	if reader == nil || coordinator == nil {
		return nil, errors.New("history cursor storage is unavailable")
	}
	key, err := readCursorKey(ctx, reader)
	if err == nil {
		return newCursorCodec(key)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	generated := make([]byte, sha256.Size)
	if _, err := rand.Read(generated); err != nil {
		return nil, fmt.Errorf("generate history cursor key: %w", err)
	}
	encoded, err := json.Marshal(base64.RawURLEncoding.EncodeToString(generated))
	if err != nil {
		return nil, fmt.Errorf("encode history cursor key: %w", err)
	}
	nowUS := time.Now().UTC().UnixMicro()
	if err := coordinator.Do(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value_json, updated_at_us)
			VALUES (?, ?, ?)
			ON CONFLICT(key) DO NOTHING`,
			historyCursorKeySetting, string(encoded), nowUS,
		)
		if err != nil {
			return database.Classify("store history cursor key", err)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("initialize history cursor key: %w", err)
	}
	key, err = readCursorKey(ctx, reader)
	if err != nil {
		return nil, err
	}
	return newCursorCodec(key)
}

func readCursorKey(ctx context.Context, reader *sql.DB) ([]byte, error) {
	var valueJSON string
	if err := reader.QueryRowContext(ctx,
		`SELECT value_json FROM settings WHERE key = ?`,
		historyCursorKeySetting,
	).Scan(&valueJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("read history cursor key: %w", err)
	}
	var encoded string
	if err := json.Unmarshal([]byte(valueJSON), &encoded); err != nil {
		return nil, errors.New("stored history cursor key is invalid")
	}
	key, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(key) != sha256.Size {
		return nil, errors.New("stored history cursor key is invalid")
	}
	return key, nil
}

func newCursorCodec(key []byte) (*CursorCodec, error) {
	if len(key) != sha256.Size {
		return nil, errors.New("history cursor key must be 32 bytes")
	}
	codec := &CursorCodec{}
	copy(codec.key[:], key)
	return codec, nil
}

// Encode returns a URL-safe cursor bound to the canonical query without its
// current cursor value.
func (c *CursorCodec) Encode(query HistoryQuery, eventAtUS, id int64) (string, error) {
	if c == nil || id <= 0 || !validDirection(query.Direction) {
		return "", ErrInvalidHistoryCursor
	}
	payload := make([]byte, historyCursorPayloadLen)
	payload[0] = historyCursorVersion
	binary.BigEndian.PutUint64(payload[1:9], uint64(eventAtUS))
	binary.BigEndian.PutUint64(payload[9:17], uint64(id))
	payload[17] = directionByte(query.Direction)
	fingerprint := historyQueryFingerprint(query)
	copy(payload[18:], fingerprint[:])

	signature := hmac.New(sha256.New, c.key[:])
	_, _ = signature.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(signature.Sum(nil)), nil
}

// Decode verifies the cursor signature, version, traversal direction, and
// canonical query fingerprint before returning its keyset.
func (c *CursorCodec) Decode(query HistoryQuery, encoded string) (HistoryCursor, error) {
	if c == nil || len(encoded) == 0 || len(encoded) > maxHistoryCursorBytes ||
		strings.ContainsAny(encoded, "\r\n") || !validDirection(query.Direction) {
		return HistoryCursor{}, ErrInvalidHistoryCursor
	}
	parts := strings.Split(encoded, ".")
	if len(parts) != 2 {
		return HistoryCursor{}, ErrInvalidHistoryCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(payload) != historyCursorPayloadLen {
		return HistoryCursor{}, ErrInvalidHistoryCursor
	}
	providedSignature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(providedSignature) != sha256.Size {
		return HistoryCursor{}, ErrInvalidHistoryCursor
	}
	signature := hmac.New(sha256.New, c.key[:])
	_, _ = signature.Write(payload)
	if !hmac.Equal(providedSignature, signature.Sum(nil)) ||
		payload[0] != historyCursorVersion {
		return HistoryCursor{}, ErrInvalidHistoryCursor
	}
	direction, ok := decodeDirection(payload[17])
	if !ok || direction != query.Direction {
		return HistoryCursor{}, ErrInvalidHistoryCursor
	}
	fingerprint := historyQueryFingerprint(query)
	if !hmac.Equal(payload[18:], fingerprint[:]) {
		return HistoryCursor{}, ErrInvalidHistoryCursor
	}
	id := int64(binary.BigEndian.Uint64(payload[9:17]))
	if id <= 0 {
		return HistoryCursor{}, ErrInvalidHistoryCursor
	}
	return HistoryCursor{
		EventAtUS: int64(binary.BigEndian.Uint64(payload[1:9])),
		ID:        id,
		Direction: direction,
	}, nil
}

func historyQueryFingerprint(query HistoryQuery) [sha256.Size]byte {
	return sha256.Sum256([]byte(query.CanonicalValues(false).Encode()))
}

func validDirection(direction Direction) bool {
	return direction == DirectionOlder || direction == DirectionNewer
}

func directionByte(direction Direction) byte {
	if direction == DirectionNewer {
		return 1
	}
	return 0
}

func decodeDirection(value byte) (Direction, bool) {
	switch value {
	case 0:
		return DirectionOlder, true
	case 1:
		return DirectionNewer, true
	default:
		return "", false
	}
}
