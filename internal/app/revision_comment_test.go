package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

const cmtRevisionKey = "revision:1001:P2-R2"

func cmtNotice() governance.RevisionNotice {
	previous := contract.ProposalReference{ProjectID: "project", SuiteID: "default", ProposalID: "P2", RevisionID: "R1"}
	return governance.RevisionNotice{
		Reference: contract.ProposalReference{ProjectID: "project", SuiteID: "default", ProposalID: "P2", RevisionID: "R2"}, Previous: &previous, Canonical: "V1",
		Added: []string{"tests/new_test.go"}, Modified: []string{"tests/a_test.go", "tests/b_test.go"}, Removed: []string{"tests/old_test.go"},
	}
}

func cmtRevisionMessage(t *testing.T, notice governance.RevisionNotice, tier string) governance.OutboxMessage {
	t.Helper()
	message, err := newRevisionMessage(cmtTarget(), notice, tier)
	if err != nil {
		t.Fatalf("newRevisionMessage: %v", err)
	}
	return message
}

func TestRevisionMessageKeysTheCommentByRepositoryAndReference(t *testing.T) {
	message := cmtRevisionMessage(t, cmtNotice(), "isolated")

	if message.Key != cmtRevisionKey || message.Kind != githubCommentKind {
		t.Fatalf("message %s of kind %s, want %s of kind %s", message.Key, message.Kind, cmtRevisionKey, githubCommentKind)
	}
	var payload map[string]any
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"repositoryId": float64(cmtRepoID), "repo": cmtRepo, "pull": float64(cmtPull), "reference": "P2-R2", "previous": "P2-R1", "canonical": "V1",
		"added": []any{"tests/new_test.go"}, "modified": []any{"tests/a_test.go", "tests/b_test.go"}, "removed": []any{"tests/old_test.go"},
		"addedTotal": float64(1), "modifiedTotal": float64(2), "removedTotal": float64(1), "reduced": false, "tier": "isolated"}
	if len(payload) != len(want) {
		t.Fatalf("payload %v, want exactly %v", payload, want)
	}
	for key, value := range want {
		if got, _ := json.Marshal(payload[key]); string(got) != string(cmtMust(json.Marshal(value))) {
			t.Errorf("payload %s = %s, want %v", key, got, value)
		}
	}
}

func TestRevisionMessageOmitsThePreviousRevisionOfTheFirstOne(t *testing.T) {
	notice := cmtNotice()
	notice.Reference.RevisionID, notice.Previous = "R1", nil
	message := cmtRevisionMessage(t, notice, "isolated")

	var payload map[string]any
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if _, present := payload["previous"]; present || message.Key != "revision:1001:P2-R1" {
		t.Fatalf("key %s and payload %v, want no previous revision", message.Key, payload)
	}
}

func TestCommentPublisherRendersTheRevisionComment(t *testing.T) {
	notice := cmtNotice()
	notice.Reduced = true
	w := newCmtWorld(t)
	message := cmtRevisionMessage(t, notice, "co-located")

	w.mustDeliver(message)

	w.wantPost(cmtRevisionKey, commandtext.RevisionComment(commandtext.Revision{Proposal: 2, Revision: 2, Previous: "P2-R1", Canonical: 1, Added: notice.Added, Modified: notice.Modified,
		Removed: notice.Removed, AddedTotal: 1, ModifiedTotal: 2, RemovedTotal: 1, Reduced: true, Tier: commandtext.CoLocated}))
}

func TestRevisionMessageBoundsItsListsAndKeepsTheTrueTotals(t *testing.T) {
	notice := cmtNotice()
	notice.Added = nil
	for i := range 25 {
		notice.Added = append(notice.Added, "tests/"+strings.Repeat("x", 200)+string(rune('a'+i)))
	}
	message := cmtRevisionMessage(t, notice, "isolated")

	var payload struct {
		Added      []string `json:"added"`
		AddedTotal int      `json:"addedTotal"`
	}
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Added) != 20 || payload.AddedTotal != 25 {
		t.Fatalf("%d entries and a total of %d, want 20 entries and the true total 25", len(payload.Added), payload.AddedTotal)
	}
	for _, entry := range payload.Added {
		if len(entry) > 150 || !strings.HasSuffix(entry, "…") {
			t.Fatalf("entry %q has %d bytes, want one cut to 150 bytes that ends with the marker", entry, len(entry))
		}
	}
}

func TestRevisionCommentStaysBelowTheLimitsInTheWorstCase(t *testing.T) {
	hostile := strings.Repeat("\x01\xff", 400)
	notice := cmtNotice()
	notice.Reference = contract.ProposalReference{ProposalID: "P999999999", RevisionID: "R999999999"}
	previous := contract.ProposalReference{ProposalID: "P999999999", RevisionID: "R999999998"}
	notice.Previous, notice.Canonical, notice.Reduced = &previous, "V999999999", true
	notice.Added, notice.Modified, notice.Removed = nil, nil, nil
	for i := range 100 {
		entry := string(rune('a'+i%26)) + hostile
		notice.Added, notice.Modified, notice.Removed = append(notice.Added, entry), append(notice.Modified, entry), append(notice.Removed, entry)
	}
	message, err := newRevisionMessage(cmtTarget(), notice, "co-located")
	if err != nil {
		t.Fatal(err)
	}
	w := newCmtWorld(t)

	w.mustDeliver(message)

	if len(message.Payload) > 64<<10 {
		t.Fatalf("the payload has %d bytes, want at most the outbox limit of %d", len(message.Payload), 64<<10)
	}
	if body := w.writer.posts()[0].body; len(body) > 65535 {
		t.Fatalf("the comment has %d bytes, want at most GitHub's 65535", len(body))
	}
}

func TestRevisionMessageRefusesWhatItCannotRender(t *testing.T) {
	for name, change := range map[string]func(*governance.RevisionNotice, *string){
		"an unknown tier":                 func(_ *governance.RevisionNotice, tier *string) { *tier = "SECRET" },
		"a canonical that is not V<n>":    func(n *governance.RevisionNotice, _ *string) { n.Canonical = "SECRET" },
		"a previous revision of another":  func(n *governance.RevisionNotice, _ *string) { n.Previous.ProposalID = "P3" },
		"a previous that is not earlier":  func(n *governance.RevisionNotice, _ *string) { n.Previous.RevisionID = "R2" },
		"a reference that is not P<n>-Rm": func(n *governance.RevisionNotice, _ *string) { n.Reference.ProposalID = "SECRET" },
	} {
		t.Run(name, func(t *testing.T) {
			notice, tier := cmtNotice(), "isolated"
			change(&notice, &tier)
			if _, err := newRevisionMessage(cmtTarget(), notice, tier); err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("newRevisionMessage = %v, want an error that does not quote the input", err)
			}
		})
	}
}

func TestCommentPublisherRefusesARevisionPayloadThatDoesNotMatchItsKey(t *testing.T) {
	with := func(change func(map[string]any)) json.RawMessage {
		var m map[string]any
		if err := json.Unmarshal(cmtRevisionMessage(t, cmtNotice(), "isolated").Payload, &m); err != nil {
			t.Fatal(err)
		}
		change(m)
		return cmtMust(json.Marshal(m))
	}
	ack := cmtMust(newRejectedAck(cmtTarget(), cmtComment, cmtOwner, "edited-first", 0, ""))
	for _, tc := range []struct {
		name    string
		key     string
		payload json.RawMessage
	}{
		{"another revision", "revision:1001:P2-R3", with(func(map[string]any) {})},
		{"another proposal", "revision:1001:P3-R2", with(func(map[string]any) {})},
		{"another repository", "revision:1002:P2-R2", with(func(map[string]any) {})},
		{"an unknown field", cmtRevisionKey, with(func(m map[string]any) { m["body"] = cmtSecretText })},
		{"a reference that is not P<n>-R<m>", cmtRevisionKey, with(func(m map[string]any) { m["reference"] = cmtSecretText })},
		{"a previous revision of another proposal", cmtRevisionKey, with(func(m map[string]any) { m["previous"] = "P3-R1" })},
		{"a previous revision that is not earlier", cmtRevisionKey, with(func(m map[string]any) { m["previous"] = "P2-R2" })},
		{"a canonical that is not V<n>", cmtRevisionKey, with(func(m map[string]any) { m["canonical"] = cmtSecretText })},
		{"a canonical of zero", cmtRevisionKey, with(func(m map[string]any) { m["canonical"] = "V0" })},
		{"a negative total", cmtRevisionKey, with(func(m map[string]any) { m["addedTotal"] = -1 })},
		{"a tier that is unknown", cmtRevisionKey, with(func(m map[string]any) { m["tier"] = cmtSecretText })},
		{"a payload that is not JSON", cmtRevisionKey, json.RawMessage(`{"repositoryId": ` + cmtSecretText)},
		{"a repository that is not owner/name", cmtRevisionKey, with(func(m map[string]any) { m["repo"] = "SECRET" })},
		{"an acknowledgment payload under a revision key", cmtRevisionKey, ack.Payload},
		{"a revision payload under an acknowledgment key", "ack:1001:555", with(func(map[string]any) {})},
		{"a revision key with two parts", "revision:1001", with(func(map[string]any) {})},
		{"a revision key with four parts", "revision:1001:P2-R2:1", with(func(map[string]any) {})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCmtWorld(t)
			err := w.pub.publish(t.Context(), tc.key, tc.payload)
			if err == nil {
				t.Fatal("publish succeeded")
			}
			for _, leak := range []string{cmtSecretText, "SECRET"} {
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

func TestBootstrapSummaryFromAPullStatesTheHeadAndNoBranch(t *testing.T) {
	inputs := cmtSummaryInputs()
	inputs.Source, inputs.Branch = "pull", ""
	message := cmtMust(newSummaryMessage(cmtTarget(), inputs))

	var payload struct {
		Summary map[string]any `json:"summary"`
	}
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Summary["source"] != "pull" || payload.Summary["origin"] != bootHead {
		t.Fatalf("summary %v, want source pull and the head as origin", payload.Summary)
	}
	if _, present := payload.Summary["branch"]; present {
		t.Fatalf("summary %v holds a branch for a pull request", payload.Summary)
	}
	w := newCmtWorld(t)

	w.mustDeliver(message)

	want := cmtSummaryInputs()
	want.IncludeTotal, want.ExcludeTotal = len(want.Include), len(want.Exclude)
	w.wantPost(message.Key, commandtext.BootstrapSummary(commandtext.Summary{Proposal: 1, Revision: 1, Source: commandtext.SourcePull, Origin: bootHead, Protected: 2, Paths: want.Paths,
		Include: want.Include, Exclude: want.Exclude, IncludeTotal: want.IncludeTotal, ExcludeTotal: want.ExcludeTotal, Tier: commandtext.CoLocated}))
}

func TestBootstrapSummaryPayloadsWithoutASourceAreFromTheBranch(t *testing.T) {
	w := newCmtWorld(t)
	var payload map[string]any
	if err := json.Unmarshal(cmtMust(newSummaryMessage(cmtTarget(), cmtSummaryInputs())).Payload, &payload); err != nil {
		t.Fatal(err)
	}
	delete(payload["summary"].(map[string]any), "source") // a message written before M1.7b
	raw := cmtMust(json.Marshal(payload))

	if err := w.pub.publish(t.Context(), "bootstrap-summary:1001:P1-R1", raw); err != nil {
		t.Fatal(err)
	}
	if body := w.writer.posts()[0].body; !strings.Contains(body, "on branch `main`") {
		t.Fatalf("body %q does not name the branch", body)
	}
}

func TestBootstrapSummaryRefusesAnUnknownSourceAndAPullWithABranch(t *testing.T) {
	for name, change := range map[string]func(*bootstrapSummaryInputs){
		"an unknown source":       func(i *bootstrapSummaryInputs) { i.Source = "SECRET" },
		"a pull with a branch":    func(i *bootstrapSummaryInputs) { i.Source = "pull" },
		"a branch with no branch": func(i *bootstrapSummaryInputs) { i.Source, i.Branch = "branch", "" },
	} {
		t.Run(name, func(t *testing.T) {
			inputs := cmtSummaryInputs()
			change(&inputs)
			message := cmtMust(newSummaryMessage(cmtTarget(), inputs))
			w := newCmtWorld(t)
			if err := w.pub.publish(t.Context(), message.Key, message.Payload); err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("publish = %v, want an error that does not quote the payload", err)
			}
		})
	}
}

func TestMergeMessageOfABlockedMergeWithoutACanonicalHasNoVersion(t *testing.T) {
	outcome := cmtOutcome(governance.MergeBlocked)
	outcome.Reason, outcome.Version = governance.MergeReasonDiffersFromApproved, ""
	message := cmtMergeMessage(t, outcome)

	var payload map[string]any
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if version, present := payload["version"]; !present || version != "" {
		t.Fatalf("payload %v, want the empty version", payload)
	}
	w := newCmtWorld(t)

	w.mustDeliver(message)

	w.wantPost(cmtMergeKey, commandtext.MergeComment(commandtext.Merge{Kind: commandtext.MergeBlocked, Reason: "differs-from-approved", Source: cmtMergeSHA, Proposal: 2, Revision: 1, Tier: commandtext.Isolated}))
	if !strings.Contains(w.writer.posts()[0].body, "no canonical contract yet") {
		t.Fatalf("body %q does not say that no canonical exists", w.writer.posts()[0].body)
	}
}

func TestMergeMessageRefusesAnEmptyVersionWhereACanonicalExists(t *testing.T) {
	for name, change := range map[string]func(*governance.MergeOutcome){
		"a promotion":  func(o *governance.MergeOutcome) { o.Kind = governance.MergePromoted },
		"a correction": func(o *governance.MergeOutcome) { o.Kind, o.Corrects = governance.MergeCorrected, "V1" },
		"an integrity-failed block": func(o *governance.MergeOutcome) {
			o.Kind, o.Reason = governance.MergeBlocked, governance.MergeReasonIntegrityFailed
		},
	} {
		t.Run(name, func(t *testing.T) {
			outcome := cmtOutcome(governance.MergePromoted)
			change(&outcome)
			outcome.Version = ""
			if _, _, err := newMergeMessage(cmtTarget(), outcome, "isolated"); err == nil {
				t.Fatal("newMergeMessage succeeded")
			}
		})
	}
}
