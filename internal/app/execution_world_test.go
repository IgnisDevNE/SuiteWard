package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution/executiontest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// The checks job for execution (docs/contracts/runtime.md, "What the checks job does for execution", "The canonical gate" and "The SuiteWard / Receipt
// check"). The world is the default canonical one, V1 from bootFiles, whose declaration holds the execution policy.

const (
	execDecl      = "version: 1\ninclude: [/tests/**]\nexecution:\n  profile: go\n  canonical: required\n  receipt: required\n"
	execMergeBase = "cccccccccccccccccccccccccccccccccccccccc"
)

// execDeclaration is the declaration of bootFiles with an execution block of the two levels.
func execDeclaration(canonical, receipt string) string {
	return "version: 1\ninclude: [/tests/**]\nexecution:\n  profile: go\n  canonical: " + canonical + "\n  receipt: " + receipt + "\n"
}

// withExecution gives the checks job the execution ledger it writes: the stores, an executor seen at start and the settings of the sweep.
func (j *checksJob) withExecution(executions execution.Store, receipts execution.ReceiptLedger, start time.Time) *checksJob {
	j.executions, j.receipts, j.liveness = executions, receipts, execution.NewLiveness(start)
	j.executor = config.ExecutorTimes{JobTimeout: 30 * time.Minute, LeaseWait: 5 * time.Minute, GitHubWait: 30 * time.Minute}
	j.executorConfigured = true
	return j
}

// MergeBase fails for the reader of a job that never reads one.
func (f *fakeBootReader) MergeBase(context.Context, string, string, string) (string, error) {
	return "", errors.New("fakeBootReader: no merge base is configured")
}

// recordingReceipts keeps what the job publishes to the ledger of the Receipt check, as cjRecordingLedger does for the Contract check.
type recordingReceipts struct {
	*executiontest.ReceiptMemory
	messages []governance.OutboxMessage
}

func (r *recordingReceipts) Publish(ctx context.Context, c execution.ReceiptCheck, at time.Time, message func(int64) (governance.OutboxMessage, error)) (execution.ReceiptRecord, bool, error) {
	return r.ReceiptMemory.Publish(ctx, c, at, func(generation int64) (governance.OutboxMessage, error) {
		m, err := message(generation)
		if err == nil {
			r.messages = append(r.messages, m)
		}
		return m, err
	})
}

// execHeadFiles is bootFiles of content under the declaration decl.
func execHeadFiles(decl, content string) map[string]string {
	files := bootFiles(content)
	files[".suiteward.yml"] = decl
	return files
}

// newExecWorld is the default canonical world with the declaration decl, whose content store the job reads back.
func newExecWorld(t *testing.T, decl string) *cjJobWorld {
	t.Helper()
	w := newCJWorldOf(t, execHeadFiles(decl, "v1"))
	w.check.contents = w.content
	return w
}

// execRun runs the checks job once, with an executor that is seen at that time.
func (w *cjJobWorld) execRun() {
	w.t.Helper()
	w.check.liveness.Saw(w.clock.Add(time.Minute))
	w.mustCheck()
}

// execPull opens pull 5 at cjHead2 with the files of its head, next to pull 4 at the canonical's head, and runs the job once.
func (w *cjJobWorld) execPull(files map[string]string) {
	w.t.Helper()
	w.tree(cjHead2, files)
	w.sync(map[int]string{4: bootHead, 5: cjHead2})
	w.execRun()
}

// headJobs are the jobs of a head for a plan, oldest first.
func (w *cjJobWorld) headJobs(pull int, head string, plan execution.Plan) []execution.Job {
	w.t.Helper()
	jobs, err := w.jobs.HeadJobs(w.t.Context(), bootRepoID, pull, head, plan)
	if err != nil {
		w.t.Fatal(err)
	}
	return jobs
}

// onlyJob is the one job of a head for a plan.
func (w *cjJobWorld) onlyJob(pull int, head string, plan execution.Plan) execution.Job {
	w.t.Helper()
	jobs := w.headJobs(pull, head, plan)
	if len(jobs) != 1 {
		w.t.Fatalf("pull %d at %s has %d %s jobs, want 1\nlogs: %s", pull, head[:4], len(jobs), plan, w.logs)
	}
	return jobs[0]
}

func (w *cjJobWorld) noJobs(pull int, head string, plan execution.Plan) {
	w.t.Helper()
	if jobs := w.headJobs(pull, head, plan); len(jobs) != 0 {
		w.t.Fatalf("pull %d at %s has %d %s jobs (%+v), want none", pull, head[:4], len(jobs), plan, jobs)
	}
}

// openJobs are the pending and leased jobs of the repository.
func (w *cjJobWorld) openJobs() []execution.Job {
	w.t.Helper()
	jobs, err := w.jobs.OpenJobs(w.t.Context(), bootRepoID)
	if err != nil {
		w.t.Fatal(err)
	}
	return jobs
}

// jobByID reads a job again.
func (w *cjJobWorld) jobByID(id int64) execution.Job {
	w.t.Helper()
	job, found, err := w.jobs.Job(w.t.Context(), id)
	if err != nil || !found {
		w.t.Fatalf("job %d: found %v, error %v", id, found, err)
	}
	return job
}

// canonicalManifest is the digest of the manifest of V1.
func (w *cjJobWorld) canonicalManifest() artifact.Digest {
	w.t.Helper()
	var digest artifact.Digest
	w.read(func(ctx context.Context, tx governance.Tx) {
		state, err := tx.Suite(ctx)
		if err != nil {
			w.t.Fatal(err)
		}
		digest = state.Canonical.Contract().Manifest().Digest()
	})
	return digest
}

// revisionManifest is the digest of the manifest that revision R<revision> of proposal P<proposal> binds.
func (w *cjJobWorld) revisionManifest(proposal, revision int) artifact.Digest {
	w.t.Helper()
	var digest artifact.Digest
	w.read(func(ctx context.Context, tx governance.Tx) {
		p, _, err := tx.Proposal(ctx, proposalID(proposal))
		if err != nil {
			w.t.Fatal(err)
		}
		digest = p.Revisions()[revision-1].Binding().ManifestDigest()
	})
	return digest
}

// execKey is the key of a job, written out so that the test pins the contract: the SHA-256 of six lines, each ended by a newline.
func execKey(plan execution.Plan, profile, head, base string, governing, declaration artifact.Digest) string {
	lines := []string{string(plan), profile, head, base, governing.String(), declaration.String()}
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n") + "\n"))
	return hex.EncodeToString(sum[:])
}

// requireSpec requires a job to hold the identity of want, pending, with the key of its six lines.
func requireSpec(t *testing.T, got execution.Job, want execution.Spec) {
	t.Helper()
	key := execKey(want.Plan, want.Profile, want.HeadSHA, want.BaseSHA, want.GoverningDigest, want.DeclarationDigest)
	if got.Key != key {
		t.Errorf("job %d key %s, want %s", got.ID, got.Key, key)
	}
	same := got.RepositoryID == want.RepositoryID && got.PullNumber == want.PullNumber && got.Plan == want.Plan && got.Profile == want.Profile && got.HeadSHA == want.HeadSHA &&
		got.BaseSHA == want.BaseSHA && got.GoverningDigest == want.GoverningDigest && got.GoverningSource == want.GoverningSource && got.DeclarationDigest == want.DeclarationDigest &&
		got.CanonicalLevel == want.CanonicalLevel && got.ReceiptLevel == want.ReceiptLevel && got.ChangedTotal == want.ChangedTotal &&
		strings.Join(got.ChangedPaths, "\n") == strings.Join(want.ChangedPaths, "\n") && strings.Join(got.UnapprovedPaths, "\n") == strings.Join(want.UnapprovedPaths, "\n")
	if !same {
		t.Errorf("job %d is\n%+v\nwant\n%+v", got.ID, got.Spec, want)
	}
}

// requireFinal requires a job created already unavailable, or ended so, for the reason.
func requireFinal(t *testing.T, got execution.Job, reason execution.Reason) {
	t.Helper()
	if got.State != execution.StateUnavailable || got.Reason != reason {
		t.Fatalf("job %d is %s/%s, want unavailable/%s", got.ID, got.State, got.Reason, reason)
	}
}

// finish leases the oldest ready job, which must be job, and completes it with the tests.
func (w *cjJobWorld) finish(job execution.Job, tests []execution.Test, edit ...func(*execution.Result)) {
	w.t.Helper()
	leased, found, err := w.jobs.Lease(w.t.Context(), execution.LeaseRequest{Now: w.clock, LeaseID: fmt.Sprintf("%032x", job.ID*16+int64(job.Attempts)), JobTimeout: time.Hour, GitHubWait: time.Hour},
		func(context.Context, execution.Job) error { return nil })
	if err != nil || !found || leased.ID != job.ID {
		w.t.Fatalf("lease of job %d: got job %d, found %v, error %v", job.ID, leased.ID, found, err)
	}
	result := execution.Result{LeaseID: leased.LeaseID, Tests: tests, Report: execution.Report{Status: execution.StateComplete, ExecutorVersion: "0.4.0-test",
		ExecutorSHA256: artifact.Hash([]byte("binary")), ImageDigest: artifact.Hash([]byte("image")), Toolchain: "go1.25.1", Runtime: "podman 5.5.0",
		StartedAt: w.clock.Add(-2 * time.Minute), EndedAt: w.clock.Add(-time.Minute), BodyDigest: artifact.Hash([]byte(fmt.Sprint("body", job.ID)))}}
	for _, apply := range edit {
		apply(&result)
	}
	if _, err := w.jobs.Complete(w.t.Context(), job.ID, result, w.clock, time.Hour); err != nil {
		w.t.Fatalf("complete job %d: %v", job.ID, err)
	}
}

// canonicalTests are declared tests in the given states, named after their position.
func canonicalTests(states ...string) []execution.Test {
	runs := map[string][]string{"pass": {"pass"}, "fail": {"fail"}, "skipped": {"skip"}, "missing": {"absent"}, "flaky": {"pass", "fail", "pass"}}
	tests := make([]execution.Test, len(states))
	for i, state := range states {
		tests[i] = execution.Test{Package: ".", Name: fmt.Sprintf("TestC%02d", i), File: "a_test.go", Kind: execution.KindDeclared, State: state, HeadRuns: runs[state]}
		if state == "excluded" {
			tests[i].ExcludedReason = execution.ExcludedBuildConstraint
		}
	}
	return tests
}

// receiptTests are added tests in the given states, named after their position.
func receiptTests(states ...string) []execution.Test {
	runs := map[string][2][]string{"inert": {{"pass"}, {"pass"}}, "detects": {{"pass"}, {"fail"}}, "new_api": {{"pass"}, {"build-error"}}, "fails": {{"fail"}, {"pass"}},
		"missing": {{"absent"}, {"pass"}}, "flaky": {{"pass", "fail", "pass"}, {"pass"}}, "skipped": {{"skip"}, {"pass"}}}
	tests := make([]execution.Test, len(states))
	for i, state := range states {
		tests[i] = execution.Test{Package: ".", Name: fmt.Sprintf("TestR%02d", i), File: "a_test.go", Kind: execution.KindAdded, State: state, HeadRuns: runs[state][0], BaseRuns: runs[state][1]}
		if state == "excluded" {
			tests[i].ExcludedReason, tests[i].HeadRuns, tests[i].BaseRuns = execution.ExcludedBuildConstraint, nil, nil
		}
	}
	return tests
}

// payload decodes the payload of an outbox message.
func payloadOf(t *testing.T, m governance.OutboxMessage) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(m.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

// checkPayloads are the payloads of the Contract check messages written for a pull request, oldest first.
func (w *cjJobWorld) checkPayloads(pull int) []map[string]any {
	w.t.Helper()
	var found []map[string]any
	for _, m := range w.ledger.messages {
		if strings.HasPrefix(m.Key, fmt.Sprintf("check:%d:%d:", bootRepoID, pull)) {
			found = append(found, payloadOf(w.t, m))
		}
	}
	return found
}

// lastCheckPayload is the payload of the latest Contract check message of a pull request.
func (w *cjJobWorld) lastCheckPayload(pull int) map[string]any {
	w.t.Helper()
	all := w.checkPayloads(pull)
	if len(all) == 0 {
		w.t.Fatalf("pull %d has no check message", pull)
	}
	return all[len(all)-1]
}

// receipt reads the Receipt check of a pull request.
func (w *cjJobWorld) receipt(pull int) (execution.ReceiptRecord, bool) {
	w.t.Helper()
	record, found, err := w.receipts.Current(w.t.Context(), bootRepoID, pull)
	if err != nil {
		w.t.Fatal(err)
	}
	return record, found
}
