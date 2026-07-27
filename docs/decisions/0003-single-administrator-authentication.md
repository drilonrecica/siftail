# ADR 0003: Single-Administrator Authentication

**Status:** Accepted  
**Date:** 2026-07-27

## Context

Siftail protects sensitive application logs but is intentionally not a multi-user or
enterprise identity system. It must work locally without an identity provider and must
not expose an unauthenticated setup workflow.

## Decision

Support exactly one local administrator. Hash passwords with Argon2id using stored
parameters, initially 32 MiB memory, three iterations, and parallelism one, with at
most two concurrent hash operations. Use opaque 32-byte browser session tokens, store
only SHA-256 token hashes, cap active sessions at 64, and enforce absolute and idle
expiry, revocation, secure cookies, CSRF tokens, and Origin validation.

Create or reset the administrator through the focused CLI. When the server is running,
administrative CLI mutations use an owner-only Unix control socket under `/data` so
the process write coordinator remains authoritative. Version one supports ordinary
reverse proxies but does not trust identity headers or forward-auth assertions.

## Alternatives

- JWTs complicate immediate revocation and add no value for one local administrator.
- An unauthenticated browser setup page expands the pre-authentication attack surface.
- Multiple users, roles, SSO, and trusted identity headers expand scope and require a
  different threat model.
- Plaintext command arguments expose passwords through shell history or process lists.

## Consequences

- Losing the administrator credential requires local CLI access.
- Password verification has a deliberate, bounded memory and CPU cost.
- Browser sessions can be revoked immediately and are excluded from every backup.
- Reverse proxies terminate TLS and may supply trusted routing metadata only from
  configured networks; they do not authenticate the administrator.

## Migration and compatibility impact

Authentication schema must retain hash parameters and session expiry/revocation data.
Restoring any backup invalidates sessions and requires a new sign-in. Adding trusted
identity authentication, multiple users, roles, or a public administration API
requires a superseding ADR and coordinated product/security changes.
