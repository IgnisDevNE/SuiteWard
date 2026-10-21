package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// migrateWait is how migrate waits for the database, which is often still starting (a started container does not yet
// accept connections): it tries the owner connection attempts times, interval apart, each bounded to timeout.
type migrateWait struct {
	attempts int
	interval time.Duration
	timeout  time.Duration
}

// runMigrate brings the schema to migrations.SupportedVersion as the owner of the schema and then grants the
// application role its access (migrations.GrantApp). It is the only command that changes the schema; serve and setup
// connect as the application role and refuse a schema that is not at the supported version.
func runMigrate(ctx context.Context, cfg config.Config, role string, wait migrateWait, stderr io.Writer, logger *slog.Logger) int {
	owner := cfg.MigrationDatabaseURL.Reveal()
	if err := waitForDatabase(ctx, owner, wait); err != nil {
		reportf(stderr, "%v\n", err)
		return 1
	}
	if err := migrations.CheckAppRole(ctx, owner, role); err != nil { // before anything changes
		reportf(stderr, "%v\n", err)
		return 1
	}
	if err := migrations.Up(ctx, owner); err != nil {
		reportf(stderr, "migrate the database: %v\n", err)
		return 1
	}
	if err := migrations.GrantApp(ctx, owner, role); err != nil {
		reportf(stderr, "%v\n", err)
		return 1
	}
	logger.InfoContext(ctx, "database migrated", "version", migrations.SupportedVersion, "role", role)
	return 0
}

// waitForDatabase returns once the database accepts a connection with databaseURL, or at once when it refuses the
// login or does not have the database. The error never carries the URL.
func waitForDatabase(ctx context.Context, databaseURL string, wait migrateWait) error {
	var last error
	for attempt := range wait.attempts {
		if attempt > 0 {
			select {
			case <-time.After(wait.interval):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		attemptCtx, cancel := context.WithTimeout(ctx, wait.timeout)
		connection, err := pgx.Connect(attemptCtx, databaseURL)
		if err == nil {
			_ = connection.Close(attemptCtx) // the connection only proved the database accepts them
			cancel()
			return nil
		}
		cancel()
		var answered *pgconn.PgError
		// A refused login or an unknown database stays so; "the database system is starting up" (57P03) does not.
		if errors.As(err, &answered) && (strings.HasPrefix(answered.Code, "28") || answered.Code == "3D000") {
			return fmt.Errorf("the database refused the owner connection: %w", err)
		}
		last = err
	}
	return fmt.Errorf("the database is not reachable after %d attempts: %w", wait.attempts, last)
}
