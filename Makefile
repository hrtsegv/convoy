GO ?= go

.PHONY: help fmt fmt-check vet build test test-race check migrate migrate-version

help:
	@printf '%s\n' \
		'make fmt        Format Go source' \
		'make fmt-check  Check Go formatting' \
		'make vet        Run go vet' \
		'make build      Build all packages' \
		'make test       Run tests' \
		'make test-race  Run tests with the race detector' \
		'make check      Check formatting, vet, tests, and build' \
		'make migrate    Apply pending database migrations' \
		'make migrate-version  Show the database migration version'

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
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

check: fmt-check vet test build

migrate:
	$(GO) run ./cmd/migrate up

migrate-version:
	$(GO) run ./cmd/migrate version
