package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	setTestEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	want := Config{
		DatabaseURL:      "postgres://convoy:local@localhost:5432/convoy",
		HTTPAddr:         "127.0.0.1:8080",
		LogLevel:         slog.LevelInfo,
		DBMaxConns:       4,
		DBConnectTimeout: 5 * time.Second,
		DBQueryTimeout:   2 * time.Second,
		JobQueryTimeout:  5 * time.Second,
		ShutdownTimeout:  10 * time.Second,
	}
	if cfg != want {
		t.Error("configuration did not match defaults")
	}
}

func TestLoadOverrides(t *testing.T) {
	setTestEnv(t)
	t.Setenv("CONVOY_DATABASE_URL", "host=localhost dbname=convoy_test")
	t.Setenv("CONVOY_HTTP_ADDR", "[::1]:9000")
	t.Setenv("CONVOY_LOG_LEVEL", "debug")
	t.Setenv("CONVOY_DB_MAX_CONNS", "8")
	t.Setenv("CONVOY_DB_CONNECT_TIMEOUT", "3s")
	t.Setenv("CONVOY_DB_QUERY_TIMEOUT", "500ms")
	t.Setenv("CONVOY_JOB_QUERY_TIMEOUT", "750ms")
	t.Setenv("CONVOY_SHUTDOWN_TIMEOUT", "20s")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	want := Config{
		DatabaseURL:      "host=localhost dbname=convoy_test",
		HTTPAddr:         "[::1]:9000",
		LogLevel:         slog.LevelDebug,
		DBMaxConns:       8,
		DBConnectTimeout: 3 * time.Second,
		DBQueryTimeout:   500 * time.Millisecond,
		JobQueryTimeout:  750 * time.Millisecond,
		ShutdownTimeout:  20 * time.Second,
	}
	if cfg != want {
		t.Error("configuration did not match environment overrides")
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "missing database", key: "CONVOY_DATABASE_URL", value: ""},
		{name: "blank database", key: "CONVOY_DATABASE_URL", value: " \t"},
		{name: "missing port", key: "CONVOY_HTTP_ADDR", value: "localhost"},
		{name: "named port", key: "CONVOY_HTTP_ADDR", value: "localhost:http"},
		{name: "port overflow", key: "CONVOY_HTTP_ADDR", value: "localhost:65536"},
		{name: "unknown log level", key: "CONVOY_LOG_LEVEL", value: "verbose"},
		{name: "zero connections", key: "CONVOY_DB_MAX_CONNS", value: "0"},
		{name: "negative connections", key: "CONVOY_DB_MAX_CONNS", value: "-1"},
		{name: "fractional connections", key: "CONVOY_DB_MAX_CONNS", value: "1.5"},
		{name: "connection count overflow", key: "CONVOY_DB_MAX_CONNS", value: "2147483648"},
		{name: "invalid connect timeout", key: "CONVOY_DB_CONNECT_TIMEOUT", value: "soon"},
		{name: "zero connect timeout", key: "CONVOY_DB_CONNECT_TIMEOUT", value: "0s"},
		{name: "negative query timeout", key: "CONVOY_DB_QUERY_TIMEOUT", value: "-1s"},
		{name: "duration overflow", key: "CONVOY_DB_QUERY_TIMEOUT", value: "999999999999h"},
		{name: "invalid shutdown timeout", key: "CONVOY_SHUTDOWN_TIMEOUT", value: "ten seconds"},
		{name: "invalid job timeout", key: "CONVOY_JOB_QUERY_TIMEOUT", value: "soon"},
		{name: "zero job timeout", key: "CONVOY_JOB_QUERY_TIMEOUT", value: "0s"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setTestEnv(t)
			t.Setenv(test.key, test.value)

			cfg, err := Load()
			if err == nil {
				t.Fatal("expected invalid configuration to fail")
			}
			if !strings.Contains(err.Error(), test.key) {
				t.Errorf("error should identify %s", test.key)
			}
			if cfg != (Config{}) {
				t.Error("expected no partial configuration on failure")
			}
			if strings.Contains(err.Error(), "postgres://") {
				t.Error("error exposed the database connection string")
			}
		})
	}
}

func setTestEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"CONVOY_HTTP_ADDR",
		"CONVOY_LOG_LEVEL",
		"CONVOY_DB_MAX_CONNS",
		"CONVOY_DB_CONNECT_TIMEOUT",
		"CONVOY_DB_QUERY_TIMEOUT",
		"CONVOY_JOB_QUERY_TIMEOUT",
		"CONVOY_SHUTDOWN_TIMEOUT",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("CONVOY_DATABASE_URL", "postgres://convoy:local@localhost:5432/convoy")
}
