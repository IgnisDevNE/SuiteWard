package contract

import "errors"

// PolicyRevisionID identifies one immutable governing policy revision.
type PolicyRevisionID string

// ErrInvalidPolicy identifies an invalid policy identity or owner.
var ErrInvalidPolicy = errors.New("invalid policy")

// Policy is an immutable snapshot of the MVP's registered human owner.
type Policy struct {
	project  ProjectID
	revision PolicyRevisionID
	owner    PrincipalID
}

// NewPolicy constructs a policy snapshot, without authorizing its adoption.
func NewPolicy(project ProjectID, revision PolicyRevisionID, owner Principal) (Policy, error) {
	return Policy{}, nil
}

// ProjectID returns the internal project identity governed by the policy.
func (p Policy) ProjectID() ProjectID { return p.project }

// RevisionID returns the immutable policy revision identity.
func (p Policy) RevisionID() PolicyRevisionID { return p.revision }

// OwnerID returns the registered human owner's identity.
func (p Policy) OwnerID() PrincipalID { return p.owner }
