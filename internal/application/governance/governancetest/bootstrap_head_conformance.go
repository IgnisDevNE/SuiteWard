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
		{"AssessBootstrapHeadProposesAndAssessesTheFirstRevision", cfBootstrapHeadFirstRevision},
		{"AssessBootstrapHeadRepeatedWritesNothing", cfBootstrapHeadRepeated},
		{"AssessBootstrapHeadImplementationOnlyPushKeepsTheApproval", cfBootstrapHeadImplementationOnlyPush},
		{"AssessBootstrapHeadProtectedPushNeedsANewApproval", cfBootstrapHeadProtectedPush},
		{"AssessBootstrapHeadReadinessFollowsApprovalAndRevoke", cfBootstrapHeadReadiness},
		{"AssessBootstrapHeadOfAnEmptyHeadWritesNothing", cfBootstrapHeadEmpty},
		{"AssessBootstrapHeadOfAnEmptyHeadWithACanonicalIsStillEmpty", cfBootstrapHeadEmptyWithACanonical},
		{"AssessBootstrapHeadWithACanonicalIsCanonicalMoved", cfBootstrapHeadCanonicalPresent},
		{"AssessBootstrapHeadRejectsAnInvalidRequest", cfBootstrapHeadInvalidRequest},
		{"AssessBootstrapHeadOfAnInvalidTreeWritesNothing", cfBootstrapHeadInvalidTree},
		{"AssessBootstrapHeadSummaryFailureRollsBackTheRevision", cfBootstrapHeadSummaryFailure},
		{"AssessBootstrapHeadSummaryOutboxConflictRollsBackTheRevision", cfBootstrapHeadSummaryConflict},
		{"ConcurrentAssessmentsOfOneBootstrapCarrierWriteOneRevision", cfBootstrapHeadConcurrent},
	}
}

// bsSummaries records the summaries a call asked for.
type bsSummaries struct {
	mu    sync.Mutex
	calls []contract.ProposalReference
	key   string // overrides the key derived from the reference
	fail  error
}

func (s *bsSummaries) summary(reference contract.ProposalReference, _ inventory.Inventory) (governance.OutboxMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, reference)
	if s.fail != nil {
		return governance.OutboxMessage{}, s.fail
	}
	key := s.key
	if key == "" {
		key = fmt.Sprintf("summary/%s-%s", reference.ProposalID, reference.RevisionID)
	}
	return governance.OutboxMessage{Key: key, Kind: "bootstrap.summary", Payload: cfObject}, nil
}

// bsTree is the protected tree of content with an implementation file of src.
func bsTree(content, src string) *mcTree {
	tree := mcTreeOf(content)
	tree.files["src/main.go"] = src
	return tree
}

func bsRequest(head string, tree inventory.Tree, s *bsSummaries) governance.BootstrapHeadRequest {
	return governance.BootstrapHeadRequest{Project: cfProject, Suite: cfSuite, Carrier: bsCarrier, Head: contract.SourceRevision(head), Tree: tree, Summary: s.summary}
}

func bsRef(proposal, revision string) contract.ProposalReference {
	return contract.ProposalReference{ProjectID: cfProject, SuiteID: cfSuite, ProposalID: contract.ProposalID(proposal), RevisionID: contract.ProposalRevisionID(revision)}
}

func (e *cfEnv) assessBootstrap(request governance.BootstrapHeadRequest) governance.BootstrapHeadResult {
	e.t.Helper()
	result, err := governance.AssessBootstrapHead(context.Background(), e.store, mcDiscard{}, request)
	if err != nil {
		e.t.Fatalf("assess bootstrap head %q: %v", request.Head, err)
	}
	return result
}

func (e *cfEnv) assessBootstrapErr(request governance.BootstrapHeadRequest) error {
	e.t.Helper()
	_, err := governance.AssessBootstrapHead(context.Background(), e.store, mcDiscard{}, request)
	if err == nil {
		e.t.Fatalf("assess bootstrap head %q succeeded, want an error", request.Head)
	}
	return err
}

// requireBootstrapHead fails unless result names reference ("" for none) with the given commit flag and readiness, and its
// revision is the one the Suite holds now.
func (e *cfEnv) requireBootstrapHead(result governance.BootstrapHeadResult, reference *contract.ProposalReference, committed, ready bool, reason contract.PromotionReason) {
	e.t.Helper()
	same := (result.Reference == nil) == (reference == nil) && (reference == nil || *result.Reference == *reference)
	if !same || result.Empty != (reference == nil) || result.Committed != committed || result.Ready != ready || result.Reason != reason || result.Revision != int64(e.revision()) && reference != nil {
		e.t.Fatalf("result = %+v (reference %v), want reference %v, committed %v, ready %v, reason %d at revision %d", result, result.Reference, reference, committed, ready, reason, e.revision())
	}
}

func bsRefPtr(proposal, revision string) *contract.ProposalReference {
	reference := bsRef(proposal, revision)
	return &reference
}

// bsConsent records the owner's decisions on the revisions of the bootstrap PR, in increasing order.
type bsConsent struct {
	e     *cfEnv
	order contract.CommandOrder
}

func (c *bsConsent) decide(reference contract.ProposalReference, action contract.ConsentAction) governance.ConsentResponse {
	c.e.t.Helper()
	c.order++
	name := fmt.Sprintf("%s-%s-%d", reference.ProposalID, reference.RevisionID, c.order)
	return c.e.consent(cfMust(contract.NewCommand(contract.CommandInput{OperationID: contract.OperationID("op-" + name), SourceCommandID: contract.SourceCommandID("source-" + name),
		Actor: c.e.owner, Reference: reference, Carrier: bsCarrier, Action: action, Order: c.order})))
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

func cfBootstrapHeadFirstRevision(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	before := e.revision()
	summaries := &bsSummaries{}

	result := e.assessBootstrap(bsRequest("head-1", bsTree("v1", "package main\n"), summaries))

	e.requireBootstrapHead(result, bsRefPtr("P1", "R1"), true, false, contract.PromotionReasonApprovalMissing)
	e.requireWrites(before, 2) // the revision and the assessment of the head
	if len(summaries.calls) != 1 || summaries.calls[0] != bsRef("P1", "R1") {
		e.t.Fatalf("summaries asked for %v, want one for P1-R1", summaries.calls)
	}
	e.requireQueued(nil, []string{"summary/P1-R1"})
	revisions := e.revisionsOf("P1")
	if len(revisions) != 1 || revisions[0].OriginKind() != contract.OriginPull || revisions[0].Origin() != "head-1" || revisions[0].Carrier() != bsCarrier || revisions[0].Binding().ExpectedCanonical() != "" {
		e.t.Fatalf("revisions = %d, want one pull revision of the carrier from head-1 that expects no canonical", len(revisions))
	}
	if assessment, found := e.assessmentAt(bsRef("P1", "R1"), "head-1"); !found || !assessment.Passed() {
		e.t.Fatalf("assessment of the head = %+v (found %v), want a passed one", assessment, found)
	}
	e.requireBaselineProposal(bsCarrier, "P1")
}

func cfBootstrapHeadRepeated(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	summaries := &bsSummaries{}
	e.assessBootstrap(bsRequest("head-1", bsTree("v1", "package main\n"), summaries))
	before := e.revision()

	again := e.assessBootstrap(bsRequest("head-1", bsTree("v1", "package main\n"), summaries))

	e.requireBootstrapHead(again, bsRefPtr("P1", "R1"), false, false, contract.PromotionReasonApprovalMissing)
	e.requireWrites(before, 0)
	if len(summaries.calls) != 1 {
		e.t.Fatalf("summaries asked for %d times, want only the first call's", len(summaries.calls))
	}
	e.requireQueued(nil, []string{"summary/P1-R1"})
}

func cfBootstrapHeadImplementationOnlyPush(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	summaries := &bsSummaries{}
	consent := &bsConsent{e: e}
	e.assessBootstrap(bsRequest("head-1", bsTree("v1", "package main\n"), summaries))
	consent.approve(bsRef("P1", "R1"))
	before := e.revision()

	pushed := e.assessBootstrap(bsRequest("head-2", bsTree("v1", "package main\n\nfunc main() {}\n"), summaries))

	e.requireBootstrapHead(pushed, bsRefPtr("P1", "R1"), false, true, contract.PromotionReasonNone) // the approval stands
	e.requireWrites(before, 1)                                                                      // only the assessment of the new head
	if len(summaries.calls) != 1 || len(e.revisionsOf("P1")) != 1 {
		e.t.Fatalf("summaries %d, revisions %d; want an implementation-only push to add neither", len(summaries.calls), len(e.revisionsOf("P1")))
	}
}

func cfBootstrapHeadProtectedPush(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	summaries := &bsSummaries{}
	consent := &bsConsent{e: e}
	e.assessBootstrap(bsRequest("head-1", bsTree("v1", "package main\n"), summaries))
	consent.approve(bsRef("P1", "R1"))

	pushed := e.assessBootstrap(bsRequest("head-2", bsTree("v2", "package main\n"), summaries))

	e.requireBootstrapHead(pushed, bsRefPtr("P1", "R2"), true, false, contract.PromotionReasonApprovalMissing) // R1's approval does not carry over
	if len(summaries.calls) != 2 || summaries.calls[1] != bsRef("P1", "R2") {
		e.t.Fatalf("summaries asked for %v, want P1-R1 then P1-R2", summaries.calls)
	}
	e.requireQueued(nil, []string{"summary/P1-R1", "summary/P1-R2"})
	revisions := e.revisionsOf("P1")
	if len(revisions) != 2 || revisions[1].OriginKind() != contract.OriginPull || revisions[1].Origin() != "head-2" {
		e.t.Fatalf("revisions = %d, want a second pull revision from head-2", len(revisions))
	}
	consent.approve(bsRef("P1", "R2"))
	again := e.assessBootstrap(bsRequest("head-2", bsTree("v2", "package main\n"), summaries))
	e.requireBootstrapHead(again, bsRefPtr("P1", "R2"), false, true, contract.PromotionReasonNone)
}

func cfBootstrapHeadReadiness(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	summaries := &bsSummaries{}
	consent := &bsConsent{e: e}
	tree := func() *mcTree { return bsTree("v1", "package main\n") }

	e.requireBootstrapHead(e.assessBootstrap(bsRequest("head-1", tree(), summaries)), bsRefPtr("P1", "R1"), true, false, contract.PromotionReasonApprovalMissing)
	consent.approve(bsRef("P1", "R1"))
	e.requireBootstrapHead(e.assessBootstrap(bsRequest("head-1", tree(), summaries)), bsRefPtr("P1", "R1"), false, true, contract.PromotionReasonNone)
	consent.decide(bsRef("P1", "R1"), contract.RevokeConsent)
	e.requireBootstrapHead(e.assessBootstrap(bsRequest("head-1", tree(), summaries)), bsRefPtr("P1", "R1"), false, false, contract.PromotionReasonApprovalMissing)
}

func cfBootstrapHeadEmpty(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	e.requireEmptyBootstrapHead()
}

// With a canonical an empty head is still Empty, not ErrCanonicalMoved: the emptiness is decided before the Suite is read.
func cfBootstrapHeadEmptyWithACanonical(e *cfEnv) {
	e.mcSeed(false)
	e.requireEmptyBootstrapHead()
}

func (e *cfEnv) requireEmptyBootstrapHead() {
	e.t.Helper()
	before := e.revision()
	summaries := &bsSummaries{}

	result := e.assessBootstrap(bsRequest("head-1", &mcTree{files: map[string]string{"src/main.go": "package main\n"}}, summaries))

	if !result.Empty || result.Reference != nil || result.Committed || result.Ready {
		e.t.Fatalf("result = %+v, want Empty and nothing else", result)
	}
	e.requireWrites(before, 0)
	e.requireQueued(nil, nil)
	if len(summaries.calls) != 0 {
		e.t.Fatalf("summaries asked for %v, want none", summaries.calls)
	}
}

func cfBootstrapHeadCanonicalPresent(e *cfEnv) {
	e.mcSeed(false)
	before := e.revision()
	summaries := &bsSummaries{}

	err := e.assessBootstrapErr(bsRequest("head-1", bsTree("v2", "package main\n"), summaries))

	if !errors.Is(err, governance.ErrCanonicalMoved) || !errors.Is(err, governance.ErrConflict) {
		e.t.Fatalf("error = %v, want ErrCanonicalMoved", err)
	}
	e.requireWrites(before, 0)
	e.requireQueued(nil, nil)
	if len(summaries.calls) != 0 {
		e.t.Fatalf("summaries asked for %v, want none", summaries.calls)
	}
}

func cfBootstrapHeadInvalidRequest(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	before := e.revision()
	valid := func() governance.BootstrapHeadRequest {
		return bsRequest("head-1", bsTree("v1", "package main\n"), &bsSummaries{})
	}
	for name, edit := range map[string]func(*governance.BootstrapHeadRequest){
		"no summary function": func(r *governance.BootstrapHeadRequest) { r.Summary = nil },
		"no tree":             func(r *governance.BootstrapHeadRequest) { r.Tree = nil },
		"blank head":          func(r *governance.BootstrapHeadRequest) { r.Head = " " },
		"blank carrier":       func(r *governance.BootstrapHeadRequest) { r.Carrier = "" },
	} {
		request := valid()
		edit(&request)

		if err := e.assessBootstrapErr(request); !errors.Is(err, governance.ErrInvalidRequest) {
			e.t.Fatalf("%s: error = %v, want ErrInvalidRequest", name, err)
		}
	}
	if _, err := governance.AssessBootstrapHead(context.Background(), nil, mcDiscard{}, valid()); !errors.Is(err, governance.ErrInvalidRequest) {
		e.t.Fatalf("without a unit of work: error = %v, want ErrInvalidRequest", err)
	}
	if _, err := governance.AssessBootstrapHead(context.Background(), e.store, nil, valid()); !errors.Is(err, governance.ErrInvalidRequest) {
		e.t.Fatalf("without a content writer: error = %v, want ErrInvalidRequest", err)
	}
	e.requireWrites(before, 0)
}

func cfBootstrapHeadInvalidTree(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	before := e.revision()
	link := bsTree("v1", "package main\n")
	link.extra = []inventory.Entry{{Path: "tests/link", Kind: inventory.Symlink}}

	err := e.assessBootstrapErr(bsRequest("head-1", link, &bsSummaries{}))

	if !errors.Is(err, inventory.ErrUnsafeEntry) {
		e.t.Fatalf("error = %v, want the unsafe entry of the head", err)
	}
	e.requireWrites(before, 0)
	e.requireBaselineProposal(bsCarrier, "")
}

func cfBootstrapHeadSummaryFailure(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	before := e.revision()
	boom := errors.New("summary cannot be built")

	err := e.assessBootstrapErr(bsRequest("head-1", bsTree("v1", "package main\n"), &bsSummaries{fail: boom}))

	if !errors.Is(err, boom) {
		e.t.Fatalf("error = %v, want the summary error", err)
	}
	e.requireWrites(before, 0)
	e.requireBaselineProposal(bsCarrier, "")
	e.requireQueued(nil, nil)
}

func cfBootstrapHeadSummaryConflict(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	if err := e.do(func(ctx context.Context, tx governance.Tx) error {
		return tx.Outbox(ctx, governance.OutboxMessage{Key: "summary/shared", Kind: "bootstrap.summary", Payload: cfObject})
	}); err != nil {
		e.t.Fatal(err)
	}
	before := e.revision()

	err := e.assessBootstrapErr(bsRequest("head-1", bsTree("v1", "package main\n"), &bsSummaries{key: "summary/shared"}))

	if !errors.Is(err, governance.ErrOperationConflict) {
		e.t.Fatalf("error = %v, want ErrOperationConflict", err)
	}
	e.requireWrites(before, 0)
	e.requireBaselineProposal(bsCarrier, "")
	e.requireQueued(nil, []string{"summary/shared"})
}

func cfBootstrapHeadConcurrent(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	before := e.revision()
	summaries := &bsSummaries{}
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
			results[i], errs[i] = governance.AssessBootstrapHead(context.Background(), e.store, mcDiscard{}, bsRequest("head-1", bsTree("v1", "package main\n"), summaries))
		}()
	}
	wg.Wait()

	committed := 0
	for i := range callers {
		if errs[i] != nil {
			e.t.Fatalf("caller %d: %v", i, errs[i])
		}
		if results[i].Committed {
			committed++
		}
		if results[i].Reference == nil || *results[i].Reference != bsRef("P1", "R1") {
			e.t.Fatalf("caller %d reference = %v, want P1-R1", i, results[i].Reference)
		}
	}
	if committed != 1 || len(e.revisionsOf("P1")) != 1 {
		e.t.Fatalf("%d callers committed, %d revisions; want exactly one of each", committed, len(e.revisionsOf("P1")))
	}
	e.requireWrites(before, 2)
	e.requireQueued(nil, []string{"summary/P1-R1"})
}
