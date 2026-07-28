# Production container image

Siftail's production artifact is one multi-architecture image for
`linux/amd64` and `linux/arm64`. It starts one Siftail Go process, exposes the
UI listener on `8080` and ingestion on `8081`, and declares `/data` as its only
persistent volume.

This document records the SFT-050 image contract and its verification on
2026-07-28. It does not declare a published release or image tag.

## Runtime contract

The final stage is the digest-pinned
`gcr.io/distroless/cc-debian12:nonroot` image. Siftail runs directly as numeric
user and group `65532:65532`; there is no shell or init wrapper between the
container runtime and Siftail. `SIGTERM` therefore reaches the Go process
directly and invokes its bounded graceful shutdown.

The image contains:

- `/siftail`;
- an owner-writable empty `/data`;
- `/LICENSE`;
- glibc and the dynamic loader required by the CGO SQLite binary;
- the system CA bundle; and
- the timezone database.

It does not contain a shell, `apt`, `dpkg`, a compiler, Go, Node.js, npm,
browser artifacts, tests, repository documentation, or source directories.
The exported-filesystem assertions in `scripts/container-smoke.sh` protect
this boundary.

The default command is `/siftail serve`. The image declares:

- volume `/data`;
- TCP ports `8080` and `8081`; and
- `HEALTHCHECK CMD ["/siftail", "healthcheck"]` with a 30-second interval,
  5-second timeout, 10-second start period, and three retries.

The health command parses the same bounded configuration as the server,
converts wildcard UI binds to loopback, disables environment proxies and
redirects, and requires HTTP `200` with the exact bounded body `ok`. It exposes
no configuration detail in its errors.

## Build inputs and reproducibility

The Dockerfile pins these multi-architecture image indexes:

| Input | Index digest |
|---|---|
| `golang:1.25.12-bookworm` | `sha256:ea341baa9bd5ba6784f6d7161ace70544349a6242d54d34a0fbfd2c4d51c9d58` |
| `distroless/cc-debian12:nonroot` | `sha256:fccdbb0a547c14e23fcf4ce8ad62ca5d43b4faae8d22cd292f490fef9946c96e` |

The amd64 builder installs no additional packages for an amd64 target. An
arm64 cross-build uses only the builder-stage
`gcc-aarch64-linux-gnu=4:12.2.0-3` and
`libc6-dev-arm64-cross=2.36-8cross1`. Neither reaches the runtime image.

Release automation must supply immutable values for `VERSION`, `COMMIT`, and
`BUILD_DATE`, and normalize layer timestamps. The Go build removes filesystem
paths, VCS auto-discovery, debug tables, and the random build ID. Module
downloads are checked against `go.sum` with `go mod verify`.

The verification builds used Podman 5.8.4 on x86-64:

```bash
podman build --format docker --timestamp 0 --no-cache \
  --platform linux/amd64 \
  --build-arg BUILDARCH=amd64 --build-arg TARGETARCH=amd64 \
  --build-arg VERSION=0.5.0-dev --build-arg COMMIT=1b1767f \
  --build-arg BUILD_DATE=2026-07-28T00:00:00Z \
  -t localhost/siftail:sft050-amd64 .

podman build --format docker --timestamp 0 \
  --platform linux/arm64 \
  --build-arg BUILDARCH=amd64 --build-arg TARGETARCH=arm64 \
  --build-arg VERSION=0.5.0-dev --build-arg COMMIT=1b1767f \
  --build-arg BUILD_DATE=2026-07-28T00:00:00Z \
  -t localhost/siftail:sft050-arm64 .
```

Two independent no-cache amd64 builds with those exact inputs produced the
same image ID:
`sha256:93cfacbdfe1995cac8805442b8d48bb424c7996d94c7ec9b687c2831745268b1`.
Changing source, dependency, metadata, base image, package repository, or
timestamp inputs is expected to change the result.

Podman's OCI output format does not retain Docker health-check metadata.
The tested command therefore uses `--format docker`. Docker Buildx emits
Docker/OCI registry manifests with the Dockerfile health configuration; release
automation must inspect the final registry artifact rather than assuming it.

## Measured footprint

Measurements used a Fedora x86-64 host with Podman 5.8.4. The compressed value
is a deterministic `gzip -n` of the Docker archive and is not a registry
transfer guarantee. Image store size is the local unpacked image size.

| Platform | Image store | Compressed archive | Binary | Binary SHA-256 |
|---|---:|---:|---:|---|
| `linux/amd64` | 37,202,267 B | 14,270,687 B | 12,131,960 B | `30c09914aff7fe67d4e2886a0ef2fdbf2272e8431db27f398a2278461b7c6c7a` |
| `linux/arm64` | 46,821,738 B | 13,635,765 B | 11,235,256 B | `20ff9fcf6bf0f61f6330210be2f8dc03e87caa6eb0e4e3e8ff1fac7351969785` |

The native amd64 container became ready in 395 ms, used 15,140 KiB process RSS
at readiness, and remained at 15,140 KiB after ten idle seconds. Its cgroup
working measurement was 4.088 MB. These are one-host observations, not
cross-host guarantees.

The arm64 image was executed through a bind-mounted static QEMU user emulator
because this host had no binfmt registration. It became ready in 824 ms and
the combined emulator/process RSS was 44,420 KiB at readiness and 44,432 KiB
after ten seconds (31.17 MB cgroup measurement). Those values validate the
image but are not representative native-arm64 performance measurements.

## Runtime dependencies, licensing, and security maintenance

The Go builder and cross compiler are build-time inputs only. The runtime is
the Apache-2.0-licensed Distroless project plus the Debian package content it
assembles. The image retains the applicable package copyright files under
`/usr/share/doc`. Runtime package families are `base-files`,
`ca-certificates`, `gcc-12-base`, `libc6`, `libgcc-s1`, `libgomp1`, `libssl3`,
`libstdc++6`, `media-types`, `netbase`, and `tzdata`; each retains its own
upstream/Debian license terms. `/LICENSE` covers Siftail itself.

Digest pinning makes changes reviewable but does not provide security updates.
Each release and base-image refresh must:

1. resolve and review new builder and runtime index digests;
2. rebuild both architectures;
3. inspect the exported runtime filesystem and package inventory;
4. run both architecture smoke tests; and
5. scan the final archives before publication.

Trivy `0.70.0`, pinned by container digest
`sha256:be1190afcb28352bfddc4ddeb71470835d16462af68d310f9f4bca710961a41e`,
scanned both final archives on 2026-07-28. Both had zero fixable
high/critical findings and no detected secrets. The complete amd64 report had
13 low, four medium, and one unknown finding, all without a Debian or Go fixed
version in that database. The unknown item was GO-2026-5932 in the unimported
deprecated `golang.org/x/crypto/openpgp` module path; `govulncheck` separately
reported no reachable or imported vulnerability. These results are
time-specific and must not be reused as a claim about a later image.

## Verification and platform limits

Run native smoke with:

```bash
scripts/container-smoke.sh localhost/siftail:sft050-amd64 amd64
```

The smoke creates a fresh database and administrator, creates a Server and
one-time ingestion token, starts both listeners, exercises the image health
command, ingests an event, signs in through the browser route, verifies
History, sends SIGTERM during a bounded batch, restarts, and verifies the
committed event. It also tests an intentionally unwritable bind mount and an
owner-mapped bind mount.

For non-native arm64 verification without system-wide binfmt registration,
provide an independently obtained static emulator:

```bash
SIFTAIL_QEMU_AARCH64=/absolute/path/to/qemu-aarch64-static \
  scripts/container-smoke.sh localhost/siftail:sft050-arm64 arm64
```

QEMU is a test-host tool and is never copied into the image. Native execution
remains required for representative arm64 performance evidence. Siftail
supports Linux containers only; native binaries for other operating systems
are not release artifacts.
