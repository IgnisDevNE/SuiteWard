package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// The result route (docs/contracts/runtime.md, "The executor API", POST /jobs/{id}/result). The checks are in the contract's order: the size, the
// job, a job that already finished (the same body is recognized whatever the lease), the lease, the strict decoding, the heads, and every rule.
// Whatever the body says is evidence about one run: the service derives each test's state itself, from the observations.

// clockSkew is how far ahead of the service's clock a time of a result may be.
const clockSkew = 5 * time.Minute

// maxLoggedReason bounds the text of a refusal that reaches the log: it may quote what the executor sent.
const maxLoggedReason = 200

var testName = regexp.MustCompile(`^Test[\p{L}\p{Nd}_]*$`)

// The wire of a result. A pointer marks a field that must be told absent from empty.
type (
	resultBody struct {
		Status     string          `json:"status"`
		Reason     *string         `json:"reason"`
		Head       *resultCommit   `json:"head"`
		Base       *resultCommit   `json:"base"`
		Executor   *resultExecutor `json:"executor"`
		Sandbox    *resultSandbox  `json:"sandbox"`
		StartedAt  string          `json:"startedAt"`
		EndedAt    string          `json:"endedAt"`
		Tests      []resultTest    `json:"tests"`
		Markers    []resultMarker  `json:"markers"`
		Submodules []string        `json:"submodules"`
		Unparsed   []string        `json:"unparsed"`
	}
	resultCommit struct {
		SHA string `json:"sha"`
	}
	resultExecutor struct {
		Version      string `json:"version"`
		BinarySHA256 string `json:"binarySha256"`
	}
	resultSandbox struct {
		ImageDigest string `json:"imageDigest"`
		Toolchain   string `json:"toolchain"`
		Runtime     string `json:"runtime"`
	}
	resultTest struct {
		Package  string   `json:"package"`
		Name     string   `json:"name"`
		File     string   `json:"file"`
		Kind     string   `json:"kind"`
		Head     []string `json:"head"`
		Base     []string `json:"base"`
		Excluded *string  `json:"excluded"`
	}
	resultMarker struct {
		Kind string `json:"kind"`
		Path string `json:"path"`
		Line int    `json:"line"`
	}
)

// result stores what an executor reports about a leased job, or queues the job again.
func (h *executorAPI) result(w http.ResponseWriter, r *http.Request) {
	raw, ok := h.resultBytes(w, r)
	if !ok {
		return
	}
	id, ok := jobID(r)
	if !ok {
		h.fail(w, http.StatusNotFound, "not-found")
		return
	}
	job, found, err := h.store.Job(r.Context(), id)
	switch {
	case err != nil:
		h.internal(w, r, "read a job", err)
		return
	case !found:
		h.fail(w, http.StatusNotFound, "not-found")
		return
	}
	now, digest := h.now(), artifact.Hash(raw)
	if job.State.Final() { // the store compares the digest of the body with the stored one, whatever the lease
		out, err := h.store.Complete(r.Context(), id, execution.Result{Report: execution.Report{BodyDigest: digest}}, now, h.times.GitHubWait)
		h.settled(w, r, out, err)
		return
	}
	lease := r.Header.Get(leaseHeader)
	if job.CheckLease(lease, now) != nil {
		h.fail(w, http.StatusConflict, "lease-lost")
		return
	}
	result, err := parseResult(job, lease, raw, digest, now)
	if err != nil {
		h.logger.WarnContext(r.Context(), "executor API: a result was refused", "job", job.ID, "reason", truncate(err.Error(), maxLoggedReason))
		h.fail(w, http.StatusBadRequest, "invalid")
		return
	}
	out, err := h.store.Complete(r.Context(), id, result, now, h.times.GitHubWait)
	h.settled(w, r, out, err)
}

// resultBytes reads the body, at most 4 MiB; a body over it is 413 before anything else is looked at.
func (h *executorAPI) resultBytes(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.ContentLength > maxResultBody {
		h.fail(w, http.StatusRequestEntityTooLarge, "too-large")
		return nil, false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxResultBody))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		h.fail(w, http.StatusRequestEntityTooLarge, "too-large")
		return nil, false
	case err != nil:
		h.fail(w, http.StatusBadRequest, "invalid") // the request broke off
		return nil, false
	}
	return raw, true
}

// settled answers with what the store did with the result, and wakes the checks when it stored one that ended the job.
func (h *executorAPI) settled(w http.ResponseWriter, r *http.Request, out execution.Outcome, err error) {
	switch {
	case errors.Is(err, execution.ErrLeaseLost):
		h.fail(w, http.StatusConflict, "lease-lost")
	case errors.Is(err, execution.ErrConflict):
		h.fail(w, http.StatusConflict, "conflict")
	case errors.Is(err, execution.ErrNotFound):
		h.fail(w, http.StatusNotFound, "not-found")
	case errors.Is(err, execution.ErrInvalidRequest):
		h.logger.WarnContext(r.Context(), "executor API: the store refused a result", "reason", truncate(err.Error(), maxLoggedReason))
		h.fail(w, http.StatusBadRequest, "invalid")
	case err != nil:
		h.internal(w, r, "store a result", err)
	default:
		if !out.Replayed && out.State.Final() {
			h.wakeChecks(r.Context())
		}
		h.reply(w, r, http.StatusOK, map[string]string{"state": string(out.State)})
	}
}

// wakeChecks asks for a run of github_checks after the commit of a result. A failure is logged and never reaches the request.
func (h *executorAPI) wakeChecks(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := h.wake(ctx); err != nil {
		h.logger.WarnContext(ctx, "executor API: the wake-up of github_checks failed", "error", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "..."
}

// parseResult decodes and validates the body of a result for the job, and derives the state of each test. Any error is a refusal: nothing is stored.
func parseResult(job execution.Job, lease string, raw []byte, digest artifact.Digest, now time.Time) (execution.Result, error) {
	if !utf8.Valid(raw) {
		return execution.Result{}, errors.New("the body is not UTF-8")
	}
	var b resultBody
	if err := decodeStrict(raw, &b); err != nil {
		return execution.Result{}, fmt.Errorf("decode the result: %w", err)
	}
	if err := errors.Join(b.checkCommits(job), b.checkText()); err != nil {
		return execution.Result{}, err
	}
	result, err := b.toResult(lease, digest, now)
	if err != nil {
		return execution.Result{}, err
	}
	if err := errors.Join(deriveStates(job.Plan, result.Tests), b.checkPaths()); err != nil {
		return execution.Result{}, err
	}
	if err := result.Validate(job.Plan); err != nil {
		return execution.Result{}, err
	}
	return result, nil
}

// checkCommits holds the heads to the job: head must be its head, and base, present exactly for a receipt, its merge base.
func (b resultBody) checkCommits(job execution.Job) error {
	switch {
	case b.Head == nil || b.Head.SHA != job.HeadSHA:
		return errors.New("the head is not the job's")
	case job.Plan == execution.PlanReceipt && (b.Base == nil || b.Base.SHA != job.BaseSHA):
		return errors.New("the base is not the job's")
	case job.Plan != execution.PlanReceipt && b.Base != nil:
		return errors.New("a canonical result has no base")
	}
	return nil
}

// checkText holds every string of the body to the contract: valid UTF-8 (the body was checked as a whole) with no control or format character and
// no newline. A line or paragraph separator is a newline too.
func (b resultBody) checkText() error {
	for _, s := range b.strings() {
		if strings.IndexFunc(s, unclean) >= 0 {
			return errors.New("a string holds a control or format character or a newline")
		}
	}
	return nil
}

func unclean(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
}

// strings lists every string of the body.
func (b resultBody) strings() []string {
	all := []string{b.Status, b.StartedAt, b.EndedAt}
	all = append(all, b.Submodules...)
	all = append(all, b.Unparsed...)
	if b.Reason != nil {
		all = append(all, *b.Reason)
	}
	if b.Head != nil {
		all = append(all, b.Head.SHA)
	}
	if b.Base != nil {
		all = append(all, b.Base.SHA)
	}
	if b.Executor != nil {
		all = append(all, b.Executor.Version, b.Executor.BinarySHA256)
	}
	if b.Sandbox != nil {
		all = append(all, b.Sandbox.ImageDigest, b.Sandbox.Toolchain, b.Sandbox.Runtime)
	}
	for _, m := range b.Markers {
		all = append(all, m.Kind, m.Path)
	}
	for _, t := range b.Tests {
		all = append(all, t.Package, t.Name, t.File, t.Kind)
		all = append(all, t.Head...)
		all = append(all, t.Base...)
		if t.Excluded != nil {
			all = append(all, *t.Excluded)
		}
	}
	return all
}

// checkPaths holds the names and paths of a result to their shape: a package is a directory relative to the root, a name is Test and word characters,
// a file is a test file, and a marker or a skipped submodule is a clean relative path. The store checks the lengths.
func (b resultBody) checkPaths() error {
	for _, t := range b.Tests {
		if !t.shaped() {
			return errors.New("a test names a package, a name or a file that has not the shape of one")
		}
	}
	for _, m := range b.Markers {
		if !cleanRelative(m.Path) {
			return errors.New("a marker names a path that is not a clean relative path")
		}
	}
	for _, p := range b.Submodules {
		if !cleanRelative(p) {
			return errors.New("a submodule is not a clean relative path")
		}
	}
	return nil
}

// shaped is true for a test whose package, name and file have the shape of one.
func (t resultTest) shaped() bool {
	return (t.Package == "." || cleanRelative(t.Package)) && testName.MatchString(t.Name) && cleanRelative(t.File) && strings.HasSuffix(t.File, "_test.go")
}

// cleanRelative is true for a relative slash path with no empty, "." or ".." segment.
func cleanRelative(p string) bool {
	if p == "" || p == "." || strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return false
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == ".." {
			return false
		}
	}
	return true
}

// toResult converts the body, which the checks above did not look into, to the store's result. The tests have no state yet.
func (b resultBody) toResult(lease string, digest artifact.Digest, now time.Time) (execution.Result, error) {
	report := execution.Report{Status: execution.State(b.Status), BodyDigest: digest, Submodules: b.Submodules, Unparsed: b.Unparsed}
	var err error
	if b.Executor == nil {
		return execution.Result{}, errors.New("the result names no executor")
	}
	report.ExecutorVersion = b.Executor.Version
	if report.ExecutorSHA256, err = artifact.ParseDigest("sha256:" + b.Executor.BinarySHA256); err != nil {
		return execution.Result{}, errors.New("the binary digest is not 64 lower-case hexadecimal digits")
	}
	if report.Reason, err = b.reason(); err != nil {
		return execution.Result{}, err
	}
	if err := b.sandbox(&report); err != nil {
		return execution.Result{}, err
	}
	if report.StartedAt, err = resultTime(b.StartedAt, now); err != nil {
		return execution.Result{}, err
	}
	if report.EndedAt, err = resultTime(b.EndedAt, now); err != nil {
		return execution.Result{}, err
	}
	tests, err := b.convertTests()
	if err != nil {
		return execution.Result{}, err
	}
	for _, m := range b.Markers {
		report.Markers = append(report.Markers, execution.Marker{Kind: m.Kind, Path: m.Path, Line: m.Line})
	}
	return execution.Result{LeaseID: lease, Report: report, Tests: tests}, nil
}

// reason is present exactly for an unavailable result.
func (b resultBody) reason() (execution.Reason, error) {
	switch {
	case b.Status == string(execution.StateUnavailable) && b.Reason != nil:
		return execution.Reason(*b.Reason), nil
	case b.Status == string(execution.StateComplete) && b.Reason == nil:
		return "", nil
	}
	return "", errors.New("a reason belongs to an unavailable result, and to it only")
}

// sandbox reads the sandbox of the run: required for a complete result, and well formed whenever it is there. The toolchain and the runtime are
// printable ASCII.
func (b resultBody) sandbox(report *execution.Report) error {
	if b.Sandbox == nil {
		if report.Status == execution.StateComplete {
			return errors.New("a complete result names its sandbox")
		}
		return nil
	}
	image, err := artifact.ParseDigest(b.Sandbox.ImageDigest)
	if err != nil || !printableASCII(b.Sandbox.Toolchain) || !printableASCII(b.Sandbox.Runtime) {
		return errors.New("the sandbox needs an image digest and a toolchain and runtime of printable ASCII")
	}
	report.ImageDigest, report.Toolchain, report.Runtime = image, b.Sandbox.Toolchain, b.Sandbox.Runtime
	return nil
}

// printableASCII is true for 1 to 64 bytes of printable ASCII, spaces included.
func printableASCII(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := range len(s) {
		if s[i] < ' ' || s[i] > '~' {
			return false
		}
	}
	return true
}

// resultTime reads an RFC 3339 time that is not more than five minutes ahead of the service's clock.
func resultTime(s string, now time.Time) (time.Time, error) {
	at, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, errors.New("a time is not RFC 3339")
	}
	if at.After(now.Add(clockSkew)) {
		return time.Time{}, errors.New("a time is more than five minutes ahead of the clock")
	}
	return at.UTC(), nil
}

// convertTests reads the tests; the excluded reason is one of the two codes, or the test is not excluded.
func (b resultBody) convertTests() ([]execution.Test, error) {
	tests := make([]execution.Test, len(b.Tests))
	for i, t := range b.Tests {
		reason := ""
		if t.Excluded != nil {
			if reason = *t.Excluded; reason != execution.ExcludedBuildConstraint && reason != execution.ExcludedNestedModule {
				return nil, errors.New("a test is excluded for a reason that is neither build-constraint nor nested-module")
			}
		}
		tests[i] = execution.Test{Package: t.Package, Name: t.Name, File: t.File, Kind: t.Kind, ExcludedReason: reason, HeadRuns: t.Head, BaseRuns: t.Base}
	}
	return tests, nil
}
