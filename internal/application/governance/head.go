package governance

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// ErrNoCanonical reports that the Suite has no canonical yet: no change proposal exists before one does (D-CHECKS).
var ErrNoCanonical = errors.New("the suite has no canonical yet")

// ErrCanonicalMoved reports that the canonical changed between the classification of a head and the write of its
// proposal. It is an ErrConflict a caller can retry on its next run, unlike a conflicting assessment.
var ErrCanonicalMoved = fmt.Errorf("the canonical moved while the head was assessed: %w", ErrConflict)

// ContentReader reads artifact bytes by digest. filesystem.Store implements it.
type ContentReader interface {
	Read(ctx context.Context, digest artifact.Digest) ([]byte, error)
}

type HeadRequest struct {
	Project contract.ProjectID
	Suite   contract.SuiteID
	Carrier contract.ApprovalCarrierID // github-pr:<repository id>/<pull>
	Head    contract.SourceRevision    // the PR head SHA
	Tree    inventory.Tree             // that commit's source tree (AssessHead only)
	// Revision builds the comment about a revision this call commits; false writes no message. Callers that want no
	// comment pass a function that returns false.
	Revision func(RevisionNotice) (OutboxMessage, bool, error)
}

// RevisionNotice is what the comment about a new change-proposal revision states.
type RevisionNotice struct {
	Reference                contract.ProposalReference
	Previous                 *contract.ProposalReference // the revision it supersedes, nil for R1
	Canonical                contract.SuiteVersionID     // the base version
	Added, Modified, Removed []string                    // inventory.Classify against the canonical, sorted
	Reduced                  bool                        // a scope reduction
	// ReducedRules and ReducedPaths detail the reduction (inventory.Classify), sorted, and are empty unless Reduced.
	ReducedRules, ReducedPaths []string
}

type HeadResult struct {
	Change    inventory.Change            // Unchanged, ProtectedChange or ScopeReduction
	Reference *contract.ProposalReference // the PR's current revision; nil when Unchanged
	Ready     bool                        // CheckPromotionReadiness answered ready for Head
	Reason    contract.PromotionReason    // when not Ready
	Canonical contract.SuiteVersionID
	Revision  int64 // the Suite revision the result was computed at
}

// AssessHead classifies Head against the canonical and, for a protected change, keeps the PR's proposal
// current, records the head's integrity assessment and evaluates readiness for Head. A tree is never read
// while the Suite is locked: the canonical is read first, the tree and the content store are used outside the
// lock, and the proposal is written in a second unit of work that fails with ErrConflict when the canonical
// moved in between.
func AssessHead(ctx context.Context, uow UnitOfWork, reader ContentReader, writer ContentWriter, request HeadRequest) (HeadResult, error) {
	switch {
	case uow == nil:
		return HeadResult{}, fmt.Errorf("%w: no unit of work", ErrInvalidRequest)
	case reader == nil:
		return HeadResult{}, fmt.Errorf("%w: no content reader", ErrInvalidRequest)
	case writer == nil:
		return HeadResult{}, fmt.Errorf("%w: no content writer", ErrInvalidRequest)
	case request.Tree == nil:
		return HeadResult{}, fmt.Errorf("%w: no tree", ErrInvalidRequest)
	case request.Revision == nil:
		return HeadResult{}, fmt.Errorf("%w: no revision comment function", ErrInvalidRequest)
	case strings.TrimSpace(string(request.Head)) == "" || strings.TrimSpace(string(request.Carrier)) == "":
		return HeadResult{}, fmt.Errorf("%w: a head assessment names a head and a carrier", ErrInvalidRequest)
	}
	var canonical contract.CanonicalSnapshot
	err := uow.Do(ctx, request.Project, request.Suite, func(ctx context.Context, tx Tx) error {
		state, err := tx.Suite(ctx)
		if err != nil {
			return fmt.Errorf("load suite: %w", err)
		}
		canonical = state.Canonical
		return nil
	})
	if err != nil {
		return HeadResult{}, err
	}
	version, present := canonical.Suite().CurrentVersionID()
	if !present {
		return HeadResult{}, ErrNoCanonical
	}
	governing, err := governingOf(ctx, reader, canonical)
	if err != nil {
		return HeadResult{}, err
	}
	classification, inv, err := inventory.Classify(ctx, governing, request.Tree)
	if err != nil {
		return HeadResult{}, fmt.Errorf("classify the head: %w", err)
	}
	if classification.Change == inventory.Unchanged {
		return HeadResult{Change: inventory.Unchanged, Canonical: version, Revision: int64(canonical.Suite().Revision())}, nil
	}
	proposed, err := inv.Contract()
	if err != nil {
		return HeadResult{}, fmt.Errorf("contract of the head: %w", err)
	}
	if err := storeContent(ctx, request.Tree, proposed.Manifest(), writer); err != nil {
		return HeadResult{}, err
	}
	result := HeadResult{Change: classification.Change, Canonical: version}
	err = uow.Do(ctx, request.Project, request.Suite, func(ctx context.Context, tx Tx) error {
		state, err := tx.Suite(ctx)
		if err != nil {
			return fmt.Errorf("load suite: %w", err)
		}
		if current, _ := state.Canonical.Suite().CurrentVersionID(); current != version {
			return fmt.Errorf("%w: from %q to %q", ErrCanonicalMoved, version, current)
		}
		revised, previous, err := keepProposalCurrent(ctx, tx, state, request, proposed)
		if err != nil {
			return err
		}
		reference := revised.Current
		if revised.Committed {
			notice := RevisionNotice{Reference: reference, Previous: previous, Canonical: version, Added: classification.Added, Modified: classification.Modified,
				Removed: classification.Removed, Reduced: classification.Change == inventory.ScopeReduction,
				ReducedRules: classification.ReducedRules, ReducedPaths: classification.ReducedPaths}
			message, send, err := request.Revision(notice)
			if err != nil {
				return fmt.Errorf("build the revision comment: %w", err)
			}
			if send {
				if err := writeOutbox(ctx, tx, []OutboxMessage{message}); err != nil {
					return err
				}
			}
		}
		proposal, consent, err := tx.Proposal(ctx, reference.ProposalID)
		if err != nil {
			return fmt.Errorf("load proposal: %w", err)
		}
		evidence, err := HeadEvidence(inv, proposal.Current().Binding(), request.Head)
		if err != nil {
			return err
		}
		if err := tx.RecordAssessment(ctx, AssessmentWrite{Reference: reference, Source: request.Head, Evidence: evidence}); err != nil {
			return fmt.Errorf("record the assessment of the head: %w", err)
		}
		assessment, found, err := tx.Assessment(ctx, reference, request.Head)
		if err != nil {
			return fmt.Errorf("load assessment: %w", err)
		}
		if !found {
			return fmt.Errorf("%w: the assessment of %v at %q is missing right after it was recorded", ErrInvalidState, reference, request.Head)
		}
		decision, err := contract.CheckPromotionReadiness(contract.PromotionContext{
			Canonical: state.Canonical, Proposed: proposed, Proposal: proposal, Reference: reference, Carrier: request.Carrier,
			Policy: state.Policy, Consent: consent, Assessment: assessment,
		}, request.Head)
		if err != nil {
			return fmt.Errorf("check readiness: %w", err)
		}
		written, err := tx.Suite(ctx) // the revision this answer holds at, after its own writes
		if err != nil {
			return fmt.Errorf("load suite: %w", err)
		}
		result.Reference = &reference
		result.Ready, result.Reason = decision.Outcome() == contract.PromotionReady, decision.Reason()
		result.Revision = int64(written.Canonical.Suite().Revision())
		return nil
	})
	if err != nil {
		return HeadResult{}, err
	}
	return result, nil
}

// keepProposalCurrent makes the carrier's change proposal cover proposed under the locked Suite's canonical and
// policy: it creates the proposal with R1 when the carrier has none, adds R<m+1> when the coverage changed,
// and otherwise leaves the current revision, so consent survives an implementation-only push. It returns the
// proposal's current reference, whether it was appended, and the revision it superseded (nil when there was none).
func keepProposalCurrent(ctx context.Context, tx Tx, state SuiteState, request HeadRequest, proposed contract.ProtectedContract) (revised ReviseResult, previous *contract.ProposalReference, err error) {
	id, found, err := tx.ChangeProposalFor(ctx, request.Carrier)
	if err != nil {
		return ReviseResult{}, nil, fmt.Errorf("find the change proposal of %q: %w", request.Carrier, err)
	}
	next := 1
	if found {
		proposal, _, err := tx.Proposal(ctx, id)
		if err != nil {
			return ReviseResult{}, nil, fmt.Errorf("load proposal: %w", err)
		}
		last := proposal.Current().Binding().Reference()
		previous = &last
		next = len(proposal.Revisions()) + 1
	} else if id, err = tx.NextProposalID(ctx); err != nil {
		return ReviseResult{}, nil, fmt.Errorf("allocate a proposal id: %w", err)
	}
	suite := state.Canonical.Suite()
	expected, _ := suite.CurrentVersionID()
	binding, err := contract.NewApprovalBinding(contract.BindingInput{
		Reference:         contract.ProposalReference{ProjectID: suite.ProjectID(), SuiteID: suite.ID(), ProposalID: id, RevisionID: contract.ProposalRevisionID("R" + strconv.Itoa(next))},
		ExpectedCanonical: expected, Manifest: proposed.Manifest().Digest(), Scope: proposed.ScopeDigest(), PolicyRevision: state.Policy.RevisionID(), CoveredInputs: proposed.CoveredInputs(),
	})
	if err != nil {
		return ReviseResult{}, nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	revision, err := contract.NewProposalRevision(binding, request.Head, contract.OriginPull, request.Carrier)
	if err != nil {
		return ReviseResult{}, nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	revised, err = reviseWithin(ctx, tx, state, revision)
	if err != nil {
		return ReviseResult{}, nil, err
	}
	return revised, previous, nil
}

// governingOf rebuilds the governing scope and exact inventory of the canonical. The scope comes from the
// canonical's own .suiteward.yml bytes, read back from the content store, and must hash to the digest the
// canonical was approved with.
func governingOf(ctx context.Context, reader ContentReader, canonical contract.CanonicalSnapshot) (inventory.Governing, error) {
	protected := canonical.Contract()
	declaration := scope.NoDeclaration()
	for _, entry := range protected.Manifest().Entries() {
		if entry.Path != scope.DeclarationPath {
			continue
		}
		raw, err := reader.Read(ctx, entry.Content)
		if err != nil {
			return inventory.Governing{}, fmt.Errorf("read the canonical %s: %w", scope.DeclarationPath, err)
		}
		if declaration, err = scope.ParseDeclaration(raw); err != nil {
			// Not wrapped: the canonical's declaration is stored state, not the owner's source under review.
			return inventory.Governing{}, fmt.Errorf("%w: the canonical %s does not parse: %v", ErrInvalidState, scope.DeclarationPath, err)
		}
	}
	governing := scope.NewScope(declaration)
	if governing.Digest() != protected.ScopeDigest() {
		return inventory.Governing{}, fmt.Errorf("%w: the canonical's declaration does not hash to its approved scope", ErrInvalidState)
	}
	return inventory.Governing{Scope: governing, Manifest: protected.Manifest()}, nil
}

// HeadEvidence is the integrity evidence of the service about source: Passed when the protected inventory
// of source equals what binding covers (manifest and scope), Failed otherwise. AssessHead and the bootstrap
// share it.
func HeadEvidence(inv inventory.Inventory, binding contract.ApprovalBinding, source contract.SourceRevision) (contract.IntegrityEvidence, error) {
	outcome := contract.IntegrityFailed
	if inv.Manifest().Digest() == binding.ManifestDigest() && inv.Scope().Digest() == binding.ScopeDigest() {
		outcome = contract.IntegrityPassed
	}
	evidence, err := contract.NewIntegrityEvidence(ServiceEmitter.ID(), source, binding, outcome)
	if err != nil {
		return contract.IntegrityEvidence{}, fmt.Errorf("build the integrity evidence: %w", err)
	}
	return evidence, nil
}

// proposalNumber is n of a proposal named P<n> in canonical decimal, and 0 for any other name.
func proposalNumber(id contract.ProposalID) uint64 {
	digits, ok := strings.CutPrefix(string(id), "P")
	n, err := strconv.ParseUint(digits, 10, 64)
	if !ok || err != nil || strconv.FormatUint(n, 10) != digits {
		return 0
	}
	return n
}

// NextNumberedProposalID returns P<n+1>, n being the highest number among ids named P<number> in canonical
// decimal. Other names are not part of the numbering. Both stores use it so they number alike.
func NextNumberedProposalID(ids []contract.ProposalID) contract.ProposalID {
	var highest uint64
	for _, id := range ids {
		highest = max(highest, proposalNumber(id))
	}
	return contract.ProposalID("P" + strconv.FormatUint(highest+1, 10))
}

// LatestProposalID returns the newest of ids, the one with the highest number among those named P<number>. A name that is
// not numbered ranks below every numbered one, and such names are ordered by name. False when ids is empty. Both stores use
// it for LatestBootstrapProposal so they agree.
func LatestProposalID(ids []contract.ProposalID) (contract.ProposalID, bool) {
	if len(ids) == 0 {
		return "", false
	}
	return slices.MaxFunc(ids, func(a, b contract.ProposalID) int {
		return cmp.Or(cmp.Compare(proposalNumber(a), proposalNumber(b)), strings.Compare(string(a), string(b)))
	}), true
}
