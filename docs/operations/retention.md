# Application-log retention and late events

**Reviewed:** 2026-11-05
**Scope:** SFT-091, ADR 0008 retention basis and late events; SFT-092
footprint measure

## What the limits mean

Siftail keeps application events under two global limits, configured together
on the Settings page:

- **Age:** 1 to 3,650 whole days, default 14.
- **Active database footprint:** 1 to 1,024 whole GiB, default 4 GiB.

Retention is measured from **receipt**. Siftail assigns each event's receive
time when the request arrives, and no producer can change it. The age limit
means "kept for at least N days after Siftail received it". An event's own
timestamp does not shorten or extend that period.

The retention worker runs at startup and then hourly. It deletes events
received before `now − age` in bounded transactions, ordered by
`(received_at_us, id)`. When the footprint reaches 95% of the size limit, it
deletes the earliest-received events, whatever their age, until the footprint
is back at 90%.

The footprint is the database's in-use pages plus its WAL and shared-memory
files. Pages freed by deletion count as reclaimed immediately: SQLite reuses
them before it grows the file. The database file itself shrinks more slowly:
each reclamation cycle (one after age deletion, one after each size-deletion
chunk) returns at most 8,192 pages, 32 MiB at the default page size, to the
filesystem. For a while after a large deletion the file on disk can therefore
be larger than the footprint. The Status page lists the file sizes, which
include those free pages.

A `204 No Content` from ingestion therefore guarantees that age retention will
not remove the event for the configured age. An accepted event can leave
earlier only in these ways, and none of them is silent:

- size retention, which you configure and which Status reports in its cleanup
  result;
- an explicit operator action, such as removing a source, Clear logs, or a
  restore.

## Late events

A *late event* is an event whose own time is already older than the age cutoff
when Siftail receives it. Two situations produce them:

- **A host clock behind real time** by more than the age limit.
- **A replayed backlog:** Fluent Bit flushing a buffer after a long outage, or
  an imported archive.

Siftail accepts late events (`204`), stores them, and retains them for the full
age after receipt, like any other event. It does not reject them. No status
code makes Fluent Bit drop a chunk: it retries indefinitely, so a rejection
would hold back every fresh record batched with the stale one.

History orders by event time, so a late event appears at its claimed time.
History's oldest visible event can therefore be older than the age limit, and
Status's "Oldest event" can be too. That is expected after late arrivals.

A host whose clock runs **ahead** gains nothing. Its events are still deleted
by receive time, so a year-2099 timestamp cannot evade retention.

### What Status shows

On the authenticated Status page, under **Ingestion**:

- **Late events kept:** the number of late events stored since the process
  started.
- **Late events by Server:** shown once any late event has arrived. It lists
  each Server's count and the time of its last late event, for the 20 Servers
  with the most recent late events, plus one line counting any Servers not
  shown. A removed Server appears as `Removed Server #<id>`.

The Diagnostics list receives at most one `ingestion · late_event` entry per
Server per hour. It is a fixed summary and carries no Server, source, or event
content. The per-Server attribution is in the counter.

The counters are process-local and reset when Siftail restarts. They are not
stored in the database.

### What to do about late events

1. Find the Server in "Late events by Server".
2. Check the host clock, for example with `timedatectl status` on systemd hosts.
   Make sure NTP synchronization is active.
3. If the clock is correct, the events are probably a delivery backlog. Check
   Fluent Bit's logs on that host for a long outage or a large buffered queue.
   The backlog is stored and searchable at its original times. No action is
   needed beyond understanding why delivery was delayed.
4. A host that keeps producing late events with a correct clock is replaying
   old data. Find what is re-sending it.

Late events never change readiness or health severity.

## Changing the limits

Lowering the age limit takes effect at the next retention run (startup or
within the hour). Events received before the new cutoff are then deleted.
Raising it keeps events that have not yet been deleted. Events that are
already gone stay gone.

The late-event check uses the age limit in force when each batch is written.
Changing the limit does not re-evaluate events that were already counted.

## Upgrading from schema 6

Before schema 7, retention used the earlier of event time and receive time.
After the upgrade, every stored event is retained by its recorded receive time.
No event becomes eligible for deletion sooner than before. Events whose time
preceded their receipt are kept longer. The one-time index build at upgrade is
described in
[`upgrade-compatibility.md`](upgrade-compatibility.md#schema-7-retention-by-receipt).

## Related

- ADR 0008: `docs/decisions/0008-retention-basis-and-stale-events.md`
- Ingestion contract: `README.md`, "Ingestion compatibility"
- Reclamation and writer-interference measurements:
  [`docs/performance/retention.md`](../performance/retention.md)
- Storage-full procedure: [`storage-full-recovery.md`](storage-full-recovery.md)
