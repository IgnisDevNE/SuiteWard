package postgres

import (
	"bytes"
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func TestStoredAuthorityRejectsVersionAliases(t *testing.T) {
	owner := codecValue(contract.NewPrincipal("owner", contract.Human))
	policy := codecValue(contract.NewPolicy("project", "policy", owner))
	suite := codecValue(contract.NewSuite("project", "suite", "", 0))
	canonical := codecValue(contract.NewCanonicalSnapshot(suite, contract.SuiteVersion{}, contract.ProtectedContract{}, contract.PromotionRecord{}))
	schedule := codecValue(contract.NewSchedule("project", "suite"))
	encoded, err := encodeAuthority(authorityState{canonical: canonical, policy: policy, scheduling: schedule, target: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeAuthority(encoded, "project", "suite", "", "0"); err != nil {
		t.Fatalf("valid stored authority cannot round trip: %v", err)
	}
	for _, alias := range []string{"Version", "VERSION", "verſion"} {
		t.Run(alias, func(t *testing.T) {
			mutated := bytes.Replace(encoded, []byte(`"version":1`), []byte(`"version":99,"`+alias+`":1`), 1)
			if bytes.Equal(encoded, mutated) {
				t.Fatal("fixture did not replace the version field")
			}
			if _, err := decodeAuthority(mutated, "project", "suite", "", "0"); !errors.Is(err, governance.ErrInvalidSnapshot) {
				t.Fatalf("unsupported authority version hidden by field alias %q was accepted: %v", alias, err)
			}
		})
	}
}

func codecValue[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
