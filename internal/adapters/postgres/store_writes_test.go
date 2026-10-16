//go:build integration

package postgres_test

import (
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

func (w *pgWorld) suiteRows(suite contract.SuiteID) int {
	w.t.Helper()
	var n int
	if err := w.db.conn.QueryRow(w.t.Context(), "SELECT count(*) FROM suites WHERE project_id = $1 AND suite_id = $2", string(fxProject), string(suite)).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

// createTogether runs one CreateSuite per target at the same moment.
func (w *pgWorld) createTogether(suite contract.SuiteID, targets ...contract.IntegrationTargetID) []error {
	w.t.Helper()
	failures := make([]error, len(targets))
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i, target := range targets {
		workers.Go(func() {
			<-start
			failures[i] = w.store.CreateSuite(w.t.Context(), fxProject, suite, fxPolicy, target)
		})
	}
	close(start)
	workers.Wait()
	return failures
}

// TestConcurrentIdenticalCreateSuiteCallsLeaveOneSuite has several callers
// create the same Suite at the same moment: all succeed and one row exists.
func TestConcurrentIdenticalCreateSuiteCallsLeaveOneSuite(t *testing.T) {
	w := newPGWorld(t)
	w.seedSuite(fxProject, fxSuite) // registers the policy
	for round := range 8 {
		suite := contract.SuiteID("created-" + strconv.Itoa(round))
		for i, err := range w.createTogether(suite, fxTarget, fxTarget, fxTarget) {
			if err != nil {
				t.Fatalf("round %d caller %d: %v, want success", round, i, err)
			}
		}
		if got := w.suiteRows(suite); got != 1 {
			t.Fatalf("round %d: %d rows, want 1", round, got)
		}
	}
}

// TestConcurrentConflictingCreateSuiteCallsLetOneWin has two callers ask for
// the same Suite with different targets: the first to commit creates it and
// the other gets ErrConflict, in whatever order they run.
func TestConcurrentConflictingCreateSuiteCallsLetOneWin(t *testing.T) {
	w := newPGWorld(t)
	w.seedSuite(fxProject, fxSuite)
	for round := range 8 {
		suite := contract.SuiteID("created-" + strconv.Itoa(round))
		succeeded, conflicted := 0, 0
		for _, err := range w.createTogether(suite, "branch:a", "branch:b") {
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, governance.ErrConflict):
				conflicted++
			default:
				t.Fatalf("round %d: %v, want success or a conflict", round, err)
			}
		}
		if rows := w.suiteRows(suite); succeeded != 1 || conflicted != 1 || rows != 1 {
			t.Fatalf("round %d: %d succeeded, %d conflicted, %d rows; want exactly one of each and one row", round, succeeded, conflicted, rows)
		}
	}
}

// TestBaselineOutboxCommitsWithTheRevisionAndRollsBackWithIt checks the
// outbox of ProposeBaseline against the real tables.
func TestBaselineOutboxCommitsWithTheRevisionAndRollsBackWithIt(t *testing.T) {
	w := newPGWorld(t)
	w.seedSuite(fxProject, fxSuite)
	propose := func(revision contract.ProposalRevisionID, tree mapTree, keys ...string) (governance.BaselineResult, error) {
		request := governance.BaselineRequest{
			Reference: contract.ProposalReference{ProjectID: fxProject, SuiteID: fxSuite, ProposalID: "p1", RevisionID: revision},
			Carrier:   "carrier-p1", Origin: "origin", OriginKind: contract.OriginBranch, Tree: tree,
		}
		for _, key := range keys {
			request.Outbox = append(request.Outbox, governance.OutboxMessage{Key: key, Kind: "pr_comment", Payload: json.RawMessage(`{"operation":"` + key + `"}`)})
		}
		return governance.ProposeBaseline(t.Context(), w.store, w.content, request)
	}
	declaration := "version: 1\ninclude: [/tests/**]\n"
	tree := mapTree{scope.DeclarationPath: declaration, "tests/a_test.go": "package tests\n"}
	before := w.revision()

	if result, err := propose("revision-1", tree, "ack-1"); err != nil || !result.Revise.Committed {
		t.Fatalf("first proposal = %+v, %v, want a committed revision", result.Revise, err)
	}
	if w.count("outbox") != 1 || w.revision() != before+1 {
		t.Fatalf("%d outbox rows at revision %d, want 1 at %d", w.count("outbox"), w.revision(), before+1)
	}

	if result, err := propose("revision-2", tree, "ack-unchanged"); err != nil || result.Revise.Committed || w.count("outbox") != 1 {
		t.Fatalf("unchanged proposal = %+v, %v with %d outbox rows, want no write", result.Revise, err, w.count("outbox"))
	}

	changed := mapTree{scope.DeclarationPath: declaration, "tests/a_test.go": "package changed\n"}
	if _, err := propose("revision-2", changed, "ack-1"); !errors.Is(err, governance.ErrOperationConflict) {
		t.Fatalf("proposal with a taken outbox key = %v, want the key conflict", err)
	}
	if w.count("outbox") != 1 || w.count("proposal_revisions") != 1 || w.revision() != before+1 {
		t.Fatalf("%d outbox rows, %d revisions at revision %d, want the failed proposal rolled back", w.count("outbox"), w.count("proposal_revisions"), w.revision())
	}
}
