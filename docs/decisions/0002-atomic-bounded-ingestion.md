# ADR 0002: Atomic Bounded Ingestion and Acknowledgement

**Status:** Accepted  
**Date:** 2026-07-27

## Context

Fluent Bit retries ambiguous and retryable HTTP outcomes. Siftail must not report
success for data that is only parsed or queued, partially retain malformed requests,
or allow compressed input and concurrent requests to create unbounded memory use.

## Decision

Authenticate before expensive decoding where practical. Accept only the documented
JSON and NDJSON shapes, validate every record, and admit one complete `WriteBatch`
atomically. A request succeeds only after its SQLite transaction commits.

Bound compressed bytes, decompressed bytes, event count, event bytes, JSON depth,
attributes, concurrent decoders, aggregate decode-plus-queue events and bytes, and the
queue subset. Accounting transfers from decoder to queue and is released exactly once.
Queue saturation returns `503`; storage-full returns `507`. A disconnect after queueing
may leave an ambiguous outcome and the writer may still commit.

## Alternatives

- Acknowledge after parsing or queue admission lowers latency but can lose acknowledged
  events during failure.
- Partial batch acceptance complicates retry behavior and silently loses bad records.
- An internal disk spool duplicates Fluent Bit's bounded retry storage and expands the
  failure surface.
- Per-event goroutines or unbounded channels violate the resource model.

## Consequences

- Response latency includes queue wait and commit time.
- Producers can safely retry retryable or ambiguous outcomes, subject to at-least-once
  semantics.
- Large valid requests consume bounded resident capacity and may be rejected under
  load.
- Writer completion delivery must never block after client cancellation.
- Load, saturation, cancellation, atomicity, and payload-leak tests are release gates.

## Migration and compatibility impact

The HTTP status and format contract is public compatibility surface. Changes to
acknowledgement timing, atomicity, or retry semantics require a superseding ADR and
release notes. Limit defaults may evolve only with documented operational impact.
