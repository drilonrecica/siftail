.PHONY: fmt fmt-check vet test race-test build check clean

GO       := go
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
	$(GO) test ./...

race-test:
	$(GO) test -race ./...

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/siftail

check: fmt-check vet test build

clean:
	rm -f $(BINARY)
