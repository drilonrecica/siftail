package sources

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	DefaultCatalogLimit = 100
	MaxCatalogLimit     = 200
	MaxDetailContainers = 200

	sourceActiveWindow = 24 * time.Hour
	sourceCleanupAge   = 90 * 24 * time.Hour
)

var ErrSourceNotFound = errors.New("source not found")

type CatalogQuery struct {
	AfterID int64
	Limit   int
}

type CatalogSource struct {
	ID                  int64
	ServerID            int64
	ServerName          string
	ServerHostname      string
	ProjectKey          string
	EnvironmentKey      string
	ApplicationKey      string
	ServiceKey          string
	ProjectLabel        string
	EnvironmentLabel    string
	ApplicationLabel    string
	ServiceLabel        string
	Alias               *string
	FirstSeenAtUS       int64
	LastSeenAtUS        int64
	Active              bool
	CleanupEligible     bool
	HasRetainedEvents   bool
	LatestRetainedAtUS  *int64
	HasContainerHistory bool
}

func (s CatalogSource) DisplayName() string {
	if s.Alias != nil {
		return *s.Alias
	}
	return s.ApplicationLabel + "/" + s.ServiceLabel
}

type CatalogPage struct {
	Sources   []CatalogSource
	HasMore   bool
	NextAfter int64
}

type ContainerObservation struct {
	ID            int64
	ContainerID   string
	ContainerName string
	FirstSeenAtUS int64
	LastSeenAtUS  int64
	Active        bool
}

type SourceDetail struct {
	Source              CatalogSource
	Containers          []ContainerObservation
	ContainersTruncated bool
}

func (s *Store) Catalog(ctx context.Context, query CatalogQuery) (CatalogPage, error) {
	if s == nil || s.db == nil {
		return CatalogPage{}, errors.New("source catalog is unavailable")
	}
	if query.AfterID < 0 {
		return CatalogPage{}, errors.New("source cursor must not be negative")
	}
	if query.Limit == 0 {
		query.Limit = DefaultCatalogLimit
	}
	if query.Limit < 1 || query.Limit > MaxCatalogLimit {
		return CatalogPage{}, fmt.Errorf("source limit must be between 1 and %d", MaxCatalogLimit)
	}
	rows, err := s.db.QueryContext(ctx, catalogSourceSQL+`
		WHERE source.id > ?
		ORDER BY source.id
		LIMIT ?`, query.AfterID, query.Limit+1)
	if err != nil {
		return CatalogPage{}, catalogReadError(ctx, "query source catalog", err)
	}
	defer rows.Close()

	now := s.now()
	page := CatalogPage{Sources: make([]CatalogSource, 0, query.Limit+1)}
	for rows.Next() {
		source, err := scanCatalogSource(rows, now)
		if err != nil {
			return CatalogPage{}, fmt.Errorf("scan source catalog: %w", err)
		}
		page.Sources = append(page.Sources, source)
	}
	if err := rows.Err(); err != nil {
		return CatalogPage{}, catalogReadError(ctx, "iterate source catalog", err)
	}
	if len(page.Sources) > query.Limit {
		page.HasMore = true
		page.Sources = page.Sources[:query.Limit]
		page.NextAfter = page.Sources[len(page.Sources)-1].ID
	}
	return page, nil
}

func (s *Store) SourceDetail(ctx context.Context, id int64) (SourceDetail, error) {
	if s == nil || s.db == nil {
		return SourceDetail{}, errors.New("source catalog is unavailable")
	}
	if id <= 0 {
		return SourceDetail{}, ErrSourceNotFound
	}
	source, err := scanCatalogSource(
		s.db.QueryRowContext(ctx, catalogSourceSQL+` WHERE source.id = ?`, id),
		s.now(),
	)
	if errors.Is(err, sql.ErrNoRows) {
		return SourceDetail{}, ErrSourceNotFound
	}
	if err != nil {
		return SourceDetail{}, catalogReadError(ctx, "query source detail", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, coalesce(container_id, ''), coalesce(container_name, ''),
		first_seen_at_us, last_seen_at_us
		FROM container_instances
		WHERE source_id = ?
		ORDER BY last_seen_at_us DESC, id DESC
		LIMIT ?`, id, MaxDetailContainers+1)
	if err != nil {
		return SourceDetail{}, catalogReadError(ctx, "query source containers", err)
	}
	defer rows.Close()

	detail := SourceDetail{
		Source:     source,
		Containers: make([]ContainerObservation, 0, MaxDetailContainers+1),
	}
	activeAfter := s.now().Add(-sourceActiveWindow).UnixMicro()
	for rows.Next() {
		var container ContainerObservation
		if err := rows.Scan(
			&container.ID, &container.ContainerID, &container.ContainerName,
			&container.FirstSeenAtUS, &container.LastSeenAtUS,
		); err != nil {
			return SourceDetail{}, fmt.Errorf("scan source container: %w", err)
		}
		container.Active = container.LastSeenAtUS >= activeAfter
		detail.Containers = append(detail.Containers, container)
	}
	if err := rows.Err(); err != nil {
		return SourceDetail{}, catalogReadError(ctx, "iterate source containers", err)
	}
	if len(detail.Containers) > MaxDetailContainers {
		detail.ContainersTruncated = true
		detail.Containers = detail.Containers[:MaxDetailContainers]
	}
	return detail, nil
}

const catalogSourceSQL = `SELECT
	source.id, source.server_id, server.name, coalesce(server.hostname, ''),
	source.project_key, source.environment_key, source.application_key, source.service_key,
	source.project_label, source.environment_label, source.application_label, source.service_label,
	source.alias, source.first_seen_at_us, source.last_seen_at_us,
	(SELECT event.event_at_us
		FROM log_events AS event
		WHERE event.source_id = source.id
		ORDER BY event.event_at_us DESC, event.id DESC
		LIMIT 1),
	EXISTS(SELECT 1 FROM container_instances AS container
		WHERE container.source_id = source.id LIMIT 1)
	FROM sources AS source
	JOIN servers AS server ON server.id = source.server_id`

type catalogScanner interface {
	Scan(...any) error
}

func scanCatalogSource(scanner catalogScanner, now time.Time) (CatalogSource, error) {
	var source CatalogSource
	var alias sql.NullString
	var latestRetained sql.NullInt64
	if err := scanner.Scan(
		&source.ID, &source.ServerID, &source.ServerName, &source.ServerHostname,
		&source.ProjectKey, &source.EnvironmentKey, &source.ApplicationKey, &source.ServiceKey,
		&source.ProjectLabel, &source.EnvironmentLabel,
		&source.ApplicationLabel, &source.ServiceLabel,
		&alias, &source.FirstSeenAtUS, &source.LastSeenAtUS,
		&latestRetained, &source.HasContainerHistory,
	); err != nil {
		return CatalogSource{}, err
	}
	if alias.Valid {
		source.Alias = &alias.String
	}
	if latestRetained.Valid {
		source.HasRetainedEvents = true
		source.LatestRetainedAtUS = &latestRetained.Int64
	}
	source.Active = source.LastSeenAtUS >= now.Add(-sourceActiveWindow).UnixMicro()
	source.CleanupEligible = source.LastSeenAtUS < now.Add(-sourceCleanupAge).UnixMicro()
	return source, nil
}

func catalogReadError(ctx context.Context, operation string, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return fmt.Errorf("%s: %w", operation, contextErr)
	}
	return fmt.Errorf("%s: %w", operation, err)
}
