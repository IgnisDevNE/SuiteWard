package installer

import (
	"context"
	"fmt"
	"strings"
)

const (
	appRole   = "suiteward_app" // the role serve connects as: least privilege, created here once
	ownerRole = "suiteward"     // the bootstrap role, used only by migrate
)

// ownerPSQL runs SQL as the database owner over the container's local socket, for both tiers. The SQL goes in on stdin: an
// argument would show in the process list. A sensitive statement carries a password and a failing statement echoes itself, so
// its error says only that it failed.
func ownerPSQL(ctx context.Context, rt Runtime, sql string, sensitive bool) (string, error) {
	argv := []string{"psql", "-X", "-q", "-t", "-A", "-v", "ON_ERROR_STOP=1", "-U", ownerRole, "-d", "suiteward", "-f", "-"}
	out, err := rt.Exec(ctx, ContainerDB, argv, strings.NewReader(sql))
	if err != nil {
		if sensitive {
			return "", fmt.Errorf("psql in %s failed (the statement is not shown: it carries a password)", ContainerDB)
		}
		return "", fmt.Errorf("psql in %s failed: %w", ContainerDB, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// appRoleExists says whether the application role exists in the database.
func appRoleExists(ctx context.Context, rt Runtime) (bool, error) {
	out, err := ownerPSQL(ctx, rt, "SELECT 1 FROM pg_roles WHERE rolname = '"+appRole+"'", false)
	return out == "1", err
}

// setAppRolePassword creates the application role, or gives the role that exists a new password, and returns the password for
// the caller's URL secret. The statement is not logged by the database, not even a failing one.
func setAppRolePassword(ctx context.Context, rt Runtime, exists bool) (string, error) {
	password, err := NewPassword()
	if err != nil {
		return "", err
	}
	statement := "CREATE ROLE " + appRole + " LOGIN PASSWORD '" + password + "'"
	if exists {
		statement = "ALTER ROLE " + appRole + " PASSWORD '" + password + "'"
	}
	if _, err := ownerPSQL(ctx, rt, "SET log_statement = 'none'; SET log_min_error_statement = 'panic'; SET log_min_duration_statement = -1; "+statement+";", true); err != nil {
		return "", err
	}
	return password, nil
}
