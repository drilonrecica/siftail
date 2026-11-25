# Dependency and license inventory

**Reviewed:** 2026-11-25 (SFT-057), for the 0.5.0 source tree. Each release's
SBOMs (see [`process.md`](process.md#checking-a-published-release)) are the
exact, per-image record; this page is the human-readable summary and must be
updated whenever `go.mod`, the vendored assets, or the base images change.

Siftail is licensed under the Apache License 2.0 ([`LICENSE`](../../LICENSE),
also at `/LICENSE` in the image).

## Linked into the binary

Generated with `go run github.com/google/go-licenses/v2@v2.0.1 report
./cmd/siftail` and checked against `go version -m` of a release build.

| Module | Version | License | Why |
|---|---|---|---|
| `github.com/mattn/go-sqlite3` (bundles SQLite 3.54.1) | v1.14.53 | MIT (SQLite: public domain) | The SQLite driver (ADR 0001). |
| `golang.org/x/crypto` (`argon2` only) | v0.59.0 | BSD-3-Clause | Argon2id password hashes. |
| `golang.org/x/sync` (`errgroup` only) | v0.25.0 | BSD-3-Clause | Owned component lifecycles. |
| `golang.org/x/term` | v0.48.0 | BSD-3-Clause | No-echo password prompt. |
| `golang.org/x/sys` | v0.50.0 | BSD-3-Clause | Required by `x/term`. |
| Go standard library and runtime | go1.27.4 | BSD-3-Clause | Toolchain from `go.mod`. |

`golang.org/x/net` and `golang.org/x/text` appear in `go list -m all` as
module-graph entries only; no package from them is linked. Why each linked
dependency exists, and its size cost, is in the README's
[dependency rationale](../../README.md#current-dependency-rationale).

## Embedded browser assets

Served same-origin from the binary; their license texts are embedded with them
in [`internal/web/ui/licenses/`](../../internal/web/ui/licenses/).

| Asset | Version | License |
|---|---|---|
| htmx (`htmx-2.0.11.min.js`, SHA-384 pinned by `internal/web/ui/ui_test.go`) | 2.0.11 | 0BSD |
| JetBrains Mono, 400 and 700, Latin and Latin Extended subsets (`.woff2`) | as vendored by ADR 0005 | SIL Open Font License 1.1 |

All other CSS, JavaScript, and SVG are Siftail's own.

## Runtime image

`gcr.io/distroless/base-nossl-debian13:nonroot`, pinned by index digest in the
[`Dockerfile`](../../Dockerfile). Distroless itself is Apache-2.0; its Debian 13
packages (`base-files`, `ca-certificates`, `libc6`, `media-types`, `netbase`,
`tzdata`, `tzdata-legacy`) keep their own licenses, whose copyright files remain in the image
under `/usr/share/doc`. See
[`container-image.md`](../operations/container-image.md#runtime-dependencies-licensing-and-security-maintenance).

## Build and development only

Never present in the image.

| Input | Pinned as | Use |
|---|---|---|
| `golang:1.27.4-bookworm` | index digest in the Dockerfile | Builder stage. |
| `gcc-aarch64-linux-gnu`, `libc6-dev-arm64-cross` (and the amd64 equivalents) | exact Debian versions in the Dockerfile | Cross-compiling the CGO binary. |
| `moby/buildkit` v0.35.1 | digest in `release.yml` | Reproducible release builds. |
| GitHub Actions | commit SHAs in `.github/workflows/` | CI and release automation. |
| Trivy v0.76.2, Syft (via `anchore/sbom-action`) | versions in the workflows | Scans and SBOMs. |
| `govulncheck` v1.10.0 | version in `ci.yml` | Reachable-vulnerability check. |
| `@playwright/test` 1.65.1, `@axe-core/playwright` 4.14.1 | exact versions in `package.json` and `package-lock.json` | Browser test suite. |

## Keeping it current

`AGENTS.md` §21 sets the policy: Renovate proposes grouped weekly updates for
Go modules, base-image digests, Actions, and npm packages, and nothing merges
without review; `govulncheck` runs on every push; the Go line must still
receive security fixes. The vendored htmx and fonts are reviewed per
milestone and replaced only by an explicit change with integrity tests.
