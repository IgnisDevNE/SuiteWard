package migrations

import "context"

// GrantApp gives the application role the data access of the service in the schema of databaseURL. Stub: the grants
// come with the implementation.
func GrantApp(_ context.Context, _, _ string) error { return nil }
