# Development diary

## 2026-10-12 — Core request generator

- Added configurable single-request HTTP execution and a CLI.
- Recorded status, duration, response bytes, and error type.
- Verified request behavior, timeout, cancellation, and CLI output with local tests.

## 2026-10-12 — Tooling update

- Updated the project to Go 1.27.2.
- Added Makefile commands for running, testing, formatting, checking, and building.
- Removed source comments and simplified the diary format.
- Verified `make check`, `make build`, and `make run` against a local server.

## 2026-10-15 — Concurrency patterns

- Added interchangeable worker-pool, fan-out/fan-in, and pipeline strategies in `internal/load`, reusing the existing HTTP request generator.
- Kept job and result queues bounded by concurrency, with a single owner of summary counters. Fan-in uses individual worker outputs and merging goroutines; the pipeline separates result classification into its own stage.
- Added request-count and duration limits, cancellation propagation, and result draining so every started request is accounted for before shutdown. Duration expiry is normal completion; external cancellation returns a partial summary and an error.
- Added CLI strategy selection, concurrency and workload flags, final summaries, and Ctrl+C/SIGTERM handling while retaining the original single-request mode.
- Verified configuration, exact request counts, simultaneous execution limits, repeated runs, deadlines, cancellation under backpressure, shared HTTP configuration, and CLI behavior with tests, race detection, and `go vet`.
- Built the binary and checked all three patterns against a local HTTP server, including exact request counts, duration expiry, SIGINT, and SIGTERM shutdown.
