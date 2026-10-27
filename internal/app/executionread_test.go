package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// The reader that the checks job, the commands job and the receipt page share (docs/contracts/runtime.md, "The job a gate reads"). Its tests are the rule itself: the
// two jobs' own tests reach it only through a gate.

// readerStore is the execution store of a reader test: the reads of the reader and nothing else.
type readerStore struct {
	execution.Store
	jobs      []execution.Job // of the head, oldest first
	tests     []execution.Test
	result    execution.StoredResult
	hasResult bool
	err       error // every read fails with it
}

func (s readerStore) HeadJobs(context.Context, int64, int, string, execution.Plan) ([]execution.Job, error) {
	return s.jobs, s.err
}

func (s readerStore) Tests(context.Context, int64) ([]execution.Test, error) { return s.tests, s.err }

func (s readerStore) Result(context.Context, int64) (execution.StoredResult, bool, error) {
	return s.result, s.hasResult, s.err
}

// readerContents is a content store that holds the bytes of the manifests of a test.
type readerContents map[artifact.Digest][]byte

func (c readerContents) Read(_ context.Context, digest artifact.Digest) ([]byte, error) {
	if raw, found := c[digest]; found {
		return raw, nil
	}
	return nil, fmt.Errorf("no content for %s", digest)
}

func (c readerContents) add(t *testing.T, files map[string]string) artifact.Manifest {
	t.Helper()
	var entries []artifact.Entry
	for path, content := range files {
		entries = append(entries, artifact.Entry{Path: path, Content: artifact.Hash([]byte(content))})
	}
	manifest, err := artifact.NewManifest(entries)
	if err != nil {
		t.Fatal(err)
	}
	c[manifest.Digest()] = manifest.Encoding()
	return manifest
}

func readerOf(store execution.Store, report func(string, error) error) executionReader {
	return executionReader{executions: store, contents: readerContents{}, report: report}
}

func readerJob(id int64, state execution.State, governing string) execution.Job {
	return execution.Job{ID: id, State: state, Spec: execution.Spec{Plan: execution.PlanReceipt, GoverningDigest: artifact.Hash([]byte(governing))}}
}

func TestTheJobAGateReadsIsTheNewestThatIsNotCancelled(t *testing.T) {
	created := readerJob(5, execution.StateUnavailable, "m")
	created.Reason = execution.ReasonNotConfigured
	for _, c := range []struct {
		name string
		jobs []execution.Job // oldest first
		want int64           // the id of the job, 0 for none
	}{
		{"no job", nil, 0},
		{"one pending job", []execution.Job{readerJob(1, execution.StatePending, "m")}, 1},
		{"the newest of several", []execution.Job{readerJob(1, execution.StateComplete, "m"), readerJob(2, execution.StateLeased, "m")}, 2},
		{"a cancelled job is skipped", []execution.Job{readerJob(1, execution.StateComplete, "m"), readerJob(2, execution.StateCancelled, "m")}, 1},
		{"only cancelled jobs", []execution.Job{readerJob(1, execution.StateCancelled, "m"), readerJob(2, execution.StateCancelled, "m")}, 0},
		{"a job created already final counts", []execution.Job{readerJob(1, execution.StateComplete, "m"), created}, 5},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, found, err := readerOf(readerStore{jobs: c.jobs}, wrapRead).gateJob(t.Context(), 1, 5, "head", execution.PlanCanonical, artifact.Digest{})
			if err != nil || found != (c.want != 0) || got.ID != c.want {
				t.Fatalf("gateJob = job %d, found %v, error %v; want job %d", got.ID, found, err, c.want)
			}
		})
	}
}

// A revision whose expected canonical is no longer the current one reads the jobs governed by the manifest it expects: a newer job of another manifest is not its
// job, and none of the manifest means the receipt is pending.
func TestTheReceiptJobAGateReadsIsTheNewestOfTheManifestTheRevisionExpects(t *testing.T) {
	jobs := []execution.Job{readerJob(1, execution.StateComplete, "v1"), readerJob(2, execution.StateComplete, "v2"), readerJob(3, execution.StatePending, "v2")}
	for _, c := range []struct {
		name      string
		expecting string
		want      int64
	}{
		{"the older manifest", "v1", 1},
		{"the newer manifest", "v2", 3},
		{"a manifest no job holds", "v3", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, found, err := readerOf(readerStore{jobs: jobs}, wrapRead).gateJob(t.Context(), 1, 5, "head", execution.PlanReceipt, artifact.Hash([]byte(c.expecting)))
			if err != nil || found != (c.want != 0) || got.ID != c.want {
				t.Fatalf("gateJob = job %d, found %v, error %v; want job %d", got.ID, found, err, c.want)
			}
		})
	}
}

func TestAFailedReadOfTheJobsIsReportedInTheClassOfTheJobThatReads(t *testing.T) {
	storeErr := errors.New("the store is down")
	_, _, err := readerOf(readerStore{err: storeErr}, opaqueRead).gateJob(t.Context(), 1, 5, "head", execution.PlanReceipt, artifact.Hash([]byte("m")))
	var hidden *opaque
	if !errors.As(err, &hidden) || err.Error() != "read the receipt jobs of the head failed" || !errors.Is(err, storeErr) {
		t.Errorf("an opaque reader failed with %v (opaque %v), want the fixed text, and the cause for errors.Is", err, hidden != nil)
	}
	_, _, err = readerOf(readerStore{err: storeErr}, wrapRead).gateJob(t.Context(), 1, 5, "head", execution.PlanReceipt, artifact.Hash([]byte("m")))
	if errors.As(err, &hidden) || err == nil || err.Error() != "read the receipt jobs of the head: the store is down" {
		t.Errorf("a wrapping reader failed with %v, want the cause in the text", err)
	}
}

func TestTheReceiptRunOfAJobIsItsStatusAndForACompleteOneItsCounts(t *testing.T) {
	complete := readerJob(7, execution.StateComplete, "m")
	tests := func(states ...string) []execution.Test {
		out := make([]execution.Test, len(states))
		for i, s := range states {
			out[i] = execution.Test{Name: fmt.Sprintf("T%d", i), State: s}
		}
		return out
	}
	for _, c := range []struct {
		name    string
		job     execution.Job
		found   bool
		store   readerStore
		status  domain.RunStatus
		state   execution.RunState
		counts  domain.ReceiptCounts
		markers int
	}{
		{"no job reads as pending", execution.Job{}, false, readerStore{}, domain.RunPending, execution.RunNone, domain.ReceiptCounts{}, 0},
		{"a pending job", readerJob(7, execution.StatePending, "m"), true, readerStore{}, domain.RunPending, execution.RunPending, domain.ReceiptCounts{}, 0},
		{"a leased job", readerJob(7, execution.StateLeased, "m"), true, readerStore{}, domain.RunPending, execution.RunPending, domain.ReceiptCounts{}, 0},
		{"an unavailable job", readerJob(7, execution.StateUnavailable, "m"), true, readerStore{}, domain.RunUnavailable, execution.RunUnavailable, domain.ReceiptCounts{}, 0},
		{"a complete job counts its states, unparsed files and markers", complete, true,
			readerStore{tests: tests("fails", "detects", "detects", "new_api", "excluded", "inert", "missing", "flaky", "skipped"),
				result: execution.StoredResult{Report: execution.Report{Unparsed: []string{"a_test.go", "b_test.go"}, Markers: []execution.Marker{{}, {}, {}}}}, hasResult: true},
			domain.RunComplete, execution.RunComplete,
			domain.ReceiptCounts{Fails: 1, Detects: 2, NewAPI: 1, Excluded: 1, Inert: 1, Missing: 1, Flaky: 1, Skipped: 1, Unparsed: 2}, 3},
		{"a complete job with no test and no unparsed file", complete, true, readerStore{hasResult: true}, domain.RunComplete, execution.RunComplete, domain.ReceiptCounts{}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			run, err := readerOf(c.store, wrapRead).receipt(t.Context(), c.job, c.found)
			if err != nil {
				t.Fatal(err)
			}
			if run.status != c.status || run.state != c.state || run.counts != c.counts || run.markers != c.markers || run.job.ID != c.job.ID {
				t.Errorf("receipt = %+v, want status %v state %q counts %+v markers %d of job %d", run, c.status, c.state, c.counts, c.markers, c.job.ID)
			}
		})
	}
}

func TestTheReceiptRunRefusesWhatNoGateCanRead(t *testing.T) {
	for _, c := range []struct {
		name  string
		job   execution.Job
		store readerStore
	}{
		{"a cancelled job", readerJob(7, execution.StateCancelled, "m"), readerStore{}},
		{"a state that a receipt does not have", readerJob(7, execution.StateComplete, "m"), readerStore{tests: []execution.Test{{State: "pass"}}, hasResult: true}},
		{"a complete job with no results row", readerJob(7, execution.StateComplete, "m"), readerStore{}},
		{"a store that fails", readerJob(7, execution.StateComplete, "m"), readerStore{err: errors.New("the store is down")}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := readerOf(c.store, wrapRead).receipt(t.Context(), c.job, true); err == nil {
				t.Fatal("the receipt of a job that cannot be read was read")
			}
			var hidden *opaque
			if _, err := readerOf(c.store, opaqueRead).receipt(t.Context(), c.job, true); !errors.As(err, &hidden) {
				t.Errorf("an opaque reader failed with %v, want an opaque error", err)
			}
		})
	}
}

func TestTheCanonicalRunOfAJobIsItsStatusAndForACompleteOneItsCounts(t *testing.T) {
	tests := []execution.Test{{State: "pass"}, {State: "pass"}, {State: "fail"}, {State: "missing"}, {State: "flaky"}, {State: "skipped"}, {State: "excluded"}}
	run, err := readerOf(readerStore{tests: tests}, wrapRead).canonical(t.Context(), readerJob(7, execution.StateComplete, "m"), true)
	want := canonicalRun{status: domain.RunComplete, counts: domain.CanonicalCounts{Pass: 2, Fail: 1, Missing: 1, Flaky: 1, Skipped: 1, Excluded: 1}}
	if err != nil || run != want {
		t.Fatalf("canonical = %+v, %v; want %+v", run, err, want)
	}
	for state, want := range map[execution.State]domain.RunStatus{execution.StatePending: domain.RunPending, execution.StateLeased: domain.RunPending, execution.StateUnavailable: domain.RunUnavailable} {
		if got, err := readerOf(readerStore{}, wrapRead).canonical(t.Context(), readerJob(7, state, "m"), true); err != nil || got.status != want {
			t.Errorf("a %s job reads %+v, %v; want %v", state, got, err, want)
		}
	}
	if got, err := readerOf(readerStore{}, wrapRead).canonical(t.Context(), execution.Job{}, false); err != nil || got.status != domain.RunPending {
		t.Errorf("no job reads %+v, %v; want pending", got, err)
	}
	for name, c := range map[string]struct {
		job   execution.Job
		store readerStore
	}{
		"a cancelled job":                        {readerJob(7, execution.StateCancelled, "m"), readerStore{}},
		"a state that a canonical does not have": {readerJob(7, execution.StateComplete, "m"), readerStore{tests: []execution.Test{{State: "fails"}}}},
	} {
		if _, err := readerOf(c.store, wrapRead).canonical(t.Context(), c.job, true); err == nil {
			t.Errorf("%s was read", name)
		}
	}
}

func TestTheFilesTheReceiptEvaluatesAreThoseTheSelectionMatchesAndTheExpectedCanonicalLacksOrHoldsDifferently(t *testing.T) {
	declaration, err := scope.ParseDeclaration([]byte("version: 1\ninclude: [/tests/**]\nexclude: [/tests/skip_test.go]\n"))
	if err != nil {
		t.Fatal(err)
	}
	selection := domain.Selection{Include: declaration.Include(), Exclude: declaration.Exclude()}
	contents := readerContents{}
	expected := contents.add(t, map[string]string{"tests/same_test.go": "1", "tests/changed_test.go": "1", "main.go": "1", "tests/skip_test.go": "1"})
	revision := contents.add(t, map[string]string{"tests/same_test.go": "1", "tests/changed_test.go": "2", "tests/new_test.go": "1", "main.go": "2", "tests/skip_test.go": "2"})
	reader := executionReader{contents: contents, report: wrapRead}

	files, err := reader.evaluatedFiles(t.Context(), revision.Digest(), expected, selection)

	if want := []string{"tests/changed_test.go", "tests/new_test.go"}; err != nil || !slices.Equal(files, want) {
		t.Errorf("evaluatedFiles = %q, %v; want %q (a file outside the include, an excluded one and an unchanged one are not evaluated)", files, err, want)
	}
	files, err = reader.evaluatedFiles(t.Context(), expected.Digest(), expected, selection)
	if err != nil || len(files) != 0 {
		t.Errorf("a revision equal to the canonical evaluates %q, %v; want nothing", files, err)
	}
}

// Whatever the reader's class of error, a manifest that the store lacks, or whose bytes are another manifest's, or an expected canonical that is absent (a baseline,
// which the callers that can see one ask about first), fails opaque: what the store says, and what a manifest holds, may hold paths the repository chose.
func TestTheFilesTheReceiptEvaluatesFailOpaqueWhenAManifestCannotBeUsed(t *testing.T) {
	contents := readerContents{}
	expected := contents.add(t, map[string]string{"tests/a_test.go": "1"})
	revision := contents.add(t, map[string]string{"tests/a_test.go": "2"})
	contents[revision.Digest()] = expected.Encoding() // the bytes of another manifest than the digest names
	other := contents.add(t, map[string]string{"tests/b_test.go": "1"})
	delete(contents, other.Digest())
	selection := domain.Selection{}
	for name, c := range map[string]struct {
		revision artifact.Digest
		expected artifact.Manifest
	}{
		"a manifest the store lacks":      {other.Digest(), expected},
		"bytes that are another manifest": {revision.Digest(), expected},
		"a baseline, which expects none":  {expected.Digest(), artifact.Manifest{}},
	} {
		var hidden *opaque
		_, err := executionReader{contents: contents, report: wrapRead}.evaluatedFiles(t.Context(), c.revision, c.expected, selection)
		if !errors.As(err, &hidden) {
			t.Errorf("%s: error %v, want an opaque one", name, err)
		}
	}
}

func TestTheLevelsOfAPolicyAreItsTwoLevelsAndAnUnknownOneIsAnError(t *testing.T) {
	canonical, receipt, err := executionLevels(scope.Execution{Profile: "go", Canonical: "advisory", Receipt: "before-approval"})
	if err != nil || canonical != domain.CanonicalLevelAdvisory || receipt != domain.ReceiptLevelBeforeApproval {
		t.Errorf("executionLevels = %v, %v, %v; want advisory and before-approval", canonical, receipt, err)
	}
	for _, bad := range []scope.Execution{{Profile: "go", Canonical: "sometimes", Receipt: "off"}, {Profile: "go", Canonical: "off", Receipt: "sometimes"}} {
		if _, _, err := executionLevels(bad); !errors.Is(err, domain.ErrInvalidLevel) {
			t.Errorf("executionLevels(%+v) = %v, want ErrInvalidLevel", bad, err)
		}
	}
}

// A receipt job is read by the manifest of the canonical version that its revision expects, so a zero digest (a baseline, which expects none, whose receipt is not
// applicable) is no wildcard for the receipt: it is an error, not a job of another binding. The canonical plan takes any job with the zero digest, as above.
func TestTheReceiptJobAGateReadsNeedsTheManifestTheRevisionExpects(t *testing.T) {
	jobs := []execution.Job{readerJob(1, execution.StateComplete, "v1"), readerJob(2, execution.StatePending, "v2")}
	for name, report := range map[string]func(string, error) error{"opaque": opaqueRead, "wrapping": wrapRead} {
		t.Run(name, func(t *testing.T) {
			got, found, err := readerOf(readerStore{jobs: jobs}, report).gateJob(t.Context(), 1, 5, "head", execution.PlanReceipt, artifact.Digest{})
			if err == nil || found || got.ID != 0 {
				t.Fatalf("gateJob = job %d, found %v, error %v; want an error and no job", got.ID, found, err)
			}
		})
	}
}

// A project with no Suite has nothing to read: the view is empty, and no error. That is all a not found means to the view; a revision that the Suite names and the
// unit of work cannot read is a failure, never an empty view that the callers read as "no Suite" (the unblock refused as "not unavailable", an approve that is not gated).
func TestTheReceiptViewOfAProjectWithNoSuiteIsEmpty(t *testing.T) {
	w := newExecWorld(t, gateDecl)

	view, err := w.check.reads().receiptView(t.Context(), bootRepoID+1, func(context.Context, governance.Tx) (contract.Proposal, bool, error) {
		t.Error("find was called for a project with no Suite")
		return contract.Proposal{}, false, nil
	})

	if err != nil || view.revised || !view.expected.IsZero() {
		t.Fatalf("receiptView revised %v, expected manifest zero %v, error %v; want the empty view and no error", view.revised, view.expected.IsZero(), err)
	}
}

func TestTheReceiptViewFailsWhenTheRevisionTheSuiteNamesCannotBeRead(t *testing.T) {
	w := newExecWorld(t, gateDecl)

	view, err := w.check.reads().receiptView(t.Context(), bootRepoID, func(ctx context.Context, tx governance.Tx) (contract.Proposal, bool, error) {
		proposal, _, err := tx.Proposal(ctx, proposalID(99)) // the carrier names it, and the Suite does not hold it
		return proposal, err == nil, err
	})

	var failure *opaque
	if !errors.As(err, &failure) || view.revised {
		t.Fatalf("receiptView revised %v, error %v; want an opaque error and no view", view.revised, err)
	}
}
