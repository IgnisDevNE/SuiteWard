// Package contract contains provider-independent canonical contract rules.
package contract

import "errors"

// ProjectID identifies a SuiteWard project, independently of its source provider.
type ProjectID string

// PrincipalID identifies an internally resolved principal.
type PrincipalID string

// PrincipalKind distinguishes human authority from agent and service identities.
type PrincipalKind uint8

const (
	Human PrincipalKind = iota + 1
	Agent
	Service
)

// ErrInvalidPrincipal identifies an invalid internal identity or principal kind.
var ErrInvalidPrincipal = errors.New("invalid principal")

// Principal is an immutable identity value, not a proof of authentication.
type Principal struct {
	id   PrincipalID
	kind PrincipalKind
}

// NewPrincipal constructs an identity already resolved by a trusted caller.
func NewPrincipal(id PrincipalID, kind PrincipalKind) (Principal, error) {
	return Principal{}, nil
}

// ID returns the unmodified internal principal identity.
func (p Principal) ID() PrincipalID { return p.id }

// Kind returns the principal kind; zero identifies an unconstructed value.
func (p Principal) Kind() PrincipalKind { return p.kind }
