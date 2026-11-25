# Security policy

## Reporting a vulnerability

Please report suspected vulnerabilities privately through GitHub: open the
repository's **Security** tab and choose **Report a vulnerability**. Do not
open a public issue, pull request, or discussion for a suspected
vulnerability.

Include the Siftail version (`siftail version` or the Status page), how it is
deployed, the steps to reproduce, and what an attacker gains. Never include
real log data, passwords, ingestion tokens, session cookies, or backups;
describe them or use made-up values.

Siftail is maintained by one person. You can expect an acknowledgement within
seven days. The maintainer will confirm or reject the report, agree on a
disclosure date with you, and credit you in the release notes unless you
prefer otherwise. Fixes ship as a new release through the normal
[release process](docs/release/process.md); the notes' **Security** section
describes the issue once the fix is available.

## Supported versions

Before 1.0, only the latest stable release receives fixes (`PRODUCT.md`
§18.5). Release candidates and `edge` images are for testing and receive no
separate fixes. Older releases are fixed by upgrading.

| Version | Supported |
|---|---|
| latest stable `0.x` | yes |
| older releases, candidates, `edge` | no |

## Scope

In scope: the Siftail binary and container image, its HTTP listeners,
authentication, sessions, ingestion tokens, CLI, backups and restore, and the
supported Compose and Coolify deployment files.

The security model assumes the host, container runtime, environment, and data
volume are trusted. Someone who controls any of them controls Siftail and its
data; that is not a vulnerability in Siftail. Also out of scope: missing
features that the product deliberately excludes (for example encryption at
rest, redaction, or multiple users), the reverse proxy's TLS configuration, and
findings that require a malicious administrator.

## What Siftail does and does not do

- No telemetry, analytics, crash reporting, update checks, or external assets;
  ordinary operation makes no outbound request.
- One administrator with an Argon2id password hash; opaque server-side
  sessions; CSRF tokens plus `Origin` checks; strict security headers; `__Host-`
  cookies and HSTS on an `https` public URL; throttled and audited sign-in.
- Ingestion tokens are stored only as hashes and decide which Server a batch
  belongs to; nothing in a payload can claim another Server.
- Logs are stored and shown as received: there is no redaction and no
  encryption at rest. Backups contain password and token hashes and, for a full
  backup, the retained logs.

The full threat review and its residual limitations are in
[`docs/security/0.5.0-hardening-review.md`](docs/security/0.5.0-hardening-review.md);
current limitations are in [`docs/release/known-issues.md`](docs/release/known-issues.md).

## Verifying what you run

Every published image has GitHub build provenance and SBOM attestations, and
every release has checksums and a reproducible-build record. The commands are
in [`docs/release/process.md`](docs/release/process.md#checking-a-published-release).
