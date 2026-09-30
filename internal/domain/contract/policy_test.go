package contract_test

import (
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func principalForTest(t *testing.T, id contract.PrincipalID, kind contract.PrincipalKind) contract.Principal {
	t.Helper()
	principal, err := contract.NewPrincipal(id, kind)
	if err != nil {
		t.Fatal(err)
	}
	return principal
}

func TestPolicyPreservesSuppliedIdentities(t *testing.T) {
	owner := principalForTest(t, " Owner-一 ", contract.Human)
	policy, err := contract.NewPolicy(" project-1 ", " revision-2\t", owner)
	if err != nil {
		t.Fatal(err)
	}
	if policy.ProjectID() != " project-1 " || policy.RevisionID() != " revision-2\t" || policy.OwnerID() != owner.ID() {
		t.Errorf("policy = project %q, revision %q, owner %q; want supplied identities unchanged", policy.ProjectID(), policy.RevisionID(), policy.OwnerID())
	}
}

func TestPolicyRejectsInvalidIdentityOrOwner(t *testing.T) {
	human := principalForTest(t, "owner", contract.Human)
	agent := principalForTest(t, "owner", contract.Agent)
	service := principalForTest(t, "owner", contract.Service)
	tests := []struct {
		name     string
		project  contract.ProjectID
		revision contract.PolicyRevisionID
		owner    contract.Principal
	}{
		{"empty project", "", "policy-1", human},
		{"blank project", " \t\u2003", "policy-1", human},
		{"empty revision", "project-1", "", human},
		{"blank revision", "project-1", "\n\u2003", human},
		{"unconstructed owner", "project-1", "policy-1", contract.Principal{}},
		{"agent owner", "project-1", "policy-1", agent},
		{"service owner", "project-1", "policy-1", service},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy, err := contract.NewPolicy(tt.project, tt.revision, tt.owner)
			if !errors.Is(err, contract.ErrInvalidPolicy) {
				t.Errorf("NewPolicy error = %v; want ErrInvalidPolicy", err)
			}
			if policy != (contract.Policy{}) {
				t.Errorf("rejected policy returned a constructed value: %v", policy)
			}
		})
	}
}

func policyForTest(t *testing.T, owner contract.Principal) contract.Policy {
	t.Helper()
	policy, err := contract.NewPolicy("project-1", "policy-1", owner)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func TestPolicyOnlyRegisteredHumanOwnerHasPrivilegedCapabilities(t *testing.T) {
	owner := principalForTest(t, "owner", contract.Human)
	policy := policyForTest(t, owner)
	tests := []struct {
		name    string
		actor   contract.Principal
		allowed bool
	}{
		{"registered owner including their own proposal", owner, true},
		{"unregistered repository administrator", principalForTest(t, "repo-admin", contract.Human), false},
		{"agent", principalForTest(t, "agent", contract.Agent), false},
		{"service", principalForTest(t, "service", contract.Service), false},
		{"agent sharing owner ID", principalForTest(t, owner.ID(), contract.Agent), false},
		{"service sharing owner ID", principalForTest(t, owner.ID(), contract.Service), false},
		{"different exact ID", principalForTest(t, "owner ", contract.Human), false},
		{"unconstructed principal", contract.Principal{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capabilities := []struct {
				name    string
				allowed bool
			}{
				{"approve", policy.CanApprove(tt.actor)},
				{"administer", policy.CanAdminister(tt.actor)},
				{"request priority", policy.CanRequestPriority(tt.actor)},
				{"authorize policy change", policy.CanAuthorizePolicyChange(tt.actor)},
			}
			for _, capability := range capabilities {
				if capability.allowed != tt.allowed {
					t.Errorf("%s = %t; want %t", capability.name, capability.allowed, tt.allowed)
				}
			}
		})
	}
}

func TestPolicyRevocationRequiresOwnConsent(t *testing.T) {
	owner := principalForTest(t, "owner", contract.Human)
	policy := policyForTest(t, owner)
	agent := principalForTest(t, owner.ID(), contract.Agent)
	service := principalForTest(t, owner.ID(), contract.Service)
	otherHuman := principalForTest(t, "other-human", contract.Human)
	tests := []struct {
		name    string
		actor   contract.Principal
		author  contract.PrincipalID
		allowed bool
	}{
		{"owner withdraws own consent", owner, owner.ID(), true},
		{"owner cannot withdraw someone else's consent", owner, otherHuman.ID(), false},
		{"absent author", owner, "", false},
		{"different exact author ID", owner, "owner ", false},
		{"agent cannot impersonate owner", agent, owner.ID(), false},
		{"service cannot impersonate owner", service, owner.ID(), false},
		{"unregistered human cannot use own authorship as authority", otherHuman, otherHuman.ID(), false},
		{"unconstructed principal and absent author", contract.Principal{}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := policy.CanRevoke(tt.actor, tt.author); got != tt.allowed {
				t.Errorf("CanRevoke = %t; want %t", got, tt.allowed)
			}
		})
	}
}

func TestPolicyCandidateCannotAuthorizeItsOwnReplacement(t *testing.T) {
	currentOwner := principalForTest(t, "current-owner", contract.Human)
	candidateOwner := principalForTest(t, "candidate-owner", contract.Human)
	governing := policyForTest(t, currentOwner)
	candidate, err := contract.NewPolicy(governing.ProjectID(), "candidate-policy", candidateOwner)
	if err != nil {
		t.Fatal(err)
	}
	// Constructing a candidate snapshot does not install it as the authority.
	if !candidate.CanAuthorizePolicyChange(candidateOwner) {
		t.Fatal("candidate owner would have authority only under their proposed policy")
	}
	if governing.CanAuthorizePolicyChange(candidateOwner) {
		t.Fatal("candidate owner gained authority under the governing policy")
	}
	if !governing.CanAuthorizePolicyChange(currentOwner) {
		t.Fatal("the current human owner must retain governing-policy authority")
	}
	if governing.OwnerID() != currentOwner.ID() || governing.RevisionID() != "policy-1" {
		t.Fatal("candidate construction or capability inspection changed the governing snapshot")
	}
}

func TestPolicyZeroValueDeniesEveryPrivilegedCapability(t *testing.T) {
	var policy contract.Policy
	owner := principalForTest(t, "owner", contract.Human)
	for _, actor := range []contract.Principal{owner, {}} {
		if policy.CanApprove(actor) || policy.CanAdminister(actor) || policy.CanRequestPriority(actor) || policy.CanAuthorizePolicyChange(actor) || policy.CanRevoke(actor, actor.ID()) {
			t.Error("unconstructed policy granted authority")
		}
	}
}
