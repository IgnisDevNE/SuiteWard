package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/filesystem"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

const (
	readHeaderTimeout = 10 * time.Second
	// forceGrace is what cancelled jobs get to return after SHUTDOWN_TIMEOUT ran out. The supervisor's
	// stop timeout must exceed SHUTDOWN_TIMEOUT by at least this much (the contract's 45 s over 30 s does).
	forceGrace = 5 * time.Second
)

// newLogger is the structured JSON logger of the runtime contract.
func newLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

// connect composes the PostgreSQL unit of work over a new pool, which the
// caller closes. It does not migrate: NewStore rejects a schema that is not ready.
func connect(ctx context.Context, cfg config.Config) (*pgxpool.Pool, *postgres.Store, error) {
	artifacts, err := filesystem.NewStore(cfg.ArtifactDir)
	if err != nil {
		return nil, nil, fmt.Errorf("open the artifact store: %w", err)
	}
	inserter, err := river.NewInserter(cfg.JobMaxAttempts)
	if err != nil {
		return nil, nil, err
	}
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL.Reveal())
	if err != nil {
		return nil, nil, fmt.Errorf("open the connection pool: %w", err)
	}
	store, err := postgres.NewStore(pool, artifacts, inserter)
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	return pool, store, nil
}

// serve runs the service until ctx is canceled and returns the exit code:
// 0 for a clean stop, 1 for a fatal startup error or an unclean stop. handlers
// are job handlers added to the built-in probe.
func serve(ctx context.Context, cfg config.Config, logger *slog.Logger, handlers map[string]river.Handler) int {
	if err := runService(ctx, cfg, logger, handlers); err != nil {
		logger.ErrorContext(ctx, "service failed", "error", err)
		return 1
	}
	return 0
}

func runService(ctx context.Context, cfg config.Config, logger *slog.Logger, handlers map[string]river.Handler) error {
	if err := migrations.Up(ctx, cfg.DatabaseURL.Reveal()); err != nil {
		return fmt.Errorf("migrate the database: %w", err)
	}
	pool, store, err := connect(ctx, cfg)
	if err != nil {
		return err
	}
	runtime, err := river.NewRuntime(pool, store, river.Config{
		Logger: logger, Workers: cfg.JobWorkers, JobTimeout: cfg.JobTimeout,
		OutboxMaxAttempts: cfg.OutboxMaxAttempts, OutboxPollInterval: cfg.OutboxPollInterval, OutboxLease: cfg.OutboxLease,
		Handlers: handlers,
	})
	if err != nil {
		pool.Close()
		return fmt.Errorf("compose the job runtime: %w", err)
	}
	// River's jobs inherit the context given to Start: the signal context would cancel them and defeat the soft stop.
	if err := runtime.Start(context.WithoutCancel(ctx)); err != nil {
		pool.Close()
		return fmt.Errorf("start the job runtime: %w", err)
	}
	a := &api{store: store, logger: logger}
	srv := &http.Server{Handler: newRouter(a), ReadHeaderTimeout: readHeaderTimeout}
	serveErr := listenAndServe(ctx, cfg.HTTPAddr, srv, logger)
	return errors.Join(serveErr, shutdown(logger, a, srv, runtime, pool, cfg.ShutdownTimeout))
}

// listenAndServe serves until ctx is canceled. It returns nil for that cancellation.
func listenAndServe(ctx context.Context, addr string, srv *http.Server, logger *slog.Logger) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	failed := make(chan error, 1)
	go func() { failed <- srv.Serve(listener) }()
	logger.Info("listening", "addr", listener.Addr().String())
	select {
	case <-ctx.Done():
		return nil
	case err := <-failed:
		return fmt.Errorf("http server: %w", err)
	}
}

// shutdown stops the service within one budget of timeout: readiness turns
// 503, the HTTP server drains, River stops softly (running jobs finish), then
// the pool closes. When the budget runs out River is stopped with
// cancellation, for at most forceGrace more, and the error says so. A River
// that cannot be stopped still holds the pool, so the pool stays open then.
func shutdown(logger *slog.Logger, a *api, srv *http.Server, runtime *river.Runtime, pool *pgxpool.Pool, timeout time.Duration) error {
	a.stopping.Store(true)
	logger.Info("shutting down", "timeout", timeout.String())
	budget, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var errs []error
	if err := srv.Shutdown(budget); err != nil {
		errs = append(errs, fmt.Errorf("drain the http server: %w", err))
	} else {
		logger.Info("http server stopped")
	}
	forced := false
	if err := runtime.Stop(budget); err != nil {
		errs = append(errs, fmt.Errorf("stop the job runtime within %v: %w", timeout, err))
		logger.Warn("job runtime did not stop in time; cancelling running jobs")
		grace, cancelGrace := context.WithTimeout(context.Background(), forceGrace)
		defer cancelGrace()
		if err := runtime.StopAndCancel(grace); err != nil {
			return errors.Join(append(errs, fmt.Errorf("cancel running jobs: %w", err))...)
		}
		forced = true
	}
	logger.Info("job runtime stopped", "forced", forced)
	pool.Close()
	logger.Info("database pool closed")
	return errors.Join(errs...)
}
