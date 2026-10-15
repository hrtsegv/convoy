package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hrtsegv/convoy/internal/config"
)

// Open creates a pool and verifies connectivity using the caller's context.
// It reads only DatabaseURL and DBMaxConns from cfg. The caller supplies any
// startup deadline and owns the returned pool, including closing it.
// On failure, Open closes any pool it created and returns nil with the error.
func Open(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("open PostgreSQL pool: %w", err)
	}
	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		return nil, errors.New("PostgreSQL connection string is required")
	}

	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL pool configuration: %w", err)
	}
	poolConfig.MaxConns = cfg.DBMaxConns

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}

	return pool, nil
}
