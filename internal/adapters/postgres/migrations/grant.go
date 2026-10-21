package migrations

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// GrantApp gives the application role the access the service needs in the schema of databaseURL (its current_schema),
// and nothing more: it may read and write rows, use sequences and read the schema version. It owns nothing, so it can
// neither disable or drop the immutability triggers, nor truncate history, nor set session_replication_role.
//
// databaseURL is the owner of the schema's objects; the role must exist, because creating it needs a password and
// that is the operator's, and must be least-privilege (see CheckAppRole). The grants are idempotent, and the default
// privileges cover what later migrations create.
func GrantApp(ctx context.Context, databaseURL, role string) (err error) {
	connection, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect to grant the application role: %w", err)
	}
	defer func() { err = errors.Join(err, connection.Close(context.WithoutCancel(ctx))) }()
	schema, owner, err := checkAppRole(ctx, connection, role)
	if err != nil {
		return err
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
		// Converge: nothing may leave the role able to truncate history or add triggers.
		"REVOKE TRUNCATE, TRIGGER, REFERENCES ON ALL TABLES IN SCHEMA " + s + " FROM " + r,
		"ALTER DEFAULT PRIVILEGES FOR ROLE " + o + " IN SCHEMA " + s + " GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO " + r,
		"ALTER DEFAULT PRIVILEGES FOR ROLE " + o + " IN SCHEMA " + s + " GRANT USAGE, SELECT ON SEQUENCES TO " + r,
	} {
		if _, err := transaction.Exec(ctx, statement); err != nil {
			return fmt.Errorf("grant the application role %q: %w", role, err)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit the grants: %w", err)
	}
	return nil
}

// CheckAppRole reports why role cannot be the role the service connects as: it does not exist, or it is the role of
// databaseURL, a superuser, a role that bypasses row security, a member of the migration role or of the owner of the
// schema, the owner of the schema, or the owner of a table in it, any of which would let the service undo what only the
// owner may do. It changes nothing, so migrate runs it before it migrates.
func CheckAppRole(ctx context.Context, databaseURL, role string) (err error) {
	connection, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect to check the application role: %w", err)
	}
	defer func() { err = errors.Join(err, connection.Close(context.WithoutCancel(ctx))) }()
	_, _, err = checkAppRole(ctx, connection, role)
	return err
}

// checkAppRole returns the schema and the connection's user, once role is known to be least-privilege.
func checkAppRole(ctx context.Context, connection *pgx.Conn, role string) (schema, owner string, err error) {
	var superuser, bypassRLS, ownsSchema, migrationMember, ownsRelation bool
	// pg_has_role(..., 'MEMBER') is true for the role itself and for any role that inherits its privileges. The schema owner may
	// not be the migration role.
	err = connection.QueryRow(ctx, `SELECT COALESCE(current_schema(), ''), current_user, r.rolsuper, r.rolbypassrls,
		COALESCE((SELECT pg_has_role(r.oid, n.nspowner, 'MEMBER') FROM pg_namespace n WHERE n.nspname = current_schema()), false),
		pg_has_role(r.oid, current_user, 'MEMBER'),
		EXISTS (SELECT 1 FROM pg_class c WHERE c.relowner = r.oid AND c.relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = current_schema()))
		FROM pg_roles r WHERE r.rolname = $1`, role).Scan(&schema, &owner, &superuser, &bypassRLS, &ownsSchema, &migrationMember, &ownsRelation)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", fmt.Errorf("the database role %q does not exist: create it with a login and a password, then run migrate again", role)
	}
	if err != nil {
		return "", "", fmt.Errorf("read the schema and the role: %w", err)
	}
	if schema == "" {
		return "", "", errors.New("the connection has no current schema to grant on")
	}
	var reasons []string
	for _, c := range []struct {
		failed bool
		reason string
	}{
		{migrationMember, "it is the migration role or a member of it"},
		{superuser, "it is a superuser"},
		{bypassRLS, "it bypasses row security"},
		{ownsSchema, "it owns the schema or is a member of its owner"},
		{ownsRelation, "it owns a table in the schema"},
	} {
		if c.failed {
			reasons = append(reasons, c.reason)
		}
	}
	if len(reasons) > 0 {
		return "", "", fmt.Errorf("the service must connect as a least-privilege role, and %q is not: %s", role, strings.Join(reasons, ", "))
	}
	return schema, owner, nil
}
