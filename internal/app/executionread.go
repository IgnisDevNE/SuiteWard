package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// executionReader reads what a gate reads of the execution of a head (docs/contracts/runtime.md, "The job a gate reads"). The checks job, the commands job and the
// receipt page read the job of a plan, its counts and the files the receipt evaluates through it, so that each rule is written once.
type executionReader struct {
	executions execution.Store
	contents   governance.ContentReader
	suites     governance.UnitOfWork                // the units of work that read a revision and the canonical it expects: only receiptView needs them
	report     func(what string, cause error) error // how a failed read of the store is reported: each job keeps the class of error it has always returned
}

// reads is the reader of the checks job, whose errors say what failed and why.
func (j *checksJob) reads() executionReader {
	return executionReader{executions: j.executions, contents: j.contents, suites: j.suites, report: wrapRead}
}

// reads is the reader of the commands job, whose errors say only what failed.
func (j *commandsJob) reads() executionReader {
	return executionReader{executions: j.executions, contents: j.contents, suites: j.suites, report: opaqueRead}
}

func opaqueRead(what string, cause error) error { return &opaque{what: what, cause: cause} }

func wrapRead(what string, cause error) error { return fmt.Errorf("%s: %w", what, cause) }

// newestJob is the newest job of the head and plan that is not cancelled and that keep accepts. A job created already final counts: it is the job of its plan until
// a fresh one replaces it. found is false for none.
func (r executionReader) newestJob(ctx context.Context, repositoryID int64, pull int, head string, plan execution.Plan, keep func(execution.Job) bool) (execution.Job, bool, error) {
	jobs, err := r.executions.HeadJobs(ctx, repositoryID, pull, head, plan) // oldest first
	if err != nil {
		return execution.Job{}, false, r.report(fmt.Sprintf("read the %s jobs of the head", plan), err)
	}
	for _, job := range slices.Backward(jobs) {
		if job.State != execution.StateCancelled && keep(job) {
			return job, true, nil
		}
	}
	return execution.Job{}, false, nil
}

// gateJob is the job of a plan that every gate reads for the head of a pull request: the newest job of its pull request, head and plan that is not cancelled. For
// the receipt, governing is the manifest of the canonical version that the revision expects, and only a job it governs counts (a revision whose canonical moved
// reads the jobs of the one it expects, so it may read none until the checks job rebinds the head); the canonical plan passes the zero digest and takes any.
// A receipt with the zero digest is an error, never a wildcard: a baseline revision expects no canonical, so its receipt is not applicable and the caller asks
// before. found is false for none, which reads as pending.
func (r executionReader) gateJob(ctx context.Context, repositoryID int64, pull int, head string, plan execution.Plan, governing artifact.Digest) (execution.Job, bool, error) {
	if plan == execution.PlanReceipt && governing.IsZero() {
		return execution.Job{}, false, r.report("read the receipt jobs of the head", errors.New("no manifest of an expected canonical version is given"))
	}
	return r.newestJob(ctx, repositoryID, pull, head, plan, func(job execution.Job) bool { return governing.IsZero() || job.GoverningDigest == governing })
}

// runOf is the status of the run of a job and the state that an approval records for it: no job reads as pending, as does one that is pending or leased.
func (r executionReader) runOf(job execution.Job, found bool) (domain.RunStatus, execution.RunState, error) {
	switch {
	case !found:
		return domain.RunPending, execution.RunNone, nil
	case job.State == execution.StatePending || job.State == execution.StateLeased:
		return domain.RunPending, execution.RunPending, nil
	case job.State == execution.StateUnavailable:
		return domain.RunUnavailable, execution.RunUnavailable, nil
	case job.State == execution.StateComplete:
		return domain.RunComplete, execution.RunComplete, nil
	}
	return 0, "", r.report(fmt.Sprintf("read the %s job %d", job.Plan, job.ID), fmt.Errorf("it is %s: only a job that is not cancelled is read", job.State))
}

// receiptRun is the receipt run of a head as a gate reads it: its job (the zero job for none), its status and, for a complete one, the tests by state, the changed
// test files that do not parse (each a blocking failure) and the marker lines it saw.
type receiptRun struct {
	job     execution.Job
	status  domain.RunStatus
	state   execution.RunState // the state an approval records
	counts  domain.ReceiptCounts
	markers int
}

// receipt reads the receipt run of the job that gateJob found.
func (r executionReader) receipt(ctx context.Context, job execution.Job, found bool) (receiptRun, error) {
	status, state, err := r.runOf(job, found)
	if err != nil {
		return receiptRun{}, err
	}
	run := receiptRun{job: job, status: status, state: state}
	if status != domain.RunComplete {
		return run, nil
	}
	states, err := readStates(ctx, r, job.ID, receiptStates)
	if err != nil {
		return receiptRun{}, err
	}
	if run.counts, err = domain.CountReceipt(states); err != nil {
		return receiptRun{}, r.report(fmt.Sprintf("count the tests of job %d", job.ID), err)
	}
	result, stored, err := r.executions.Result(ctx, job.ID)
	if err != nil {
		return receiptRun{}, r.report(fmt.Sprintf("read the result of job %d", job.ID), err)
	}
	if !stored { // a complete job always has its results row
		return receiptRun{}, r.report(fmt.Sprintf("read the result of job %d", job.ID), errors.New("a complete job has no results row"))
	}
	run.counts.Unparsed, run.markers = len(result.Unparsed), len(result.Markers)
	return run, nil
}

// canonicalRun is the canonical run of a head as a gate reads it: its status and, for a complete one, the tests by state.
type canonicalRun struct {
	status domain.RunStatus
	counts domain.CanonicalCounts
}

// canonical reads the canonical run of the job that gateJob found.
func (r executionReader) canonical(ctx context.Context, job execution.Job, found bool) (canonicalRun, error) {
	status, _, err := r.runOf(job, found)
	if err != nil {
		return canonicalRun{}, err
	}
	run := canonicalRun{status: status}
	if status != domain.RunComplete {
		return run, nil
	}
	states, err := readStates(ctx, r, job.ID, canonicalStates)
	if err != nil {
		return canonicalRun{}, err
	}
	if run.counts, err = domain.CountCanonical(states); err != nil {
		return canonicalRun{}, r.report(fmt.Sprintf("count the tests of job %d", job.ID), err)
	}
	return run, nil
}

// receiptStates and canonicalStates are the states of a stored test, which the store keeps in lower case, by name.
var (
	receiptStates   = statesByName(domain.ReceiptExcluded, domain.ReceiptInert)
	canonicalStates = statesByName(domain.CanonicalPass, domain.CanonicalExcluded)
)

func statesByName[S interface {
	~uint8
	String() string
}](first, last S) map[string]S {
	states := map[string]S{}
	for s := first; s <= last; s++ {
		states[strings.ToLower(s.String())] = s
	}
	return states
}

// readStates reads the tests of a complete job and maps their stored states to the states of its plan, which names says.
func readStates[S any](ctx context.Context, r executionReader, jobID int64, names map[string]S) ([]S, error) {
	tests, err := r.executions.Tests(ctx, jobID)
	if err != nil {
		return nil, r.report(fmt.Sprintf("read the tests of job %d", jobID), err)
	}
	states := make([]S, len(tests))
	for i, test := range tests {
		state, known := names[test.State]
		if !known {
			return nil, r.report(fmt.Sprintf("read the tests of job %d", jobID), fmt.Errorf("it stores the test state %q, which is no state of its plan", test.State))
		}
		states[i] = state
	}
	return states, nil
}

// evaluatedFiles lists the files the receipt of a revision evaluates (docs/contracts/runtime.md, "Applicable and not applicable"): those of the revision that the
// selection matches and that expected, the manifest of the canonical version the revision expects, lacks or holds with other bytes. It is EvaluatedFiles of the
// domain, which decides whether the receipt applies. A revision that expects no canonical, a baseline, has nothing to be added to or changed against: the callers that
// can see one ask before, and a zero expected manifest is an error here. A manifest that the store lacks or that does not parse is an impossible state, since
// nothing is deleted from it, and fails opaque whatever the class of the reader: what a manifest holds may be a path that the repository chose.
func (r executionReader) evaluatedFiles(ctx context.Context, revision artifact.Digest, expected artifact.Manifest, selection domain.Selection) ([]string, error) {
	head, err := r.revisionManifest(ctx, revision)
	if err != nil {
		return nil, err
	}
	files, err := domain.EvaluatedFiles(head, expected, selection)
	if err != nil {
		return nil, &opaque{what: "list the files of the receipt", cause: err}
	}
	return files, nil
}

// revisionManifest reads the manifest of a revision from the content store, and checks that it is the one the digest names.
func (r executionReader) revisionManifest(ctx context.Context, digest artifact.Digest) (artifact.Manifest, error) {
	raw, err := r.contents.Read(ctx, digest)
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

// executionLevels are the levels of a declaration's execution policy, which ParseDeclaration validated, so that a level it cannot read is an impossible state.
func executionLevels(policy scope.Execution) (domain.CanonicalLevel, domain.ReceiptLevel, error) {
	canonical, err := domain.ParseCanonicalLevel(policy.Canonical)
	if err != nil {
		return 0, 0, fmt.Errorf("the canonical level: %w", err)
	}
	receipt, err := domain.ParseReceiptLevel(policy.Receipt)
	if err != nil {
		return 0, 0, fmt.Errorf("the receipt level: %w", err)
	}
	return canonical, receipt, nil
}

// receiptView is what the receipt of a revision is decided on, read in one unit of work.
type receiptView struct {
	canonical contract.CanonicalSnapshot // the Suite's; with no current version before the first one
	revised   bool                       // the revision that was looked for exists
	binding   contract.ApprovalBinding   // of that revision
	expected  artifact.Manifest          // of the canonical version that it expects; zero for a revision that expects none
}

// receiptView reads the Suite's canonical and, when it has a current version, the revision of the proposal that find returns (it reports false for none) and the
// manifest of the canonical version that the revision expects. A project with no Suite has nothing to read: the view is empty.
func (r executionReader) receiptView(ctx context.Context, repositoryID int64, find func(context.Context, governance.Tx) (contract.Proposal, bool, error)) (receiptView, error) {
	var view receiptView
	err := r.suites.Do(ctx, owner.ProjectID(repositoryID), defaultSuite, func(ctx context.Context, tx governance.Tx) error {
		state, err := tx.Suite(ctx)
		if err != nil {
			return fmt.Errorf("load suite: %w", err)
		}
		view.canonical = state.Canonical
		if _, current := state.Canonical.Suite().CurrentVersionID(); !current {
			return nil // no policy before the first version, so nothing to decide
		}
		proposal, found, err := find(ctx, tx)
		if err != nil || !found {
			return err
		}
		view.binding, view.revised = proposal.Current().Binding(), true
		version := view.binding.ExpectedCanonical()
		if version == "" {
			return nil
		}
		historical, found, err := tx.Version(ctx, version)
		if err != nil {
			return err
		}
		if !found { // a binding names a version that was promoted: its absence is stored state that is broken, not a missing Suite
			return fmt.Errorf("the canonical version %s that %s expects is not stored", version, view.binding.Reference().RevisionID)
		}
		view.expected = historical.Version().Manifest()
		return nil
	})
	if errors.Is(err, governance.ErrNotFound) {
		return receiptView{}, nil
	}
	if err != nil {
		return receiptView{}, &opaque{what: "read the revision", cause: err}
	}
	return view, nil
}

// receiptApplies reports whether the receipt of the revision applies: whether it adds or changes a file that the include of the canonical version it expects
// matches and the exclude does not (docs/contracts/runtime.md, "Applicable and not applicable"). A revision that expects no canonical, a baseline, has nothing to
// be added to or changed against. governing is the digest of the manifest of that version, which the receipt jobs of the revision name.
func (r executionReader) receiptApplies(ctx context.Context, view receiptView) (applies bool, governing artifact.Digest, err error) {
	if !view.revised || view.expected.IsZero() {
		return false, artifact.Digest{}, nil
	}
	declaration := scope.NoDeclaration()
	if i := slices.IndexFunc(view.expected.Entries(), func(e artifact.Entry) bool { return e.Path == scope.DeclarationPath }); i >= 0 {
		raw, err := r.contents.Read(ctx, view.expected.Entries()[i].Content)
		if err != nil {
			return false, artifact.Digest{}, &opaque{what: "read the declaration of the version", cause: err}
		}
		if declaration, err = scope.ParseDeclaration(raw); err != nil {
			return false, artifact.Digest{}, &opaque{what: "parse the declaration of the version", cause: err}
		}
	}
	files, err := r.evaluatedFiles(ctx, view.binding.ManifestDigest(), view.expected, domain.Selection{Include: declaration.Include(), Exclude: declaration.Exclude()})
	if err != nil {
		return false, artifact.Digest{}, err
	}
	return len(files) > 0, view.expected.Digest(), nil
}
