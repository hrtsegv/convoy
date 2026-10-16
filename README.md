# Convoy

Convoy is a Go project for building a PostgreSQL-backed durable job execution system.

The repository currently contains configuration parsing, PostgreSQL pool bootstrap, a migration command, and development checks.

## Development

Use Go **1.27.2** and Make.

```sh
go version
make check
```

Run `make help` for available commands. Use `make test-race` to run tests with Go's race detector.

CI selects the Go version from `go.mod` and checks formatting, vet, build, and tests with the race detector.

The default tests run without PostgreSQL. To run the bootstrap integration test against a running test database:

```sh
CONVOY_TEST_DATABASE_URL='postgres://user:password@127.0.0.1:5432/convoy_test?sslmode=disable' \
  go test -race -tags=integration ./internal/postgres -v
```

The integration test opens a pool, cancels its startup context, and verifies that a ping using a fresh context still succeeds. Selecting the `integration` build tag requires `CONVOY_TEST_DATABASE_URL`; a missing value fails the test.

To verify the migration command and driver against PostgreSQL:

```sh
CONVOY_TEST_DATABASE_URL='postgres://user:password@127.0.0.1:5432/convoy_test?sslmode=disable' \
  go test -race -tags=integration ./internal/migrate ./cmd/migrate -v
```

These tests create and drop isolated schemas, so the test database user needs permission to create schemas. They verify that whole-file execution rolls back the first statement when the second fails, while statement splitting leaves partial state. Both cases retain dirty migration metadata and block subsequent `up` invocations. They also cover the embedded baseline, repeated execution, graceful stopping between files, initialization cleanup, and command output.

## Migrations

Set `CONVOY_DATABASE_URL` to the target database, then run migrations separately from the server:

```sh
go run ./cmd/migrate up
go run ./cmd/migrate version
```

`make migrate` and `make migrate-version` run the same commands. The command only reads `CONVOY_DATABASE_URL`; server settings do not affect migration execution. Build a standalone binary with `go build -o bin/migrate ./cmd/migrate` when needed.

SQL files are embedded from `internal/migrate/migrations`. Version 1 is a no-op baseline (`SELECT 1`); it creates no application tables. Repeating `up` with no pending migrations succeeds. `version` reports the version and dirty flag, or `version=none dirty=false` on a fresh database. Initializing either command creates the migration metadata table if needed.

The command uses golang-migrate's pgx/v5 driver through `WithInstance`, pgx simple-query mode, and `MultiStatementEnabled: false`. Ordinary migration files contain no explicit transaction-control statements or operations that cannot run in a transaction: each whole file runs in PostgreSQL's implicit transaction. DSN parameters beginning with `x-` are rejected; migration driver options belong in code.

Failed migrations leave dirty metadata and block further `up` invocations. Inspect the actual database state before manual recovery; the command exposes neither `force` nor automatic repair. Dirty metadata alone does not prove whether SQL committed after a connection failure or forced interruption.

The first Ctrl-C requests a graceful stop after the current file and its version bookkeeping finish. Cancellation is checked before the next file is marked dirty, avoiding a race in the pinned library's `GracefulStop` implementation. Interrupted execution exits with status 130. After the first interrupt, normal signal handling is restored so a second Ctrl-C can force termination and leave dirty metadata. The driver adds no execution timeout; PostgreSQL's configured timeouts still apply. `CONVOY_DB_QUERY_TIMEOUT` is reserved for readiness checks. Graceful stop does not bound waits during initialization, locking, or cleanup. A dedicated migration timeout is deferred until substantive schema changes need an execution budget.

## Configuration

Configuration is read from environment variables. Optional variables use their defaults when unset or blank. Environment files are not loaded automatically.

| Variable | Default | Meaning |
| --- | --- | --- |
| `CONVOY_DATABASE_URL` | Required | PostgreSQL connection string |
| `CONVOY_HTTP_ADDR` | `127.0.0.1:8080` | HTTP listen address, including port |
| `CONVOY_LOG_LEVEL` | `INFO` | Go structured logging level, such as `DEBUG`, `INFO`, `WARN`, or `ERROR` |
| `CONVOY_DB_MAX_CONNS` | `4` | Maximum database pool connections |
| `CONVOY_DB_CONNECT_TIMEOUT` | `5s` | Database startup connection-check deadline |
| `CONVOY_DB_QUERY_TIMEOUT` | `2s` | Database readiness-check deadline |
| `CONVOY_SHUTDOWN_TIMEOUT` | `10s` | HTTP shutdown deadline |

Timeouts must be positive Go durations, such as `500ms` or `5s`. The pool connection limit must be a positive 32-bit integer. Database connection-string syntax is validated by the database driver when connecting.
