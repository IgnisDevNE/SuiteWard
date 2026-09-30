package contract_test

import (
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func TestPrincipalPreservesResolvedIdentity(t *testing.T) {
	for _, kind := range []contract.PrincipalKind{contract.Human, contract.Agent, contract.Service} {
		for _, id := range []contract.PrincipalID{"owner-1", " Owner-1 ", "proprietário-一"} {
			principal, err := contract.NewPrincipal(id, kind)
			if err != nil {
				t.Fatalf("NewPrincipal(%q, %d): %v", id, kind, err)
			}
			if principal.ID() != id || principal.Kind() != kind {
				t.Errorf("NewPrincipal(%q, %d) = identity %q, kind %d; want unchanged inputs", id, kind, principal.ID(), principal.Kind())
			}
		}
	}
}

func TestPrincipalRejectsInvalidIdentity(t *testing.T) {
	tests := []struct {
		name string
		id   contract.PrincipalID
		kind contract.PrincipalKind
	}{
		{"empty ID", "", contract.Human},
		{"blank ID", " \t\r\n", contract.Human},
		{"Unicode whitespace ID", "\u2003", contract.Agent},
		{"zero kind", "owner-1", 0},
		{"unknown kind", "owner-1", 255},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			principal, err := contract.NewPrincipal(tt.id, tt.kind)
			if !errors.Is(err, contract.ErrInvalidPrincipal) {
				t.Errorf("NewPrincipal(%q, %d) error = %v; want ErrInvalidPrincipal", tt.id, tt.kind, err)
			}
			if principal != (contract.Principal{}) {
				t.Errorf("rejected identity returned a constructed principal: %v", principal)
			}
		})
	}
}

func TestPrincipalZeroValueIsUnconstructed(t *testing.T) {
	var principal contract.Principal
	if principal.ID() != "" || principal.Kind() != 0 {
		t.Errorf("zero principal = identity %q, kind %d; want absent identity", principal.ID(), principal.Kind())
	}
}
