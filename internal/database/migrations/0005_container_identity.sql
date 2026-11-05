-- siftail:foreign-keys-off
-- ADR 0006: a container instance is identified by its container ID; the name
-- is a mutable label. Producer metadata is never a uniqueness constraint, so
-- both UNIQUE constraints are dropped. SQLite cannot drop a constraint in
-- place, so the table is rebuilt. log_events references this table without a
-- cascade, so the rebuild runs with foreign keys disabled outside the
-- migration transaction and the runner executes PRAGMA foreign_key_check
-- before commit (SQLite's documented table-rebuild procedure). Every row is
-- copied with its id, so every log_events reference stays valid.
CREATE TABLE container_instances_0005 (
    id INTEGER PRIMARY KEY,
    source_id INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    container_id TEXT COLLATE BINARY
        CHECK (container_id IS NULL OR length(CAST(container_id AS BLOB)) BETWEEN 1 AND 255),
    container_name TEXT COLLATE BINARY
        CHECK (container_name IS NULL OR length(CAST(container_name AS BLOB)) BETWEEN 1 AND 255),
    first_seen_at_us INTEGER NOT NULL,
    last_seen_at_us INTEGER NOT NULL CHECK (last_seen_at_us >= first_seen_at_us),
    CHECK (container_id IS NOT NULL OR container_name IS NOT NULL),
    UNIQUE (id, source_id)
) STRICT;

INSERT INTO container_instances_0005(
    id, source_id, container_id, container_name, first_seen_at_us, last_seen_at_us
)
SELECT id, source_id, container_id, container_name, first_seen_at_us, last_seen_at_us
FROM container_instances;

DROP TABLE container_instances;

ALTER TABLE container_instances_0005 RENAME TO container_instances;

-- Writer resolution by container ID (one lookup per distinct container per
-- batch). Its leading source_id also serves per-source detail listing, the
-- per-server instance count, and source-scoped cleanup. The table is bounded
-- to 100,000 instances per server, so the write/storage cost is small, and
-- the index does not participate in event retention deletion.
CREATE INDEX container_instances_source_container_id_idx
ON container_instances(source_id, container_id);

-- Writer resolution for name-only events. Partial: rows that carry a container
-- ID are never matched by name, so they cost nothing here.
CREATE INDEX container_instances_source_name_idx
ON container_instances(source_id, container_name)
WHERE container_id IS NULL;
