CREATE TABLE settings (
    key TEXT PRIMARY KEY COLLATE BINARY
        CHECK (length(CAST(key AS BLOB)) BETWEEN 1 AND 128),
    value_json TEXT NOT NULL CHECK (json_valid(value_json)),
    updated_at_us INTEGER NOT NULL
) STRICT;

CREATE TABLE servers (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL COLLATE BINARY
        CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 128),
    hostname TEXT COLLATE BINARY
        CHECK (hostname IS NULL OR length(CAST(hostname AS BLOB)) BETWEEN 1 AND 255),
    created_at_us INTEGER NOT NULL,
    UNIQUE (name)
) STRICT;

CREATE TABLE ingestion_tokens (
    id INTEGER PRIMARY KEY,
    server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    name TEXT NOT NULL COLLATE BINARY
        CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 128),
    token_hash BLOB NOT NULL CHECK (length(token_hash) = 32),
    fingerprint TEXT NOT NULL COLLATE BINARY
        CHECK (length(CAST(fingerprint AS BLOB)) BETWEEN 8 AND 32),
    created_at_us INTEGER NOT NULL,
    last_used_at_us INTEGER,
    revoked_at_us INTEGER,
    UNIQUE (token_hash),
    UNIQUE (server_id, name)
) STRICT;

CREATE INDEX ingestion_tokens_fingerprint_idx
ON ingestion_tokens(fingerprint);

CREATE TABLE sources (
    id INTEGER PRIMARY KEY,
    server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    project_key TEXT NOT NULL COLLATE BINARY
        CHECK (length(CAST(project_key AS BLOB)) BETWEEN 1 AND 128),
    environment_key TEXT NOT NULL COLLATE BINARY
        CHECK (length(CAST(environment_key AS BLOB)) BETWEEN 1 AND 128),
    application_key TEXT NOT NULL COLLATE BINARY
        CHECK (length(CAST(application_key AS BLOB)) BETWEEN 1 AND 128),
    service_key TEXT NOT NULL COLLATE BINARY
        CHECK (length(CAST(service_key AS BLOB)) BETWEEN 1 AND 128),
    project_label TEXT NOT NULL CHECK (length(CAST(project_label AS BLOB)) BETWEEN 1 AND 128),
    environment_label TEXT NOT NULL CHECK (length(CAST(environment_label AS BLOB)) BETWEEN 1 AND 128),
    application_label TEXT NOT NULL CHECK (length(CAST(application_label AS BLOB)) BETWEEN 1 AND 128),
    service_label TEXT NOT NULL CHECK (length(CAST(service_label AS BLOB)) BETWEEN 1 AND 128),
    alias TEXT CHECK (alias IS NULL OR length(CAST(alias AS BLOB)) BETWEEN 1 AND 128),
    first_seen_at_us INTEGER NOT NULL,
    last_seen_at_us INTEGER NOT NULL CHECK (last_seen_at_us >= first_seen_at_us),
    UNIQUE (server_id, project_key, environment_key, application_key, service_key)
) STRICT;

CREATE TABLE container_instances (
    id INTEGER PRIMARY KEY,
    source_id INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    container_id TEXT COLLATE BINARY
        CHECK (container_id IS NULL OR length(CAST(container_id AS BLOB)) BETWEEN 1 AND 255),
    container_name TEXT COLLATE BINARY
        CHECK (container_name IS NULL OR length(CAST(container_name AS BLOB)) BETWEEN 1 AND 255),
    first_seen_at_us INTEGER NOT NULL,
    last_seen_at_us INTEGER NOT NULL CHECK (last_seen_at_us >= first_seen_at_us),
    CHECK (container_id IS NOT NULL OR container_name IS NOT NULL),
    UNIQUE (id, source_id),
    UNIQUE (source_id, container_id),
    UNIQUE (source_id, container_name)
) STRICT;

CREATE TABLE log_events (
    id INTEGER PRIMARY KEY,
    event_at_us INTEGER NOT NULL,
    received_at_us INTEGER NOT NULL,
    retention_at_us INTEGER GENERATED ALWAYS AS
        (min(event_at_us, received_at_us)) STORED,
    source_id INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    container_instance_id INTEGER,
    stream TEXT NOT NULL CHECK (stream IN ('stdout', 'stderr', 'unknown')),
    level_normalized TEXT NOT NULL
        CHECK (level_normalized IN ('trace', 'debug', 'info', 'warn', 'error', 'fatal', 'unknown')),
    level_original TEXT
        CHECK (level_original IS NULL OR length(CAST(level_original AS BLOB)) BETWEEN 1 AND 64),
    message_raw BLOB NOT NULL CHECK (length(message_raw) <= 1048576),
    message_text TEXT NOT NULL CHECK (length(CAST(message_text AS BLOB)) <= 1048576),
    attributes_json TEXT
        CHECK (attributes_json IS NULL OR
            (json_valid(attributes_json) AND json_type(attributes_json) = 'object'
             AND length(CAST(attributes_json AS BLOB)) <= 262144)),
    source_event_id TEXT COLLATE BINARY
        CHECK (source_event_id IS NULL OR length(CAST(source_event_id AS BLOB)) BETWEEN 1 AND 255),
    logger TEXT CHECK (logger IS NULL OR length(CAST(logger AS BLOB)) <= 255),
    request_id TEXT CHECK (request_id IS NULL OR length(CAST(request_id AS BLOB)) <= 255),
    error_type TEXT CHECK (error_type IS NULL OR length(CAST(error_type AS BLOB)) <= 255),
    http_method TEXT CHECK (http_method IS NULL OR length(CAST(http_method AS BLOB)) <= 32),
    http_path TEXT CHECK (http_path IS NULL OR length(CAST(http_path AS BLOB)) <= 2048),
    http_status INTEGER CHECK (http_status IS NULL OR http_status BETWEEN 100 AND 999),
    duration_ms REAL CHECK (duration_ms IS NULL OR duration_ms >= 0),
    FOREIGN KEY (container_instance_id, source_id)
        REFERENCES container_instances(id, source_id)
) STRICT;

CREATE UNIQUE INDEX log_events_source_event_id_uq
ON log_events(source_id, source_event_id)
WHERE source_event_id IS NOT NULL;

-- Primary History order; high write/storage cost is accepted because every
-- unfiltered page uses it. It does not slow retention lookup.
CREATE INDEX log_events_time_idx
ON log_events(event_at_us DESC, id DESC);

-- Oldest-first bounded retention deletion; high selectivity by timestamp and
-- direct deletion tie-breaker justify its write/storage cost.
CREATE INDEX log_events_retention_idx
ON log_events(retention_at_us, id);

-- Stable-source History filtering; selective on deployments with many sources.
CREATE INDEX log_events_source_time_idx
ON log_events(source_id, event_at_us DESC, id DESC);

-- Source plus severity History filtering; its extra write/storage cost is
-- bounded to canonical levels and it is not used by retention deletion.
CREATE INDEX log_events_source_level_time_idx
ON log_events(source_id, level_normalized, event_at_us DESC, id DESC);

-- Sparse exact request correlation. The partial index avoids cost for the
-- expected majority of events without request IDs.
CREATE INDEX log_events_request_id_idx
ON log_events(request_id)
WHERE request_id IS NOT NULL;

-- Sparse secondary container inspection. It does not participate in retention.
CREATE INDEX log_events_container_time_idx
ON log_events(container_instance_id, event_at_us DESC, id DESC)
WHERE container_instance_id IS NOT NULL;

CREATE TRIGGER log_events_immutable_update
BEFORE UPDATE ON log_events
BEGIN
    SELECT RAISE(ABORT, 'log events are immutable');
END;
