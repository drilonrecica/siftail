# Ingestion-token lifecycle and exposure boundary

**Reviewed:** 2026-07-28
**Scope:** SFT-031 browser and CLI Server/token administration

## Identity and storage

An ingestion token is generated from 32 bytes of `crypto/rand` entropy and
encoded with the `sft_` prefix plus URL-safe base64. Its plaintext selects
exactly one trusted Server; payload fields cannot select or impersonate another
Server.

SQLite stores only:

- SHA-256 token hash;
- 12-character nonsecret fingerprint;
- operator-chosen name and Server ID;
- creation, last-use, and revocation timestamps.

The plaintext is not stored in SQLite, process logs, URLs, diagnostics, or
reusable browser state. Database backups remain sensitive because token hashes,
administrator hashes, metadata, and application logs still require protection.

## One-time browser display

The protected creation POST returns one `Cache-Control: no-store` HTML response
containing the plaintext in a masked read-only input. A local script provides
show/hide and clipboard actions, immediately replaces the POST document's
history URL with the secret-free Server URL, and clears the input on
`pagehide`. The Done action returns to ordinary Server detail, which can show
only the fingerprint and metadata. Back, reload, and later GET responses cannot
retrieve the plaintext from Siftail.

JavaScript-disabled clients still receive the one response and an explicit Done
link. Browser developer tools, clipboard history, screenshots, extensions, and
the operator's destination configuration are outside Siftail's process
boundary, so the operator must treat the one-time page as a credential display.

## Rotation and revocation

Creating a replacement token leaves existing tokens active. This permits the
operator to update and test a sender before revoking the old token. Token names
are unique within a Server and every token remains independently revocable.

Revocation updates the selected token through the single database coordinator.
New requests reject revoked tokens immediately. The writer also rechecks token
ID, Server ownership, and active state while committing the admitted batch; a
revocation that wins that coordinator order prevents the batch from committing.
Another Server's token is never affected.

`last_used_at_us` is updated in the same durable transaction as a successfully
committed ingestion batch, using the batch's receive time. Authentication does
not add a separate write transaction and a rejected or rolled-back request does
not claim successful use.

## Browser mutation protections

Server creation, token creation/rotation, and revocation require:

- a valid single-administrator server-side session;
- same-origin validation;
- a valid CSRF token;
- URL-encoded form content within the shared 64 KiB limit;
- exact known fields with no duplicates;
- bounded UTF-8 names without control characters.

Revocation additionally requires typing the visible token name. Responses are
`no-store`; unexpected errors contain only generic text and a request ID.
The bounded audit store exists after migration `0004`, but Server/token action
wiring is assigned to the following audit integration task and is not claimed
by this pre-integration implementation.

## Verification and measured impact

Real-SQLite, HTTP, race, and production-browser tests cover generation,
hash-only storage, one-time display, navigation loss, overlap during rotation,
immediate revocation, Server binding, coordinator races, CSRF/origin failures,
unsafe form data, and secret-free process logs and errors.

The existing ingestion benchmark was rerun after placing last-use updates in
the batch transaction:

```bash
go test -run '^$' \
  -bench '^BenchmarkHTTP(CommitLatency|Sustained100EventBatch)$' \
  -benchtime=2s -benchmem ./internal/ingest
```

On the documented Fedora/i5 development host, single-event HTTP commit measured
323.778 µs/op with p95 449.956 µs, and 100-event batches measured 8,742
events/second. The earlier baseline was 304.820 µs/op and 8,736 events/second,
so the single-event change is about 6.2% and remains below the 15% review
threshold; sustained throughput is effectively unchanged. These are
development-machine measurements, not cross-host guarantees.
