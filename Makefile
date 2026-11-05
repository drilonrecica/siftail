.PHONY: fmt fmt-check vet test race-test build compose-check check bench frontend-check playwright clean

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

frontend-check:
	npm run frontend-check

playwright:
	npm run playwright

clean:
	rm -f $(BINARY)
	rm -rf .bench-tmp
