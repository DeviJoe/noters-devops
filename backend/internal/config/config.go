package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	HTTPAddr          string
	LogLevel          slog.Level
	Database          *pgxpool.Config
	SessionTTL        time.Duration
	SecureCookie      bool
	AllowRegistration bool
}

func Load() (Config, error) {
	c := Config{HTTPAddr: ":8080", LogLevel: slog.LevelInfo, SessionTTL: 24 * time.Hour, AllowRegistration: true}
	if value := os.Getenv("AUTH_SESSION_TTL"); value != "" {
		ttl, err := time.ParseDuration(value)
		if err != nil || ttl < time.Minute || ttl > 30*24*time.Hour {
			return c, fmt.Errorf("AUTH_SESSION_TTL must be between 1m and 720h")
		}
		c.SessionTTL = ttl
	}
	for _, setting := range []struct {
		name   string
		target *bool
	}{
		{"AUTH_COOKIE_SECURE", &c.SecureCookie}, {"AUTH_ALLOW_REGISTRATION", &c.AllowRegistration},
	} {
		if value := os.Getenv(setting.name); value != "" {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return c, fmt.Errorf("%s must be a boolean", setting.name)
			}
			*setting.target = parsed
		}
	}
	if value := os.Getenv("HTTP_ADDR"); value != "" {
		c.HTTPAddr = value
	}
	if value := os.Getenv("LOG_LEVEL"); value != "" {
		if err := c.LogLevel.UnmarshalText([]byte(value)); err != nil {
			return c, fmt.Errorf("LOG_LEVEL must be DEBUG, INFO, WARN or ERROR")
		}
	}
	// An empty connection string makes pgx read standard PG* environment variables.
	db, err := pgxpool.ParseConfig("")
	if err != nil {
		return c, fmt.Errorf("invalid PostgreSQL configuration: %w", err)
	}
	db.MaxConns = 10
	if value := os.Getenv("DB_MAX_CONNS"); value != "" {
		n, err := strconv.ParseInt(value, 10, 32)
		if err != nil || n < 1 || n > 100 {
			return c, fmt.Errorf("DB_MAX_CONNS must be between 1 and 100")
		}
		db.MaxConns = int32(n)
	}
	db.ConnConfig.ConnectTimeout = 5 * time.Second
	db.MaxConnLifetime = time.Hour
	db.MaxConnIdleTime = 5 * time.Minute
	c.Database = db
	return c, nil
}
