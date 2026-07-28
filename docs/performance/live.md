# Live delivery baseline

**Measured:** 2026-07-28
**Scope:** SFT-028 development and release-gate evidence; not a
hardware-independent guarantee

## Method and environment

- Host: Fedora Linux 7.1.4-204.fc44.x86_64, linux/amd64.
- CPU: Intel Core i5-7500, four physical cores, 3.40 GHz nominal.
- Memory: 31 GiB host RAM; no container memory or CPU limit.
- Toolchain: Go 1.26.5 linux/amd64 with CGO. The repository compatibility floor
  remains the version in `go.mod`.
- Broker fixture: one application-owned broker at the production defaults and
  all 16 allowed subscribers. Every measured operation admits one representative
  committed event and waits until all 16 subscriptions receive it. This measures
  bounded broker fan-out, not SQLite commit or network transport.
- SSE fixture: the production JSON and SSE frame encoder with representative
  source, container, level, stream, attribute, and common-field data, writing
  through a response writer that implements production deadline and flush
  interfaces. It measures encoding and framing, not session lookup, TCP, proxy,
  or browser rendering.
- Benchmark command:

  ```bash
  go test -run '^$' \
    -bench '^BenchmarkLive(BrokerPublish16Subscribers|SSEFrame)$' \
    -benchtime=3s -count=5 -benchmem ./internal/logs ./internal/auth
  ```

## Resource ledger

The default bounds are asserted directly by
`TestLiveBrokerDefaultResourceLedger`:

| Resource | Bound |
|---|---:|
| Concurrent Live subscriptions | 16 |
| Queue per subscription | 256 messages |
| Retained canonical bytes per subscription | 2 MiB |
| Broker command ingress | 256 commands |
| Queued broker publications | 10,000 events |
| Retained canonical broker publication bytes | 16 MiB |
| Filter values per filter dimension | 256 |
| Encoded SSE message plus attribute preview | 8 KiB |
| Rendered browser rows per Live workspace | 1,000 |
| Pending browser events per Live workspace | 2,000 |

At the maximum, subscriber queues retain at most 32 MiB of accounted canonical
payload and the publication queue retains at most another 16 MiB. The 48 MiB
sum is an admission ledger, not an RSS prediction: Go objects, maps, channel
storage, encoded frames, the browser process, and allocator overhead are not
represented by it. Publications that cannot enter the fixed event, byte, or
command bounds fail immediately and force current subscribers to reconnect with
an explicit gap.

## Results

Five development-machine samples produced:

| Benchmark | Median | Sample range | Allocated bytes/op | Allocations/op |
|---|---:|---:|---:|---:|
| Broker publication to 16 subscribers | 4.909 µs/event | 4.897–4.922 µs | 440 | 2 |
| SSE JSON encoding and framing | 2.277 µs/frame | 2.256–2.290 µs | 1,913 | 7 |

The broker benchmark reports exactly 16 deliveries per operation. The SSE
fixture averaged 692.6 encoded wire bytes per operation; event-ID digit growth
causes the fractional average.

## Concurrency and browser evidence

The targeted stress fixture starts eight publishers and eight subscription
churn workers. Each repetition attempts 4,000 non-blocking publications and
completes 800 subscribe/read/close lifecycles. It then proves a fresh subscriber
receives a final event, waits for every owned worker and broker goroutine, and
asserts the broker's queued-event and queued-byte ledgers both return to zero.

Commands and results:

```bash
go test -count=100 ./internal/logs \
  -run '^TestLiveBrokerConcurrentStressDrainsOwnedResources$'
# pass in 21.048s

go test -race -count=25 ./internal/logs \
  -run '^TestLiveBrokerConcurrentStressDrainsOwnedResources$'
# pass in 13.301s
```

The HTTP transport tests separately hold a slow stream write while publishing,
verify another connection is refused at the configured limit, and verify
overflow produces a visible `truncated` control before the handler exits.
Reconnect coverage sends `Last-Event-ID`, receives `possible_gap`, and observes
only newly published broker events; version one does not claim replay.

The production-binary Chromium suite navigates between History and Live,
reconnects filters and the manual control while maintaining one active native
`EventSource`, closes streams on navigation, and proves that Clear view does not
delete retained History. It also exercises the 1,000-rendered and 2,000-pending
browser caps, slow-reader scroll ownership, online session invalidation, hostile
content, keyboard use, themes, reduced motion, accessibility, and a 390 px
viewport.

## Review and limitations

- Publication remains post-commit and non-blocking; Live delivery cannot delay
  or roll back the controlled SQLite writer.
- Slow subscribers and publication loss close with explicit truncation rather
  than allocating past the ledger. Reconnection begins with future commits and
  directs possible gaps to History.
- Every stress-owned goroutine has a wait boundary. Passing these finite tests
  is evidence against lifecycle leaks, not a proof about an indefinitely long
  process.
- Benchmarks contain synthetic content and no real payloads, credentials,
  telemetry, or outbound runtime calls.
- These microbenchmarks do not include SQLite, ingestion decoding, HTTP/TCP,
  reverse proxies, browser RSS, a production container, or a long soak. Those
  remain later performance and stable-release gates and these values must not be
  generalized to other hardware.
