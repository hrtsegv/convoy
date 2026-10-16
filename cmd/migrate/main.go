package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"

	gomigrate "github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/hrtsegv/convoy/internal/migrate"
)

func main() {
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-interrupts:
			signal.Stop(interrupts)
			fmt.Fprintln(os.Stderr, "interrupt requested; waiting for the current operation; press Ctrl-C again to force exit")
			cancel()
		case <-ctx.Done():
		}
	}()
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	signal.Stop(interrupts)
	cancel()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprintln(stdout, "usage: migrate <up|version>")
		return 0
	}
	if len(args) != 1 || (args[0] != "up" && args[0] != "version") {
		fmt.Fprintln(stderr, "usage: migrate <up|version>")
		return 2
	}
	if ctx.Err() != nil {
		return 130
	}
	runner, err := migrate.Open(ctx, os.Getenv("CONVOY_DATABASE_URL"))
	if err != nil {
		reportError(stderr, err)
		if ctx.Err() != nil {
			return 130
		}
		return 1
	}
	code := execute(ctx, runner, args[0], stdout, stderr)
	sourceErr, databaseErr := runner.Close()
	if sourceErr != nil || databaseErr != nil {
		fmt.Fprintln(stderr, "could not close migration resources")
		if code == 0 {
			code = 1
		}
	}
	if ctx.Err() != nil {
		return 130
	}
	return code
}

func execute(ctx context.Context, runner *gomigrate.Migrate, command string, stdout, stderr io.Writer) int {
	if ctx.Err() != nil {
		return 130
	}
	if command == "version" {
		version, dirty, err := runner.Version()
		if errors.Is(err, gomigrate.ErrNilVersion) {
			fmt.Fprintln(stdout, "version=none dirty=false")
		} else if err != nil {
			reportError(stderr, err)
			return 1
		} else {
			fmt.Fprintf(stdout, "version=%d dirty=%t\n", version, dirty)
		}
		if ctx.Err() != nil {
			return 130
		}
		return 0
	}

	err := runner.Up()
	if err != nil && !errors.Is(err, gomigrate.ErrNoChange) {
		if !errors.Is(err, ctx.Err()) {
			reportError(stderr, err)
		}
		if ctx.Err() != nil {
			fmt.Fprintln(stderr, "migration interrupted; pending migrations may remain")
			return 130
		}
		return 1
	}
	if ctx.Err() != nil {
		fmt.Fprintln(stderr, "migration interrupted; pending migrations may remain")
		return 130
	}
	if errors.Is(err, gomigrate.ErrNoChange) {
		fmt.Fprintln(stdout, "no pending migrations")
	} else {
		fmt.Fprintln(stdout, "migrations applied")
	}
	return 0
}

func reportError(stderr io.Writer, err error) {
	var parseErr *pgconn.ParseConfigError
	var dirtyErr gomigrate.ErrDirty
	switch {
	case errors.As(err, &parseErr):
		fmt.Fprintln(stderr, "invalid PostgreSQL connection configuration")
	case errors.As(err, &dirtyErr):
		fmt.Fprintf(stderr, "migration version %d is dirty; inspect the database and recover manually before retrying\n", dirtyErr.Version)
	default:
		fmt.Fprintf(stderr, "migration failed: %v\n", err)
	}
}
