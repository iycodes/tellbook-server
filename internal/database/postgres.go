package database

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"booking/go-server/internal/config"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func OpenPool(ctx context.Context, cfg config.Config, tracer pgx.QueryTracer) (*pgxpool.Pool, error) {
	return openPool(ctx, cfg.DatabaseURL, poolOptions{
		name:     "query",
		maxConns: cfg.DatabaseMaxConnections,
		minConns: cfg.DatabaseMinConnections,
		config:   cfg,
		tracer:   tracer,
	})
}

// OpenDirectPool opens the small pool reserved for LISTEN and session-level
// leadership connections. DATABASE_DIRECT_URL should point directly at
// PostgreSQL when the ordinary query pool is routed through PgBouncer.
func OpenDirectPool(ctx context.Context, cfg config.Config, tracer pgx.QueryTracer) (*pgxpool.Pool, error) {
	databaseURL := strings.TrimSpace(cfg.DatabaseDirectURL)
	if databaseURL == "" {
		databaseURL = cfg.DatabaseURL
	}
	return openPool(ctx, databaseURL, poolOptions{
		name:     "direct",
		maxConns: cfg.DatabaseDirectMaxConnections,
		minConns: 0,
		config:   cfg,
		tracer:   tracer,
	})
}

type poolOptions struct {
	name     string
	maxConns int32
	minConns int32
	config   config.Config
	tracer   pgx.QueryTracer
}

func openPool(ctx context.Context, databaseURL string, options poolOptions) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse %s database url: %w", options.name, err)
	}

	poolConfig.MaxConns = options.maxConns
	poolConfig.MinConns = options.minConns
	poolConfig.MaxConnLifetime = options.config.DatabaseMaxConnectionLifetime
	poolConfig.MaxConnLifetimeJitter = options.config.DatabaseMaxConnectionLifetimeJitter
	poolConfig.MaxConnIdleTime = options.config.DatabaseMaxConnectionIdleTime
	poolConfig.HealthCheckPeriod = options.config.DatabaseHealthCheckPeriod
	poolConfig.ConnConfig.ConnectTimeout = options.config.DatabaseConnectTimeout
	poolConfig.ConnConfig.Tracer = options.tracer
	poolConfig.ConnConfig.RuntimeParams["application_name"] = fmt.Sprintf(
		"tellbook-%s-%s-%s",
		options.config.AppEnv,
		options.config.ProcessRole,
		options.name,
	)
	poolConfig.ConnConfig.RuntimeParams["statement_timeout"] = postgresDuration(options.config.DatabaseStatementTimeout)
	poolConfig.ConnConfig.RuntimeParams["lock_timeout"] = postgresDuration(options.config.DatabaseLockTimeout)
	poolConfig.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = postgresDuration(options.config.DatabaseIdleTransactionTimeout)

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("open %s pgx pool: %w", options.name, err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping %s database: %w", options.name, err)
	}

	return pool, nil
}

func postgresDuration(value time.Duration) string {
	return strconv.FormatInt(value.Milliseconds(), 10)
}
