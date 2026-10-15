package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL      string
	HTTPAddr         string
	LogLevel         slog.Level
	DBMaxConns       int32
	DBConnectTimeout time.Duration
	DBQueryTimeout   time.Duration
	ShutdownTimeout  time.Duration
}

func Load() (Config, error) {
	var cfg Config

	cfg.DatabaseURL = os.Getenv("CONVOY_DATABASE_URL")
	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		return Config{}, errors.New("CONVOY_DATABASE_URL is required")
	}

	cfg.HTTPAddr = envOrDefault("CONVOY_HTTP_ADDR", "127.0.0.1:8080")
	_, port, err := net.SplitHostPort(cfg.HTTPAddr)
	if err != nil {
		return Config{}, errors.New("CONVOY_HTTP_ADDR must contain a host and numeric port")
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return Config{}, errors.New("CONVOY_HTTP_ADDR must contain a host and numeric port")
	}

	if err := cfg.LogLevel.UnmarshalText([]byte(envOrDefault("CONVOY_LOG_LEVEL", "INFO"))); err != nil {
		return Config{}, errors.New("CONVOY_LOG_LEVEL must be a valid Go logging level")
	}

	maxConns, err := strconv.ParseInt(envOrDefault("CONVOY_DB_MAX_CONNS", "4"), 10, 32)
	if err != nil || maxConns <= 0 {
		return Config{}, errors.New("CONVOY_DB_MAX_CONNS must be a positive 32-bit integer")
	}
	cfg.DBMaxConns = int32(maxConns)

	cfg.DBConnectTimeout, err = positiveDuration("CONVOY_DB_CONNECT_TIMEOUT", "5s")
	if err != nil {
		return Config{}, err
	}
	cfg.DBQueryTimeout, err = positiveDuration("CONVOY_DB_QUERY_TIMEOUT", "2s")
	if err != nil {
		return Config{}, err
	}
	cfg.ShutdownTimeout, err = positiveDuration("CONVOY_SHUTDOWN_TIMEOUT", "10s")
	if err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func positiveDuration(key, fallback string) (time.Duration, error) {
	duration, err := time.ParseDuration(envOrDefault(key, fallback))
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive Go duration", key)
	}
	return duration, nil
}
