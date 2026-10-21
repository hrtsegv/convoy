package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/hrtsegv/convoy/internal/jobs"
)

type Handler func(context.Context, jobs.Job) error

var ErrUnknownKind = errors.New("no handler registered for job kind")

// Run executes one job at a time. The registry must not change while Run is active.
// Cancellation stops polling and is passed to the current handler. Failed or
// interrupted execution can leave running jobs; Run never retries or reclaims them.
func Run(ctx context.Context, store *jobs.Store, handlers map[string]Handler, queryTimeout time.Duration, logger *slog.Logger) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		queryCtx, cancel := context.WithTimeout(ctx, queryTimeout)
		job, err := store.ClaimNext(queryCtx)
		cancel()
		if errors.Is(err, jobs.ErrNoAvailableJob) {
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
				continue
			}
		}
		if err != nil {
			logFailure(logger, "claim", "", err)
			return err
		}
		logger.Info("job claimed", "job_id", job.ID)
		handler := handlers[job.Kind]
		if handler == nil {
			logFailure(logger, "dispatch", job.ID, ErrUnknownKind)
			return fmt.Errorf("dispatch job %s: %w", job.ID, ErrUnknownKind)
		}
		// A claim may finish just as cancellation arrives. Do not start new work.
		if err := ctx.Err(); err != nil {
			logFailure(logger, "execute", job.ID, err)
			return fmt.Errorf("execute job %s: %w", job.ID, err)
		}
		if err := handler(ctx, job); err != nil {
			logFailure(logger, "execute", job.ID, err)
			return fmt.Errorf("execute job %s: %w", job.ID, err)
		}
		queryCtx, cancel = context.WithTimeout(ctx, queryTimeout)
		err = store.Complete(queryCtx, job.ID)
		cancel()
		if err != nil {
			logFailure(logger, "complete", job.ID, err)
			return fmt.Errorf("complete job %s: %w", job.ID, err)
		}
		logger.Info("job succeeded", "job_id", job.ID)
	}
}

func logFailure(logger *slog.Logger, operation, id string, err error) {
	attrs := []any{"operation", operation, "job_id", id}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		attrs = append(attrs, "sqlstate", pgErr.Code)
	}
	// Handler and database error text can contain arbitrary payloads or secrets.
	logger.Error("worker stopped after job operation failed", attrs...)
}
