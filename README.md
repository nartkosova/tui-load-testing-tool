# HTTP load testing and uptime monitoring tool

An incremental Go implementation of the terminal tool described in the accompanying thesis project. The current phase sends **one** HTTP request and reports its outcome. Concurrent load generation, metrics aggregation, the TUI, uptime monitoring, and persistence are future phases.

## Run one request

Requires Go 1.27.2 or newer.

```sh
go run ./cmd/httpload -url http://localhost:8080/health
go run ./cmd/httpload -url http://localhost:8080/api -method POST -header 'Content-Type: application/json' -body '{"ok":true}' -timeout 3s
```

`-header` can be repeated. The command reports the HTTP status, elapsed time through full response body consumption, and number of response bytes. Network failures, cancellation, timeouts, and body read failures are classified separately. An HTTP error status is a completed response, not a transport failure. The response body is discarded after counting its bytes.

Only send requests to systems you control or have permission to test.

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

The request execution package lives in `internal/request`. It owns configuration validation and a reusable `http.Client`; future scheduling strategies can call `Generator.Send` without changing HTTP execution or result format. Progress and decisions are recorded in [DEVELOPMENT_DIARY.md](DEVELOPMENT_DIARY.md).
