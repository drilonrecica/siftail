# syntax=docker/dockerfile:1

ARG GO_IMAGE=docker.io/library/golang:1.25.12-bookworm@sha256:ea341baa9bd5ba6784f6d7161ace70544349a6242d54d34a0fbfd2c4d51c9d58
ARG RUNTIME_IMAGE=gcr.io/distroless/cc-debian12:nonroot@sha256:fccdbb0a547c14e23fcf4ce8ad62ca5d43b4faae8d22cd292f490fef9946c96e

FROM --platform=${BUILDPLATFORM} ${GO_IMAGE} AS build

ARG BUILDARCH
ARG TARGETARCH
ARG TARGETOS=linux
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=1970-01-01T00:00:00Z

WORKDIR /src

RUN if [ "${BUILDARCH}" != "${TARGETARCH}" ]; then \
      case "${TARGETARCH}" in \
        amd64) packages="gcc-x86-64-linux-gnu=4:12.2.0-3 libc6-dev-amd64-cross=2.36-8cross1" ;; \
        arm64) packages="gcc-aarch64-linux-gnu=4:12.2.0-3 libc6-dev-arm64-cross=2.36-8cross1" ;; \
        *) echo "unsupported target architecture: ${TARGETARCH}" >&2; exit 1 ;; \
      esac; \
      apt-get update; \
      apt-get install -y --no-install-recommends ${packages}; \
      rm -rf /var/lib/apt/lists/*; \
    fi

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY cmd ./cmd
COPY internal ./internal

RUN case "${TARGETARCH}" in \
      amd64) cross_cc=x86_64-linux-gnu-gcc ;; \
      arm64) cross_cc=aarch64-linux-gnu-gcc ;; \
      *) echo "unsupported target architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    if [ "${BUILDARCH}" = "${TARGETARCH}" ]; then cross_cc=gcc; fi; \
    mkdir -p /out/data; \
    CGO_ENABLED=1 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" CC="${cross_cc}" \
      go build -trimpath -buildvcs=false \
      -ldflags="-s -w -buildid= \
        -X github.com/drilonrecica/siftail/internal/version.Version=${VERSION} \
        -X github.com/drilonrecica/siftail/internal/version.Commit=${COMMIT} \
        -X github.com/drilonrecica/siftail/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/siftail ./cmd/siftail

FROM --platform=${TARGETPLATFORM} ${RUNTIME_IMAGE}

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=1970-01-01T00:00:00Z

LABEL org.opencontainers.image.title="Siftail" \
      org.opencontainers.image.description="Fast, private logs for self-hosted apps." \
      org.opencontainers.image.source="https://github.com/drilonrecica/siftail" \
      org.opencontainers.image.url="https://github.com/drilonrecica/siftail" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}"

COPY --from=build --chown=65532:65532 /out/siftail /siftail
COPY --from=build --chown=65532:65532 /out/data /data
COPY --chown=65532:65532 LICENSE /LICENSE

USER 65532:65532
WORKDIR /
VOLUME ["/data"]
EXPOSE 8080 8081

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/siftail", "healthcheck"]

ENTRYPOINT ["/siftail"]
CMD ["serve"]
