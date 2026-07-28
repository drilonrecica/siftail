package logs

import (
	"context"
	"database/sql"
	"fmt"
)

// Store owns bounded read-only application-log SQL.
type Store struct {
	db          *sql.DB
	cursorCodec *CursorCodec
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

type StoredEvent struct {
	ID           int64
	EventAtUS    int64
	ReceivedAtUS int64
	MessageRaw   []byte
	MessageText  string
}

// Recent returns at most limit events in canonical History order.
func (s *Store) Recent(ctx context.Context, limit int) ([]StoredEvent, error) {
	if limit < 1 || limit > 500 {
		return nil, fmt.Errorf("recent event limit must be between 1 and 500")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, event_at_us, received_at_us, message_raw, message_text
		FROM log_events
		ORDER BY event_at_us DESC, id DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent events: %w", err)
	}
	defer rows.Close()
	events := make([]StoredEvent, 0, limit)
	for rows.Next() {
		var event StoredEvent
		if err := rows.Scan(
			&event.ID, &event.EventAtUS, &event.ReceivedAtUS,
			&event.MessageRaw, &event.MessageText,
		); err != nil {
			return nil, fmt.Errorf("scan recent event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list recent events: %w", err)
	}
	return events, nil
}
