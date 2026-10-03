// Package migrations applies the reviewed PostgreSQL schema sequence.
package migrations

import "context"

// Up applies the supported schema sequence. Its behavior is introduced by the
// schema task after the compiled fresh-install RED checkpoint.
func Up(context.Context, string) error { return nil }

// UpTo applies migrations through a supported forward version.
func UpTo(context.Context, string, int64) error { return nil }
