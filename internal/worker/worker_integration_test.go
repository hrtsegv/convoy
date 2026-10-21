//go:build integration

package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hrtsegv/convoy/internal/config"
	"github.com/hrtsegv/convoy/internal/jobs"
	"github.com/hrtsegv/convoy/internal/postgres"
	"github.com/hrtsegv/convoy/internal/testdb"
)

func TestHandlerRunsAfterCommitWithoutHoldingConnectionOrLock(t *testing.T) {
	pool := workerPool(t)
	store := jobs.New(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	created := enqueue(t, ctx, store)
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	var logs bytes.Buffer
	go func() {
		done <- Run(ctx, store, map[string]Handler{"test": func(ctx context.Context, _ jobs.Job) error {
			close(started)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}}, time.Second, slog.New(slog.NewJSONHandler(&logs, nil)))
	}()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("handler did not start")
	}
	probeCtx, stopProbe := context.WithTimeout(ctx, time.Second)
	defer stopProbe()
	connection, err := pool.Acquire(probeCtx)
	if err != nil {
		t.Fatal("handler held the only pool connection")
	}
	defer connection.Release()
	tx, err := connection.Begin(probeCtx)
	if err != nil {
		t.Fatal("could not inspect claim from another transaction")
	}
	defer tx.Rollback(ctx)
	var state string
	if err := tx.QueryRow(probeCtx, "SELECT state FROM jobs WHERE id = $1 FOR UPDATE NOWAIT", created.ID).Scan(&state); err != nil || state != "running" {
		t.Fatal("handler started before the claim committed and released its lock")
	}
	if err := tx.Rollback(probeCtx); err != nil {
		t.Fatal("could not release inspection lock")
	}
	connection.Release()
	close(release)
	for {
		job, err := store.Get(ctx, created.ID)
		if err != nil {
			t.Fatal("could not inspect handler completion")
		}
		if job.State == "succeeded" && job.CompletedAt != nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("handler did not complete")
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Log("while handler was blocked, the only pool connection was free and another transaction locked the committed running row")
}

func TestWorkerStopsAtFailureBoundary(t *testing.T) {
	for _, operation := range []string{"claim", "dispatch", "execute", "complete"} {
		t.Run(operation, func(t *testing.T) {
			pool := workerPool(t)
			store := jobs.New(pool)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			first := enqueue(t, ctx, store)
			second := enqueue(t, ctx, store)
			if operation == "claim" || operation == "complete" {
				state := "running"
				if operation == "complete" {
					state = "succeeded"
				}
				_, err := pool.Exec(ctx, fmt.Sprintf(`
					CREATE FUNCTION reject_transition() RETURNS trigger LANGUAGE plpgsql AS $$
					BEGIN
						IF NEW.state = '%s' THEN
							RAISE EXCEPTION 'secret-handler-payload' USING ERRCODE = '23514', DETAIL = NEW.payload::text;
						END IF;
						RETURN NEW;
					END $$;
					CREATE CONSTRAINT TRIGGER reject_transition AFTER UPDATE ON jobs
					DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_transition();`, state))
				if err != nil {
					t.Fatal("could not install deferred transition failure")
				}
			}
			handlerErr := errors.New("secret-handler-payload")
			calls := 0
			handlers := map[string]Handler{"test": func(context.Context, jobs.Job) error {
				calls++
				if operation == "execute" {
					return handlerErr
				}
				return nil
			}}
			if operation == "dispatch" {
				delete(handlers, "test")
			}
			var logs bytes.Buffer
			err := Run(ctx, store, handlers, time.Second, slog.New(slog.NewJSONHandler(&logs, nil)))
			if err == nil || ctx.Err() != nil {
				t.Fatal("worker must stop promptly at its first failure")
			}
			wantCalls := 1
			if operation == "claim" || operation == "dispatch" {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("handler calls=%d; want %d", calls, wantCalls)
			}
			if operation == "execute" && !errors.Is(err, handlerErr) {
				t.Fatal("handler error cause was lost")
			}
			if operation == "dispatch" && !errors.Is(err, ErrUnknownKind) {
				t.Fatal("missing handler must return ErrUnknownKind")
			}
			if operation == "claim" || operation == "complete" {
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
					t.Fatal("deferred SQL failure cause was lost")
				}
			}
			wantState := "running"
			if operation == "claim" {
				wantState = "available"
			}
			read, err := store.Get(ctx, first.ID)
			if err != nil || read.State != wantState || read.CompletedAt != nil {
				t.Fatal("unexpected durable state after failed operation")
			}
			read, err = store.Get(ctx, second.ID)
			if err != nil || read.State != "available" {
				t.Fatal("worker continued claiming after failure")
			}
			if strings.Contains(logs.String(), "secret-handler-payload") || !strings.Contains(logs.String(), `"operation":"`+operation+`"`) {
				t.Fatal("failure logging must identify the operation without exposing error text")
			}
		})
	}
}

func TestWorkerClaimDeadlineDoesNotInvokeHandler(t *testing.T) {
	pool := workerPool(t)
	store := jobs.New(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	created := enqueue(t, ctx, store)
	held, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal("could not occupy pool")
	}
	defer held.Release()
	calls := 0
	var logs bytes.Buffer
	err = Run(ctx, store, map[string]Handler{"test": func(context.Context, jobs.Job) error {
		calls++
		return nil
	}}, 25*time.Millisecond, slog.New(slog.NewJSONHandler(&logs, nil)))
	if !errors.Is(err, context.DeadlineExceeded) || calls != 0 {
		t.Fatal("claim deadline must stop the worker without invoking a handler")
	}
	held.Release()
	read, err := store.Get(ctx, created.ID)
	if err != nil || read.State != "available" {
		t.Fatal("pool acquisition timeout must not claim a job")
	}
}

func TestWorkerCancellationLeavesRunningJob(t *testing.T) {
	pool := workerPool(t)
	store := jobs.New(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first := enqueue(t, ctx, store)
	second := enqueue(t, ctx, store)
	workerCtx, stop := context.WithCancel(ctx)
	defer stop()
	started := make(chan struct{})
	done := make(chan error, 1)
	var logs bytes.Buffer
	go func() {
		done <- Run(workerCtx, store, map[string]Handler{"test": func(ctx context.Context, _ jobs.Job) error {
			if ctx.Err() != nil {
				return errors.New("handler inherited the cancelled claim context")
			}
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}}, time.Second, slog.New(slog.NewJSONHandler(&logs, nil)))
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("handler did not receive a live execution context")
	}
	stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("worker must preserve the handler cancellation cause")
		}
	case <-ctx.Done():
		t.Fatal("worker did not stop after handler cancellation")
	}
	read, err := store.Get(ctx, first.ID)
	if err != nil || read.State != "running" || read.CompletedAt != nil {
		t.Fatal("cancelled handler must leave the claimed job running")
	}
	read, err = store.Get(ctx, second.ID)
	if err != nil || read.State != "available" {
		t.Fatal("cancelled worker must not start the next job")
	}
}

func workerPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := postgres.Open(ctx, config.Config{DatabaseURL: testdb.New(t), DBMaxConns: 1})
	if err != nil {
		t.Fatal("could not open worker test pool")
	}
	t.Cleanup(pool.Close)
	return pool
}

func enqueue(t *testing.T, ctx context.Context, store *jobs.Store) jobs.Job {
	t.Helper()
	job, err := store.Enqueue(ctx, "test", []byte(`{"secret":"secret-handler-payload"}`))
	if err != nil {
		t.Fatal("could not prepare worker test job")
	}
	return job
}
