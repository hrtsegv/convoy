package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/hrtsegv/convoy/internal/jobs"
)

const maxJobRequestBytes = 1 << 20

func registerJobRoutes(mux *http.ServeMux, lifecycle context.Context, store *jobs.Store, queryTimeout time.Duration, logger *slog.Logger) {
	mux.HandleFunc("POST /v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		if lifecycle.Err() != nil {
			jobError(w, http.StatusServiceUnavailable, "server is shutting down")
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			jobError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxJobRequestBytes)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var request struct {
			Kind    string          `json:"kind"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := decoder.Decode(&request); err != nil {
			jobBodyError(w, err)
			return
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			jobBodyError(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
		defer cancel()
		job, err := store.Enqueue(ctx, request.Kind, request.Payload)
		switch {
		case errors.Is(err, jobs.ErrInvalidKind):
			jobError(w, http.StatusBadRequest, jobs.ErrInvalidKind.Error())
		case errors.Is(err, jobs.ErrInvalidPayload):
			jobError(w, http.StatusBadRequest, jobs.ErrInvalidPayload.Error())
		case err != nil:
			logJobError(logger, "enqueue", err)
			jobError(w, http.StatusServiceUnavailable, "could not confirm job creation; outcome may be unknown; retrying may create another job")
		default:
			w.Header().Set("Location", "/v1/jobs/"+job.ID)
			jobJSON(w, http.StatusCreated, job)
		}
	})
	mux.HandleFunc("GET /v1/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		if lifecycle.Err() != nil {
			jobError(w, http.StatusServiceUnavailable, "server is shutting down")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
		defer cancel()
		job, err := store.Get(ctx, r.PathValue("id"))
		switch {
		case errors.Is(err, jobs.ErrInvalidID):
			jobError(w, http.StatusBadRequest, jobs.ErrInvalidID.Error())
		case errors.Is(err, jobs.ErrNotFound):
			jobError(w, http.StatusNotFound, jobs.ErrNotFound.Error())
		case err != nil:
			logJobError(logger, "get", err)
			jobError(w, http.StatusServiceUnavailable, "job lookup is unavailable")
		default:
			jobJSON(w, http.StatusOK, job)
		}
	})
}

func jobBodyError(w http.ResponseWriter, err error) {
	var sizeErr *http.MaxBytesError
	if errors.As(err, &sizeErr) {
		jobError(w, http.StatusRequestEntityTooLarge, "request body exceeds 1 MiB")
		return
	}
	jobError(w, http.StatusBadRequest, "body must contain one JSON object with only kind and payload fields")
}

func jobError(w http.ResponseWriter, status int, message string) {
	jobJSON(w, status, map[string]string{"error": message})
}

func jobJSON(w http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(append(data, '\n'))
}

func logJobError(logger *slog.Logger, operation string, err error) {
	// PostgreSQL messages and details can contain row values, including payloads.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		logger.Error("job database operation failed", "operation", operation, "sqlstate", pgErr.Code)
		return
	}
	logger.Error("job database operation failed", "operation", operation)
}
