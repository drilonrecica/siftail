.PHONY: fmt fmt-check vet test race-test build compose-check check bench container-bench soak frontend-check playwright playwright-update release-plan-test release-dry-run clean

GO       := go
# Upper bound for one package's test binary; a hung test fails instead of stalling igris.
TEST_TIMEOUT ?= 10m
BINARY   := siftail

VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -X github.com/drilonrecica/siftail/internal/version.Version=$(VERSION) \
           -X github.com/drilonrecica/siftail/internal/version.Commit=$(COMMIT) \
           -X github.com/drilonrecica/siftail/internal/version.BuildDate=$(BUILD_DATE)

fmt:
	$(GO) fmt ./...

fmt-check:
	@files="$$(gofmt -l .)"; \
	if [ -n "$$files" ]; then \
		echo "Go files need formatting:"; \
		echo "$$files"; \
		exit 1; \
	fi

vet:
	$(GO) vet ./...

test:
	$(GO) test -timeout $(TEST_TIMEOUT) ./...

race-test:
	$(GO) test -race -timeout $(TEST_TIMEOUT) ./...

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/siftail

compose-check:
	scripts/compose-validate.sh

check: fmt-check vet test build compose-check

# Benchmarks recorded under docs/performance. TMPDIR must be on a real disk
# (AGENTS.md section 17.2); scripts/bench.sh refuses tmpfs. BENCH_GROUPS limits
# the run, e.g. `make bench BENCH_GROUPS="ingest logs"`; `all` adds group-commit.
BENCH_GROUPS ?=
bench:
	scripts/bench.sh $(BENCH_GROUPS)

# Production-container measurement and 10-million-event query gate
# (docs/performance/0.5.0-container.md): about an hour and 20 GB of TMPDIR.
# IMAGE names a built image, e.g. `make container-bench IMAGE=localhost/siftail:dev`.
container-bench:
	scripts/container-bench.sh $(IMAGE)

# The bounded production soak (docs/performance/0.5.0-soak.md), three hours
# by default: `make soak IMAGE=localhost/siftail:dev [SOAK_FLAGS=--quick]`.
soak:
	scripts/soak.sh $(IMAGE) $(SOAK_FLAGS)

# Channel and tag rules of the release workflow (docs/release/process.md).
release-plan-test:
	scripts/release-plan-test.sh

# Unpublished local release build of a committed revision: the
# multi-architecture OCI archive, SBOMs, metadata, and checksums in
# RELEASE_DIR, then the image contract check. Example:
# `make release-dry-run RELEASE_VERSION=0.5.0-rc.1 RELEASE_BUILDER=siftail-release`.
RELEASE_VERSION ?= edge-$(shell git rev-parse --short=12 HEAD 2>/dev/null)
RELEASE_REF     ?= HEAD
RELEASE_DIR     ?= .bench-tmp/release-$(RELEASE_VERSION)
release-dry-run:
	scripts/release-build.sh $(RELEASE_VERSION) $(RELEASE_REF) $(RELEASE_DIR)
	scripts/release-inspect.sh $(RELEASE_DIR)/siftail-$(RELEASE_VERSION).oci.tar \
		$(RELEASE_VERSION) $$(git rev-parse --short=12 $(RELEASE_REF))

frontend-check:
	npm run frontend-check

playwright:
	npm run playwright

# Rewrites the compared reference screenshots under tests/browser/__screenshots__
# after an intended visual change; review the image diff before committing.
playwright-update:
	npm run playwright:update

clean:
	rm -f $(BINARY)
	rm -rf .bench-tmp || podman unshare rm -rf .bench-tmp
