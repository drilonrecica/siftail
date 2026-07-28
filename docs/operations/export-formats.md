# History export format and store contract

**Reviewed:** 2026-07-28
**Scope:** SFT-045 version-one streaming formats and limits

SFT-045 defines the internal streaming store and stable payload formats.
SFT-046 exposes the authenticated audited workflow. There is no public export
route until that workflow is complete.

## Scope and limits

Export uses the current typed History range and every source, container, level,
stream, literal contains/excludes, request ID, logger, HTTP, and error-type
filter. It does not export only loaded rows: cursor, page direction, and page
size are removed, and matching events are read in
`event_at_us DESC, id DESC` order.

Hard defaults are 100,000 events, 256 MiB encoded output, a 31-day range, and a
two-minute store operation. The query requests one extra row to detect count
overflow without `COUNT(*)`. The encoder buffers one bounded event/encoded row,
then synchronously writes it. A row limit, byte limit, cancellation, deadline,
SQLite failure, or writer failure makes the staging artifact invalid; the
workflow must delete it and refuse download rather than report a partial
success.

## Shared fields

Both formats carry these fields in the documented order/names:

| Group | Fields |
|---|---|
| Contract/identity | schema version where applicable, local event ID |
| Time | event time, receive time as UTC RFC3339Nano |
| Source | source ID, trusted Server ID/name, project/environment/application/service keys and labels, optional alias |
| Container | optional instance ID, container ID, and container name |
| Classification | stream, canonical level, optional original level |
| Payload | message text, standard padded-base64 raw payload, JSON attributes or `null` |
| Correlation | optional source event ID, logger, request ID, error type |
| HTTP | optional method, path, status, duration milliseconds |

The local event ID is not globally unique across restored databases. Attributes
remain a JSON object. Every optional field is explicit `null`. Raw payload is
base64 so arbitrary bytes round-trip without changing NDJSON type or text
framing.

## NDJSON schema 1

Each physical line is one JSON object and ends with `\n`. Every row contains
`"schema_version":1`. JSON string escaping preserves multiline, tab, control,
quote, backslash, and hostile HTML text as data. The encoder does not
HTML-escape `<`, `>`, or `&` because the artifact is not rendered as HTML.

Field names are:

```text
schema_version, id, event_at, received_at, source_id, server_id, server_name,
project_key, environment_key, application_key, service_key, project_label,
environment_label, application_label, service_label, source_alias,
container_instance_id, container_id, container_name, stream, level,
level_original, message, message_raw_base64, attributes, source_event_id,
logger, request_id, error_type, http_method, http_path, http_status, duration_ms
```

An empty result is an empty NDJSON artifact.

## Text schema 1

The artifact starts with:

```text
# siftail-text-v1
id	event_at	received_at	...
```

The second line contains the shared field names except `schema_version`, in the
same order as NDJSON. Each later physical line is one event. Fields are
tab-separated. Every string, including timestamps, enum values, message,
base64, and attributes, uses reversible `strconv.Quote`-compatible quoting;
control characters therefore appear as escapes. Numbers are unquoted decimal
values and absence is bare `null`.

An empty text result contains only the two header lines.

## Safe operation metadata

The store returns format, optional Server ID, absolute range, row/byte maxima,
emitted row/byte count, and one closed failure category. This is enough for the
mandatory SFT-046 security audit without copying source names, filter text,
attributes, raw payload, or messages into audit/process logs.

## Development-host measurement

The benchmark source has 10,000 events with 256-byte raw payloads, 52-character
messages, attributes, logger, and request ID. The encoded NDJSON artifact is
about 11.6 MiB:

```bash
go test -run '^$' -bench '^BenchmarkExport' \
  -benchtime=1s -count=3 -benchmem ./internal/logs
```

Three Fedora/i5 runs measured:

- time to the first NDJSON write attempt: 144.679 µs median
  (143.354–144.983 µs), 11,157–11,165 bytes/op, 217 allocations/op;
- full read-only export: 140.771 ms median, 82.51 MB/s median,
  52,704,716–52,714,941 bytes/op, 760,073–760,099 allocations/op; and
- export while a concurrent SQLite application-event write committed:
  141.847 ms median, 81.89 MB/s median,
  52,705,478–52,709,855 bytes/op, 760,082–760,094 allocations/op.

The concurrent case was about 0.76% slower by median latency. Allocated bytes
are cumulative allocation churn across all 10,000 encodes, not resident memory;
live encoder state remains bounded to one canonical event/row. Results are local
measurements, not guarantees for larger fields, filters, storage, encryption,
or competing workloads.
