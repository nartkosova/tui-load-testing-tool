GO ?= go

.PHONY: run test test-race vet fmt check build bench

run:
	$(GO) run ./cmd/httpload $(ARGS)

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

check: test test-race vet

build:
	$(GO) build -o bin/httpload ./cmd/httpload

bench:
	$(GO) test -run XXX -bench . -benchmem ./...
