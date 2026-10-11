package governance_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

const (
	baselineProposal = contract.ProposalID("baseline")
	baselineCarrier  = contract.ApprovalCarrierID("carrier-baseline")
)

func regularFile(path, content string) treeFile { return treeFile{path, inventory.File, content} }

// baselineTree protects tests/** next to the default workflows; testContent
// is the content of the one protected test file.
func baselineTree(testContent string) *fakeTree {
	return newFakeTree(
		regularFile(scope.DeclarationPath, "version: 1\ninclude: [/tests/**]\n"),
		regularFile(".github/workflows/ci.yml", "on: push\n"),
		regularFile("tests/a_test.go", testContent),
		regularFile("src/main.go", "package main\n"),
	)
}

func baselineRequest(revision contract.ProposalRevisionID, origin string, tree inventory.Tree) governance.BaselineRequest {
	return governance.BaselineRequest{
		Reference: contract.ProposalReference{ProjectID: projectID, SuiteID: suiteID, ProposalID: baselineProposal, RevisionID: revision},
		Carrier:   baselineCarrier, Origin: contract.SourceRevision(origin), Tree: tree,
	}
}

func (w *world) propose(writer governance.ContentWriter, request governance.BaselineRequest) governance.BaselineResult {
	w.t.Helper()
	result, err := governance.ProposeBaseline(context.Background(), w.mem, writer, request)
	if err != nil {
		w.t.Fatalf("propose baseline: %v", err)
	}
	return result
}

func (w *world) hasProposal(id contract.ProposalID) bool {
	w.t.Helper()
	var found bool
	w.read(func(ctx context.Context, tx governance.Tx) error {
		_, _, err := tx.Proposal(ctx, id)
		found = err == nil
		if errors.Is(err, governance.ErrNotFound) {
			return nil
		}
		return err
	})
	return found
}

func TestBaselineFromAnInventoryBootstrapsTheFirstCanonical(t *testing.T) {
	tree := baselineTree("v1")
	inv := must(inventory.Build(context.Background(), tree))
	protected := must(inv.Contract())
	// An earlier revision of the same proposal over other coverage is seeded
	// only to hold the assessment of revision-2: assessments have no write
	// path until M1.6, and PostgreSQL would reject an assessment of a
	// revision that does not exist yet, which only this fake accepts.
	earlier := newCandidate(t, string(baselineProposal), "", protectedOf(t, "earlier"))
	origin := contract.SourceRevision("pinned-commit")
	binding := must(contract.NewApprovalBinding(earlier.bindingInput("revision-2", "", protected)))
	evidence := must(contract.NewIntegrityEvidence("verifier", origin, binding, contract.IntegrityPassed))
	earlier.assessments = append(earlier.assessments, must(contract.AssessIntegrity(origin, binding, &evidence)))
	w := newWorld(t, earlier)
	writer := newFakeWriter()

	result := w.propose(writer, baselineRequest("revision-2", string(origin), tree))

	if !result.Revise.Committed || result.Revise.Current != earlier.reference("revision-2") || !result.Contract.Equal(protected) {
		t.Fatalf("result = %+v, want a committed revision-2 proposing the inventory's contract", result)
	}
	for _, entry := range inv.Manifest().Entries() {
		if _, stored := writer.stored[entry.Content]; !stored {
			t.Fatalf("the bytes of %s were not stored", entry.Path)
		}
	}
	approval := w.consent(w.command(earlier, "revision-2", "approve-baseline", "comment-baseline", contract.ApproveConsent, 1))
	if !approval.Receipt.CurrentApprovalEligible {
		t.Fatalf("approval of revision-2 is not eligible: %v", approval.Receipt.Result.Reason())
	}
	integration := must(contract.NewIntegration(projectID, target, origin, "", contract.IntegrationExistingBaseline))

	promoted := w.bootstrap(governance.PromoteRequest{OperationID: "bootstrap-baseline", Reference: result.Revise.Current, Carrier: baselineCarrier,
		Proposed: result.Contract, AssessmentSource: origin, Integration: integration, NewVersionID: "version-1", RecordedAt: recordedAt})

	if promoted.Outcome != contract.PromotionProposed || !promoted.Committed {
		t.Fatalf("bootstrap = %+v, want a committed proposed promotion", promoted)
	}
	canonical, found := w.version("version-1")
	if !found || canonical.Version().Manifest().Digest() != inv.Manifest().Digest() {
		t.Fatalf("canonical manifest = %v (found %v), want the inventory's %v", canonical.Version().Manifest().Digest(), found, inv.Manifest().Digest())
	}
}

func TestRepeatedBaselineOfTheSameTreeWritesNothing(t *testing.T) {
	w := newWorld(t)
	start := w.revision()
	first := w.propose(newFakeWriter(), baselineRequest("revision-1", "pinned-commit", baselineTree("v1")))
	// The world has no proposal yet, so this is the create path.
	if !first.Revise.Committed || !w.hasProposal(baselineProposal) {
		t.Fatalf("first = %+v, proposal stored %v; want a committed creation", first.Revise, w.hasProposal(baselineProposal))
	}
	w.requireWrites(start, 1)
	before := w.revision()

	again := w.propose(newFakeWriter(), baselineRequest("revision-2", "pinned-later", baselineTree("v1")))

	if again.Revise.Committed || again.Revise.Current != first.Revise.Current {
		t.Fatalf("repeat = %+v, want the existing reference %v and no write", again.Revise, first.Revise.Current)
	}
	w.requireWrites(before, 0)
}

func TestChangedProtectedFileCreatesANewBaselineRevision(t *testing.T) {
	w := newWorld(t)
	first := w.propose(newFakeWriter(), baselineRequest("revision-1", "pinned-commit", baselineTree("v1")))
	before := w.revision()

	next := w.propose(newFakeWriter(), baselineRequest("revision-2", "pinned-later", baselineTree("v2")))

	if !next.Revise.Committed || next.Revise.Current == first.Revise.Current || next.Revise.Current.RevisionID != "revision-2" {
		t.Fatalf("result = %+v, want a committed revision-2", next.Revise)
	}
	w.requireWrites(before, 1)
	if got := w.proposal(baselineProposal).Current().Binding().Reference(); got != next.Revise.Current {
		t.Fatalf("current revision = %v, want %v", got, next.Revise.Current)
	}
}

func TestBaselineIsRejectedOnceACanonicalExists(t *testing.T) {
	first := newCandidate(t, "p1", "", protectedOf(t, "v1"))
	w := newWorld(t, first)
	w.approve(first)
	w.promote(first.mergedRequest(t, "promote-1", "version-1"))
	before := w.revision()

	_, err := governance.ProposeBaseline(context.Background(), w.mem, newFakeWriter(), baselineRequest("revision-1", "pinned-commit", baselineTree("v1")))

	if !errors.Is(err, governance.ErrInvalidRequest) {
		t.Fatalf("error = %v, want an invalid request", err)
	}
	w.requireWrites(before, 0)
	if w.hasProposal(baselineProposal) {
		t.Fatal("a proposal was stored")
	}
}

func TestBaselineWritesNothingToTheDatabaseWhenContentCannotBeStored(t *testing.T) {
	t.Run("put failure", func(t *testing.T) {
		w := newWorld(t)
		before := w.revision()
		writer := newFakeWriter()
		writer.fail = errors.New("disk full")

		_, err := governance.ProposeBaseline(context.Background(), w.mem, writer, baselineRequest("revision-1", "pinned-commit", baselineTree("v1")))

		if !errors.Is(err, writer.fail) {
			t.Fatalf("error = %v, want the store failure", err)
		}
		w.requireWrites(before, 0)
		if w.hasProposal(baselineProposal) {
			t.Fatal("a proposal was stored")
		}
	})

	t.Run("bytes differ from the manifest", func(t *testing.T) {
		w := newWorld(t)
		before := w.revision()
		writer := newFakeWriter()
		tree := baselineTree("v1")
		tree.changed["tests/a_test.go"] = "tampered"

		_, err := governance.ProposeBaseline(context.Background(), w.mem, writer, baselineRequest("revision-1", "pinned-commit", tree))

		if !errors.Is(err, inventory.ErrInvalidTree) {
			t.Fatalf("error = %v, want an invalid tree", err)
		}
		w.requireWrites(before, 0)
		if w.hasProposal(baselineProposal) {
			t.Fatal("a proposal was stored")
		}
		for _, content := range []string{"v1", "tampered"} {
			if _, stored := writer.stored[artifact.Hash([]byte(content))]; stored {
				t.Fatalf("the mismatching entry was stored as %q", content)
			}
		}
	})
}

func TestBaselineSurfacesInventoryErrorsAndWritesNothing(t *testing.T) {
	tests := []struct {
		name string
		tree *fakeTree
		want error
	}{
		{"invalid declaration", newFakeTree(regularFile(scope.DeclarationPath, "")), scope.ErrInvalidDeclaration},
		{"unsafe entry", newFakeTree(treeFile{".github/workflows/ci.yml", inventory.Symlink, ""}), inventory.ErrUnsafeEntry},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := newWorld(t)
			before := w.revision()
			writer := newFakeWriter()

			_, err := governance.ProposeBaseline(context.Background(), w.mem, writer, baselineRequest("revision-1", "pinned-commit", test.tree))

			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			w.requireWrites(before, 0)
			if w.hasProposal(baselineProposal) || len(writer.stored) != 0 {
				t.Fatalf("something was written: proposal %v, %d blobs", w.hasProposal(baselineProposal), len(writer.stored))
			}
		})
	}
}

func TestBaselineRejectsMissingCollaborators(t *testing.T) {
	w := newWorld(t)
	request := baselineRequest("revision-1", "pinned-commit", baselineTree("v1"))
	noTree := request
	noTree.Tree = nil
	tests := []struct {
		name    string
		uow     governance.UnitOfWork
		content governance.ContentWriter
		request governance.BaselineRequest
		missing string
	}{
		{"unit of work", nil, newFakeWriter(), request, "no unit of work"},
		{"content writer", w.mem, nil, request, "no content writer"},
		{"tree", w.mem, newFakeWriter(), noTree, "no tree"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := governance.ProposeBaseline(context.Background(), test.uow, test.content, test.request)
			if !errors.Is(err, governance.ErrInvalidRequest) || !strings.Contains(err.Error(), test.missing) {
				t.Fatalf("error = %v, want an invalid request naming %q", err, test.missing)
			}
		})
	}
}
