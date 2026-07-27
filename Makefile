.PHONY: fmt vet test race-test build clean

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

vet: fmt
	$(GO) vet ./...

test: vet
	$(GO) test ./...

race-test: vet
	$(GO) test -race ./...

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/siftail

clean:
	rm -f $(BINARY)
