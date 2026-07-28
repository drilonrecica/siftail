CREATE TABLE security_audit_events (
    id INTEGER PRIMARY KEY,
    occurred_at_us INTEGER NOT NULL CHECK (occurred_at_us > 0),
    category TEXT NOT NULL COLLATE BINARY CHECK (category IN (
        'authentication',
        'session',
        'administrator_credential',
        'ingestion_token',
        'source_administration',
        'retention_settings',
        'backup_restore',
        'export',
        'proxy_auth_configuration',
        'destructive_operation'
    )),
    action TEXT NOT NULL COLLATE BINARY
        CHECK (length(CAST(action AS BLOB)) BETWEEN 1 AND 128),
    outcome TEXT NOT NULL COLLATE BINARY CHECK (outcome IN (
        'succeeded',
        'failed',
        'rejected',
        'canceled'
    )),
    actor_type TEXT NOT NULL COLLATE BINARY CHECK (actor_type IN (
        'administrator',
        'unauthenticated',
        'local_operator',
        'system'
    )),
    administrator_id INTEGER CHECK (
        administrator_id IS NULL OR administrator_id > 0
    ),
    server_id INTEGER CHECK (server_id IS NULL OR server_id > 0),
    source_id INTEGER CHECK (source_id IS NULL OR source_id > 0),
    safe_metadata_json TEXT NOT NULL CHECK (
        json_valid(safe_metadata_json)
        AND json_type(safe_metadata_json) = 'object'
        AND length(CAST(safe_metadata_json AS BLOB)) <= 2048
    ),
    request_id TEXT COLLATE BINARY CHECK (
        request_id IS NULL
        OR length(CAST(request_id AS BLOB)) BETWEEN 1 AND 128
    )
) STRICT;

-- Audit list pages and cleanup both use chronological order. One index serves
-- newest-first reads and oldest-first bounded deletion. The table is capped at
-- 100,000 rows, so category/outcome filters do not justify more write/storage
-- cost before their measured UI queries exist.
CREATE INDEX security_audit_events_time_idx
ON security_audit_events(occurred_at_us DESC, id DESC);

CREATE TRIGGER security_audit_events_immutable_update
BEFORE UPDATE ON security_audit_events
BEGIN
    SELECT RAISE(ABORT, 'security audit events are immutable');
END;
