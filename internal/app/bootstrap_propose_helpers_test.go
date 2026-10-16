package app

import (
	"context"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// proposeBaseline proposes the bootstrap on a pull request as the commands job does: P<n+1>-R1 of the origin kind, carried by
// the pull request, from head with files, with its bootstrap summary. The Suite must exist.
func proposeBaseline(t *testing.T, uow governance.UnitOfWork, content governance.ContentWriter, repositoryID int64, repo string, kind contract.OriginKind, pull int, head string,
	files map[string]string) contract.ProposalReference {
	t.Helper()
	project := owner.ProjectID(repositoryID)
	var id contract.ProposalID
	if err := uow.Do(t.Context(), project, defaultSuite, func(ctx context.Context, tx governance.Tx) (err error) {
		id, err = tx.NextProposalID(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	reference := contract.ProposalReference{ProjectID: project, SuiteID: defaultSuite, ProposalID: id, RevisionID: "R1"}
	proposal, revision, _ := parseReference(referenceText(reference))
	tree := &bootTree{files: files}
	inv, err := inventory.Build(t.Context(), tree)
	if err != nil {
		t.Fatal(err)
	}
	inputs := bootstrapSummaryInputs{Proposal: proposal, Revision: revision, Source: sourcePull, Origin: head, Tier: "isolated"}
	if kind == contract.OriginBranch {
		inputs.Source, inputs.Branch = sourceBranch, "main"
	}
	message, err := newSummaryMessage(commentTarget{RepositoryID: repositoryID, Repo: repo, Pull: pull}, inventoryInputs(inputs, inv))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := governance.ProposeBaseline(t.Context(), uow, content, governance.BaselineRequest{Reference: reference, Carrier: carrierID(repositoryID, pull),
		Origin: contract.SourceRevision(head), OriginKind: kind, Tree: tree, Outbox: []governance.OutboxMessage{message}}); err != nil {
		t.Fatalf("propose the bootstrap of pull %d: %v", pull, err)
	}
	return reference
}

// pullProposal proposes the bootstrap of pull request pull from its head, as the commands job does.
func (w *bootWorld) pullProposal(pull int, head string, files map[string]string) contract.ProposalReference {
	w.t.Helper()
	if !w.suiteExists() {
		w.seedSuite(bootRepoID, "branch:main")
	}
	return proposeBaseline(w.t, w.suites, w.content, bootRepoID, bootRepo, contract.OriginPull, pull, head, files)
}

// branchProposal proposes the bootstrap of the default branch head (bootHead, protecting tests/**) on pull request pull.
func (w *bootWorld) branchProposal(pull int) contract.ProposalReference {
	w.t.Helper()
	if !w.suiteExists() {
		w.seedSuite(bootRepoID, "branch:main")
	}
	return proposeBaseline(w.t, w.suites, w.content, bootRepoID, bootRepo, contract.OriginBranch, pull, bootHead, bootFiles("v1"))
}
