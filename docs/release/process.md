# Release process and channels

**Scope:** SFT-057. How a Siftail image goes from a reviewed commit to
`ghcr.io/drilonrecica/siftail`, and how anyone can check what was published.
The automation is [`.github/workflows/release.yml`](../../.github/workflows/release.yml);
the scripts it runs are in [`scripts/`](../../scripts/) so the same steps run on
a workstation.

Nothing in this document publishes anything by itself. Tagging, approving, and
publishing a release are always the maintainer's own actions.

## Channels

| Channel | Image tags | Comes from | Who publishes |
|---|---|---|---|
| **edge** | `edge`, `edge-<12-hex commit>` | every push to `master` whose CI run succeeded | the workflow, automatically after CI |
| **candidate** | `X.Y.Z-rc.N` only | a maintainer-pushed tag `vX.Y.Z-rc.N` on a commit already on `master` | the maintainer: tag, then approve the `release` environment, then publish the draft pre-release |
| **stable** | `X.Y.Z`, `X.Y`, from 1.0 also `X`, and `latest` when it is the highest stable version | a maintainer-pushed tag `vX.Y.Z` on a commit already on `master` | the maintainer: tag, then approve the `release` environment, then publish the draft release |

Rules the workflow enforces (`scripts/release-plan.sh`, tested by
`make release-plan-test` in CI):

- `latest` is only ever a stable release and only the highest stable version;
  a patch to an older line never moves it. Candidates and edge never touch
  `latest` or any `X.Y` tag (`AGENTS.md` §29).
- Candidate and stable commits must already be on `origin/master`, so nothing
  from an unreviewed branch can become a release. A tag must be exactly
  `vX.Y.Z` or `vX.Y.Z-rc.N`; anything else fails the run.
- Candidate and stable releases need `docs/release/notes/X.Y.Z.md` at the
  tagged commit. A candidate uses the notes of the version it previews.
- Edge versions are `edge-<commit>`, so `siftail version` and Status never show
  a release number for a development image. Edge is for testing only; the
  deployment guides tell operators never to track `edge` or `latest`.
- Before `1.0` a minor release may break compatibility (`PRODUCT.md` §18.4), so
  no bare `0` tag is published. The `X` tag starts with `1.0.0`.
- Before `1.0` only the latest stable release is supported (`PRODUCT.md` §18.5).

## What one run does

The same jobs run for every channel; only **publish** and **draft release**
depend on it.

1. **Plan**: classify the tag, check it is on `master`, and compute the
   version and image tags.
2. **CI**: the complete `ci.yml` (format, vet, tests, build, Compose
   validation, release-plan rules, race tests, browser suite, `go mod verify`,
   `govulncheck`). Edge skips this because it starts only after CI passed for
   the same commit.
3. **Build** (`scripts/release-build.sh`): builds `linux/amd64` and
   `linux/arm64` from `git archive` of the commit with a digest-pinned BuildKit,
   writes one OCI image archive, a CycloneDX SBOM per platform, the release
   metadata, and `SHA256SUMS`.
4. **Inspect** (`scripts/release-inspect.sh`): the archive holds exactly the two
   platforms, and each image has user `65532:65532`, entrypoint `/siftail`,
   command `serve`, ports `8080`/`8081`, volume `/data`, the readiness health
   check, the version and revision labels, and no secret-looking environment.
5. **Scan**: Trivy (pinned) scans both platform images for fixable HIGH and
   CRITICAL vulnerabilities and for embedded secrets; a finding fails the run.
6. **Smoke**: `scripts/container-smoke.sh` runs each platform natively, amd64
   on an x86-64 runner and arm64 on an Arm runner: fresh database, an
   administrator, a Server and token, ingestion, sign-in, History, graceful
   shutdown with queued writes, restart, unwritable and owner-mapped volumes,
   and the exported filesystem (no shell, package manager, compiler, Node, or
   sources).
7. **Reproduce**: a second runner rebuilds the same commit from scratch and
   must produce the same image index digest, platform manifests, and binary
   checksums.
8. **Publish** (not in a dry run): copies the exact tested archive to GHCR for
   each tag with `skopeo copy --all --preserve-digests`, checks that the
   registry digest equals the recorded one, and adds GitHub build provenance
   for the image and the release files plus an SBOM attestation per platform.
9. **Draft release** (candidate and stable): creates a *draft* GitHub release
   from the notes file with the artifacts attached. A draft is not visible to
   users until the maintainer publishes it.

## Maintainer steps

One-time repository settings (owner-only; the workflow cannot do this):

- Create the `release` environment with the maintainer as required reviewer
  and deployment tags limited to `v*`. Without that protection rule a
  candidate or stable image is published as soon as the tag is pushed, without
  a second approval.
- After the first push, check that the `siftail` package on GHCR is linked to
  the repository and public.
- Enable **Private vulnerability reporting** (Settings → Security), which
  [`SECURITY.md`](../../SECURITY.md) tells reporters to use.

Cutting a candidate, then a stable release:

1. Merge everything to `master` and wait for green CI. Write or update
   `docs/release/notes/X.Y.Z.md` (removing its draft status line) and
   [`known-issues.md`](known-issues.md).
2. Optional dry run: **Actions → Release → Run workflow** with the version, or
   `make release-dry-run RELEASE_VERSION=X.Y.Z-rc.N` locally (below).
3. `git tag -a vX.Y.Z-rc.1 -m "Siftail X.Y.Z-rc.1" && git push origin vX.Y.Z-rc.1`.
4. Review the run, approve the `release` environment, then review and publish
   the draft pre-release.
5. When the candidate has been dogfooded, tag `vX.Y.Z` on the same or a later
   `master` commit and repeat steps 3–4.

A failed run publishes nothing after the failing job. If a run fails after
publishing (for example while drafting the release), rerun the failed jobs;
the push is idempotent because the digest is fixed. Never move or reuse a
published version tag: fix forward with a new patch version or candidate.

## Dry run on a workstation

```bash
# Once: a builder with the same pinned BuildKit the workflow uses.
docker buildx create --name siftail-release --driver docker-container \
  --driver-opt image=docker.io/moby/buildkit:v0.35.1@sha256:baf6138eb8c37644800c7c913fba051e840ebb9ba13eaf92d92ef9a1712d0b4c

# Build, SBOM, metadata, checksums, and the image-contract check.
TMPDIR=$PWD/.bench-tmp RELEASE_BUILDER=siftail-release \
  make release-dry-run RELEASE_VERSION=0.5.0-rc.1

# Native smoke of the amd64 image (Podman).
skopeo copy --override-arch amd64 \
  oci-archive:.bench-tmp/release-0.5.0-rc.1/siftail-0.5.0-rc.1.oci.tar \
  docker-archive:.bench-tmp/siftail-amd64.tar:localhost/siftail:release-amd64
podman load --input .bench-tmp/siftail-amd64.tar
scripts/container-smoke.sh localhost/siftail:release-amd64 amd64
```

The build uses only committed files. It needs Docker with Buildx, `skopeo`,
`jq`, and `syft`. Load the image through `docker-archive` as above: Podman
ignores the health check in an OCI image configuration, so loading the OCI
archive directly would make the smoke's health step fail even though the
archive carries it.

## Checking a published release

Every check below works without trusting the maintainer's workstation.

```bash
# Provenance: built by this repository's release workflow from the tagged commit.
gh attestation verify oci://ghcr.io/drilonrecica/siftail:0.5.0 --repo drilonrecica/siftail

# The tag resolves to the digest in the release notes and in the metadata file.
skopeo inspect --raw docker://ghcr.io/drilonrecica/siftail:0.5.0 | sha256sum

# Release files match their checksums, and the checksums file is attested.
sha256sum --check SHA256SUMS
gh attestation verify siftail-0.5.0.oci.tar --repo drilonrecica/siftail

# Pin the digest in deployments.
COMPOSE_SIFTAIL_IMAGE=ghcr.io/drilonrecica/siftail:0.5.0@sha256:<index digest>
```

The release files are:

| File | Contents |
|---|---|
| `siftail-X.Y.Z.oci.tar` | The published multi-architecture image, byte for byte. |
| `siftail-X.Y.Z-linux-amd64.cdx.json`, `…-arm64.cdx.json` | CycloneDX SBOM of each platform image (Syft): Debian runtime packages, Go modules, and the Go toolchain. |
| `siftail-X.Y.Z.release.json` | Commit, `SOURCE_DATE_EPOCH`, image index digest, per-platform manifest, config and `/siftail` SHA-256, base-image digests, Buildx and BuildKit versions. |
| `SHA256SUMS` | SHA-256 of the files above. |

### Reproducing a build

The build input is the commit, the digest-pinned base images in the
[`Dockerfile`](../../Dockerfile), the pinned BuildKit, and the commit time as
`SOURCE_DATE_EPOCH` (it also becomes the image creation time and the
`BuildDate` that `siftail version` shows). With those, an independent rebuild
gives the same digest:

```bash
docker buildx create --name siftail-release --driver docker-container \
  --driver-opt image=docker.io/moby/buildkit:v0.35.1@sha256:baf6138eb8c37644800c7c913fba051e840ebb9ba13eaf92d92ef9a1712d0b4c
git checkout vX.Y.Z
RELEASE_BUILDER=siftail-release scripts/release-build.sh X.Y.Z vX.Y.Z /tmp/rebuild
jq .index_digest /tmp/rebuild/siftail-X.Y.Z.release.json
```

The arm64 cross-compiler packages are installed from Debian at pinned
versions; if Debian withdraws one, the rebuild fails rather than silently
changing. SBOMs carry a generation time and serial number, so their checksums
differ between runs; compare the image digest and the binary checksums
instead.

## Image contents

The image holds `/siftail`, an empty owner-writable `/data`, `/LICENSE`, and
the Distroless `base-nossl-debian13` runtime (glibc, CA certificates, timezone data,
Debian package copyright files). It has no shell, package manager, compiler,
Node.js, tests, or sources. Details, sizes, and the license of every runtime
package family are in
[`docs/operations/container-image.md`](../operations/container-image.md); the
dependency and license inventory is [`dependencies.md`](dependencies.md); each
release's SBOMs list every package with its version.

## Vulnerabilities and security fixes

The reporting process and supported versions are in
[`SECURITY.md`](../../SECURITY.md). A security fix ships as a new patch release
through this same workflow; the release notes' **Security** section names it.
`govulncheck` runs on every push, and the weekly
[`supply-chain.yml`](../../.github/workflows/supply-chain.yml) rescans a fresh
image build, so a newly published base-image vulnerability surfaces even
without a code change. Base-image and Action updates arrive as reviewed
Renovate pull requests; the BuildKit image pin in `release.yml` is refreshed
by hand at each release.
