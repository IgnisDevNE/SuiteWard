package app

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The acknowledgments of the execution commands (docs/contracts/runtime.md, "Commands"): the payload of each of the five dispositions is stored by the commands
// job and rendered by the comment publisher, which refuses a payload whose fields do not fit its disposition.

// executionAck is the message of an acknowledgment payload with these fields on top of the target's.
func executionAck(t *testing.T, fields map[string]any) governance.OutboxMessage {
	t.Helper()
	payload := map[string]any{"repositoryId": cmtRepoID, "repo": cmtRepo, "pull": cmtPull, "comment": cmtComment, "owner": cmtOwner}
	for k, v := range fields {
		payload[k] = v
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return governance.OutboxMessage{Key: fmt.Sprintf("ack:%d:%d", cmtRepoID, cmtComment), Kind: githubCommentKind, Payload: raw}
}

func TestCommentPublisherRendersTheAcknowledgmentsOfTheExecutionCommands(t *testing.T) {
	base := commandtext.Ack{Owner: cmtOwner, Source: cmtComment}
	with := func(f func(*commandtext.Ack)) commandtext.Ack {
		a := base
		f(&a)
		return a
	}
	for _, tc := range []struct {
		name    string
		payload map[string]any
		want    commandtext.Ack
	}{
		{"a mode that was set", map[string]any{"disposition": "mode-set", "mode": "feature"},
			with(func(a *commandtext.Ack) { a.Outcome, a.Mode = commandtext.ModeSet, execution.ModeFeature })},
		{"a mode refused on a fork", map[string]any{"disposition": "mode-refused", "reason": "fork"},
			with(func(a *commandtext.Ack) { a.Outcome, a.Refusal = commandtext.ModeRefused, commandtext.Fork })},
		{"a mode refused on a pull request that is not open", map[string]any{"disposition": "mode-refused", "reason": "not-open"},
			with(func(a *commandtext.Ack) { a.Outcome, a.Refusal = commandtext.ModeRefused, commandtext.NotOpen })},
		{"an unblock of both plans", map[string]any{"disposition": "unblocked", "plans": []string{"canonical", "receipt"}},
			with(func(a *commandtext.Ack) {
				a.Outcome, a.Plans = commandtext.Unblocked, commandtext.Plans{Canonical: true, Receipt: true}
			})},
		{"an unblock of the receipt", map[string]any{"disposition": "unblocked", "plans": []string{"receipt"}},
			with(func(a *commandtext.Ack) { a.Outcome, a.Plans = commandtext.Unblocked, commandtext.Plans{Receipt: true} })},
		{"an unblock refused on a fork", map[string]any{"disposition": "unblock-refused", "reason": "fork"},
			with(func(a *commandtext.Ack) { a.Outcome, a.Refusal = commandtext.UnblockRefused, commandtext.Fork })},
		{"an unblock refused on a pull request that is not open", map[string]any{"disposition": "unblock-refused", "reason": "not-open"},
			with(func(a *commandtext.Ack) { a.Outcome, a.Refusal = commandtext.UnblockRefused, commandtext.NotOpen })},
		{"an unblock refused for want of an unavailable run", map[string]any{"disposition": "unblock-refused", "reason": "not-unavailable"},
			with(func(a *commandtext.Ack) {
				a.Outcome, a.Refusal = commandtext.UnblockRefused, commandtext.NotUnavailable
			})},
		{"a receipt that is pending", map[string]any{"disposition": "receipt-refused", "reference": "P2-R1", "reason": "receipt-pending"},
			with(func(a *commandtext.Ack) {
				a.Outcome, a.Proposal, a.Revision, a.Gate = commandtext.ReceiptRefused, 2, 1, execution.ReasonReceiptPending
			})},
		{"a receipt that is unavailable, with the mode of the approve", map[string]any{"disposition": "receipt-refused", "reference": "P2-R1", "reason": "receipt-unavailable", "mode": "fix"},
			with(func(a *commandtext.Ack) {
				a.Outcome, a.Proposal, a.Revision, a.Gate, a.Mode = commandtext.ReceiptRefused, 2, 1, execution.ReasonReceiptUnavailable, execution.ModeFix
			})},
		{"a mode that the approve lacks", map[string]any{"disposition": "receipt-refused", "reference": "P2-R1", "reason": "mode-required"},
			with(func(a *commandtext.Ack) {
				a.Outcome, a.Proposal, a.Revision, a.Gate = commandtext.ReceiptRefused, 2, 1, execution.ReasonModeRequired
			})},
		{"a mode that the receipt does not meet", map[string]any{"disposition": "receipt-refused", "reference": "P2-R1", "reason": "mode-not-met", "mode": "refactor",
			"counts": cjCounts(1, 2, 3, 4, 5, 6, 7, 8, 9, 10)},
			with(func(a *commandtext.Ack) {
				a.Outcome, a.Proposal, a.Revision, a.Gate, a.Mode = commandtext.ReceiptRefused, 2, 1, execution.ReasonModeNotMet, execution.ModeRefactor
				a.Counts = execution.ReceiptCounts{Detects: 1, Inert: 2, NewAPI: 3, Fails: 4, Missing: 5, Flaky: 6, Skipped: 7, Excluded: 8, Unparsed: 10}
				a.Markers = 9
			})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCmtWorld(t)
			message := executionAck(t, tc.payload)

			w.mustDeliver(message)

			w.wantPost(message.Key, commandtext.Acknowledge(tc.want))
		})
	}
}

// A payload whose fields do not fit its disposition is an error that names the key, never a comment.
func TestCommentPublisherRefusesAnExecutionAcknowledgmentThatDoesNotFitItsDisposition(t *testing.T) {
	counts := cjCounts(0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	for name, payload := range map[string]map[string]any{
		"a mode-set with no mode":                       {"disposition": "mode-set"},
		"a mode-set with an unknown mode":               {"disposition": "mode-set", "mode": "loud"},
		"a mode-set with a reason":                      {"disposition": "mode-set", "mode": "fix", "reason": "fork"},
		"a mode-refused with an unknown reason":         {"disposition": "mode-refused", "reason": "not-unavailable"},
		"a mode-refused with no reason":                 {"disposition": "mode-refused"},
		"an unblocked with no plan":                     {"disposition": "unblocked"},
		"an unblocked with an unknown plan":             {"disposition": "unblocked", "plans": []string{"canonical", "everything"}},
		"an unblocked that lists a plan twice":          {"disposition": "unblocked", "plans": []string{"receipt", "receipt"}},
		"an unblock-refused with an unknown reason":     {"disposition": "unblock-refused", "reason": "carrier"},
		"a refusal that carries plans":                  {"disposition": "unblock-refused", "reason": "fork", "plans": []string{"receipt"}},
		"a receipt-refused with no reference":           {"disposition": "receipt-refused", "reason": "receipt-pending"},
		"a receipt-refused with no reason":              {"disposition": "receipt-refused", "reference": "P2-R1"},
		"a receipt-refused with a reason of the domain": {"disposition": "receipt-refused", "reference": "P2-R1", "reason": "canonical-pending"},
		"a mode-required that names a mode":             {"disposition": "receipt-refused", "reference": "P2-R1", "reason": "mode-required", "mode": "fix"},
		"a mode-not-met with no mode":                   {"disposition": "receipt-refused", "reference": "P2-R1", "reason": "mode-not-met", "counts": counts},
		"a receipt-pending with counts":                 {"disposition": "receipt-refused", "reference": "P2-R1", "reason": "receipt-pending", "counts": cjCounts(1, 0, 0, 0, 0, 0, 0, 0, 0, 0)},
		"a mode-not-met with a negative unparsed count": {"disposition": "receipt-refused", "reference": "P2-R1", "reason": "mode-not-met", "mode": "fix", "counts": cjCounts(0, 0, 0, 0, 0, 0, 0, 0, 0, -1)},
		"a counts object on a mode-set":                 {"disposition": "mode-set", "mode": "fix", "counts": counts},
		"a mode on an unblocked":                        {"disposition": "unblocked", "plans": []string{"receipt"}, "mode": "fix"},
		"a mode on a processed acknowledgment":          {"project": "p", "suite": "default", "operation": "op", "mode": "fix"},
		"a mode on a missing declaration":               {"disposition": "propose-refused", "reason": "declaration-missing", "branch": "feature", "mode": "fix"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newCmtWorld(t)
			message := executionAck(t, payload)

			if err := w.deliver(message); err == nil {
				t.Fatalf("publish accepted the payload %s", message.Payload)
			}
			if posts := w.writer.posts(); len(posts) != 0 {
				t.Fatalf("posted %d comments for a payload that does not fit", len(posts))
			}
		})
	}
}

// The counts of a mode that the receipt did not meet carry the number of changed test files that do not parse, which block every mode: the payload has the key
// and the acknowledgment the publisher renders from it has the count.
func TestTheCountsOfAModeThatWasNotMetCarryTheUnparsedFiles(t *testing.T) {
	message, err := newReceiptRefusedAck(cmtTarget(), cmtComment, cmtOwner, "P2-R1", execution.ReasonModeNotMet, execution.ModeFix, execution.ReceiptCounts{Detects: 1, Unparsed: 3}, 2)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	counts, _ := payload["counts"].(map[string]any)
	if got := counts["unparsed"]; got != float64(3) || len(counts) != 10 {
		t.Fatalf("counts = %v; want the ten keys with unparsed 3", counts)
	}
	var stored ackPayload
	if err := json.Unmarshal(message.Payload, &stored); err != nil {
		t.Fatal(err)
	}
	if ack := rejectedAckOf(stored); ack.Counts != (execution.ReceiptCounts{Detects: 1, Unparsed: 3}) || ack.Markers != 2 {
		t.Fatalf("the acknowledgment holds counts %+v and %d markers; want 1 DETECTS, 3 unparsed and 2 markers", ack.Counts, ack.Markers)
	}
}

// The counts of an unmet mode are the ten counts of the Receipt check's payload, every key present: the producer always writes them all, and a key that is missing
// would read as a count of zero, so the publisher refuses the payload instead of stating a receipt that was never recorded.
func TestCommentPublisherRefusesTheCountsOfAnUnmetModeThatLackAKey(t *testing.T) {
	for _, key := range receiptCountKeys {
		t.Run(key, func(t *testing.T) {
			counts := cjCounts(1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
			delete(counts, key)
			w := newCmtWorld(t)
			message := executionAck(t, map[string]any{"disposition": "receipt-refused", "reference": "P2-R1", "reason": "mode-not-met", "mode": "fix", "counts": counts})

			if err := w.deliver(message); err == nil {
				t.Fatalf("publish accepted the payload %s", message.Payload)
			}
			if posts := w.writer.posts(); len(posts) != 0 {
				t.Fatalf("posted %d comments for counts that lack %q", len(posts), key)
			}
		})
	}
}
