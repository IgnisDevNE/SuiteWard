package execution

import (
	"fmt"
	"math"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
)

// The checks of this file are the shape the stores accept (persistence.md, "Execution (M2.1; normative)"): the codes, the sizes and
// the plan against the rows. They are the same constraints as the CHECKs of migration 00021, so that the in-memory store and
// PostgreSQL refuse the same requests. They decide nothing of the domain.

var (
	headPattern      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sourcePattern    = regexp.MustCompile(`^(V[1-9][0-9]{0,8}|P[1-9][0-9]{0,8}-R[1-9][0-9]{0,8})$`)
	leasePattern     = regexp.MustCompile(`^[0-9a-f]{32}$`)
	versionPattern   = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$`)
	referencePattern = regexp.MustCompile(`^(P[0-9]+-R[0-9]+)?$`)
)

const (
	maxPaths         = 100
	maxPathBytes     = 1024 // a path of a job, and the file of a test
	maxTreePathBytes = 4096 // a skipped submodule or a marked file: a path of a tree
	maxMarkers       = 200
	maxLine          = 10_000_000
	maxSubmodules    = 100
	maxText          = 64 // the toolchain and the runtime
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, args...))
}

// ValidateRepository reports why no repository has this id, as ErrInvalidRequest.
func ValidateRepository(repositoryID int64) error {
	if repositoryID <= 0 {
		return invalid("the repository id must be positive")
	}
	return nil
}

// ValidatePull reports why no pull request has these ids, as ErrInvalidRequest.
func ValidatePull(repositoryID int64, pull int) error {
	if repositoryID <= 0 || pull < 1 || pull > math.MaxInt32 {
		return invalid("the repository id must be positive and the pull number between 1 and %d", math.MaxInt32)
	}
	return nil
}

// Validate reports why the job cannot be created, as ErrInvalidRequest.
func (n NewJob) Validate() error {
	switch {
	case n.At.IsZero():
		return invalid("the creation time is required")
	case n.Unavailable != "" && !n.Unavailable.createdFinal():
		return invalid("a job is created unavailable only for no-merge-base, executor-not-configured or github-unavailable, not %q", n.Unavailable)
	}
	return n.validate(n.Unavailable != "")
}

func validPaths(name string, paths []string) error {
	if len(paths) > maxPaths {
		return invalid("%s has %d paths; at most %d", name, len(paths), maxPaths)
	}
	for _, p := range paths {
		if len(p) > maxPathBytes {
			return invalid("a path of %s is %d bytes; at most %d", name, len(p), maxPathBytes)
		}
	}
	return nil
}

func (s Spec) validate(createdFinal bool) error {
	if err := ValidatePull(s.RepositoryID, s.PullNumber); err != nil {
		return err
	}
	switch {
	case s.Plan != PlanCanonical && s.Plan != PlanReceipt:
		return invalid("unknown plan %q", s.Plan)
	case s.Profile != "go":
		return invalid("unknown profile %q", s.Profile)
	case !headPattern.MatchString(s.HeadSHA):
		return invalid("the head must be 40 lowercase hexadecimal digits")
	case s.GoverningDigest.IsZero() || s.DeclarationDigest.IsZero():
		return invalid("the governing and declaration digests are required")
	case !sourcePattern.MatchString(string(s.GoverningSource)):
		return invalid("the governing source %q is not V<n> or P<n>-R<m>", s.GoverningSource)
	case s.CanonicalLevel.String() == "" || s.ReceiptLevel.String() == "":
		return invalid("the canonical and receipt levels are required")
	case s.ChangedTotal < 0:
		return invalid("the changed total must not be negative")
	}
	return s.validatePlan(createdFinal)
}

// validatePlan checks the fields that depend on the plan: the base, the level that runs, and the display lists.
func (s Spec) validatePlan(createdFinal bool) error {
	if s.Plan == PlanCanonical {
		switch {
		case s.BaseSHA != "":
			return invalid("a canonical job has no base")
		case s.CanonicalLevel.String() == "off":
			return invalid("a canonical job needs a canonical level that is not off")
		case len(s.UnapprovedPaths) > 0 || len(s.ChangedPaths) > 0:
			return invalid("a canonical job lists no paths")
		}
		return nil
	}
	switch {
	case createdFinal && s.BaseSHA != "":
		return invalid("a receipt job created unavailable has no base")
	case !createdFinal && !headPattern.MatchString(s.BaseSHA):
		return invalid("a receipt job needs a base of 40 lowercase hexadecimal digits")
	case s.ReceiptLevel.String() == "off":
		return invalid("a receipt job needs a receipt level that is not off")
	}
	return errorsOf(validPaths("unapproved paths", s.UnapprovedPaths), validPaths("changed paths", s.ChangedPaths))
}

func errorsOf(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// Validate reports why the lease request cannot be answered, as ErrInvalidRequest. The prepare function is required: Lease has no way
// to read the trees of a job without it.
func (r LeaseRequest) Validate(prepare Prepare) error {
	switch {
	case r.Now.IsZero():
		return invalid("the lease time is required")
	case !leasePattern.MatchString(r.LeaseID):
		return invalid("the lease id must be 32 lowercase hexadecimal digits")
	case prepare == nil:
		return invalid("prepare is required")
	case r.JobTimeout <= 0 || r.GitHubWait <= 0:
		return invalid("the job timeout and the GitHub wait must be positive")
	}
	return nil
}

// Validate reports why the sweep cannot be applied, as ErrInvalidRequest.
func (p ExpireParams) Validate() error {
	if p.Now.IsZero() || p.LastSeenAt.IsZero() {
		return invalid("the sweep needs the time now and the time an executor was last seen")
	}
	if p.LeaseWait <= 0 {
		return invalid("the lease wait must be positive")
	}
	return ValidateGitHubWait(p.GitHubWait)
}

// ValidateGitHubWait reports why a GitHub wait cannot be applied, as ErrInvalidRequest: it must be positive, as it is for a lease.
func ValidateGitHubWait(wait time.Duration) error {
	if wait <= 0 {
		return invalid("the GitHub wait must be positive")
	}
	return nil
}

// ValidateCancel reports why a job cannot be cancelled for the reason, as ErrInvalidRequest.
func ValidateCancel(reason Reason) error {
	if !reason.cancelReason() {
		return invalid("a job is cancelled for head-moved, closed or obsolete, not %q", reason)
	}
	return nil
}

var (
	observations   = []string{"pass", "fail", "build-error", "skip", "absent"}
	canonicalTests = []string{"pass", "fail", "skipped", "missing", "flaky", "excluded"}
	receiptTests   = []string{"excluded", "missing", "flaky", "skipped", "fails", "new_api", "detects", "inert"}
	markerKinds    = []string{"testing-import", "testing-testing", "flag-lookup-test", "exit-in-init", "test-file-read", "env-read", "forged-event"}
)

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func validText(name, s string, required bool) error {
	if len(s) > maxText || (required && s == "") {
		return invalid("%s must be 1 to %d bytes", name, maxText)
	}
	return nil
}

// validate checks a report that an executor sent, on its own.
func (r Report) validate() error {
	switch {
	case r.BodyDigest.IsZero():
		return invalid("the body digest is required")
	case r.Status != StateComplete && r.Status != StateUnavailable:
		return invalid("a result is complete or unavailable, not %q", r.Status)
	case r.Status == StateComplete && r.Reason != "":
		return invalid("a complete result has no reason")
	case r.Status == StateUnavailable && !r.Reason.executorSet():
		return invalid("%q is not a reason that an executor gives", r.Reason)
	case !versionPattern.MatchString(r.ExecutorVersion) || r.ExecutorSHA256.IsZero():
		return invalid("the executor version and binary digest are required")
	case r.StartedAt.IsZero() || r.EndedAt.IsZero() || r.EndedAt.Before(r.StartedAt):
		return invalid("the run needs a start and an end that is not before it")
	case r.Status == StateComplete && r.ImageDigest.IsZero():
		return invalid("a complete result needs the image digest")
	}
	required := r.Status == StateComplete
	return errorsOf(r.validPaths(), validText("the toolchain", r.Toolchain, required), validText("the runtime", r.Runtime, required))
}

// validPaths checks the skipped submodules and the changed test files that do not parse.
func (r Report) validPaths() error {
	switch {
	case len(r.Submodules) > maxSubmodules:
		return invalid("%d submodules; at most %d", len(r.Submodules), maxSubmodules)
	case len(r.Unparsed) > maxPaths:
		return invalid("%d unparsed files; at most %d", len(r.Unparsed), maxPaths)
	}
	for _, p := range r.Submodules {
		if len(p) > maxTreePathBytes {
			return invalid("a submodule path of %d bytes; at most %d", len(p), maxTreePathBytes)
		}
	}
	for _, p := range r.Unparsed {
		if !validTestFile(p) {
			return invalid("an unparsed file must be a clean relative path of at most %d bytes that ends _test.go", maxPathBytes)
		}
	}
	return nil
}

// validTestFile is true for a clean relative path, with no .. segment, that ends _test.go and is at most 1024 bytes.
func validTestFile(p string) bool {
	return len(p) <= maxPathBytes && strings.HasSuffix(p, "_test.go") && !strings.HasPrefix(p, "/") && path.Clean(p) == p && p != ".." && !strings.HasPrefix(p, "../")
}

func (m Marker) validate() error {
	if !contains(markerKinds, m.Kind) || m.Line < 1 || m.Line > maxLine || m.Path == "" || len(m.Path) > maxTreePathBytes {
		return invalid("a marker needs a known kind, a path and a line between 1 and %d", maxLine)
	}
	return nil
}

// Validate reports why the result cannot be stored for a job of the plan, as ErrInvalidRequest.
func (r Result) Validate(plan Plan) error {
	if err := r.validate(); err != nil {
		return err
	}
	switch {
	case r.Status == StateUnavailable && len(r.Tests) > 0:
		return invalid("an unavailable result has no tests")
	case len(r.Tests) > MaxTests:
		return invalid("%d tests; at most %d", len(r.Tests), MaxTests)
	case plan == PlanCanonical && (len(r.Markers) > 0 || len(r.Unparsed) > 0):
		return invalid("a canonical result has no markers and no unparsed files")
	case len(r.Markers) > maxMarkers:
		return invalid("%d markers; at most %d", len(r.Markers), maxMarkers)
	}
	for _, m := range r.Markers {
		if err := m.validate(); err != nil {
			return err
		}
	}
	seen := make(map[[2]string]bool, len(r.Tests))
	for _, t := range r.Tests {
		if err := t.validate(plan); err != nil {
			return err
		}
		id := [2]string{t.Package, t.Name}
		if seen[id] {
			return invalid("the test %s of package %s is reported twice", t.Name, t.Package)
		}
		seen[id] = true
	}
	return nil
}

func validRuns(name string, runs []string, least int) error {
	if len(runs) < least || len(runs) > 3 {
		return invalid("%s has %d observations; want %d to 3", name, len(runs), least)
	}
	for _, o := range runs {
		if !contains(observations, o) {
			return invalid("%s has the unknown observation %q", name, o)
		}
	}
	return nil
}

func (t Test) validate(plan Plan) error {
	canonical := plan == PlanCanonical
	kindOK, states := t.Kind == KindDeclared, canonicalTests
	if !canonical {
		kindOK, states = t.Kind == KindAdded || t.Kind == KindChanged, receiptTests
	}
	excluded := t.State == "excluded"
	switch {
	case t.Package == "" || len(t.Package) > 512 || t.Name == "" || len(t.Name) > 200 || t.File == "" || len(t.File) > maxPathBytes:
		return invalid("the package, name and file of a test are required and bounded")
	case !kindOK:
		return invalid("a test of kind %q does not belong in a %s result", t.Kind, plan)
	case !contains(states, t.State):
		return invalid("the state %q is not one of a %s result", t.State, plan)
	case excluded != (t.ExcludedReason != ""), t.ExcludedReason != "" && t.ExcludedReason != ExcludedBuildConstraint && t.ExcludedReason != ExcludedNestedModule:
		return invalid("a test is excluded exactly when it has the reason %q or %q", ExcludedBuildConstraint, ExcludedNestedModule)
	case excluded && (len(t.HeadRuns) > 0 || len(t.BaseRuns) > 0):
		return invalid("an excluded test has no runs")
	case excluded:
		return nil
	case canonical && len(t.BaseRuns) > 0:
		return invalid("a canonical test has no base runs")
	}
	if err := validRuns("the head runs", t.HeadRuns, 1); err != nil {
		return err
	}
	if canonical {
		return nil
	}
	return validRuns("the base runs", t.BaseRuns, 1)
}

// Validate reports why the gate record cannot be stored, as ErrInvalidRequest.
func (g ApprovalGate) Validate() error {
	hasJob := g.RunState == RunPending || g.RunState == RunComplete || g.RunState == RunUnavailable
	switch {
	case !headPattern.MatchString(g.HeadSHA):
		return invalid("the head must be 40 lowercase hexadecimal digits")
	case g.ReceiptLevel.String() == "" || g.ReceiptLevel.String() == "off":
		return invalid("the receipt level of a gate is advisory, before-approval or required")
	case !hasJob && g.RunState != RunNone && g.RunState != RunNotApplicable:
		return invalid("unknown run state %q", g.RunState)
	case hasJob != (g.JobID > 0), g.JobID < 0:
		return invalid("a job belongs to the run states pending, complete and unavailable, and only to them")
	case g.Summary != SummaryNone && g.Summary != SummaryMet && g.Summary != SummaryNotMet, g.Summary != SummaryNone && g.RunState != RunComplete:
		return invalid("a summary is met or not-met, and only for a complete run")
	}
	return nil
}

// Validate reports why the Receipt check cannot be recorded, as ErrInvalidRequest.
func (r ReceiptCheck) Validate() error {
	if err := ValidatePull(r.RepositoryID, r.PullNumber); err != nil {
		return err
	}
	switch {
	case !headPattern.MatchString(r.HeadSHA):
		return invalid("the head must be 40 lowercase hexadecimal digits")
	case strings.TrimSpace(r.Reason) == "":
		return invalid("the reason is required")
	case r.State != CheckSuccess && r.State != CheckInProgress && r.State != CheckFailure && r.State != CheckNeutral:
		return invalid("unknown state %q", r.State)
	case !referencePattern.MatchString(r.Reference):
		return invalid("the reference %q is not P<n>-R<m>", r.Reference)
	case r.JobID < 0:
		return invalid("the job id must not be negative")
	}
	return nil
}

// ValidateUnblock reports why the entry, the head, the jobs and the time cannot be recorded as an unblock, as ErrInvalidRequest. The state of a job is
// one that an unblock lifts or ends (unavailable, pending or leased), with a lease id that is empty or well formed. That each job exists, is as observed
// and is of the head and the pull request is for the store to check.
func ValidateUnblock(e commands.Entry, headSHA string, jobs []UnblockJob, at time.Time) error {
	switch {
	case e.Disposition != commands.Unblocked:
		return invalid("the jobs of an unblock are recorded with the disposition %q, not %q", commands.Unblocked, e.Disposition)
	case !headPattern.MatchString(headSHA):
		return invalid("the head must be 40 lowercase hexadecimal digits")
	case len(jobs) == 0:
		return invalid("an unblock lifts at least one job")
	case at.IsZero():
		return invalid("the time of the unblock is required")
	}
	seen := make(map[int64]bool, len(jobs))
	for _, job := range jobs {
		switch {
		case job.ID <= 0 || seen[job.ID]:
			return invalid("the lifted jobs are distinct and positive")
		case job.State != StateUnavailable && job.State != StatePending && job.State != StateLeased:
			return invalid("job %d is observed as %q; an unblock lifts a job that is unavailable, pending or leased", job.ID, job.State)
		case job.LeaseID != "" && !leasePattern.MatchString(job.LeaseID):
			return invalid("job %d is observed with a lease id that is not 32 lower-case hexadecimal digits", job.ID)
		}
		seen[job.ID] = true
	}
	return nil
}
