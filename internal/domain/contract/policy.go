package contract

import (
	"errors"
	"strings"
)

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
	if strings.TrimSpace(string(project)) == "" || strings.TrimSpace(string(revision)) == "" || owner.kind != Human {
		return Policy{}, ErrInvalidPolicy
	}
	return Policy{project: project, revision: revision, owner: owner.id}, nil
}

// ProjectID returns the internal project identity governed by the policy.
func (p Policy) ProjectID() ProjectID { return p.project }

// RevisionID returns the immutable policy revision identity.
func (p Policy) RevisionID() PolicyRevisionID { return p.revision }

// OwnerID returns the registered human owner's identity.
func (p Policy) OwnerID() PrincipalID { return p.owner }

// CanApprove checks owner authority, not proposal eligibility or existing consent.
func (p Policy) CanApprove(actor Principal) bool { return p.isOwner(actor) }

// CanAdminister checks project owner authority without bypassing governance.
func (p Policy) CanAdminister(actor Principal) bool { return p.isOwner(actor) }

// CanRequestPriority checks authority for the separate scheduling operation.
func (p Policy) CanRequestPriority(actor Principal) bool { return p.isOwner(actor) }

// CanAuthorizePolicyChange evaluates authority under this governing policy.
// The caller must supply the current policy, never substitute the candidate.
func (p Policy) CanAuthorizePolicyChange(actor Principal) bool { return p.isOwner(actor) }

// CanRevoke permits the registered human owner to withdraw only their own consent.
func (p Policy) CanRevoke(actor Principal, approvalAuthor PrincipalID) bool {
	return p.isOwner(actor) && actor.id == approvalAuthor
}

func (p Policy) isOwner(actor Principal) bool {
	return p.owner != "" && actor.kind == Human && actor.id == p.owner
}
