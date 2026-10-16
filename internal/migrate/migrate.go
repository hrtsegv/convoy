package migrate

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	gomigrate "github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/*.up.sql
var migrations embed.FS

// Open initializes the migration runner with the embedded SQL files.
// The caller owns the returned runner and must close it. Driver initialization
// does not accept a caller context. Cancellation stops before the next file;
// it does not interrupt initialization or a file already in progress.
// Wrapped connection-string parse errors must be classified before logging.
func Open(ctx context.Context, databaseURL string) (*gomigrate.Migrate, error) {
	return open(ctx, databaseURL, migrations, "migrations")
}

func open(ctx context.Context, databaseURL string, files fs.FS, directory string) (*gomigrate.Migrate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(databaseURL) == "" {
		return nil, errors.New("CONVOY_DATABASE_URL is required")
	}
	connConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse migration database configuration: %w", err)
	}
	for key := range connConfig.RuntimeParams {
		if strings.HasPrefix(strings.ToLower(key), "x-") {
			return nil, errors.New("migration driver options are not allowed in CONVOY_DATABASE_URL")
		}
	}
	connConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	source, err := iofs.New(files, directory)
	if err != nil {
		return nil, fmt.Errorf("open migration source: %w", err)
	}
	var connection *pgx.Conn
	db := stdlib.OpenDB(*connConfig, stdlib.OptionAfterConnect(func(_ context.Context, conn *pgx.Conn) error {
		connection = conn
		return nil
	}))
	db.SetMaxOpenConns(1)
	driver, err := migratepgx.WithInstance(db, &migratepgx.Config{
		MultiStatementEnabled: false,
		StatementTimeout:      0,
	})
	if err != nil {
		// The driver can fail after reserving a sql.Conn without releasing it.
		if connection != nil {
			connection.Close(context.Background())
		}
		db.Close()
		source.Close()
		return nil, fmt.Errorf("initialize migration database: %w", err)
	}
	runner, err := gomigrate.NewWithInstance("iofs", source, "pgx5", stoppableDriver{Driver: driver, ctx: ctx})
	if err != nil {
		driver.Close()
		source.Close()
		return nil, fmt.Errorf("initialize migrations: %w", err)
	}
	return runner, nil
}

// golang-migrate v4.20.1 races on its internal GracefulStop state. Check at
// the same migration boundary without that channel: let an admitted file run
// and become clean, but do not mark another file dirty after cancellation.
type stoppableDriver struct {
	database.Driver
	ctx context.Context
}

func (d stoppableDriver) SetVersion(version int, dirty bool) error {
	if dirty {
		if err := d.ctx.Err(); err != nil {
			return err
		}
	}
	return d.Driver.SetVersion(version, dirty)
}
