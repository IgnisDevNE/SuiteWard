package governance

import (
	"fmt"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// ServiceEmitter is the principal that emits the integrity evidence SuiteWard
// itself observes (D-COMMANDS). RecordAssessment accepts no other emitter.
var ServiceEmitter = mustPrincipal("suiteward", contract.Service)

// mustPrincipal cannot panic for the built-in values: a non-blank id and a valid kind.
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

// Validate reports ErrInvalidRequest when the write is incomplete, names
// another Suite than the locked one, carries evidence not emitted by
// ServiceEmitter, or carries evidence bound to another proposal: an assessment
// and its evidence are both rebuilt from revisions of one proposal.
func (w AssessmentWrite) Validate(project contract.ProjectID, suite contract.SuiteID) error {
	cited := w.Evidence.Binding().Reference()
	switch {
	case w.Reference.ProjectID != project || w.Reference.SuiteID != suite:
		return fmt.Errorf("%w: an assessment belongs to the locked suite", ErrInvalidRequest)
	case strings.TrimSpace(string(w.Reference.ProposalID)) == "" || strings.TrimSpace(string(w.Reference.RevisionID)) == "" || strings.TrimSpace(string(w.Source)) == "":
		return fmt.Errorf("%w: an assessment names a proposal revision and a source", ErrInvalidRequest)
	case w.Evidence.IsZero():
		return fmt.Errorf("%w: an assessment carries evidence", ErrInvalidRequest)
	case w.Evidence.EmitterID() != ServiceEmitter.ID():
		return fmt.Errorf("%w: evidence is emitted by %q, not %q", ErrInvalidRequest, w.Evidence.EmitterID(), ServiceEmitter.ID())
	case cited.ProjectID != project || cited.SuiteID != suite || cited.ProposalID != w.Reference.ProposalID:
		return fmt.Errorf("%w: evidence cites a revision of another proposal", ErrInvalidRequest)
	}
	return nil
}

// Matches reports whether stored holds exactly the evidence of the write.
func (w AssessmentWrite) Matches(stored contract.IntegrityAssessment) bool {
	evidence, present := stored.Evidence()
	return present && evidence.EmitterID() == w.Evidence.EmitterID() && evidence.Source() == w.Evidence.Source() &&
		evidence.Outcome() == w.Evidence.Outcome() && evidence.Binding().Equal(w.Evidence.Binding())
}
