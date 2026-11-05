# SQLite upgrade fixtures

These databases are immutable, synthetic historical snapshots for Siftail
schema versions 1 through 10. They contain no production credentials or
application payloads. Names, tokens, password/session material, addresses, and
events are deliberately recognizable test values.

`generate.go` records each released migration only up to the fixture's schema,
then inserts representative settings, trusted Server/token state, source and
container identity, and an immutable event. Later schemas additionally carry
the administrator, session, and security-audit state introduced by that
version. Schema 5 additionally carries a recreated container (the same name
with a new container ID), which schema 4's uniqueness constraints could not
hold. Schema 6 additionally carries a stable-ID event whose time was defaulted
to receive time (`event_at_defaulted = 1`, ADR 0007). Schema 7 additionally
carries a late event whose event time is 30 days before its receipt (ADR
0008). Schema 8 changes only an index, so it carries the same state as schema
7; every fixture's event 15 has a request ID that the rebuilt request-ID index
must cover. Schema 9 adds only the trigger-maintained security audit row
counter, seeded from the existing rows, so it carries the same state as
schema 8 with a counter of 1. Schema 10 adds only the trigger-maintained
counter of unauthenticated audit rows and its partial index (ADR 0010); the
fixture's one audit row is a system row, so it carries the same state as
schema 9 with an unauthenticated counter of 0. Fixed timestamps and deterministic synthetic hashes make provenance
reviewable.

The compatibility tests pin each database's SHA-256 digest. Do not regenerate
or edit a fixture when a new migration is added. Add only the new current
schema fixture. Replacing an existing fixture requires an explicit correction,
review of the historical migration contract, updated digest, and release note.

Pinned digests:

```text
schema-1.db  fa41df2de53d37597eedf87dba7e854539c0a61197f25f2d8f98d76e8a74d0a3
schema-2.db  3d12a740144c0c1a08dff3b8a629a9d79180f2c0a4ab2d5f5180e45f8dbce9d4
schema-3.db  feaef12f35a3a01256eebce642c6679960dc4219ddb1df9d1952143e0056320b
schema-4.db  5c9926b8c354e9622147acb0f10454e81db764068677776bd31fcddac92e4fe2
schema-5.db  3e051e348460e4ceb851028a93a998032d295a3865c770ac35c478ab7a840870
schema-6.db  d8c662571bb2fc726d93dae1493ae8a189e219d5e46b627fcb60b785762f43dc
schema-7.db  07354718efe238051c3025b24c44945cfa7a470a9b8d4b59927bcf69b0de7c75
schema-8.db  2675b8dc80d5add3ab8a3321db62c7c857673e931d59a646848b86006b3fe67a
schema-9.db  100cb9adde8912731e28f1ace2702c8b44bd8cbd86cc290cfd99d2460587230f
schema-10.db 1081960d2d8ed429c470ecbb8e1fb1a5507f82013c9aa2fa0f8d59e9d100d220
```

After adding a migration, update the generator's ordered name list and current
version, then generate only the new fixture from the repository root:

```bash
go run ./internal/database/testdata/upgrades/generate.go -version 11
```

An intentional correction to one existing fixture is explicit:

```bash
go run ./internal/database/testdata/upgrades/generate.go -version 2 -overwrite
```

The generator refuses replacement without `-overwrite`.
