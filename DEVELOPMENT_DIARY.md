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
