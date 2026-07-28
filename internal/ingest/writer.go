package ingest

import (
	"container/list"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/drilonrecica/siftail/internal/database"
	"github.com/drilonrecica/siftail/internal/logs"
)

const (
	defaultSourceLimit     = 10_000
	defaultContainerLimit  = 100_000
	sourceCacheCapacity    = 4_096
	containerCacheCapacity = 8_192
)

var errCanonicalConflict = errors.New("stable source event ID conflicts with persisted content")

// Publisher must enqueue without waiting for consumers. False indicates that
// Live delivery was truncated; durable persistence remains successful.
type Publisher interface {
	TryPublish([]logs.CommittedEvent) bool
}

type WriterOptions struct {
	SourceLimit    int
	ContainerLimit int
	SourceCache    int
	ContainerCache int
}

type BatchWriter struct {
	coordinator *database.Coordinator
	publisher   Publisher
	options     WriterOptions
	sources     *boundedCache[sourceKey]
	containers  *boundedCache[containerKey]
}

func NewBatchWriter(coordinator *database.Coordinator, publisher Publisher) *BatchWriter {
	return NewBatchWriterWithOptions(coordinator, publisher, WriterOptions{})
}

func NewBatchWriterWithOptions(coordinator *database.Coordinator, publisher Publisher, options WriterOptions) *BatchWriter {
	if options.SourceLimit <= 0 {
		options.SourceLimit = defaultSourceLimit
	}
	if options.ContainerLimit <= 0 {
		options.ContainerLimit = defaultContainerLimit
	}
	if options.SourceCache <= 0 {
		options.SourceCache = sourceCacheCapacity
	}
	if options.ContainerCache <= 0 {
		options.ContainerCache = containerCacheCapacity
	}
	return &BatchWriter{
		coordinator: coordinator,
		publisher:   publisher,
		options:     options,
		sources:     newBoundedCache[sourceKey](options.SourceCache),
		containers:  newBoundedCache[containerKey](options.ContainerCache),
	}
}

type sourceKey struct {
	serverID    int64
	project     string
	environment string
	application string
	service     string
}

type containerKey struct {
	sourceID int64
	id       string
	name     string
}

type cacheUpdate[K comparable] struct {
	key K
	id  int64
}

// Persist commits exactly one HTTP batch transaction. It does not complete the
// queue result; the lifecycle-owned queue worker does that in SFT-013.
func (w *BatchWriter) Persist(ctx context.Context, batch *WriteBatch) error {
	if w == nil || w.coordinator == nil || batch == nil || len(batch.Events) == 0 {
		return &Error{Category: CategoryUnavailable}
	}
	var committed []logs.CommittedEvent
	var sourceUpdates []cacheUpdate[sourceKey]
	var containerUpdates []cacheUpdate[containerKey]
	err := w.coordinator.Do(ctx, func(tx *sql.Tx) error {
		// Admission transfers persistence ownership away from the request.
		// BeginTx remains tied to the coordinator lifecycle; request
		// cancellation must not roll back an already admitted batch.
		mutationCtx := context.Background()
		var err error
		committed, sourceUpdates, containerUpdates, err = w.persistTransaction(mutationCtx, tx, batch)
		return err
	})
	if err != nil {
		return persistenceError(err)
	}
	for _, update := range sourceUpdates {
		w.sources.Add(update.key, update.id)
	}
	for _, update := range containerUpdates {
		w.containers.Add(update.key, update.id)
	}
	sort.Slice(committed, func(i, j int) bool { return committed[i].ID < committed[j].ID })
	if len(committed) > 0 && w.publisher != nil {
		w.publisher.TryPublish(committed)
	}
	return nil
}

func (w *BatchWriter) persistTransaction(
	ctx context.Context,
	tx *sql.Tx,
	batch *WriteBatch,
) ([]logs.CommittedEvent, []cacheUpdate[sourceKey], []cacheUpdate[containerKey], error) {
	if batch.AuthenticatedTokenID > 0 {
		result, err := tx.ExecContext(ctx, `UPDATE ingestion_tokens
			SET last_used_at_us = max(coalesce(last_used_at_us, 0), ?)
			WHERE id = ? AND server_id = ? AND revoked_at_us IS NULL`,
			maxBatchReceivedAt(batch.Events), batch.AuthenticatedTokenID,
			batch.AuthenticatedServerID,
		)
		if err != nil {
			return nil, nil, nil, database.Classify("update ingestion-token use", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, nil, nil, database.Classify("read ingestion-token update", err)
		}
		if affected != 1 {
			return nil, nil, nil, &Error{Category: CategoryForbidden}
		}
	}
	for _, event := range batch.Events {
		if batch.AuthenticatedServerID <= 0 || event.Source.ServerID != batch.AuthenticatedServerID {
			return nil, nil, nil, &Error{Category: CategoryForbidden}
		}
	}

	sourceIDs := make(map[sourceKey]int64)
	var sourceUpdates []cacheUpdate[sourceKey]
	for _, event := range batch.Events {
		key := sourceCacheKey(event.Source)
		if _, ok := sourceIDs[key]; ok {
			continue
		}
		if id, ok := w.sources.Get(key); ok {
			sourceIDs[key] = id
			continue
		}
		id, created, err := w.resolveSource(ctx, tx, event.Source, event.ReceivedAtUS)
		if err != nil {
			return nil, nil, nil, err
		}
		if created {
			var count int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sources WHERE server_id=?", key.serverID).Scan(&count); err != nil {
				return nil, nil, nil, database.Classify("count server sources", err)
			}
			if count > w.options.SourceLimit {
				return nil, nil, nil, &Error{Category: CategoryForbidden}
			}
		}
		sourceIDs[key] = id
		sourceUpdates = append(sourceUpdates, cacheUpdate[sourceKey]{key: key, id: id})
	}

	containerIDs := make(map[containerKey]int64)
	var containerUpdates []cacheUpdate[containerKey]
	for _, event := range batch.Events {
		if event.Container == nil {
			continue
		}
		sourceID := sourceIDs[sourceCacheKey(event.Source)]
		key := containerKey{sourceID: sourceID, id: event.Container.ID, name: event.Container.Name}
		if _, ok := containerIDs[key]; ok {
			continue
		}
		if id, ok := w.containers.Get(key); ok {
			containerIDs[key] = id
			continue
		}
		id, created, err := w.resolveContainer(ctx, tx, sourceID, *event.Container, event.ReceivedAtUS)
		if err != nil {
			return nil, nil, nil, err
		}
		if created {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT count(*)
				FROM container_instances c JOIN sources s ON s.id=c.source_id
				WHERE s.server_id=?`, event.Source.ServerID).Scan(&count); err != nil {
				return nil, nil, nil, database.Classify("count server containers", err)
			}
			if count > w.options.ContainerLimit {
				return nil, nil, nil, &Error{Category: CategoryForbidden}
			}
		}
		containerIDs[key] = id
		containerUpdates = append(containerUpdates, cacheUpdate[containerKey]{key: key, id: id})
	}

	var committed []logs.CommittedEvent
	for _, event := range batch.Events {
		sourceID := sourceIDs[sourceCacheKey(event.Source)]
		var containerID int64
		var databaseContainerID any
		if event.Container != nil {
			containerID = containerIDs[containerKey{sourceID: sourceID, id: event.Container.ID, name: event.Container.Name}]
			databaseContainerID = containerID
		}
		if event.SourceEventID != "" {
			existing, found, err := loadCanonicalEvent(ctx, tx, sourceID, event.SourceEventID, event.Source)
			if err != nil {
				return nil, nil, nil, err
			}
			if found {
				if !logs.CanonicalEqual(existing, event) {
					return nil, nil, nil, errCanonicalConflict
				}
				continue
			}
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO log_events(
			event_at_us, received_at_us, source_id, container_instance_id, stream,
			level_normalized, level_original, message_raw, message_text, attributes_json,
			source_event_id, logger, request_id, error_type, http_method, http_path,
			http_status, duration_ms
		) VALUES (?, ?, ?, ?, ?, ?, nullif(?, ''), ?, ?, nullif(?, ''),
			nullif(?, ''), nullif(?, ''), nullif(?, ''), nullif(?, ''), nullif(?, ''),
			nullif(?, ''), ?, ?)`,
			event.EventAtUS, event.ReceivedAtUS, sourceID, databaseContainerID, event.Stream,
			event.Level, event.OriginalLevel, event.MessageRaw, event.MessageText, string(event.Attributes),
			event.SourceEventID, event.Common.Logger, event.Common.RequestID, event.Common.ErrorType,
			event.Common.HTTPMethod, event.Common.HTTPPath, event.Common.HTTPStatus, event.Common.DurationMS)
		if err != nil {
			return nil, nil, nil, database.Classify("insert log event", err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			return nil, nil, nil, database.Classify("read log event ID", err)
		}
		committed = append(committed, logs.CommittedEvent{
			ID: id, SourceID: sourceID, ContainerInstanceID: containerID, Event: event,
		})
	}

	if err := updateLastSeen(ctx, tx, batch.Events, sourceIDs, containerIDs); err != nil {
		return nil, nil, nil, err
	}
	return committed, sourceUpdates, containerUpdates, nil
}

func maxBatchReceivedAt(events []logs.CanonicalEvent) int64 {
	var latest int64
	for _, event := range events {
		if event.ReceivedAtUS > latest {
			latest = event.ReceivedAtUS
		}
	}
	return latest
}

func (w *BatchWriter) resolveSource(ctx context.Context, tx *sql.Tx, source logs.SourceIdentity, seen int64) (int64, bool, error) {
	key := sourceCacheKey(source)
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM sources
		WHERE server_id=? AND project_key=? AND environment_key=? AND application_key=? AND service_key=?`,
		key.serverID, key.project, key.environment, key.application, key.service).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, database.Classify("resolve source", err)
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO sources(
		server_id, project_key, environment_key, application_key, service_key,
		project_label, environment_label, application_label, service_label,
		first_seen_at_us, last_seen_at_us
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		source.ServerID, source.Project, source.Environment, source.Application, source.Service,
		source.ProjectLabel, source.EnvLabel, source.AppLabel, source.ServiceLabel, seen, seen)
	if err != nil {
		return 0, false, database.Classify("create source", err)
	}
	id, err = result.LastInsertId()
	if err != nil {
		return 0, false, database.Classify("read source ID", err)
	}
	return id, true, nil
}

func (w *BatchWriter) resolveContainer(
	ctx context.Context,
	tx *sql.Tx,
	sourceID int64,
	container logs.ContainerIdentity,
	seen int64,
) (int64, bool, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM container_instances
		WHERE source_id=? AND container_id IS ? AND container_name IS ?`,
		sourceID, nullable(container.ID), nullable(container.Name)).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, database.Classify("resolve container", err)
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO container_instances(
		source_id, container_id, container_name, first_seen_at_us, last_seen_at_us
	) VALUES (?, nullif(?, ''), nullif(?, ''), ?, ?)`,
		sourceID, container.ID, container.Name, seen, seen)
	if err != nil {
		return 0, false, database.Classify("create container", err)
	}
	id, err = result.LastInsertId()
	if err != nil {
		return 0, false, database.Classify("read container ID", err)
	}
	return id, true, nil
}

func updateLastSeen(
	ctx context.Context,
	tx *sql.Tx,
	events []logs.CanonicalEvent,
	sourceIDs map[sourceKey]int64,
	containerIDs map[containerKey]int64,
) error {
	sourceSeen := make(map[int64]int64)
	containerSeen := make(map[int64]int64)
	sourceLabels := make(map[int64]logs.SourceIdentity)
	for _, event := range events {
		sourceID := sourceIDs[sourceCacheKey(event.Source)]
		if event.ReceivedAtUS > sourceSeen[sourceID] {
			sourceSeen[sourceID] = event.ReceivedAtUS
			sourceLabels[sourceID] = event.Source
		}
		if event.Container != nil {
			containerID := containerIDs[containerKey{sourceID: sourceID, id: event.Container.ID, name: event.Container.Name}]
			if event.ReceivedAtUS > containerSeen[containerID] {
				containerSeen[containerID] = event.ReceivedAtUS
			}
		}
	}
	for id, seen := range sourceSeen {
		labels := sourceLabels[id]
		if _, err := tx.ExecContext(ctx, `UPDATE sources SET
			project_label=?, environment_label=?, application_label=?, service_label=?,
			last_seen_at_us=max(last_seen_at_us, ?) WHERE id=?`,
			labels.ProjectLabel, labels.EnvLabel, labels.AppLabel, labels.ServiceLabel, seen, id); err != nil {
			return database.Classify("update source last seen", err)
		}
	}
	for id, seen := range containerSeen {
		if _, err := tx.ExecContext(ctx,
			"UPDATE container_instances SET last_seen_at_us=max(last_seen_at_us, ?) WHERE id=?",
			seen, id); err != nil {
			return database.Classify("update container last seen", err)
		}
	}
	return nil
}

func loadCanonicalEvent(
	ctx context.Context,
	tx *sql.Tx,
	sourceID int64,
	sourceEventID string,
	source logs.SourceIdentity,
) (logs.CanonicalEvent, bool, error) {
	var event logs.CanonicalEvent
	var containerID, containerName sql.NullString
	var levelOriginal, attributes sql.NullString
	var logger, requestID, errorType, method, path sql.NullString
	var status sql.NullInt64
	var duration sql.NullFloat64
	err := tx.QueryRowContext(ctx, `SELECT
		e.event_at_us, e.stream, e.level_normalized, e.level_original,
		e.message_raw, e.message_text, e.attributes_json, e.source_event_id,
		e.logger, e.request_id, e.error_type, e.http_method, e.http_path,
		e.http_status, e.duration_ms, c.container_id, c.container_name
		FROM log_events e
		LEFT JOIN container_instances c ON c.id=e.container_instance_id
		WHERE e.source_id=? AND e.source_event_id=?`,
		sourceID, sourceEventID).Scan(
		&event.EventAtUS, &event.Stream, &event.Level, &levelOriginal,
		&event.MessageRaw, &event.MessageText, &attributes, &event.SourceEventID,
		&logger, &requestID, &errorType, &method, &path, &status, &duration,
		&containerID, &containerName)
	if errors.Is(err, sql.ErrNoRows) {
		return logs.CanonicalEvent{}, false, nil
	}
	if err != nil {
		return logs.CanonicalEvent{}, false, database.Classify("load stable event identity", err)
	}
	event.Source = source
	event.OriginalLevel = levelOriginal.String
	if attributes.Valid {
		event.Attributes = []byte(attributes.String)
	}
	event.Common.Logger = logger.String
	event.Common.RequestID = requestID.String
	event.Common.ErrorType = errorType.String
	event.Common.HTTPMethod = method.String
	event.Common.HTTPPath = path.String
	if status.Valid {
		event.Common.HTTPStatus = &status.Int64
	}
	if duration.Valid {
		event.Common.DurationMS = &duration.Float64
	}
	if containerID.Valid || containerName.Valid {
		event.Container = &logs.ContainerIdentity{ID: containerID.String, Name: containerName.String}
	}
	return event, true, nil
}

func sourceCacheKey(source logs.SourceIdentity) sourceKey {
	return sourceKey{
		serverID: source.ServerID, project: source.Project, environment: source.Environment,
		application: source.Application, service: source.Service,
	}
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func persistenceError(err error) error {
	if errors.Is(err, errCanonicalConflict) {
		return &Error{Category: CategoryConflict}
	}
	var ingestErr *Error
	if errors.As(err, &ingestErr) {
		return ingestErr
	}
	var databaseErr *database.CategoryError
	if errors.As(err, &databaseErr) {
		switch databaseErr.Category {
		case database.CategoryBusy, database.CategoryIO:
			return &Error{Category: CategoryUnavailable}
		case database.CategoryFull:
			return &Error{Category: CategoryStorageFull}
		default:
			return &Error{Category: CategoryUnavailable}
		}
	}
	if errors.Is(err, database.ErrCoordinatorClosed) || errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return &Error{Category: CategoryUnavailable}
	}
	return &Error{Category: CategoryUnavailable}
}

type boundedCache[K comparable] struct {
	capacity int
	order    *list.List
	entries  map[K]*list.Element
	mu       sync.Mutex
}

type cacheEntry[K comparable] struct {
	key K
	id  int64
}

func newBoundedCache[K comparable](capacity int) *boundedCache[K] {
	return &boundedCache[K]{capacity: capacity, order: list.New(), entries: make(map[K]*list.Element)}
}

func (c *boundedCache[K]) Get(key K) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[key]
	if !ok {
		return 0, false
	}
	c.order.MoveToFront(element)
	return element.Value.(cacheEntry[K]).id, true
}

func (c *boundedCache[K]) Add(key K, id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		element.Value = cacheEntry[K]{key: key, id: id}
		c.order.MoveToFront(element)
		return
	}
	element := c.order.PushFront(cacheEntry[K]{key: key, id: id})
	c.entries[key] = element
	if c.order.Len() <= c.capacity {
		return
	}
	oldest := c.order.Back()
	delete(c.entries, oldest.Value.(cacheEntry[K]).key)
	c.order.Remove(oldest)
}

func (c *boundedCache[K]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

func (c *boundedCache[K]) String() string {
	return fmt.Sprintf("bounded cache %d/%d", c.Len(), c.capacity)
}
