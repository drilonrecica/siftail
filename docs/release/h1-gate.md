# H1 gate: data integrity, ingestion correctness, performance

**Completed:** 2026-11-05
**Scope:** SFT-087 through SFT-100 (H1), reviewed by SFT-101
**Release action:** None. No tag, image, or publication.

This is the self-check gate for phase H1. It records an adversarial review of
every H1 change against `DOMAIN.md` and ADRs 0006–0009, and the verification
runs the task required. Every figure below was measured in this session on the
tree at `c4d44b1` plus this task's working changes; nothing is carried over
from the implementing tasks' own evidence.

## Verdict

**Pass. No blocker.** The review found one Medium and two Low normalizer
defects, all pre-dating or widened by H1, and fixed them in this task with
table-driven tests. No H1 change violates a `DOMAIN.md` invariant. The race
suite, every upgrade fixture, the backup/restore drill on a production-shaped
database, and the durability smoke test hold. Three Low observations are left
for the owner or the backlog (findings 4–6).

## Environment

Fedora Linux 44, Linux 7.2.10 x86-64, four logical CPUs, Go 1.27.3,
go-sqlite3 v1.14.52 (SQLite 3.53.4). The repository, `/var/tmp`, and the
drill directory are on btrfs (`/dev/sda3`, Crucial BX500 SATA SSD); `/tmp` is
tmpfs. Every durability, backup, and restore run below used a btrfs path.

## Results

Go commands ran with `GOFLAGS=-count=1` so no result came from the test cache.
The final `make check` and `make race-test` ran after the fixes below, on the
tree as left for igris.

| Command | Result | Duration |
|---|---|---|
| `make check` (fmt-check, vet, test, build, compose-check) | pass, 16 packages ok | 18 s |
| `make race-test` | pass, 16 packages ok, no races | 29 s |
| Upgrade fixtures: `go test -v -run 'Upgrade\|Fixture\|Migration\|SchemaToo\|Immutable' ./internal/database ./internal/backup ./internal/ingest` | pass, 18 tests (56 with subtests): every historical fixture schema 1–9 through the production critical paths, full and configuration backup/restore of every fixture, released-migration and fixture digests immutable, newer-schema refusal, failed-next-migration rollback | 1 s |
| Durability smoke: `TMPDIR=/var/tmp go test -count=3 -run TestDurableIngestionSubprocessSmoke ./cmd/siftail` | pass 3/3 (1.53–1.62 s each) on btrfs | 6 s |
| Backup/restore drill (below), final run on the fixed tree | pass, 89 of 89 checks | 96 s |
| `govulncheck ./...` | 0 vulnerabilities affecting the code; 1 in a required module the code does not call (unchanged from the H0 gate) | 7 s |
| `git diff --check` | clean | n/a |
| `BenchmarkHTTPCommitLatency`, `-benchtime=1000x -count=20`, btrfs `TMPDIR` | 20,000 sequential requests, all 204; p50 2.6–4.1 ms, p95 4.9–6.7 ms, p99 10.5–22.3 ms | 84 s |

## Adversarial review

Method: the gate session read in full the write path (`internal/ingest/http.go`,
`admission.go`, `worker.go`, `writer.go`, `internal/database/coordinator.go`,
`database.go`), migrations 0005–0009, the CLI offline path
(`cmd/siftail/admin.go`, `database.go`, `internal/database/maintenance.go`),
and the application shutdown sequence. Two independent reviewers, given only
the specification sections and the files (not the implementing tasks'
conclusions), covered retention, snapshot backup, and restore
(`internal/retention`, `internal/database/snapshot.go`, `internal/backup`) and
History, normalization, export, and Status (`internal/logs`,
`internal/auth/history*.go`, `internal/status`, `internal/ingest/decoder.go`).
Each was asked for concrete failure scenarios with file and line, and for the
invariants they confirmed hold. Every finding below was re-read in the code by
the gate session before it was recorded; the three fixed ones were reproduced
by a failing test first.

### Invariants checked and holding

Ingestion and group commit (ADR 0009; `DOMAIN.md` §6 invariants 9–11, §29):

- One `BEGIN IMMEDIATE` per group on a pinned writer connection
  (`coordinator.go` `executeImmediate`); one `SAVEPOINT` per batch
  (`writer.go` `persistSavepoint`). A batch-scoped failure (forbidden,
  constraint, internal) does `ROLLBACK TO` + `RELEASE` and becomes that
  batch's outcome; busy, I/O, corrupt, full, a failed savepoint statement, or
  a closed coordinator fails the whole group (`isTransactionLevel`).
- Nothing is acknowledged before `COMMIT` returns: the worker calls
  `batch.Complete` only after `PersistGroup` returns, and `PersistGroup`
  fills caches and publishes to Live only after `DoImmediate` returned nil,
  from successful batches only, in queue (ID) order.
- Deduplication, quotas, and the source and container caps see earlier
  batches of the same group, because they read the same transaction.
- Trusted Server identity: every event's `Source.ServerID` must equal the
  token's Server or the batch is forbidden; the token row is re-checked inside
  the transaction (`revoked_at_us IS NULL`, exactly one row affected), so a
  revocation committed before the group takes effect for it.
- Admission accounting is released exactly once (`WriteBatch.Complete` under
  `sync.Once`); the decoder answers an empty batch with 400 before any lease
  is queued, so the `Enqueue` empty-batch guard is unreachable.
- Error scope (SFT-089): only `Unavailable` and `StorageFull` degrade
  globally (`worker.go` `observe`); a request timeout writes 503 explicitly
  while a client cancellation writes nothing (`http.go`); a token-lookup
  database error is 503, a definite mismatch 401. `CategoryInternal` adds a
  Status diagnostic and never touches readiness or the database category
  (`status/state.go`).
- Coordinator: an admitted operation always receives its real result
  (`submit` waits on the result channel after admission); `Run` calls `Close`
  before draining on both shutdown paths; a connection left inside a
  transaction after a failed `ROLLBACK` is discarded, never returned to the
  pool.
- Shutdown (`internal/app/app.go`): admission closes, the queue closes, the
  worker drains every queued batch through the same grouping, and only after
  `SIFTAIL_SHUTDOWN_TIMEOUT` is the writer cancelled; the coordinator closes
  after the writer and a passive checkpoint ends the run.

Deduplication and late events (ADRs 0007, 0008):

- `EventAtDefaulted` is set only for an absent or `null` timestamp; a supplied
  value must parse or the request is rejected. `CanonicalEqual` compares event
  time only when neither side defaulted it, containers by ID when either side
  has one and by name only when neither does; the stored side is rebuilt from
  every compared column (`writer.go` `loadCanonicalEvent`), so identical
  retries are no-ops and the first content for an ID wins in the database or
  earlier in the same transaction. The drill below exercised both outcomes.
- Late detection pre-filters with the one-day minimum and reads the age
  policy inside the transaction with the same `AddDate` and strict `<` as the
  cleaner, so the counter and retention agree at the boundary.

Retention and container pruning (ADR 0006, `DOMAIN.md` §14, §16):

- Age and size deletion both use `received_at_us < ?` and `ORDER BY
  received_at_us, id` through `log_events_received_idx`; only `log_events` and
  `container_instances` are ever deleted by the cleaner.
- Pruning deletes an instance only when `NOT EXISTS` a referencing event,
  inside the same coordinator transaction; the writer and the cleaner share
  the one coordinator worker and one writer connection, so no interleaving is
  possible, and cached instance IDs are re-verified against their full
  identity, so rowid reuse after a prune cannot misattribute an event.
- Footprint is `(page_count − freelist_count) × page_size` plus WAL and SHM
  from one statement; the size loop stops at target, at zero deletions, or on
  a busy checkpoint, and each iteration deletes at least one row. Incremental
  vacuum runs at most 1,024 pages per writer turn and 8,192 per cycle, exits
  on an empty freelist, and rejects a zero-page request.

Backup and restore (ADR 0009 items 8–13, `DOMAIN.md` §26):

- The staging file is created `O_EXCL` mode 0600 with a random suffix and
  re-checked with `Lstat` as a regular empty file; `VACUUM INTO` runs on a
  dedicated read connection and only reads the source; the live main file is
  never byte-copied. The snapshot is committed under `synchronous=FULL`,
  synced, verified, and hard-linked without replacing an existing path.
- Restore holds the maintenance lock from before verification to after
  installation; classification happens before any change and refuses
  non-regular paths in every mode; the `--replace-unhealthy` flag never
  bypasses confirmation, verification, the lock, the space preflight, or the
  audit record. Unhealthy files and stray sidecars are moved by same-directory
  rename into a fresh 0700 directory and never deleted; the managed
  `.rollback` is replaced only by a verified artifact.
- Verify reads every `integrity_check` row, requires exactly one completed
  format-1 metadata row, zero sessions, the audit counter table at schema 9,
  and empty history tables for a configuration artifact. The counter stays
  consistent after a configuration artifact empties the audit table, because
  the triggers disable SQLite's truncate optimization.

History and export (`DOMAIN.md` §7, §24):

- The cursor tightens the range only (`to = cursor + 1` when the cursor is
  inside the range; `from = max(from, cursor)`), with the existing tie-break
  predicate, so no row is skipped or repeated at equal timestamps and no
  int64 overflow is possible. The cursor is HMAC-bound to version, position,
  direction, and a fingerprint of the full query shape.
- Hierarchy subqueries carry `server_id = ?` whenever a Server is set and are
  fully parameterised; the `+` hint only steers the planner.
- Literal filters use `instr(lower(...), lower(?))`, so `%`, `_`, and `\`
  have no meaning and non-ASCII case is distinct.
- Export writes the success audit row before headers, delivers with a fresh
  per-chunk deadline and exact `Content-Length`, records `delivery_interrupted`
  on any delivery failure, and removes the staging file on every path. No
  filter text, payload, path, or secret reaches audit rows or process logs;
  the request logger records only the route pattern.

Schema (`AGENTS.md` §8.7): 0005 rebuilds `container_instances` without the
producer-metadata UNIQUEs, keeps `UNIQUE (id, source_id)` and the FK, copies
rows with their IDs, and runs under the `-- siftail:foreign-keys-off`
directive with `PRAGMA foreign_key_check` before commit. 0006 adds
`event_at_defaulted` and backfills only stable-ID rows with equal times, with
the immutability trigger dropped and recreated inside the transaction. 0007,
0008, and 0009 are index swaps and a trigger-maintained counter. The fixture
matrix (schema 1–9) and digest immutability tests pass.

CLI (DAT-09): a live control socket is used for mutations; otherwise the
maintenance lock is acquired before `database.Open` and released after
`db.Close`, a stale socket is reported and the command continues offline, and
a held lock yields the documented busy message.

### Findings

Fixed in this task (each reproduced by a failing test before the change, then
`go test ./internal/logs ./internal/ingest ./internal/auth ./cmd/siftail` and
the final `make check` and `make race-test` passed):

1. **Medium. Ill-typed common-field members were dropped from attributes.**
   `canonicalAttributes` skipped every key that `normalizeCommon` *could*
   consume, whether or not it did. `normalizeCommon` consumes only a string
   (`request_id`, `logger`, …), an integer (`http_status`/`status`), or a
   non-negative finite float (`duration_ms`), and the level extractor only a
   string. A record such as `{"message":"job finished","status":"completed",
   "request_id":12345,"duration_ms":-1,"level":30,"keep":"yes"}` therefore
   stored `{"keep":"yes"}` and lost `status`, `request_id`, `duration_ms`, and
   the numeric `level` from common fields, attributes, and (since SFT-099
   made the `message` text the raw payload) the raw payload too. A second
   alias of a consumed field (`trace_id` next to `request_id`) was dropped the
   same way. This violates `DOMAIN.md` §6 invariant 8 and §11. Fix
   (`internal/logs/normalize.go`): attributes keep every member the
   normalizers did not consume; consumed members are still removed, because
   every consuming helper deletes its key. `isCommonField` is gone. Test:
   `TestUnconsumedCommonFieldsStayInAttributes` (four cases; three failed
   before the fix). The pinned compatibility fixtures are unchanged, so no
   shipped fixture carried an ill-typed common field.
2. **Low. A `null` source timestamp skipped the Fluent Bit record timestamp.**
   `normalizeTimestamp` took the first present `timestamp`/`@timestamp`/`time`
   member even when it was `null`, so `{"time":null,"date":…}` defaulted to
   the receive time and set `event_at_defaulted`, contradicting `DOMAIN.md`
   §5.2 precedence step 2 and weakening ADR 0007 equality for that event.
   Fix: a `null` member counts as absent and the next source is consulted.
   `DOMAIN.md` §5.2 states the rule. Test row "null source uses fluent date".
3. **Low. `log: null` shadowed a present `message`.** The payload loop took
   the first present key, so `{"log":null,"message":"real text"}` stored an
   empty payload. Fix: a `null` payload member counts as absent for the
   selection and stays in attributes; a record whose only payload member is
   `null` is itself the payload. `DOMAIN.md` §5.10 and `ARCHITECTURE.md`
   §12.4 state the rule. Test `TestNullPayloadMemberIsAbsent`.

For the owner (not changed in this task):

4. **Low. Container-instance prune target is 90 % of the cap, the documents
   say "while a server exceeds its cap".** `internal/retention/cleaner.go`
   prunes a Server's oldest unreferenced instances down to 90,000 of the
   100,000 cap. SFT-087 chose this deliberately and recorded it in its
   completion evidence, because the writer rejects any batch that would
   exceed the cap, so the literal rule could never fire and DAT-14 would
   recur. The implementation is sound and never deletes a referenced
   instance, but `DOMAIN.md` §14, ADR 0006 §5, and `ARCHITECTURE.md` §23.7
   still state the literal rule. Decision for the owner: amend the three
   documents to the 90 % headroom target (one sentence each), or change the
   target. The gate did not edit a domain rule on its own.
5. **Low, pre-existing, backlog. `log_events.id` is not monotonic after the
   table empties.** The column is `INTEGER PRIMARY KEY` without
   `AUTOINCREMENT`, and size retention may legitimately delete every row.
   The next insert then receives id 1, against `DOMAIN.md` §5.1
   "monotonically increasing". Cursors are HMAC-bound to position and query
   shape, so no undefined traversal results, but a kept History page or Live
   position can land on new rows. Not introduced by H1; a schema change needs
   its own task and migration.
6. **Low, pre-existing, backlog. `direction=newer` without a cursor.**
   `ParseHistoryQuery` accepts it, and the store then reports `HasMore` with
   a `NextCursor` that points away from the unread rows. The UI never emits
   that URL (it offers `NextURL` only for older pages), so it needs a
   hand-crafted link, and no wrong rows are returned.

Reviewed and closed:

7. **The SFT-100 `BenchmarkHTTPCommitLatency` 503 did not reproduce**: 20
   runs of 1,000 sequential requests on btrfs all returned 204 (table above).
   For a sequential single client in that fixture only two paths yield 503:
   the fixture's 5-second request timeout, which SFT-089 answers explicitly
   while the writer still commits (production default is 30 s), and a
   token-lookup database error. The fixture holds no other writer, so
   `BEGIN IMMEDIATE` cannot be busy, and the queue and resident ledgers are
   released before the handler reads the result. A multi-second fsync stall
   on the SATA SSD while the preceding group-commit benchmarks' dirty pages
   were still flushing is the plausible cause, and the behavior under it is
   the documented one. Test-only change in `internal/ingest/benchmark_test.go`:
   both HTTP benchmarks now attach an observer to the handler and the writer
   and report, on a non-204, the elapsed time and which side rejected, or that
   neither did (which means the request timeout fired). The next occurrence
   is attributable; no product code changed.
8. **`BenchmarkLiveSSEFrame` 2.28 → 3.19 µs** against the 2026-07-28 record
   (noted by SFT-100): Live was not changed in H1, and the 2026-07-28 figure
   came from Go 1.25 on an older kernel. Not a gate concern; the note in
   `docs/performance/live.md` stands.
9. **Harness notes.** The ingestion listener serves only `/api/v1/ingest`
   (no health routes); `container-image.md` already points `HEALTHCHECK` at
   the UI listener. The drill script was corrected twice for its own errors
   (a readiness probe on the wrong listener, a load generator whose replay
   used a new wall clock, a query of the artifact-only metadata table that
   restore correctly removes); the product's behavior was identical in all
   three runs.

## Backup/restore drill

Scripted end to end with the built binary on `/var/tmp` (btrfs). Transcript
summary of the final run, after the fixes above:

**Shape.** Two trusted Servers, one token each. A stdlib load generator sent
1,200 NDJSON batches of 100 events (half gzip, four parallel senders, two
Servers), over 3 projects × 2 environments × 5 applications × 2 services with
two replicas each, container IDs recreated every 200 batches under the same
names, structured JSON payloads with `request_id`/`duration_ms`/`customer_id`,
multi-line stack traces, level-prefixed text, 10 % stable source event IDs,
12 late events (event time 30 days before receipt), and one stable ID reused
with different content on the same source.

**Measured (final run):**

| Step | Result |
|---|---|
| Ingestion | 1,200 batches, 120,000 events, 56.2 s (about 2,100 events/s through HTTP with four senders and the default group-commit bounds, run right after the race suite) (every batch 204) |
| Live database after ingestion | 119,999 events (120,000 sent − 1 conflicting duplicate dropped), ids contiguous 1…119,999, 120 sources, 1,441 container instances, 12 late events kept, 0 defaulted times, audit counter = audit rows, `foreign_key_check` empty, 50.3 MB on disk |
| `conflict-A` | stored once with the first content; `conflicting_duplicate` diagnostic present; `late_event` diagnostic present |
| Online full backup while ingesting (`siftail backup` through the control socket) | 0.51 s; artifact holds 22,400 rows = its max id (a contiguous prefix of commit order), verifies |
| Full backup after ingestion | 2.27 s; 119,999 rows, 0 sessions, 0 free pages, 48.6 MB (no free pages) |
| Configuration backup | 0 events, containers, audit rows; counter 0; 2 Servers, 2 tokens, 120 sources; 172 KB |
| `backup verify` × 3 | pass with the server active and again stopped |
| Stop on SIGTERM | clean exit, control socket removed, `database check --full` ok |
| Restore A: healthy current database | `current database: healthy`, `rollback: siftail.db.rollback`; rollback verifies as a session-free full artifact with 119,999 rows; restored database checks ok, 119,999 rows, no sessions, no artifact metadata table, `restore` audit row |
| Restore B: corrupted current database (4 pages overwritten) | `database check --full` fails without naming a path; restore without the flag refused naming `--replace-unhealthy`, files and rollback unchanged; with the flag: `current database: unhealthy`, `rollback: none`, files moved into a 0700 `siftail.db.unhealthy-<UTC>/` byte-identical to the corrupt file, rollback untouched, restored database checks ok |
| Restore C: missing current database | refused without the flag; with it `current database: missing`, `rollback: none`, rollback untouched, checks ok |
| Restore D: configuration artifact over a healthy database | rollback replaced by the pre-restore full copy; 0 events, 2 Servers, 2 tokens, 120 sources; server starts, both old tokens ingest 3 batches (204), exactly 300 events |
| Restore E: full artifact again, then replay | server starts; the stable-ID subset of batches 0–9 re-sent byte-identically: all 204, no row added, no `conflicting_duplicate` diagnostic; final `database check --full` ok |
| Privacy | 1,259 process-log lines at `info` contain no payload text, application field, application request ID, or token; CLI output of backup, verify, check, restore, and diagnostics contains no token |

Checks passed: 89 of 89. No check failed. The raw transcript, counts, and command outputs were kept in the session scratch directory and are not part of the repository.

## Cumulative verification commands

```bash
GOFLAGS=-count=1 make check
GOFLAGS=-count=1 make race-test
GOFLAGS=-count=1 go test -v -run 'Upgrade|Fixture|Migration|SchemaToo|Immutable' ./internal/database ./internal/backup ./internal/ingest
TMPDIR=/var/tmp go test -count=3 -v -run TestDurableIngestionSubprocessSmoke ./cmd/siftail
TMPDIR=$PWD/.bench-tmp go test -run '^$' -bench '^BenchmarkHTTPCommitLatency$' -benchtime=1000x -count=20 -benchmem ./internal/ingest
govulncheck ./...
git diff --check
# Drill: scripted `siftail serve`, `admin create`, `server create`, `token create`,
# load generator, `backup`, `backup --configuration-only`, `backup verify`,
# `database check --full`, `restore --confirm RESTORE` (healthy),
# `restore --confirm RESTORE --replace-unhealthy` (corrupted, then missing),
# configuration-only restore, replay; all on /var/tmp (btrfs).
```
