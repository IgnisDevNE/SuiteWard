package migrations

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// GrantApp gives the application role the access the service needs in the schema of databaseURL (its current_schema),
// and nothing more: it may read and write rows, use sequences and read the schema version. It owns nothing, so it can
// neither disable or drop the immutability triggers, nor truncate history, nor set session_replication_role.
//
// databaseURL is the owner of the schema's objects; the role must exist, because creating it needs a password and
// that is the operator's. The grants are idempotent, and the default privileges cover what later migrations create.
func GrantApp(ctx context.Context, databaseURL, role string) (err error) {
	connection, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect to grant the application role: %w", err)
	}
	defer func() { err = errors.Join(err, connection.Close(context.WithoutCancel(ctx))) }()
	var schema, owner string
	var exists bool
	if err := connection.QueryRow(ctx, "SELECT current_schema(), current_user, EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", role).Scan(&schema, &owner, &exists); err != nil {
		return fmt.Errorf("read the schema and the role: %w", err)
	}
	if !exists {
		return fmt.Errorf("the database role %q does not exist: create it with a login and a password, then run migrate again", role)
	}
	if role == owner {
		return fmt.Errorf("the application role %q is the role that owns the schema: it must be a different role", role)
	}
	s, r, o := pgx.Identifier{schema}.Sanitize(), pgx.Identifier{role}.Sanitize(), pgx.Identifier{owner}.Sanitize()
	transaction, err := connection.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin the grants: %w", err)
	}
	defer func() { _ = transaction.Rollback(context.WithoutCancel(ctx)) }() // a no-op after the commit
	for _, statement := range []string{
		"GRANT USAGE ON SCHEMA " + s + " TO " + r,
		"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA " + s + " TO " + r,
		"GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA " + s + " TO " + r,
		// The version is the gate of startup: the role reads it and cannot move it.
		"REVOKE INSERT, UPDATE, DELETE ON " + s + ".goose_db_version FROM " + r,
		"ALTER DEFAULT PRIVILEGES FOR ROLE " + o + " IN SCHEMA " + s + " GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO " + r,
		"ALTER DEFAULT PRIVILEGES FOR ROLE " + o + " IN SCHEMA " + s + " GRANT USAGE, SELECT ON SEQUENCES TO " + r,
	} {
		if _, err := transaction.Exec(ctx, statement); err != nil {
			return fmt.Errorf("grant the application role %q: %w", role, err)
		}
	}
	return transaction.Commit(ctx)
}
