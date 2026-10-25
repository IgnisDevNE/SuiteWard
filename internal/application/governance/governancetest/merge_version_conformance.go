package governancetest

import (
	"context"
	"maps"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The cases of Tx.VersionsHolding, the one read with which a merge decides whether it restores an earlier version.

// mvSeedSuite seeds a Suite whose only version, under its own id, holds the contract of content.
func (e *cfEnv) mvSeedSuite(project contract.ProjectID, suite contract.SuiteID, version contract.SuiteVersionID, content string) {
	e.t.Helper()
	c := cfNewCandidate(project, suite, "pb", "", content)
	stored := cfMust(contract.NewSuiteVersion(project, suite, version, c.protected.Manifest()))
	record := cfMust(contract.NewPromotionRecord(contract.PromotionRecordInput{OperationID: contract.OperationID("seed-op-" + version), VersionID: version, Binding: c.proposal.Current().Binding(),
		Carrier: c.carrier, Source: c.merged, Target: cfTarget, RecordedAt: cfRecordedAt}))
	history := cfMust(contract.NewHistoricalCanonical(stored, record))
	current := cfMust(contract.NewSuite(project, suite, version, cfSeededRevision))
	canonical := cfMust(contract.NewCanonicalSnapshot(current, stored, c.protected, record))
	e.seed(governance.Seed{
		Suite:     governance.SuiteState{Canonical: canonical, Policy: e.policy(project), Target: cfTarget},
		History:   []contract.HistoricalCanonical{history},
		Proposals: []governance.SeedProposal{{Proposal: c.proposal, Assessments: c.assessments}},
	})
}

// mvHolding reads the versions of a Suite that hold the manifest of the contract of content.
func (e *cfEnv) mvHolding(project contract.ProjectID, suite contract.SuiteID, content string) map[contract.SuiteVersionID]bool {
	e.t.Helper()
	var holding map[contract.SuiteVersionID]bool
	err := e.doIn(project, suite, func(ctx context.Context, tx governance.Tx) (err error) {
		holding, err = tx.VersionsHolding(ctx, cfProtectedOf(content).Manifest().Digest())
		return err
	})
	if err != nil {
		e.t.Fatalf("VersionsHolding of %s/%s: %v", project, suite, err)
	}
	return holding
}

func (e *cfEnv) mvRequireHolding(project contract.ProjectID, suite contract.SuiteID, content string, want map[contract.SuiteVersionID]bool) {
	e.t.Helper()
	if got := e.mvHolding(project, suite, content); !maps.Equal(got, want) {
		e.t.Errorf("versions of %s/%s holding the manifest of %q = %v, want %v", project, suite, content, got, want)
	}
}

// The read names every version of the locked Suite, so a version missing below the current one is seen as a gap, and says
// which of them hold the manifest. A version of another Suite is neither listed nor matched, though it holds the very same
// manifest: not one of the same project, not one of another project under the same Suite id.
func mvListsEveryVersionOfTheLockedSuite(e *cfEnv) {
	e.seedRich() // version-1 holds "v1" and version-2 holds "v2"
	e.mvSeedSuite(cfProject, "suite-c", "version-c", "v1")
	e.mvSeedSuite("project-b", cfSuite, "version-b", "v1")

	e.mvRequireHolding(cfProject, cfSuite, "v1", map[contract.SuiteVersionID]bool{"version-1": true, "version-2": false})
	e.mvRequireHolding(cfProject, cfSuite, "v2", map[contract.SuiteVersionID]bool{"version-1": false, "version-2": true})
	e.mvRequireHolding(cfProject, cfSuite, "v3", map[contract.SuiteVersionID]bool{"version-1": false, "version-2": false})
	e.mvRequireHolding(cfProject, "suite-c", "v1", map[contract.SuiteVersionID]bool{"version-c": true})
	e.mvRequireHolding("project-b", cfSuite, "v1", map[contract.SuiteVersionID]bool{"version-b": true})
	e.mvRequireHolding("project-b", cfSuite, "v2", map[contract.SuiteVersionID]bool{"version-b": false})
}

// A Suite without a canonical has no version to list, and a transaction that ended cannot be read.
func mvIsEmptyWithoutVersionsAndFailsAfterTheUnitOfWork(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite, e.candidate("p1", "", "v1"))

	e.mvRequireHolding(cfProject, cfSuite, "v1", map[contract.SuiteVersionID]bool{})

	var leaked governance.Tx
	e.read(func(_ context.Context, tx governance.Tx) error {
		leaked = tx
		return nil
	})
	if _, err := leaked.VersionsHolding(context.Background(), artifact.Hash(cfBlob("v1"))); err == nil {
		e.t.Error("a transaction can still list versions after its unit of work ended")
	}
}
