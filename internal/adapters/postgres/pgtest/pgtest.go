// Package pgtest gives integration tests the least-privilege application role the service runs as, so that what they
// exercise is what production can do: data access and nothing else.
package pgtest

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
)

// Role is the application role of the tests. Roles belong to the cluster, not to a schema, so every test of every
// package shares this one.
const Role = "suiteward_test_app"

const (
	duplicateObject = "42710" // CREATE ROLE of a role that exists
	uniqueViolation = "23505" // two sessions racing to create it: pg_authid's unique index fires first
)

// EnsureRole creates Role, when it is missing, with the password of ownerURL (or without one when ownerURL has none:
// the cluster then trusts local connections). It is safe to call from parallel tests and packages.
func EnsureRole(t testing.TB, ownerURL string) {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), ownerURL)
	if err != nil {
		t.Fatalf("connect the database owner: %v", err)
	}
	defer func() { _ = conn.Close(context.Background()) }() // a failed close only means the connection is gone
	statement := "CREATE ROLE " + pgx.Identifier{Role}.Sanitize() + " LOGIN"
	if password := password(t, ownerURL); password != "" {
		// CREATE ROLE takes no bind parameters: the server quotes the literal.
		var quoted string
		if err := conn.QueryRow(t.Context(), "SELECT quote_literal($1::text)", password).Scan(&quoted); err != nil {
			t.Fatal(err)
		}
		statement += " PASSWORD " + quoted
	}
	var pgErr *pgconn.PgError
	if _, err := conn.Exec(t.Context(), statement); err != nil {
		if !errors.As(err, &pgErr) || (pgErr.Code != duplicateObject && pgErr.Code != uniqueViolation) {
			t.Fatalf("create the application role: %v", err)
		}
	}
}

// AppURL grants Role, in the schema that ownerURL selects, what `suiteward migrate` grants the application role, and
// returns ownerURL for Role. The schema must be migrated.
func AppURL(t testing.TB, ownerURL string) string {
	t.Helper()
	EnsureRole(t, ownerURL)
	if err := migrations.GrantApp(t.Context(), ownerURL, Role); err != nil {
		t.Fatal(err)
	}
	return AsRole(t, ownerURL)
}

// AsRole returns ownerURL with the user of Role, which has the owner's password.
func AsRole(t testing.TB, ownerURL string) string {
	t.Helper()
	parsed, err := url.Parse(ownerURL)
	if err != nil {
		t.Fatal("the test database must be a PostgreSQL URL")
	}
	if password, ok := parsed.User.Password(); ok {
		parsed.User = url.UserPassword(Role, password)
	} else {
		parsed.User = url.User(Role)
	}
	return parsed.String()
}

func password(t testing.TB, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal("the test database must be a PostgreSQL URL")
	}
	password, _ := parsed.User.Password()
	return password
}
