# Convoy: repository guidance

## Purpose and working style

Convoy is a Go-first, PostgreSQL-backed durable job execution system under development. The eventual delivery model is at least once; arbitrary external side effects are not guaranteed to happen exactly once. PostgreSQL is the authoritative durable store.

This project develops the owner's backend, Go, PostgreSQL, distributed-systems, reliability, and infrastructure skills. Optimize for learning value and forward progress. Use established patterns and reasonable defaults for conventional infrastructure, including migrations, CLI wiring, configuration, CI, health endpoints, logging, and basic server lifecycle, without stopping for every small decision.

Pause for decisions that materially affect correctness or architecture, PostgreSQL concurrency or transaction semantics, the distributed-systems topics below, or educationally important tradeoffs. Explain the problem, concepts, approaches, tradeoffs, and recommendation; let the owner decide before implementation. Invite their reasoning or first implementation. When they delegate implementation, follow the agreed contract. Distinguish discussions as "must understand now" (correctness or important learning), "good to know" (brief, nonblocking), and "later concern" (record and continue).

Involve the owner heavily in transaction boundaries, locking, claiming, leases, fencing, ACK/NACK, retries, idempotency, concurrency, recovery, backpressure, and coordination. Mechanical work such as formatting, boilerplate, Makefile targets, and simple CI can proceed within the authorized task. Work incrementally and honor review checkpoints and component/phase boundaries.

## Current scope

Phase 0: repository and engineering baseline, still in progress.

- Implemented: Go module, environment configuration, PostgreSQL pool bootstrap, migration command with embedded no-op baseline, server lifecycle and HTTP health/readiness skeleton, unit and opt-in integration tests, development commands, and basic CI.
- Pending: repeatable integration-test database orchestration.
- Server lifecycle and HTTP health/readiness skeleton reviewed and approved. Next component: repeatable integration-test database orchestration. Migration command is reviewed and committed.

Phase 0 ends when a fresh database can migrate, the server starts and passes readiness, and one command runs unit and integration tests. Integration testing is currently separate.

Do not add job tables, enqueue APIs, workers, claiming, leases, retries, or scheduling in Phase 0. Kubernetes, KEDA, Prometheus, brokers, multiple control-plane nodes, partitioning, and sharding belong to later work. Keep application containerization and worker scaling for their own milestone.

## Repository map

- `go.mod`: Go 1.27.2 baseline and pgx v5 dependency.
- `internal/config`: reads and validates `CONVOY_*` environment variables. See `README.md` for settings and defaults.
- `internal/postgres`: concrete pool bootstrap and its unit/integration tests.
- `cmd/migrate`: `up` and `version` command; reads only `CONVOY_DATABASE_URL`, handles interrupts, and classifies parse errors before logging.
- `internal/migrate`: concrete migration runner, embedded SQL files, and tests for atomicity, dirty state, graceful stopping, and initialization cleanup.
- `cmd/server`: configuration, structured logging, signal handling, startup connection deadline, and pool ownership.
- `internal/server`: HTTP probes and graceful HTTP lifecycle using `net/http`; no queue APIs.
- `Makefile`: local formatting, vet, build, and test commands.
- `.github/workflows/ci.yml`: selects Go from `go.mod`; checks formatting, vet, build, and tests with the race detector. PostgreSQL integration tests are not yet wired into CI.

Update this overview as approved work changes it. Read the relevant implementation and tests before editing.

## PostgreSQL bootstrap contract

`postgres.Open(ctx, cfg)` accepts the existing `config.Config` and reads only `DatabaseURL` and `DBMaxConns`.

- The caller owns the startup context/deadline; the helper must not apply `DBConnectTimeout` or create a startup timeout.
- Create the pool and verify connectivity with `Ping(ctx)`. Every failure returns a nil pool and an error; after creation, failure cleanup closes the pool synchronously.
- Wrap underlying errors appropriately for `errors.Is` and `errors.As`.
- Before formatting errors at startup or test logging boundaries, classify wrapped `*pgconn.ParseConfigError` values with `errors.As`. For parse failures, emit a static configuration-error message; never log the error text, its causes, or `ConnString`. Driver password redaction is best-effort. Preserve the error chain for programmatic inspection.
- Success transfers ownership, including `Close`, to the caller. Cancelling the startup context afterward does not close the pool; later operations use their own contexts.
- `Close` has no context parameter, so startup deadlines are not strict cleanup-time bounds.

Prefer concrete code and explicit ownership. Add abstractions only for a demonstrated need; avoid generic storage layers and interfaces created solely for mocks. Keep SQL visible, transactions short, and handler work outside database transactions when execution is introduced. Do not log connection strings, credentials, or arbitrary job payloads.

## Migration execution contract

Verified with golang-migrate v4.20.1 and pgx v5.11.0: use the `database/pgx/v5` driver through `WithInstance`, a pgx `stdlib.OpenDB` connection, `QueryExecModeSimpleProtocol`, and `MultiStatementEnabled: false`. Ordinary files are sent as one query, which PostgreSQL executes in an implicit transaction. Do not put explicit transaction-control statements or non-transactional operations in ordinary migration files.

The command rejects DSN parameters beginning with `x-`; driver options belong in code. Splitting statements is only a negative control in tests and demonstrates partial committed state after failure.

After a SQL error in an ordinary migration, its SQL changes roll back but the separately recorded version remains dirty. Connection loss or forced termination can leave uncertain completion, so never infer rollback from dirty metadata alone. A new `up` invocation must refuse to proceed. Never automatically clear dirty state or force a version; recovery requires inspecting the actual schema. The command exposes `up` and `version`, runs separately from the server, and treats no pending migrations as success. Version 1 is a no-op baseline with no application tables.

The first Ctrl-C cancels the command context; a small driver adapter checks it before marking the next file dirty. The current file and clean-version bookkeeping finish before stopping. Do not use the upstream `GracefulStop` channel: v4.20.1 has a reproduced data race on that path. The adapter preserves the library's full-run advisory lock and SQL execution behavior. Interrupted execution returns status 130. The second Ctrl-C uses normal signal termination. Driver `StatementTimeout` is explicitly zero, and readiness timeout configuration is not reused. Initialization, locking, and cleanup are not bounded by graceful stop. Later concern: revisit a dedicated per-file execution timeout before substantive migrations; do not expand migration plumbing without a concrete need.

## Server lifecycle contract

The application applies `DBConnectTimeout` to a startup context, opens and verifies the pool, and cancels that context immediately afterward. It retains pool ownership until HTTP shutdown completes. Migrations run separately.

`GET /healthz` checks process liveness without PostgreSQL. `GET /readyz` pings the concrete pool with a request-owned context capped by `DBQueryTimeout`, returning 503 on failure or shutdown. Responses contain no database error details. Later concern: schema compatibility checks when application tables are introduced; Phase 0 readiness checks connectivity only.

The first SIGINT or SIGTERM cancels the application lifecycle and restores default handling for a second signal. HTTP shutdown uses a fresh background context with `ShutdownTimeout`, allowing in-flight requests to drain rather than immediately cancelling them. A shutdown deadline force-closes connections and returns an error. Pool cleanup remains synchronous and is not strictly bounded by the HTTP shutdown timeout.

Use JSON `slog` output with configured severity. Classify DSN parse errors before formatting them, including their underlying causes. Do not log the whole configuration.

## Verification

```sh
make check
make test-race
```

For PostgreSQL bootstrap integration testing, set `CONVOY_TEST_DATABASE_URL` to a running test database, then run:

```sh
go test -race -tags=integration ./internal/postgres -v
```

The integration tag requires the test database URL; a missing value fails rather than skipping. Use real PostgreSQL for database semantics. The stalled TCP unit test covers startup cancellation and connection cleanup.

Run migration-driver verification against a PostgreSQL test URL with permission to create schemas:

```sh
go test -race -tags=integration ./internal/migrate ./cmd/migrate -v
```

These tests create and drop their own isolated schemas. They verify rollback on statement 2 failure, dirty-version persistence across runner sessions, refusal to retry dirty migrations, the embedded baseline from zero, command behavior, graceful stop, and cleanup after failed initialization.

Run all PostgreSQL integration tests, including HTTP readiness and request deadline checks, with:

```sh
go test -race -tags=integration ./... -v
```

For stop/start experiments, keep the database endpoint stable; Docker can reassign dynamically published host ports on restart.

Run checks appropriate to the change and report what actually ran, including any unavailable checks. Define expected behavior before failure experiments and explain observed results.

## Git workflow

- Never add `Co-authored-by` trailers or AI attribution to commit messages. Preserve the configured Git author and committer identity.
- After the owner approves changes, stage and commit that approved set automatically; this is standing commit authorization, so no separate commit request is needed. Preserve unrelated edits and leave unreviewed work out of the commit.
- Push only when explicitly authorized. Approval and commit authorization do not imply push authorization.
- Stage explicit paths, respect Git ignore rules, and never force-add ignored files. Review the staged diff before committing.
- Keep commits small and logical, with messages describing the actual change. Preserve unrelated owner edits.
