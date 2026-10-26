# HTTP load testing and uptime monitoring tool

An incremental Go implementation of the terminal tool described in the accompanying thesis project. It supports individual HTTP requests and concurrent load generation using three interchangeable concurrency patterns. It reports throughput, error rates and HDR-histogram latency percentiles, and can show a live terminal dashboard. Uptime monitoring and persistence are future phases.

## Run one request

Requires Go 1.27.2 or newer.

```sh
go run ./cmd/httpload -url http://localhost:8080/health
go run ./cmd/httpload -url http://localhost:8080/api -method POST -header 'Content-Type: application/json' -body '{"ok":true}' -timeout 3s
```

`-header` can be repeated. The command reports the HTTP status, elapsed time through full response body consumption, and number of response bytes. Network failures, cancellation, timeouts, and body read failures are classified separately. An HTTP error status is a completed response, not a transport failure. The response body is discarded after counting its bytes.

Only send requests to systems you control or have permission to test.

## Run a concurrent load test

```sh
go run ./cmd/httpload -url http://localhost:8080/health -pattern worker-pool -concurrency 10 -requests 1000
go run ./cmd/httpload -url http://localhost:8080/health -pattern fan-out-fan-in -concurrency 10 -duration 30s
go run ./cmd/httpload -url http://localhost:8080/health -pattern pipeline -concurrency 10 -duration 30s
```

All request options from single-request mode also apply to load tests. Supplying any load option (`-pattern`, `-concurrency`, `-requests`, or `-duration`) selects load mode. Defaults are `worker-pool`, concurrency `1`, and one request. A positive duration without an explicit request count runs continuously until the duration expires. With both limits, the first reached stops the test. `-requests 0` requires a positive duration; negative limits and nonpositive concurrency are rejected before sending requests.

| Pattern | Work distribution and result collection |
| --- | --- |
| `worker-pool` | A fixed worker group reads a shared job queue and writes to a shared results channel. |
| `fan-out-fan-in` | Workers read a shared job queue, write to individual output channels, and forwarding goroutines merge those outputs. |
| `pipeline` | Job generation, concurrent HTTP execution, result classification, and summary aggregation run as connected stages. |

These patterns overlap conceptually. The implementations make their channel layouts explicit so their coordination overhead can be compared with the same request generator and summary logic. Each limits simultaneous `Send` calls to `-concurrency` and uses channel capacity proportional to that value, without storing the full request workload or result history. Workers send as quickly as the target and result collector allow; there is no fixed request rate.

Ctrl+C or SIGTERM stops scheduling and cancels active HTTP requests. Duration expiry also cancels active requests. Every started request contributes exactly one result before the runner returns; queued jobs that have not started are excluded. A request is started after checking cancellation, so cancellation can race with an individual `Send` call; that call receives the canceled context. The runner relies on request senders honoring context cancellation.

The final summary reports attempted requests, fully consumed responses, execution errors, HTTP error statuses (400 and above), cancellations, elapsed time, attempted requests per second, and response bytes. HTTP error statuses also count as completed responses when their bodies are read successfully. Execution errors include network, timeout, cancellation, and body-read failures. The HTTP error count includes any received error status, even if the body subsequently fails. Cancellations are the subset classified as `canceled`; requests interrupted by the duration deadline are classified as timeouts and included in execution errors. Elapsed time includes shutdown and result collection.

The summary also reports error rates and latency distributions (min, average, p50, p95, p99, max) for successful responses, HTTP error responses and execution failures, recorded in HDR histograms with a 1µs–10min range and three significant digits.

## Interactive terminal UI

```sh
go run ./cmd/httpload -tui
go run ./cmd/httpload -tui -url http://localhost:8080/health -pattern pipeline -concurrency 50
```

`-tui` opens a configuration form (URL, method, pattern, workers, requests, duration, timeout); any flags prefill it, and headers and body are taken from flags. Without `-requests` the form starts with no request limit and a 10s duration. Use ↑/↓ to move, ←/→ to change options and Enter to start; invalid values are shown under the form.

While a test runs, the dashboard shows request packets flowing to the server (red for errors), a progress bar, a requests-per-second sparkline, result counts, live latency percentiles and a latency histogram (16 buckets from min to p99). `q` or Ctrl+C stops the test early. Afterwards `r` returns to the form for another run and `q` exits, printing the summary of the last test.

A completed load run exits with code `0`, including when individual requests fail; inspect the summary for those failures. External cancellation exits with code `1` after printing the partial summary. Invalid arguments exit with code `2`. Single-request mode retains its original output and exits with code `1` for an execution error.

## Development

```sh
make run ARGS='-url http://localhost:8080/health'
make test
make test-race
make vet
make check
make build
```

`make run` passes `ARGS` to the CLI. `make check` runs tests, race tests, and `go vet`. `make build` writes `bin/httpload`.

The request execution package lives in `internal/request`. It owns configuration validation and a reusable `http.Client`. `internal/load` selects a concurrency strategy through `NewRunner` and calls the shared `Sender` interface (`Generator.Send`), then aggregates results in one goroutine. `internal/tui` renders the dashboard with Bubble Tea from snapshots produced by `Runner.RunWithProgress`. `Runner.Run` returns a summary and an error for external cancellation; reaching the configured duration is normal completion. Progress and decisions are recorded in [DEVELOPMENT_DIARY.md](DEVELOPMENT_DIARY.md).
