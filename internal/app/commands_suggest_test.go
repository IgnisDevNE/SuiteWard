package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// The command /suiteward suggest (docs/contracts/runtime.md, "Scope discovery (M1.9)"): before the first contract it answers like
// a propose that finds no declaration, afterwards with the scope notice of the head. Repository 2 gets a canonical of its own
// where a test needs one, because repository 1 holds the bootstrap Suite of the default world.

func cjSuggest(id int64, created time.Time) cjComment {
	return cjComment{pull: cjPull, id: id, author: cjOwnerID, created: created, updated: created, body: cjMarker + "\n/suiteward suggest"}
}

// seedDeclared gives the repository's project a Suite whose canonical V1 holds the declaration, as the bootstrap leaves it, and the job
// a content store that returns it. The head of the observed pull requests holds the files.
func (w *cjWorld) seedDeclared(repositoryID int64, declaration string, head map[string]string) {
	w.t.Helper()
	project, carrier := owner.ProjectID(repositoryID), carrierID(repositoryID, 3)
	parsed := cjMust(scope.ParseDeclaration([]byte(declaration)))
	protected := cjMust(contract.NewProtectedContract(cjMust(artifact.NewManifest([]artifact.Entry{{Path: scope.DeclarationPath, Content: artifact.Hash([]byte(declaration))}})),
		scope.NewScope(parsed).Digest(), map[string]string{}))
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
	w.job.contents = declarationContents(declaration)
	w.reader.repo(cjRepoName[repositoryID]).trees[cjHead] = &bootTree{files: head}
}

// scopeKeys are the keys of the scope-notice and scope-review messages a unit of work wrote: a command writes none, for its answer is its acknowledgment.
func (w *cjWorld) scopeKeys() []string {
	var keys []string
	for _, m := range w.suites.messages {
		if strings.HasPrefix(m.Key, "scope-") {
			keys = append(keys, m.Key)
		}
	}
	return keys
}

func TestSuggestBeforeTheFirstContractRepliesWithTheDeclarationMissingReply(t *testing.T) {
	runnerFiles := map[string]string{"conftest.py": "x", "package.json": "{}"}
	for _, tc := range []struct {
		name       string
		main, head map[string]string
		extra      map[string]any
		readsMain  bool // the default branch is read too
	}{
		{"from the test-like paths of the pull request head", map[string]string{"spec/a_spec.rb": "x"}, map[string]string{"tests/a_test.go": "x", "src/main.go": "y"},
			map[string]any{"suggestion": []string{"/tests/", "*_test.go"}}, false},
		{"from the default branch when the pull request gives none", map[string]string{"spec/a_spec.rb": "x"}, map[string]string{"src/main.go": "y"},
			map[string]any{"suggestion": []string{"/spec/", "*_spec.rb"}}, true},
		{"empty when no path is test-like", map[string]string{"README.md": "x"}, map[string]string{"src/main.go": "y"}, nil, true},
		{"with the runner files and the mixed files found", nil, runnerFiles,
			map[string]any{"runner": []string{".pytest.ini", ".pytest.toml", "conftest.py", "pytest.ini", "pytest.toml"}, "mixed": []string{"package.json"}}, false},
		// A declaration that exists changes nothing: the owner asked for a suggestion, and gets the one the head's paths give.
		{"although the default branch and the head hold a declaration", bootFiles("v1"), bootFiles("v2"),
			map[string]any{"suggestion": []string{"/tests/", "*_test.go"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCjWorld(t)
			w.trees(tc.main, tc.head)
			w.syncAll([]cjComment{cjSuggest(500, cjAt(10))})
			w.mustRun()

			extra := map[string]any{"reason": "declaration-missing", "branch": cjBranch}
			maps.Copy(extra, tc.extra)
			w.requireAckPayload(cjID1, 500, commands.Suggested, extra)
			if reads := w.reader.calls["head"]; (reads != 0) != tc.readsMain {
				t.Fatalf("%d default branch reads; want them only when the head gives no pattern (%v)", reads, tc.readsMain)
			}
			w.requireNoProposal("P2")
			if w.summaries() != 0 || len(w.owners.consumed) != 0 {
				t.Fatalf("%d summaries and %d consumed steps; a suggestion proposes nothing and uses no code", w.summaries(), len(w.owners.consumed))
			}
			w.requireNothingLeaked()
		})
	}
}

// A suggestion before the first contract says what a propose that finds no declaration says, for the same trees.
func TestSuggestBeforeTheFirstContractIsTheReplyOfAProposeWithoutADeclaration(t *testing.T) {
	main, head := map[string]string{"spec/a_spec.rb": "x"}, map[string]string{"tests/a_test.go": "x", "conftest.py": "x", "package.json": "{}"}
	proposed, suggested := newCjWorld(t), newCjWorld(t)
	for w, comment := range map[*cjWorld]cjComment{proposed: cjPropose(500, cjAt(10)), suggested: cjSuggest(500, cjAt(10))} {
		w.trees(main, head)
		w.syncAll([]cjComment{comment})
		w.mustRun()
	}

	_, want := proposed.requireDisposition(cjID1, 500, commands.ProposeRefused)
	_, got := suggested.requireDisposition(cjID1, 500, commands.Suggested)
	if want == nil || got == nil || want.Key != got.Key || string(want.Payload) != strings.Replace(string(got.Payload), `"suggested"`, `"propose-refused"`, 1) {
		t.Fatalf("suggested reply %+v; want the payload of the propose reply %+v with its disposition", got, want)
	}
}

func TestSuggestAfterTheFirstContractRepliesWithTheScopeNoticeOfTheHead(t *testing.T) {
	head := withFiles(bootFiles("v1"), "pkg/util_test.go", "package.json")
	w := newCjWorld(t)
	w.seedDeclared(cjID2, cjDecl, head)
	w.syncAll(nil, cjSuggest(500, cjAt(10)))
	w.mustRun()

	declaration, err := scope.AddPatterns([]byte(cjDecl), []string{"*_test.go"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w.requireAckPayload(cjID2, 500, commands.Suggested, map[string]any{"reason": "scope-notice", "branch": cjBranch, "paths": []string{"pkg/util_test.go"}, "pathsTotal": 1,
		"declaration": string(declaration), "mixed": []string{"package.json"}})
	if keys := w.scopeKeys(); len(keys) != 0 {
		t.Fatalf("scope messages %v; a command answers in its acknowledgment and writes no notice or review", keys)
	}
	if len(w.owners.consumed) != 0 {
		t.Fatalf("consumed %v; a suggestion uses no code", w.owners.consumed)
	}
	w.requireNothingLeaked()
}

// The answer is the head's whole uncovered set, not only what the pull request adds: the base commit is never read.
func TestSuggestAfterTheFirstContractListsTwentyPathsAndCountsAllOfThem(t *testing.T) {
	var extra []string
	for i := range 25 {
		extra = append(extra, fmt.Sprintf("pkg%02d/x_test.go", i))
	}
	w := newCjWorld(t)
	w.seedDeclared(cjID2, cjDecl, withFiles(bootFiles("v1"), extra...))
	w.syncAll(nil, cjSuggest(500, cjAt(10)))
	w.mustRun()

	declaration, err := scope.AddPatterns([]byte(cjDecl), []string{"*_test.go"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w.requireAckPayload(cjID2, 500, commands.Suggested, map[string]any{"reason": "scope-notice", "branch": cjBranch, "paths": extra[:20], "pathsTotal": 25, "declaration": string(declaration)})
}

func TestSuggestAfterTheFirstContractSaysWhenNothingIsUncovered(t *testing.T) {
	w := newCjWorld(t)
	w.seedDeclared(cjID2, cjDecl, bootFiles("v1"))
	w.syncAll(nil, cjSuggest(500, cjAt(10)))
	w.mustRun()

	w.requireAckPayload(cjID2, 500, commands.Suggested, map[string]any{"reason": "nothing-uncovered"})
}

func TestSuggestIsRefusedForAForkAndAClosedPullRequestBeforeAnyTreeIsRead(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fork   bool
		closed bool
		reason string
	}{
		{"a fork", true, false, "fork"},
		{"a pull request that is not open", false, true, "not-open"},
		{"a fork outranks a closed pull request", true, true, "fork"},
	} {
		for _, canonical := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, canonical %v", tc.name, canonical), func(t *testing.T) {
				w := newCjWorld(t)
				repository := cjID1
				if canonical {
					repository = cjID2
					w.seedDeclared(cjID2, cjDecl, withFiles(bootFiles("v1"), cjUtilTst))
				}
				if tc.fork {
					w.headRepo = "evil/widgets"
				}
				if repository == cjID1 {
					w.syncAll([]cjComment{cjSuggest(500, cjAt(10))})
				} else {
					w.syncAll(nil, cjSuggest(500, cjAt(10)))
				}
				if tc.closed {
					w.closePull(repository, cjSync.Add(time.Minute))
				}
				w.mustRun()

				w.requireAckPayload(repository, 500, commands.SuggestRefused, map[string]any{"reason": tc.reason})
				if w.reader.calls["head"] != 0 || w.reader.calls["tree"] != 0 || len(w.owners.consumed) != 0 {
					t.Fatalf("%d branch reads, %d tree reads and %d consumed steps; a refused suggestion reads and uses nothing", w.reader.calls["head"], w.reader.calls["tree"], len(w.owners.consumed))
				}
			})
		}
	}
}

// The owner is told that the bootstrap is pending, as for a propose, but the entry is a suggestion: a no-suite entry would count as the
// owner's latest command, and a rejected approve could then stop showing as owner-command-rejected once the Suite exists.
func TestSuggestForAProjectWithNoSuiteSaysTheBootstrapIsPendingAndIsNotACommand(t *testing.T) {
	w := newCjWorld(t)
	w.syncAll(nil, cjSuggest(500, cjAt(10)))
	w.mustRun()

	_, ack := w.requireDisposition(cjID2, 500, commands.Suggested)
	if ack == nil {
		t.Fatal("no acknowledgment")
	}
	var payload ackPayload
	if err := json.Unmarshal(ack.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Disposition != string(commands.NoSuite) || payload.Reference != "" {
		t.Fatalf("acknowledgment %+v; want the no-suite one without a reference", payload)
	}
	if len(w.owners.consumed) != 0 {
		t.Fatalf("consumed %v; a suggestion uses no code", w.owners.consumed)
	}
}

func TestSuggestFromAnotherAccountIsIgnoredAndNoCodeIsRead(t *testing.T) {
	w := newCjWorld(t)
	other := cjSuggest(500, cjAt(10))
	other.author = cjOtherID
	w.syncAll([]cjComment{other})
	w.mustRun()

	w.requireNoAck(cjID1, 500, commands.IgnoredAuthor)
	if len(w.owners.consumed) != 0 || w.reader.calls["tree"] != 0 {
		t.Fatalf("%d consumed steps and %d tree reads; another account's command acts on nothing", len(w.owners.consumed), w.reader.calls["tree"])
	}
}

func TestSuggestTakesNoCodeAndExtraTextIsMalformed(t *testing.T) {
	w := newCjWorld(t)
	extra := cjSuggest(500, cjAt(10))
	extra.body = cjMarker + "\n/suiteward suggest " + cjCode(cjAt(10), 0)
	w.syncAll([]cjComment{extra})
	w.mustRun()

	w.requireAckPayload(cjID1, 500, commands.Malformed, map[string]any{"reason": "extra-text"})
	w.requireNothingLeaked(cjCode(cjAt(10), 0))
}

// After a rejected approve, a suggestion leaves the check at owner-command-rejected: the ledger's latest owner command is still the approve.
func TestSuggestIsNotTheOwnersLatestCommand(t *testing.T) {
	w := newCjWorld(t)
	w.trees(bootFiles("v1"), bootFiles("v1"))
	w.syncAll([]cjComment{ownerCommand(500, cjAt(10), "approve", "P1-R2", 9), cjSuggest(501, cjAt(11))})
	w.mustRun()

	w.requireDisposition(cjID1, 500, commands.InvalidCode)
	w.requireDisposition(cjID1, 501, commands.Suggested)
	latest, found, err := w.ledger.LatestOwnerCommand(t.Context(), cjID1, cjPull, cjOwnerID)
	if err != nil || !found || latest.CommentID != 500 || latest.Disposition != commands.InvalidCode {
		t.Fatalf("latest owner command = %+v, found %v, error %v; want the rejected approve, comment 500", latest, found, err)
	}
}

// A source the suggestion cannot be built from is a fact about the commit: the command is recorded at once as suggested without an answer, never retried.
func TestSuggestRecordsASourceItCannotReadAsSuggestedWithoutAnAnswer(t *testing.T) {
	truncated := fmt.Errorf("%w: %w", inventory.ErrInvalidTree, github.ErrTreeTruncated)
	tooLong := "version: 1\ninclude:\n"
	for i := range 400 {
		tooLong += fmt.Sprintf("  - \"tests/directory%03d/**\"\n", i)
	}
	for _, tc := range []struct {
		name  string
		setup func(w *cjWorld) int64
	}{
		{"a truncated head tree before the first contract", func(w *cjWorld) int64 {
			w.reader.repo(cjRepo1).treeErr[cjHead] = truncated
			return cjID1
		}},
		{"a truncated head tree after the first contract", func(w *cjWorld) int64 {
			w.seedDeclared(cjID2, cjDecl, bootFiles("v1"))
			w.reader.repo(cjRepo2).treeErr[cjHead] = truncated
			return cjID2
		}},
		{"a canonical that holds no declaration", func(w *cjWorld) int64 {
			w.seedCanonical(cjID2)
			w.reader.repo(cjRepo2).trees[cjHead] = &bootTree{files: withFiles(bootFiles("v1"), cjUtilTst)}
			return cjID2
		}},
		{"a declaration over the limit of a notice", func(w *cjWorld) int64 {
			w.seedDeclared(cjID2, tooLong, withFiles(bootFiles("v1"), cjUtilTst))
			return cjID2
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCjWorld(t)
			repository := tc.setup(w)
			if repository == cjID1 {
				w.syncAll([]cjComment{cjSuggest(500, cjAt(10))})
			} else {
				w.syncAll(nil, cjSuggest(500, cjAt(10)))
			}
			w.mustRun()

			w.requireSilent(repository, 500, commands.Suggested)
			if !strings.Contains(w.logs.String(), "github suggest skipped") {
				t.Fatalf("no log line says the suggestion was skipped\nlogs: %s", w.logs)
			}
			w.requireNothingLeaked()
		})
	}
}

func TestSuggestRetriesAGitHubFailureAndIsFailedAfterFiveAttemptsLoggingOnlyKindAndStatus(t *testing.T) {
	for _, canonical := range []bool{false, true} {
		t.Run(fmt.Sprintf("canonical %v", canonical), func(t *testing.T) {
			w := newCjWorld(t)
			repository, name := cjID1, cjRepo1
			if canonical {
				repository, name = cjID2, cjRepo2
				w.seedDeclared(cjID2, cjDecl, withFiles(bootFiles("v1"), cjUtilTst))
			}
			w.reader.repo(name).treeErr[cjHead] = errors.New(bootSecret)
			if canonical {
				w.syncAll(nil, cjSuggest(500, cjAt(10)))
			} else {
				w.syncAll([]cjComment{cjSuggest(500, cjAt(10))})
			}
			for attempt := 1; attempt <= 4; attempt++ {
				if err := w.run(); err != nil {
					t.Fatalf("attempt %d: %v; a GitHub failure stops the run, it does not fail the job", attempt, err)
				}
				w.requireNoEntry(repository, 500)
			}
			w.mustRun()

			w.requireSilent(repository, 500, commands.Failed) // only a propose gets its failure acknowledged
			if !strings.Contains(w.logs.String(), `"kind"`) || !strings.Contains(w.logs.String(), `"status"`) {
				t.Fatalf("the failure log lacks the kind and status of the read\nlogs: %s", w.logs)
			}
			w.requireNothingLeaked(bootSecret)
		})
	}
}
