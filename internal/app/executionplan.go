package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// The checks job is the planner of execution (docs/contracts/runtime.md, "What the checks job does for execution"): it reads the policy of the
// canonical, decides which jobs each open pull request wants, creates them, and cancels the open jobs that nothing wants any more.

// maxExecutionPaths is how many paths a job lists: the files its revision changes and the files that are not evaluated.
const maxExecutionPaths = 100

// executionPolicy is the execution policy of a project, read from its canonical declaration (the zero value is no policy: both levels off).
type executionPolicy struct {
	profile     string
	canonical   domain.CanonicalLevel
	receipt     domain.ReceiptLevel
	declaration artifact.Digest   // of the canonical declaration, which every job key holds, so that a changed policy is a new job
	scope       scope.Declaration // its include and exclude, which decide whether a receipt applies
}

func (p executionPolicy) canonicalOn() bool { return p.canonical > domain.CanonicalLevelOff }
func (p executionPolicy) receiptOn() bool   { return p.receipt > domain.ReceiptLevelOff }
func (p executionPolicy) wants() bool       { return p.canonicalOn() || p.receiptOn() }

// policy reads the execution policy from the declaration of the canonical (docs/contracts/runtime.md, "The effective policy"). The canonical
// declaration governs every pull request, the one that changes it included.
func (j *checksJob) policy(ctx context.Context, p owner.ProjectPolicy) (executionPolicy, error) {
	raw, canonical, err := canonicalDeclaration(ctx, j.suites, j.contents, owner.ProjectID(p.RepositoryID))
	if errors.Is(err, governance.ErrNoCanonicalDeclaration) {
		return executionPolicy{}, nil
	}
	if err != nil {
		return executionPolicy{}, fmt.Errorf("read the canonical declaration: %w", err)
	}
	declaration := canonical.Declaration()
	execution, found := declaration.Execution()
	if !found {
		return executionPolicy{}, nil
	}
	canonicalLevel, err := domain.ParseCanonicalLevel(execution.Canonical)
	if err != nil {
		return executionPolicy{}, fmt.Errorf("the canonical declaration: %w", err) // ParseDeclaration validated it: an impossible state
	}
	receiptLevel, err := domain.ParseReceiptLevel(execution.Receipt)
	if err != nil {
		return executionPolicy{}, fmt.Errorf("the canonical declaration: %w", err)
	}
	return executionPolicy{profile: execution.Profile, canonical: canonicalLevel, receipt: receiptLevel, declaration: artifact.Hash(raw), scope: declaration}, nil
}

// wantedJob is the binding a job runs against, which says whether a job is still the wanted one: its plan, the governing manifest and the
// canonical declaration. The head is the pull request's, and the merge base of a receipt is not part of it (see holder).
type wantedJob struct {
	plan                   execution.Plan
	governing, declaration artifact.Digest
}

func wantedOf(s execution.Spec) wantedJob {
	return wantedJob{plan: s.Plan, governing: s.GoverningDigest, declaration: s.DeclarationDigest}
}

func (w wantedJob) holds(job execution.Job) bool {
	return job.Plan == w.plan && job.GoverningDigest == w.governing && job.DeclarationDigest == w.declaration
}

// executionRun is what one run of the checks job knows about the execution of one project: its policy, read once.
type executionRun struct {
	policy executionPolicy
	known  bool // the policy was read: a run that could not read it plans nothing and cancels nothing as unwanted
}

// executionRunOf reads the policy of the project, once for the run. Before the first canonical, and for a canonical that failed its integrity
// verification, nothing is planned. A policy that cannot be read is the error, and the run decides nothing.
func (j *checksJob) executionRunOf(ctx context.Context, p owner.ProjectPolicy, suite suiteView) (*executionRun, error) {
	x := &executionRun{}
	if suite.canonical == "" || suite.integrityFailed {
		return x, nil
	}
	policy, err := j.policy(ctx, p)
	if err != nil {
		return x, err
	}
	x.policy, x.known = policy, true
	return x, nil
}

// sweep applies the expiry of leases, the no-executor wait and the GitHub bound to every open job (docs/contracts/runtime.md, "Job lifecycle").
// It runs first, so the decisions of the same run already read what it changed.
func (j *checksJob) sweep(ctx context.Context) error {
	changed, err := j.executions.Expire(ctx, execution.ExpireParams{Now: j.now(), LastSeenAt: j.liveness.LastSeen(), LeaseWait: j.executor.LeaseWait, GitHubWait: j.executor.GitHubWait})
	if err != nil {
		return fmt.Errorf("sweep the execution jobs: %w", err)
	}
	if changed > 0 {
		j.logger.InfoContext(ctx, "github checks execution jobs swept", "jobs", changed)
	}
	return nil
}

// cancelStale cancels the open jobs of a repository whose pull request is no longer open (closed) or whose head moved (head-moved).
func (j *checksJob) cancelStale(ctx context.Context, repositoryID int64, pulls []githubsync.PullState) error {
	open, err := j.executions.OpenJobs(ctx, repositoryID)
	if err != nil {
		return fmt.Errorf("read the open execution jobs: %w", err)
	}
	heads := map[int]string{}
	for _, pull := range pulls {
		if pull.NotOpenSince.IsZero() {
			heads[pull.Number] = pull.HeadSHA
		}
	}
	var failures []error
	for _, job := range open {
		head, isOpen := heads[job.PullNumber]
		reason := execution.ReasonClosed
		if isOpen {
			reason = execution.ReasonHeadMoved
		}
		if isOpen && head == job.HeadSHA {
			continue
		}
		if _, err := j.executions.Cancel(ctx, job.ID, reason, j.now()); err != nil {
			failures = append(failures, fmt.Errorf("cancel job %d: %w", job.ID, err))
		}
	}
	return errors.Join(failures...)
}

// cancelObsolete cancels the open jobs of the head whose binding is no longer wanted (the governing manifest or the policy changed), so that
// the newest job of the head that is not cancelled is a wanted one.
func (j *checksJob) cancelObsolete(ctx context.Context, s *pullState, wants []wantedJob) error {
	for _, plan := range []execution.Plan{execution.PlanCanonical, execution.PlanReceipt} {
		jobs, err := j.executions.HeadJobs(ctx, s.p.RepositoryID, s.pull.Number, s.pull.HeadSHA, plan)
		if err != nil {
			return fmt.Errorf("read the %s jobs of the head: %w", plan, err)
		}
		for _, job := range jobs {
			if job.State.Final() || slices.ContainsFunc(wants, func(w wantedJob) bool { return w.holds(job) }) {
				continue
			}
			if _, err := j.executions.Cancel(ctx, job.ID, execution.ReasonObsolete, j.now()); err != nil {
				return fmt.Errorf("cancel job %d: %w", job.ID, err)
			}
		}
	}
	return nil
}

// executionBasis says whether the head stands on a revision that is ready, as its check was decided before another rule held it back: ready
// when the check is ready, or is a success that the runner block or the canonical gate held back for a head that is not unchanged. ok is
// false for a check that says nothing of it (a wrong code, an invalid source, a head that could not be assessed): nothing is planned then.
func executionBasis(r checks.Result) (ready, ok bool) {
	switch r.Reason {
	case reasonReady:
		return true, true
	case reasonUnchanged, reasonApprovalMissing, reasonNotCurrent:
		return false, true
	case reasonRunnerUncovered:
		return r.Change != changeCodes[inventory.Unchanged] && r.Reference != "", true
	}
	return false, false
}

// revisionBinding is what the revision of a head binds: the canonical version it expected, that version's manifest, and its own manifest.
type revisionBinding struct {
	version  contract.SuiteVersionID
	base     artifact.Manifest
	manifest artifact.Digest
}

// bindingOf reads the binding of the revision a check names. current is false when the revision expects another canonical than the Suite's:
// the run read the Suite before the head was assessed, so the next run decides.
func (j *checksJob) bindingOf(ctx context.Context, s *pullState, reference string) (b revisionBinding, current bool, err error) {
	proposal, revision, ok := commandtext.ParseReference(reference)
	if !ok {
		return revisionBinding{}, false, fmt.Errorf("the check names the reference %q, which is not P<n>-R<m>", reference)
	}
	b.version, b.base, b.manifest, err = (&changeReports{suites: j.suites}).bindingOf(ctx, revisionReference(s.p.RepositoryID, proposal, revision))
	if err != nil {
		return revisionBinding{}, false, fmt.Errorf("read the binding of %s: %w", reference, err)
	}
	return b, b.version == s.suite.canonical, nil
}

// baseSpec is the identity of a job that every plan shares.
func (x *executionRun) baseSpec(s *pullState, plan execution.Plan) execution.Spec {
	return execution.Spec{RepositoryID: s.p.RepositoryID, PullNumber: s.pull.Number, Plan: plan, Profile: x.policy.profile, HeadSHA: s.pull.HeadSHA,
		DeclarationDigest: x.policy.declaration, CanonicalLevel: x.policy.canonical, ReceiptLevel: x.policy.receipt}
}

// execute decides what execution wants of the head of a pull request, whose check was decided as assessed before another rule held it back, and
// creates the jobs (docs/contracts/runtime.md, "What the checks job does for execution", steps 2 to 5). A head whose check says nothing of its
// revision wants nothing yet, and its open jobs stay.
func (j *checksJob) execute(ctx context.Context, s *pullState, x *executionRun, assessed checks.Result) error {
	if !x.known {
		return nil
	}
	if isFork(s.pull.HeadRepo, s.p.Repo) || !x.policy.wants() {
		return j.cancelObsolete(ctx, s, nil)
	}
	ready, known := executionBasis(assessed)
	if !known {
		return nil
	}
	var bound revisionBinding
	if assessed.Reference != "" && (ready || x.policy.receiptOn()) {
		var current bool
		var err error
		if bound, current, err = j.bindingOf(ctx, s, assessed.Reference); err != nil || !current {
			return err
		}
	}
	var wants []wantedJob
	if x.policy.canonicalOn() {
		spec := x.baseSpec(s, execution.PlanCanonical)
		spec.GoverningDigest, spec.GoverningSource = s.suite.manifest, domain.ManifestSource(s.suite.canonical)
		if ready {
			spec.GoverningDigest, spec.GoverningSource = bound.manifest, domain.ManifestSource(assessed.Reference)
		}
		if _, err := j.canonicalJob(ctx, spec); err != nil {
			return err
		}
		wants = append(wants, wantedOf(spec))
	}
	if x.policy.receiptOn() && assessed.Reference != "" {
		spec := x.baseSpec(s, execution.PlanReceipt)
		spec.GoverningDigest, spec.GoverningSource = bound.base.Digest(), domain.ManifestSource(bound.version)
		_, _, applies, err := j.receiptJob(ctx, s, x, spec, bound)
		if err != nil {
			return err
		}
		if applies {
			wants = append(wants, wantedOf(spec))
		}
	}
	return j.cancelObsolete(ctx, s, wants)
}

// holder is the latest job of the head and plan that is not cancelled and runs under the binding of spec (its governing manifest and the
// canonical declaration), whatever merge base its key holds: a head's receipt job is looked up without reading its merge base again.
func (j *checksJob) holder(ctx context.Context, spec execution.Spec) (execution.Job, bool, error) {
	jobs, err := j.executions.HeadJobs(ctx, spec.RepositoryID, spec.PullNumber, spec.HeadSHA, spec.Plan)
	if err != nil {
		return execution.Job{}, false, fmt.Errorf("read the %s jobs of the head: %w", spec.Plan, err)
	}
	for _, job := range slices.Backward(jobs) {
		if job.State != execution.StateCancelled && job.GoverningDigest == spec.GoverningDigest && job.DeclarationDigest == spec.DeclarationDigest {
			return job, true, nil
		}
	}
	return execution.Job{}, false, nil
}

// replaceable reports whether a fresh job may replace the job that holds a binding: only one that is unavailable for executor-not-configured
// once an executor token is configured, or for no-executor once the last real executor request (a lease request or a heartbeat that
// arrived: the startup clock is none) is later than its end ("Recovery"). Both times are compared at the precision that is stored, because a
// stored time has no monotonic reading.
func (j *checksJob) replaceable(job execution.Job) bool {
	if job.State != execution.StateUnavailable {
		return false
	}
	switch job.Reason {
	case execution.ReasonNotConfigured:
		return j.executorConfigured
	case execution.ReasonNoExecutor:
		last, requested := j.liveness.LastRequest()
		return requested && execution.Stored(last).After(job.FinishedAt)
	}
	return false
}

// unconfigured is the reason a job is created final without an executor token, and none with one.
func (j *checksJob) unconfigured() execution.Reason {
	if j.executorConfigured {
		return ""
	}
	return execution.ReasonNotConfigured
}

func (j *checksJob) enqueue(ctx context.Context, spec execution.Spec, final execution.Reason) (execution.Job, error) {
	job, _, err := j.executions.Enqueue(ctx, execution.NewJob{Spec: spec, Unavailable: final, At: j.now()})
	if err != nil {
		return execution.Job{}, fmt.Errorf("create the %s job of the head: %w", spec.Plan, err)
	}
	return job, nil
}

// canonicalJob finds or creates the canonical job of the head under spec: without an executor token it is created already unavailable.
func (j *checksJob) canonicalJob(ctx context.Context, spec execution.Spec) (execution.Job, error) {
	held, found, err := j.holder(ctx, spec)
	if err != nil || (found && !j.replaceable(held)) {
		return held, err
	}
	return j.enqueue(ctx, spec, j.unconfigured())
}

// receiptJob finds or creates the receipt job of the head under spec. applies is false when the receipt does not apply to the revision. found
// is false when it applies and there is no job yet, because GitHub could not be read: the receipt reads as pending, and no job is made until it
// can be, or until EXECUTOR_GITHUB_WAIT has passed.
func (j *checksJob) receiptJob(ctx context.Context, s *pullState, x *executionRun, spec execution.Spec, bound revisionBinding) (job execution.Job, found, applies bool, err error) {
	held, found, err := j.holder(ctx, spec)
	if err != nil {
		return execution.Job{}, false, false, err
	}
	if found && !j.replaceable(held) {
		return held, true, true, nil
	}
	if spec.ChangedPaths, spec.ChangedTotal, err = j.revisionChanges(ctx, bound, x.policy); err != nil || spec.ChangedTotal == 0 {
		return execution.Job{}, false, false, err
	}
	read, err := j.readReceipt(ctx, s, x)
	if err != nil || read.wait {
		return execution.Job{}, false, true, err
	}
	spec.UnapprovedPaths, spec.BaseSHA = read.unapproved, read.base
	job, err = j.enqueue(ctx, spec, read.final)
	return job, err == nil, true, err
}

// revisionChanges lists the files that the canonical include matches (and its exclude does not) and the revision adds or changes against the
// manifest of the canonical it expects: at most maxExecutionPaths, in path order, and how many there are. The service decides it from the two
// manifests, with no Go parsing. None means the receipt is not applicable.
func (j *checksJob) revisionChanges(ctx context.Context, b revisionBinding, policy executionPolicy) (paths []string, total int, err error) {
	raw, err := j.contents.Read(ctx, b.manifest)
	if err != nil {
		return nil, 0, fmt.Errorf("read the manifest %s of the revision: %w", b.manifest, err)
	}
	proposed, err := artifact.ParseManifest(raw)
	if err == nil && proposed.Digest() != b.manifest { // the bytes of another manifest than the one the revision names
		err = fmt.Errorf("%w: the entries are those of %s", artifact.ErrInvalidEncoding, proposed.Digest())
	}
	if err != nil {
		return nil, 0, fmt.Errorf("the manifest %s of the revision: %w", b.manifest, err)
	}
	before := map[string]artifact.Digest{}
	for _, e := range b.base.Entries() {
		before[e.Path] = e.Content
	}
	include, exclude := policy.scope.Include(), policy.scope.Exclude()
	for _, e := range proposed.Entries() {
		if content, found := before[e.Path]; found && content == e.Content {
			continue
		}
		if !matchesAny(include, e.Path) || matchesAny(exclude, e.Path) {
			continue
		}
		total++
		if len(paths) < maxExecutionPaths {
			paths = append(paths, e.Path)
		}
	}
	return paths, total, nil
}

// matchesAny reports whether a pattern matches the path. A path that is no valid path (artifact.ErrInvalidPath) matches none: no manifest holds it.
func matchesAny(patterns []scope.Pattern, path string) bool {
	return slices.ContainsFunc(patterns, func(p scope.Pattern) bool {
		matched, err := p.Match(path)
		return err == nil && matched
	})
}

// receiptRead is what the checks job read from GitHub to create the receipt job: the files not evaluated and the merge base. final is the reason
// the job is created already unavailable, and wait says that GitHub could not be read and no job is made yet.
type receiptRead struct {
	unapproved []string
	base       string
	final      execution.Reason
	wait       bool
}

// readReceipt reads what the receipt job of the head is created with. Without an executor token no merge base is read. A merge base that GitHub
// does not have (404) makes the job unavailable for no-merge-base; any other failure waits, and once EXECUTOR_GITHUB_WAIT has passed since the
// first failure for the head the job is created unavailable for github-unavailable, with none of the two (the first failure is kept in memory,
// so a restart starts the clock again).
func (j *checksJob) readReceipt(ctx context.Context, s *pullState, x *executionRun) (receiptRead, error) {
	unapproved, err := j.unapprovedPaths(ctx, s, x)
	if err == nil && !j.executorConfigured {
		return receiptRead{unapproved: unapproved, final: execution.ReasonNotConfigured}, nil
	}
	var base string
	if err == nil {
		switch base, err = j.reader.MergeBase(ctx, s.p.Repo, s.pull.BaseSHA, s.pull.HeadSHA); {
		case err == nil:
			delete(j.mergeFailures, checkPull{s.p.RepositoryID, s.pull.Number})
			return receiptRead{unapproved: unapproved, base: base}, nil
		case errors.Is(err, rest.ErrNotFound):
			return receiptRead{unapproved: unapproved, final: execution.ReasonNoMergeBase}, nil
		}
		err = githubRead(err)
	}
	if cancelled := ctx.Err(); cancelled != nil {
		return receiptRead{}, cancelled
	}
	if !errors.Is(err, errGitHubRead) && !invalidSource(err) {
		return receiptRead{}, &opaque{what: "read the receipt of the head", cause: err}
	}
	logReadFailure(ctx, j.logger, "github checks execution read failed", s.p.Repo, err)
	if j.firstFailure(s).Add(j.executor.GitHubWait).After(j.now()) {
		return receiptRead{wait: true}, nil
	}
	return receiptRead{final: execution.ReasonGitHubUnavailable}, nil
}

// mergeFailure is the first failed read of the receipt of a head, which EXECUTOR_GITHUB_WAIT is counted from.
type mergeFailure struct {
	head  string
	since time.Time
}

// firstFailure is the time of the first failed read for the head of the pull request: now, when the head has none yet.
func (j *checksJob) firstFailure(s *pullState) time.Time {
	key := checkPull{s.p.RepositoryID, s.pull.Number}
	if known, found := j.mergeFailures[key]; found && known.head == s.pull.HeadSHA {
		return known.since
	}
	if j.mergeFailures == nil {
		j.mergeFailures = map[checkPull]mergeFailure{}
	}
	j.mergeFailures[key] = mergeFailure{head: s.pull.HeadSHA, since: j.now()}
	return j.now()
}

// unapprovedPaths lists the files that the pull request adds or changes, that the include of the head's declaration matches and that the include
// of the canonical's does not: "not evaluated: pattern not approved yet". It is display only, so at most maxExecutionPaths are kept, in path order.
// The head's declaration is read only when it is not the canonical's (a pull request that does not touch it has none to list).
func (j *checksJob) unapprovedPaths(ctx context.Context, s *pullState, x *executionRun) ([]string, error) {
	read, err := j.reader.Tree(ctx, s.p.Repo, s.pull.HeadSHA)
	if err != nil {
		return nil, githubRead(err)
	}
	head := &githubTree{tree: read, files: map[string][]byte{}}
	entries, err := inventory.ListEntries(ctx, head)
	if err != nil {
		return nil, err
	}
	if !hasDeclaration(entries) { // no include: the head's scope is the protected defaults
		return nil, nil
	}
	raw, err := head.Read(ctx, scope.DeclarationPath)
	if err != nil {
		return nil, err
	}
	if artifact.Hash(raw) == x.policy.declaration {
		return nil, nil
	}
	declaration, err := scope.ParseDeclaration(raw)
	if err != nil {
		return nil, err
	}
	changed, err := j.reader.PullFiles(ctx, s.p.Repo, s.pull.Number)
	if err != nil {
		return nil, githubRead(err)
	}
	return unapprovedFiles(changed, declaration.Include(), x.policy.scope.Include()), nil
}

// unapprovedFiles is the files of changed that were not removed and that include matches and approved does not, sorted and cut at maxExecutionPaths.
func unapprovedFiles(changed []github.PullFile, include, approved []scope.Pattern) []string {
	var paths []string
	for _, f := range changed {
		if f.Status != "removed" && f.Status != "unchanged" && matchesAny(include, f.Path) && !matchesAny(approved, f.Path) {
			paths = append(paths, f.Path)
		}
	}
	slices.Sort(paths)
	return paths[:min(len(paths), maxExecutionPaths)]
}
