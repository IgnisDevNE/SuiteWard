package governancetest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// The cases of the revision comment: AssessHead calls HeadRequest.Revision, in the transaction of the revision it
// committed, and only then.

const rvCarrier = contract.ApprovalCarrierID("github-pr:1/5")

func revisionCases() []struct {
	name string
	run  func(*cfEnv)
} {
	return []struct {
		name string
		run  func(*cfEnv)
	}{
		{"RevisionCommentIsWrittenOncePerCommittedRevision", cfRevisionCommentOncePerRevision},
		{"RevisionNoticeListsAreSortedAndTheScopeReductionIsReported", cfRevisionNoticeContents},
		{"RevisionCommentThatAnswersFalseWritesNoMessage", cfRevisionCommentFalse},
		{"RevisionCommentFailureRollsBackTheRevision", cfRevisionCommentFailure},
		{"RevisionCommentOutboxConflictRollsBackTheRevision", cfRevisionCommentConflict},
		{"AssessHeadNeedsARevisionFunction", cfAssessHeadNeedsARevisionFunction},
	}
}

// mcReader serves the declaration the merge cases' canonical versions were built from, which is all a head
// assessment reads of a canonical.
type mcReader struct{}

func (mcReader) Read(_ context.Context, digest artifact.Digest) ([]byte, error) {
	if digest != artifact.Hash([]byte(mcDeclaration)) {
		return nil, fmt.Errorf("no content %s", digest)
	}
	return []byte(mcDeclaration), nil
}

// rvNotices records the notices a call was asked to comment on.
type rvNotices struct {
	mu    sync.Mutex
	calls []governance.RevisionNotice
	mute  bool   // answer false
	key   string // overrides the key derived from the reference
	fail  error
}

func (n *rvNotices) notice(notice governance.RevisionNotice) (governance.OutboxMessage, bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, notice)
	if n.fail != nil {
		return governance.OutboxMessage{}, false, n.fail
	}
	if n.mute {
		return governance.OutboxMessage{}, false, nil
	}
	key := n.key
	if key == "" {
		key = fmt.Sprintf("revision/%s-%s", notice.Reference.ProposalID, notice.Reference.RevisionID)
	}
	return governance.OutboxMessage{Key: key, Kind: "revision.comment", Payload: cfObject}, true, nil
}

func (e *cfEnv) headRequest(head string, tree inventory.Tree, n *rvNotices) governance.HeadRequest {
	request := governance.HeadRequest{Project: cfProject, Suite: cfSuite, Carrier: rvCarrier, Head: contract.SourceRevision(head), Tree: tree}
	if n != nil {
		request.Revision = n.notice
	}
	return request
}

func (e *cfEnv) assessHead(request governance.HeadRequest) governance.HeadResult {
	e.t.Helper()
	result, err := governance.AssessHead(context.Background(), e.store, mcReader{}, mcDiscard{}, request)
	if err != nil {
		e.t.Fatalf("assess head %q: %v", request.Head, err)
	}
	return result
}

func (e *cfEnv) assessHeadErr(request governance.HeadRequest) error {
	e.t.Helper()
	_, err := governance.AssessHead(context.Background(), e.store, mcReader{}, mcDiscard{}, request)
	if err == nil {
		e.t.Fatalf("assess head %q succeeded, want an error", request.Head)
	}
	return err
}

// requireNotice fails unless got is want; the lists compare as sets of sorted paths, nil being empty.
func (e *cfEnv) requireNotice(got, want governance.RevisionNotice) {
	e.t.Helper()
	samePrevious := (got.Previous == nil) == (want.Previous == nil) && (got.Previous == nil || *got.Previous == *want.Previous)
	if got.Reference != want.Reference || !samePrevious || got.Canonical != want.Canonical || got.Reduced != want.Reduced ||
		!slices.Equal(got.ReducedRules, want.ReducedRules) || !slices.Equal(got.ReducedPaths, want.ReducedPaths) || !slices.Equal(got.Added, want.Added) || !slices.Equal(got.Modified, want.Modified) || !slices.Equal(got.Removed, want.Removed) {
		e.t.Fatalf("notice = %+v (previous %v), want %+v (previous %v)", got, got.Previous, want, want.Previous)
	}
}

func cfRevisionCommentOncePerRevision(e *cfEnv) {
	e.mcSeed(false)
	notices := &rvNotices{}

	first := e.assessHead(e.headRequest("head-1", mcTreeOf("v2"), notices))

	if first.Change != inventory.ProtectedChange || first.Reference == nil || *first.Reference != bsRef("P1", "R1") {
		e.t.Fatalf("result = %+v, want a protected change proposed as P1-R1", first)
	}
	if len(notices.calls) != 1 {
		e.t.Fatalf("notices = %d, want one for the new revision", len(notices.calls))
	}
	e.requireNotice(notices.calls[0], governance.RevisionNotice{Reference: bsRef("P1", "R1"), Canonical: "V1", Modified: []string{"tests/a.txt"}})
	e.requireQueued(nil, []string{"revision/P1-R1"})

	before := e.revision()
	e.assessHead(e.headRequest("head-1", mcTreeOf("v2"), notices)) // the same head again
	e.requireWrites(before, 0)
	e.assessHead(e.headRequest("head-2", bsTree("v2", "package main\n\nfunc main() {}\n"), notices)) // an implementation-only push
	e.requireWrites(before, 1)                                                                       // its assessment
	if unchanged := e.assessHead(e.headRequest("head-3", mcTreeOf("v1"), notices)); unchanged.Change != inventory.Unchanged {
		e.t.Fatalf("a head equal to the canonical = %+v, want Unchanged", unchanged)
	}
	if len(notices.calls) != 1 {
		e.t.Fatalf("notices = %d after a repeated head, an implementation-only push and an unchanged head; want still 1", len(notices.calls))
	}
	e.requireQueued(nil, []string{"revision/P1-R1"})

	second := e.assessHead(e.headRequest("head-4", mcTreeOf("v3"), notices))

	if second.Reference == nil || *second.Reference != bsRef("P1", "R2") || len(notices.calls) != 2 {
		e.t.Fatalf("result = %+v with %d notices, want P1-R2 and a second notice", second, len(notices.calls))
	}
	previous := bsRef("P1", "R1")
	e.requireNotice(notices.calls[1], governance.RevisionNotice{Reference: bsRef("P1", "R2"), Previous: &previous, Canonical: "V1", Modified: []string{"tests/a.txt"}})
	e.requireQueued(nil, []string{"revision/P1-R1", "revision/P1-R2"})
}

func cfRevisionNoticeContents(e *cfEnv) {
	e.mcSeed(false)
	notices := &rvNotices{}
	moved := &mcTree{files: map[string]string{scope.DeclarationPath: mcDeclaration, "tests/c.txt": string(mcBlob("v3")), "tests/b.txt": string(mcBlob("v2")), "src/main.go": "package main\n"}}

	e.assessHead(e.headRequest("head-1", moved, notices))

	if len(notices.calls) != 1 {
		e.t.Fatalf("notices = %d, want one for the new revision", len(notices.calls))
	}
	e.requireNotice(notices.calls[0], governance.RevisionNotice{Reference: bsRef("P1", "R1"), Canonical: "V1",
		Added: []string{"tests/b.txt", "tests/c.txt"}, Removed: []string{"tests/a.txt"}})

	narrowed := &mcTree{files: map[string]string{scope.DeclarationPath: "version: 1\ninclude: [/tests/b.txt]\n", "tests/b.txt": string(mcBlob("v2"))}}
	reduction := e.assessHead(e.headRequest("head-2", narrowed, notices))

	if reduction.Change != inventory.ScopeReduction || len(notices.calls) != 2 || !notices.calls[1].Reduced || notices.calls[1].Reference != bsRef("P1", "R2") {
		e.t.Fatalf("result = %+v with notices %+v, want a scope reduction as P1-R2 that the notice reports as reduced", reduction, notices.calls)
	}
	// The notice details the reduction: the rule the head dropped and the path it no longer covers.
	previous := bsRef("P1", "R1")
	e.requireNotice(notices.calls[1], governance.RevisionNotice{Reference: bsRef("P1", "R2"), Previous: &previous, Canonical: "V1", Reduced: true,
		ReducedRules: []string{"rule /tests/**"}, ReducedPaths: []string{"tests/a.txt"}, Added: []string{"tests/b.txt"}, Modified: []string{".suiteward.yml"}, Removed: []string{"tests/a.txt"}})
}

func cfRevisionCommentFalse(e *cfEnv) {
	e.mcSeed(false)
	notices := &rvNotices{mute: true}

	result := e.assessHead(e.headRequest("head-1", mcTreeOf("v2"), notices))

	if result.Reference == nil || len(notices.calls) != 1 {
		e.t.Fatalf("result = %+v with %d notices, want the revision committed and the function asked once", result, len(notices.calls))
	}
	e.requireQueued(nil, nil)
}

func cfRevisionCommentFailure(e *cfEnv) {
	e.mcSeed(false)
	before := e.revision()
	boom := errors.New("notice cannot be built")

	err := e.assessHeadErr(e.headRequest("head-1", mcTreeOf("v2"), &rvNotices{fail: boom}))

	if !errors.Is(err, boom) {
		e.t.Fatalf("error = %v, want the notice error", err)
	}
	e.requireWrites(before, 0)
	e.requireChangeProposal(rvCarrier, "")
	e.requireQueued(nil, nil)
}

func cfRevisionCommentConflict(e *cfEnv) {
	e.mcSeed(false)
	if err := e.do(func(ctx context.Context, tx governance.Tx) error {
		return tx.Outbox(ctx, governance.OutboxMessage{Key: "revision/shared", Kind: "revision.comment", Payload: cfObject})
	}); err != nil {
		e.t.Fatal(err)
	}
	before := e.revision()

	err := e.assessHeadErr(e.headRequest("head-1", mcTreeOf("v2"), &rvNotices{key: "revision/shared"}))

	if !errors.Is(err, governance.ErrOperationConflict) {
		e.t.Fatalf("error = %v, want ErrOperationConflict", err)
	}
	e.requireWrites(before, 0)
	e.requireChangeProposal(rvCarrier, "")
	e.requireQueued(nil, []string{"revision/shared"})
}

func cfAssessHeadNeedsARevisionFunction(e *cfEnv) {
	e.mcSeed(false)
	before := e.revision()

	err := e.assessHeadErr(e.headRequest("head-1", mcTreeOf("v2"), nil))

	if !errors.Is(err, governance.ErrInvalidRequest) {
		e.t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
	e.requireWrites(before, 0)
}
