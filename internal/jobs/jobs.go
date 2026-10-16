package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInvalidKind    = errors.New("kind must start with a letter and contain at most 128 letters, digits, underscores, dots, or hyphens")
	ErrInvalidPayload = errors.New("payload must be a JSON value supported by PostgreSQL JSONB")
	ErrInvalidID      = errors.New("job ID must be a UUID")
	ErrNotFound       = errors.New("job not found")
	kindPattern       = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)
)

type Job struct {
	ID        string          `json:"id"`
	Queue     string          `json:"queue"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
	State     string          `json:"state"`
	CreatedAt time.Time       `json:"created_at"`
}

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Enqueue executes one insert in PostgreSQL's implicit transaction. Scan closes
// the result and checks its final error before success is reported. Any error
// returns a zero Job, but connection loss can still leave a committed insert.
func (s *Store) Enqueue(ctx context.Context, kind string, payload json.RawMessage) (Job, error) {
	if !kindPattern.MatchString(kind) {
		return Job{}, ErrInvalidKind
	}
	if !utf8.Valid(payload) || !json.Valid(payload) {
		return Job{}, ErrInvalidPayload
	}
	var job Job
	err := s.pool.QueryRow(ctx, `
		INSERT INTO jobs (kind, payload) VALUES ($1, $2)
		RETURNING id, queue, kind, payload, state, created_at`, kind, payload).
		Scan(&job.ID, &job.Queue, &job.Kind, &job.Payload, &job.State, &job.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && (pgErr.Code == "22P02" || pgErr.Code == "22P05" || pgErr.Code == "22003") {
			err = errors.Join(ErrInvalidPayload, err)
		}
		return Job{}, fmt.Errorf("enqueue job: %w", err)
	}
	job.CreatedAt = job.CreatedAt.UTC()
	return job, nil
}

func (s *Store) Get(ctx context.Context, id string) (Job, error) {
	var uuid pgtype.UUID
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return Job{}, ErrInvalidID
	}
	if err := uuid.Scan(id); err != nil {
		return Job{}, ErrInvalidID
	}
	var job Job
	err := s.pool.QueryRow(ctx, `
		SELECT id, queue, kind, payload, state, created_at FROM jobs WHERE id = $1`, uuid).
		Scan(&job.ID, &job.Queue, &job.Kind, &job.Payload, &job.State, &job.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = errors.Join(ErrNotFound, err)
		}
		return Job{}, fmt.Errorf("get job: %w", err)
	}
	job.CreatedAt = job.CreatedAt.UTC()
	return job, nil
}
