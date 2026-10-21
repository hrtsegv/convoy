//go:build integration

package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestClaimOrderAndCompletionGuards(t *testing.T) {
	pool := integrationPool(t)
	store := New(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Equal timestamps exercise the UUID tie-breaker. Other queues and states
	// are deliberately older, so ordering alone cannot filter them out.
	_, err := pool.Exec(ctx, `
		INSERT INTO jobs (id, queue, kind, payload, state, created_at, completed_at) VALUES
		('00000000-0000-0000-0000-000000000003', 'default', 'noop', '{}', 'available', '2026-01-02', NULL),
		('00000000-0000-0000-0000-000000000002', 'default', 'noop', '{}', 'available', '2026-01-01', NULL),
		('00000000-0000-0000-0000-000000000001', 'default', 'noop', '{}', 'available', '2026-01-01', NULL),
		('00000000-0000-0000-0000-000000000004', 'other', 'noop', '{}', 'available', '2025-01-01', NULL),
		('00000000-0000-0000-0000-000000000005', 'default', 'noop', '{}', 'running', '2025-01-01', NULL),
		('00000000-0000-0000-0000-000000000006', 'default', 'noop', '{}', 'succeeded', '2025-01-01', now())`)
	if err != nil {
		t.Fatal("could not prepare ordered jobs")
	}
	firstID := "00000000-0000-0000-0000-000000000001"
	if err := store.Complete(ctx, firstID); !errors.Is(err, ErrNotRunning) {
		t.Fatal("completion must reject an unclaimed job")
	}
	for _, id := range []string{firstID, "00000000-0000-0000-0000-000000000002", "00000000-0000-0000-0000-000000000003"} {
		job, err := store.ClaimNext(ctx)
		if err != nil || job.ID != id || job.State != "running" || job.CompletedAt != nil {
			t.Fatal("claim did not return the next available default-queue job")
		}
		read, err := store.Get(ctx, id)
		if err != nil || read.State != "running" {
			t.Fatal("claim must be committed before it returns")
		}
		if err := store.Complete(ctx, id); err != nil {
			t.Fatal("could not complete the running job")
		}
		read, err = store.Get(ctx, id)
		if err != nil || read.State != "succeeded" || read.CompletedAt == nil || read.CompletedAt.IsZero() {
			t.Fatal("completed job must remain inspectable with its completion time")
		}
		completedAt := *read.CompletedAt
		if err := store.Complete(ctx, id); !errors.Is(err, ErrNotRunning) {
			t.Fatal("completion must reject a job that already succeeded")
		}
		read, err = store.Get(ctx, id)
		if err != nil || read.CompletedAt == nil || !read.CompletedAt.Equal(completedAt) {
			t.Fatal("rejected completion must preserve the original completion time")
		}
	}
	job, err := store.ClaimNext(ctx)
	if !errors.Is(err, ErrNoAvailableJob) || job.ID != "" {
		t.Fatal("running, succeeded, and other-queue jobs must not be claimed")
	}
	if err := store.Complete(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrNotRunning) {
		t.Fatal("completion must reject a missing job")
	}
}

func TestExecutionOperationsRespectCancellation(t *testing.T) {
	store := New(integrationPool(t))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	created, err := store.Enqueue(ctx, "noop", []byte(`{}`))
	if err != nil {
		t.Fatal("could not enqueue job")
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	job, err := store.ClaimNext(cancelled)
	if !errors.Is(err, context.Canceled) || job.ID != "" {
		t.Fatal("cancelled claim must return no job and preserve its cause")
	}
	job, err = store.ClaimNext(ctx)
	if err != nil || job.ID != created.ID {
		t.Fatal("cancelled claim unexpectedly changed the available job")
	}
	if err := store.Complete(cancelled, job.ID); !errors.Is(err, context.Canceled) {
		t.Fatal("completion must preserve caller cancellation")
	}
	read, err := store.Get(ctx, job.ID)
	if err != nil || read.State != "running" || read.CompletedAt != nil {
		t.Fatal("pre-cancelled completion must not change the running job")
	}
}
