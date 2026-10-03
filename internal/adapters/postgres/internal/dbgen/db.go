// Package dbgen is private SQL persistence code. These temporary declarations
// make the query behavior test executable before sqlc implements the queries.
package dbgen

type Queries struct{}

func New(any) *Queries { return &Queries{} }
