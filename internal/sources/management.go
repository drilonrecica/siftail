package sources

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const (
	DefaultServerPageLimit = 100
	MaxServerPageLimit     = 200
	MaxServerTokens        = 1_000
)

var (
	ErrServerNotFound = errors.New("server not found")
	ErrTokenNotFound  = errors.New("ingestion token not found")
)

type ServerPageQuery struct {
	AfterID int64
	Limit   int
}

type ServerSummary struct {
	Server
	SourceCount      int64
	ActiveTokenCount int64
	LastEventAtUS    *int64
}

type ServerPage struct {
	Servers   []ServerSummary
	HasMore   bool
	NextAfter int64
}

type TokenMetadata struct {
	ID           int64
	ServerID     int64
	Name         string
	Fingerprint  string
	CreatedAtUS  int64
	LastUsedAtUS *int64
	RevokedAtUS  *int64
}

type ServerManagementDetail struct {
	Server          ServerSummary
	Tokens          []TokenMetadata
	TokensTruncated bool
}

func (s *Store) ServerPage(ctx context.Context, query ServerPageQuery) (ServerPage, error) {
	if s == nil || s.db == nil {
		return ServerPage{}, errors.New("server store is unavailable")
	}
	if query.AfterID < 0 {
		return ServerPage{}, errors.New("server cursor must not be negative")
	}
	if query.Limit == 0 {
		query.Limit = DefaultServerPageLimit
	}
	if query.Limit < 1 || query.Limit > MaxServerPageLimit {
		return ServerPage{}, fmt.Errorf("server limit must be between 1 and %d", MaxServerPageLimit)
	}
	rows, err := s.db.QueryContext(ctx, serverSummarySQL+`
		WHERE server.id > ?
		ORDER BY server.id
		LIMIT ?`, query.AfterID, query.Limit+1)
	if err != nil {
		return ServerPage{}, managementReadError(ctx, "query Servers", err)
	}
	defer rows.Close()
	page := ServerPage{Servers: make([]ServerSummary, 0, query.Limit+1)}
	for rows.Next() {
		server, err := scanServerSummary(rows)
		if err != nil {
			return ServerPage{}, fmt.Errorf("scan Server: %w", err)
		}
		page.Servers = append(page.Servers, server)
	}
	if err := rows.Err(); err != nil {
		return ServerPage{}, managementReadError(ctx, "iterate Servers", err)
	}
	if len(page.Servers) > query.Limit {
		page.HasMore = true
		page.Servers = page.Servers[:query.Limit]
		page.NextAfter = page.Servers[len(page.Servers)-1].ID
	}
	return page, nil
}

func (s *Store) ServerManagementDetail(
	ctx context.Context,
	serverID int64,
) (ServerManagementDetail, error) {
	if s == nil || s.db == nil {
		return ServerManagementDetail{}, errors.New("server store is unavailable")
	}
	if serverID <= 0 {
		return ServerManagementDetail{}, ErrServerNotFound
	}
	server, err := scanServerSummary(
		s.db.QueryRowContext(ctx, serverSummarySQL+` WHERE server.id = ?`, serverID),
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ServerManagementDetail{}, ErrServerNotFound
	}
	if err != nil {
		return ServerManagementDetail{}, managementReadError(ctx, "query Server", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT
		id,server_id,name,fingerprint,created_at_us,last_used_at_us,revoked_at_us
		FROM ingestion_tokens
		WHERE server_id = ?
		ORDER BY id DESC
		LIMIT ?`, serverID, MaxServerTokens+1)
	if err != nil {
		return ServerManagementDetail{}, managementReadError(ctx, "query ingestion tokens", err)
	}
	defer rows.Close()
	detail := ServerManagementDetail{
		Server: server,
		Tokens: make([]TokenMetadata, 0, MaxServerTokens+1),
	}
	for rows.Next() {
		var token TokenMetadata
		var lastUsed, revoked sql.NullInt64
		if err := rows.Scan(
			&token.ID, &token.ServerID, &token.Name, &token.Fingerprint,
			&token.CreatedAtUS, &lastUsed, &revoked,
		); err != nil {
			return ServerManagementDetail{}, fmt.Errorf("scan ingestion token: %w", err)
		}
		if lastUsed.Valid {
			token.LastUsedAtUS = &lastUsed.Int64
		}
		if revoked.Valid {
			token.RevokedAtUS = &revoked.Int64
		}
		detail.Tokens = append(detail.Tokens, token)
	}
	if err := rows.Err(); err != nil {
		return ServerManagementDetail{}, managementReadError(ctx, "iterate ingestion tokens", err)
	}
	if len(detail.Tokens) > MaxServerTokens {
		detail.TokensTruncated = true
		detail.Tokens = detail.Tokens[:MaxServerTokens]
	}
	return detail, nil
}

func (s *Store) TokenMetadata(ctx context.Context, tokenID int64) (TokenMetadata, error) {
	if s == nil || s.db == nil || tokenID <= 0 {
		return TokenMetadata{}, ErrTokenNotFound
	}
	var token TokenMetadata
	var lastUsed, revoked sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT
		id,server_id,name,fingerprint,created_at_us,last_used_at_us,revoked_at_us
		FROM ingestion_tokens WHERE id = ?`, tokenID).Scan(
		&token.ID, &token.ServerID, &token.Name, &token.Fingerprint,
		&token.CreatedAtUS, &lastUsed, &revoked,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return TokenMetadata{}, ErrTokenNotFound
	}
	if err != nil {
		return TokenMetadata{}, managementReadError(ctx, "query ingestion token", err)
	}
	if lastUsed.Valid {
		token.LastUsedAtUS = &lastUsed.Int64
	}
	if revoked.Valid {
		token.RevokedAtUS = &revoked.Int64
	}
	return token, nil
}

const serverSummarySQL = `SELECT
	server.id,server.name,coalesce(server.hostname,''),server.created_at_us,
	(SELECT count(*) FROM sources AS source WHERE source.server_id=server.id),
	(SELECT count(*) FROM ingestion_tokens AS token
		WHERE token.server_id=server.id AND token.revoked_at_us IS NULL),
	(SELECT max(source.last_seen_at_us) FROM sources AS source
		WHERE source.server_id=server.id)
	FROM servers AS server`

func scanServerSummary(scanner catalogScanner) (ServerSummary, error) {
	var server ServerSummary
	var lastEvent sql.NullInt64
	if err := scanner.Scan(
		&server.ID, &server.Name, &server.Hostname, &server.CreatedAtUS,
		&server.SourceCount, &server.ActiveTokenCount, &lastEvent,
	); err != nil {
		return ServerSummary{}, err
	}
	if lastEvent.Valid {
		server.LastEventAtUS = &lastEvent.Int64
	}
	return server, nil
}

func managementReadError(ctx context.Context, operation string, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return fmt.Errorf("%s: %w", operation, contextErr)
	}
	return fmt.Errorf("%s: %w", operation, err)
}
