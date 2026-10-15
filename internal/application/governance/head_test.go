package governance_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

const (
	bootstrapCarrier = contract.ApprovalCarrierID("github-pr:1/1") // the PR that carried the baseline
	prA              = contract.ApprovalCarrierID("github-pr:1/2")
	prB              = contract.ApprovalCarrierID("github-pr:1/3")
	declaration      = "version: 1\ninclude: [/tests/**]\n"
)

// fakeReader serves what a fakeWriter stored; replace substitutes bytes and onRead runs once before the first read.
type fakeReader struct {
	writer  *fakeWriter
	replace map[artifact.Digest]string
	onRead  func()
}

func (r *fakeReader) Read(_ context.Context, digest artifact.Digest) ([]byte, error) {
	if hook := r.onRead; hook != nil {
		r.onRead = nil
		hook()
	}
	if content, replaced := r.replace[digest]; replaced {
		return []byte(content), nil
	}
	content, found := r.writer.stored[digest]
	if !found {
		return nil, fmt.Errorf("no content %s", digest)
	}
	return []byte(content), nil
}

// headTree is the baseline tree with a chosen protected test file and implementation file.
func headTree(test, src string) *fakeTree {
	return newFakeTree(
		regularFile(scope.DeclarationPath, declaration),
		regularFile(".github/workflows/ci.yml", "on: push\n"),
		regularFile("tests/a_test.go", test),
		regularFile("src/main.go", src),
	)
}

// headEnv is a Suite whose first canonical (version-1) came from the baseline PR, with every content blob in writer.
type headEnv struct {
	*world
	writer *fakeWriter
	reader *fakeReader
	other  candidate // an approved candidate that can move the canonical to version-2
	order  contract.CommandOrder
}

// newHeadEnv seeds extra proposals next to the one that can move the canonical.
func newHeadEnv(t *testing.T, extra ...candidate) *headEnv {
	t.Helper()
	// A real inventory, so that once promoted its declaration is readable and hashes to its scope.
	other := newCandidate(t, "px", "version-1", must(must(inventory.Build(context.Background(), headTree("other", "package main\n"))).Contract()))
	e := &headEnv{world: newWorld(t, append([]candidate{other}, extra...)...), writer: newFakeWriter(), other: other}
	e.reader = &fakeReader{writer: e.writer}
	result := e.propose(e.writer, governance.BaselineRequest{
		Reference: contract.ProposalReference{ProjectID: projectID, SuiteID: suiteID, ProposalID: "P1", RevisionID: "R1"},
		Carrier:   bootstrapCarrier, Origin: "pinned", OriginKind: contract.OriginBranch, Tree: headTree("v1", "package main\n"),
	})
	e.approveRef(result.Revise.Current, bootstrapCarrier)
	e.recordAssessment(result.Revise.Current, "pinned", contract.IntegrityPassed)
	integration := must(contract.NewIntegration(projectID, target, "pinned", "", contract.IntegrationExistingBaseline))
	promoted := e.bootstrap(governance.PromoteRequest{OperationID: "bootstrap-P1-R1", Reference: result.Revise.Current, Carrier: bootstrapCarrier,
		Proposed: result.Contract, AssessmentSource: "pinned", Integration: integration, NewVersionID: "version-1", RecordedAt: recordedAt})
	if !promoted.Committed {
		t.Fatalf("bootstrap = %+v, want a first canonical", promoted)
	}
	return e
}

// approveRef records the owner's approval of the exact revision at reference, carried by carrier.
func (e *headEnv) approveRef(reference contract.ProposalReference, carrier contract.ApprovalCarrierID) {
	e.t.Helper()
	e.order++
	name := fmt.Sprintf("%s-%s-%d", reference.ProposalID, reference.RevisionID, e.order)
	command := must(contract.NewCommand(contract.CommandInput{OperationID: contract.OperationID("op-" + name), SourceCommandID: contract.SourceCommandID("source-" + name),
		Actor: e.owner, Reference: reference, Carrier: carrier, Action: contract.ApproveConsent, Order: e.order}))
	if response := e.consent(command); !response.Receipt.CurrentApprovalEligible {
		e.t.Fatalf("approval of %v is not eligible: %v", reference, response.Receipt.Result.Reason())
	}
}

func (e *headEnv) request(carrier contract.ApprovalCarrierID, head string, tree inventory.Tree) governance.HeadRequest {
	return governance.HeadRequest{Project: projectID, Suite: suiteID, Carrier: carrier, Head: contract.SourceRevision(head), Tree: tree, Revision: noRevision}
}

func (e *headEnv) assess(carrier contract.ApprovalCarrierID, head string, tree inventory.Tree) governance.HeadResult {
	e.t.Helper()
	result, err := governance.AssessHead(context.Background(), e.mem, e.reader, e.writer, e.request(carrier, head, tree))
	if err != nil {
		e.t.Fatalf("assess head %s: %v", head, err)
	}
	return result
}

func (e *headEnv) assessErr(carrier contract.ApprovalCarrierID, head string, tree inventory.Tree) error {
	e.t.Helper()
	_, err := governance.AssessHead(context.Background(), e.mem, e.reader, e.writer, e.request(carrier, head, tree))
	if err == nil {
		e.t.Fatalf("assess head %s succeeded, want an error", head)
	}
	return err
}

func (e *headEnv) assessment(reference contract.ProposalReference, head string) (contract.IntegrityAssessment, bool) {
	e.t.Helper()
	var assessment contract.IntegrityAssessment
	var found bool
	e.read(func(ctx context.Context, tx governance.Tx) (err error) {
		assessment, found, err = tx.Assessment(ctx, reference, contract.SourceRevision(head))
		return err
	})
	return assessment, found
}

func (e *headEnv) revisions(id contract.ProposalID) int { return len(e.proposal(id).Revisions()) }

func (e *headEnv) requireChange(result governance.HeadResult, want inventory.Change, reference string, ready bool, reason contract.PromotionReason) {
	e.t.Helper()
	got := "none"
	if result.Reference != nil {
		got = fmt.Sprintf("%s-%s", result.Reference.ProposalID, result.Reference.RevisionID)
	}
	if result.Change != want || got != reference || result.Ready != ready || result.Reason != reason {
		e.t.Fatalf("result = change %d, reference %s, ready %v, reason %d; want change %d, reference %s, ready %v, reason %d",
			result.Change, got, result.Ready, result.Reason, want, reference, ready, reason)
	}
}

func TestAssessHeadOfAnUnchangedTreeWritesNothing(t *testing.T) {
	for name, tree := range map[string]*fakeTree{
		"identical":           headTree("v1", "package main\n"),
		"implementation-only": headTree("v1", "package main\n\nfunc main() {}\n"),
	} {
		t.Run(name, func(t *testing.T) {
			e := newHeadEnv(t)
			before := e.revision()
			blobs := len(e.writer.stored)

			result := e.assess(prA, "head-a", tree)

			e.requireChange(result, inventory.Unchanged, "none", false, contract.PromotionReasonNone)
			if result.Canonical != "version-1" || result.Revision != int64(before) {
				t.Fatalf("result = canonical %q at revision %d, want version-1 at %d", result.Canonical, result.Revision, before)
			}
			e.requireWrites(before, 0)
			if e.hasProposal("P2") || len(e.writer.stored) != blobs {
				t.Fatalf("something was written: proposal %v, %d new blobs", e.hasProposal("P2"), len(e.writer.stored)-blobs)
			}
		})
	}
}

func TestAssessHeadNeedsACanonical(t *testing.T) {
	w := newWorld(t)
	before := w.revision()
	reader := &fakeReader{writer: newFakeWriter()}
	request := governance.HeadRequest{Project: projectID, Suite: suiteID, Carrier: prA, Head: "head-a", Tree: headTree("v1", "package main\n"), Revision: noRevision}

	_, err := governance.AssessHead(context.Background(), w.mem, reader, newFakeWriter(), request)

	if !errors.Is(err, governance.ErrNoCanonical) {
		t.Fatalf("error = %v, want ErrNoCanonical", err)
	}
	w.requireWrites(before, 0)
	if w.hasProposal("P1") {
		t.Fatal("a proposal was created before any canonical exists")
	}
}

func TestAssessHeadRejectsAGoverningScopeThatIsNotTheCanonicalsDigest(t *testing.T) {
	e := newHeadEnv(t)
	e.reader.replace = map[artifact.Digest]string{artifact.Hash([]byte(declaration)): "version: 1\ninclude: [/other/**]\n"}
	before := e.revision()

	err := e.assessErr(prA, "head-a", headTree("v2", "package main\n"))

	if !errors.Is(err, governance.ErrInvalidState) {
		t.Fatalf("error = %v, want ErrInvalidState", err)
	}
	e.requireWrites(before, 0)
	if e.hasProposal("P2") {
		t.Fatal("a proposal was created")
	}
}

func TestAssessHeadSurfacesSourceErrorsAndWritesNothing(t *testing.T) {
	truncated := errors.New("tree truncated")
	tests := []struct {
		name string
		tree inventory.Tree
		want error
	}{
		{"invalid declaration", newFakeTree(regularFile(scope.DeclarationPath, "")), scope.ErrInvalidDeclaration},
		{"unsafe entry", newFakeTree(regularFile(scope.DeclarationPath, declaration), treeFile{".github/workflows/ci.yml", inventory.Symlink, ""}), inventory.ErrUnsafeEntry},
		{"invalid tree", newFakeTree(regularFile(scope.DeclarationPath, declaration), regularFile("tests/a.go", "x"), regularFile("tests/a.go", "y")), inventory.ErrInvalidTree},
		{"truncated tree", truncatedTree{truncated}, truncated},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			e := newHeadEnv(t)
			before := e.revision()
			blobs := len(e.writer.stored)

			err := e.assessErr(prA, "head-a", test.tree)

			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			e.requireWrites(before, 0)
			if e.hasProposal("P2") || len(e.writer.stored) != blobs {
				t.Fatal("something was written")
			}
		})
	}
}

// truncatedTree fails the way a tree GitHub truncated does: the error wraps a marker the caller can match.
type truncatedTree struct{ marker error }

func (t truncatedTree) Entries(context.Context) ([]inventory.Entry, error) {
	return nil, fmt.Errorf("list entries: %w", t.marker)
}

func (t truncatedTree) Read(context.Context, string) ([]byte, error) { return nil, t.marker }

func TestFirstProtectedChangeOfAPullCreatesTheNextProposal(t *testing.T) {
	e := newHeadEnv(t)
	before := e.revision()

	result := e.assess(prA, "head-a", headTree("v2", "package main\n"))

	e.requireChange(result, inventory.ProtectedChange, "P2-R1", false, contract.PromotionReasonApprovalMissing)
	if result.Canonical != "version-1" {
		t.Fatalf("canonical = %q, want version-1", result.Canonical)
	}
	revision := e.proposal("P2").Current()
	if revision.Carrier() != prA || revision.Origin() != "head-a" || revision.Binding().ExpectedCanonical() != "version-1" || len(revision.Binding().CoveredInputs()) != 0 {
		t.Fatalf("stored revision = carrier %q origin %q expects %q, want carrier %q origin head-a expecting version-1", revision.Carrier(), revision.Origin(), revision.Binding().ExpectedCanonical(), prA)
	}
	assessment, found := e.assessment(*result.Reference, "head-a")
	if !found || !assessment.Passed() {
		t.Fatalf("assessment at the head = %+v (found %v), want a passed one", assessment, found)
	}
	if _, stored := e.writer.stored[artifact.Hash([]byte("v2"))]; !stored {
		t.Fatal("the changed protected file was not stored")
	}
	// a revision, an assessment: two writes
	e.requireWrites(before, 2)
	if result.Revision != int64(e.revision()) {
		t.Fatalf("result revision = %d, want the revision after the writes, %d", result.Revision, e.revision())
	}
}

func TestImplementationOnlyPushKeepsTheRevisionAndStaysReady(t *testing.T) {
	e := newHeadEnv(t)
	first := e.assess(prA, "head-a", headTree("v2", "package main\n"))
	e.approveRef(*first.Reference, prA)

	pushed := e.assess(prA, "head-b", headTree("v2", "package main\n\nfunc main() {}\n"))

	e.requireChange(pushed, inventory.ProtectedChange, "P2-R1", true, contract.PromotionReasonNone)
	if e.revisions("P2") != 1 {
		t.Fatalf("P2 has %d revisions, want the one it had", e.revisions("P2"))
	}
	if assessment, found := e.assessment(*pushed.Reference, "head-b"); !found || !assessment.Passed() {
		t.Fatalf("assessment at the new head = %+v (found %v), want a passed one", assessment, found)
	}
	before := e.revision()
	e.assess(prA, "head-b", headTree("v2", "package main\n\nfunc main() {}\n"))
	e.requireWrites(before, 0)
}

func TestProtectedPushNeedsAFreshApproval(t *testing.T) {
	e := newHeadEnv(t)
	first := e.assess(prA, "head-a", headTree("v2", "package main\n"))
	e.approveRef(*first.Reference, prA)

	pushed := e.assess(prA, "head-b", headTree("v3", "package main\n"))

	e.requireChange(pushed, inventory.ProtectedChange, "P2-R2", false, contract.PromotionReasonApprovalMissing)
	if e.revisions("P2") != 2 {
		t.Fatalf("P2 has %d revisions, want 2", e.revisions("P2"))
	}
	e.approveRef(*pushed.Reference, prA)
	if again := e.assess(prA, "head-b", headTree("v3", "package main\n")); !again.Ready {
		t.Fatalf("result after approving R2 = %+v, want ready", again)
	}
}

func TestBootstrapCarrierGetsANewProposalForAProtectedChange(t *testing.T) {
	e := newHeadEnv(t)

	result := e.assess(bootstrapCarrier, "head-after-bootstrap", headTree("v2", "package main\n"))

	e.requireChange(result, inventory.ProtectedChange, "P2-R1", false, contract.PromotionReasonApprovalMissing)
	if e.revisions("P1") != 1 {
		t.Fatalf("the baseline proposal P1 has %d revisions, want it untouched", e.revisions("P1"))
	}
}

func TestTwoPullsGetDistinctProposals(t *testing.T) {
	e := newHeadEnv(t)

	a := e.assess(prA, "head-a", headTree("v2", "package main\n"))
	b := e.assess(prB, "head-b", headTree("v2", "package main\n"))

	e.requireChange(a, inventory.ProtectedChange, "P2-R1", false, contract.PromotionReasonApprovalMissing)
	e.requireChange(b, inventory.ProtectedChange, "P3-R1", false, contract.PromotionReasonApprovalMissing)
	if again := e.assess(prA, "head-a", headTree("v2", "package main\n")); again.Reference == nil || *again.Reference != *a.Reference {
		t.Fatalf("repeated assessment of pull A = %+v, want %v", again.Reference, a.Reference)
	}
}

func TestConcurrentAssessmentsOfTwoPullsAllocateDistinctProposals(t *testing.T) {
	e := newHeadEnv(t)
	carriers := []contract.ApprovalCarrierID{prA, prB, "github-pr:1/4", "github-pr:1/5"}
	references := make([]contract.ProposalReference, len(carriers))
	var wg sync.WaitGroup
	for i, carrier := range carriers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := governance.AssessHead(context.Background(), e.mem, e.reader, newFakeWriter(), e.request(carrier, fmt.Sprintf("head-%d", i), headTree("v2", "package main\n")))
			if err != nil || result.Reference == nil {
				t.Errorf("assess %s: result %+v, error %v", carrier, result, err)
				return
			}
			references[i] = *result.Reference
		}()
	}
	wg.Wait()

	seen := map[contract.ProposalID]bool{}
	for _, reference := range references {
		if seen[reference.ProposalID] || reference.ProposalID == "P1" {
			t.Fatalf("references = %+v, want four distinct proposals after P1", references)
		}
		seen[reference.ProposalID] = true
	}
}

func TestAssessHeadReportsAMovedCanonicalAsAConflict(t *testing.T) {
	e := newHeadEnv(t)
	e.reader.onRead = func() {
		e.approve(e.other)
		e.promote(e.other.mergedRequest(t, "promote-px", "version-2"))
	}
	before := e.revision()

	err := e.assessErr(prA, "head-a", headTree("v2", "package main\n"))

	if !errors.Is(err, governance.ErrCanonicalMoved) || !errors.Is(err, governance.ErrConflict) {
		t.Fatalf("error = %v, want ErrCanonicalMoved, which is an ErrConflict", err)
	}
	if e.hasProposal("P2") {
		t.Fatal("a proposal was bound to a canonical the head was not classified against")
	}
	if e.currentVersion() != "version-2" || e.revision() <= before {
		t.Fatalf("the hook did not move the canonical: version %q", e.currentVersion())
	}
}

func TestAssessHeadWritesNothingWhenContentCannotBeStored(t *testing.T) {
	e := newHeadEnv(t)
	e.writer.fail = errors.New("disk full")
	before := e.revision()

	err := e.assessErr(prA, "head-a", headTree("v2", "package main\n"))

	if !errors.Is(err, e.writer.fail) {
		t.Fatalf("error = %v, want the store failure", err)
	}
	e.requireWrites(before, 0)
}

func TestHeadEvidenceComparesTheInventoryToTheBinding(t *testing.T) {
	inv := must(inventory.Build(context.Background(), headTree("v1", "package main\n")))
	reference := contract.ProposalReference{ProjectID: projectID, SuiteID: suiteID, ProposalID: "P2", RevisionID: "R1"}
	bind := func(manifest, scopeDigest artifact.Digest) contract.ApprovalBinding {
		return must(contract.NewApprovalBinding(contract.BindingInput{Reference: reference, ExpectedCanonical: "version-1", Manifest: manifest,
			Scope: scopeDigest, PolicyRevision: policyID, CoveredInputs: map[string]string{}}))
	}
	other := artifact.Hash([]byte("other"))
	tests := []struct {
		name    string
		binding contract.ApprovalBinding
		want    contract.IntegrityOutcome
	}{
		{"same inventory", bind(inv.Manifest().Digest(), inv.Scope().Digest()), contract.IntegrityPassed},
		{"other manifest", bind(other, inv.Scope().Digest()), contract.IntegrityFailed},
		{"other scope", bind(inv.Manifest().Digest(), other), contract.IntegrityFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence, err := governance.HeadEvidence(inv, test.binding, "head-a")

			if err != nil {
				t.Fatal(err)
			}
			if evidence.Outcome() != test.want || evidence.EmitterID() != governance.ServiceEmitter.ID() || evidence.Source() != "head-a" || !evidence.Binding().Equal(test.binding) {
				t.Fatalf("evidence = outcome %v emitter %q source %q, want outcome %v from the service at head-a", evidence.Outcome(), evidence.EmitterID(), evidence.Source(), test.want)
			}
		})
	}
}

func TestAssessHeadRejectsMissingCollaborators(t *testing.T) {
	e := newHeadEnv(t)
	request := e.request(prA, "head-a", headTree("v2", "package main\n"))
	noTree, noHead, noCarrier := request, request, request
	noTree.Tree, noHead.Head, noCarrier.Carrier = nil, "", ""
	tests := []struct {
		name    string
		uow     governance.UnitOfWork
		reader  governance.ContentReader
		writer  governance.ContentWriter
		request governance.HeadRequest
	}{
		{"unit of work", nil, e.reader, e.writer, request},
		{"reader", e.mem, nil, e.writer, request},
		{"writer", e.mem, e.reader, nil, request},
		{"tree", e.mem, e.reader, e.writer, noTree},
		{"head", e.mem, e.reader, e.writer, noHead},
		{"carrier", e.mem, e.reader, e.writer, noCarrier},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := governance.AssessHead(context.Background(), test.uow, test.reader, test.writer, test.request)
			if !errors.Is(err, governance.ErrInvalidRequest) {
				t.Fatalf("error = %v, want an invalid request", err)
			}
		})
	}
}

// The tests below pin behavior that already held when they were written.

// movedCanonical promotes the other candidate, so version-2 becomes the canonical.
func (e *headEnv) movedCanonical() {
	e.t.Helper()
	e.approve(e.other)
	e.promote(e.other.mergedRequest(e.t, "promote-px", "version-2"))
}

func TestApprovedPullNeedsAFreshApprovalOnceTheCanonicalMoves(t *testing.T) {
	e := newHeadEnv(t)
	tree := headTree("v2", "package main\n")
	first := e.assess(prA, "head-a", tree)
	e.approveRef(*first.Reference, prA)
	e.requireChange(e.assess(prA, "head-a", tree), inventory.ProtectedChange, "P2-R1", true, contract.PromotionReasonNone)

	e.movedCanonical()
	moved := e.assess(prA, "head-a", tree)

	e.requireChange(moved, inventory.ProtectedChange, "P2-R2", false, contract.PromotionReasonApprovalMissing)
	if moved.Canonical != "version-2" || e.proposal("P2").Current().Binding().ExpectedCanonical() != "version-2" {
		t.Fatalf("canonical = %q, revision expects %q, want both version-2", moved.Canonical, e.proposal("P2").Current().Binding().ExpectedCanonical())
	}
}

func TestPullBoundToAnOlderPolicyGetsARevisionUnderTheGoverningOne(t *testing.T) {
	protected := must(must(inventory.Build(context.Background(), headTree("v2", "package main\n"))).Contract())
	older := sealRevision(contract.BindingInput{Reference: contract.ProposalReference{ProjectID: projectID, SuiteID: suiteID, ProposalID: "P2", RevisionID: "R1"},
		ExpectedCanonical: "version-1", Manifest: protected.Manifest().Digest(), Scope: protected.ScopeDigest(), PolicyRevision: "policy-0", CoveredInputs: protected.CoveredInputs()}, "head-a", prA)
	e := newHeadEnv(t, candidate{project: projectID, suite: suiteID, id: "P2", protected: protected, proposal: must(contract.NewProposal(older)), carrier: prA})

	result := e.assess(prA, "head-a", headTree("v2", "package main\n"))

	e.requireChange(result, inventory.ProtectedChange, "P2-R2", false, contract.PromotionReasonApprovalMissing)
	if got := e.proposal("P2").Current().Binding().PolicyRevisionID(); got != policyID {
		t.Fatalf("revision policy = %q, want the governing %q", got, policyID)
	}
}

func TestScopeReductionGetsAProposalLikeAProtectedChange(t *testing.T) {
	e := newHeadEnv(t)
	dropped := newFakeTree( // the declaration no longer includes /tests/**, so a protected path leaves the scope
		regularFile(scope.DeclarationPath, "version: 1\n"),
		regularFile(".github/workflows/ci.yml", "on: push\n"),
		regularFile("tests/a_test.go", "v1"),
		regularFile("src/main.go", "package main\n"),
	)

	result := e.assess(prA, "head-a", dropped)

	e.requireChange(result, inventory.ScopeReduction, "P2-R1", false, contract.PromotionReasonApprovalMissing)
	if assessment, found := e.assessment(*result.Reference, "head-a"); !found || !assessment.Passed() {
		t.Fatalf("assessment at the head = %+v (found %v), want a passed one", assessment, found)
	}
}
