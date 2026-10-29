package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The outbox rows that the M1.15 binary (v0.4.0-alpha.3) wrote and had not delivered when it was upgraded are decoded and rendered by this one. The payloads below are
// written out from the structs of that tag, never from the current builders: a field that M2.1 added is absent, which the strict decoder must read as its zero value.

// A github_check with no `execution` and no `unblocked` renders as the check it was.
func TestTheChecksOfTheM115BinaryStillRender(t *testing.T) {
	row := fmt.Sprintf(`{"repositoryId":%d,"repo":%q,"pull":%d,"headSha":%q,"generation":3,"state":"in_progress","reason":"approval-missing","reference":"P1-R2","tier":"isolated","version":"V2"}`,
		cmtRepoID, cmtRepo, cmtPull, chkHead)
	old := chkMessage(t, "in_progress", "approval-missing", "P1-R2") // the same facts, built today
	old.Payload = []byte(row)
	w, current := newChkWorld(), newChkWorld()

	if err := w.deliver(old); err != nil {
		t.Fatalf("publish of the row of the M1.15 binary: %v", err)
	}
	if err := current.deliver(chkMessage(t, "in_progress", "approval-missing", "P1-R2")); err != nil {
		t.Fatal(err)
	}

	posted, want := w.runner.posts(), current.runner.posts()
	if len(posted) != 1 || len(want) != 1 || posted[0].Status != "in_progress" || posted[0].Title != want[0].Title || posted[0].Summary != want[0].Summary {
		t.Fatalf("posted %+v, want the run of the same facts built today: %+v", posted, want)
	}
}

// A revision comment whose reduction detail was left out, with neither `reducedPolicy` nor `reducedScope`, reads as a reduction of the protected scope.
func TestTheRevisionCommentsOfTheM115BinaryStillRender(t *testing.T) {
	w := newCmtWorld(t)
	row := fmt.Sprintf(`{"repositoryId":%d,"repo":%q,"pull":%d,"reference":"P2-R2","previous":"P2-R1","canonical":"V1","added":["tests/new_test.go"],"modified":[],"removed":[],`+
		`"addedTotal":1,"modifiedTotal":0,"removedTotal":0,"reduced":true,"reducedOmitted":true,"tier":"isolated"}`, cmtRepoID, cmtRepo, cmtPull)

	w.mustDeliver(governance.OutboxMessage{Key: cmtRevisionKey, Kind: githubCommentKind, Payload: []byte(row)})

	posts := w.writer.posts()
	if len(posts) != 1 || !strings.Contains(posts[0].body, "**This revision reduces the protected scope.**") || strings.Contains(posts[0].body, "test requirements") {
		t.Fatalf("posted %+v, want the one comment that says the revision reduces the protected scope", posts)
	}
}

// A processed acknowledgment, which names the operation of a consent receipt and has none of the fields of the execution commands, renders as the approval it records.
func TestTheProcessedAcknowledgmentsOfTheM115BinaryStillRender(t *testing.T) {
	w := newCmtWorld(t)
	operation, _ := w.consent(contract.ApproveConsent, "R2")
	row := fmt.Sprintf(`{"repositoryId":%d,"repo":%q,"pull":%d,"project":%q,"suite":"default","operation":%q,"comment":%d,"owner":%q}`,
		cmtRepoID, cmtRepo, cmtPull, w.project, operation, cmtComment, cmtOwner)
	key := fmt.Sprintf("ack:%d:%d", cmtRepoID, cmtComment)

	w.mustDeliver(governance.OutboxMessage{Key: key, Kind: githubCommentKind, Payload: []byte(row)})

	w.wantPost(key, commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.Approved, Proposal: 1, Revision: 2, Owner: cmtOwner, Source: cmtComment, Eligible: true}))
}
