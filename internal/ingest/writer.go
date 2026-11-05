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
	defaultContainerLimit  = logs.MaxContainerInstancesPerServer
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
	// beforeInsert is a test seam that runs inside the batch transaction
	// after resolution; it simulates a cached row disappearing unseen.
	beforeInsert func(*sql.Tx) error
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

// containerKey is the writer's container identity (ADR 0006): the container
// ID when the producer supplies one, otherwise the name. Exactly one of id and
// name is set, so a renamed container keeps its key.
type containerKey struct {
	sourceID int64
	id       string
	name     string
}

func containerCacheKey(sourceID int64, container logs.ContainerIdentity) containerKey {
	if container.ID != "" {
		return containerKey{sourceID: sourceID, id: container.ID}
	}
	return containerKey{sourceID: sourceID, name: container.Name}
}

// containerResolution is one container instance as seen inside a batch
// transaction: its stored label and last-seen time, and the newest label the
// batch carries for it.
type containerResolution struct {
	instanceID int64
	storedName string
	storedSeen int64
	label      string
	labelSeen  int64
}

func (r *containerResolution) observe(name string, seen int64) {
	if seen >= r.labelSeen {
		r.label, r.labelSeen = name, seen
	}
}

type cacheUpdate[K comparable] struct {
	key K
	id  int64
}

// Persist commits exactly one HTTP batch transaction. It does not complete the
// queue result; the lifecycle-owned queue worker does that in SFT-013.
//
// The source and container caches are hints: every hit is confirmed inside the
// transaction, because source removal, Clear logs, and retention pruning delete
// rows the writer may still cache (audit DAT-02). Should a stale ID ever reach
// an INSERT regardless, the foreign-key failure clears the batch's entries and
// the batch is retried once, uncached, within the same request.
func (w *BatchWriter) Persist(ctx context.Context, batch *WriteBatch) error {
	if w == nil || w.coordinator == nil || batch == nil || len(batch.Events) == 0 {
		return &Error{Category: CategoryUnavailable}
	}
	committed, err := w.persistAttempt(ctx, batch)
	if database.IsForeignKeyViolation(err) {
		w.forgetBatch(batch)
		committed, err = w.persistAttempt(ctx, batch)
	}
	if err != nil {
		return persistenceError(err)
	}
	sort.Slice(committed, func(i, j int) bool { return committed[i].ID < committed[j].ID })
	if len(committed) > 0 && w.publisher != nil {
		w.publisher.TryPublish(committed)
	}
	return nil
}

// persistAttempt runs one batch transaction and, only after it commits, records
// the IDs it resolved in the caches.
func (w *BatchWriter) persistAttempt(ctx context.Context, batch *WriteBatch) ([]logs.CommittedEvent, error) {
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
		return nil, err
	}
	for _, update := range sourceUpdates {
		w.sources.Add(update.key, update.id)
	}
	for _, update := range containerUpdates {
		w.containers.Add(update.key, update.id)
	}
	return committed, nil
}

// forgetBatch drops every cache entry the batch could have used, so the retry
// resolves each source and container from SQLite.
func (w *BatchWriter) forgetBatch(batch *WriteBatch) {
	for _, event := range batch.Events {
		key := sourceCacheKey(event.Source)
		if sourceID, ok := w.sources.Get(key); ok && event.Container != nil {
			w.containers.Remove(containerCacheKey(sourceID, *event.Container))
		}
		w.sources.Remove(key)
	}
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
			// Source removal deletes rows the cache may still hold, and SQLite
			// reuses a freed rowid, so the hit must still carry this identity.
			found, err := cachedSourceExists(ctx, tx, id, key)
			if err != nil {
				return nil, nil, nil, err
			}
			if found {
				sourceIDs[key] = id
				continue
			}
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

	containers := make(map[containerKey]*containerResolution)
	var containerOrder []containerKey
	var containerUpdates []cacheUpdate[containerKey]
	containerCreated := false
	for _, event := range batch.Events {
		if event.Container == nil {
			continue
		}
		sourceID := sourceIDs[sourceCacheKey(event.Source)]
		key := containerCacheKey(sourceID, *event.Container)
		if resolved, ok := containers[key]; ok {
			resolved.observe(event.Container.Name, event.ReceivedAtUS)
			continue
		}
		var resolved *containerResolution
		if id, ok := w.containers.Get(key); ok {
			// Retention pruning and source cleanup delete unreferenced
			// instances, so a cached ID is a hint that SQLite confirms.
			cached, found, err := loadCachedContainer(ctx, tx, id, key)
			if err != nil {
				return nil, nil, nil, err
			}
			if found {
				resolved = &cached
			}
		}
		if resolved == nil {
			fresh, created, err := w.resolveContainer(ctx, tx, sourceID, *event.Container, event.ReceivedAtUS)
			if err != nil {
				return nil, nil, nil, err
			}
			containerCreated = containerCreated || created
			resolved = &fresh
			containerUpdates = append(containerUpdates, cacheUpdate[containerKey]{key: key, id: fresh.instanceID})
		}
		resolved.observe(event.Container.Name, event.ReceivedAtUS)
		containers[key] = resolved
		containerOrder = append(containerOrder, key)
	}
	if containerCreated {
		// One count per batch: the rejection is atomic for the whole batch,
		// so the final total is the only one that matters.
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*)
			FROM container_instances c JOIN sources s ON s.id=c.source_id
			WHERE s.server_id=?`, batch.AuthenticatedServerID).Scan(&count); err != nil {
			return nil, nil, nil, database.Classify("count server containers", err)
		}
		if count > w.options.ContainerLimit {
			return nil, nil, nil, &Error{Category: CategoryForbidden}
		}
	}
	if err := updateContainerLabels(ctx, tx, containerOrder, containers); err != nil {
		return nil, nil, nil, err
	}
	if w.beforeInsert != nil {
		if err := w.beforeInsert(tx); err != nil {
			return nil, nil, nil, err
		}
	}

	var committed []logs.CommittedEvent
	for _, event := range batch.Events {
		sourceID := sourceIDs[sourceCacheKey(event.Source)]
		var containerID int64
		var databaseContainerID any
		if event.Container != nil {
			containerID = containers[containerCacheKey(sourceID, *event.Container)].instanceID
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

	if err := updateLastSeen(ctx, tx, batch.Events, sourceIDs, containers); err != nil {
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

// cachedSourceExists confirms a cached source ID with one primary-key lookup.
func cachedSourceExists(ctx context.Context, tx *sql.Tx, id int64, key sourceKey) (bool, error) {
	var found int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM sources
		WHERE id=? AND server_id=? AND project_key=? AND environment_key=?
		AND application_key=? AND service_key=?`,
		id, key.serverID, key.project, key.environment, key.application, key.service).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, database.Classify("verify cached source", err)
	}
	return true, nil
}

// resolveContainer applies ADR 0006: an event with a container ID resolves by
// (source, container ID); a name-only event resolves only to an ID-less
// instance with that name, because attaching it to an instance with an ID would
// guess identity. Neither is unique in the schema; should a defect ever store
// two candidates, the most recently seen wins rather than blocking ingestion.
func (w *BatchWriter) resolveContainer(
	ctx context.Context,
	tx *sql.Tx,
	sourceID int64,
	container logs.ContainerIdentity,
	seen int64,
) (containerResolution, bool, error) {
	query := `SELECT id, coalesce(container_name, ''), last_seen_at_us
		FROM container_instances
		WHERE source_id=? AND container_id=?
		ORDER BY last_seen_at_us DESC, id DESC LIMIT 1`
	match := container.ID
	if container.ID == "" {
		query = `SELECT id, coalesce(container_name, ''), last_seen_at_us
			FROM container_instances
			WHERE source_id=? AND container_id IS NULL AND container_name=?
			ORDER BY last_seen_at_us DESC, id DESC LIMIT 1`
		match = container.Name
	}
	var resolved containerResolution
	err := tx.QueryRowContext(ctx, query, sourceID, match).Scan(
		&resolved.instanceID, &resolved.storedName, &resolved.storedSeen)
	if err == nil {
		return resolved, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return containerResolution{}, false, database.Classify("resolve container", err)
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO container_instances(
		source_id, container_id, container_name, first_seen_at_us, last_seen_at_us
	) VALUES (?, nullif(?, ''), nullif(?, ''), ?, ?)`,
		sourceID, container.ID, container.Name, seen, seen)
	if err != nil {
		return containerResolution{}, false, database.Classify("create container", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return containerResolution{}, false, database.Classify("read container ID", err)
	}
	return containerResolution{instanceID: id, storedName: container.Name, storedSeen: seen}, true, nil
}

// loadCachedContainer confirms a cached instance ID. SQLite reuses the largest
// rowid after a deletion, so the row must still carry the cached identity, not
// merely the cached ID.
func loadCachedContainer(ctx context.Context, tx *sql.Tx, id int64, key containerKey) (containerResolution, bool, error) {
	query := `SELECT coalesce(container_name, ''), last_seen_at_us
		FROM container_instances WHERE id=? AND source_id=? AND container_id=?`
	match := key.id
	if key.id == "" {
		query = `SELECT coalesce(container_name, ''), last_seen_at_us
			FROM container_instances
			WHERE id=? AND source_id=? AND container_id IS NULL AND container_name=?`
		match = key.name
	}
	resolved := containerResolution{instanceID: id}
	err := tx.QueryRowContext(ctx, query, id, key.sourceID, match).Scan(
		&resolved.storedName, &resolved.storedSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return containerResolution{}, false, nil
	}
	if err != nil {
		return containerResolution{}, false, database.Classify("verify cached container", err)
	}
	return resolved, true, nil
}

// updateContainerLabels records a changed container name on its instance. The
// name is a mutable label (ADR 0006); a label older than the instance's last
// observation (a redelivered chunk) does not overwrite a newer one.
func updateContainerLabels(
	ctx context.Context,
	tx *sql.Tx,
	order []containerKey,
	containers map[containerKey]*containerResolution,
) error {
	for _, key := range order {
		resolved := containers[key]
		if resolved.label == resolved.storedName || resolved.labelSeen < resolved.storedSeen {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE container_instances SET container_name=nullif(?, '') WHERE id=?",
			resolved.label, resolved.instanceID); err != nil {
			return database.Classify("update container label", err)
		}
	}
	return nil
}

func updateLastSeen(
	ctx context.Context,
	tx *sql.Tx,
	events []logs.CanonicalEvent,
	sourceIDs map[sourceKey]int64,
	containers map[containerKey]*containerResolution,
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
			containerID := containers[containerCacheKey(sourceID, *event.Container)].instanceID
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

func (c *boundedCache[K]) Remove(key K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		delete(c.entries, key)
		c.order.Remove(element)
	}
}

func (c *boundedCache[K]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

func (c *boundedCache[K]) String() string {
	return fmt.Sprintf("bounded cache %d/%d", c.Len(), c.capacity)
}
