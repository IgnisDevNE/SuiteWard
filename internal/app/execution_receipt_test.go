package app

import (
	"fmt"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
)

// The SuiteWard / Receipt check (docs/contracts/runtime.md, "The SuiteWard / Receipt check"): the checks job decides its state from the receipt job of the
// head and writes it to the ledger of the check, with the github_receipt_check message. These worlds plan the receipt alone.

const receiptKeyOf = "receipt-check:1001:5:" // and the generation

// receiptWorld is the default canonical world with receipt: required, and pull 5 alone at cjHead2 changing a test under the canonical include.
func receiptWorld(t *testing.T, receipt string) *cjJobWorld {
	t.Helper()
	decl := execDeclaration("off", receipt)
	w := newExecWorld(t, decl)
	w.execPullFive(execHeadFiles(decl, "v2"))
	return w
}

// lastReceiptPayload is the payload of the latest github_receipt_check message.
func (w *cjJobWorld) lastReceiptPayload() map[string]any {
	w.t.Helper()
	if len(w.receipts.messages) == 0 {
		w.t.Fatal("no github_receipt_check message was written")
	}
	return payloadOf(w.t, w.receipts.messages[len(w.receipts.messages)-1])
}

// wantReceipt requires the Receipt check of pull 5 to have the state, the reason and the generation.
func (w *cjJobWorld) wantReceipt(state execution.CheckState, reason string, generation int64) execution.ReceiptRecord {
	w.t.Helper()
	record, found := w.receipt(5)
	if !found {
		w.t.Fatalf("pull 5 has no Receipt check\nlogs: %s", w.logs)
	}
	if record.State != state || record.Reason != reason || record.Generation != generation {
		w.t.Fatalf("Receipt check %s/%s generation %d, want %s/%s generation %d\nlogs: %s", record.State, record.Reason, record.Generation, state, reason, generation, w.logs)
	}
	return record
}

// wantCounts requires the ten counts of a payload, every key present.
func wantCounts(t *testing.T, payload map[string]any, want map[string]int) {
	t.Helper()
	got, ok := payload["counts"].(map[string]any)
	if !ok {
		t.Fatalf("payload %v has no counts", payload)
	}
	keys := []string{"detects", "inert", "newApi", "fails", "missing", "flaky", "skipped", "excluded", "markers", "unparsed"}
	if len(got) != len(keys) {
		t.Errorf("counts %v, want exactly the ten keys %v", got, keys)
	}
	for _, key := range keys {
		if value, found := got[key]; !found || value != float64(want[key]) {
			t.Errorf("count %s is %v, want %d (counts %v)", key, value, want[key], got)
		}
	}
}

func TestChecksPublishAReceiptThatIsRunningWhileItsJobIsPending(t *testing.T) {
	w := receiptWorld(t, "required")

	record := w.wantReceipt(execution.CheckInProgress, "receipt-running", 1)

	job := w.onlyJob(5, cjHead2, execution.PlanReceipt)
	if record.Reference != "P2-R1" || record.JobID != job.ID || record.HeadSHA != cjHead2 || record.Unblocked {
		t.Errorf("record %+v, want the head, the revision P2-R1 and job %d, not unblocked", record, job.ID)
	}
	if got := w.receipts.messages[0]; got.Kind != "github_receipt_check" || got.Key != receiptKeyOf+"1" {
		t.Errorf("message %s of kind %s, want %s1 of kind github_receipt_check", got.Key, got.Kind, receiptKeyOf)
	}
	payload := w.lastReceiptPayload()
	for key, want := range map[string]any{"repositoryId": float64(bootRepoID), "repo": bootRepo, "pull": float64(5), "headSha": cjHead2, "generation": float64(1), "state": "in_progress",
		"reason": "receipt-running", "reference": "P2-R1", "tier": "isolated"} {
		if payload[key] != want {
			t.Errorf("payload %s is %v, want %v", key, payload[key], want)
		}
	}
	if payload["unavailable"] != nil || payload["unblocked"] != nil {
		t.Errorf("payload %v has an unavailable reason or the unblocked flag", payload)
	}
	wantCounts(t, payload, nil)
	w.execRun()
	w.wantReceipt(execution.CheckInProgress, "receipt-running", 1) // nothing moved: the same generation, and no new message
	if len(w.receipts.messages) != 1 {
		t.Errorf("%d messages, want the one of generation 1", len(w.receipts.messages))
	}
}

func TestChecksDecideTheReceiptFromTheCompleteRunByItsBlockingStates(t *testing.T) {
	cases := []struct {
		name   string
		tests  []execution.Test
		edit   func(*execution.Result)
		state  execution.CheckState
		reason string
	}{
		{"detects and inert", receiptTests("detects", "inert", "new_api", "skipped"), nil, execution.CheckSuccess, "receipt-complete"},
		{"a failing test", receiptTests("detects", "fails"), nil, execution.CheckFailure, "receipt-failing"},
		{"a missing test", receiptTests("detects", "missing"), nil, execution.CheckFailure, "receipt-failing"},
		{"a flaky test does not fail the receipt", receiptTests("detects", "flaky"), nil, execution.CheckSuccess, "receipt-complete"},
		{"no evaluated test: an empty receipt is complete", nil, nil, execution.CheckSuccess, "receipt-complete"},
		{"nothing but excluded tests", receiptTests("excluded", "excluded"), nil, execution.CheckSuccess, "receipt-complete"},
		{"a changed test file that does not parse, though nothing else is listed", nil, func(r *execution.Result) { r.Unparsed = []string{"pkg/broken_test.go"} }, execution.CheckFailure, "receipt-failing"},
		{"a file that does not parse beside passing tests", receiptTests("detects"), func(r *execution.Result) { r.Unparsed = []string{"pkg/a_test.go", "pkg/b_test.go"} }, execution.CheckFailure, "receipt-failing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := receiptWorld(t, "required")
			job := w.onlyJob(5, cjHead2, execution.PlanReceipt)
			var edits []func(*execution.Result)
			if c.edit != nil {
				edits = append(edits, c.edit)
			}

			w.finish(job, c.tests, edits...)
			w.execRun()

			w.wantReceipt(c.state, c.reason, 2)
		})
	}
}

func TestChecksNeverMakeTheReceiptConclusionDependOnTheLevel(t *testing.T) {
	for _, level := range []string{"advisory", "before-approval", "required"} {
		t.Run(level, func(t *testing.T) {
			w := receiptWorld(t, level)
			w.wantReceipt(execution.CheckInProgress, "receipt-running", 1)
			w.finish(w.onlyJob(5, cjHead2, execution.PlanReceipt), receiptTests("fails", "detects"))

			w.execRun()

			w.wantReceipt(execution.CheckFailure, "receipt-failing", 2)
		})
	}
}

func TestChecksCountEveryStateTheExcludedTestsTheMarkersAndTheFilesThatDoNotParse(t *testing.T) {
	w := receiptWorld(t, "required")
	tests := receiptTests("detects", "detects", "inert", "new_api", "fails", "missing", "flaky", "skipped", "excluded")

	w.finish(w.onlyJob(5, cjHead2, execution.PlanReceipt), tests, func(r *execution.Result) {
		r.Markers = []execution.Marker{{Kind: "env-read", Path: "a_test.go", Line: 3}, {Kind: "forged-event", Path: "b_test.go", Line: 9}}
		r.Unparsed = []string{"pkg/broken_test.go"}
	})
	w.execRun()

	w.wantReceipt(execution.CheckFailure, "receipt-failing", 2)
	wantCounts(t, w.lastReceiptPayload(), map[string]int{"detects": 2, "inert": 1, "newApi": 1, "fails": 1, "missing": 1, "flaky": 1, "skipped": 1, "excluded": 1, "markers": 2, "unparsed": 1})
}

func TestChecksPublishAReceiptWhoseJobEndedForNoExecutorAsNeutralWithItsReason(t *testing.T) {
	w := receiptWorld(t, "required")

	w.clock = w.clock.Add(10 * time.Minute) // nobody was seen for the wait
	w.mustCheck()

	w.wantReceipt(execution.CheckNeutral, "receipt-not-run", 2)
	payload := w.lastReceiptPayload()
	if payload["state"] != "neutral" || payload["unavailable"] != "no-executor" || payload["unblocked"] != nil {
		t.Errorf("payload %v, want neutral for no-executor, not unblocked", payload)
	}
	wantCounts(t, payload, nil)
}

func TestChecksPublishAReceiptJobCreatedUnavailableAsNeutral(t *testing.T) {
	t.Run("executor-not-configured", func(t *testing.T) {
		decl := execDeclaration("off", "required")
		w := newExecWorld(t, decl)
		w.check.executorConfigured = false

		w.execPullFive(execHeadFiles(decl, "v2"))

		w.wantReceipt(execution.CheckNeutral, "receipt-not-run", 1)
		if got := w.lastReceiptPayload()["unavailable"]; got != "executor-not-configured" {
			t.Errorf("unavailable is %v, want executor-not-configured", got)
		}
	})
	t.Run("no-merge-base", func(t *testing.T) {
		decl := execDeclaration("off", "required")
		w := newExecWorld(t, decl)
		w.reader.mergeErrs = map[string]error{cjHead2: fmt.Errorf("compare: %w", rest.ErrNotFound)}

		w.execPullFive(execHeadFiles(decl, "v2"))

		w.wantReceipt(execution.CheckNeutral, "receipt-not-run", 1)
		if got := w.lastReceiptPayload()["unavailable"]; got != "no-merge-base" {
			t.Errorf("unavailable is %v, want no-merge-base", got)
		}
	})
}

func TestChecksMarkAnUnblockedReceiptAndKeepItNeutral(t *testing.T) {
	decl := execDeclaration("off", "required")
	w := newExecWorld(t, decl)
	w.check.executorConfigured = false
	w.execPullFive(execHeadFiles(decl, "v2"))
	w.wantReceipt(execution.CheckNeutral, "receipt-not-run", 1)
	unavailable := w.onlyJob(5, cjHead2, execution.PlanReceipt)

	w.unblock(5, 301, cjHead2, unavailable)
	w.execRun()

	record := w.wantReceipt(execution.CheckNeutral, "receipt-not-run", 2) // an unblock does not change the conclusion
	if !record.Unblocked || w.lastReceiptPayload()["unblocked"] != true {
		t.Errorf("record %+v and payload %v, want the unblocked head marked", record, w.lastReceiptPayload())
	}

	w.check.executorConfigured = true // a job created later is not the one the owner unblocked, and the newest job is the one that is read
	w.execRun()

	record = w.wantReceipt(execution.CheckInProgress, "receipt-running", 3)
	jobs := w.headJobs(5, cjHead2, execution.PlanReceipt)
	if len(jobs) != 2 || record.JobID != jobs[1].ID || record.Unblocked {
		t.Errorf("record %+v over jobs %+v, want the fresh job, not unblocked", record, jobs)
	}
	if w.lastReceiptPayload()["unblocked"] != nil {
		t.Errorf("payload %v marks unblocked", w.lastReceiptPayload())
	}
}

func TestChecksPublishNoReceiptWhenTheReceiptLevelIsOffOrThereIsNoRevision(t *testing.T) {
	t.Run("off", func(t *testing.T) {
		decl := execDeclaration("advisory", "off")
		w := newExecWorld(t, decl)
		w.execPullFive(execHeadFiles(decl, "v2"))
		if record, found := w.receipt(5); found {
			t.Fatalf("a repository with receipt: off has the Receipt check %+v", record)
		}
	})
	t.Run("a head with no revision", func(t *testing.T) {
		w := newExecWorld(t, execDeclaration("off", "required"))
		w.sync(map[int]string{4: bootHead})
		w.execRun()
		if record, found := w.receipt(4); found {
			t.Fatalf("an unchanged head has the Receipt check %+v", record)
		}
	})
}
