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

// selection is what the canonical declaration selects: its include less its exclude.
func (p executionPolicy) selection() domain.Selection {
	return domain.Selection{Include: p.scope.Include(), Exclude: p.scope.Exclude()}
}

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
	failed bool // the policy could not be read: no new success is decided while it cannot say whether the canonical run gates the check, and the wait rule applies
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
		x.failed = true
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

// cancelStale cancels the open jobs of a repository whose pull request is no longer open (closed) or whose head moved (head-moved), and, when the canonical of
// the repository failed its integrity check, all the others (obsolete): nothing is run against a canonical that cannot be trusted.
func (j *checksJob) cancelStale(ctx context.Context, repositoryID int64, pulls []githubsync.PullState, integrityFailed bool) error {
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
		var reason execution.Reason
		switch head, isOpen := heads[job.PullNumber]; {
		case !isOpen:
			reason = execution.ReasonClosed
		case head != job.HeadSHA:
			reason = execution.ReasonHeadMoved
		case integrityFailed:
			reason = execution.ReasonObsolete
		default:
			continue
		}
		if _, err := j.executions.Cancel(ctx, job.ID, reason, j.now()); err != nil {
			failures = append(failures, fmt.Errorf("cancel job %d: %w", job.ID, err))
		}
	}
	return errors.Join(failures...)
}

// cancelObsolete cancels the open jobs of the head, for the plans that the run decided, whose binding is no longer wanted (the governing manifest or the policy
// changed), so that the newest job of the head that is not cancelled is a wanted one.
func (j *checksJob) cancelObsolete(ctx context.Context, s *pullState, wants []wantedJob, plans []execution.Plan) error {
	for _, plan := range plans {
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
// false for a check that says nothing of it (a wrong code, an invalid source, a head that could not be assessed): no canonical job is planned then.
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
// creates the jobs (docs/contracts/runtime.md, "What the checks job does for execution", steps 2 to 5). The canonical plan depends on whether the revision is
// ready, which a check that another rule decided (a wrong code, an invalid source) does not say: such a head wants no canonical job yet, and its open
// canonical jobs stay. The receipt plan needs only the revision and the canonical version it expects, so it is decided whenever the check names the revision.
func (j *checksJob) execute(ctx context.Context, s *pullState, x *executionRun, assessed checks.Result) (headJobs, error) {
	if !x.known {
		return headJobs{}, nil
	}
	both := []execution.Plan{execution.PlanCanonical, execution.PlanReceipt}
	fork := isFork(s.pull.HeadRepo, s.p.Repo)
	if fork || !x.policy.wants() {
		return headJobs{receiptDecided: !fork}, j.cancelObsolete(ctx, s, nil, both) // nothing is wanted: a Receipt check that was published is withdrawn
	}
	ready, known := executionBasis(assessed)
	receiptWanted := x.policy.receiptOn() && assessed.Reference != ""
	if !known && !receiptWanted {
		return headJobs{receiptDecided: !x.policy.receiptOn()}, nil // a receipt level that is off decides the receipt whatever the check says of the head
	}
	var bound revisionBinding
	if assessed.Reference != "" && ((known && ready) || receiptWanted) {
		var current bool
		var err error
		if bound, current, err = j.bindingOf(ctx, s, assessed.Reference); err != nil || !current {
			return headJobs{}, err
		}
	}
	var wants []wantedJob
	jobs := headJobs{reference: assessed.Reference, receiptDecided: true}
	plans := []execution.Plan{execution.PlanReceipt}
	if known {
		plans = both
		if x.policy.canonicalOn() {
			want, err := j.wantCanonical(ctx, s, x, assessed.Reference, ready, bound)
			if err != nil {
				return headJobs{}, err
			}
			wants, jobs.canonicalPlanned = append(wants, want), true
		}
	}
	if receiptWanted {
		want, applies, err := j.wantReceipt(ctx, s, x, bound)
		if err != nil {
			return headJobs{}, err
		}
		if applies {
			wants, jobs.receiptApplies = append(wants, want), true
		}
	}
	if err := j.cancelObsolete(ctx, s, wants, plans); err != nil {
		return headJobs{}, err
	}
	return j.readHeadJobs(ctx, s, bound, jobs)
}

// wantCanonical creates the canonical job of the head when none holds its binding: the governing manifest is the revision's once the head is ready, and the
// canonical's otherwise.
func (j *checksJob) wantCanonical(ctx context.Context, s *pullState, x *executionRun, reference string, ready bool, bound revisionBinding) (wantedJob, error) {
	spec := x.baseSpec(s, execution.PlanCanonical)
	spec.GoverningDigest, spec.GoverningSource = s.suite.manifest, domain.ManifestSource(s.suite.canonical)
	if ready {
		spec.GoverningDigest, spec.GoverningSource = bound.manifest, domain.ManifestSource(reference)
	}
	if err := j.canonicalJob(ctx, spec); err != nil {
		return wantedJob{}, err
	}
	return wantedOf(spec), nil
}

// wantReceipt finds or creates the receipt job of the head, with the manifest of the canonical version that the revision expects. applies is false when the
// receipt does not apply to the revision.
func (j *checksJob) wantReceipt(ctx context.Context, s *pullState, x *executionRun, bound revisionBinding) (want wantedJob, applies bool, err error) {
	spec := x.baseSpec(s, execution.PlanReceipt)
	spec.GoverningDigest, spec.GoverningSource = bound.base.Digest(), domain.ManifestSource(bound.version)
	if applies, err = j.receiptJob(ctx, s, x, spec, bound); err != nil || !applies {
		return wantedJob{}, false, err
	}
	return wantedOf(spec), true, nil
}

// headJobs are the jobs of a head that the canonical gate and the Receipt check read. A plan with no job reads as pending.
type headJobs struct {
	canonical, receipt       execution.Job
	hasCanonical, hasReceipt bool
	canonicalPlanned         bool   // a canonical job is wanted for the head, which the canonical gate reads
	receiptApplies           bool   // the receipt applies to the revision of the head: its job may not exist yet
	receiptDecided           bool   // the run decided whether the receipt applies: when it does not, a Receipt check that was published is withdrawn
	reference                string // the revision of the head: P<n>-R<m>, empty for none
}

// readHeadJobs reads the job of each wanted plan that the gates and the Receipt check read.
func (j *checksJob) readHeadJobs(ctx context.Context, s *pullState, bound revisionBinding, jobs headJobs) (headJobs, error) {
	var err error
	if jobs.canonicalPlanned {
		if jobs.canonical, jobs.hasCanonical, err = j.gateJob(ctx, s, execution.PlanCanonical, artifact.Digest{}); err != nil {
			return headJobs{}, err
		}
	}
	if jobs.receiptApplies {
		if jobs.receipt, jobs.hasReceipt, err = j.gateJob(ctx, s, execution.PlanReceipt, bound.base.Digest()); err != nil {
			return headJobs{}, err
		}
	}
	return jobs, nil
}

// gateJob is the job of a plan that the canonical gate, the Receipt check, the receipt gate of an approve and unblock read for the head of the pull request:
// the newest job of its pull request, head and plan that is not cancelled (docs/contracts/runtime.md, "The job a gate reads"). For the receipt, expecting is
// the manifest of the canonical version that the revision expects, and only a job it governs counts; the canonical plan passes the zero digest and takes any.
// A job created already final counts. found is false for none, which reads as pending.
func (j *checksJob) gateJob(ctx context.Context, s *pullState, plan execution.Plan, expecting artifact.Digest) (job execution.Job, found bool, err error) {
	return j.newestJob(ctx, s.p.RepositoryID, s.pull.Number, s.pull.HeadSHA, plan, func(candidate execution.Job) bool {
		return expecting.IsZero() || candidate.GoverningDigest == expecting
	})
}

// newestJob is the newest job of the head and plan that is not cancelled and that keep accepts.
func (j *checksJob) newestJob(ctx context.Context, repositoryID int64, pull int, head string, plan execution.Plan, keep func(execution.Job) bool) (execution.Job, bool, error) {
	jobs, err := j.executions.HeadJobs(ctx, repositoryID, pull, head, plan)
	if err != nil {
		return execution.Job{}, false, fmt.Errorf("read the %s jobs of the head: %w", plan, err)
	}
	for _, job := range slices.Backward(jobs) {
		if job.State != execution.StateCancelled && keep(job) {
			return job, true, nil
		}
	}
	return execution.Job{}, false, nil
}

// holder is the newest job of the head and plan that is not cancelled and runs under the binding of spec (its governing manifest and the canonical
// declaration), whatever merge base its key holds: a head's receipt job is looked up without reading its merge base again.
func (j *checksJob) holder(ctx context.Context, spec execution.Spec) (execution.Job, bool, error) {
	return j.newestJob(ctx, spec.RepositoryID, spec.PullNumber, spec.HeadSHA, spec.Plan, func(job execution.Job) bool {
		return job.GoverningDigest == spec.GoverningDigest && job.DeclarationDigest == spec.DeclarationDigest
	})
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

func (j *checksJob) enqueue(ctx context.Context, spec execution.Spec, final execution.Reason) error {
	if _, _, err := j.executions.Enqueue(ctx, execution.NewJob{Spec: spec, Unavailable: final, At: j.now()}); err != nil {
		return fmt.Errorf("create the %s job of the head: %w", spec.Plan, err)
	}
	return nil
}

// canonicalJob creates the canonical job of the head under spec unless one holds its binding: without an executor token it is created already unavailable.
func (j *checksJob) canonicalJob(ctx context.Context, spec execution.Spec) error {
	held, found, err := j.holder(ctx, spec)
	if err != nil || (found && !j.replaceable(held)) {
		return err
	}
	return j.enqueue(ctx, spec, j.unconfigured())
}

// receiptJob creates the receipt job of the head under spec unless one holds its binding. applies is false when the receipt does not apply to the revision.
// When it applies and there is no job yet because GitHub could not be read, the receipt reads as pending, and no job is made until it can be, or until
// EXECUTOR_GITHUB_WAIT has passed.
func (j *checksJob) receiptJob(ctx context.Context, s *pullState, x *executionRun, spec execution.Spec, bound revisionBinding) (applies bool, err error) {
	held, found, err := j.holder(ctx, spec)
	if err != nil {
		return false, err
	}
	if found && !j.replaceable(held) {
		return true, nil
	}
	if spec.ChangedPaths, spec.ChangedTotal, err = j.revisionChanges(ctx, bound, x.policy); err != nil || spec.ChangedTotal == 0 {
		return false, err
	}
	read, err := j.readReceipt(ctx, s, x)
	if err != nil || read.wait {
		return true, err
	}
	spec.UnapprovedPaths, spec.BaseSHA = read.unapproved, read.base
	if err := j.enqueue(ctx, spec, read.final); err != nil {
		return true, err // the clock stays: no job was created
	}
	delete(j.mergeFailures, checkPull{s.p.RepositoryID, s.pull.Number}) // a job is created for the head, whatever it is, and that ends the clock of its failures
	return true, nil
}

// revisionChanges lists the files that the receipt evaluates (docs/contracts/runtime.md, "Applicable and not applicable"): the files of the revision that the
// canonical include matches and its exclude does not, and that the manifest of the canonical version it expects lacks or holds with other bytes. It is
// EvaluatedFiles of the domain, which the receipt gate of the commands job uses too. At most maxExecutionPaths are kept, in path order, and how many there are.
// None means the receipt is not applicable.
func (j *checksJob) revisionChanges(ctx context.Context, b revisionBinding, policy executionPolicy) (paths []string, total int, err error) {
	revision, err := j.revisionManifest(ctx, b.manifest)
	if err != nil {
		return nil, 0, err
	}
	files, err := domain.EvaluatedFiles(revision, b.base, policy.selection())
	if err != nil {
		return nil, 0, &opaque{what: "list the files of the receipt", cause: err}
	}
	return files[:min(len(files), maxExecutionPaths)], len(files), nil
}

// revisionManifest reads the manifest of a revision from the content store and checks that it is the one the digest names. The store never deletes, so a manifest
// that it lacks is an impossible state, and so is one that does not parse: both fail the step with an error that says nothing of the store.
func (j *checksJob) revisionManifest(ctx context.Context, digest artifact.Digest) (artifact.Manifest, error) {
	raw, err := j.contents.Read(ctx, digest)
	if err != nil {
		return artifact.Manifest{}, &opaque{what: "read the manifest of the revision", cause: err}
	}
	manifest, err := artifact.ParseManifest(raw)
	if err == nil && manifest.Digest() != digest { // the bytes of another manifest than the one the revision names
		err = fmt.Errorf("%w: the entries are those of %s", artifact.ErrInvalidEncoding, manifest.Digest())
	}
	if err != nil {
		return artifact.Manifest{}, &opaque{what: "parse the manifest of the revision", cause: err}
	}
	return manifest, nil
}

// receiptRead is what the checks job read from GitHub to create the receipt job: the files not evaluated and the merge base. final is the reason
// the job is created already unavailable, and wait says that GitHub could not be read and no job is made yet.
type receiptRead struct {
	unapproved []string
	base       string
	final      execution.Reason
	wait       bool
}

// readReceipt reads what the receipt job of the head is created with. Without an executor token no merge base is read, and the job is created already
// unavailable whatever GitHub says of the files that are not evaluated (they are display only, and a head must always find a job). A merge base that GitHub
// does not have (404) makes the job unavailable for no-merge-base; any other failure waits, and once EXECUTOR_GITHUB_WAIT has passed since the first
// failure for the head the job is created unavailable for github-unavailable, with none of the two (the first failure is kept in memory, so a restart
// starts the clock again).
func (j *checksJob) readReceipt(ctx context.Context, s *pullState, x *executionRun) (receiptRead, error) {
	unapproved, readable, err := j.unapprovedPaths(ctx, s, x)
	switch {
	case err != nil:
		return receiptRead{}, err
	case !j.executorConfigured:
		return receiptRead{unapproved: unapproved, final: execution.ReasonNotConfigured}, nil
	case !readable:
		return j.receiptWaits(s), nil
	}
	base, err := j.reader.MergeBase(ctx, s.p.Repo, s.pull.BaseSHA, s.pull.HeadSHA)
	switch {
	case err == nil:
		return receiptRead{unapproved: unapproved, base: base}, nil
	case errors.Is(err, rest.ErrNotFound):
		return receiptRead{unapproved: unapproved, final: execution.ReasonNoMergeBase}, nil
	}
	if err := j.githubFailure(ctx, s, githubRead(err)); err != nil {
		return receiptRead{}, err
	}
	return j.receiptWaits(s), nil
}

// githubFailure says what a failed read for the receipt is: nil for GitHub, which could not be read (it is logged, with no text GitHub sent), and the error
// when the context ended or the failure is not GitHub's.
func (j *checksJob) githubFailure(ctx context.Context, s *pullState, err error) error {
	if cancelled := ctx.Err(); cancelled != nil {
		return cancelled
	}
	if !errors.Is(err, errGitHubRead) && !invalidSource(err) {
		return &opaque{what: "read the receipt of the head", cause: err}
	}
	logReadFailure(ctx, j.logger, "github checks execution read failed", s.p.Repo, err)
	return nil
}

// receiptWaits is what the receipt does while GitHub cannot be read: it waits, and once EXECUTOR_GITHUB_WAIT has passed since the first failure for the head
// its job is created already unavailable for github-unavailable.
func (j *checksJob) receiptWaits(s *pullState) receiptRead {
	if j.firstFailure(s).Add(j.executor.GitHubWait).After(j.now()) {
		return receiptRead{wait: true}
	}
	return receiptRead{final: execution.ReasonGitHubUnavailable}
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

// unapprovedPaths lists, with readable false when GitHub could not be read, the files that the pull request adds or changes, that the include of the head's declaration matches and that the include
// of the canonical's does not: "not evaluated: pattern not approved yet". It is display only, so at most maxExecutionPaths are kept, in path order.
// The head's declaration is read only when it is not the canonical's (a pull request that does not touch it has none to list).
func (j *checksJob) unapprovedPaths(ctx context.Context, s *pullState, x *executionRun) (paths []string, readable bool, err error) {
	paths, err = j.readUnapproved(ctx, s, x)
	if err != nil {
		return nil, false, j.githubFailure(ctx, s, err) // nil: GitHub could not be read
	}
	return paths, true, nil
}

func (j *checksJob) readUnapproved(ctx context.Context, s *pullState, x *executionRun) ([]string, error) {
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
	return unapprovedFiles(changed, domain.Selection{Include: declaration.Include(), Exclude: declaration.Exclude()}, x.policy.selection())
}

// unapprovedFiles is the files of changed that were not removed, that the head's declaration selects and that the canonical's does not ("not evaluated: pattern
// not approved yet"), sorted and cut at maxExecutionPaths. The files of a pull request that the head holds are valid paths, so a path that no selection can
// match is an impossible state, and an error.
func unapprovedFiles(changed []github.PullFile, head, approved domain.Selection) ([]string, error) {
	var paths []string
	for _, f := range changed {
		if f.Status == "removed" || f.Status == "unchanged" {
			continue
		}
		selected, err := head.Matches(f.Path)
		if err != nil {
			return nil, &opaque{what: "match a file of the pull request", cause: err}
		}
		if !selected {
			continue
		}
		covered, err := approved.Matches(f.Path)
		if err != nil {
			return nil, &opaque{what: "match a file of the pull request", cause: err}
		}
		if !covered {
			paths = append(paths, f.Path)
		}
	}
	slices.Sort(paths)
	return paths[:min(len(paths), maxExecutionPaths)], nil
}
