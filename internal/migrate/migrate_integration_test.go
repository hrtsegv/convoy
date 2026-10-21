//go:build integration

package migrate

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"testing/fstest"
	"time"

	gomigrate "github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestMigrationDriverAtomicityAndDirtyState(t *testing.T) {
	tests := []struct {
		name          string
		split         bool
		wantTableLeft bool
	}{
		{name: "whole file rolls back", split: false, wantTableLeft: false},
		{name: "splitting leaves partial state", split: true, wantTableLeft: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsn, observer, schema := isolatedSchema(t)
			files := fstest.MapFS{
				"000001_initial.up.sql": {Data: []byte("CREATE TABLE version_one (id integer);")},
				"000002_failure.up.sql": {Data: []byte("CREATE TABLE atomic_probe (id integer); SELECT 1 / 0;")},
			}
			runner := migrationRunner(t, dsn, files, test.split)
			t.Cleanup(func() { closeRunner(t, runner) })

			if err := runner.Steps(1); err != nil {
				t.Fatal("could not apply initial migration")
			}
			assertVersion(t, runner, 1, false)

			err := runner.Up()
			var migrationErr database.Error
			if !errors.As(err, &migrationErr) {
				t.Fatal("expected the second migration to return a database execution error")
			}
			var pgErr *pgconn.PgError
			if !errors.As(migrationErr.OrigErr, &pgErr) || pgErr.Code != "22012" {
				t.Fatal("expected division by zero in statement 2, not a parsing or setup error")
			}

			assertVersion(t, runner, 2, true)
			assertTableExists(t, observer, schema, "version_one", true)
			assertTableExists(t, observer, schema, "atomic_probe", test.wantTableLeft)
			t.Logf("statement 2 failed; statement 1 remains=%t; version=2 dirty=true", test.wantTableLeft)

			// Reopening proves dirty state persists beyond the failed runner's session.
			closeRunner(t, runner)
			runner = nil
			runner = migrationRunner(t, dsn, files, test.split)
			var dirtyErr gomigrate.ErrDirty
			if err := runner.Up(); !errors.As(err, &dirtyErr) || dirtyErr.Version != 2 {
				t.Fatal("expected a new up invocation to refuse dirty version 2")
			}
			assertVersion(t, runner, 2, true)
			assertTableExists(t, observer, schema, "atomic_probe", test.wantTableLeft)
			t.Log("new up invocation refused the dirty version without retrying or repairing it")
		})
	}
}

func TestEmbeddedMigrationsPreserveBaselineAndJobs(t *testing.T) {
	dsn, observer, schema := isolatedSchema(t)
	runner, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatal("could not initialize embedded baseline")
	}
	t.Cleanup(func() { closeRunner(t, runner) })

	if _, _, err := runner.Version(); !errors.Is(err, gomigrate.ErrNilVersion) {
		t.Fatal("expected no applied migration in a fresh schema")
	}
	if err := runner.Steps(1); err != nil {
		t.Fatal("could not apply no-op baseline")
	}
	assertVersion(t, runner, 1, false)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var count int
	if err := observer.QueryRow(ctx,
		"SELECT count(*) FROM information_schema.tables WHERE table_schema = $1", schema).Scan(&count); err != nil {
		t.Fatal("could not inspect tables after baseline")
	}
	if count != 1 {
		t.Fatalf("expected only the migration metadata table, got %d tables", count)
	}
	assertTableExists(t, observer, schema, "schema_migrations", true)
	if err := runner.Steps(1); err != nil {
		t.Fatal("could not apply the jobs migration after the baseline")
	}
	assertVersion(t, runner, 2, false)
	assertTableExists(t, observer, schema, "jobs", true)
	jobTable := pgx.Identifier{schema, "jobs"}.Sanitize()
	var id string
	if err := observer.QueryRow(ctx, "INSERT INTO "+jobTable+" (kind, payload) VALUES ('noop', '{}') RETURNING id").Scan(&id); err != nil {
		t.Fatal("could not enqueue a job before the execution migration")
	}
	if err := runner.Up(); err != nil {
		t.Fatal("could not apply the execution migration")
	}
	assertVersion(t, runner, 3, false)
	var preserved bool
	if err := observer.QueryRow(ctx, "SELECT state = 'available' AND completed_at IS NULL FROM "+jobTable+" WHERE id = $1", id).Scan(&preserved); err != nil || !preserved {
		t.Fatal("execution migration must preserve existing available jobs")
	}
	if err := runner.Up(); !errors.Is(err, gomigrate.ErrNoChange) {
		t.Fatal("expected a repeated up invocation to report no change")
	}
}

func TestMigrationGracefulStopFinishesCurrentFile(t *testing.T) {
	dsn, observer, schema := isolatedSchema(t)
	key := time.Now().UnixNano()
	migrationCtx, stop := context.WithCancel(context.Background())
	defer stop()
	runner, err := open(migrationCtx, dsn, fstest.MapFS{
		"000001_current.up.sql": {Data: fmt.Appendf(nil,
			"CREATE TABLE graceful_probe (id integer); SELECT pg_advisory_xact_lock(%d);", key)},
		"000002_pending.up.sql": {Data: []byte("CREATE TABLE pending_probe (id integer);")},
	}, ".")
	if err != nil {
		t.Fatal("could not initialize cancellable migration runner")
	}
	t.Cleanup(func() { closeRunner(t, runner) })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := observer.Exec(ctx, "SELECT pg_advisory_lock($1)", key); err != nil {
		t.Fatal("could not pause the current migration")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		observer.Exec(ctx, "SELECT pg_advisory_unlock($1)", key)
	})
	result := make(chan error, 1)
	go func() { result <- runner.Up() }()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
waiting:
	for {
		select {
		case <-ticker.C:
			var version int
			var dirty bool
			err := observer.QueryRow(ctx, "SELECT version, dirty FROM "+pgx.Identifier{schema, "schema_migrations"}.Sanitize()).Scan(&version, &dirty)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				t.Fatal("could not inspect the running migration")
			}
			if err == nil && version == 1 && dirty {
				break waiting
			}
		case <-result:
			t.Fatal("migration returned before its pause was released")
		case <-ctx.Done():
			t.Fatal("migration did not start before the test deadline")
		}
	}

	stop()
	if _, err := observer.Exec(ctx, "SELECT pg_advisory_unlock($1)", key); err != nil {
		t.Fatal("could not release the current migration")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("expected cancellation after completing the current file")
		}
	case <-ctx.Done():
		t.Fatal("graceful stop did not finish before the test deadline")
	}
	assertVersion(t, runner, 1, false)
	assertTableExists(t, observer, schema, "graceful_probe", true)
	assertTableExists(t, observer, schema, "pending_probe", false)
	t.Log("graceful stop committed the current file, recorded clean version 1, and left migration 2 pending")
}

func TestOpenCleansUpFailedInitialization(t *testing.T) {
	dsn, observer, schema := isolatedSchema(t)
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("invalid isolated database URL")
	}
	query := parsed.Query()
	query.Set("default_transaction_read_only", "on")
	query.Set("application_name", schema)
	parsed.RawQuery = query.Encode()
	runner, err := Open(context.Background(), parsed.String())
	if err == nil || runner != nil {
		t.Fatal("expected metadata initialization to fail in a read-only session")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var count int
		if err := observer.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE application_name = $1", schema).Scan(&count); err != nil {
			t.Fatal("could not inspect initialization connection cleanup")
		}
		if count == 0 {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("failed initialization left its connection open")
		}
	}
}

func migrationRunner(t *testing.T, dsn string, files fstest.MapFS, split bool) *gomigrate.Migrate {
	t.Helper()
	if !split {
		runner, err := open(context.Background(), dsn, files, ".")
		if err != nil {
			t.Fatal("could not initialize production migration runner")
		}
		return runner
	}
	connConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid migration test database connection configuration")
	}
	connConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	db := stdlib.OpenDB(*connConfig)
	db.SetMaxOpenConns(1)

	// split=true is solely a negative control; the runner will use false.
	driver, err := migratepgx.WithInstance(db, &migratepgx.Config{
		MultiStatementEnabled: split,
	})
	if err != nil {
		db.Close()
		t.Fatal("could not initialize pgx migration test driver")
	}
	source, err := iofs.New(files, ".")
	if err != nil {
		driver.Close()
		t.Fatal("could not initialize migration test source")
	}
	runner, err := gomigrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		source.Close()
		driver.Close()
		t.Fatal("could not initialize migration test runner")
	}
	return runner
}

func isolatedSchema(t *testing.T) (string, *pgx.Conn, string) {
	t.Helper()
	dsn := os.Getenv("CONVOY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("CONVOY_TEST_DATABASE_URL is required for integration tests")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("migration integration tests require a PostgreSQL connection URL")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	observer, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("could not connect to migration integration database")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := observer.Close(ctx); err != nil {
			t.Error("could not close migration integration database connection")
		}
	})

	schema := fmt.Sprintf("convoy_migrate_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := observer.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal("could not create isolated migration test schema")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := observer.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error("could not drop isolated migration test schema")
		}
	})

	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String(), observer, schema
}

func assertVersion(t *testing.T, runner *gomigrate.Migrate, want uint, wantDirty bool) {
	t.Helper()
	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatal("could not inspect migration version")
	}
	if version != want || dirty != wantDirty {
		t.Fatalf("version=%d dirty=%t; want version=%d dirty=%t", version, dirty, want, wantDirty)
	}
}

func assertTableExists(t *testing.T, observer *pgx.Conn, schema, table string, want bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var exists bool
	if err := observer.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", schema+"."+table).Scan(&exists); err != nil {
		t.Fatal("could not inspect migration test table")
	}
	if exists != want {
		t.Fatalf("table %s exists=%t; want %t", table, exists, want)
	}
}

func closeRunner(t *testing.T, runner *gomigrate.Migrate) {
	t.Helper()
	if runner == nil {
		return
	}
	sourceErr, databaseErr := runner.Close()
	if sourceErr != nil || databaseErr != nil {
		t.Error("could not close migration test runner")
	}
}
