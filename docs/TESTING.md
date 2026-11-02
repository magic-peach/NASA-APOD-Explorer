# Testing

The suite has 67 test functions (153 test cases including table-driven subtests) and 3 benchmarks. It runs in a few seconds with no network access: NASA, the solar-system API and the language model are replaced by fakes or `httptest` servers, and the database is an in-memory SQLite.

## Running the tests

```sh
make test                      # go test ./...
make race                      # go test -race ./...  (what CI runs)
make cover                     # coverage summary + coverage.html
make lint                      # gofmt check, go vet, golangci-lint

go test ./internal/apod/...    # one package
go test -run Singleflight -v ./internal/apod   # one test, verbose
go test -count=5 -race ./internal/apod ./internal/cache   # repeat to shake out flakiness
```

CI runs `go test -race -covermode=atomic -coverprofile=coverage.out ./...` on every push and pull request. Total statement coverage is **69.2%**. The run page shows the summary, and `coverage.out` is uploaded as an artifact; open it with `go tool cover -html=coverage.out`.

## The race detector

`-race` instruments every memory access. If two goroutines touch the same variable without synchronisation and at least one of them writes, the test fails with a report like:

```
WARNING: DATA RACE
Write at 0x00c000123456 by goroutine 42:
  github.com/.../internal/cache.(*Cache).Set(...)  cache.go:71
Previous read at 0x00c000123456 by goroutine 17:
  github.com/.../internal/cache.(*Cache).Get(...)  cache.go:58
```

- **Reading a report:** the two stacks show the conflicting accesses, so the fix goes wherever they should have shared a lock or channel.
- **It needs CGO and a C compiler**, which is why the Jenkins pipeline uses the Debian-based `golang` image rather than Alpine.
- **It's dynamic.** It only reports races that actually happen during the run. That's why the concurrency tests deliberately start many goroutines at once (via a closed channel acting as a starting gun) instead of hoping the scheduler overlaps them.

## What the concurrency tests prove

### `TestSingleflightPreventsStampede` (`internal/apod/cached_test.go`)

The setup:

- **Fake upstream.** `slowUpstream` counts its calls with an `atomic.Int64` and takes 50 ms to answer. The delay keeps the first call in flight long enough for every other goroutine to arrive, which is exactly the window in which a stampede happens.
- **100 goroutines at once.** They are started and blocked on a channel; closing the channel releases them all at the same instant, and a `sync.WaitGroup` waits for them to finish.
- **Each goroutine writes only its own slot** in a results slice, so collecting results needs no lock.

It then asserts:

1. **The upstream was called exactly once.** This is the point of `singleflight`.
2. **All 100 callers got the same result.** They share the same backing array (`&items[0]` is identical for every caller), so it's literally one result handed to everyone, not 100 equal copies.
3. **A follow-up request is a plain cache hit:** still one upstream call in total.

The test passes under `-race`, so the sharing is also free of data races.

### `TestNaiveCacheStampedes`

The control experiment. Under identical load, a cache **without** `singleflight` (check, miss, fetch, store) makes **100** upstream calls. Without this test, "one call" could just mean the fake was too fast for the goroutines to overlap. With it, the test shows the stampede is real and that `singleflight` is what removes it.

### Other concurrency tests

| Test | Proves |
|---|---|
| `TestCallerCancellationDoesNotCancelSharedFetch` | A caller whose context times out gets `context.DeadlineExceeded`, while a second caller sharing the same flight still succeeds, and there is still only one upstream call. The shared fetch is not tied to the first caller. |
| `TestErrorsAreNotCached` | A failed fetch is retried by the next request. |
| `TestRandomCountQueriesBypassCache` | `?count=` calls upstream every time (random results must not be cached or shared). |
| `TestConcurrentAccess` (cache) | 50 goroutines mixing `Set`, `Get`, `Delete` while the janitor runs. It asserts little itself; its job is to give `-race` something to catch. |
| `TestJanitorEvictsAndStopsOnCancel` | The janitor evicts expired entries and its goroutine exits when the context is cancelled (no goroutine leak). |
| `TestSummaryCoalescesConcurrentMisses` (spacefacts) | 50 concurrent page loads make one upstream call. |
| `TestServeDrainsInFlightRequests` (cmd/server) | A request that is mid-flight when the stop signal arrives still completes; `serve` only returns afterwards; new connections are then refused. |

## Benchmarks

```sh
make bench
# = go test -run '^$' -bench . -benchmem ./internal/cache ./internal/apod
```

`-run '^$'` matches no tests, so only benchmarks run. Sample output on an Apple M3 Pro:

```
BenchmarkCacheGet-11                         68.8 ns/op     0 B/op   0 allocs/op
BenchmarkCacheGetParallel-11                 94.8 ns/op     0 B/op   0 allocs/op
BenchmarkStampede/with_singleflight-11    1379806 ns/op   1.000 upstream-calls/op
BenchmarkStampede/cache_only-11           1419595 ns/op   99.99 upstream-calls/op
```

How to read them:

- **`BenchmarkCacheGet`.** A cache hit costs about 70 ns and allocates nothing.
- **`BenchmarkCacheGetParallel`.** Many goroutines reading at once are *slower* per operation than one. Readers never block each other under `RWMutex`, but each `RLock` still updates a shared counter, so that memory location bounces between CPU cores. If this ever mattered, the usual fixes are sharding the map or `sync.Map`. For this app, where requests are network-bound, it doesn't.
- **`BenchmarkStampede`.** The interesting number is the custom `upstream-calls/op` metric (reported with `b.ReportMetric`), not ns/op. Each iteration fires 100 concurrent requests at an uncached key:
  - with `singleflight`, NASA is called once;
  - with only a cache, about 100 times.

  The ns/op is similar because the fake upstream answers in 1 ms either way. The difference that matters in production is 100× fewer calls against a rate-limited API.

Compare runs with `benchstat`:

```sh
go test -run '^$' -bench . -count 10 ./internal/apod > new.txt
benchstat old.txt new.txt
```

## Test map

| Package | What's covered |
|---|---|
| `internal/cache` | expiry at and just before the TTL, per-entry TTL, overwrite refreshing expiry, eviction, janitor shutdown, concurrent access |
| `internal/apod` | query keys, both NASA response shapes, upstream errors, the API key never appearing in errors, placeholder detection, the demo client, `singleflight` behaviour (above) |
| `internal/config` | defaults, overrides, and rejecting bad values (TTL, rate limits, log format, short session secret, booleans) |
| `internal/store` | duplicate users, idempotent favorites, owner-only deletes, a SQL-injection string matching nothing, ratings, sessions expiring, migrations being versioned and idempotent, placeholders and samples never persisted |
| `internal/httpapi` | response shapes; the JSON error shape for every error class; validation tables (formats, archive bounds, New York "today", 30-day ranges, counts, conflicting parameters); middleware (request IDs, panic recovery, CORS, per-IP rate limits, trusted proxy header, security headers); health and readiness |
| `internal/httpapi`, auth and favorites | cookie flags, signup validation, identical login failures, logout invalidation, session expiry, hashed token storage, legacy password upgrade, favorites CRUD and isolation, cross-site writes blocked, history only on explicit views |
| `internal/spacefacts`, `internal/llm` | summary maths, bearer key, request coalescing; model reply vs fallback, UTF-8-safe truncation |
| `cmd/server`, `web` | graceful shutdown, the container healthcheck, embedded frontend files present |

## Not covered by automated tests

- **The frontend** has no browser test suite yet; Playwright is listed under future work in the README. It was checked by hand in Chrome:
  - views: hero, strip, search, gallery, viewer;
  - keyboard: ←/→/Esc, with focus returning to the opener;
  - data: deep links, the favorites move on login, ratings, comments, the explanation tools;
  - safety: no CSP violations, and a comment containing HTML displayed as plain text;
  - layout: checked at 500 px wide (headless Chrome's minimum).
- **The Jenkins pipeline, Airflow DAG and n8n workflow** were checked as described in [`INTEGRATIONS.md`](INTEGRATIONS.md#how-these-were-checked), not run inside their real tools.
