# SQLite upgrade fixtures

These databases are immutable, synthetic historical snapshots for Siftail
schema versions 1 through 4. They contain no production credentials or
application payloads. Names, tokens, password/session material, addresses, and
events are deliberately recognizable test values.

`generate.go` records each released migration only up to the fixture's schema,
then inserts representative settings, trusted Server/token state, source and
container identity, and an immutable event. Later schemas additionally carry
the administrator, session, and security-audit state introduced by that
version. Fixed timestamps and deterministic synthetic hashes make provenance
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
```

After adding a migration, update the generator's ordered name list and current
version, then generate only the new fixture from the repository root:

```bash
go run ./internal/database/testdata/upgrades/generate.go -version 5
```

An intentional correction to one existing fixture is explicit:

```bash
go run ./internal/database/testdata/upgrades/generate.go -version 2 -overwrite
```

The generator refuses replacement without `-overwrite`.
