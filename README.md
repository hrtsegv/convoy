# Convoy

Convoy is a Go project for building a PostgreSQL-backed durable job execution system.

The repository currently contains configuration parsing, PostgreSQL pool bootstrap, and development checks.

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
