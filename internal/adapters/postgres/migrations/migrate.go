// Package migrations applies the reviewed PostgreSQL schema sequence.
package migrations

import (
	"context"
	"embed"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// ErrForwardOnly rejects a schema downgrade.
var ErrForwardOnly = errors.New("schema migrations are forward only")

// SupportedVersion is the schema version this build migrates to and expects at runtime.
const SupportedVersion int64 = 10

//go:embed *.sql
var migrationFiles embed.FS

// Up applies the complete supported schema sequence.
func Up(ctx context.Context, databaseURL string) error {
	return UpTo(ctx, databaseURL, SupportedVersion)
}

// UpTo applies migrations through a supported forward version.
func UpTo(ctx context.Context, databaseURL string, version int64) (err error) {
	if version < 1 {
		return ErrForwardOnly
	}
	if version > SupportedVersion {
		return fmt.Errorf("unsupported schema migration version: %d", version)
	}
	configuration, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return fmt.Errorf("parse migration database configuration: %w", err)
	}
	database := stdlib.OpenDB(*configuration)
	defer func() { err = errors.Join(err, database.Close()) }()
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(1, 30))
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, database, migrationFiles,
		goose.WithSessionLocker(locker), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return err
	}
	current, err := provider.GetDBVersion(ctx)
	if err != nil {
		return err
	}
	if version < current {
		return ErrForwardOnly
	}
	_, err = provider.UpTo(ctx, version)
	return err
}
