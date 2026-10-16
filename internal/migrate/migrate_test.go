package migrate

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestOpenRejectsInvalidConfiguration(t *testing.T) {
	for _, dsn := range []string{
		"",
		" \t",
		"postgres://user:password@localhost/db?x-multi-statement=true",
		"postgres://user:password@localhost/db?x-multi-statement=false",
		"host=localhost dbname=convoy x-multi-statement=true",
		"postgres://user:password@localhost/db?x-statement-timeout=1",
	} {
		runner, err := Open(context.Background(), dsn)
		if err == nil || runner != nil {
			t.Fatal("expected configuration rejection with a nil runner")
		}
	}
}

func TestOpenPreservesParseError(t *testing.T) {
	runner, err := Open(context.Background(), "postgres://user:password@localhost/db?connect_timeout=invalid")
	var parseErr *pgconn.ParseConfigError
	if runner != nil || !errors.As(err, &parseErr) {
		t.Fatal("expected a nil runner and a wrapped connection-string parse error")
	}
}
