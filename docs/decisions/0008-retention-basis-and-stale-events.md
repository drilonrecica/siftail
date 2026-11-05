# ADR 0008: Retention Basis and Stale Events

**Status:** Accepted

**Date:** 2026-11-05

**Amends:** [ADR 0004](0004-canonical-events-sources-and-deduplication.md)
(retention order)

## Context

ADR 0004 and `DOMAIN.md` §16.2 derive the retention timestamp as
`min(event_at_us, received_at_us)`. Taking the minimum stops a future-skewed
clock from evading age retention. It also makes an event whose event time is
already older than the age limit expire the moment it is stored.

Audit DAT-10 records the result. A client with a clock behind by more than the
age limit, or a replayed backlog older than the limit (a Fluent Bit buffer
flushed after a long outage, or an imported archive), receives `204` for every
batch. The next retention run, at startup or within the hour, deletes those
events, and nothing tells the operator. Siftail acknowledges data it is about
to delete. The repository's own smoke test failed this way once its fixture
dates aged past 14 days.

The audit allowed three resolutions: flag stale events, reject them, or switch
the retention basis to received time.

## Decision

1. **Retention is measured from receipt.** The retention timestamp of an event
   is its `received_at_us`, which Siftail assigns and producers cannot
   influence. Age retention deletes events received before the cutoff. Size
   retention and every bounded deletion use the order
   `received_at_us ASC, id ASC`. An accepted event is therefore kept for at
   least the configured age after Siftail received it, unless size retention,
   an explicit purge, or a restore removes it first.
2. **Late events are counted, not rejected.** An event whose `event_at_us` is
   already older than the age cutoff when it is received is a *late event*. It
   is accepted and retained like any other event. A per-server late-event
   counter (cumulative since process start, with the time of the last
   occurrence) is shown on the Status page. At most one closed-category
   diagnostic entry per server per hour (`ARCHITECTURE.md` §27.5) reports
   that events older than the age limit are arriving, which indicates a clock
   or replay problem. The entry carries no event content or application
   field.
3. **History is unchanged.** History still orders by
   `event_at_us DESC, id DESC`, so a late event appears at its claimed time.
   A delayed backlog therefore stays searchable in the past for the full
   retention period after it arrives.

### The "acknowledged then silently purged" scenario

Under this decision, a `204` for an event guarantees that age retention will
not remove it for the configured age. The wrong-clock client and the replayed
backlog both keep their data for the full period, and the late-event counter
tells the operator their clocks or delivery are wrong. The only remaining ways
an acknowledged event can disappear early are size retention, which is
operator-configured and already reported by retention status, and explicit
operator actions. Neither is silent.

## Alternatives

- **Keep `min(event, receive)` and flag stale events at ingest.** Flagging
  makes the deletion visible but still deletes the data. A host with a skewed
  clock loses everything it sends, which is the worst outcome for a log tool
  on exactly the host that needs debugging.
- **Keep `min(event, receive)` and reject stale events at ingest.** No status
  code makes Fluent Bit drop a chunk: with `Retry_Limit False` it re-sends the
  chunk indefinitely (ADR 0007). Rejecting the whole request would block every
  co-batched fresh event behind one stale record, which is the DAT-01 failure
  class. Rejecting only the stale records would be the same silent loss as
  flagging, decided earlier.
- **Retain by `max(event, receive)`.** A future-skewed clock (year 2099) would
  evade age retention indefinitely.
- **Retain by event time with a grace period for late arrivals.** This needs a
  per-event value that depends on the policy in force at ingest, so changing
  the policy would leave old rows with stale meanings.

## Consequences

- An operator's age limit means "kept for N days after Siftail received it".
  For normally delivered events, receive time is within seconds of event time,
  so ordinary behavior is unchanged.
- Future-skewed clocks still cannot evade retention.
- Deletion order follows receipt, which closely tracks insertion order, so
  bounded deletion walks the table in near-rowid order.
- History's oldest visible event can be older than the age limit when late
  events were received. User-facing retention copy must say "received", not
  "older than".
- The smoke test and fixtures no longer depend on fixture dates relative to the
  wall clock for retention, though SFT-082's relative timestamps remain.
- Tests cover old delayed events (retained, counted), future-skewed events
  (expire by receive time), equal timestamps, and size-retention order.

## Migration and compatibility impact

A forward migration creates `log_events_received_idx ON log_events
(received_at_us, id)` and drops `log_events_retention_idx`. The stored
generated column `retention_at_us` cannot be redefined without rewriting every
row, so it remains in the schema unused and is documented as deprecated. A
later rebuild may remove it. Building the index reads the whole table once at
upgrade. Its duration, peak memory, and temporary disk use are measured on a
4 GiB real-disk fixture and stated in the release notes and
`docs/operations/upgrade-compatibility.md`, which recommends a verified backup
before upgrading. An upgrade fixture is required.

Events stored before the upgrade keep their recorded receive times, so the
new basis applies uniformly to old and new rows. Rows whose event time
preceded their receive time become retained longer than before. None become
eligible sooner.

Retention order is a compatibility contract under ADR 0004. This ADR is that
required amendment. Implemented by SFT-091; SFT-092's footprint and reclaim
changes are independent of the basis.
