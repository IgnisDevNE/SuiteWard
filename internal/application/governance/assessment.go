package governance

import (
	"fmt"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// ServiceEmitter is the principal that emits the integrity evidence SuiteWard
// itself observes (D-COMMANDS).
var ServiceEmitter = mustPrincipal("suiteward", contract.Service)

func mustPrincipal(id contract.PrincipalID, kind contract.PrincipalKind) contract.Principal {
	principal, err := contract.NewPrincipal(id, kind)
	if err != nil {
		panic(fmt.Sprintf("governance: invalid built-in principal %q: %v", id, err))
	}
	return principal
}

// AssessmentWrite is one integrity assessment of a proposal revision at a source.
type AssessmentWrite struct {
	Reference contract.ProposalReference
	Source    contract.SourceRevision
	Evidence  contract.IntegrityEvidence // emitted by ServiceEmitter
}
