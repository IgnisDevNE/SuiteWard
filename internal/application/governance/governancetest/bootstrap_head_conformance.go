package governancetest

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The cases of AssessBootstrapHead: the proposal, assessment and readiness of the head of the bootstrap PR.

const bsCarrier = contract.ApprovalCarrierID("github-pr:1/9")

func bootstrapHeadCases() []struct {
	name string
	run  func(*cfEnv)
} {
	return []struct {
		name string
		run  func(*cfEnv)
	}{
		{"AssessBootstrapHeadAssessesTheCurrentRevision", cfBootstrapHeadAssesses},
		{"AssessBootstrapHeadRepeatedWritesNothing", cfBootstrapHeadRepeated},
		{"AssessBootstrapHeadImplementationOnlyPushKeepsTheApproval", cfBootstrapHeadImplementationOnlyPush},
		{"AssessBootstrapHeadReadinessFollowsApprovalAndRevoke", cfBootstrapHeadReadiness},
		{"AssessBootstrapHeadWithOtherCoverageIsCancelledAndWritesNothing", cfBootstrapHeadCoverageDiffers},
		{"AssessBootstrapHeadIsCurrentAgainWhenAPushRestoresTheCoverage", cfBootstrapHeadRestored},
		{"AssessBootstrapHeadWithoutProtectedFilesIsCancelledAndWritesNothing", cfBootstrapHeadNoProtectedFiles},
		{"AssessBootstrapHeadWithoutAProposalIsCancelledAndWritesNothing", cfBootstrapHeadNoProposal},
		{"AssessBootstrapHeadOfAnotherCarrierIsCancelledAndWritesNothing", cfBootstrapHeadOtherCarrier},
		{"AssessBootstrapHeadOfASupersededCarrierIsCancelledAndWritesNothing", cfBootstrapHeadSuperseded},
		{"AssessBootstrapHeadOfABranchProposalIsCancelledAndWritesNothing", cfBootstrapHeadBranchProposal},
		{"AssessBootstrapHeadWithACanonicalIsCanonicalMoved", cfBootstrapHeadCanonicalPresent},
		{"AssessBootstrapHeadRejectsAnInvalidRequest", cfBootstrapHeadInvalidRequest},
		{"AssessBootstrapHeadOfAnInvalidTreeWritesNothing", cfBootstrapHeadInvalidTree},
		{"ConcurrentAssessmentsOfOneBootstrapHeadWriteOneAssessment", cfBootstrapHeadConcurrent},
	}
}

// bsTree is the protected tree of content with an implementation file of src.
func bsTree(content, src string) *mcTree {
	tree := mcTreeOf(content)
	tree.files["src/main.go"] = src
	return tree
}

func bsRequest(head string, tree inventory.Tree) governance.BootstrapHeadRequest {
	return governance.BootstrapHeadRequest{Project: cfProject, Suite: cfSuite, Carrier: bsCarrier, Head: contract.SourceRevision(head), Tree: tree}
}

func bsRef(proposal, revision string) contract.ProposalReference {
	return contract.ProposalReference{ProjectID: cfProject, SuiteID: cfSuite, ProposalID: contract.ProposalID(proposal), RevisionID: contract.ProposalRevisionID(revision)}
}

func (e *cfEnv) assessBootstrap(request governance.BootstrapHeadRequest) governance.BootstrapHeadResult {
	e.t.Helper()
	result, err := governance.AssessBootstrapHead(context.Background(), e.store, request)
	if err != nil {
		e.t.Fatalf("assess bootstrap head %q: %v", request.Head, err)
	}
	return result
}

func (e *cfEnv) assessBootstrapErr(request governance.BootstrapHeadRequest) error {
	e.t.Helper()
	_, err := governance.AssessBootstrapHead(context.Background(), e.store, request)
	if err == nil {
		e.t.Fatalf("assess bootstrap head %q succeeded, want an error", request.Head)
	}
	return err
}

// requireAssessed fails unless result names reference with the given readiness, at the revision the Suite holds now.
func (e *cfEnv) requireAssessed(result governance.BootstrapHeadResult, reference *contract.ProposalReference, ready bool, reason contract.PromotionReason) {
	e.t.Helper()
	if result.Cancelled || result.Reference == nil || *result.Reference != *reference || result.Ready != ready || result.Reason != reason || result.Revision != int64(e.revision()) {
		e.t.Fatalf("result = %+v (reference %v), want reference %v, ready %v, reason %d at revision %d", result, result.Reference, reference, ready, reason, e.revision())
	}
}

// requireCancelled fails unless result is a cancellation, and the Suite holds no more than before: no proposal, no
// assessment of head, no revision.
func (e *cfEnv) requireCancelled(result governance.BootstrapHeadResult, before contract.StateRevision) {
	e.t.Helper()
	if !result.Cancelled || result.Reference != nil || result.Ready || result.Revision != int64(before) {
		e.t.Fatalf("result = %+v, want Cancelled at revision %d and nothing else", result, before)
	}
	e.requireWrites(before, 0)
}

// proposePull proposes the tree of content as the R1 of proposal for carrier, sourced from the PR head of kind pull, as the
// commands job does; the proposal is the bootstrap proposal to assess.
func (e *cfEnv) proposePull(proposal string, carrier contract.ApprovalCarrierID, head, content string) contract.ProposalReference {
	e.t.Helper()
	return e.proposeAs(proposal, carrier, head, contract.OriginPull, content)
}

func (e *cfEnv) proposeAs(proposal string, carrier contract.ApprovalCarrierID, head string, kind contract.OriginKind, content string) contract.ProposalReference {
	e.t.Helper()
	reference := bsRef(proposal, "R1")
	if result, err := e.proposeBaseline(reference, carrier, contract.SourceRevision(head), kind, content); err != nil || !result.Revise.Committed {
		e.t.Fatalf("propose %v from %q: %+v, %v", reference, head, result.Revise, err)
	}
	return reference
}

func bsRefPtr(proposal, revision string) *contract.ProposalReference {
	reference := bsRef(proposal, revision)
	return &reference
}

// bsConsent records the owner's decisions on the revisions of the bootstrap PR, in increasing order.
type bsConsent struct {
	e       *cfEnv
	carrier contract.ApprovalCarrierID // the bootstrap PR when empty
	order   contract.CommandOrder
}

func (c *bsConsent) decide(reference contract.ProposalReference, action contract.ConsentAction) governance.ConsentResponse {
	c.e.t.Helper()
	c.order++
	carrier := c.carrier
	if carrier == "" {
		carrier = bsCarrier
	}
	name := fmt.Sprintf("%s-%s-%d", reference.ProposalID, reference.RevisionID, c.order)
	return c.e.consent(cfMust(contract.NewCommand(contract.CommandInput{OperationID: contract.OperationID("op-" + name), SourceCommandID: contract.SourceCommandID("source-" + name),
		Actor: c.e.owner, Reference: reference, Carrier: carrier, Action: action, Order: c.order})))
}

func (c *bsConsent) approve(reference contract.ProposalReference) {
	c.e.t.Helper()
	if response := c.decide(reference, contract.ApproveConsent); !response.Receipt.CurrentApprovalEligible {
		c.e.t.Fatalf("approval of %v is not eligible: %v", reference, response.Receipt.Result.Reason())
	}
}

func (e *cfEnv) revisionsOf(proposal contract.ProposalID) []contract.ProposalRevision {
	e.t.Helper()
	var revisions []contract.ProposalRevision
	e.read(func(ctx context.Context, tx governance.Tx) error {
		p, _, err := tx.Proposal(ctx, proposal)
		revisions = p.Revisions()
		return err
	})
	return revisions
}

// bsHead is the head of the bootstrap PR with the protected tests of content.
func bsHead(head, content string) governance.BootstrapHeadRequest {
	return bsRequest(head, bsTree(content, "package main\n"))
}

func cfBootstrapHeadAssesses(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	reference := e.proposePull("P1", bsCarrier, "head-1", "v1")
	before := e.revision()

	result := e.assessBootstrap(bsHead("head-1", "v1"))

	e.requireAssessed(result, &reference, false, contract.PromotionReasonApprovalMissing)
	e.requireWrites(before, 1) // the assessment of the head; no revision, no message
	if assessment, found := e.assessmentAt(reference, "head-1"); !found || !assessment.Passed() {
		e.t.Fatalf("assessment of the head = %+v (found %v), want a passed one", assessment, found)
	}
	if len(e.revisionsOf("P1")) != 1 {
		e.t.Fatalf("P1 has %d revisions, want the assessment to add none", len(e.revisionsOf("P1")))
	}
	e.requireOutbox(nil)
}

func cfBootstrapHeadRepeated(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	reference := e.proposePull("P1", bsCarrier, "head-1", "v1")
	e.assessBootstrap(bsHead("head-1", "v1"))
	before := e.revision()

	again := e.assessBootstrap(bsHead("head-1", "v1"))

	e.requireAssessed(again, &reference, false, contract.PromotionReasonApprovalMissing)
	e.requireWrites(before, 0)
}

func cfBootstrapHeadImplementationOnlyPush(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	reference := e.proposePull("P1", bsCarrier, "head-1", "v1")
	(&bsConsent{e: e}).approve(reference)
	before := e.revision()

	pushed := e.assessBootstrap(bsRequest("head-2", bsTree("v1", "package main\n\nfunc main() {}\n")))

	e.requireAssessed(pushed, &reference, true, contract.PromotionReasonNone) // the approval stands
	e.requireWrites(before, 1)                                                // only the assessment of the new head
}

func cfBootstrapHeadReadiness(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	reference := e.proposePull("P1", bsCarrier, "head-1", "v1")
	consent := &bsConsent{e: e}

	e.requireAssessed(e.assessBootstrap(bsHead("head-1", "v1")), &reference, false, contract.PromotionReasonApprovalMissing)
	consent.approve(reference)
	e.requireAssessed(e.assessBootstrap(bsHead("head-1", "v1")), &reference, true, contract.PromotionReasonNone)
	consent.decide(reference, contract.RevokeConsent)
	e.requireAssessed(e.assessBootstrap(bsHead("head-1", "v1")), &reference, false, contract.PromotionReasonApprovalMissing)
}

// A head that no longer has the proposed coverage is not assessed, however it was approved: the proposal is the owner's
// to renew, and this call never revises it.
func cfBootstrapHeadCoverageDiffers(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	reference := e.proposePull("P1", bsCarrier, "head-1", "v1")
	(&bsConsent{e: e}).approve(reference)
	before := e.revision()

	result := e.assessBootstrap(bsHead("head-2", "v2"))

	e.requireCancelled(result, before)
	if _, found := e.assessmentAt(reference, "head-2"); found {
		e.t.Fatal("the cancelled head was assessed")
	}
	if len(e.revisionsOf("P1")) != 1 {
		e.t.Fatalf("P1 has %d revisions, want the cancellation to add none", len(e.revisionsOf("P1")))
	}
}

func cfBootstrapHeadRestored(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	reference := e.proposePull("P1", bsCarrier, "head-1", "v1")
	(&bsConsent{e: e}).approve(reference)
	e.requireCancelled(e.assessBootstrap(bsHead("head-2", "v2")), e.revision())

	restored := e.assessBootstrap(bsHead("head-3", "v1"))

	e.requireAssessed(restored, &reference, true, contract.PromotionReasonNone)
}

func cfBootstrapHeadNoProtectedFiles(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	e.proposePull("P1", bsCarrier, "head-1", "v1")
	before := e.revision()

	result := e.assessBootstrap(bsRequest("head-2", &mcTree{files: map[string]string{"src/main.go": "package main\n"}}))

	e.requireCancelled(result, before)
}

func cfBootstrapHeadNoProposal(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	before := e.revision()

	result := e.assessBootstrap(bsHead("head-1", "v1"))

	e.requireCancelled(result, before)
}

func cfBootstrapHeadOtherCarrier(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	e.proposePull("P1", "github-pr:1/10", "head-1", "v1")
	before := e.revision()

	result := e.assessBootstrap(bsHead("head-1", "v1")) // the proposal is carried by another PR

	e.requireCancelled(result, before)
}

// Only the latest bootstrap proposal is assessed: the earlier carrier's proposal is superseded.
func cfBootstrapHeadSuperseded(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	e.proposePull("P1", bsCarrier, "head-1", "v1")
	const second = contract.ApprovalCarrierID("github-pr:1/10")
	latest := e.proposePull("P2", second, "head-2", "v2")
	before := e.revision()

	result := e.assessBootstrap(bsHead("head-1", "v1"))

	e.requireCancelled(result, before)
	current := bsHead("head-2", "v2")
	current.Carrier = second
	e.requireAssessed(e.assessBootstrap(current), &latest, false, contract.PromotionReasonApprovalMissing)
}

func cfBootstrapHeadBranchProposal(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	e.proposeAs("P1", bsCarrier, "branch-head", contract.OriginBranch, "v1")
	before := e.revision()

	result := e.assessBootstrap(bsHead("head-1", "v1"))

	e.requireCancelled(result, before)
}

func cfBootstrapHeadCanonicalPresent(e *cfEnv) {
	e.mcSeed(false)
	before := e.revision()

	err := e.assessBootstrapErr(bsHead("head-1", "v2"))

	if !errors.Is(err, governance.ErrCanonicalMoved) || !errors.Is(err, governance.ErrConflict) {
		e.t.Fatalf("error = %v, want ErrCanonicalMoved", err)
	}
	e.requireWrites(before, 0)
}

func cfBootstrapHeadInvalidRequest(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	before := e.revision()
	for name, edit := range map[string]func(*governance.BootstrapHeadRequest){
		"no tree":       func(r *governance.BootstrapHeadRequest) { r.Tree = nil },
		"blank head":    func(r *governance.BootstrapHeadRequest) { r.Head = " " },
		"blank carrier": func(r *governance.BootstrapHeadRequest) { r.Carrier = "" },
	} {
		request := bsHead("head-1", "v1")
		edit(&request)

		if err := e.assessBootstrapErr(request); !errors.Is(err, governance.ErrInvalidRequest) {
			e.t.Fatalf("%s: error = %v, want ErrInvalidRequest", name, err)
		}
	}
	if _, err := governance.AssessBootstrapHead(context.Background(), nil, bsHead("head-1", "v1")); !errors.Is(err, governance.ErrInvalidRequest) {
		e.t.Fatalf("without a unit of work: error = %v, want ErrInvalidRequest", err)
	}
	e.requireWrites(before, 0)
}

func cfBootstrapHeadInvalidTree(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	e.proposePull("P1", bsCarrier, "head-1", "v1")
	before := e.revision()
	link := bsTree("v1", "package main\n")
	link.extra = []inventory.Entry{{Path: "tests/link", Kind: inventory.Symlink}}

	err := e.assessBootstrapErr(bsRequest("head-2", link))

	if !errors.Is(err, inventory.ErrUnsafeEntry) {
		e.t.Fatalf("error = %v, want the unsafe entry of the head", err)
	}
	e.requireWrites(before, 0)
}

func cfBootstrapHeadConcurrent(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	reference := e.proposePull("P1", bsCarrier, "head-1", "v1")
	before := e.revision()
	const callers = 4
	results, errs := make([]governance.BootstrapHeadResult, callers), make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					errs[i] = fmt.Errorf("panic: %v", r)
				}
			}()
			results[i], errs[i] = governance.AssessBootstrapHead(context.Background(), e.store, bsHead("head-1", "v1"))
		}()
	}
	wg.Wait()

	for i := range callers {
		if errs[i] != nil {
			e.t.Fatalf("caller %d: %v", i, errs[i])
		}
		if results[i].Cancelled || results[i].Reference == nil || *results[i].Reference != reference {
			e.t.Fatalf("caller %d = %+v, want an assessment of %v", i, results[i], reference)
		}
	}
	e.requireWrites(before, 1) // one assessment of the head, whoever recorded it
}
