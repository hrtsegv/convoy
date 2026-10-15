package postgres_test

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/hrtsegv/convoy/internal/config"
	"github.com/hrtsegv/convoy/internal/postgres"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOpenRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name       string
		cfg        config.Config
		parseError bool
		wrapped    bool
	}{
		{name: "missing connection string", cfg: config.Config{DBMaxConns: 1}},
		{name: "blank connection string", cfg: config.Config{DatabaseURL: " \t", DBMaxConns: 1}},
		{
			name:       "malformed connection string",
			cfg:        config.Config{DatabaseURL: "postgres://localhost:invalid/test", DBMaxConns: 1},
			parseError: true,
			wrapped:    true,
		},
		{
			name:    "zero connection limit",
			cfg:     config.Config{DatabaseURL: "postgres://localhost/test", DBMaxConns: 0},
			wrapped: true,
		},
		{
			name:    "negative connection limit",
			cfg:     config.Config{DatabaseURL: "postgres://localhost/test", DBMaxConns: -1},
			wrapped: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			pool, err := postgres.Open(ctx, test.cfg)
			if pool != nil {
				pool.Close()
				t.Fatal("failure returned a non-nil pool")
			}
			if err == nil {
				t.Fatal("expected invalid configuration to fail")
			}
			if test.wrapped && errors.Unwrap(err) == nil {
				t.Error("expected the underlying driver error to be wrapped")
			}
			if test.parseError {
				var parseErr *pgconn.ParseConfigError
				if !errors.As(err, &parseErr) {
					t.Error("expected the driver parse error to remain inspectable")
				}
			}
		})
	}
}

func TestOpenRejectsFinishedContext(t *testing.T) {
	tests := []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		want error
	}{
		{
			name: "cancelled",
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			want: context.Canceled,
		},
		{
			name: "expired deadline",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			},
			want: context.DeadlineExceeded,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := test.ctx()
			defer cancel()

			pool, err := postgres.Open(ctx, config.Config{
				DatabaseURL: "postgres://localhost/test",
				DBMaxConns:  1,
			})
			if pool != nil {
				pool.Close()
				t.Fatal("failure returned a non-nil pool")
			}
			if !errors.Is(err, test.want) {
				t.Errorf("expected %v, got %v", test.want, err)
			}
		})
	}
}

func TestOpenClosesConnectionOnCancelledStartup(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type result struct {
		pool *pgxpool.Pool
		err  error
	}
	done := make(chan result, 1)
	go func() {
		pool, err := postgres.Open(ctx, config.Config{
			DatabaseURL: "postgres://test:test@" + listener.Addr().String() + "/test?sslmode=disable",
			DBMaxConns:  1,
		})
		done <- result{pool: pool, err: err}
	}()

	// Accept TCP but withhold the PostgreSQL handshake so startup stays in flight.
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	cancel()

	select {
	case got := <-done:
		if got.pool != nil {
			got.pool.Close()
			t.Fatal("cancelled startup returned a non-nil pool")
		}
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("expected wrapped context cancellation, got %v", got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Open did not finish after startup cancellation")
	}

	// Draining to EOF verifies failure cleanup closed the client connection.
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, conn); err != nil {
		t.Fatalf("client connection remained open after failed startup: %v", err)
	}
}
