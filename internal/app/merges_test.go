package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The merge commits of the pull requests of the tests.
var (
	mgSHA5     = strings.Repeat("5a", 20)
	mgSHA6     = strings.Repeat("6b", 20)
	mgSHA7     = strings.Repeat("7c", 20)
	mgSHAOther = strings.Repeat("8d", 20)
)

type mgKey struct {
	repo   string
	number int
}

// mgReader answers the three reads of the merges job from memory and remembers them in order.
type mgReader struct {
	pulls     map[mgKey]github.PullDetail
	pullErr   map[mgKey]error
	closed    map[string][]github.PullDetail
	closedErr error
	trees     map[string]inventory.Tree // by merge commit
	treeErr   map[string]error
	treeReads []string
	pullReads []mgKey
}

var _ mergeReader = (*mgReader)(nil)

func (r *mgReader) Pull(_ context.Context, repo string, number int) (github.PullDetail, error) {
	key := mgKey{repo, number}
	r.pullReads = append(r.pullReads, key)
	if err := r.pullErr[key]; err != nil {
		return github.PullDetail{}, err
	}
	detail, found := r.pulls[key]
	if !found {
		return github.PullDetail{}, &rest.StatusError{Status: 404, Body: bootSecret}
	}
	return detail, nil
}

func (r *mgReader) ClosedPulls(_ context.Context, repo string) ([]github.PullDetail, error) {
	return r.closed[repo], r.closedErr
}

func (r *mgReader) Tree(_ context.Context, _, sha string) (inventory.Tree, error) {
	r.treeReads = append(r.treeReads, sha)
	if err := r.treeErr[sha]; err != nil {
		return nil, err
	}
	tree, found := r.trees[sha]
	if !found {
		return nil, &rest.StatusError{Status: 404, Body: bootSecret}
	}
	return tree, nil
}

// mgWorld is a bootstrapped repository (canonical V1) with the job's fakes and a GitHub that answers what the test sets.
type mgWorld struct {
	*cjJobWorld
	gh      *mgReader
	merges  *mergesJob
	mgProbs *bootstrapProblems
}

func newMgWorld(t *testing.T, options ...func(*bootOptions)) *mgWorld {
	t.Helper()
	w := &mgWorld{cjJobWorld: newCJWorld(t, true, options...), mgProbs: &bootstrapProblems{},
		gh: &mgReader{pulls: map[mgKey]github.PullDetail{}, pullErr: map[mgKey]error{}, closed: map[string][]github.PullDetail{}, trees: map[string]inventory.Tree{}, treeErr: map[string]error{}}}
	w.merges = &mergesJob{repos: w.job.repos, reader: w.gh, owners: w.owners, suites: w.suites, content: w.content, store: w.pulls, integrity: w.verdicts,
		tier: owner.TierIsolated, problems: w.mgProbs, logger: slog.New(slog.NewJSONHandler(w.logs, nil)), now: func() time.Time { return w.clock }}
	return w
}

func (w *mgWorld) advance(d time.Duration) { w.clock = w.clock.Add(d) }

func (w *mgWorld) runMerges() error { return w.merges.run(w.t.Context()) }

func (w *mgWorld) mustMerge() {
	w.t.Helper()
	if err := w.runMerges(); err != nil {
		w.t.Fatalf("merges run: %v\nlogs: %s", err, w.logs)
	}
}

// cycleAt records a cycle of repo at the given time in which exactly the pull requests of heads are open.
func (w *mgWorld) cycleAt(at time.Time, repo string, heads map[int]string) {
	w.t.Helper()
	cycle := githubsync.Cycle{Repo: repo, InstallationID: 1, At: at, Comments: map[int][]githubsync.ObservedComment{}}
	for number, head := range heads {
		cycle.OpenPulls = append(cycle.OpenPulls, githubsync.ObservedPull{Number: number, HeadSHA: head, HeadRef: "feature", BaseRef: "main", BaseSHA: bootHead, UpdatedAt: bootTime})
		cycle.Comments[number] = nil
	}
	if err := w.pulls.RecordCycle(w.t.Context(), cycle); err != nil {
		w.t.Fatal(err)
	}
}

// open opens pull request number at head, whose protected tests hold content, and checks it, which proposes its change.
func (w *mgWorld) open(number int, head, content string) {
	w.t.Helper()
	w.tree(head, bootFiles(content))
	heads := maps.Clone(w.heads)
	heads[number] = head
	w.sync(heads)
	w.mustCheck()
}

// leave removes pull requests from the open listing at the clock: SuiteWard sees them absent since then.
func (w *mgWorld) leave(numbers ...int) {
	w.t.Helper()
	heads := maps.Clone(w.heads)
	for _, number := range numbers {
		delete(heads, number)
	}
	w.cycleAt(w.clock, bootRepo, heads)
	w.heads = heads
}

func mgDetail(number int, head string) github.PullDetail {
	return github.PullDetail{Pull: github.Pull{Number: number, HeadSHA: head, HeadRef: "feature", HeadRepo: bootRepo, BaseRef: "main", BaseSHA: bootHead, UpdatedAt: bootTime.Add(2 * time.Hour)}, State: "closed"}
}

// merged tells GitHub that pull request number was merged as sha, whose tree holds content, mergedAt after the test's start.
func (w *mgWorld) merged(number int, head, sha, content string, mergedAt time.Duration) {
	w.t.Helper()
	d := mgDetail(number, head)
	d.Merged, d.MergedAt, d.MergeCommitSHA = true, bootTime.Add(2*time.Hour+mergedAt), sha
	w.gh.pulls[mgKey{bootRepo, number}] = d
	if sha != "" {
		w.gh.trees[sha] = &bootTree{files: bootFiles(content)}
	}
}

func (w *mgWorld) outcome(number int) (governance.MergeOutcome, bool) {
	w.t.Helper()
	outcome, found, err := governance.MergeOutcomeOf(w.t.Context(), w.mem, w.project(), "default", carrierID(bootRepoID, number))
	if err != nil {
		w.t.Fatal(err)
	}
	return outcome, found
}

func (w *mgWorld) wantOutcome(number int, kind governance.MergeKind, reason governance.MergeReason) governance.MergeOutcome {
	w.t.Helper()
	outcome, found := w.outcome(number)
	if !found {
		w.t.Fatalf("pull %d has no merge outcome\nlogs: %s", number, w.logs)
	}
	if outcome.Kind != kind || outcome.Reason != reason {
		w.t.Fatalf("pull %d: outcome %s/%s, want %s/%s\nlogs: %s", number, outcome.Kind, outcome.Reason, kind, reason, w.logs)
	}
	return outcome
}

func (w *mgWorld) wantNoOutcome(number int) {
	w.t.Helper()
	if outcome, found := w.outcome(number); found {
		w.t.Fatalf("pull %d has the merge outcome %+v, want none", number, outcome)
	}
}

// processed reports whether the merged pull request is marked processed.
func (w *mgWorld) processed(number int) bool {
	w.t.Helper()
	merged, err := w.pulls.Merged(w.t.Context(), bootRepo)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, m := range merged {
		if m.PullState.Number == number {
			return m.Processed
		}
	}
	return false
}

// resolved lists the merged pull requests the store holds.
func (w *mgWorld) resolved() []int {
	w.t.Helper()
	merged, err := w.pulls.Merged(w.t.Context(), bootRepo)
	if err != nil {
		w.t.Fatal(err)
	}
	numbers := []int{}
	for _, m := range merged {
		numbers = append(numbers, m.PullState.Number)
	}
	return numbers
}

func (w *mgWorld) awaiting() []int {
	w.t.Helper()
	pulls, err := w.pulls.AwaitingResolution(w.t.Context(), bootRepo)
	if err != nil {
		w.t.Fatal(err)
	}
	numbers := []int{}
	for _, p := range pulls {
		numbers = append(numbers, p.Number)
	}
	return numbers
}

// mergeKeys lists the outcome messages written so far.
func (w *mgWorld) mergeKeys() []string {
	keys := []string{}
	for _, m := range w.suites.messages {
		if strings.HasPrefix(m.Key, "merge:") {
			keys = append(keys, m.Key)
		}
	}
	return keys
}

func (w *mgWorld) wantProblem(code string) {
	w.t.Helper()
	if got := w.mgProbs.lookup(bootRepo); got != code {
		w.t.Fatalf("merges problem %q, want %q\nlogs: %s", got, code, w.logs)
	}
}

// approved opens pull request number (proposal P<proposal>) with a protected change and the owner's approval of it.
func (w *mgWorld) approved(number int, head, content string, proposal int, comment int64) {
	w.t.Helper()
	w.open(number, head, content)
	if outcome := w.consent(contract.ApproveConsent, comment, proposal, 1, number); outcome != contract.ConsentApproved {
		w.t.Fatalf("approval outcome %v", outcome)
	}
}

func TestMergesPromoteAnApprovedMergeAndSayItInOneMessage(t *testing.T) {
	w := newMgWorld(t)
	w.approved(5, cjHead2, "v2", 2, 201)
	w.leave(5)
	w.merged(5, cjHead2, mgSHA5, "v2", time.Minute)
	w.advance(time.Minute)

	w.mustMerge()

	outcome := w.wantOutcome(5, governance.MergePromoted, "")
	if outcome.Version != "V2" || outcome.Source != contract.SourceRevision(mgSHA5) || outcome.Reference == nil || outcome.Reference.ProposalID != "P2" {
		t.Fatalf("outcome %+v, want V2 from the merge commit under P2", outcome)
	}
	if version, _ := w.canonical(); version != "V2" {
		t.Fatalf("canonical %s, want V2", version)
	}
	if !w.processed(5) {
		t.Fatal("the merge is not marked processed")
	}
	if got := w.mergeKeys(); !slices.Equal(got, []string{"merge:1001:5"}) {
		t.Fatalf("merge messages %v, want one for pull 5", got)
	}
	payload := w.payload("merge:1001:5")
	for key, want := range map[string]any{"repo": bootRepo, "kind": "promoted", "version": "V2", "source": mgSHA5, "reference": "P2-R1", "tier": "isolated"} {
		if payload[key] != want {
			t.Errorf("payload %s = %v, want %v (%v)", key, payload[key], want, payload)
		}
	}
	if payload["pull"] != float64(5) || payload["repositoryId"] != float64(bootRepoID) {
		t.Errorf("payload %v does not name pull 5 of repository %d", payload, bootRepoID)
	}
	if _, has := payload["reason"]; has {
		t.Errorf("a promotion payload has a reason: %v", payload)
	}

	reads := len(w.gh.treeReads)
	w.mustMerge() // a replay decides nothing again
	if len(w.gh.treeReads) != reads || len(w.mergeKeys()) != 1 {
		t.Fatalf("the replay read %d more trees and wrote %d messages", len(w.gh.treeReads)-reads, len(w.mergeKeys()))
	}
	w.wantProblem("")
}

func TestMergesWriteNoMessageForAMergeThatChangedNothingProtected(t *testing.T) {
	t.Run("an implementation-only merge", func(t *testing.T) {
		w := newMgWorld(t)
		w.sync(map[int]string{4: bootHead, 5: cjHead3})
		w.leave(5)
		w.merged(5, cjHead3, mgSHA5, "v1", time.Minute) // the protected files stay those of V1
		w.advance(time.Minute)

		w.mustMerge()

		w.wantOutcome(5, governance.MergeNoChange, "")
		if keys := w.mergeKeys(); len(keys) != 0 {
			t.Fatalf("merge messages %v, want none", keys)
		}
		if !w.processed(5) {
			t.Fatal("the merge is not marked processed")
		}
	})
	t.Run("a merge to another branch", func(t *testing.T) {
		w := newMgWorld(t)
		w.sync(map[int]string{4: bootHead, 5: cjHead3})
		w.leave(5)
		w.merged(5, cjHead3, mgSHA5, "v2", time.Minute)
		detail := w.gh.pulls[mgKey{bootRepo, 5}]
		detail.BaseRef = "develop"
		w.gh.pulls[mgKey{bootRepo, 5}] = detail
		w.advance(time.Minute)

		w.mustMerge()

		w.wantOutcome(5, governance.MergeNotTarget, "")
		if keys := w.mergeKeys(); len(keys) != 0 {
			t.Fatalf("merge messages %v, want none", keys)
		}
	})
	t.Run("a merge before the canonical", func(t *testing.T) {
		w := newMgWorld(t)
		w.cycleAt(bootTime.Add(30*time.Minute), bootRepo, map[int]string{4: bootHead, 7: cjHead3})
		w.cycleAt(bootTime.Add(40*time.Minute), bootRepo, map[int]string{4: bootHead}) // absent before V1 was promoted
		w.merged(7, cjHead3, mgSHA7, "v2", time.Minute)

		w.mustMerge()

		w.wantOutcome(7, governance.MergeBeforeCanonical, "")
		if keys := w.mergeKeys(); len(keys) != 0 {
			t.Fatalf("merge messages %v, want none", keys)
		}
	})
}

func TestMergesResolveEveryPullThatLeftTheOpenList(t *testing.T) {
	t.Run("a pull GitHub still reports open stays unresolved", func(t *testing.T) {
		w := newMgWorld(t)
		w.sync(map[int]string{4: bootHead, 5: cjHead3})
		w.leave(5)
		open := mgDetail(5, cjHead3)
		open.State = "open"
		w.gh.pulls[mgKey{bootRepo, 5}] = open

		w.mustMerge()

		if got := w.awaiting(); !slices.Equal(got, []int{5}) || len(w.resolved()) != 0 {
			t.Fatalf("awaiting %v, merged %v; want pull 5 still awaiting and nothing merged", got, w.resolved())
		}
		w.wantProblem("")
	})
	t.Run("a closed and unmerged pull is resolved with no outcome", func(t *testing.T) {
		w := newMgWorld(t)
		w.sync(map[int]string{4: bootHead, 5: cjHead3})
		w.leave(5)
		w.gh.pulls[mgKey{bootRepo, 5}] = mgDetail(5, cjHead3)

		w.mustMerge()

		if got := w.awaiting(); len(got) != 0 || len(w.resolved()) != 0 {
			t.Fatalf("awaiting %v, merged %v; want pull 5 resolved and not merged", got, w.resolved())
		}
		w.wantNoOutcome(5)
		if len(w.gh.treeReads) != 0 || len(w.mergeKeys()) != 0 {
			t.Fatal("an unmerged pull cost a tree read or a message")
		}
	})
	t.Run("a merged pull with a merge commit is resolved and decided", func(t *testing.T) {
		w := newMgWorld(t)
		w.sync(map[int]string{4: bootHead, 5: cjHead3})
		w.leave(5)
		w.merged(5, cjHead3, mgSHA5, "v1", time.Minute)

		w.mustMerge()

		w.wantOutcome(5, governance.MergeNoChange, "")
		if got := w.awaiting(); len(got) != 0 {
			t.Fatalf("awaiting %v, want none", got)
		}
	})
	t.Run("a merged pull without a merge commit waits for the bound", func(t *testing.T) {
		w := newMgWorld(t)
		w.sync(map[int]string{4: bootHead, 5: cjHead3})
		w.leave(5)
		w.merged(5, cjHead3, "", "", time.Minute)

		w.advance(29*time.Minute + 59*time.Second)
		w.mustMerge()

		if got := w.awaiting(); !slices.Equal(got, []int{5}) {
			t.Fatalf("awaiting %v, want pull 5 still waiting", got)
		}
		w.wantNoOutcome(5)
		w.wantProblem("waiting")

		w.advance(time.Second) // 30 minutes after the absence
		w.mustMerge()

		w.wantOutcome(5, governance.MergeBlocked, governance.MergeReasonIntegrationUnreadable)
		payload := w.payload("merge:1001:5")
		if payload["kind"] != "blocked" || payload["reason"] != "integration-unreadable" || payload["source"] != "" || payload["version"] != "V1" {
			t.Fatalf("payload %v, want blocked integration-unreadable without a commit, V1 in force", payload)
		}
		if len(w.gh.treeReads) != 0 {
			t.Fatalf("a merge without a commit read the trees %v", w.gh.treeReads)
		}
		w.wantProblem("")
	})
	t.Run("a failed read leaves the pull unresolved", func(t *testing.T) {
		w := newMgWorld(t)
		w.sync(map[int]string{4: bootHead, 5: cjHead3})
		w.leave(5)
		w.gh.pullErr[mgKey{bootRepo, 5}] = &rest.StatusError{Status: 502, Body: bootSecret}

		w.mustMerge()

		if got := w.awaiting(); !slices.Equal(got, []int{5}) {
			t.Fatalf("awaiting %v, want pull 5", got)
		}
		w.wantProblem("read-failed")
		delete(w.gh.pullErr, mgKey{bootRepo, 5})
		w.merged(5, cjHead3, mgSHA5, "v1", time.Minute)
		w.mustMerge()
		w.wantOutcome(5, governance.MergeNoChange, "")
		w.wantProblem("")
	})
	t.Run("a pull that reopened is skipped", func(t *testing.T) {
		w := newMgWorld(t)
		w.sync(map[int]string{4: bootHead, 5: cjHead3})
		w.leave(5)
		w.gh.pulls[mgKey{bootRepo, 5}] = mgDetail(5, cjHead3)
		reopening := &reopeningStore{mergeStore: w.pulls, onResolve: func() { w.cycleAt(w.clock.Add(time.Second), bootRepo, map[int]string{4: bootHead, 5: cjHead3}) }}
		w.merges.store = reopening

		w.mustMerge()

		if got := w.awaiting(); len(got) != 0 {
			t.Fatalf("awaiting %v, want none: the pull is open again", got)
		}
		w.wantProblem("")
	})
}

// reopeningStore reopens the pull request in the middle of its resolution.
type reopeningStore struct {
	mergeStore
	onResolve func()
}

func (s *reopeningStore) RecordResolution(ctx context.Context, repo string, r githubsync.Resolution) error {
	s.onResolve()
	return s.mergeStore.RecordResolution(ctx, repo, r)
}

func TestMergesDecideAMergeTheOpenListNeverShowed(t *testing.T) {
	w := newMgWorld(t)
	w.mustMerge() // the first listing only sets the watermark
	w.advance(time.Minute)
	listed := mgDetail(9, cjHead3)
	listed.Merged, listed.MergedAt = true, bootTime.Add(2*time.Hour+30*time.Second)
	listed.MergeCommitSHA = mgSHA7
	listed.UpdatedAt = bootTime.Add(2*time.Hour + time.Minute)
	w.gh.closed[bootRepo] = []github.PullDetail{listed}
	w.gh.pulls[mgKey{bootRepo, 9}] = listed
	w.gh.trees[mgSHA7] = &bootTree{files: bootFiles("v9")}

	w.mustMerge()

	w.wantOutcome(9, governance.MergeBlocked, governance.MergeReasonUnapprovedChange)
	if payload := w.payload("merge:1001:9"); payload["reason"] != "unapproved-change" || payload["kind"] != "blocked" {
		t.Fatalf("payload %v, want blocked unapproved-change", payload)
	}
	if !w.processed(9) {
		t.Fatal("the merge is not marked processed")
	}
}

func TestMergesDecideMergesInMergeOrder(t *testing.T) {
	w := newMgWorld(t)
	w.approved(5, cjHead2, "v2", 2, 201)
	w.approved(6, cjHead3, "v3", 3, 202)
	w.leave(5, 6)
	w.merged(6, cjHead3, mgSHA6, "v3", time.Minute) // pull 6 merged first, although pull 5 is the lower number
	w.merged(5, cjHead2, mgSHA5, "v2", 2*time.Minute)
	w.advance(time.Minute)

	w.mustMerge()

	if !slices.Equal(w.gh.treeReads, []string{mgSHA6, mgSHA5}) {
		t.Fatalf("trees read %v, want the earlier merge first", w.gh.treeReads)
	}
	w.wantOutcome(6, governance.MergePromoted, "")
	w.wantOutcome(5, governance.MergeBlocked, governance.MergeReasonCanonicalChanged) // approved against V1, which pull 6 replaced
	if version, _ := w.canonical(); version != "V2" {
		t.Fatalf("canonical %s, want V2", version)
	}
}

func TestMergesStopAtTheFirstMergeThatCannotBeDecided(t *testing.T) {
	t.Run("an earlier merge still waiting for its commit", func(t *testing.T) {
		w := newMgWorld(t)
		w.approved(5, cjHead2, "v2", 2, 201)
		w.approved(6, cjHead3, "v3", 3, 202)
		w.leave(5, 6)
		w.merged(5, cjHead2, "", "", time.Minute) // GitHub has not sent the merge commit yet
		w.merged(6, cjHead3, mgSHA6, "v3", 2*time.Minute)
		w.advance(time.Minute)

		w.mustMerge()

		w.wantNoOutcome(5)
		w.wantNoOutcome(6)
		if len(w.gh.treeReads) != 0 {
			t.Fatalf("trees read %v, want none: the later merge must wait", w.gh.treeReads)
		}
		if got := w.resolved(); !slices.Equal(got, []int{6}) {
			t.Fatalf("merged %v, want only pull 6 resolved", got)
		}
		w.wantProblem("waiting")

		w.advance(30 * time.Minute)
		w.mustMerge()

		w.wantOutcome(5, governance.MergeBlocked, governance.MergeReasonIntegrationUnreadable)
		w.wantOutcome(6, governance.MergePromoted, "") // decided after pull 5, which left V1 in force
	})
	t.Run("an earlier merge whose tree cannot be read", func(t *testing.T) {
		w := newMgWorld(t)
		w.seedOther()
		w.approved(5, cjHead2, "v2", 2, 201)
		w.approved(6, cjHead3, "v3", 3, 202)
		w.leave(5, 6)
		w.merged(5, cjHead2, mgSHA5, "v2", time.Minute)
		w.merged(6, cjHead3, mgSHA6, "v3", 2*time.Minute)
		w.gh.treeErr[mgSHA5] = &rest.StatusError{Status: 502, Body: bootSecret}
		w.otherMerged()
		w.advance(time.Minute)

		w.mustMerge()

		w.wantNoOutcome(5)
		w.wantNoOutcome(6)
		if !slices.Equal(w.gh.treeReads, []string{mgSHA5, mgSHAOther}) {
			t.Fatalf("trees read %v, want the failing one and the other repository's, never pull 6's", w.gh.treeReads)
		}
		if outcome, found := w.otherOutcome(); !found || outcome.Reason != governance.MergeReasonUnapprovedChange {
			t.Fatalf("the other repository's outcome %+v (found %v), want a decided merge: one repository's stop must not stop the other", outcome, found)
		}
		w.wantProblem("read-failed")
		if w.mgProbs.lookup(bootOtherRepo) != "" {
			t.Fatalf("the other repository has the problem %q", w.mgProbs.lookup(bootOtherRepo))
		}

		delete(w.gh.treeErr, mgSHA5)
		w.mustMerge()

		w.wantOutcome(5, governance.MergePromoted, "")
		w.wantOutcome(6, governance.MergeBlocked, governance.MergeReasonCanonicalChanged)
		w.wantProblem("")
	})
	t.Run("a failed closed listing", func(t *testing.T) {
		w := newMgWorld(t)
		w.sync(map[int]string{4: bootHead, 5: cjHead3})
		w.leave(5)
		w.merged(5, cjHead3, mgSHA5, "v1", time.Minute)
		w.gh.closedErr = &rest.StatusError{Status: 502, Body: bootSecret}

		w.mustMerge()

		w.wantNoOutcome(5)
		w.wantProblem("read-failed")
		w.gh.closedErr = nil
		w.mustMerge()
		w.wantOutcome(5, governance.MergeNoChange, "")
		w.wantProblem("")
	})
}

func TestMergesMarkAnOutcomeThatWasAlreadyRecordedWithoutReadingAnyTree(t *testing.T) {
	w := newMgWorld(t)
	w.sync(map[int]string{4: bootHead, 5: cjHead3})
	w.leave(5)
	w.merged(5, cjHead3, mgSHA5, "v1", time.Minute)
	w.merges.store = &markFailing{mergeStore: w.pulls, err: errBootStore}

	err := w.runMerges() // the outcome is recorded, then the mark fails

	if !errors.Is(err, errBootStore) {
		t.Fatalf("run error %v, want the store error", err)
	}
	w.wantOutcome(5, governance.MergeNoChange, "")
	if w.processed(5) {
		t.Fatal("the mark failed, yet the merge is processed")
	}
	reads := len(w.gh.treeReads)
	w.merges.store = w.pulls

	w.mustMerge()

	if len(w.gh.treeReads) != reads {
		t.Fatalf("trees read %v, want no new read for an outcome that exists", w.gh.treeReads)
	}
	if !w.processed(5) {
		t.Fatal("the recorded outcome was not marked processed")
	}
}

// markFailing fails MarkProcessed.
type markFailing struct {
	mergeStore
	err error
}

func (s *markFailing) MarkProcessed(context.Context, string, int) error { return s.err }

func TestMergesRefuseWhatSuiteWardCannotTrust(t *testing.T) {
	t.Run("a pull request from a fork", func(t *testing.T) {
		for _, headRepo := range []string{"mallory/widgets", "(deleted)"} {
			w := newMgWorld(t)
			w.approved(5, cjHead2, "v2", 2, 201)
			w.leave(5)
			w.merged(5, cjHead2, mgSHA5, "v2", time.Minute)
			detail := w.gh.pulls[mgKey{bootRepo, 5}]
			detail.HeadRepo = headRepo
			w.gh.pulls[mgKey{bootRepo, 5}] = detail

			w.mustMerge()

			w.wantOutcome(5, governance.MergeBlocked, governance.MergeReasonForkUnsupported)
			if len(w.gh.treeReads) != 0 {
				t.Fatalf("%s: a fork merge read the trees %v", headRepo, w.gh.treeReads)
			}
			if version, _ := w.canonical(); version != "V1" {
				t.Fatalf("%s: canonical %s, want V1", headRepo, version)
			}
			if payload := w.payload("merge:1001:5"); payload["reason"] != "fork-unsupported" {
				t.Fatalf("%s: payload %v", headRepo, payload)
			}
		}
	})
	t.Run("a head repository that differs only in case is not a fork", func(t *testing.T) {
		w := newMgWorld(t)
		w.approved(5, cjHead2, "v2", 2, 201)
		w.leave(5)
		w.merged(5, cjHead2, mgSHA5, "v2", time.Minute)
		detail := w.gh.pulls[mgKey{bootRepo, 5}]
		detail.HeadRepo = "ACME/Widgets"
		w.gh.pulls[mgKey{bootRepo, 5}] = detail

		w.mustMerge()

		w.wantOutcome(5, governance.MergePromoted, "")
	})
	t.Run("two pull requests that share a merge commit", func(t *testing.T) {
		w := newMgWorld(t)
		w.approved(5, cjHead2, "v2", 2, 201)
		w.approved(6, cjHead3, "v3", 3, 202)
		w.leave(5, 6)
		w.merged(5, cjHead2, mgSHA5, "v2", time.Minute)
		w.merged(6, cjHead3, mgSHA5, "v2", 2*time.Minute)

		w.mustMerge()

		w.wantOutcome(5, governance.MergeBlocked, governance.MergeReasonSharedMergeCommit)
		w.wantOutcome(6, governance.MergeBlocked, governance.MergeReasonSharedMergeCommit)
		if version, _ := w.canonical(); version != "V1" || len(w.gh.treeReads) != 0 {
			t.Fatalf("canonical %s and trees read %v, want V1 and no read", version, w.gh.treeReads)
		}
	})
	t.Run("an outcome already recorded for the other pull request stands", func(t *testing.T) {
		w := newMgWorld(t)
		w.approved(5, cjHead2, "v2", 2, 201)
		w.approved(6, cjHead3, "v3", 3, 202)
		w.leave(5)
		w.merged(5, cjHead2, mgSHA5, "v2", time.Minute)
		w.mustMerge()
		w.wantOutcome(5, governance.MergePromoted, "")

		w.leave(6)
		w.merged(6, cjHead3, mgSHA5, "v2", 2*time.Minute) // the same merge commit, later
		w.mustMerge()

		w.wantOutcome(5, governance.MergePromoted, "")
		w.wantOutcome(6, governance.MergeBlocked, governance.MergeReasonSharedMergeCommit)
	})
	t.Run("a canonical that failed its integrity verification", func(t *testing.T) {
		w := newMgWorld(t)
		w.approved(5, cjHead2, "v2", 2, 201)
		w.leave(5)
		w.merged(5, cjHead2, mgSHA5, "v2", time.Minute)
		if err := w.verdicts.Record(t.Context(), integrity.Record{Project: w.project(), Suite: "default", Version: "V1", Verdict: integrity.Failed, CheckedAt: w.clock}); err != nil {
			t.Fatal(err)
		}

		w.mustMerge()

		w.wantOutcome(5, governance.MergeBlocked, governance.MergeReasonIntegrityFailed)
		if len(w.gh.treeReads) != 0 {
			t.Fatalf("trees read %v, want none", w.gh.treeReads)
		}
	})
	t.Run("a failed verdict of another version, or a passed one, refuses nothing", func(t *testing.T) {
		w := newMgWorld(t)
		w.approved(5, cjHead2, "v2", 2, 201)
		w.leave(5)
		w.merged(5, cjHead2, mgSHA5, "v2", time.Minute)
		if err := w.verdicts.Record(t.Context(), integrity.Record{Project: w.project(), Suite: "default", Version: "V0", Verdict: integrity.Failed, CheckedAt: w.clock}); err != nil {
			t.Fatal(err)
		}

		w.mustMerge()

		w.wantOutcome(5, governance.MergePromoted, "")
	})
	t.Run("a refusal needs no commit", func(t *testing.T) {
		w := newMgWorld(t)
		w.approved(5, cjHead2, "v2", 2, 201)
		w.leave(5)
		w.merged(5, cjHead2, "", "", time.Minute)
		detail := w.gh.pulls[mgKey{bootRepo, 5}]
		detail.HeadRepo = "mallory/widgets"
		w.gh.pulls[mgKey{bootRepo, 5}] = detail
		w.advance(31 * time.Minute)

		w.mustMerge() // the contract's request validation would refuse a fork refusal without a commit

		w.wantOutcome(5, governance.MergeBlocked, governance.MergeReasonIntegrationUnreadable)
		w.wantProblem("")
	})
}

func TestMergesReadAnIntegratedTreeThatMayBeLate(t *testing.T) {
	late := map[string]error{
		"truncated":     fmt.Errorf("%w: %w: %s", inventory.ErrInvalidTree, github.ErrTreeTruncated, bootSecret),
		"not found":     &rest.StatusError{Status: 404, Body: bootSecret},
		"unprocessable": &rest.StatusError{Status: 422, Body: bootSecret},
	}
	for name, failure := range late {
		t.Run(name, func(t *testing.T) {
			w := newMgWorld(t)
			w.approved(5, cjHead2, "v2", 2, 201)
			w.leave(5)
			w.merged(5, cjHead2, mgSHA5, "v2", time.Minute)
			w.gh.treeErr[mgSHA5] = failure

			w.advance(29 * time.Minute)
			w.mustMerge()

			w.wantNoOutcome(5)
			w.wantProblem("waiting")
			if w.processed(5) {
				t.Fatal("an undecided merge is marked processed")
			}

			w.advance(time.Minute)
			w.mustMerge()

			outcome := w.wantOutcome(5, governance.MergeBlocked, governance.MergeReasonIntegrationUnreadable)
			if outcome.Source != contract.SourceRevision(mgSHA5) {
				t.Fatalf("source %q, want the merge commit kept", outcome.Source)
			}
			w.wantProblem("")
			if payload := w.payload("merge:1001:5"); payload["reason"] != "integration-unreadable" || payload["source"] != mgSHA5 {
				t.Fatalf("payload %v", payload)
			}
		})
	}
}

func TestMergesNeverRecordATruncatedTreeAsAnInvalidSource(t *testing.T) {
	w := newMgWorld(t)
	w.approved(5, cjHead2, "v2", 2, 201)
	w.leave(5)
	w.merged(5, cjHead2, mgSHA5, "v2", time.Minute)
	w.gh.treeErr[mgSHA5] = fmt.Errorf("%w: %w", inventory.ErrInvalidTree, github.ErrTreeTruncated)
	for _, wait := range []time.Duration{time.Minute, 20 * time.Minute, 30 * time.Minute} {
		w.advance(wait)
		w.mustMerge()
		if outcome, found := w.outcome(5); found && outcome.Reason == governance.MergeReasonInvalidSource {
			t.Fatalf("after %v a truncated tree was recorded as %+v", wait, outcome)
		}
	}
	w.wantOutcome(5, governance.MergeBlocked, governance.MergeReasonIntegrationUnreadable)
}

func TestMergesRecordATreeThatIsInvalidAsAnInvalidSource(t *testing.T) {
	w := newMgWorld(t)
	w.approved(5, cjHead2, "v2", 2, 201)
	w.leave(5)
	w.merged(5, cjHead2, mgSHA5, "v2", time.Minute)
	w.gh.treeErr[mgSHA5] = fmt.Errorf("%w: %q has mode %q", inventory.ErrInvalidTree, "tests/x", "999999")

	w.mustMerge()

	w.wantOutcome(5, governance.MergeBlocked, governance.MergeReasonInvalidSource)
	if payload := w.payload("merge:1001:5"); payload["reason"] != "invalid-source" {
		t.Fatalf("payload %v", payload)
	}
}

func TestMergesStopOnATransientReadWhateverTheAge(t *testing.T) {
	transient := map[string]error{
		"a server error":   &rest.StatusError{Status: 503, Body: bootSecret},
		"a rate limit":     &rest.RateLimitedError{Status: 403, Until: bootTime.Add(3 * time.Hour)},
		"a transport fail": errors.New(bootSecret),
	}
	for name, failure := range transient {
		t.Run(name, func(t *testing.T) {
			w := newMgWorld(t)
			w.approved(5, cjHead2, "v2", 2, 201)
			w.leave(5)
			w.merged(5, cjHead2, mgSHA5, "v2", time.Minute)
			w.gh.treeErr[mgSHA5] = failure
			w.advance(2 * time.Hour)

			w.mustMerge()

			w.wantNoOutcome(5)
			w.wantProblem("read-failed")
		})
	}
}

func TestMergesReturnATransientBlobReadAsAnErrorNeverAsAnOutcome(t *testing.T) {
	w := newMgWorld(t)
	w.approved(5, cjHead2, "v2", 2, 201)
	w.leave(5)
	w.merged(5, cjHead2, mgSHA5, "v2", time.Minute)
	w.gh.trees[mgSHA5] = &bootTree{files: bootFiles("v2"), readErr: errors.New(bootSecret)} // the listing works; a blob does not
	w.advance(2 * time.Hour)

	w.mustMerge()

	w.wantNoOutcome(5)
	w.wantProblem("read-failed")
	if w.processed(5) {
		t.Fatal("a failed blob read is marked processed")
	}
	w.gh.trees[mgSHA5] = &bootTree{files: bootFiles("v2")}
	w.mustMerge()
	w.wantOutcome(5, governance.MergePromoted, "")
}

func TestMergesStatusShowsTheLastProblemUntilACleanRun(t *testing.T) {
	status := func(w *mgWorld) string {
		t.Helper()
		entries, err := (&githubStatus{repos: []string{bootRepo}, store: w.pulls, ids: &repositoryIDs{}, problems: &bootstrapProblems{}, checks: &bootstrapProblems{}, merges: w.mgProbs}).report(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		return entries[0].Merges
	}
	t.Run("read-failed and waiting", func(t *testing.T) {
		w := newMgWorld(t)
		w.sync(map[int]string{4: bootHead, 5: cjHead3})
		w.leave(5)
		w.merged(5, cjHead3, mgSHA5, "v1", time.Minute)
		w.gh.treeErr[mgSHA5] = &rest.StatusError{Status: 502, Body: bootSecret}
		w.mustMerge()
		if got := status(w); got != "read-failed" {
			t.Fatalf("status merges %q, want read-failed", got)
		}
		w.gh.treeErr[mgSHA5] = &rest.StatusError{Status: 404, Body: bootSecret}
		w.mustMerge()
		if got := status(w); got != "waiting" {
			t.Fatalf("status merges %q, want waiting", got)
		}
		delete(w.gh.treeErr, mgSHA5)
		w.mustMerge()
		if got := status(w); got != "" {
			t.Fatalf("status merges %q after a clean run, want none", got)
		}
	})
	t.Run("assessment-conflict", func(t *testing.T) {
		w := newMgWorld(t)
		w.approved(5, cjHead2, "v2", 2, 201)
		w.leave(5)
		w.merged(5, cjHead2, mgSHA5, "v2", time.Minute)
		w.merges.suites = assessmentConflicts{w.suites}

		w.mustMerge()

		w.wantOutcome(5, governance.MergeBlocked, governance.MergeReasonAssessmentConflict)
		if got := status(w); got != "assessment-conflict" {
			t.Fatalf("status merges %q, want assessment-conflict", got)
		}
		w.mustMerge()
		if got := status(w); got != "" {
			t.Fatalf("status merges %q after a clean run, want none", got)
		}
	})
	t.Run("failed", func(t *testing.T) {
		w := newMgWorld(t)
		w.merges.suites = &erroringSuites{UnitOfWork: w.suites, project: w.project(), err: errBootStore}

		err := w.runMerges()

		if !errors.Is(err, errBootStore) {
			t.Fatalf("run error %v, want the store error", err)
		}
		if got := status(w); got != "failed" {
			t.Fatalf("status merges %q, want failed", got)
		}
		w.merges.suites = w.suites
		w.mustMerge()
		if got := status(w); got != "" {
			t.Fatalf("status merges %q after a clean run, want none", got)
		}
	})
}

// assessmentConflicts makes every assessment a merge records conflict with one already stored.
type assessmentConflicts struct{ governance.UnitOfWork }

func (s assessmentConflicts) Do(ctx context.Context, project contract.ProjectID, suite contract.SuiteID, fn func(context.Context, governance.Tx) error) error {
	return s.UnitOfWork.Do(ctx, project, suite, func(ctx context.Context, tx governance.Tx) error { return fn(ctx, conflictingTx{tx}) })
}

type conflictingTx struct{ governance.Tx }

func (conflictingTx) RecordAssessment(context.Context, governance.AssessmentWrite) error {
	return governance.ErrConflict
}

func TestMergesNeverLogOrReturnWhatGitHubSent(t *testing.T) {
	w := newMgWorld(t)
	w.approved(5, cjHead2, "v2", 2, 201)
	w.approved(6, cjHead3, "v3", 3, 202)
	w.leave(5, 6)
	w.merged(5, cjHead2, mgSHA5, "v2", time.Minute)
	w.gh.pullErr[mgKey{bootRepo, 6}] = &rest.StatusError{Status: 500, Body: bootSecret}
	w.gh.closedErr = errors.New(bootSecret)
	w.gh.treeErr[mgSHA5] = &rest.StatusError{Status: 500, Body: bootSecret}
	phases := []func(){
		func() {}, // the listing fails, and so does the read of pull 6
		func() {
			w.gh.closedErr = nil
			delete(w.gh.pullErr, mgKey{bootRepo, 6})
			w.merged(6, cjHead3, mgSHA6, "v3", 2*time.Minute)
		}, // the tree read fails
		func() {
			delete(w.gh.treeErr, mgSHA5)
			w.gh.trees[mgSHA5] = &bootTree{files: bootFiles("v2"), readErr: fmt.Errorf("%s", bootSecret)} // a blob read fails
		},
	}
	for i, phase := range phases {
		phase()
		err := w.runMerges()
		if err != nil && strings.Contains(err.Error(), bootSecret) {
			t.Fatalf("phase %d: the run error carries what GitHub sent: %v", i, err)
		}
		if w.mgProbs.lookup(bootRepo) != "read-failed" {
			t.Fatalf("phase %d: problem %q, want read-failed\nlogs: %s", i, w.mgProbs.lookup(bootRepo), w.logs)
		}
	}

	if strings.Contains(w.logs.String(), bootSecret) {
		t.Fatalf("a log line carries what GitHub sent: %s", w.logs)
	}
	for _, m := range w.suites.messages {
		if strings.Contains(string(m.Payload), bootSecret) {
			t.Fatalf("a message carries what GitHub sent: %s", m.Payload)
		}
	}
}

// TestTheMergesJobIsTheOnlyWriterOfMergeFacts scans the composition: nothing but the job resolves pulls, records
// the closed listing, marks merges processed or promotes a merge (runtime contract, github_merges).
func TestTheMergesJobIsTheOnlyWriterOfMergeFacts(t *testing.T) {
	writes := []string{"governance.PromoteMerge(", ".RecordResolution(", ".RecordClosed(", ".MarkProcessed("}
	var callers []string
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, call := range writes {
			if strings.Contains(string(source), call) && filepath.Base(path) != "merges.go" {
				callers = append(callers, path+" calls "+call)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) != 0 {
		t.Fatalf("merge facts are written outside the merges job: %v", callers)
	}
}

// seedOther gives the other repository a canonical V1 of its own and makes the job look at it. Its baseline cannot be
// bootstrapped next to the first one: the operation id of a bootstrap promotion is bootstrap:P1-R1 in every project.
func (w *mgWorld) seedOther() {
	w.t.Helper()
	w.merges.repos = []string{bootRepo, bootOtherRepo}
	project := owner.ProjectID(bootOtherRepoID)
	carrier := carrierID(bootOtherRepoID, 3)
	protected := bootMust(contract.NewProtectedContract(bootMust(artifact.NewManifest([]artifact.Entry{{Path: "tests/a_test.go", Content: artifact.Hash([]byte("v1"))}})),
		artifact.Hash([]byte("tests/**")), map[string]string{}))
	reference := contract.ProposalReference{ProjectID: project, SuiteID: "default", ProposalID: "P1", RevisionID: "R1"}
	binding := bootMust(contract.NewApprovalBinding(contract.BindingInput{Reference: reference, Manifest: protected.Manifest().Digest(), Scope: protected.ScopeDigest(),
		PolicyRevision: owner.RevisionID(1), CoveredInputs: protected.CoveredInputs()}))
	proposal := bootMust(contract.NewProposal(bootMust(contract.NewProposalRevision(binding, "origin", carrier))))
	version := bootMust(contract.NewSuiteVersion(project, "default", "V1", protected.Manifest()))
	record := bootMust(contract.NewPromotionRecord(contract.PromotionRecordInput{OperationID: "seed-other", VersionID: "V1", Binding: binding, Carrier: carrier,
		Source: "origin", Target: "branch:main", RecordedAt: bootTime.Add(time.Hour)}))
	canonical := bootMust(contract.NewCanonicalSnapshot(bootMust(contract.NewSuite(project, "default", "V1", 1)), version, protected, record))
	seed := governance.Seed{Suite: governance.SuiteState{Canonical: canonical, Policy: bootMust(contract.NewPolicy(project, owner.RevisionID(1), bootOwnerPrincipal())), Target: "branch:main"},
		History: []contract.HistoricalCanonical{bootMust(contract.NewHistoricalCanonical(version, record))}, Proposals: []governance.SeedProposal{{Proposal: proposal}}}
	if err := w.mem.Seed(w.t.Context(), seed); err != nil {
		w.t.Fatal(err)
	}
}

// otherMerged leaves pull request 4 of the other repository, merged with no protected change.
func (w *mgWorld) otherMerged() {
	w.t.Helper()
	w.cycleAt(w.clock.Add(-time.Minute), bootOtherRepo, map[int]string{4: bootHead})
	w.cycleAt(w.clock, bootOtherRepo, map[int]string{})
	d := mgDetail(4, bootHead)
	d.HeadRepo, d.Merged, d.MergedAt, d.MergeCommitSHA = bootOtherRepo, true, bootTime.Add(2*time.Hour+time.Second), mgSHAOther
	w.gh.pulls[mgKey{bootOtherRepo, 4}] = d
	w.gh.trees[mgSHAOther] = &bootTree{files: bootFiles("v1")}
}

func (w *mgWorld) otherOutcome() (governance.MergeOutcome, bool) {
	w.t.Helper()
	outcome, found, err := governance.MergeOutcomeOf(w.t.Context(), w.mem, owner.ProjectID(bootOtherRepoID), "default", carrierID(bootOtherRepoID, 4))
	if err != nil {
		w.t.Fatal(err)
	}
	return outcome, found
}

// A blob of the integrated tree that answers 404 or an invalid response follows the bound of the tree itself: a file
// that is permanently unreadable must not stall the repository.
func TestMergesBoundABlobThatMayBeLate(t *testing.T) {
	late := map[string]error{
		"not found":        &rest.StatusError{Status: 404, Body: bootSecret},
		"invalid response": fmt.Errorf("%w: %s", github.ErrInvalidResponse, bootSecret),
	}
	for name, failure := range late {
		t.Run(name, func(t *testing.T) {
			w := newMgWorld(t)
			w.approved(5, cjHead2, "v2", 2, 201)
			w.leave(5)
			w.merged(5, cjHead2, mgSHA5, "v2", time.Minute)
			w.gh.trees[mgSHA5] = &bootTree{files: bootFiles("v2"), readErr: failure} // the listing works; a blob does not

			w.advance(29 * time.Minute)
			w.mustMerge()

			w.wantNoOutcome(5)
			w.wantProblem("waiting")
			if w.processed(5) {
				t.Fatal("an undecided merge is marked processed")
			}

			w.advance(time.Minute)
			w.mustMerge()

			outcome := w.wantOutcome(5, governance.MergeBlocked, governance.MergeReasonIntegrationUnreadable)
			if outcome.Source != contract.SourceRevision(mgSHA5) {
				t.Fatalf("source %q, want the merge commit kept", outcome.Source)
			}
			w.wantProblem("")
		})
	}
}

func TestMergesKeepAFailedBlobReadTransientPastTheBound(t *testing.T) {
	w := newMgWorld(t)
	w.approved(5, cjHead2, "v2", 2, 201)
	w.leave(5)
	w.merged(5, cjHead2, mgSHA5, "v2", time.Minute)
	w.gh.trees[mgSHA5] = &bootTree{files: bootFiles("v2"), readErr: &rest.StatusError{Status: 503, Body: bootSecret}}
	w.advance(2 * time.Hour)

	w.mustMerge()

	w.wantNoOutcome(5)
	w.wantProblem("read-failed")
}
