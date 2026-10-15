# Convoy: repository guidance

## Purpose and working style

Convoy is a Go-first, PostgreSQL-backed durable job execution system under development. The eventual delivery model is at least once; arbitrary external side effects are not guaranteed to happen exactly once. PostgreSQL is the authoritative durable store.

This project develops the owner's backend, Go, PostgreSQL, distributed-systems, reliability, and infrastructure skills. For educational decisions, explain the problem, concepts, approaches, tradeoffs, and recommendation; let the owner decide before implementation. Invite their reasoning or first implementation. When they delegate implementation, follow the agreed contract.

Involve the owner heavily in transaction boundaries, locking, claiming, leases, fencing, ACK/NACK, retries, idempotency, concurrency, recovery, backpressure, and coordination. Mechanical work such as formatting, boilerplate, Makefile targets, and simple CI can proceed within the authorized task. Work incrementally and honor review checkpoints and component/phase boundaries.

## Current scope

Phase 0: repository and engineering baseline, still in progress.

- Implemented: Go module, environment configuration, PostgreSQL pool bootstrap, unit tests, opt-in bootstrap integration tests, development commands, and basic CI.
- Pending: migration command and baseline migration, server lifecycle, HTTP health/readiness skeleton, and repeatable integration-test database orchestration.
- Next learning checkpoint: migration version tracking, transaction boundaries, and failure behavior before implementing the migration command.

Phase 0 ends when a fresh database can migrate, the server starts and passes readiness, and one command runs unit and integration tests. Integration testing is currently separate.

Do not add job tables, enqueue APIs, workers, claiming, leases, retries, or scheduling in Phase 0. Kubernetes, KEDA, Prometheus, brokers, multiple control-plane nodes, partitioning, and sharding belong to later work. Keep application containerization and worker scaling for their own milestone.

## Repository map

- `go.mod`: Go 1.27.2 baseline and pgx v5 dependency.
- `internal/config`: reads and validates `CONVOY_*` environment variables. See `README.md` for settings and defaults.
- `internal/postgres`: concrete pool bootstrap and its unit/integration tests.
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

Run checks appropriate to the change and report what actually ran, including any unavailable checks. Define expected behavior before failure experiments and explain observed results.

## Git workflow

- Never add `Co-authored-by` trailers or AI attribution to commit messages. Preserve the configured Git author and committer identity.
- Stage, commit, or push only when the owner explicitly authorizes the action. Commit authorization does not imply push authorization.
- Stage explicit paths, respect Git ignore rules, and never force-add ignored files. Review the staged diff before committing.
- Keep commits small and logical, with messages describing the actual change. Preserve unrelated owner edits.
