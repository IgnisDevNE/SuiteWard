package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The explicit bootstrap commands, /suiteward propose and /suiteward skip (docs/contracts/runtime.md, "Explicit
// bootstrap: propose and skip"). The default world has repository 1 with a Suite whose proposal P1 is carried by pull
// request 7, and repository 2 with no Suite.

const cjOtherPull = 8

func cjPropose(id int64, created time.Time) cjComment {
	return cjComment{pull: cjPull, id: id, author: cjOwnerID, created: created, updated: created, body: cjMarker + "\n/suiteward propose"}
}

func cjSkip(id int64, created time.Time, offset int64) cjComment {
	return cjComment{pull: cjPull, id: id, author: cjOwnerID, created: created, updated: created, body: cjMarker + "\n/suiteward skip " + cjCode(created, offset)}
}

func (c cjComment) onPull(pull int) cjComment {
	c.pull = pull
	return c
}

// trees sets the default branch of repository 1 and the head of its pull requests to these files.
func (w *cjWorld) trees(main, head map[string]string) {
	w.t.Helper()
	repo := w.reader.repo(cjRepo1)
	repo.trees[bootHead] = &bootTree{files: main}
	repo.trees[cjHead] = &bootTree{files: head}
}

// ackPayload requires the entry of the comment to be d with this acknowledgment and no other field.
func (w *cjWorld) requireAckPayload(repositoryID, comment int64, d commands.Disposition, extra map[string]any) {
	w.t.Helper()
	_, ack := w.requireDisposition(repositoryID, comment, d)
	if ack == nil {
		w.t.Fatalf("comment %d (%s) has no acknowledgment", comment, d)
	}
	if want := fmt.Sprintf("ack:%d:%d", repositoryID, comment); ack.Key != want || ack.Kind != githubCommentKind {
		w.t.Fatalf("acknowledgment %s %s; want %s %s", ack.Key, ack.Kind, want, githubCommentKind)
	}
	want := map[string]any{"repositoryId": repositoryID, "repo": cjRepoName[repositoryID], "pull": cjPull, "comment": comment, "owner": cjLogin, "disposition": string(d)}
	for key, value := range extra {
		want[key] = value
	}
	raw, err := json.Marshal(want)
	if err != nil {
		w.t.Fatal(err)
	}
	var wantDecoded, got map[string]any
	if err := json.Unmarshal(raw, &wantDecoded); err != nil {
		w.t.Fatal(err)
	}
	if err := json.Unmarshal(ack.Payload, &got); err != nil {
		w.t.Fatal(err)
	}
	if !reflect.DeepEqual(got, wantDecoded) {
		w.t.Fatalf("comment %d: acknowledgment payload %s; want %s", comment, ack.Payload, raw)
	}
}

func (w *cjWorld) requireNoAck(repositoryID, comment int64, d commands.Disposition) {
	w.t.Helper()
	w.requireSilent(repositoryID, comment, d)
}

// message returns the outbox message with this key that a unit of work committed.
func (w *cjWorld) message(key string) (governance.OutboxMessage, bool) {
	for _, m := range w.suites.messages {
		if m.Key == key {
			return m, true
		}
	}
	return governance.OutboxMessage{}, false
}

func (w *cjWorld) summaries() int {
	count := 0
	for _, m := range w.suites.messages {
		if strings.HasPrefix(m.Key, bootstrapSummaryKey+":") {
			count++
		}
	}
	return count
}

// proposal returns the current revision of the proposal.
func (w *cjWorld) proposal(repositoryID int64, id contract.ProposalID) (contract.ProposalRevision, bool) {
	w.t.Helper()
	var current contract.ProposalRevision
	found := true
	err := w.mem.Do(w.t.Context(), owner.ProjectID(repositoryID), "default", func(ctx context.Context, tx governance.Tx) error {
		proposal, _, err := tx.Proposal(ctx, id)
		if errors.Is(err, governance.ErrNotFound) {
			found = false
			return nil
		}
		if err != nil {
			return err
		}
		current = proposal.Current()
		return nil
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return current, found
}

func (w *cjWorld) requireProposal(id contract.ProposalID, revision contract.ProposalRevisionID, origin string, kind contract.OriginKind, pull int) {
	w.t.Helper()
	current, found := w.proposal(cjID1, id)
	if !found {
		w.t.Fatalf("proposal %s does not exist\nlogs: %s", id, w.logs)
	}
	if current.Binding().Reference().RevisionID != revision || string(current.Origin()) != origin || current.OriginKind() != kind || current.Carrier() != carrierID(cjID1, pull) {
		w.t.Fatalf("proposal %s current revision = %s from %s (%v) carried by %s; want %s from %s (%v) carried by pull %d", id, current.Binding().Reference().RevisionID,
			current.Origin(), current.OriginKind(), current.Carrier(), revision, origin, kind, pull)
	}
}

func (w *cjWorld) requireNoProposal(id contract.ProposalID) {
	w.t.Helper()
	if _, found := w.proposal(cjID1, id); found {
		w.t.Fatalf("proposal %s exists; want none", id)
	}
}

// seedCanonical gives the repository's project a Suite whose canonical is V1.
func (w *cjWorld) seedCanonical(repositoryID int64) {
	w.t.Helper()
	project, carrier := owner.ProjectID(repositoryID), carrierID(repositoryID, 3)
	protected := cjMust(contract.NewProtectedContract(cjMust(artifact.NewManifest([]artifact.Entry{{Path: "tests/a_test.go", Content: artifact.Hash([]byte("v1"))}})),
		artifact.Hash([]byte("tests/**")), map[string]string{}))
	reference := contract.ProposalReference{ProjectID: project, SuiteID: "default", ProposalID: "P1", RevisionID: "R1"}
	binding := cjMust(contract.NewApprovalBinding(contract.BindingInput{Reference: reference, Manifest: protected.Manifest().Digest(), Scope: protected.ScopeDigest(),
		PolicyRevision: "setup-1", CoveredInputs: protected.CoveredInputs()}))
	proposal := cjMust(contract.NewProposal(cjMust(contract.NewProposalRevision(binding, "origin", contract.OriginBranch, carrier))))
	version := cjMust(contract.NewSuiteVersion(project, "default", "V1", protected.Manifest()))
	record := cjMust(contract.NewPromotionRecord(contract.PromotionRecordInput{OperationID: "seed-canonical", VersionID: "V1", Binding: binding, Carrier: carrier,
		Source: "origin", Target: "branch:main", RecordedAt: cjOrigin.Add(time.Minute)}))
	canonical := cjMust(contract.NewCanonicalSnapshot(cjMust(contract.NewSuite(project, "default", "V1", 1)), version, protected, record))
	principal := cjMust(contract.NewPrincipal(owner.PrincipalID(cjOwnerID), contract.Human))
	seed := governance.Seed{Suite: governance.SuiteState{Canonical: canonical, Policy: cjMust(contract.NewPolicy(project, "setup-1", principal)), Target: "branch:main"},
		History: []contract.HistoricalCanonical{cjMust(contract.NewHistoricalCanonical(version, record))}, Proposals: []governance.SeedProposal{{Proposal: proposal}}}
	if err := w.mem.Seed(w.t.Context(), seed); err != nil {
		w.t.Fatal(err)
	}
}

// seedBaseline gives the repository's project a Suite (policy setup-1) whose only proposal P1-R1 is the branch baseline
// of the default branch's tree as it is now, carried by pull request 7 and bound to bindingPolicy.
func (w *cjWorld) seedBaseline(repositoryID int64, bindingPolicy contract.PolicyRevisionID) {
	w.t.Helper()
	project := owner.ProjectID(repositoryID)
	inv, err := inventory.Build(w.t.Context(), w.reader.repo(cjRepoName[repositoryID]).trees[bootHead])
	if err != nil {
		w.t.Fatal(err)
	}
	protected := cjMust(inv.Contract())
	reference := contract.ProposalReference{ProjectID: project, SuiteID: "default", ProposalID: "P1", RevisionID: "R1"}
	binding := cjMust(contract.NewApprovalBinding(contract.BindingInput{Reference: reference, Manifest: protected.Manifest().Digest(), Scope: protected.ScopeDigest(),
		PolicyRevision: bindingPolicy, CoveredInputs: protected.CoveredInputs()}))
	proposal := cjMust(contract.NewProposal(cjMust(contract.NewProposalRevision(binding, bootHead, contract.OriginBranch, carrierID(repositoryID, cjPull)))))
	principal := cjMust(contract.NewPrincipal(owner.PrincipalID(cjOwnerID), contract.Human))
	canonical := cjMust(contract.NewCanonicalSnapshot(cjMust(contract.NewSuite(project, "default", "", 7)), contract.SuiteVersion{}, contract.ProtectedContract{}, contract.PromotionRecord{}))
	seed := governance.Seed{Suite: governance.SuiteState{Canonical: canonical, Policy: cjMust(contract.NewPolicy(project, "setup-1", principal)), Target: "branch:main"},
		Proposals: []governance.SeedProposal{{Proposal: proposal}}}
	if err := w.mem.Seed(w.t.Context(), seed); err != nil {
		w.t.Fatal(err)
	}
}

func TestProposeFromTheDefaultBranchWhenItHoldsTheDeclaration(t *testing.T) {
	w := newCjWorld(t)
	w.trees(bootFiles("v1"), bootFiles("v2")) // both have .suiteward.yml: the default branch wins
	w.syncAll([]cjComment{cjPropose(500, cjAt(10))})
	w.mustRun()

	w.requireNoAck(cjID1, 500, commands.Proposed)
	w.requireProposal("P2", "R1", bootHead, contract.OriginBranch, cjPull) // beside the seeded P1
	message, found := w.message("bootstrap-summary:1001:P2-R1")
	if !found {
		t.Fatalf("no bootstrap summary among %d messages", len(w.suites.messages))
	}
	var payload bootstrapSummaryPayload
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	summary := payload.Summary
	if payload.Repo != cjRepo1 || payload.Pull != cjPull || summary.Source != sourceBranch || summary.Origin != bootHead || summary.Branch != "main" ||
		summary.Protected != 3 || summary.Tier != owner.TierIsolated || summary.Proposal != 2 || summary.Revision != 1 {
		t.Fatalf("summary = %+v on %+v", summary, payload.commentTarget)
	}
	if len(w.content.stored) != 3 {
		t.Fatalf("stored %d files; want the 3 protected ones", len(w.content.stored))
	}
	w.requireNothingLeaked(cjCode(cjAt(10), 0))
}

func TestProposeFromThePullRequestWhenOnlyItHoldsTheDeclaration(t *testing.T) {
	w := newCjWorld(t)
	w.trees(map[string]string{"README.md": "x"}, bootFiles("v2"))
	w.syncAll([]cjComment{cjPropose(500, cjAt(10))})
	w.mustRun()

	w.requireNoAck(cjID1, 500, commands.Proposed)
	w.requireProposal("P2", "R1", cjHead, contract.OriginPull, cjPull)
	message, found := w.message("bootstrap-summary:1001:P2-R1")
	if !found {
		t.Fatal("no bootstrap summary")
	}
	var payload bootstrapSummaryPayload
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Summary.Source != sourcePull || payload.Summary.Origin != cjHead || payload.Summary.Branch != "" || payload.Summary.Protected != 3 {
		t.Fatalf("summary = %+v; want a pull summary of the head, with no branch", payload.Summary)
	}
}

func TestProposeRefusesAndSuggestsWhenNoTreeHoldsADeclaration(t *testing.T) {
	for _, tc := range []struct {
		name       string
		main, head map[string]string
		suggestion []string
	}{
		{"from the test-like paths of the pull request head", map[string]string{"spec/a_spec.rb": "x"}, map[string]string{"tests/a_test.go": "x", "src/main.go": "y"}, []string{"tests/**", "tests/*_test.*"}},
		{"from the default branch when the pull request gives none", map[string]string{"spec/a_spec.rb": "x"}, map[string]string{"src/main.go": "y"}, []string{"spec/**"}},
		{"empty when no path is test-like", map[string]string{"README.md": "x"}, map[string]string{"src/main.go": "y"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCjWorld(t)
			w.trees(tc.main, tc.head)
			w.syncAll([]cjComment{cjPropose(500, cjAt(10))})
			w.mustRun()

			extra := map[string]any{"reason": "declaration-missing", "branch": cjBranch}
			if tc.suggestion != nil {
				extra["suggestion"] = tc.suggestion
			}
			w.requireAckPayload(cjID1, 500, commands.ProposeRefused, extra)
			w.requireNoProposal("P2")
			w.requireNothingLeaked()
		})
	}
}

func TestProposeRefusesAForkAndACanonicalAndNoSuite(t *testing.T) {
	for _, tc := range []struct {
		name       string
		repository int64
		setup      func(w *cjWorld)
		d          commands.Disposition
		extra      map[string]any
	}{
		{"a pull request from a fork", cjID1, func(w *cjWorld) { w.headRepo = "evil/widgets" }, commands.ProposeRefused, map[string]any{"reason": "fork"}},
		{"a Suite that has a canonical", cjID2, func(w *cjWorld) { w.seedCanonical(cjID2) }, commands.ProposeRefused, map[string]any{"reason": "canonical-present"}},
		{"a canonical wins over a fork", cjID2, func(w *cjWorld) { w.seedCanonical(cjID2); w.headRepo = "evil/gadgets" }, commands.ProposeRefused, map[string]any{"reason": "canonical-present"}},
		{"no Suite", cjID2, func(*cjWorld) {}, commands.NoSuite, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCjWorld(t)
			tc.setup(w)
			if tc.repository == cjID1 {
				w.syncAll([]cjComment{cjPropose(500, cjAt(10))})
			} else {
				w.syncAll(nil, cjPropose(500, cjAt(10)))
			}
			w.mustRun()

			w.requireAckPayload(tc.repository, 500, tc.d, tc.extra)
			if w.summaries() != 0 || len(w.owners.consumed) != 0 {
				t.Fatalf("%d summaries and %d consumed steps; a refused propose proposes nothing and uses no code", w.summaries(), len(w.owners.consumed))
			}
		})
	}
}

func TestProposeTakesTheNextNumberBesideOlderProposalsOfEitherKind(t *testing.T) {
	w := newCjWorld(t)
	w.trees(bootFiles("v1"), bootFiles("v1"))
	w.syncAll([]cjComment{cjPropose(500, cjAt(10)), cjPropose(501, cjAt(11)).onPull(cjOtherPull)})
	w.mustRun()

	w.requireProposal("P2", "R1", bootHead, contract.OriginBranch, cjPull)
	w.requireProposal("P3", "R1", bootHead, contract.OriginBranch, cjOtherPull) // a different carrier: a new proposal, not a reuse
	if w.summaries() != 2 {
		t.Fatalf("%d summaries; want one for each proposal", w.summaries())
	}
}

func TestProposeAgainOnAnUnchangedPullRequestReusesTheProposal(t *testing.T) {
	w := newCjWorld(t)
	w.trees(bootFiles("v1"), bootFiles("v1"))
	w.syncAll([]cjComment{cjPropose(500, cjAt(10)), cjPropose(501, cjAt(11))})
	w.mustRun()

	w.requireNoAck(cjID1, 500, commands.Proposed)
	w.requireAckPayload(cjID1, 501, commands.Proposed, map[string]any{"reference": "P2-R1"})
	w.requireNoProposal("P3")
	if w.summaries() != 1 {
		t.Fatalf("%d summaries; want the one of the first propose", w.summaries())
	}
}

func TestProposeAfterACrashBetweenTheProposalAndTheLedgerReusesIt(t *testing.T) {
	w := newCjWorld(t)
	w.trees(bootFiles("v1"), bootFiles("v1"))
	w.syncAll([]cjComment{cjPropose(500, cjAt(10))})
	w.ledger.recordErr = errCjStore
	if err := w.run(); !errors.Is(err, errLedger) {
		t.Fatalf("run = %v; want the ledger failure", err)
	}
	w.requireProposal("P2", "R1", bootHead, contract.OriginBranch, cjPull) // written before the ledger failed
	w.ledger.recordErr = nil
	w.mustRun()

	w.requireAckPayload(cjID1, 500, commands.Proposed, map[string]any{"reference": "P2-R1"})
	w.requireNoProposal("P3")
	if w.summaries() != 1 {
		t.Fatalf("%d summaries; want one", w.summaries())
	}
}

func TestProposeReusesAProposalOnlyWhenThePolicyRevisionMatchesToo(t *testing.T) {
	for _, tc := range []struct {
		name       string
		binding    contract.PolicyRevisionID
		wantReuse  bool
		afterSetup func(w *cjWorld)
	}{
		{"the same origin and coverage", "setup-1", true, func(*cjWorld) {}},
		// A carrier that AssessBootstrapHead cancels for a policy-only difference must not be told "already proposed".
		{"a different policy revision", "setup-0", false, func(*cjWorld) {}},
		{"a default branch that moved", "setup-1", false, func(w *cjWorld) {
			w.reader.repo(cjRepo2).heads["main"] = bootMovedHead
			w.reader.repo(cjRepo2).trees[bootMovedHead] = &bootTree{files: bootFiles("v1")}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCjWorld(t)
			w.seedBaseline(cjID2, tc.binding)
			tc.afterSetup(w)
			w.syncAll(nil, cjPropose(500, cjAt(10)))
			w.mustRun()

			if tc.wantReuse {
				w.requireAckPayload(cjID2, 500, commands.Proposed, map[string]any{"reference": "P1-R1"})
				if w.summaries() != 0 {
					t.Fatalf("%d summaries; a reused proposal writes none", w.summaries())
				}
				return
			}
			w.requireNoAck(cjID2, 500, commands.Proposed)
			if _, found := w.message("bootstrap-summary:1002:P2-R1"); !found {
				t.Fatalf("no summary of P2-R1; the proposal must not be reused\nlogs: %s", w.logs)
			}
		})
	}
}

func TestProposeRetriesAGitHubFailureAndIsFailedAfterFiveAttempts(t *testing.T) {
	w := newCjWorld(t)
	w.reader.repo(cjRepo1).headErr = errors.New(bootSecret)
	w.syncAll([]cjComment{cjPropose(500, cjAt(10))})
	for attempt := 1; attempt <= 4; attempt++ {
		if err := w.run(); err != nil {
			t.Fatalf("attempt %d: %v; a GitHub failure stops the run, it does not fail the job", attempt, err)
		}
		w.requireNoEntry(cjID1, 500)
	}
	w.mustRun()

	w.requireAckPayload(cjID1, 500, commands.Failed, nil)
	w.requireNothingLeaked(bootSecret)
}

func TestProposeFailureOfAnotherCommandGetsNoAcknowledgment(t *testing.T) {
	w := newCjWorld(t)
	w.syncAll([]cjComment{ownerCommand(500, cjAt(10), "approve", "P1-R2", 0)})
	w.owners.consumeErr = func(contract.SourceCommandID) error { return errCjStore }
	for range 5 {
		if err := w.run(); err != nil {
			t.Fatal(err)
		}
	}

	w.requireSilent(cjID1, 500, commands.Failed)
}

func TestProposeIsNotAnOwnerCommandForTheLedger(t *testing.T) {
	w := newCjWorld(t)
	w.trees(bootFiles("v1"), bootFiles("v1"))
	w.syncAll([]cjComment{ownerCommand(500, cjAt(10), "approve", "P1-R2", 0), cjPropose(501, cjAt(11)), cjSkip(502, cjAt(12), 0).onPull(cjOtherPull)})
	w.mustRun()

	w.requireProcessed(cjID1, 500)
	w.requireDisposition(cjID1, 501, commands.Proposed)
	w.requireDisposition(cjID1, 502, commands.Skipped)
	latest, found, err := w.ledger.LatestOwnerCommand(t.Context(), cjID1, cjPull, cjOwnerID)
	if err != nil || !found || latest.CommentID != 500 {
		t.Fatalf("latest owner command = %+v, %v, %v; want the approval 500", latest, found, err)
	}
}

func TestSkipRejectsAnInvalidAndAReplayedCodeWithoutAReference(t *testing.T) {
	w := newCjWorld(t)
	w.syncAll([]cjComment{cjSkip(500, cjAt(10), 5).onPull(cjOtherPull), cjSkip(501, cjAt(20), 0).onPull(cjOtherPull), cjSkip(502, cjAt(20).Add(5*time.Second), 0).onPull(cjOtherPull)})
	w.mustRun()

	w.requireAckPayload(cjID1, 500, commands.InvalidCode, map[string]any{"pull": cjOtherPull})
	w.requireDisposition(cjID1, 501, commands.Skipped)
	w.requireAckPayload(cjID1, 502, commands.ReplayedCode, map[string]any{"pull": cjOtherPull})
	w.requireNothingLeaked(cjCode(cjAt(10), 0), cjCode(cjAt(10), 5), cjCode(cjAt(20), 0))
}

func TestSkipRefusesAfterUsingTheCode(t *testing.T) {
	for _, tc := range []struct {
		name       string
		repository int64
		pull       int
		setup      func(w *cjWorld)
		reason     string
	}{
		{"a Suite that has a canonical", cjID2, cjPull, func(w *cjWorld) { w.seedCanonical(cjID2) }, "canonical-present"},
		{"a pull request from a fork", cjID1, cjOtherPull, func(w *cjWorld) { w.headRepo = "evil/widgets" }, "fork"},
		{"the carrier of the latest bootstrap proposal", cjID1, cjPull, func(*cjWorld) {}, "carrier"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCjWorld(t)
			tc.setup(w)
			comment := cjSkip(500, cjAt(10), 0).onPull(tc.pull)
			if tc.repository == cjID1 {
				w.syncAll([]cjComment{comment})
			} else {
				w.syncAll(nil, comment)
			}
			w.mustRun()

			w.requireAckPayload(tc.repository, 500, commands.SkipRefused, map[string]any{"reason": tc.reason, "pull": tc.pull})
			if len(w.owners.consumed) != 1 {
				t.Fatalf("consumed %d steps; a refused skip has used its code", len(w.owners.consumed))
			}
		})
	}
}

func TestSkipForAProjectWithNoSuiteIsNoSuiteWithoutAReference(t *testing.T) {
	w := newCjWorld(t)
	w.syncAll(nil, cjSkip(500, cjAt(10), 0))
	w.mustRun()

	w.requireAckPayload(cjID2, 500, commands.NoSuite, nil)
	if len(w.owners.consumed) != 1 {
		t.Fatalf("consumed %d steps; the code is used before the Suite is looked for", len(w.owners.consumed))
	}
}

func TestSkipRecordsSkippedForThatPullRequestOnly(t *testing.T) {
	w := newCjWorld(t)
	w.syncAll([]cjComment{cjSkip(500, cjAt(10), 0).onPull(cjOtherPull)})
	w.mustRun()

	w.requireAckPayload(cjID1, 500, commands.Skipped, map[string]any{"pull": cjOtherPull})
	for pull, want := range map[int]bool{cjOtherPull: true, cjPull: false, 9: false} {
		if got, err := w.ledger.Skipped(t.Context(), cjID1, pull, cjOrigin); err != nil || got != want {
			t.Fatalf("Skipped(pull %d) = %v, %v; want %v", pull, got, err, want)
		}
	}
	w.requireNothingLeaked(cjCode(cjAt(10), 0))
	w.requireNoEntryAfterSecondRun(500)
}

// The publisher renders the new acknowledgments.

func cmtAckPayload(extra map[string]any) json.RawMessage {
	payload := map[string]any{"repositoryId": cmtRepoID, "repo": cmtRepo, "pull": cmtPull, "comment": cmtComment, "owner": cmtOwner}
	for key, value := range extra {
		payload[key] = value
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return raw
}

func TestCommentPublisherRendersTheBootstrapAcknowledgments(t *testing.T) {
	suggestion := []string{"tests/**", "spec/**"}
	ack := func(a commandtext.Ack) func() string {
		a.Owner, a.Source = cmtOwner, cmtComment
		return func() string { return commandtext.Acknowledge(a) }
	}
	suggest := func(branch string, patterns []string) func() string {
		return func() string {
			return commandtext.SuggestDeclaration(commandtext.DeclarationSuggestion{Source: cmtComment, Repo: cmtRepo, Branch: branch, Patterns: patterns})
		}
	}
	for _, tc := range []struct {
		name  string
		extra map[string]any
		text  func() string
	}{
		{"propose refused for a canonical", map[string]any{"disposition": "propose-refused", "reason": "canonical-present"}, ack(commandtext.Ack{Outcome: commandtext.ProposeRefused, Refusal: commandtext.CanonicalPresent})},
		{"propose refused for a fork", map[string]any{"disposition": "propose-refused", "reason": "fork"}, ack(commandtext.Ack{Outcome: commandtext.ProposeRefused, Refusal: commandtext.Fork})},
		{"propose refused for an empty inventory", map[string]any{"disposition": "propose-refused", "reason": "empty-inventory"}, ack(commandtext.Ack{Outcome: commandtext.ProposeRefused, Refusal: commandtext.EmptyInventory})},
		{"propose refused for a missing declaration", map[string]any{"disposition": "propose-refused", "reason": "declaration-missing", "branch": "feature/x", "suggestion": suggestion}, suggest("feature/x", suggestion)},
		{"propose refused for a missing declaration with no suggestion", map[string]any{"disposition": "propose-refused", "reason": "declaration-missing", "branch": "feature"}, suggest("feature", nil)},
		{"already proposed", map[string]any{"disposition": "proposed", "reference": "P3-R1"}, ack(commandtext.Ack{Outcome: commandtext.AlreadyProposed, Proposal: 3, Revision: 1})},
		{"propose failed", map[string]any{"disposition": "failed"}, ack(commandtext.Ack{Outcome: commandtext.ProposeFailed})},
		{"skipped", map[string]any{"disposition": "skipped"}, ack(commandtext.Ack{Outcome: commandtext.Skipped})},
		{"skip refused for a canonical", map[string]any{"disposition": "skip-refused", "reason": "canonical-present"}, ack(commandtext.Ack{Outcome: commandtext.SkipRefused, Refusal: commandtext.CanonicalPresent})},
		{"skip refused for a fork", map[string]any{"disposition": "skip-refused", "reason": "fork"}, ack(commandtext.Ack{Outcome: commandtext.SkipRefused, Refusal: commandtext.Fork})},
		{"skip refused for the carrier", map[string]any{"disposition": "skip-refused", "reason": "carrier"}, ack(commandtext.Ack{Outcome: commandtext.SkipRefused, Refusal: commandtext.Carrier})},
		{"an invalid code without a reference", map[string]any{"disposition": "invalid-code"}, ack(commandtext.Ack{Outcome: commandtext.InvalidCode})},
		{"a replayed code without a reference", map[string]any{"disposition": "replayed-code"}, ack(commandtext.Ack{Outcome: commandtext.ReplayedCode})},
		{"no Suite without a reference", map[string]any{"disposition": "no-suite"}, ack(commandtext.Ack{Outcome: commandtext.NoSuite})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCmtWorld(t)
			key := fmt.Sprintf("ack:%d:%d", cmtRepoID, cmtComment)
			if err := w.pub.publish(t.Context(), key, cmtAckPayload(tc.extra)); err != nil {
				t.Fatalf("publish: %v", err)
			}
			w.wantPost(key, tc.text())
		})
	}
}

func TestCommentPublisherRefusesADeclarationMissingPayloadTheRendererWouldPanicOn(t *testing.T) {
	good := map[string]any{"disposition": "propose-refused", "reason": "declaration-missing", "branch": "feature", "suggestion": []string{"tests/**"}}
	with := func(key string, value any) map[string]any {
		payload := map[string]any{}
		for k, v := range good {
			payload[k] = v
		}
		if value == nil {
			delete(payload, key)
		} else {
			payload[key] = value
		}
		return payload
	}
	for name, extra := range map[string]map[string]any{
		"a branch with two dots":            with("branch", "a..b"),
		"a branch that is empty":            with("branch", ""),
		"a branch that is absent":           with("branch", nil),
		"a branch with a space":             with("branch", "a b"),
		"more than 20 suggested patterns":   with("suggestion", strings.Split(strings.Repeat("tests/**,", 21), ",")[:21]),
		"a branch on another disposition":   {"disposition": "skipped", "branch": "feature"},
		"a suggestion on another reason":    {"disposition": "propose-refused", "reason": "fork", "suggestion": []string{"tests/**"}},
		"a reason that is not a refusal":    {"disposition": "propose-refused", "reason": "bad-code"},
		"a skip refused for an empty input": {"disposition": "skip-refused", "reason": "empty-inventory"},
		"a reference on a skip":             {"disposition": "skipped", "reference": "P1-R1"},
		"a proposed without a reference":    {"disposition": "proposed"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newCmtWorld(t)
			err := w.pub.publish(t.Context(), fmt.Sprintf("ack:%d:%d", cmtRepoID, cmtComment), cmtAckPayload(extra))
			if err == nil || !strings.Contains(err.Error(), "does not match its key") {
				t.Fatalf("publish = %v; want a validation error, not a render failure", err)
			}
			if len(w.writer.posts()) != 0 {
				t.Fatal("a comment was posted")
			}
		})
	}
	t.Run("a repository the renderer refuses", func(t *testing.T) {
		w := newCmtWorld(t)
		payload := map[string]any{"repositoryId": cmtRepoID, "repo": "-bad/widgets", "pull": cmtPull, "comment": cmtComment, "owner": cmtOwner,
			"disposition": "propose-refused", "reason": "declaration-missing", "branch": "feature"}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		err = w.pub.publish(t.Context(), fmt.Sprintf("ack:%d:%d", cmtRepoID, cmtComment), raw)
		if err == nil || !strings.Contains(err.Error(), "does not match its key") {
			t.Fatalf("publish = %v; want a validation error, not a render failure", err)
		}
	})
}

func TestAcknowledgmentConstructorsTakeNoReferenceForTheCodeOutcomes(t *testing.T) {
	for name, err := range map[string]error{
		"an invalid code": second(newRejectedAck(cmtTarget(), cmtComment, cmtOwner, commands.InvalidCode, 0, "")),
		"a replayed code": second(newRejectedAck(cmtTarget(), cmtComment, cmtOwner, commands.ReplayedCode, 0, "")),
		"no Suite":        second(newRejectedAck(cmtTarget(), cmtComment, cmtOwner, commands.NoSuite, 0, "")),
	} {
		if err != nil {
			t.Errorf("%s without a reference was refused: %v", name, err)
		}
	}
	for name, err := range map[string]error{
		"an unknown proposal without a reference": second(newRejectedAck(cmtTarget(), cmtComment, cmtOwner, commands.UnknownProposal, 0, "")),
		"a malformed command with a reference":    second(newRejectedAck(cmtTarget(), cmtComment, cmtOwner, commands.Malformed, commandtext.BadCode, "P1-R1")),
	} {
		if err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// linkedDeclaration is a tree whose .suiteward.yml is a symbolic link.
type linkedDeclaration struct{ *bootTree }

func (t linkedDeclaration) Entries(ctx context.Context) ([]inventory.Entry, error) {
	entries, err := t.bootTree.Entries(ctx)
	for i := range entries {
		if entries[i].Path == ".suiteward.yml" {
			entries[i].Kind = inventory.Symlink
		}
	}
	return entries, err
}

func TestProposeTakesTheDeclarationOnlyFromAFileEntry(t *testing.T) {
	w := newCjWorld(t)
	w.trees(bootFiles("v1"), bootFiles("v2"))
	w.reader.repo(cjRepo1).trees[bootHead] = linkedDeclaration{&bootTree{files: bootFiles("v1")}} // not a file entry: the pull request head is read
	w.syncAll([]cjComment{cjPropose(500, cjAt(10))})
	w.mustRun()

	w.requireNoAck(cjID1, 500, commands.Proposed)
	w.requireProposal("P2", "R1", cjHead, contract.OriginPull, cjPull)
}

// linkedFile is a tree where one path is a symbolic link.
type linkedFile struct {
	*bootTree
	path string
}

func (t linkedFile) Entries(ctx context.Context) ([]inventory.Entry, error) {
	entries, err := t.bootTree.Entries(ctx)
	for i := range entries {
		if entries[i].Path == t.path {
			entries[i].Kind = inventory.Symlink
		}
	}
	return entries, err
}

func TestProposeRefusesASourceThatCannotBeReadAsAContract(t *testing.T) {
	invalidDeclaration := map[string]string{".suiteward.yml": "version: 2\n", ".github/workflows/ci.yml": "on: push\n"}
	for _, tc := range []struct {
		name  string
		setup func(w *cjWorld)
	}{
		{"an invalid declaration on the default branch", func(w *cjWorld) { w.trees(invalidDeclaration, bootFiles("v1")) }},
		{"an invalid declaration in the pull request head", func(w *cjWorld) {
			w.trees(map[string]string{"README.md": "x"}, invalidDeclaration)
		}},
		{"a link inside the protected files", func(w *cjWorld) {
			w.trees(bootFiles("v1"), bootFiles("v1"))
			w.reader.repo(cjRepo1).trees[bootHead] = linkedFile{&bootTree{files: bootFiles("v1")}, "tests/a_test.go"}
		}},
		{"a tree GitHub truncated", func(w *cjWorld) {
			w.trees(bootFiles("v1"), bootFiles("v1"))
			w.reader.repo(cjRepo1).treeErr[bootHead] = fmt.Errorf("%w: %w", inventory.ErrInvalidTree, github.ErrTreeTruncated)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCjWorld(t)
			tc.setup(w)
			w.syncAll([]cjComment{cjPropose(500, cjAt(10))})
			w.mustRun() // refused at once: a deterministic source is not retried

			w.requireAckPayload(cjID1, 500, commands.ProposeRefused, map[string]any{"reason": "invalid-source"})
			w.requireNoProposal("P2")
			w.requireNothingLeaked()
		})
	}
}

func TestProposeStillRetriesATransientTreeReadFailure(t *testing.T) {
	w := newCjWorld(t)
	w.trees(bootFiles("v1"), bootFiles("v1"))
	w.reader.repo(cjRepo1).treeErr[bootHead] = errors.New(bootSecret)
	w.syncAll([]cjComment{cjPropose(500, cjAt(10))})
	w.mustRun()

	w.requireNoEntry(cjID1, 500)
}

func TestCommentPublisherRendersTheInvalidSourceRefusal(t *testing.T) {
	w := newCmtWorld(t)
	key := fmt.Sprintf("ack:%d:%d", cmtRepoID, cmtComment)
	if err := w.pub.publish(t.Context(), key, cmtAckPayload(map[string]any{"disposition": "propose-refused", "reason": "invalid-source"})); err != nil {
		t.Fatalf("publish: %v", err)
	}
	w.wantPost(key, commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.ProposeRefused, Refusal: commandtext.InvalidSource, Owner: cmtOwner, Source: cmtComment}))
}
