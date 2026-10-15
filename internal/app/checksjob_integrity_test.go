package app

import (
	"context"
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity"
	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity/integritytest"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// verdict records the integrity verdict of the default Suite for version.
func (w *cjJobWorld) verdict(version contract.SuiteVersionID, verdict integrity.Verdict) {
	w.t.Helper()
	err := w.verdicts.Record(w.t.Context(), integrity.Record{Project: w.project(), Suite: "default", Version: version, Verdict: verdict, CheckedAt: w.clock})
	if err != nil {
		w.t.Fatal(err)
	}
}

func (w *cjJobWorld) treeReads() int {
	n := 0
	for _, reads := range w.reader.reads {
		n += reads
	}
	return n
}

func TestChecksFailEveryOpenPullWhenTheCanonicalFailedItsIntegrityAndReadNoTree(t *testing.T) {
	w := newCJWorld(t, true)
	w.tree(cjHead2, bootFiles("v2"))
	w.sync(map[int]string{4: bootHead, 5: cjHead2})
	w.verdict("V1", integrity.Failed)

	w.mustCheck()

	w.want(4, checks.Failure, "integrity-failed", 1)
	w.want(5, checks.Failure, "integrity-failed", 1)
	if reads := w.treeReads(); reads != 0 {
		t.Fatalf("%d trees were read; want none while the canonical failed its integrity", reads)
	}
	w.mustCheck()
	w.want(4, checks.Failure, "integrity-failed", 1) // the same state writes no new generation
}

func TestChecksReportTheIntegrityFailureOverARejectedOwnerCommand(t *testing.T) {
	w := newCJWorld(t, true)
	w.protectedPull()
	w.entry(5, 301, bootOwnerID, commands.Malformed, "")
	w.verdict("V1", integrity.Failed)

	w.mustCheck()

	w.want(5, checks.Failure, "integrity-failed", 2)
}

func TestChecksReportAForkOverARejectedOwnerCommand(t *testing.T) {
	w := newCJWorld(t, true)
	w.protectedPull()
	w.entry(5, 301, bootOwnerID, commands.Malformed, "")
	w.headRepo = map[int]string{5: "fork/widgets"}
	w.sync(map[int]string{4: bootHead, 5: cjHead2})

	w.mustCheck()

	w.want(5, checks.InProgress, "fork-unsupported", 2)
}

func TestChecksReportTheIntegrityFailureOnAForkToo(t *testing.T) {
	w := newCJWorld(t, true)
	w.tree(cjHead2, bootFiles("v2"))
	w.headRepo = map[int]string{5: "fork/widgets"}
	w.sync(map[int]string{4: bootHead, 5: cjHead2})
	w.verdict("V1", integrity.Failed)

	w.mustCheck()

	w.want(5, checks.Failure, "integrity-failed", 1) // integrity comes before the fork rule
}

func TestChecksIgnoreAVerdictOfAnotherVersionAndAnUnavailableOne(t *testing.T) {
	for name, set := range map[string]func(*cjJobWorld){
		"a failure of an older version": func(w *cjJobWorld) { w.verdict("V0", integrity.Failed) },
		"unavailable":                   func(w *cjJobWorld) { w.verdict("V1", integrity.Unavailable) },
		"passed":                        func(w *cjJobWorld) { w.verdict("V1", integrity.Passed) },
	} {
		t.Run(name, func(t *testing.T) {
			w := newCJWorld(t, true)
			w.sync(map[int]string{4: bootHead})
			set(w)

			w.mustCheck()

			w.want(4, checks.Success, "unchanged", 1)
		})
	}
}

func TestChecksRecoverWhenALaterVerificationPasses(t *testing.T) {
	w := newCJWorld(t, true)
	w.sync(map[int]string{4: bootHead})
	w.mustCheck()
	w.want(4, checks.Success, "unchanged", 1)
	w.verdict("V1", integrity.Failed)
	w.mustCheck()
	w.want(4, checks.Failure, "integrity-failed", 2)
	w.verdict("V1", integrity.Unavailable) // an unreadable store never clears a confirmed failure
	w.mustCheck()
	w.want(4, checks.Failure, "integrity-failed", 2)

	w.verdict("V1", integrity.Passed)
	w.mustCheck()

	w.want(4, checks.Success, "unchanged", 3)
}

func TestChecksReadTheIntegrityVerdictOfTheSuiteOnlyWhenThereIsACanonical(t *testing.T) {
	w := newCJWorld(t, false)
	w.sync(map[int]string{4: bootHead})
	w.verdict("V1", integrity.Failed) // a stale verdict of a Suite that has no canonical (yet)

	w.mustCheck()

	w.want(4, checks.InProgress, "bootstrap-pending", 1)
}

func TestChecksFailClosedOnAForkPullAndNeverAssessIt(t *testing.T) {
	for name, headRepo := range map[string]string{"another repository": "fork/widgets", "a deleted fork": "(deleted)"} {
		t.Run(name, func(t *testing.T) {
			w := newCJWorld(t, true)
			w.tree(cjHead2, bootFiles("v2")) // a protected change: assessed, it would get a proposal
			w.headRepo = map[int]string{5: headRepo}
			w.sync(map[int]string{4: bootHead, 5: cjHead2})

			w.mustCheck()

			w.want(5, checks.InProgress, "fork-unsupported", 1)
			w.want(4, checks.Success, "unchanged", 1) // a pull of this repository goes on as before
			if reads := w.reader.reads[cjHead2]; reads != 0 {
				t.Fatalf("the tree of the fork head was read %d times", reads)
			}
			if _, found := w.proposal(2); found {
				t.Fatal("a fork pull request got a proposal")
			}
			record, _ := w.record(5)
			if record.Reference != "" || record.Change != "" {
				t.Fatalf("record %+v, want no reference and no classification", record)
			}
			w.mustCheck()
			w.want(5, checks.InProgress, "fork-unsupported", 1)
		})
	}
}

func TestChecksTreatAnUnobservedHeadRepositoryAsBefore(t *testing.T) {
	w := newCJWorld(t, true)
	w.tree(cjHead2, bootFiles("v2"))
	w.sync(map[int]string{4: bootHead, 5: cjHead2}) // no HeadRepo: a row from before it was observed

	w.mustCheck()

	w.want(5, checks.InProgress, "approval-missing", 1)
	if w.reader.reads[cjHead2] == 0 {
		t.Fatal("the head of an unobserved repository was not assessed")
	}
}

func TestChecksNeverTakeTheSameRepositoryInAnotherLetterCaseForAFork(t *testing.T) {
	w := newCJWorld(t, true)
	w.headRepo = map[int]string{4: "ACME/Widgets"}
	w.sync(map[int]string{4: bootHead})

	w.mustCheck()

	w.want(4, checks.Success, "unchanged", 1)
}

func TestChecksTurnAnAssessedPullIntoAForkWhenItsHeadRepositoryIsObserved(t *testing.T) {
	w := newCJWorld(t, true)
	w.sync(map[int]string{4: bootHead})
	w.mustCheck()
	w.want(4, checks.Success, "unchanged", 1)

	w.headRepo = map[int]string{4: "fork/widgets"}
	w.sync(map[int]string{4: bootHead})
	w.mustCheck()

	w.want(4, checks.InProgress, "fork-unsupported", 2) // the stored success does not stand
}

func TestChecksFailTheRunWhenTheIntegrityVerdictCannotBeRead(t *testing.T) {
	w := newCJWorld(t, true)
	w.sync(map[int]string{4: bootHead})
	w.check.integrity = failingIntegrity{integritytest.NewMemory()}

	err := w.runChecks()

	if !errors.Is(err, errBootStore) {
		t.Fatalf("run error %v, want the store error", err)
	}
	w.wantNone(4)
	if got := w.problems.lookup(bootRepo); got != "failed" {
		t.Fatalf("problem %q, want failed", got)
	}
}

// failingIntegrity fails every read.
type failingIntegrity struct{ *integritytest.Memory }

func (failingIntegrity) Latest(context.Context, contract.ProjectID, contract.SuiteID) (integrity.Record, bool, error) {
	return integrity.Record{}, false, errBootStore
}
