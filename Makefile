GO ?= go

.PHONY: help fmt fmt-check vet build test test-unit test-race check migrate migrate-version run

help:
	@printf '%s\n' \
		'make fmt        Format Go source' \
		'make fmt-check  Check Go formatting' \
		'make vet        Run go vet' \
		'make build      Build all packages' \
		'make test       Run unit and integration tests with a disposable PostgreSQL database' \
		'make test-unit  Run unit tests without Docker' \
		'make test-race  Run the full test suite with the race detector' \
		'make check      Check formatting, vet, tests, and build' \
		'make migrate    Apply pending database migrations' \
		'make migrate-version  Show the database migration version' \
		'make run        Start the HTTP server'

fmt:
	$(GO) fmt ./...

fmt-check:
	@files="$$(gofmt -l .)" || exit $$?; \
	if [ -n "$$files" ]; then \
		printf 'Run make fmt for:\n%s\n' "$$files"; \
		exit 1; \
	fi

vet:
	$(GO) vet ./...

build:
	$(GO) build ./...

test:
	GO="$(GO)" ./scripts/test.sh

test-unit:
	$(GO) test ./...

test-race:
	GO="$(GO)" ./scripts/test.sh -race

check: fmt-check vet test build

migrate:
	$(GO) run ./cmd/migrate up

migrate-version:
	$(GO) run ./cmd/migrate version

run:
	$(GO) run ./cmd/server
