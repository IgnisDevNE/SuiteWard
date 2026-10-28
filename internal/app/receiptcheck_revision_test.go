package app

import (
	"errors"
	"slices"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// The receipt level of the comment of a new revision is decided from what the run holds: the policy read from the canonical, the manifest of that canonical and the manifest of
// the revision. The revision was assessed against the canonical that the notice names, so a run that read the policy from another canonical decides nothing.

func receiptManifest(t *testing.T, content string) artifact.Manifest {
	t.Helper()
	m, err := artifact.NewManifest([]artifact.Entry{{Path: "tests/a_test.go", Content: artifact.Hash([]byte(content))}})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func receiptRunOf(t *testing.T, level domain.ReceiptLevel) *executionRun {
	t.Helper()
	declaration, err := scope.ParseDeclaration([]byte("version: 1\ninclude: [/tests/**]\n"))
	if err != nil {
		t.Fatal(err)
	}
	return &executionRun{known: true, policy: executionPolicy{profile: "go", canonical: domain.CanonicalLevelOff, receipt: level, scope: declaration}}
}

func TestRevisionReceiptIsTheLevelWhenTheRevisionChangesAFileOfTheInclude(t *testing.T) {
	suite := suiteView{canonical: "V1", base: receiptManifest(t, "v1")}
	for name, tc := range map[string]struct {
		x      *executionRun
		suite  suiteView
		notice governance.RevisionNotice
		want   string
	}{
		"a file of the include changes":  {receiptRunOf(t, domain.ReceiptLevelRequired), suite, governance.RevisionNotice{Canonical: "V1", Manifest: receiptManifest(t, "v2")}, "required"},
		"nothing of the include changes": {receiptRunOf(t, domain.ReceiptLevelRequired), suite, governance.RevisionNotice{Canonical: "V1", Manifest: receiptManifest(t, "v1")}, ""},
		"the level is off":               {receiptRunOf(t, domain.ReceiptLevelOff), suite, governance.RevisionNotice{Canonical: "V1", Manifest: receiptManifest(t, "v2")}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := revisionReceipt(tc.x, tc.suite, tc.notice)
			if err != nil || got != tc.want {
				t.Errorf("revisionReceipt = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestRevisionReceiptDecidesNothingForACanonicalThatMovedOrAPolicyThatWasNotRead(t *testing.T) {
	suite := suiteView{canonical: "V1", base: receiptManifest(t, "v1")}
	notice := governance.RevisionNotice{Canonical: "V2", Manifest: receiptManifest(t, "v2")}
	if got, err := revisionReceipt(receiptRunOf(t, domain.ReceiptLevelRequired), suite, notice); !errors.Is(err, governance.ErrCanonicalMoved) || got != "" {
		t.Errorf("revisionReceipt for a revision assessed against V2 with a policy read from V1 = %q, %v; want ErrCanonicalMoved", got, err)
	}
	notice.Canonical = "V1"
	if got, err := revisionReceipt(&executionRun{}, suite, notice); err == nil || errors.Is(err, governance.ErrCanonicalMoved) || got != "" {
		t.Errorf("revisionReceipt with no policy = %q, %v; want an error that is no moved canonical", got, err)
	}
}

// The canonical moves after the run read the Suite and its policy, and before the head is assessed: the revision is not written, the pull request waits for the next run with the
// problem "conflict", and the check it had is kept. The same assessment under the canonical the run read writes the revision with its receipt.
func TestChecksWaitWithAConflictWhenTheCanonicalMovedBeforeTheRevisionIsWritten(t *testing.T) {
	decl := execDeclaration("off", "required")
	w := newExecWorld(t, decl)
	w.execPullFive(execHeadFiles(decl, "v2")) // P2-R1 and its comment, under V1
	if keys := w.revisionKeys(); len(keys) != 1 {
		t.Fatalf("revision comments %v, want the one of P2-R1", keys)
	}
	w.tree(cjHead3, execHeadFiles(decl, "v3")) // a new head: a new revision
	w.sync(map[int]string{5: cjHead3})
	ctx := t.Context()
	enrollment, _, err := w.owners.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := enrollment.Projects[0]
	view, err := w.check.suite(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	x, err := w.check.executionRunOf(ctx, view)
	if err != nil {
		t.Fatal(err)
	}
	pulls, err := w.pulls.Pulls(ctx, p.Repo)
	index := slices.IndexFunc(pulls, func(s githubsync.PullState) bool { return s.Number == 5 })
	if err != nil || index < 0 {
		t.Fatalf("observed pulls %v, %v", pulls, err)
	}
	pull := pulls[index]
	stale := view
	stale.canonical = "V9" // the Suite the run read is not the one the assessment sees

	problem, err := w.check.pull(ctx, enrollment, p, stale, pull, x)

	if err != nil || problem != problemConflict {
		t.Fatalf("pull = %q, %v; want the problem %q and no error", problem, err, problemConflict)
	}
	if keys := w.revisionKeys(); len(keys) != 1 {
		t.Fatalf("revision comments %v, want only the one of P2-R1: nothing of the new revision is written", keys)
	}
	w.want(5, checks.InProgress, "assessment-pending", 2)

	problem, err = w.check.pull(ctx, enrollment, p, view, pull, x) // the canonical the run read
	if err != nil || problem != "" {
		t.Fatalf("pull = %q, %v; want the head assessed", problem, err)
	}
	if keys := w.revisionKeys(); len(keys) != 2 {
		t.Fatalf("revision comments %v, want the one of P2-R2 too", keys)
	}
}
