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

// The database is often still starting when migrate runs (a started container does not yet accept connections), so
// migrate tries the owner connection this many times, this far apart, before it gives up.
var (
	connectAttempts = 30
	connectInterval = time.Second
	connectTimeout  = 5 * time.Second // one attempt
)

// runMigrate brings the schema to migrations.SupportedVersion as the owner of the schema and then grants the
// application role its access (migrations.GrantApp). It is the only command that changes the schema; serve and setup
// connect as the application role and refuse a schema that is not at the supported version.
func runMigrate(ctx context.Context, cfg config.Config, stderr io.Writer, logger *slog.Logger) int {
	owner := cfg.MigrationDatabaseURL.Reveal()
	if err := waitForDatabase(ctx, owner, connectAttempts, connectInterval); err != nil {
		reportf(stderr, "%v\n", err)
		return 1
	}
	if err := migrations.Up(ctx, owner); err != nil {
		reportf(stderr, "migrate the database: %v\n", err)
		return 1
	}
	if err := migrations.GrantApp(ctx, owner, cfg.DatabaseAppRole); err != nil {
		reportf(stderr, "%v\n", err)
		return 1
	}
	logger.InfoContext(ctx, "database migrated", "version", migrations.SupportedVersion, "role", cfg.DatabaseAppRole)
	return 0
}

// waitForDatabase returns once the database accepts a connection with databaseURL, or at once when it refuses the
// login or does not have the database. The error never carries the URL.
func waitForDatabase(ctx context.Context, databaseURL string, attempts int, interval time.Duration) error {
	var last error
	for attempt := range attempts {
		if attempt > 0 {
			select {
			case <-time.After(interval):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		connection, err := pgx.Connect(ctx, databaseURL)
		if err == nil {
			_ = connection.Close(ctx) // the connection only proved the database accepts them
			return nil
		}
		var answered *pgconn.PgError
		// A refused login or an unknown database stays so; "the database system is starting up" (57P03) does not.
		if errors.As(err, &answered) && (strings.HasPrefix(answered.Code, "28") || answered.Code == "3D000") {
			return fmt.Errorf("the database refused the owner connection: %w", err)
		}
		last = err
	}
	return fmt.Errorf("the database is not reachable after %d attempts: %w", attempts, last)
}
