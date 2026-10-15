package governancetest

import (
	"context"
	"fmt"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The cases of PromoteMerge without a canonical: V1 decided by the merge of the bootstrap PR, and what every other
// merge of a Suite without a canonical records.

const mbSource = contract.SourceRevision("merge-sha-boot")

func mergeBootstrapCases() []struct {
	name string
	run  func(*cfEnv)
} {
	return []struct {
		name string
		run  func(*cfEnv)
	}{
		{"MergePromotesV1FromTheMergedBootstrapPR", mbPromotesV1},
		{"MergeOfTheBootstrapPRReplaysFromItsOutcome", mbReplay},
		{"MergeOfATreeThatDiffersFromTheApprovedBootstrapIsBlockedWithNoVersion", mbDiffers},
		{"MergeOfAMergeCommitWithoutProtectedFilesIsBlockedWithNoVersion", mbNoProtectedFiles},
		{"MergeOfAnUnapprovedBootstrapPRIsBlockedWithNoVersion", mbUnapproved},
		{"MergeOfTheBootstrapPRWithAnAssessmentConflictIsBlockedWithNoVersion", mbAssessmentConflict},
		{"MergeRefusalOfTheBootstrapPRIsBlockedWithNoVersion", mbRefusal},
		{"MergeOfAnotherPRBeforeAnyCanonicalIsBeforeTheCanonicalWithNoVersion", mbNonCarrier},
		{"MergeOfTheBootstrapPRIntoAnotherTargetIsBeforeTheCanonical", mbOtherTarget},
		{"MergeAfterAMergedV1IsDecidedAgainstV1", mbChangeAfterAMergedV1},
		{"MergeAfterABranchV1StillAppliesTheTimeRule", mbTimeRuleOfABranchV1},
		{"MergeOutcomesWithACanonicalAlwaysNameItsVersion", mbVersionWithACanonical},
	}
}

// mbRequest is the merge of carrier at source, observed an hour after the canonical, with the tree and messages given.
func mbRequest(carrier contract.ApprovalCarrierID, source contract.SourceRevision, tree inventory.Tree, messages *mcMessages) governance.MergeRequest {
	if messages.key == "" {
		messages.key = "comment/" + string(carrier)
	}
	return governance.MergeRequest{Project: cfProject, Suite: cfSuite, Carrier: carrier, Source: source, Target: cfTarget,
		ObservedAt: cfRecordedAt.Add(time.Hour), Tree: tree, RecordedAt: cfRecordedAt.Add(2 * time.Hour), Message: messages.message}
}

// bootstrapPR seeds a Suite without a canonical and proposes the tree of content as the bootstrap PR's contract, from head-1.
// It returns the owner's consent on the proposal and its reference; approve it to make the merge ready.
func (e *cfEnv) bootstrapPR(content string) (*bsConsent, contract.ProposalReference) {
	e.t.Helper()
	e.seedSimple(cfProject, cfSuite)
	result := e.assessBootstrap(bsRequest("head-1", bsTree(content, "package main\n"), &bsSummaries{}))
	if result.Reference == nil || *result.Reference != bsRef("P1", "R1") {
		e.t.Fatalf("bootstrap proposal = %+v, want P1-R1", result)
	}
	return &bsConsent{e: e}, *result.Reference
}

// requireNoCanonical fails if the Suite has a current version.
func (e *cfEnv) requireNoCanonical() {
	e.t.Helper()
	if got := e.currentVersion(); got != "" {
		e.t.Fatalf("current version = %q, want none", got)
	}
}

func mbPromotesV1(e *cfEnv) {
	consent, reference := e.bootstrapPR("v1")
	consent.approve(reference)
	before := e.revision()
	messages := &mcMessages{}

	result := e.merge(mbRequest(bsCarrier, mbSource, bsTree("v1", "package main\n\nfunc main() {}\n"), messages))

	want := governance.MergeOutcome{Kind: governance.MergePromoted, Source: mbSource, Version: "V1", Reference: &reference}
	e.requireOutcome(bsCarrier, result.MergeOutcome, want)
	if result.Duplicate || messages.calls != 1 || messages.last.Kind != governance.MergePromoted {
		e.t.Fatalf("duplicate %v, message calls %d (last %v); want a new outcome and one message about it", result.Duplicate, messages.calls, messages.last.Kind)
	}
	e.requireWrites(before, 2) // the assessment of the merge commit and the promotion
	if got := e.currentVersion(); got != "V1" {
		e.t.Fatalf("current version = %q, want V1", got)
	}
	historical, found := e.version("V1")
	record := historical.Record()
	if !found || record.OperationID() != "merge:"+contract.OperationID(bsCarrier) || record.Source() != mbSource || record.Carrier() != bsCarrier ||
		record.Target() != cfTarget || record.CorrectsVersionID() != "" || !record.RecordedAt().Equal(cfRecordedAt.Add(2*time.Hour)) {
		e.t.Fatalf("promotion record = %+v (found %v); want operation merge:%s at %s, source %s, no correction", record, found, bsCarrier, mbSource, cfRecordedAt.Add(2*time.Hour))
	}
	e.read(func(ctx context.Context, tx governance.Tx) error {
		receipt, found, err := tx.Receipt(ctx, record.OperationID())
		if err != nil || !found || receipt.Kind != governance.OperationBootstrap || receipt.Promotion == nil {
			return fmt.Errorf("receipt = %+v, %v, %v; want a bootstrap", receipt, found, err)
		}
		integration := receipt.Promotion.Identity.Request.Integration
		if integration.Kind() != contract.IntegrationMergedChange || integration.Source() != mbSource || integration.Carrier() != bsCarrier || integration.Target() != cfTarget {
			return fmt.Errorf("integration = %+v, want the merged change at %s", integration, mbSource)
		}
		return nil
	})
	if assessment, found := e.assessmentAt(reference, mbSource); !found || !assessment.Passed() {
		e.t.Fatalf("assessment of the merge commit = %+v (found %v), want a passed one", assessment, found)
	}
	e.requireQueued(nil, []string{"comment/" + string(bsCarrier), "summary/P1-R1"})
}

func mbReplay(e *cfEnv) {
	consent, reference := e.bootstrapPR("v1")
	consent.approve(reference)
	first := e.merge(mbRequest(bsCarrier, mbSource, bsTree("v1", "package main\n"), &mcMessages{}))
	after := e.revision()
	again := &mcMessages{}

	replay := e.merge(mbRequest(bsCarrier, "another-commit", bsTree("v2", "package main\n"), again))

	e.requireOutcome(bsCarrier, replay.MergeOutcome, first.MergeOutcome)
	if !replay.Duplicate || again.calls != 0 {
		e.t.Fatalf("replay duplicate %v, message calls %d; want a duplicate that calls no message", replay.Duplicate, again.calls)
	}
	e.requireWrites(after, 0)
}

// requireNoVersionBlocked checks a blocked outcome of the bootstrap PR's merge: the reason, the proposal revision, no version,
// and no canonical afterwards.
func (e *cfEnv) requireNoVersionBlocked(result governance.MergeResult, reason governance.MergeReason, source contract.SourceRevision, reference contract.ProposalReference) {
	e.t.Helper()
	e.requireOutcome(bsCarrier, result.MergeOutcome, governance.MergeOutcome{Kind: governance.MergeBlocked, Reason: reason, Source: source, Reference: &reference})
	e.requireNoCanonical()
}

func mbDiffers(e *cfEnv) {
	consent, reference := e.bootstrapPR("v1")
	consent.approve(reference)
	before := e.revision()

	result := e.merge(mbRequest(bsCarrier, mbSource, bsTree("v2", "package main\n"), &mcMessages{})) // approved v1, merged v2

	e.requireNoVersionBlocked(result, governance.MergeReasonDiffersFromApproved, mbSource, reference)
	e.requireWrites(before, 1) // the failed assessment stays; nothing is promoted
	if assessment, found := e.assessmentAt(reference, mbSource); !found || assessment.Passed() {
		e.t.Fatalf("assessment of the merge commit = %+v (found %v), want a recorded failed one", assessment, found)
	}
}

func mbNoProtectedFiles(e *cfEnv) {
	consent, reference := e.bootstrapPR("v1")
	consent.approve(reference)

	result := e.merge(mbRequest(bsCarrier, mbSource, &mcTree{files: map[string]string{"src/main.go": "package main\n"}}, &mcMessages{}))

	e.requireNoVersionBlocked(result, governance.MergeReasonDiffersFromApproved, mbSource, reference)
}

func mbUnapproved(e *cfEnv) {
	_, reference := e.bootstrapPR("v1")
	before := e.revision()

	result := e.merge(mbRequest(bsCarrier, mbSource, bsTree("v1", "package main\n"), &mcMessages{}))

	e.requireNoVersionBlocked(result, governance.MergeReasonApprovalMissing, mbSource, reference)
	e.requireWrites(before, 1) // the passed assessment of the merge commit
}

func mbAssessmentConflict(e *cfEnv) {
	consent, reference := e.bootstrapPR("v1")
	consent.approve(reference)
	before := e.revision()

	// The head was assessed as passing; a merge commit of the same name with other content cannot be assessed again.
	result := e.merge(mbRequest(bsCarrier, "head-1", bsTree("v2", "package main\n"), &mcMessages{}))

	e.requireNoVersionBlocked(result, governance.MergeReasonAssessmentConflict, "head-1", reference)
	e.requireWrites(before, 0)
}

func mbRefusal(e *cfEnv) {
	consent, reference := e.bootstrapPR("v1")
	consent.approve(reference)
	before := e.revision()
	invalid := bsTree("v1", "package main\n")
	invalid.extra = []inventory.Entry{{Path: "tests/link", Kind: inventory.Symlink}}
	fork := mbRequest(bsCarrier, mbSource, nil, &mcMessages{})
	fork.Refusal = governance.MergeReasonForkUnsupported

	e.requireNoVersionBlocked(e.merge(fork), governance.MergeReasonForkUnsupported, mbSource, reference)
	e.requireWrites(before, 0)

	// The merge tree of another bootstrap PR is invalid: the use case refuses it, with the same outcome.
	const second = contract.ApprovalCarrierID("github-pr:1/10")
	secondHead := bsRequest("head-2", bsTree("v2", "package main\n"), &bsSummaries{})
	secondHead.Carrier = second
	proposed := e.assessBootstrap(secondHead)

	got := e.merge(mbRequest(second, "merge-sha-second", invalid, &mcMessages{}))

	e.requireOutcome(second, got.MergeOutcome, governance.MergeOutcome{Kind: governance.MergeBlocked, Reason: governance.MergeReasonInvalidSource, Source: "merge-sha-second", Reference: proposed.Reference})
	e.requireNoCanonical()
}

func mbNonCarrier(e *cfEnv) {
	e.bootstrapPR("v1") // a bootstrap proposal exists, for another carrier
	before := e.revision()
	stranger := contract.ApprovalCarrierID("github-pr:1/77")
	messages := &mcMessages{mute: true}

	result := e.merge(mbRequest(stranger, "merge-sha-stranger", bsTree("v2", "package main\n"), messages))

	e.requireOutcome(stranger, result.MergeOutcome, governance.MergeOutcome{Kind: governance.MergeBeforeCanonical, Source: "merge-sha-stranger"})
	if messages.calls != 1 || messages.last.Version != "" {
		e.t.Fatalf("message calls %d (last version %q), want the caller asked once and told there is no version", messages.calls, messages.last.Version)
	}
	e.requireWrites(before, 0)
	e.requireQueued(nil, []string{"summary/P1-R1"}) // only the bootstrap summary: the merge wrote no message
	e.requireNoCanonical()

	// A refusal of a PR with no bootstrap proposal records the same.
	refused := mbRequest("github-pr:1/78", "merge-sha-refused", nil, &mcMessages{mute: true})
	refused.Refusal = governance.MergeReasonSharedMergeCommit
	e.requireOutcome("github-pr:1/78", e.merge(refused).MergeOutcome, governance.MergeOutcome{Kind: governance.MergeBeforeCanonical, Source: "merge-sha-refused"})
}

func mbOtherTarget(e *cfEnv) {
	consent, reference := e.bootstrapPR("v1")
	consent.approve(reference)
	before := e.revision()
	request := mbRequest(bsCarrier, mbSource, bsTree("v1", "package main\n"), &mcMessages{mute: true})
	request.Target = "branch:other"

	result := e.merge(request)

	e.requireOutcome(bsCarrier, result.MergeOutcome, governance.MergeOutcome{Kind: governance.MergeBeforeCanonical, Source: mbSource})
	e.requireWrites(before, 0)
	e.requireNoCanonical()
}

// mbChangePR proposes the tree of content as a change against V1 for the carrier, approves it and returns its merge request.
func (e *cfEnv) mbChangePR(carrier contract.ApprovalCarrierID, content string, observed time.Time) governance.MergeRequest {
	e.t.Helper()
	request := e.headRequest("head-change", bsTree(content, "package main\n"), &rvNotices{mute: true})
	request.Carrier = carrier
	result := e.assessHead(request)
	if result.Reference == nil {
		e.t.Fatalf("change proposal = %+v, want a proposal", result)
	}
	(&bsConsent{e: e, carrier: carrier, order: 10}).approve(*result.Reference)
	merge := mbRequest(carrier, "merge-sha-change", bsTree(content, "package main\n"), &mcMessages{})
	merge.ObservedAt = observed
	return merge
}

func mbChangeAfterAMergedV1(e *cfEnv) {
	consent, reference := e.bootstrapPR("v1")
	consent.approve(reference)
	if first := e.merge(mbRequest(bsCarrier, mbSource, bsTree("v1", "package main\n"), &mcMessages{})); first.Kind != governance.MergePromoted {
		e.t.Fatalf("bootstrap merge = %+v, want promoted", first.MergeOutcome)
	}
	const change = contract.ApprovalCarrierID("github-pr:1/20")
	// Opened and merged while the bootstrap PR was still open: before V1 was recorded (two hours after the canonical time).
	request := e.mbChangePR(change, "v2", cfRecordedAt.Add(time.Hour))
	if !request.ObservedAt.Before(cfRecordedAt.Add(2 * time.Hour)) {
		e.t.Fatalf("the change merge is observed at %s, want it before V1's recorded time", request.ObservedAt)
	}

	result := e.merge(request)

	e.requireOutcome(change, result.MergeOutcome, governance.MergeOutcome{Kind: governance.MergePromoted, Source: "merge-sha-change", Version: "V2", Reference: result.Reference})
	if got := e.currentVersion(); got != "V2" {
		e.t.Fatalf("current version = %q, want V2", got)
	}
}

func mbTimeRuleOfABranchV1(e *cfEnv) {
	pr := mcCandidate("pr", "V1", "v2")
	e.mcSeed(false, pr) // V1 came from a pinned branch commit, recorded at cfRecordedAt
	e.approve(pr)
	before := e.revision()
	request := mcRequest(pr, "v2", &mcMessages{mute: true})
	request.ObservedAt = cfRecordedAt.Add(-time.Minute)

	result := e.merge(request)

	e.requireOutcome(pr.carrier, result.MergeOutcome, governance.MergeOutcome{Kind: governance.MergeBeforeCanonical, Source: mcSource(pr), Version: "V1"})
	e.requireWrites(before, 0)
}

// Without a canonical a blocked or before-canonical outcome names no version; with one it always does, so the empty
// version is never a way to hide the canonical a merge left in force.
func mbVersionWithACanonical(e *cfEnv) {
	pr := mcCandidate("pr", "V1", "v2")
	e.mcSeed(false, pr)
	e.approve(pr)
	requests := map[string]governance.MergeRequest{}
	refused := mcRequest(mcCandidate("fork", "V1", "v2"), "v2", &mcMessages{})
	refused.Tree, refused.Refusal = nil, governance.MergeReasonForkUnsupported
	requests["refusal"] = refused
	unapproved := mcCandidate("unapproved", "V1", "v2")
	requests["unapproved change"] = mcRequest(unapproved, "v2", &mcMessages{})
	early := mcRequest(mcCandidate("early", "V1", "v2"), "v2", &mcMessages{})
	early.ObservedAt = cfRecordedAt.Add(-time.Hour)
	requests["before the canonical"] = early
	other := mcRequest(mcCandidate("other", "V1", "v2"), "v2", &mcMessages{})
	other.Target = "branch:other"
	requests["another target"] = other
	requests["a differing tree"] = mcRequest(pr, "v3", &mcMessages{})

	for name, request := range requests {
		result := e.merge(request)

		if result.Version != "V1" {
			e.t.Fatalf("%s: outcome %+v names version %q, want the canonical V1 in force", name, result.MergeOutcome, result.Version)
		}
	}
}
