package governance_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

func ack(keys ...string) []governance.OutboxMessage {
	messages := make([]governance.OutboxMessage, len(keys))
	for i, key := range keys {
		messages[i] = governance.OutboxMessage{Key: key, Kind: "pr_comment", Payload: json.RawMessage(`{"operation":"` + key + `"}`)}
	}
	return messages
}

func (w *world) outboxKeys() []string {
	w.t.Helper()
	keys, err := w.mem.OutboxKeys(context.Background())
	if err != nil {
		w.t.Fatal(err)
	}
	return keys
}

func (w *world) requireOutbox(want ...string) {
	w.t.Helper()
	if got := w.outboxKeys(); !slices.Equal(got, want) {
		w.t.Fatalf("outbox keys = %v, want %v", got, want)
	}
}

func TestBaselineWritesItsOutboxOnlyWithANewRevision(t *testing.T) {
	w := newWorld(t)
	first := baselineRequest("revision-1", "pinned-commit", baselineTree("v1"))
	first.Outbox = ack("ack-1")
	start := w.revision()

	w.propose(newFakeWriter(), first)

	w.requireOutbox("ack-1")
	w.requireWrites(start, 1) // the outbox does not advance the revision

	unchanged := baselineRequest("revision-2", "pinned-later", baselineTree("v1"))
	unchanged.Outbox = ack("ack-unchanged")
	if got := w.propose(newFakeWriter(), unchanged); got.Revise.Committed {
		t.Fatalf("unchanged coverage committed: %+v", got.Revise)
	}
	w.requireOutbox("ack-1")

	changed := baselineRequest("revision-2", "pinned-later", baselineTree("v2"))
	changed.Outbox = ack("ack-2")
	w.propose(newFakeWriter(), changed)
	w.requireOutbox("ack-1", "ack-2")
}

func TestBaselineOutboxFailureRollsBackTheRevision(t *testing.T) {
	w := newWorld(t)
	first := baselineRequest("revision-1", "pinned-commit", baselineTree("v1"))
	first.Outbox = ack("ack-1")
	w.propose(newFakeWriter(), first)
	before := w.revision()
	changed := baselineRequest("revision-2", "pinned-later", baselineTree("v2"))
	changed.Outbox = ack("ack-1") // the key is taken

	_, err := governance.ProposeBaseline(context.Background(), w.mem, newFakeWriter(), changed)

	if !errors.Is(err, governance.ErrOperationConflict) {
		t.Fatalf("error = %v, want the outbox key conflict", err)
	}
	w.requireWrites(before, 0)
	if got := w.proposal(baselineProposal).Current().Binding().Reference().RevisionID; got != "revision-1" {
		t.Fatalf("current revision = %q, want revision-1: the revision was not rolled back", got)
	}
	w.requireOutbox("ack-1")
}
