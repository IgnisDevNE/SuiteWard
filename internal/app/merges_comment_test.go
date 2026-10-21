package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

const (
	cmtMergeKey = "merge:1001:7"
	cmtMergeSHA = "9c8b7a6f5e4d3c2b1a0f9e8d7c6b5a4f3e2d1c0b"
)

var allBlockedReasons = []governance.MergeReason{
	governance.MergeReasonUnapprovedChange, governance.MergeReasonInvalidSource, governance.MergeReasonAssessmentConflict, governance.MergeReasonApprovalMissing,
	governance.MergeReasonDiffersFromApproved, governance.MergeReasonNotCurrent, governance.MergeReasonCanonicalChanged, governance.MergeReasonPolicyChanged,
	governance.MergeReasonIntegrityNotPassed, governance.MergeReasonIntegrationMismatch, governance.MergeReasonCorrectionContextReused, governance.MergeReasonForkUnsupported,
	governance.MergeReasonSharedMergeCommit, governance.MergeReasonIntegrationUnreadable, governance.MergeReasonIntegrityFailed,
}

func cmtOutcome(kind governance.MergeKind) governance.MergeOutcome {
	reference := contract.ProposalReference{ProjectID: "project", SuiteID: "default", ProposalID: "P2", RevisionID: "R1"}
	return governance.MergeOutcome{Kind: kind, Source: contract.SourceRevision(cmtMergeSHA), Version: "V2", Reference: &reference}
}

func cmtMergeMessage(t *testing.T, outcome governance.MergeOutcome) governance.OutboxMessage {
	t.Helper()
	message, send, err := newMergeMessage(cmtTarget(), outcome, "isolated")
	if err != nil || !send {
		t.Fatalf("newMergeMessage(%+v) = send %v, error %v", outcome, send, err)
	}
	return message
}

func TestMergeMessageKeysTheCommentByRepositoryAndPull(t *testing.T) {
	message := cmtMergeMessage(t, cmtOutcome(governance.MergePromoted))
	if message.Key != cmtMergeKey || message.Kind != githubCommentKind {
		t.Fatalf("message %s of kind %s, want %s of kind %s", message.Key, message.Kind, cmtMergeKey, githubCommentKind)
	}
	var payload map[string]any
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"repositoryId": float64(cmtRepoID), "repo": cmtRepo, "pull": float64(cmtPull), "kind": "promoted", "source": cmtMergeSHA, "version": "V2", "reference": "P2-R1", "tier": "isolated"}
	if len(payload) != len(want) {
		t.Fatalf("payload %v, want exactly %v", payload, want)
	}
	for key, value := range want {
		if payload[key] != value {
			t.Errorf("payload %s = %v, want %v", key, payload[key], value)
		}
	}
}

func TestMergeMessageIsWrittenOnlyForAnOutcomeThatNeedsAComment(t *testing.T) {
	for _, kind := range []governance.MergeKind{governance.MergeNoChange, governance.MergeNotTarget, governance.MergeBeforeCanonical} {
		if message, send, err := newMergeMessage(cmtTarget(), cmtOutcome(kind), "isolated"); err != nil || send || message.Key != "" {
			t.Errorf("%s: message %+v, send %v, error %v; want no message", kind, message, send, err)
		}
	}
}

func TestMergeMessageRefusesAnOutcomeItCannotRender(t *testing.T) {
	blocked := cmtOutcome(governance.MergeBlocked)
	blocked.Reason = governance.MergeReasonCanonicalChanged
	corrected := cmtOutcome(governance.MergeCorrected)
	corrected.Corrects = "V1"
	for name, tc := range map[string]struct {
		outcome governance.MergeOutcome
		tier    string
	}{
		"an unknown tier":                  {cmtOutcome(governance.MergePromoted), "somewhere"},
		"a blocked outcome with no reason": {cmtOutcome(governance.MergeBlocked), "isolated"},
		"a reason that is not a code":      {func() governance.MergeOutcome { o := blocked; o.Reason = "SECRET"; return o }(), "isolated"},
		"a version that is not V<n>":       {func() governance.MergeOutcome { o := cmtOutcome(governance.MergePromoted); o.Version = "2"; return o }(), "isolated"},
		"a source that is not a commit": {func() governance.MergeOutcome {
			o := cmtOutcome(governance.MergePromoted)
			o.Source = "SECRET"
			return o
		}(), "isolated"},
		"a correction that corrects none": {cmtOutcome(governance.MergeCorrected), "isolated"},
		"a promotion that corrects":       {func() governance.MergeOutcome { o := cmtOutcome(governance.MergePromoted); o.Corrects = "V1"; return o }(), "isolated"},
	} {
		if _, _, err := newMergeMessage(cmtTarget(), tc.outcome, tc.tier); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, _, err := newMergeMessage(cmtTarget(), corrected, "isolated"); err != nil {
		t.Errorf("a correction of V1: %v", err)
	}
}

func TestCommentPublisherPostsTheOutcomeOfEveryBlockedReasonOnce(t *testing.T) {
	for _, reason := range allBlockedReasons {
		t.Run(string(reason), func(t *testing.T) {
			w := newCmtWorld(t)
			outcome := cmtOutcome(governance.MergeBlocked)
			outcome.Reason = reason
			outcome.Version = "V3"
			if reason == governance.MergeReasonIntegrationUnreadable {
				outcome.Source = ""
			}
			message := cmtMergeMessage(t, outcome)
			w.mustDeliver(message)

			source := cmtMergeSHA
			if outcome.Source == "" {
				source = ""
			}
			want := commandtext.MergeComment(commandtext.Merge{Kind: commandtext.MergeBlocked, Reason: string(reason), Source: source, Version: 3, Proposal: 2, Revision: 1, Tier: commandtext.Isolated})
			w.wantPost(cmtMergeKey, want)
			body := w.writer.posts()[0].body
			if !strings.Contains(body, "stays at V3") || !strings.Contains(body, "no reset command") {
				t.Fatalf("the comment does not state that V3 stays and how to recover:\n%s", body)
			}

			w.mustDeliver(message) // a redelivery finds the marker
			if len(w.writer.posts()) != 1 {
				t.Fatalf("%d comments after the redelivery, want 1", len(w.writer.posts()))
			}
		})
	}
}

func TestCommentPublisherPostsAPromotionAndACorrectionWithTheVersionAndTheCommit(t *testing.T) {
	corrected := cmtOutcome(governance.MergeCorrected)
	corrected.Version, corrected.Corrects = "V3", "V2"
	for name, tc := range map[string]struct {
		outcome governance.MergeOutcome
		want    commandtext.Merge
	}{
		"promoted":  {cmtOutcome(governance.MergePromoted), commandtext.Merge{Kind: commandtext.MergePromoted, Source: cmtMergeSHA, Version: 2, Proposal: 2, Revision: 1, Tier: commandtext.Isolated}},
		"corrected": {corrected, commandtext.Merge{Kind: commandtext.MergeCorrected, Source: cmtMergeSHA, Version: 3, Corrects: 2, Proposal: 2, Revision: 1, Tier: commandtext.Isolated}},
	} {
		t.Run(name, func(t *testing.T) {
			w := newCmtWorld(t)
			w.mustDeliver(cmtMergeMessage(t, tc.outcome))

			w.wantPost(cmtMergeKey, commandtext.MergeComment(tc.want))
			body := w.writer.posts()[0].body
			if !strings.Contains(body, "did not run the tests") || !strings.Contains(body, cmtMergeSHA) || !strings.Contains(body, "V"+string(tc.outcome.Version[1])) {
				t.Fatalf("the comment does not name the version and the commit or say that tests were not run:\n%s", body)
			}
		})
	}
}

func TestMergeCommentStaysBoundedInTheWorstCase(t *testing.T) {
	longest := 0
	for _, reason := range allBlockedReasons {
		outcome := cmtOutcome(governance.MergeBlocked)
		outcome.Reason, outcome.Version = reason, "V999999999"
		outcome.Reference = &contract.ProposalReference{ProposalID: "P999999999", RevisionID: "R999999999"}
		message, _, err := newMergeMessage(cmtTarget(), outcome, "co-located")
		if err != nil {
			t.Fatal(err)
		}
		w := newCmtWorld(t)
		w.mustDeliver(message)
		longest = max(longest, len(w.writer.posts()[0].body), len(message.Payload))
	}
	if longest > 4000 {
		t.Fatalf("a merge comment or payload has %d bytes, want fixed text well below the limits", longest)
	}
}

func TestCommentPublisherRefusesAMergePayloadThatDoesNotMatchItsKey(t *testing.T) {
	good := func() map[string]any {
		var m map[string]any
		if err := json.Unmarshal(cmtMergeMessage(t, cmtOutcome(governance.MergePromoted)).Payload, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	with := func(change func(map[string]any)) json.RawMessage {
		m := good()
		change(m)
		return cmtMust(json.Marshal(m))
	}
	ack := cmtMust(newRejectedAck(cmtTarget(), cmtComment, cmtOwner, "edited-first", 0, ""))
	for _, tc := range []struct {
		name    string
		key     string
		payload json.RawMessage
	}{
		{"another pull request", "merge:1001:8", with(func(map[string]any) {})},
		{"another repository", "merge:1002:7", with(func(map[string]any) {})},
		{"an unknown field", cmtMergeKey, with(func(m map[string]any) { m["body"] = cmtLeakText })},
		{"a payload that is not JSON", cmtMergeKey, json.RawMessage(`{"repositoryId": ` + cmtLeakText)},
		{"trailing data", cmtMergeKey, json.RawMessage(string(with(func(map[string]any) {})) + cmtLeakText)},
		{"a kind that gets no comment", cmtMergeKey, with(func(m map[string]any) { m["kind"] = "no-change" })},
		{"an unknown kind", cmtMergeKey, with(func(m map[string]any) { m["kind"] = cmtLeakText })},
		{"a blocked merge without a reason", cmtMergeKey, with(func(m map[string]any) { m["kind"] = "blocked" })},
		{"a promotion with a reason", cmtMergeKey, with(func(m map[string]any) { m["reason"] = "canonical-changed" })},
		{"a reason that is not a code", cmtMergeKey, with(func(m map[string]any) { m["kind"], m["reason"] = "blocked", cmtLeakText })},
		{"a promotion that corrects", cmtMergeKey, with(func(m map[string]any) { m["corrects"] = "V1" })},
		{"a correction that corrects none", cmtMergeKey, with(func(m map[string]any) { m["kind"] = "corrected" })},
		{"a version that is not V<n>", cmtMergeKey, with(func(m map[string]any) { m["version"] = cmtLeakText })},
		{"a version of zero", cmtMergeKey, with(func(m map[string]any) { m["version"] = "V0" })},
		{"a source that is not a commit", cmtMergeKey, with(func(m map[string]any) { m["source"] = cmtLeakText })},
		{"a promotion without a commit", cmtMergeKey, with(func(m map[string]any) { m["source"] = "" })},
		{"a reference that is not P<n>-R<m>", cmtMergeKey, with(func(m map[string]any) { m["reference"] = cmtLeakText })},
		{"a tier that is unknown", cmtMergeKey, with(func(m map[string]any) { m["tier"] = cmtLeakText })},
		{"a repository that is not owner/name", cmtMergeKey, with(func(m map[string]any) { m["repo"] = "SECRET" })},
		{"a pull number of zero", cmtMergeKey, with(func(m map[string]any) { m["pull"] = 0 })},
		{"an acknowledgment payload under a merge key", cmtMergeKey, ack.Payload},
		{"a merge payload under an acknowledgment key", "ack:1001:555", with(func(map[string]any) {})},
		{"a merge key with two parts", "merge:1001", with(func(map[string]any) {})},
		{"a merge key with four parts", "merge:1001:7:1", with(func(map[string]any) {})},
		{"a pull that is not a number", "merge:1001:x", with(func(map[string]any) {})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCmtWorld(t)
			err := w.pub.publish(t.Context(), tc.key, tc.payload)
			if err == nil {
				t.Fatal("publish succeeded")
			}
			for _, leak := range []string{cmtLeakText, "SECRET"} {
				if strings.Contains(err.Error(), leak) {
					t.Fatalf("the error %q holds payload text %q", err, leak)
				}
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("the error %q does not name the key %q", err, tc.key)
			}
			if len(w.writer.posts()) != 0 || len(w.writer.listed) != 0 {
				t.Fatal("GitHub was called for a payload that was refused")
			}
		})
	}
}
