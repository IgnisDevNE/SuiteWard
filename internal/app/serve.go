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
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/mcp"
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
	pool, store, _, err := connectWithArtifacts(ctx, cfg)
	return pool, store, err
}

// connectWithArtifacts is connect that also returns the artifact store, which a process opens once: the content
// writer of the bootstrap.
func connectWithArtifacts(ctx context.Context, cfg config.Config) (*pgxpool.Pool, *postgres.Store, *filesystem.Store, error) {
	artifacts, err := filesystem.NewStore(cfg.ArtifactDir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open the artifact store: %w", err)
	}
	inserter, err := river.NewInserter(cfg.JobMaxAttempts)
	if err != nil {
		return nil, nil, nil, err
	}
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL.Reveal())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open the connection pool: %w", err)
	}
	store, err := postgres.NewStore(pool, artifacts, inserter)
	if err != nil {
		pool.Close()
		return nil, nil, nil, err
	}
	return pool, store, artifacts, nil
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
	pool, store, artifacts, err := connectWithArtifacts(ctx, cfg)
	if err != nil {
		return err
	}
	riverConfig := river.Config{
		Logger: logger, Workers: cfg.JobWorkers, JobTimeout: cfg.JobTimeout, JobMaxAttempts: cfg.JobMaxAttempts,
		OutboxMaxAttempts: cfg.OutboxMaxAttempts, OutboxPollInterval: cfg.OutboxPollInterval, OutboxLease: cfg.OutboxLease,
		Handlers: handlers,
	}
	tier := string(cfg.Tier)
	a := &api{store: store, owners: store, logger: logger, tier: tier}
	backend := &mcpBackend{store: store, owners: store, now: time.Now, timeout: storeTimeout, logger: logger, tier: tier}
	if cfg.GitHub != nil { // the GitHub settings are optional: unset, nothing GitHub-related is composed
		gh, err := composeGitHub(*cfg.GitHub, cfg.JobTimeout, failureLog{Store: store, logger: logger})
		if err != nil {
			pool.Close()
			return err
		}
		riverConfig.SyncTick, riverConfig.SyncRepo = gh.tick, gh.syncRepo
		ids := &repositoryIDs{}
		check := &protectionCheck{repos: gh.repos, appID: gh.appID, readers: gh.readers, store: store, ids: ids, logger: logger, now: time.Now, within: tickDeadline(cfg.JobTimeout)}
		riverConfig.ProtectionCheck = check.run
		problems := &bootstrapProblems{}
		bootstrap := &bootstrapJob{repos: gh.repos, reader: gh.readers.read, owners: store, suites: store, content: artifacts, pulls: store, tier: tier, problems: problems, logger: logger, now: time.Now}
		riverConfig.Bootstrap = bootstrap.run
		commandJob := &commandsJob{ledger: store, owners: store, suites: store, syncs: store, repos: gh.repos, bot: gh.bot, jobTimeout: cfg.JobTimeout, logger: logger, now: time.Now}
		riverConfig.Commands = commandJob.run
		comments := &commentPublisher{writer: gh.writer, bot: gh.bot, instance: newLazy(store.Instance), owners: store, suites: store}
		checks := &checkPublisher{runs: gh.checkRuns, ledger: store.Checks(), instance: newLazy(store.Instance), appID: gh.appID, logger: logger}
		riverConfig.Publishers = map[string]river.Publisher{githubCommentKind: comments.publish, githubCheckKind: checks.publish}
		a.github = &githubStatus{repos: gh.repos, store: store, ids: ids, problems: problems}
		backend.repos = gh.repos
		backend.ids = ids
	}
	runtime, err := river.NewRuntime(pool, store, riverConfig)
	if err != nil {
		pool.Close()
		return fmt.Errorf("compose the job runtime: %w", err)
	}
	// River's jobs inherit the context given to Start: the signal context would cancel them and defeat the soft stop.
	if err := runtime.Start(context.WithoutCancel(ctx)); err != nil {
		pool.Close()
		return fmt.Errorf("start the job runtime: %w", err)
	}
	backend.inserter = runtime
	srv := &http.Server{Handler: newRouter(a), ReadHeaderTimeout: readHeaderTimeout}
	var (
		mcpServers []*http.Server
		mcpFailed  <-chan error // nil without MCP: never ready
	)
	if cfg.MCP != nil { // contract-optional: the listener runs whenever MCP_TOKEN is set, GitHub or not
		mcpSrv, failed, err := startMCP(*cfg.MCP, backend, logger)
		if err != nil {
			return errors.Join(err, shutdown(logger, a, srv, runtime, pool, cfg.ShutdownTimeout))
		}
		mcpServers, mcpFailed = []*http.Server{mcpSrv}, failed
	}
	serveErr := listenAndServe(ctx, cfg.HTTPAddr, srv, logger, mcpFailed)
	return errors.Join(serveErr, shutdown(logger, a, srv, runtime, pool, cfg.ShutdownTimeout, mcpServers...))
}

// startMCP listens on the MCP address and serves the handler in the
// background. A listen failure is returned, so serve fails at startup; failed
// receives the error of a server that stops on its own.
func startMCP(settings config.MCP, backend mcp.Backend, logger *slog.Logger) (srv *http.Server, failed <-chan error, err error) {
	handler, err := mcp.NewHandler(backend, []byte(settings.Token.Reveal()), settings.AllowedOrigins, Version)
	if err != nil {
		return nil, nil, fmt.Errorf("compose the MCP handler: %w", err)
	}
	listener, err := net.Listen("tcp", settings.Addr)
	if err != nil {
		return nil, nil, fmt.Errorf("listen on %s for MCP: %w", settings.Addr, err)
	}
	srv = &http.Server{Handler: handler, ReadHeaderTimeout: readHeaderTimeout}
	stopped := make(chan error, 1)
	go func() { stopped <- srv.Serve(listener) }()
	logger.Info("mcp listening", "addr", listener.Addr().String())
	return srv, stopped, nil
}

// listenAndServe serves until ctx is canceled or mcpFailed reports a failure
// (a nil channel never does). It returns nil for the cancellation.
func listenAndServe(ctx context.Context, addr string, srv *http.Server, logger *slog.Logger, mcpFailed <-chan error) error {
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
	case err := <-mcpFailed:
		return fmt.Errorf("mcp server: %w", err)
	}
}

// shutdown stops the service within one budget of timeout: readiness turns
// 503, the HTTP server and then the MCP server (when there is one) drain,
// River stops softly (running jobs finish), then the pool closes. When the
// budget runs out River is stopped with cancellation, for at most forceGrace
// more, and the error says so. A River that cannot be stopped still holds the
// pool, so the pool stays open then.
func shutdown(logger *slog.Logger, a *api, srv *http.Server, runtime *river.Runtime, pool *pgxpool.Pool, timeout time.Duration, mcpServers ...*http.Server) error {
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
	for _, mcpSrv := range mcpServers {
		if err := mcpSrv.Shutdown(budget); err != nil {
			errs = append(errs, fmt.Errorf("drain the mcp server: %w", err))
		} else {
			logger.Info("mcp server stopped")
		}
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
