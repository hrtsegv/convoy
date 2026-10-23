# Convoy

Convoy is a Go project for building a PostgreSQL-backed durable job execution system.

Convoy supports durable job enqueue and lookup in one queue, plus one worker that executes handlers and records success. Configuration, migrations, HTTP health/readiness, and development checks are included. Failed or interrupted jobs require manual inspection; automatic recovery and retries are not supported.

## Development

Use Go **1.27.2**, Make, Bash, and a running local Docker daemon.

```sh
go version
make check
```

Run `make help` for available commands.

| Command | Checks |
| --- | --- |
| `make test` | Unit and integration tests against a fresh PostgreSQL database |
| `make test-race` | Full suite with Go's race detector |
| `make test-unit` | Unit tests without Docker |
| `make check` | Formatting, vet, full test suite, and build |

The full suite starts an isolated `postgres:17-alpine` container, waits for TCP readiness, applies migrations, and runs all tests with the `integration` tag. Each run uses a dynamically allocated loopback port and a fresh anonymous data volume. The script removes its container and volume on success, failure, or handled interruption. It does not reuse an existing `CONVOY_TEST_DATABASE_URL`, and test caching is disabled so each run exercises its fresh database. The suite includes application startup, HTTP health/readiness, graceful shutdown, job persistence across an API process restart, and commit-failure behavior. Job tests create and migrate their own isolated schemas.

CI selects Go from `go.mod` and runs formatting, vet, build, and the same Docker-backed full suite with the race detector.

For focused tests against a test database you manage yourself, set `CONVOY_TEST_DATABASE_URL`. For example, run the bootstrap integration tests with:

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

Run the full integration suite, including PostgreSQL readiness checks, with:

```sh
CONVOY_TEST_DATABASE_URL='postgres://user:password@127.0.0.1:5432/convoy_test?sslmode=disable' \
  go test -race -tags=integration ./... -v
```

## Server

Set `CONVOY_DATABASE_URL` to a running PostgreSQL database, apply migrations, and start the server:

```sh
make migrate
go build -o bin/server ./cmd/server
./bin/server
```

`make run` starts the server through `go run`; use the standalone binary to observe signal handling and exit status directly. The default address is `127.0.0.1:8080`.

| Endpoint | Success | Failure |
| --- | --- | --- |
| `GET /healthz` | `200 ok`: process is serving HTTP | Independent of PostgreSQL availability |
| `GET /readyz` | `200 ready`: PostgreSQL ping succeeds | `503 not ready`: database unavailable, probe deadline reached, or shutdown requested |

```sh
curl -i http://127.0.0.1:8080/healthz
curl -i http://127.0.0.1:8080/readyz
```

Startup verifies PostgreSQL connectivity within `CONVOY_DB_CONNECT_TIMEOUT` before listening. Readiness uses the request context with `CONVOY_DB_QUERY_TIMEOUT`; it checks connectivity, not schema compatibility. Database outages make readiness fail while liveness continues to succeed. The pool can reconnect after PostgreSQL returns; the server does not run migrations automatically.

Logs are structured JSON on stderr, filtered by `CONVOY_LOG_LEVEL`. Connection strings and connection-string parse error causes are never logged. Probe responses contain no underlying database error details.

SIGINT or SIGTERM stops accepting connections and gives active HTTP requests `CONVOY_SHUTDOWN_TIMEOUT` to finish. On timeout the server closes active connections and exits unsuccessfully. A second signal uses normal signal termination. The database pool closes after HTTP shutdown. The HTTP deadline does not impose a strict bound on synchronous pool cleanup. HTTP headers have a five-second read deadline, request reads have a ten-second deadline, response writes have a thirty-second deadline, and idle keep-alive connections have a one-minute deadline.

## Jobs

Enqueue a job with a JSON request, then use the returned ID to read it:

```sh
curl -i http://127.0.0.1:8080/v1/jobs \
  -H 'Content-Type: application/json' \
  -d '{"kind":"noop","payload":{"message":"hello"}}'
curl -i http://127.0.0.1:8080/v1/jobs/JOB_UUID
```

`POST /v1/jobs` returns `201 Created`, a `Location` header, and the persisted job: `id`, `queue`, `kind`, `payload`, `state`, `created_at`, and `completed_at`. Queue is always `default`; new jobs are `available` with a null completion time. `GET /v1/jobs/{id}` returns the current record, 404 for an absent job, or 400 for an invalid UUID. Succeeded jobs remain inspectable with their completion timestamp.

The request must contain only `kind` and `payload`, with `Content-Type: application/json` and a body no larger than 1 MiB. Kind starts with an ASCII letter and contains at most 128 ASCII letters, digits, underscores, dots, or hyphens. Payload is required and may be any JSONB-supported JSON value, including JSON `null`. Unknown fields, malformed JSON, and unsupported JSONB values return 400; oversized bodies return 413 and unsupported content types return 415. JSONB can normalize formatting and object-key order. The API validates kind syntax; handler registration and handler-specific payload validation belong to the worker.

Enqueue uses one `INSERT ... RETURNING` in PostgreSQL's implicit transaction and fully checks the result before returning 201. A timeout or connection failure returns 503 with an uncertain-outcome message: the row may already have committed. Enqueue is not idempotent; retrying can create another job, even when the first HTTP response was lost. No automatic enqueue retry or compensation is performed. Job queries use the request context capped by `CONVOY_JOB_QUERY_TIMEOUT`, separately from readiness checks.

Jobs survive API process restarts because PostgreSQL owns their durable state. The schema permits `available`, `running`, and `succeeded`; only succeeded jobs have a non-null completion time. Database messages and details can include payload values, so job-operation logs include only static diagnostics and SQLSTATE where available.

## Worker

Apply migrations, then start one worker using the same `CONVOY_DATABASE_URL` as the server:

```sh
make migrate
go build -o bin/worker ./cmd/worker
./bin/worker
```

`make worker` runs the command through `go run`. Use the standalone binary for signal experiments. The worker connects directly to PostgreSQL and operates independently of the HTTP server. The supported configuration is one worker process.

The command registers `noop` (succeeds without external effects) and `sleep` (waits for a positive Go duration in its payload). Register application handlers in the command's `map[string]worker.Handler`. A handler receives its execution context and the claimed job and returns an error or nil. Treat the registry as immutable while the worker runs. For example:

```sh
curl -i http://127.0.0.1:8080/v1/jobs \
  -H 'Content-Type: application/json' \
  -d '{"kind":"sleep","payload":{"duration":"10s"}}'
```

The worker selects one available job from `default`, ordered by creation time and then UUID. A conditional `UPDATE ... RETURNING` commits `available → running` before execution. The handler runs synchronously, with no store transaction or borrowed store connection held. A nil handler result triggers a separate conditional update from `running → succeeded`, setting `completed_at`. Database operations use `CONVOY_JOB_QUERY_TIMEOUT`; that timeout does not limit handler execution. Empty polls wait 250ms.

Any claim, dispatch, handler, or completion failure stops the worker. Errors never trigger an automatic retry, requeue, or compensating update. A missing handler or handler error leaves the claimed job running. A database error may have an uncertain commit outcome. Inspect actual state before taking further action: `running` means claimed with no recorded terminal outcome, not proof that a worker is alive. Worker logs include job IDs, static operation names, and optional SQLSTATE; raw handler errors, payloads, and database error details are omitted.

SIGINT or SIGTERM cancels polling and the handler context. Handlers must cooperate with cancellation; a second signal forces termination. The worker does not drain active handlers or recover interrupted work. Cancellation during a job can leave it running, and cancellation during a database operation may leave an uncertain outcome. Normal signal cancellation exits successfully; other operation failures exit with status 1. A crashed worker's running job is not reclaimed when another worker starts. This behavior does not provide at-least-once execution after worker failures.

Integration tests verify committed claims, completion guards, deferred commit failures, cancellation, and access to the only pool connection and the claimed row while a handler is blocked. The process experiment enqueues 100 jobs, restarts the API, processes all 100, and reads their completed records through HTTP. It also restarts the API during a long job, kills the worker, and verifies that a new worker processes available work while leaving the interrupted job running.

## Migrations

Set `CONVOY_DATABASE_URL` to the target database, then run migrations separately from the server:

```sh
go run ./cmd/migrate up
go run ./cmd/migrate version
```

`make migrate` and `make migrate-version` run the same commands. The command only reads `CONVOY_DATABASE_URL`; server settings do not affect migration execution. Build a standalone binary with `go build -o bin/migrate ./cmd/migrate` when needed.

SQL files are embedded from `internal/migrate/migrations`. Version 1 is an unchanged no-op baseline (`SELECT 1`); version 2 creates the jobs table; version 3 adds execution states and completion time while preserving existing jobs. Apply migrations before starting updated server or worker binaries. Repeating `up` with no pending migrations succeeds. `version` reports the version and dirty flag, or `version=none dirty=false` on a fresh database. Initializing either command creates the migration metadata table if needed.

The command uses golang-migrate's pgx/v5 driver through `WithInstance`, pgx simple-query mode, and `MultiStatementEnabled: false`. Ordinary migration files contain no explicit transaction-control statements or operations that cannot run in a transaction: each whole file runs in PostgreSQL's implicit transaction. DSN parameters beginning with `x-` are rejected; migration driver options belong in code.

Failed migrations leave dirty metadata and block further `up` invocations. Inspect the actual database state before manual recovery; the command exposes neither `force` nor automatic repair. Dirty metadata alone does not prove whether SQL committed after a connection failure or forced interruption.

The first Ctrl-C requests a graceful stop after the current file and its version bookkeeping finish. Cancellation is checked before the next file is marked dirty, avoiding a race in the pinned library's `GracefulStop` implementation. Interrupted execution exits with status 130. After the first interrupt, normal signal handling is restored so a second Ctrl-C can force termination and leave dirty metadata. The driver adds no execution timeout; PostgreSQL's configured timeouts still apply. `CONVOY_DB_QUERY_TIMEOUT` is reserved for readiness checks. Graceful stop does not bound waits during initialization, locking, or cleanup.

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
| `CONVOY_JOB_QUERY_TIMEOUT` | `5s` | Deadline for each enqueue, lookup, claim, or completion database operation |
| `CONVOY_SHUTDOWN_TIMEOUT` | `10s` | HTTP shutdown deadline |

Timeouts must be positive Go durations, such as `500ms` or `5s`. The pool connection limit must be a positive 32-bit integer. Database connection-string syntax is validated by the database driver when connecting.
