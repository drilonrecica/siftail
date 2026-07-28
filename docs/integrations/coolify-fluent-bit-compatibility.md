# Coolify and Fluent Bit compatibility

**Evidence date:** 2026-07-28

**Scope:** SFT-035 pre-release fixtures; not a release-wide support promise

## Pinned compatibility

| Integration | Pinned upstream | What is tested |
|---|---|---|
| Coolify custom log drain | Coolify `v4.1.1` (`5a27427`) with its shipped `cr.fluentbit.io/fluent/fluent-bit:2.0` image reference; registry resolution observed as Fluent Bit `v2.0.14`, manifest `sha256:1ed1a965e60999098c92c57e579d7cd9e4d01c1549318f43bfc585462cc8bee0` | Coolify's Forward input, Docker Fluentd record fields, renamed hierarchy metadata, `json_lines`, gzip, bearer authentication, HTTPS, retry configuration, bounded filesystem buffering, and self-exclusion |
| Generic Fluent Bit HTTP | Fluent Bit `v5.0.9`, manifest `sha256:1f1858b955f8fc0d5ec8878ce995c6953c0727a81a0877f655cdbb2fd75f563c` | Forward-input representative records, JSON array and `json_lines` decoding, gzip, stable source-event IDs, bearer authentication, HTTPS, retry configuration, and bounded filesystem buffering |

The Coolify pin is intentionally the complete Coolify release plus the
Fluent Bit image reference that release ships. Coolify uses the floating `2.0`
image tag rather than an immutable patch tag or digest, so Siftail does not
claim a more exact bundled Fluent Bit version. Generic Fluent Bit support is
best effort and pinned only to the `v5.0.9` fixture. Other Coolify releases,
Fluent Bit releases, inputs, processors, and output formats are unverified.

Coolify `v4.1.1` does not emit a separate application-name field in this drain
contract. Its `coolify.app_name` value becomes Siftail's service and, through
the documented source fallback, application when no richer application field
exists. Project and environment values are used when Coolify attaches their
`COOLIFY_*` environment fields; absent values use Siftail's explicit fallbacks.
The fixture represents the configured hierarchy, not a promise that every
Coolify resource type populates every field automatically.

No Siftail runtime code checks upstream versions, calls an upstream API, or
downloads configuration. These are compile-time fixtures and tests.

## Upstream evidence

The evidence was re-read from official sources on the date above:

- [Coolify `v4.1.1` release](https://github.com/coollabsio/coolify/releases/tag/v4.1.1)
  was the latest Coolify release observed.
- [Coolify drain-log documentation](https://coolify.io/docs/knowledge-base/drain-logs)
  documents server/resource opt-in, custom Fluent Bit configuration,
  `COOLIFY_APP_NAME`, the emitted `coolify.app_name`, and the need to restart
  resources after drain or environment changes.
- Coolify `v4.1.1`
  [`StartLogDrain.php`](https://github.com/coollabsio/coolify/blob/v4.1.1/app/Actions/Server/StartLogDrain.php)
  launches `cr.fluentbit.io/fluent/fluent-bit:2.0`, accepts the custom
  configuration verbatim, and demonstrates the Coolify metadata renames.
- Coolify `v4.1.1`
  [`generate_fluentd_configuration`](https://github.com/coollabsio/coolify/blob/v4.1.1/bootstrap/helpers/shared.php)
  binds resources to `127.0.0.1:24224` and attaches the four documented
  `COOLIFY_*` environment fields to Docker's Fluentd records.
- [Docker's Fluentd logging-driver contract](https://docs.docker.com/engine/logging/drivers/fluentd/)
  defines `container_id`, `container_name`, `source`, and `log`, and explains
  the attached environment fields.
- [Fluent Bit HTTP output](https://docs.fluentbit.io/manual/pipeline/outputs/http)
  documents POST output, `json_lines`, gzip, headers, timestamp formatting,
  and TLS.
- [Fluent Bit buffering](https://docs.fluentbit.io/manual/data-pipeline/buffering)
  documents filesystem chunks and the per-output
  `storage.total_limit_size` bound.
- [Fluent Bit scheduling and retries](https://docs.fluentbit.io/manual/administration/scheduling-and-retries)
  documents `Retry_Limit False` as no scheduler retry-count limit.
- [Fluent Bit release history](https://fluentbit.io/announcements/) records
  `v5.0.9` on 2026-07-03.

## Fixtures

- [`coolify-v4.1.1-fluent-bit-2.0.conf`](fixtures/coolify-v4.1.1-fluent-bit-2.0.conf)
  is the pinned Coolify custom-drain shape.
- [`fluent-bit-v5.0.9.conf`](fixtures/fluent-bit-v5.0.9.conf) is the pinned
  generic Forward-to-HTTP shape.
- `internal/ingest/testdata/coolify-v4.1.1-json-lines.ndjson` records Docker
  Fluentd fields and Coolify's post-rename hierarchy.
- `internal/ingest/testdata/fluent-bit-v5.0.9-json-array.json` records the
  generic JSON-array variant with stable source event IDs.

The configuration files are compatibility fixtures, not ready-to-paste
operator output. Siftail generates deployment-specific Coolify and generic
Fluent Bit configuration in the one-time browser token workflow or through
`siftail token create --output coolify|generic`; the generated variants retain
the pinned fixture's bounded buffering, retry, authentication, and
self-exclusion requirements.

## Recursive-ingestion prevention

Siftail's own Coolify resource must set:

```text
COOLIFY_APP_NAME=siftail-self
```

The Coolify fixture applies this exact exclusion before the modify filter
renames `COOLIFY_APP_NAME`:

```ini
[FILTER]
    Name    grep
    Match   *
    Exclude COOLIFY_APP_NAME ^siftail-self$
```

The anchored value avoids suppressing unrelated applications that merely
contain `siftail` in their name. As defense in depth, leave **Drain Logs**
disabled on the Siftail resource itself. Restart the Siftail resource after
changing its environment or drain setting, as Coolify requires.

Do not consider a configuration ready if:

- the marker is absent or differs;
- the exclusion occurs after `COOLIFY_APP_NAME` is renamed;
- the supported Coolify release does not attach that field;
- a custom source bypasses the filter; or
- Siftail's resource still drains without the exact marker.

The tests fail if the exclusion disappears, loses its anchors, moves after the
rename, or begins matching prefix/suffix application names.

## Delivery and capacity semantics

The fixtures use:

- `Format json_lines`, an explicit `Content-Type application/x-ndjson`, and
  `Compress gzip`;
- `Retry_Limit False`, allowing Fluent Bit's scheduler to keep retrying a
  retryable chunk;
- filesystem storage at the input; and
- `storage.total_limit_size 256M` for the Siftail output.

The 256 MiB output queue is a bounded retry cushion, not a lossless-delivery
promise. Fluent Bit discards the oldest queued chunk when the output reaches
that bound. Docker's asynchronous Fluentd logging driver also has its own
bounded memory behavior before Fluent Bit accepts a record. Siftail never adds
an internal persistent overflow spool.

A Siftail `204 No Content` means the complete request committed. `503` and
`507` communicate retryable capacity/storage conditions. `400`, `401`, `403`,
`409`, `413`, `415`, and `429` require correcting authentication, payload,
identity conflict, format, or configured limits rather than assuming a later
retry will make the same request valid. A connection loss after submission is
ambiguous: a retry without `source_event_id` can legitimately create repeated
events; a byte-identical retry with a stable source event ID is an idempotent
no-op.

## Verification record

The repository tests exercise:

- both pinned payload fixtures through the production decoder, admission
  ledger, queue, controlled writer, and real migrated SQLite database;
- gzip and plain JSON variants;
- trusted Server binding from the token rather than payload metadata;
- Coolify project/environment/application/service hierarchy and Docker
  stdout/stderr mapping;
- committed acknowledgement, retryable saturation, ambiguous cancellation,
  stable-ID duplicate no-op, conflicting duplicate rollback, clean queue
  shutdown, and application restart behavior;
- exact self-exclusion ordering and match behavior; and
- required output, retry, TLS, compression, and finite-storage directives.

Commands and observed results are recorded in SFT-035's completion entry in
[`docs/tasks/0.3.0.md`](../tasks/0.3.0.md).

The development environment's Docker daemon was not accessible. Rootless
Podman nevertheless pulled both pinned upstream images and each actual
`fluent-bit` binary accepted its fixture with `--dry-run`; `v2.0.14` and
`v5.0.9` both reported `configuration test is successful`. A focused `v2.0.14`
runtime check of the same anchored grep rule emitted an ordinary `checkout`
record and suppressed the exact `siftail-self` record. An actual supported
Coolify host remains a later release-gate requirement.
