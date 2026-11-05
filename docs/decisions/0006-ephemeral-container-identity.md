# ADR 0006: Ephemeral Container Identity

**Status:** Accepted

**Date:** 2026-11-05

**Amends:** migration `0001` container-instance constraints (no prior ADR)

## Context

`container_instances` carries `UNIQUE (source_id, container_id)` and
`UNIQUE (source_id, container_name)`, and the writer resolves an instance with
`container_id IS ? AND container_name IS ?`. Docker and Coolify reuse a
container name for a new container on every `docker compose up --force-recreate`
or redeploy. The new `(id, name)` pair finds no row, the INSERT violates the
name constraint, the whole batch rolls back with `503`, and Fluent Bit retries
the same chunk forever. Every source on that server is then blocked (audit
DAT-01, Critical).

The domain already treats container instances as ephemeral runtime metadata
that is not part of stable source identity (`DOMAIN.md` §§3.9, 5.6, 14), and
requires bounded cleanup of unreferenced instances. That cleanup was never
built, so the 100,000-per-server cap eventually rejects every new container
(DAT-14).

## Decision

1. **Identity is the container ID.** A container instance is identified by
   `(source_id, container_id)` when the producer supplies a container ID. The
   container name is a mutable label of that instance.
2. **Resolution rules.**
   - ID present: resolve by `(source_id, container_id)`. When the stored name
     differs from the supplied name (including absent ↔ present), update the
     label in the same transaction. When no row exists, insert one.
   - ID absent, name present: resolve by `(source_id, container_name)` among
     rows that have no container ID. Siftail never attaches an ID-less event
     to an instance that has an ID, because that would guess identity.
   - Both absent: the event has no container instance (unchanged).
3. **No uniqueness constraints on producer metadata.** Migration `0005`
   rebuilds `container_instances` without both UNIQUE constraints. It keeps
   `UNIQUE (id, source_id)` (the composite foreign-key target of
   `log_events`), the `source_id` foreign key, and the column checks. It adds
   non-unique lookup indexes on `(source_id, container_id)` and
   `(source_id, container_name) WHERE container_id IS NULL`. The single writer
   coordinator resolves before inserting, so duplicates cannot arise from
   Siftail itself. If a future defect does produce two rows for one ID,
   resolution picks the most recently seen row (`last_seen_at_us DESC, id
   DESC`). The defect then degrades presentation and does not block ingestion.
4. **Labels are current, not historical.** Events reference the instance row,
   so History shows an instance's current name. Container metadata is
   presentation context, not immutable event content (`DOMAIN.md` §5.6).
5. **Bounded pruning (`DOMAIN.md` §14).** A retention-cycle step, scheduled
   through the write coordinator in bounded chunks, deletes container
   instances that no retained event references:
   - first, unreferenced instances whose `last_seen_at_us` is older than the
     retention age cutoff, oldest first;
   - then, while a server holds more instances than its cap, its oldest
     unreferenced instances by `(last_seen_at_us, id)`.

   A referenced instance is never deleted. The `log_events` foreign key has no
   cascade, so referential integrity enforces this. The 100,000-instance
   per-server cap and its atomic rejection remain. After pruning, the cap is
   reachable only by 100,000 instances that still have retained events, which
   is the bound the cap exists to enforce.
6. **Cache key.** The writer's container cache is keyed by
   `(source_id, container_id)` when an ID is present and by
   `(source_id, container_name)` otherwise. A name change on a cached ID still
   updates the stored label. Cache invalidation after source removal is
   governed separately (SFT-088).

## Alternatives

- **Keep the constraints and update the name on conflict (upsert).** This
  fixes recreate but still lets an unrelated metadata quirk (a constant or
  truncated ID, a name reused across replicas) reject whole batches through a
  constraint the producer never promised to satisfy.
- **Keep `UNIQUE (source_id, container_id)` and drop only the name
  constraint.** Under one writer this protects nothing the resolution rule
  does not already guarantee. Changing an identity rule later (for example,
  normalizing short and full Docker IDs) would need another full table
  rebuild, because SQLite cannot drop a constraint in place.
- **Identity by name.** A name is reused across recreations, so it would merge
  distinct runtime instances and is exactly the failure mode in DAT-01.
- **Insert a fresh instance row for every distinct `(id, name)` pair.** A
  rename then fragments one container into several instances and grows the
  table toward the cap faster.
- **Never prune; raise the cap.** This only postpones the `403` and keeps the
  per-new-container count growing linearly.

## Consequences

- A recreated, renamed, removed, or re-added container never blocks ingestion.
- Container labels in History reflect the latest observed name of the
  instance.
- Retention gains one more bounded cleanup step. Its chunk size and writer
  hold time follow the same measurement discipline as event deletion.
- Canonical deduplication must compare container identity by container ID when
  one is present, not by the mutable label (ADR 0007).
- Regression tests cover: same name with a new ID, same ID with a new name,
  removal then re-ingestion, name-only events, and pruning under the cap.

## Migration and compatibility impact

Migration `0005` is forward-only. It rebuilds `container_instances` in one
migration transaction (create new table, copy rows preserving `id`, drop, and
rename), so every `log_events.container_instance_id` reference remains valid.
An upgrade fixture from schema 4 is required. Existing rows are preserved
unchanged. The old constraints guarantee that no two rows in a source share an
ID or a name, so the copied rows already satisfy the new resolution rules and
no historical instances are merged. Older binaries refuse the
new schema as usual. The table is a non-public contract (`ARCHITECTURE.md`
§31.2). Implemented by SFT-087.
