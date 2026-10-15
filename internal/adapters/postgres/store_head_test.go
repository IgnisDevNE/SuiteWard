//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// headTree is the protected baseline layout with a chosen protected test file and implementation file.
func headTree(test, src string) mapTree {
	return mapTree{
		scope.DeclarationPath:      "version: 1\ninclude: [/tests/**]\n",
		".github/workflows/ci.yml": "on: push\n",
		"tests/a_test.go":          test,
		"src/main.go":              src,
	}
}

// TestHeadAssessmentFollowsConsentAcrossPushes drives AssessHead against PostgreSQL and the filesystem content
// store: the first protected change of a pull creates its proposal, an implementation-only push keeps an
// approval, a protected push needs a fresh one, and a revocation makes the head not ready.
func TestHeadAssessmentFollowsConsentAcrossPushes(t *testing.T) {
	w := newPGWorld(t)
	w.seedSuite(fxProject, fxSuite)
	const bootstrapCarrier, pull = contract.ApprovalCarrierID("github-pr:1/1"), contract.ApprovalCarrierID("github-pr:1/2")
	order := contract.CommandOrder(0)
	decide := func(reference contract.ProposalReference, carrier contract.ApprovalCarrierID, action contract.ConsentAction) governance.ConsentResponse {
		order++
		name := fmt.Sprintf("%s-%s-%d", reference.ProposalID, reference.RevisionID, order)
		return w.consent(must(contract.NewCommand(contract.CommandInput{OperationID: contract.OperationID("op-" + name), SourceCommandID: contract.SourceCommandID("source-" + name),
			Actor: w.owner, Reference: reference, Carrier: carrier, Action: action, Order: order})))
	}
	assess := func(carrier contract.ApprovalCarrierID, head string, tree mapTree) governance.HeadResult {
		t.Helper()
		result, err := governance.AssessHead(t.Context(), w.store, w.content, w.content, governance.HeadRequest{Project: fxProject, Suite: fxSuite, Carrier: carrier, Head: contract.SourceRevision(head), Tree: tree})
		if err != nil {
			t.Fatalf("assess head %s: %v", head, err)
		}
		return result
	}
	requireReady := func(result governance.HeadResult, reference string, ready bool, reason contract.PromotionReason) {
		t.Helper()
		got := "none"
		if result.Reference != nil {
			got = fmt.Sprintf("%s-%s", result.Reference.ProposalID, result.Reference.RevisionID)
		}
		if got != reference || result.Ready != ready || result.Reason != reason {
			t.Fatalf("result = reference %s ready %v reason %d, want %s ready %v reason %d", got, result.Ready, result.Reason, reference, ready, reason)
		}
	}

	// No canonical yet: nothing is proposed.
	if _, err := governance.AssessHead(t.Context(), w.store, w.content, w.content, governance.HeadRequest{Project: fxProject, Suite: fxSuite, Carrier: pull, Head: "head-0", Tree: headTree("v1", "package main\n")}); err == nil || !errors.Is(err, governance.ErrNoCanonical) {
		t.Fatalf("assess without a canonical = %v, want ErrNoCanonical", err)
	}
	// The baseline PR bootstraps version-1.
	baseline, err := governance.ProposeBaseline(t.Context(), w.store, w.content, governance.BaselineRequest{
		Reference: contract.ProposalReference{ProjectID: fxProject, SuiteID: fxSuite, ProposalID: "P1", RevisionID: "R1"}, Carrier: bootstrapCarrier, Origin: "pinned", OriginKind: contract.OriginBranch, Tree: headTree("v1", "package main\n")})
	if err != nil {
		t.Fatal(err)
	}
	decide(baseline.Revise.Current, bootstrapCarrier, contract.ApproveConsent)
	binding := contract.ApprovalBinding{}
	w.read(func(ctx context.Context, tx governance.Tx) error {
		proposal, _, err := tx.Proposal(ctx, "P1")
		binding = proposal.Current().Binding()
		return err
	})
	w.recordAssessment(baseline.Revise.Current, "pinned", binding, contract.IntegrityPassed)
	integration := must(contract.NewIntegration(fxProject, fxTarget, "pinned", "", contract.IntegrationExistingBaseline))
	promoted, err := governance.Bootstrap(t.Context(), w.store, governance.PromoteRequest{OperationID: "bootstrap-P1-R1", Reference: baseline.Revise.Current, Carrier: bootstrapCarrier,
		Proposed: baseline.Contract, AssessmentSource: "pinned", Integration: integration, NewVersionID: "V1", RecordedAt: fxRecordedAt})
	if err != nil || !promoted.Committed {
		t.Fatalf("bootstrap = %+v, %v", promoted, err)
	}

	first := assess(pull, "head-a", headTree("v2", "package main\n"))
	requireReady(first, "P2-R1", false, contract.PromotionReasonApprovalMissing)
	if again := assess(pull, "head-a", headTree("v2", "package main\n")); again.Revision != first.Revision {
		t.Fatalf("a repeated assessment moved the revision from %d to %d", first.Revision, again.Revision)
	}
	if !decide(*first.Reference, pull, contract.ApproveConsent).Receipt.CurrentApprovalEligible {
		t.Fatal("the approval of P2-R1 is not eligible")
	}

	requireReady(assess(pull, "head-b", headTree("v2", "package main\n\nfunc main() {}\n")), "P2-R1", true, contract.PromotionReasonNone)
	pushed := assess(pull, "head-c", headTree("v3", "package main\n"))
	requireReady(pushed, "P2-R2", false, contract.PromotionReasonApprovalMissing)

	decide(*pushed.Reference, pull, contract.ApproveConsent)
	requireReady(assess(pull, "head-c", headTree("v3", "package main\n")), "P2-R2", true, contract.PromotionReasonNone)
	decide(*pushed.Reference, pull, contract.RevokeConsent)
	requireReady(assess(pull, "head-c", headTree("v3", "package main\n")), "P2-R2", false, contract.PromotionReasonApprovalMissing)

	// The baseline PR itself, pushing a protected change, gets a new proposal and leaves P1 alone.
	requireReady(assess(bootstrapCarrier, "head-d", headTree("v4", "package main\n")), "P3-R1", false, contract.PromotionReasonApprovalMissing)

	// Concurrent assessments of other pulls allocate distinct proposals under the Suite lock.
	var wg sync.WaitGroup
	seen := make([]contract.ProposalID, 4)
	for i := range seen {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := governance.AssessHead(t.Context(), w.store, w.content, w.content, governance.HeadRequest{Project: fxProject, Suite: fxSuite,
				Carrier: contract.ApprovalCarrierID(fmt.Sprintf("github-pr:1/%d", 10+i)), Head: contract.SourceRevision(fmt.Sprintf("head-%d", i)), Tree: headTree("v5", "package main\n")})
			if err != nil {
				t.Errorf("assess pull %d: %v", 10+i, err)
				return
			}
			seen[i] = result.Reference.ProposalID
		}()
	}
	wg.Wait()
	distinct := map[contract.ProposalID]bool{}
	for _, id := range seen {
		distinct[id] = true
	}
	if len(distinct) != len(seen) || distinct["P1"] || distinct["P2"] || distinct["P3"] || distinct[""] {
		t.Fatalf("proposals of the concurrent pulls = %v, want four new distinct ones", seen)
	}
}

// hookReader runs onRead once before its first read, as a canonical that moves while a head is read from the
// content store would.
type hookReader struct {
	inner  governance.ContentReader
	onRead func()
}

func (r *hookReader) Read(ctx context.Context, digest artifact.Digest) ([]byte, error) {
	if hook := r.onRead; hook != nil {
		r.onRead = nil
		hook()
	}
	return r.inner.Read(ctx, digest)
}

// TestHeadAssessmentReportsACanonicalThatMovedDuringTheCall pins existing behavior against PostgreSQL: the second
// unit of work finds another canonical than the one the head was classified against, writes nothing, and the
// error is ErrCanonicalMoved, which is also an ErrConflict.
func TestHeadAssessmentReportsACanonicalThatMovedDuringTheCall(t *testing.T) {
	w := newPGWorld(t)
	other := newCandidate("px", "V1", w.protected("other"))
	w.seedSuite(fxProject, fxSuite, other)
	const bootstrapCarrier = contract.ApprovalCarrierID("github-pr:1/1")
	baseline, err := governance.ProposeBaseline(t.Context(), w.store, w.content, governance.BaselineRequest{
		Reference: contract.ProposalReference{ProjectID: fxProject, SuiteID: fxSuite, ProposalID: "P1", RevisionID: "R1"}, Carrier: bootstrapCarrier, Origin: "pinned", OriginKind: contract.OriginBranch, Tree: headTree("v1", "package main\n")})
	if err != nil {
		t.Fatal(err)
	}
	approval := must(contract.NewCommand(contract.CommandInput{OperationID: "op-baseline", SourceCommandID: "source-baseline", Actor: w.owner,
		Reference: baseline.Revise.Current, Carrier: bootstrapCarrier, Action: contract.ApproveConsent, Order: 1}))
	w.consent(approval)
	var binding contract.ApprovalBinding
	w.read(func(ctx context.Context, tx governance.Tx) error {
		proposal, _, err := tx.Proposal(ctx, "P1")
		binding = proposal.Current().Binding()
		return err
	})
	w.recordAssessment(baseline.Revise.Current, "pinned", binding, contract.IntegrityPassed)
	integration := must(contract.NewIntegration(fxProject, fxTarget, "pinned", "", contract.IntegrationExistingBaseline))
	if promoted, err := governance.Bootstrap(t.Context(), w.store, governance.PromoteRequest{OperationID: "bootstrap-P1-R1", Reference: baseline.Revise.Current, Carrier: bootstrapCarrier,
		Proposed: baseline.Contract, AssessmentSource: "pinned", Integration: integration, NewVersionID: "V1", RecordedAt: fxRecordedAt}); err != nil || !promoted.Committed {
		t.Fatalf("bootstrap = %+v, %v", promoted, err)
	}
	reader := &hookReader{inner: w.content, onRead: func() {
		w.approve(other)
		if promoted, err := governance.Promote(t.Context(), w.store, other.mergedRequest("promote-px", "V2")); err != nil || !promoted.Committed {
			t.Errorf("promote the other change = %+v, %v", promoted, err)
		}
	}}

	_, err = governance.AssessHead(t.Context(), w.store, reader, w.content, governance.HeadRequest{Project: fxProject, Suite: fxSuite,
		Carrier: "github-pr:1/2", Head: "head-a", Tree: headTree("v2", "package main\n")})

	if !errors.Is(err, governance.ErrCanonicalMoved) || !errors.Is(err, governance.ErrConflict) {
		t.Fatalf("error = %v, want ErrCanonicalMoved, which is an ErrConflict", err)
	}
	w.read(func(ctx context.Context, tx governance.Tx) error {
		if _, _, err := tx.Proposal(ctx, "P2"); !errors.Is(err, governance.ErrNotFound) {
			t.Errorf("proposal P2 = %v, want none: the head was not bound to the new canonical", err)
		}
		return nil
	})
}
