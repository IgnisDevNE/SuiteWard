package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync/githubsynctest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance/governancetest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner/ownertest"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

const (
	bootRepo        = "acme/widgets"
	bootRepoID      = int64(1001)
	bootOtherRepo   = "acme/gadgets"
	bootOtherRepoID = int64(1002)
	bootOwnerID     = int64(7)
	bootHead        = "1111111111111111111111111111111111111111"
	bootMovedHead   = "2222222222222222222222222222222222222222"
	bootSecret      = "SECRET-TEXT-GITHUB-SENT"
)

var (
	errBootStore = errors.New("the store failed")
	errBootCrash = errors.New("the process crashed")
	bootTime     = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
)

func bootMust[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

// bootTree is a pinned source tree held in memory.
type bootTree struct {
	files   map[string]string
	readErr error // every Read fails with it
}

func (t *bootTree) Entries(context.Context) ([]inventory.Entry, error) {
	var entries []inventory.Entry
	for _, path := range slices.Sorted(maps.Keys(t.files)) {
		entries = append(entries, inventory.Entry{Path: path, Kind: inventory.File})
	}
	return entries, nil
}

func (t *bootTree) Read(_ context.Context, path string) ([]byte, error) {
	if t.readErr != nil {
		return nil, t.readErr
	}
	content, found := t.files[path]
	if !found {
		return nil, fmt.Errorf("no file %q", path)
	}
	return []byte(content), nil
}

// bootFiles protects tests/** next to the default workflows: three protected files.
func bootFiles(testContent string) map[string]string {
	return map[string]string{
		".suiteward.yml":           "version: 1\ninclude: [/tests/**]\n",
		".github/workflows/ci.yml": "on: push\n",
		"tests/a_test.go":          testContent,
		"src/main.go":              "package main\n",
	}
}

type bootRepository struct {
	info    github.Repository
	heads   map[string]string
	trees   map[string]inventory.Tree
	repoErr error
	headErr error
	treeErr map[string]error
}

func newBootRepository(name string, id int64) *bootRepository {
	return &bootRepository{
		info:    github.Repository{ID: id, FullName: name, DefaultBranch: "main"},
		heads:   map[string]string{"main": bootHead},
		trees:   map[string]inventory.Tree{bootHead: &bootTree{files: bootFiles("v1")}},
		treeErr: map[string]error{},
	}
}

// fakeBootReader answers the bootstrap's three GitHub reads from memory.
type fakeBootReader struct {
	repos map[string]*bootRepository
	calls map[string]int
}

var _ bootstrapReader = (*fakeBootReader)(nil)

func (f *fakeBootReader) repo(name string) *bootRepository {
	repo, found := f.repos[name]
	if !found {
		panic("fake reader: unknown repository " + name)
	}
	return repo
}

func (f *fakeBootReader) Repository(_ context.Context, name string) (github.Repository, error) {
	f.calls["repository"]++
	repo := f.repo(name)
	return repo.info, repo.repoErr
}

func (f *fakeBootReader) BranchHead(_ context.Context, name, branch string) (string, error) {
	f.calls["head"]++
	repo := f.repo(name)
	return repo.heads[branch], repo.headErr
}

func (f *fakeBootReader) Tree(_ context.Context, name, sha string) (inventory.Tree, error) {
	f.calls["tree"]++
	repo := f.repo(name)
	if err := repo.treeErr[sha]; err != nil {
		return nil, err
	}
	return repo.trees[sha], nil
}

// fakeBootContent is a ContentWriter that records the digests it was given.
type fakeBootContent struct{ stored map[string]bool }

func (c *fakeBootContent) Put(_ context.Context, digest artifact.Digest, _ io.Reader) error {
	c.stored[digest.String()] = true
	return nil
}

// probeSuites passes every call to the Memory, records the outbox messages of the units of work that
// committed, and injects a failure before, or a crash after, the commit of one numbered call.
type probeSuites struct {
	governance.UnitOfWork
	calls     int
	failAt    int                                      // the call that fails before it reaches the store
	crashAt   int                                      // the call that commits and then reports a crash
	createErr func(contract.IntegrationTargetID) error // when set, CreateSuite fails with it after the call is numbered
	messages  []governance.OutboxMessage
}

func (p *probeSuites) next() int {
	p.calls++
	return p.calls
}

type recordingTx struct {
	governance.Tx
	sink *[]governance.OutboxMessage
}

func (r recordingTx) Outbox(ctx context.Context, message governance.OutboxMessage) error {
	if err := r.Tx.Outbox(ctx, message); err != nil {
		return err
	}
	*r.sink = append(*r.sink, message)
	return nil
}

func (p *probeSuites) Do(ctx context.Context, project contract.ProjectID, suite contract.SuiteID, fn func(context.Context, governance.Tx) error) error {
	call := p.next()
	if call == p.failAt {
		return errBootStore
	}
	var written []governance.OutboxMessage
	err := p.UnitOfWork.Do(ctx, project, suite, func(ctx context.Context, tx governance.Tx) error {
		return fn(ctx, recordingTx{Tx: tx, sink: &written})
	})
	if err != nil {
		return err
	}
	p.messages = append(p.messages, written...)
	if call == p.crashAt {
		return errBootCrash
	}
	return nil
}

func (p *probeSuites) CreateSuite(ctx context.Context, project contract.ProjectID, suite contract.SuiteID, policy contract.PolicyRevisionID, target contract.IntegrationTargetID) error {
	call := p.next()
	if call == p.failAt {
		return errBootStore
	}
	if p.createErr != nil {
		return p.createErr(target)
	}
	if err := p.UnitOfWork.CreateSuite(ctx, project, suite, policy, target); err != nil {
		return err
	}
	if call == p.crashAt {
		return errBootCrash
	}
	return nil
}

// bootWorld is one enrolled instance with a repository, a clock and fakes for every port of the job.
type bootWorld struct {
	t         *testing.T
	mem       *governancetest.Memory
	suites    *probeSuites
	owners    *ownertest.Memory
	pulls     *githubsynctest.Memory
	reader    *fakeBootReader
	content   *fakeBootContent
	logs      *bytes.Buffer
	job       *bootstrapJob
	problems  *bootstrapProblems
	cycles    int
	tier      string         // INSTALLATION_TIER, for the jobs that read it
	headRepos map[int]string // the head repository of a pull request openPulls records; absent means not observed
}

type bootOptions struct {
	enrolledTier string // the tier recorded at enrollment; empty means isolated
	configTier   string // INSTALLATION_TIER; empty means isolated
	repos        []string
	notEnrolled  bool
}

func newBootWorld(t *testing.T, options ...func(*bootOptions)) *bootWorld {
	t.Helper()
	o := bootOptions{enrolledTier: owner.TierIsolated, configTier: owner.TierIsolated, repos: []string{bootRepo}}
	for _, apply := range options {
		apply(&o)
	}
	w := &bootWorld{t: t, mem: governancetest.NewMemory(), owners: ownertest.NewMemory(), pulls: githubsynctest.NewMemory(),
		content: &fakeBootContent{stored: map[string]bool{}}, logs: &bytes.Buffer{}, tier: o.configTier}
	w.suites = &probeSuites{UnitOfWork: w.mem}
	w.reader = &fakeBootReader{repos: map[string]*bootRepository{bootRepo: newBootRepository(bootRepo, bootRepoID), bootOtherRepo: newBootRepository(bootOtherRepo, bootOtherRepoID)},
		calls: map[string]int{}}
	if !o.notEnrolled {
		projects := []owner.ProjectPolicy{{RepositoryID: bootRepoID, Repo: bootRepo, TestCheck: "ci"}, {RepositoryID: bootOtherRepoID, Repo: bootOtherRepo, TestCheck: "ci"}}
		err := w.owners.Enroll(t.Context(), owner.Enrollment{Owner: owner.Owner{AccountID: bootOwnerID, Login: "owner"}, Secret: make([]byte, 20), Projects: projects,
			Step: 1, Tier: o.enrolledTier, EnrolledAt: bootTime})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int64{bootRepoID, bootOtherRepoID} { // the policy of each project, which CreateSuite finds only when a Seed registered it
		w.registerPolicy(owner.ProjectID(id))
	}
	w.problems = &bootstrapProblems{}
	w.job = &bootstrapJob{repos: o.repos, reader: w.reader, owners: w.owners, suites: w.suites, problems: w.problems,
		logger: slog.New(slog.NewJSONHandler(w.logs, nil)), now: func() time.Time { return bootTime.Add(time.Hour) }}
	return w
}

func withRepos(repos ...string) func(*bootOptions) {
	return func(o *bootOptions) { o.repos = repos }
}

func notEnrolled(o *bootOptions) { o.notEnrolled = true }

func bootOwnerPrincipal() contract.Principal {
	return bootMust(contract.NewPrincipal(owner.PrincipalID(bootOwnerID), contract.Human))
}

func (w *bootWorld) seed(project contract.ProjectID, suite contract.SuiteID, target contract.IntegrationTargetID) {
	w.t.Helper()
	policy := bootMust(contract.NewPolicy(project, owner.RevisionID(1), bootOwnerPrincipal()))
	canonical := bootMust(contract.NewCanonicalSnapshot(bootMust(contract.NewSuite(project, suite, "", 0)), contract.SuiteVersion{}, contract.ProtectedContract{}, contract.PromotionRecord{}))
	if err := w.mem.Seed(w.t.Context(), governance.Seed{Suite: governance.SuiteState{Canonical: canonical, Policy: policy, Target: target}}); err != nil {
		w.t.Fatal(err)
	}
}

// registerPolicy makes the policy of the enrollment known to the Memory through a Suite the job never looks at.
func (w *bootWorld) registerPolicy(project contract.ProjectID) {
	w.seed(project, "placeholder", "branch:placeholder")
}

// seedSuite adds the default Suite of the project of repository id, as an earlier run would have.
func (w *bootWorld) seedSuite(id int64, target contract.IntegrationTargetID) {
	w.seed(owner.ProjectID(id), "default", target)
}

func (w *bootWorld) run() error { _, err := w.job.run(w.t.Context()); return err }

func (w *bootWorld) mustRun() {
	w.t.Helper()
	if err := w.run(); err != nil {
		w.t.Fatalf("run: %v\nlogs: %s", err, w.logs)
	}
}

// openPulls records a synchronization cycle in which exactly these pull requests of repo are open.
func (w *bootWorld) openPulls(repo string, numbers ...int) {
	w.t.Helper()
	w.cycles++
	cycle := githubsync.Cycle{Repo: repo, InstallationID: 1, At: bootTime.Add(time.Duration(w.cycles) * time.Minute), StartedAt: bootTime.Add(time.Duration(w.cycles) * time.Minute), Requests: 1, Comments: map[int][]githubsync.ObservedComment{}}
	for _, number := range numbers {
		cycle.OpenPulls = append(cycle.OpenPulls, githubsync.ObservedPull{Number: number, HeadSHA: bootHead, HeadRef: "feature", BaseRef: "main", BaseSHA: bootHead, UpdatedAt: bootTime, HeadRepo: w.headRepos[number]})
		cycle.Comments[number] = nil
	}
	if _, err := w.pulls.RecordCycle(w.t.Context(), cycle); err != nil {
		w.t.Fatal(err)
	}
}

func (w *bootWorld) project() contract.ProjectID { return owner.ProjectID(bootRepoID) }

// read runs fn in a read of the default Suite of the main project.
func (w *bootWorld) read(fn func(ctx context.Context, tx governance.Tx)) {
	w.t.Helper()
	err := w.mem.Do(w.t.Context(), w.project(), "default", func(ctx context.Context, tx governance.Tx) error {
		fn(ctx, tx)
		return nil
	})
	if err != nil {
		w.t.Fatalf("read the default suite: %v", err)
	}
}

func (w *bootWorld) suiteExists() bool {
	w.t.Helper()
	err := w.mem.Do(w.t.Context(), w.project(), "default", func(context.Context, governance.Tx) error { return nil })
	if err != nil && !errors.Is(err, governance.ErrNotFound) {
		w.t.Fatal(err)
	}
	return err == nil
}

func (w *bootWorld) proposal(n int) (proposal contract.Proposal, found bool) {
	w.t.Helper()
	err := w.mem.Do(w.t.Context(), w.project(), "default", func(ctx context.Context, tx governance.Tx) error {
		p, _, err := tx.Proposal(ctx, contract.ProposalID(fmt.Sprintf("P%d", n)))
		proposal, found = p, err == nil
		if errors.Is(err, governance.ErrNotFound) {
			return nil
		}
		return err
	})
	if err != nil && !errors.Is(err, governance.ErrNotFound) { // no Suite means no proposal
		w.t.Fatal(err)
	}
	return proposal, found
}

func (w *bootWorld) canonical() (version contract.SuiteVersionID, found bool) {
	w.t.Helper()
	w.read(func(ctx context.Context, tx governance.Tx) {
		state, err := tx.Suite(ctx)
		if err != nil {
			w.t.Fatal(err)
		}
		version, found = state.Canonical.Suite().CurrentVersionID()
	})
	return version, found
}

func (w *bootWorld) outboxKeys() []string {
	w.t.Helper()
	keys, err := w.mem.OutboxKeys(w.t.Context())
	if err != nil {
		w.t.Fatal(err)
	}
	return keys
}

func (w *bootWorld) requireOutbox(want ...string) {
	w.t.Helper()
	if got := w.outboxKeys(); !slices.Equal(got, want) {
		w.t.Fatalf("outbox keys = %v, want %v", got, want)
	}
}

func (w *bootWorld) message(key string) governance.OutboxMessage {
	w.t.Helper()
	for _, message := range w.suites.messages {
		if message.Key == key {
			return message
		}
	}
	w.t.Fatalf("no outbox message %q among %d", key, len(w.suites.messages))
	return governance.OutboxMessage{}
}

// payload decodes the message into a generic object, so the test pins the names of the JSON keys.
func (w *bootWorld) payload(key string) map[string]any {
	w.t.Helper()
	message := w.message(key)
	if message.Kind != "github_comment" {
		w.t.Fatalf("message %q has kind %q, want github_comment", key, message.Kind)
	}
	var object map[string]any
	if err := json.Unmarshal(message.Payload, &object); err != nil {
		w.t.Fatal(err)
	}
	return object
}

// approve records the owner's approval of P<n>-R1 of the main project as the command of a comment would.
func (w *bootWorld) approve(n int, carrierPull int) {
	w.approveIn(bootRepoID, 100+int64(n), n, carrierPull)
}

// approveIn records the owner's approval of P<n>-R1 of the project of repository id, as the comment with that id would.
func (w *bootWorld) approveIn(repositoryID, comment int64, n int, carrierPull int) {
	w.t.Helper()
	reference := contract.ProposalReference{ProjectID: owner.ProjectID(repositoryID), SuiteID: "default", ProposalID: contract.ProposalID(fmt.Sprintf("P%d", n)), RevisionID: "R1"}
	command := bootMust(contract.NewCommand(contract.CommandInput{OperationID: contract.OperationID(fmt.Sprintf("consent:github-comment:%d", comment)),
		SourceCommandID: contract.SourceCommandID(fmt.Sprintf("github-comment:%d", comment)), Actor: bootOwnerPrincipal(), Reference: reference,
		Carrier: contract.ApprovalCarrierID(fmt.Sprintf("github-pr:%d/%d", repositoryID, carrierPull)), Action: contract.ApproveConsent, Order: contract.CommandOrder(comment)}))
	response, err := governance.ProcessConsent(w.t.Context(), w.mem, governance.ConsentRequest{Command: command})
	if err != nil || !response.Receipt.CurrentApprovalEligible {
		w.t.Fatalf("approve P%d-R1: eligible %v, error %v", n, response.Receipt.CurrentApprovalEligible, err)
	}
}

func (w *bootWorld) assessmentOutcome(n int, source string) (contract.IntegrityOutcome, bool) {
	w.t.Helper()
	var (
		outcome contract.IntegrityOutcome
		found   bool
	)
	w.read(func(ctx context.Context, tx governance.Tx) {
		proposal, _, err := tx.Proposal(ctx, contract.ProposalID(fmt.Sprintf("P%d", n)))
		if err != nil {
			w.t.Fatal(err)
		}
		assessment, present, err := tx.Assessment(ctx, proposal.Current().Binding().Reference(), contract.SourceRevision(source))
		if err != nil {
			w.t.Fatal(err)
		}
		if !present {
			return
		}
		evidence, hasEvidence := assessment.Evidence()
		if !hasEvidence || evidence.EmitterID() != governance.ServiceEmitter.ID() {
			w.t.Fatalf("the assessment has evidence %v from %q, want evidence from the service emitter", hasEvidence, evidence.EmitterID())
		}
		outcome, found = evidence.Outcome(), true
	})
	return outcome, found
}

// proposedWorld returns a world in which P1-R1 is proposed on pull request 4 and no approval exists.
func proposedWorld(t *testing.T, options ...func(*bootOptions)) *bootWorld {
	t.Helper()
	w := newBootWorld(t, options...)
	w.openPulls(bootRepo, 4)
	w.branchProposal(4)
	return w
}

// approvedWorld is a proposed world in which the owner approved P1-R1.
func approvedWorld(t *testing.T, options ...func(*bootOptions)) *bootWorld {
	t.Helper()
	w := proposedWorld(t, options...)
	w.approve(1, 4)
	return w
}

const (
	summaryKey  = "bootstrap-summary:1001:P1-R1"
	promotedKey = "bootstrap-promoted:1001:P1-R1"
)

func TestBootstrapCreatesTheSuiteOnlyWhenItDoesNotExist(t *testing.T) {
	t.Run("missing suite", func(t *testing.T) {
		w := newBootWorld(t)

		w.mustRun()

		if !w.suiteExists() {
			t.Fatal("the Suite was not created")
		}
		w.read(func(ctx context.Context, tx governance.Tx) {
			state, err := tx.Suite(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if state.Target != "branch:main" || state.Policy.RevisionID() != "setup-1" || state.Policy.OwnerID() != "github:7" {
				t.Fatalf("suite targets %q under policy %q of %q, want branch:main under setup-1 of github:7", state.Target, state.Policy.RevisionID(), state.Policy.OwnerID())
			}
		})
		w.mustRun()
		if got := w.reader.calls["repository"]; got != 1 {
			t.Fatalf("%d repository reads after two runs, want 1: an existing Suite needs none without a PR", got)
		}
	})
	t.Run("an existing suite is never created again", func(t *testing.T) {
		w := newBootWorld(t)
		w.seedSuite(bootRepoID, "branch:trunk") // another target than the repository's default branch: CreateSuite would conflict

		w.mustRun()

		w.read(func(ctx context.Context, tx governance.Tx) {
			if state, err := tx.Suite(ctx); err != nil || state.Target != "branch:trunk" {
				t.Fatalf("suite = %+v, %v; want the existing target", state, err)
			}
		})
		if got := w.reader.calls["repository"]; got != 0 {
			t.Fatalf("%d repository reads for an existing Suite, want 0", got)
		}
	})
}

func TestBootstrapActsOnlyForEnrolledConfiguredRepositories(t *testing.T) {
	t.Run("before setup", func(t *testing.T) {
		w := newBootWorld(t, notEnrolled)
		w.openPulls(bootRepo, 4)

		w.mustRun()

		if w.suiteExists() || w.reader.calls["repository"] != 0 {
			t.Fatal("the bootstrap acted before an owner was enrolled")
		}
	})
	t.Run("a project that is not configured", func(t *testing.T) {
		w := newBootWorld(t, withRepos(bootOtherRepo))
		w.openPulls(bootRepo, 4)

		w.mustRun()

		if w.suiteExists() {
			t.Fatal("the bootstrap created the Suite of a repository that is not configured")
		}
	})
}

func TestBootstrapPromotesAnApprovedBaselineOnce(t *testing.T) {
	w := approvedWorld(t)
	w.openPulls(bootRepo) // the carrier closes: an approved baseline does not wait for the PR to merge

	w.mustRun()

	if version, found := w.canonical(); !found || version != "V1" {
		t.Fatalf("canonical = %q (found %v), want V1\nlogs: %s", version, found, w.logs)
	}
	if outcome, found := w.assessmentOutcome(1, bootHead); !found || outcome != contract.IntegrityPassed {
		t.Fatalf("assessment = %v (found %v), want Passed from the service emitter", outcome, found)
	}
	w.requireOutbox(promotedKey, summaryKey)
	payload := w.payload(promotedKey)
	want := map[string]any{"repositoryId": float64(1001), "repo": bootRepo, "pull": float64(4), "reference": "P1-R1", "version": "V1"}
	if !maps.Equal(payload, want) {
		t.Fatalf("promoted payload = %v, want %v", payload, want)
	}

	tree := w.reader.calls["tree"]
	w.mustRun()
	w.mustRun()
	w.requireOutbox(promotedKey, summaryKey)
	if w.reader.calls["tree"] != tree {
		t.Fatalf("the bootstrap read GitHub after the canonical existed")
	}
}

// Operation ids are global: two repositories that each bootstrap their first baseline must not collide on P1-R1.
func TestBootstrapPromotesTheFirstBaselineOfEachRepository(t *testing.T) {
	w := newBootWorld(t, withRepos(bootRepo, bootOtherRepo))
	w.openPulls(bootRepo, 4)
	w.openPulls(bootOtherRepo, 5)
	w.mustRun() // creates both Suites
	proposeBaseline(t, w.suites, w.content, bootRepoID, bootRepo, contract.OriginBranch, 4, bootHead, bootFiles("v1"))
	proposeBaseline(t, w.suites, w.content, bootOtherRepoID, bootOtherRepo, contract.OriginBranch, 5, bootHead, bootFiles("v1"))
	w.approveIn(bootRepoID, 101, 1, 4)
	w.approveIn(bootOtherRepoID, 201, 1, 5)

	w.mustRun()

	for _, id := range []int64{bootRepoID, bootOtherRepoID} {
		var version contract.SuiteVersionID
		err := w.mem.Do(t.Context(), owner.ProjectID(id), "default", func(ctx context.Context, tx governance.Tx) error {
			state, err := tx.Suite(ctx)
			version, _ = state.Canonical.Suite().CurrentVersionID()
			return err
		})
		if err != nil || version != "V1" {
			t.Fatalf("repository %d: canonical = %q, %v; want V1\nlogs: %s", id, version, err, w.logs)
		}
	}
}

func TestBootstrapWakesChecksAfterPromotingV1(t *testing.T) {
	w := approvedWorld(t)
	w.openPulls(bootRepo)

	wake, err := w.job.run(t.Context())
	if err != nil {
		t.Fatalf("run: %v\nlogs: %s", err, w.logs)
	}
	if want := (river.Wake{river.ChecksKind}); !slices.Equal(wake, want) {
		t.Fatalf("a run that promoted V1 woke %v; want %v", wake, want)
	}
	if wake, err := w.job.run(t.Context()); err != nil || len(wake) != 0 { // the canonical exists now: nothing is promoted again
		t.Fatalf("the run after the promotion = %v, %v; want no wake-ups", wake, err)
	}
}

func TestBootstrapWakesNothingWhenItPromotedNothing(t *testing.T) {
	t.Run("it only created the Suite", func(t *testing.T) {
		w := newBootWorld(t)

		if wake, err := w.job.run(t.Context()); err != nil || len(wake) != 0 {
			t.Fatalf("a run that created the Suite = %v, %v; want no wake-ups", wake, err)
		}
	})
	t.Run("the baseline is not approved", func(t *testing.T) {
		w := proposedWorld(t)

		if wake, err := w.job.run(t.Context()); err != nil || len(wake) != 0 {
			t.Fatalf("a run of an unapproved baseline = %v, %v; want no wake-ups", wake, err)
		}
	})
}

func TestBootstrapDoesNotPromoteWithoutApproval(t *testing.T) {
	w := proposedWorld(t)

	w.mustRun()

	if _, found := w.canonical(); found {
		t.Fatal("a canonical exists without the owner's approval")
	}
	if _, found := w.assessmentOutcome(1, bootHead); found {
		t.Fatal("an assessment was recorded before the approval")
	}
}

func TestBootstrapDoesNotPromoteAnOriginThatChangedOrCannotBeRead(t *testing.T) {
	tests := map[string]struct {
		change  func(*bootRepository)
		outcome contract.IntegrityOutcome
	}{
		"failed: the origin tree differs from the proposed one": {
			func(r *bootRepository) { r.trees[bootHead] = &bootTree{files: bootFiles("tampered")} }, contract.IntegrityFailed},
		"unavailable: the tree is truncated": {
			func(r *bootRepository) {
				r.treeErr[bootHead] = fmt.Errorf("%w: GitHub truncated the tree", inventory.ErrInvalidTree)
			}, contract.IntegrityUnavailable},
		"unavailable: the declaration is invalid": {
			func(r *bootRepository) {
				files := bootFiles("v1")
				files[".suiteward.yml"] = "version: 99\n"
				r.trees[bootHead] = &bootTree{files: files}
			}, contract.IntegrityUnavailable},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			w := approvedWorld(t)
			test.change(w.reader.repo(bootRepo))

			w.mustRun()

			if _, found := w.canonical(); found {
				t.Fatal("a canonical exists although the origin did not pass")
			}
			if outcome, found := w.assessmentOutcome(1, bootHead); !found || outcome != test.outcome {
				t.Fatalf("assessment = %v (found %v), want %v", outcome, found, test.outcome)
			}
			w.requireOutbox(summaryKey)
			// The next run finds the recorded outcome: the commit is immutable, so reading it again would change nothing.
			tree := w.reader.calls["tree"]
			w.mustRun()
			if w.reader.calls["tree"] != tree {
				t.Fatal("the bootstrap read the origin again after recording that it did not pass")
			}
			if _, found := w.canonical(); found {
				t.Fatal("a canonical exists after the second run")
			}
		})
	}
}

// requireProblem requires the last bootstrap problem of the main repository to be code ("" for none).
func (w *bootWorld) requireProblem(code string) {
	w.t.Helper()
	if got := w.problems.lookup(bootRepo); got != code {
		w.t.Fatalf("bootstrap problem = %q, want %q; logs: %s", got, code, w.logs)
	}
}

func TestBootstrapKeepsTheProblemOfAPromotionThatCannotProceed(t *testing.T) {
	t.Run("read of the origin", func(t *testing.T) {
		w := approvedWorld(t)
		w.reader.repo(bootRepo).treeErr[bootHead] = errors.New(bootSecret)

		w.mustRun()

		w.requireProblem("read-failed")
		delete(w.reader.repo(bootRepo).treeErr, bootHead)
		w.mustRun()
		w.requireProblem("")
	})
	t.Run("origin did not pass", func(t *testing.T) {
		w := approvedWorld(t)
		w.reader.repo(bootRepo).trees[bootHead] = &bootTree{files: bootFiles("tampered")}

		w.mustRun()
		w.requireProblem("origin-not-passed")
		w.mustRun() // the recorded outcome stops the run without reading GitHub
		w.requireProblem("origin-not-passed")
	})
	t.Run("assessment conflict", func(t *testing.T) {
		w := approvedWorld(t)
		proposal, _ := w.proposal(1)
		binding := proposal.Current().Binding()
		evidence := bootMust(contract.NewIntegrityEvidence(governance.ServiceEmitter.ID(), contract.SourceRevision(bootHead), binding, contract.IntegrityPassed))
		err := w.mem.Do(t.Context(), w.project(), "default", func(ctx context.Context, tx governance.Tx) error {
			return tx.RecordAssessment(ctx, governance.AssessmentWrite{Reference: binding.Reference(), Source: contract.SourceRevision(bootHead), Evidence: evidence})
		})
		if err != nil {
			t.Fatal(err)
		}
		w.reader.repo(bootRepo).trees[bootHead] = &bootTree{files: bootFiles("tampered")} // this read disagrees with the recorded Passed

		w.mustRun()

		w.requireProblem("assessment-conflict")
	})
}

func TestBootstrapBlockedReasonsHaveDistinctCodes(t *testing.T) {
	seen := map[string]contract.PromotionReason{}
	for reason := contract.PromotionReasonContextMismatch; reason <= contract.PromotionReasonCorrectionContextReused; reason++ {
		code := blockedCode(reason)
		if !strings.HasPrefix(code, "blocked-") || len(code) == len("blocked-") {
			t.Errorf("reason %d has code %q, want blocked-<reason>", reason, code)
		}
		if other, dup := seen[code]; dup {
			t.Errorf("reasons %d and %d share the code %q", other, reason, code)
		}
		seen[code] = reason
	}
}

func TestCutBytesStopsOnAUTF8BoundaryAndMarksTheCut(t *testing.T) {
	tests := map[string]string{
		"ascii":        strings.Repeat("a", 400),
		"two bytes":    strings.Repeat("é", 200),
		"three bytes":  strings.Repeat("€", 100),
		"four bytes":   strings.Repeat("😀", 100),
		"stray bytes":  strings.Repeat("\x80", 400),
		"invalid mix":  strings.Repeat("a\xffb", 200),
		"short":        "src/main.go",
		"exactly 150":  strings.Repeat("a", 150),
		"151 bytes":    strings.Repeat("a", 151),
		"empty string": "",
	}
	for name, s := range tests {
		t.Run(name, func(t *testing.T) {
			got := cutBytes(s, 150)

			if len(got) > 150 {
				t.Fatalf("cutBytes = %d bytes, want at most 150 with the marker", len(got))
			}
			if len(s) <= 150 {
				if got != s {
					t.Fatal("a string within the limit was changed")
				}
				return
			}
			kept, marked := strings.CutSuffix(got, "\u2026")
			if !marked || kept == "" || !strings.HasPrefix(s, kept) {
				t.Fatalf("cutBytes = %q, want a non-empty prefix of the input followed by the marker", got)
			}
			if utf8.ValidString(s) && !utf8.ValidString(got) {
				t.Fatal("a valid string was cut inside a rune")
			}
		})
	}
}

func TestBootstrapSummaryCountsTheRulesItDropsAndMarksWhatItCut(t *testing.T) {
	long := strings.Repeat("r", 400)
	rules := func(n int) []string {
		list := []string{long}
		for i := 1; i < n; i++ {
			list = append(list, fmt.Sprintf("rule%d/**", i))
		}
		return list
	}
	message, err := newSummaryMessage(commentTarget{RepositoryID: 1, Repo: "a/b", Pull: 1},
		bootstrapSummaryInputs{Proposal: 1, Revision: 1, Origin: bootHead, Branch: "main", Protected: 1, Paths: []string{long}, Include: rules(25), Exclude: rules(21), Tier: owner.TierIsolated})
	if err != nil {
		t.Fatal(err)
	}

	var decoded bootstrapSummaryPayload
	if err := json.Unmarshal(message.Payload, &decoded); err != nil {
		t.Fatal(err)
	}

	s := decoded.Summary
	if s.IncludeTotal != 25 || s.ExcludeTotal != 21 || len(s.Include) != 20 || len(s.Exclude) != 20 {
		t.Fatalf("include %d of %d, exclude %d of %d; want 20 of 25 and 20 of 21", len(s.Include), s.IncludeTotal, len(s.Exclude), s.ExcludeTotal)
	}
	for _, shortened := range []string{s.Paths[0], s.Include[0], s.Exclude[0]} {
		if !strings.HasSuffix(shortened, "\u2026") || len(shortened) > maxSummaryText {
			t.Fatalf("a shortened entry is %q (%d bytes), want at most %d bytes ending in the marker", shortened, len(shortened), maxSummaryText)
		}
	}
	if s.Include[1] != "rule1/**" {
		t.Fatalf("a short rule changed to %q", s.Include[1])
	}
	rendered, err := s.summary()
	if err != nil {
		t.Fatal(err)
	}
	if body := commandtext.BootstrapSummary(rendered); !strings.Contains(body, "5 more are not listed") || !strings.Contains(body, "1 more are not listed") {
		t.Fatalf("the rendered comment does not count the rules it left out:\n%s", body)
	}
}

// worstCase is a repository whose paths and rules are as large as JSON encoding can make them.
func worstCase(unit string) bootstrapSummaryInputs {
	entry := strings.Repeat(unit, 500)
	list := func() []string { return slices.Repeat([]string{entry}, 40) } // more than the 20 the payload keeps
	return bootstrapSummaryInputs{Proposal: 999999999, Revision: 1, Origin: bootHead, Branch: entry, Protected: 100000, Paths: list(), Include: list(), Runner: list(), Exclude: list(), Tier: owner.TierCoLocated}
}

func TestBootstrapSummaryStaysWithinTheOutboxLimitForAnyRepositoryText(t *testing.T) {
	units := map[string]string{
		"control bytes": "\x01",
		"invalid UTF-8": "\xff",
		"stray":         "\x80",
		"html":          "<",
		"quotes":        `"`,
	}
	for name, unit := range units {
		t.Run(name, func(t *testing.T) {
			target := commentTarget{RepositoryID: 1<<62 + 1, Repo: strings.Repeat("o", 39) + "/" + strings.Repeat("n", 100), Pull: 1<<31 - 1}

			message, err := newSummaryMessage(target, worstCase(unit))
			if err != nil {
				t.Fatal(err)
			}

			if len(message.Payload) > 64<<10 {
				t.Fatalf("payload is %d bytes, over the 64 KiB outbox limit", len(message.Payload))
			}
			if err := message.Validate(); err != nil {
				t.Fatalf("the outbox rejects the message: %v", err)
			}
			var decoded bootstrapSummaryPayload
			if err := json.Unmarshal(message.Payload, &decoded); err != nil {
				t.Fatal(err)
			}
			if len(decoded.Summary.Paths) != 20 || len(decoded.Summary.Include) != 20 || len(decoded.Summary.Runner) != 20 || len(decoded.Summary.Exclude) != 20 || decoded.Summary.RunnerTotal != 40 {
				t.Fatalf("payload keeps %d paths, %d include and %d exclude rules, want 20 each", len(decoded.Summary.Paths), len(decoded.Summary.Include), len(decoded.Summary.Exclude))
			}
			if decoded.Summary.Branch == "" {
				t.Fatal("the branch was cut to nothing")
			}
			if decoded.Summary.IncludeTotal != 40 || decoded.Summary.ExcludeTotal != 40 {
				t.Fatalf("totals are %d and %d, want the 40 rules each list had", decoded.Summary.IncludeTotal, decoded.Summary.ExcludeTotal)
			}
			for _, entry := range slices.Concat(decoded.Summary.Paths, decoded.Summary.Include, decoded.Summary.Runner, decoded.Summary.Exclude, []string{decoded.Summary.Branch}) {
				if !strings.HasSuffix(entry, "…") {
					t.Fatalf("a cut entry has no marker: %q", entry)
				}
			}
			rendered, err := decoded.Summary.summary()
			if err != nil {
				t.Fatal(err)
			}
			if body := commandtext.BootstrapSummary(rendered); len(body) >= 65536 {
				t.Fatalf("the rendered comment is %d bytes, over GitHub's limit", len(body))
			}
		})
	}
}

func TestSummaryInputsRejectAnUnknownTier(t *testing.T) {
	if _, err := (bootstrapSummaryInputs{Tier: "somewhere"}).summary(); err == nil {
		t.Fatal("an unknown tier was accepted")
	}
}

// suiteOf reports whether the Suite of repository id exists.
func (w *bootWorld) suiteOf(id int64) bool {
	w.t.Helper()
	err := w.mem.Do(w.t.Context(), owner.ProjectID(id), defaultSuite, func(context.Context, governance.Tx) error { return nil })
	if err != nil && !errors.Is(err, governance.ErrNotFound) {
		w.t.Fatal(err)
	}
	return err == nil
}

func TestBootstrapSkipsAProjectWhoseGitHubReadFailedAndNeverLogsGitHubText(t *testing.T) {
	t.Run("suite creation", func(t *testing.T) {
		w := newBootWorld(t)
		w.reader.repo(bootRepo).repoErr = errors.New(bootSecret)

		if err := w.run(); err != nil {
			t.Fatalf("a GitHub read failure failed the run: %v", err)
		}

		if w.suiteExists() {
			t.Fatal("the Suite was created from a failed read")
		}
		if strings.Contains(w.logs.String(), bootSecret) {
			t.Fatalf("a log line holds the text GitHub sent:\n%s", w.logs)
		}
		w.reader.repo(bootRepo).repoErr = nil // the failure ends
		w.mustRun()
		if !w.suiteExists() {
			t.Fatalf("the next run did not create the Suite\nlogs: %s", w.logs)
		}
	})
	t.Run("promotion", func(t *testing.T) {
		w := approvedWorld(t)
		w.reader.repo(bootRepo).treeErr[bootHead] = errors.New(bootSecret)

		w.mustRun()

		if _, found := w.canonical(); found {
			t.Fatal("a canonical exists after a failed read")
		}
		if _, found := w.assessmentOutcome(1, bootHead); found {
			t.Fatal("a failed read was recorded as an assessment: it could never be Passed afterwards")
		}
		if strings.Contains(w.logs.String(), bootSecret) {
			t.Fatalf("a log line holds the text GitHub sent:\n%s", w.logs)
		}
		delete(w.reader.repo(bootRepo).treeErr, bootHead)
		w.mustRun()
		if version, found := w.canonical(); !found || version != "V1" {
			t.Fatalf("canonical = %q (found %v) after the read recovered, want V1", version, found)
		}
	})
}

func TestBootstrapKeepsTheLastProblemOfAGitHubReadUntilTheProjectRunsCleanly(t *testing.T) {
	failures := map[string]struct {
		fail func(*bootRepository)
		code string
	}{
		"repository": {func(r *bootRepository) { r.repoErr = errors.New(bootSecret) }, "read-failed"},
		"changed":    {func(r *bootRepository) { r.info.ID = 5555 }, "repository-changed"},
	}
	for name, test := range failures {
		t.Run(name, func(t *testing.T) {
			w := newBootWorld(t)
			good := w.reader.repo(bootRepo)
			original := newBootRepository(bootRepo, bootRepoID)
			test.fail(good)

			w.mustRun()

			w.requireProblem(test.code)
			if strings.Contains(w.logs.String(), bootSecret) || strings.Contains(w.problems.lookup(bootRepo), bootSecret) {
				t.Fatalf("GitHub text reached the problem or the logs: %s", w.logs)
			}
			w.mustRun()
			w.requireProblem(test.code) // still failing: still shown
			*good = *original
			w.mustRun()
			w.requireProblem("")
		})
	}
}

func TestBootstrapKeepsTheProblemOfAFailedRunAndClearsItWhenTheProjectIsCanonical(t *testing.T) {
	w := newBootWorld(t)
	w.suites.failAt = 1

	if err := w.run(); !errors.Is(err, errBootStore) {
		t.Fatalf("run = %v, want the store failure", err)
	}
	w.requireProblem("failed")

	w.suites.failAt = 0
	w.mustRun() // creates the Suite
	w.requireProblem("")
	w.branchProposal(4)
	w.approve(1, 4)
	w.mustRun()
	w.requireProblem("")
	if _, found := w.canonical(); !found {
		t.Fatal("the baseline was not promoted")
	}
}

func TestBootstrapContinuesWithTheNextProjectAfterAFailure(t *testing.T) {
	w := newBootWorld(t, withRepos(bootRepo, bootOtherRepo))
	w.reader.repo(bootRepo).repoErr = errors.New(bootSecret)

	w.mustRun()

	if w.suiteOf(bootRepoID) || !w.suiteOf(bootOtherRepoID) {
		t.Fatalf("the Suites exist for the first project %v and the second %v, want only the second", w.suiteOf(bootRepoID), w.suiteOf(bootOtherRepoID))
	}
}

func TestBootstrapSkipsARepositoryThatIsNotTheEnrolledOne(t *testing.T) {
	w := newBootWorld(t)
	w.reader.repo(bootRepo).info.ID = 5555 // the name now belongs to another repository

	w.mustRun()

	if w.suiteExists() {
		t.Fatal("the Suite of another repository was created")
	}
}

func TestBootstrapFailsTheRunOnAStoreFailureButTriesTheOtherProjects(t *testing.T) {
	w := newBootWorld(t, withRepos(bootRepo, bootOtherRepo))
	w.suites.failAt = 1 // the first project's first store call

	err := w.run()

	if !errors.Is(err, errBootStore) {
		t.Fatalf("run = %v, want the store failure", err)
	}
	if w.suiteOf(bootRepoID) || !w.suiteOf(bootOtherRepoID) {
		t.Fatalf("the Suites exist for the first project %v and the second %v, want only the second", w.suiteOf(bootRepoID), w.suiteOf(bootOtherRepoID))
	}
	w.suites.failAt = 0
	w.mustRun()
	if !w.suiteOf(bootRepoID) {
		t.Fatal("the first project's Suite was not created by the next run")
	}
}

// crashAtEveryStoreCall runs the job on a fresh world once per store call, committing that call and then
// crashing, reruns it cleanly and requires the same end state as an uninterrupted run.
func crashAtEveryStoreCall(t *testing.T, setup func(t *testing.T) *bootWorld, final func(t *testing.T, w *bootWorld)) {
	t.Helper()
	for crashAt := 1; ; crashAt++ {
		if crashAt > 25 {
			t.Fatal("the job still crashes at the 25th store call")
		}
		w := setup(t)
		w.suites.calls, w.suites.crashAt = 0, crashAt
		err := w.run()
		crashed := errors.Is(err, errBootCrash)
		if err != nil && !crashed {
			t.Fatalf("crash at call %d: run = %v", crashAt, err)
		}
		w.suites.crashAt = 0
		w.mustRun()
		w.mustRun()
		final(t, w)
		if t.Failed() {
			t.Fatalf("the end state is wrong after a crash at store call %d", crashAt)
		}
		if !crashed {
			return
		}
	}
}

func TestBootstrapReplayAfterACrashDuplicatesNothing(t *testing.T) {
	t.Run("creating the suite", func(t *testing.T) {
		crashAtEveryStoreCall(t, func(t *testing.T) *bootWorld { return newBootWorld(t) }, func(t *testing.T, w *bootWorld) {
			if !w.suiteExists() {
				t.Error("the Suite does not exist")
			}
			if _, found := w.proposal(1); found {
				t.Error("a proposal exists: the bootstrap job proposes nothing")
			}
			w.requireOutbox()
		})
	})
	t.Run("recording the assessment and promoting", func(t *testing.T) {
		crashAtEveryStoreCall(t, func(t *testing.T) *bootWorld { return approvedWorld(t) }, func(t *testing.T, w *bootWorld) {
			w.requireOutbox(promotedKey, summaryKey)
			if version, found := w.canonical(); !found || version != "V1" {
				t.Errorf("canonical = %q (found %v), want V1", version, found)
			}
			promoted := 0
			for _, message := range w.suites.messages {
				if message.Key == promotedKey {
					promoted++
				}
			}
			if promoted != 1 {
				t.Errorf("%d promoted messages were committed, want 1", promoted)
			}
		})
	})
}

func TestBootstrapErrorsNeverCarryTheDefaultBranchName(t *testing.T) {
	const hostile = "HOSTILE-REPOSITORY-TEXT"
	w := newBootWorld(t)
	w.reader.repo(bootRepo).info.DefaultBranch = hostile
	w.suites.createErr = func(target contract.IntegrationTargetID) error {
		return fmt.Errorf("%w: targeting %q", errBootStore, target)
	}

	err := w.run()

	if err == nil {
		t.Fatal("the run did not fail")
	}
	if strings.Contains(err.Error(), hostile) {
		t.Fatalf("the error carries repository text: %v", err)
	}
	if !errors.Is(err, errBootStore) {
		t.Fatalf("the cause was lost: %v", err)
	}
}
