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
