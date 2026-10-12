# Development diary

## 2026-10-12 — Phase 1: core HTTP request generator

The repository started empty. I used the thesis draft's **HTTP Request Execution** section as the scope for the first implementation: configure and send a single HTTP request, then report status, elapsed time, response bytes, and error type. The document also contains thesis template guidance; that guidance is not a software requirement.

I put HTTP execution in `internal/request` and kept the CLI in `cmd/httpload`. The generator copies headers and the body at construction, gives each send a fresh body reader, and reuses one HTTP client. This keeps later concurrency strategies independent of request details. Duration covers response body consumption, and HTTP error statuses remain valid results. The CLI intentionally sends only one request in this phase.

Verification: `go test ./...`, `go test -race ./...`, and `go vet ./...` passed. Local HTTP test server cases cover method, headers, body, repeated sends, status, byte count, timeout, cancellation, and invalid configuration. A CLI smoke test sent a POST to a local server and reported status 201 with two response bytes. Remaining phases: concurrency strategies, streaming metrics and latency histograms, TUI, uptime monitoring, and SQLite persistence. Benchmark claims will require controlled measurements later; none are claimed here.
