package app

import (
	"encoding/json"
	"fmt"
	"slices"
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
	message, err := newRevisionMessage(cmtTarget(), notice, tier, "")
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

	w.wantPost(cmtRevisionKey, commandtext.RevisionComment(commandtext.Revision{ChangesUnavailable: true, Proposal: 2, Revision: 2, Previous: "P2-R1", Canonical: 1, Added: notice.Added, Modified: notice.Modified,
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
	message, err := newRevisionMessage(cmtTarget(), notice, "co-located", "")
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
			if _, err := newRevisionMessage(cmtTarget(), notice, tier, ""); err == nil || strings.Contains(err.Error(), "SECRET") {
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
		{"an unknown field", cmtRevisionKey, with(func(m map[string]any) { m["body"] = cmtLeakText })},
		{"a reference that is not P<n>-R<m>", cmtRevisionKey, with(func(m map[string]any) { m["reference"] = cmtLeakText })},
		{"a previous revision of another proposal", cmtRevisionKey, with(func(m map[string]any) { m["previous"] = "P3-R1" })},
		{"a previous revision that is not earlier", cmtRevisionKey, with(func(m map[string]any) { m["previous"] = "P2-R2" })},
		{"a canonical that is not V<n>", cmtRevisionKey, with(func(m map[string]any) { m["canonical"] = cmtLeakText })},
		{"a canonical of zero", cmtRevisionKey, with(func(m map[string]any) { m["canonical"] = "V0" })},
		{"a negative total", cmtRevisionKey, with(func(m map[string]any) { m["addedTotal"] = -1 })},
		{"a tier that is unknown", cmtRevisionKey, with(func(m map[string]any) { m["tier"] = cmtLeakText })},
		{"a payload that is not JSON", cmtRevisionKey, json.RawMessage(`{"repositoryId": ` + cmtLeakText)},
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
		Include: want.Include, Runner: []string{}, Exclude: want.Exclude, IncludeTotal: want.IncludeTotal, ExcludeTotal: want.ExcludeTotal, Tier: commandtext.CoLocated}))
}

// Every release writes a source, so a summary without one is refused, not read as from the branch.
func TestBootstrapSummaryRefusesAPayloadWithoutASource(t *testing.T) {
	w := newCmtWorld(t)
	var payload map[string]any
	if err := json.Unmarshal(cmtMust(newSummaryMessage(cmtTarget(), cmtSummaryInputs())).Payload, &payload); err != nil {
		t.Fatal(err)
	}
	delete(payload["summary"].(map[string]any), "source")
	raw := cmtMust(json.Marshal(payload))

	if err := w.pub.publish(t.Context(), "bootstrap-summary:1001:P1-R1", raw); err == nil {
		t.Fatal("publish accepted a summary without a source")
	}
	if len(w.writer.posts()) != 0 {
		t.Fatal("a comment was posted")
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

func TestRevisionMessageCarriesTheBoundedReductionDetail(t *testing.T) {
	notice := cmtNotice()
	notice.Reduced = true
	for i := range 25 {
		notice.ReducedRules = append(notice.ReducedRules, fmt.Sprintf("rule /r%02d/**", i))
		notice.ReducedPaths = append(notice.ReducedPaths, "tests/"+strings.Repeat("x", 200)+string(rune('a'+i)))
	}
	message := cmtRevisionMessage(t, notice, "isolated")

	var payload struct {
		ReducedRules      []string `json:"reducedRules"`
		ReducedPaths      []string `json:"reducedPaths"`
		ReducedPathsTotal int      `json:"reducedPathsTotal"`
	}
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.ReducedRules) != 20 || len(payload.ReducedPaths) != 20 || payload.ReducedPathsTotal != 25 {
		t.Fatalf("%d rules, %d paths and a total of %d, want 20, 20 and the true total 25", len(payload.ReducedRules), len(payload.ReducedPaths), payload.ReducedPathsTotal)
	}
	for _, entry := range payload.ReducedPaths {
		if len(entry) > 150 || !strings.HasSuffix(entry, "…") {
			t.Fatalf("path %q has %d bytes, want one cut to 150 bytes that ends with the marker", entry, len(entry))
		}
	}

	w := newCmtWorld(t)
	w.mustDeliver(message)
	w.wantPost(cmtRevisionKey, commandtext.RevisionComment(commandtext.Revision{ChangesUnavailable: true, Proposal: 2, Revision: 2, Previous: "P2-R1", Canonical: 1, Added: notice.Added, Modified: notice.Modified,
		Removed: notice.Removed, AddedTotal: 1, ModifiedTotal: 2, RemovedTotal: 1, Reduced: true, ReducedRules: payload.ReducedRules, ReducedPaths: payload.ReducedPaths,
		ReducedPathsTotal: 25, Tier: commandtext.Isolated}))
}

func TestRevisionMessageOmitsTheReductionDetailOfAnotherChange(t *testing.T) {
	message := cmtRevisionMessage(t, cmtNotice(), "isolated")

	for _, field := range []string{"reducedRules", "reducedPaths", "reducedPathsTotal"} {
		if strings.Contains(string(message.Payload), field) {
			t.Fatalf("payload %s holds %s, want none for a revision that does not reduce the scope", message.Payload, field)
		}
	}
}

func TestRevisionMessageRefusesReductionDetailWithoutAReduction(t *testing.T) {
	notice := cmtNotice()
	notice.ReducedRules = []string{"rule /tests/**"}
	if _, err := newRevisionMessage(cmtTarget(), notice, "isolated", ""); err == nil {
		t.Fatal("newRevisionMessage accepted reduction detail on a revision that does not reduce the scope")
	}
}

// A payload written under v0.1.0 has none of the reduction fields: it still decodes and renders as it did.
func TestCommentPublisherRendersARevisionPayloadOfV010(t *testing.T) {
	payload := json.RawMessage(`{"repositoryId":1001,"repo":"acme/widgets","pull":7,"reference":"P2-R2","previous":"P2-R1","canonical":"V1",` +
		`"added":["tests/new_test.go"],"modified":["tests/a_test.go"],"removed":[],"addedTotal":1,"modifiedTotal":1,"removedTotal":0,"reduced":true,"tier":"isolated"}`)
	w := newCmtWorld(t)

	if err := w.pub.publish(t.Context(), cmtRevisionKey, payload); err != nil {
		t.Fatal(err)
	}

	w.wantPost(cmtRevisionKey, commandtext.RevisionComment(commandtext.Revision{ChangesUnavailable: true, Proposal: 2, Revision: 2, Previous: "P2-R1", Canonical: 1, Added: []string{"tests/new_test.go"},
		Modified: []string{"tests/a_test.go"}, Removed: []string{}, AddedTotal: 1, ModifiedTotal: 1, Reduced: true, Tier: commandtext.Isolated}))
}

// The longest revision payload: five lists of 20 entries of 150 characters that JSON writes as six bytes each, 89 KiB, over what the outbox takes.
// The revision must exist all the same, so the reduction detail, which is evidence for the owner and not the revision itself, is what gives way.
func TestRevisionMessageDropsTheReductionDetailThatDoesNotFitTheOutbox(t *testing.T) {
	list := func() []string { return slices.Repeat([]string{strings.Repeat("<", 150)}, 20) }
	notice := cmtNotice()
	notice.Added, notice.Modified, notice.Removed, notice.Reduced = list(), list(), list(), true
	notice.ReducedRules, notice.ReducedPaths = asRules(list()), append(list(), "tests/more_test.go")

	message := cmtRevisionMessage(t, notice, "isolated")

	if len(message.Payload) > maxOutboxPayload {
		t.Fatalf("payload of %d bytes, over %d", len(message.Payload), maxOutboxPayload)
	}
	if err := message.Validate(); err != nil {
		t.Fatalf("the outbox refuses the message: %v", err)
	}
	var payload revisionPayload
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Reduced || !payload.ReducedOmitted || len(payload.ReducedRules) != 0 || len(payload.ReducedPaths) != 0 || payload.ReducedPathsTotal != 21 ||
		len(payload.Added) != 20 || payload.AddedTotal != 20 {
		t.Fatalf("payload %+v, want the reduction kept without its detail but with the count of paths, and the lists of files whole", payload)
	}
	w := newCmtWorld(t)
	w.mustDeliver(message)
	body := w.writer.posts()[0].body
	for _, want := range []string{"**This revision reduces the protected scope.**", "omitted", "/suiteward approve P2-R2 <code>"} {
		if !strings.Contains(body, want) {
			t.Errorf("comment lacks %q:\n%s", want, body)
		}
	}
}

// A revision that fits keeps every detail: nothing is dropped for being possible to drop.
func TestRevisionMessageKeepsTheReductionDetailThatFits(t *testing.T) {
	notice := cmtNotice()
	notice.Reduced, notice.ReducedRules, notice.ReducedPaths = true, []string{"rule /a/**"}, []string{"tests/a_test.go"}

	var payload revisionPayload
	if err := json.Unmarshal(cmtRevisionMessage(t, notice, "isolated").Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ReducedOmitted || len(payload.ReducedRules) != 1 || len(payload.ReducedPaths) != 1 {
		t.Fatalf("payload %+v, want the detail kept and not marked omitted", payload)
	}
}

// A head that only weakens the execution policy reduces no path: its reduction is made of the two execution entries of scope.Reductions alone. The
// revision comment is posted all the same, says that the policy is lowered and lists the levels, and has no claim about files or paths.
func TestRevisionCommentOfAnExecutionOnlyWeakeningIsPostedWithTheLoweredLevels(t *testing.T) {
	notice := cmtNotice()
	notice.Reduced = true
	notice.ReducedRules = []string{"execution canonical: required -> advisory", "execution receipt: advisory -> off"}

	message := cmtRevisionMessage(t, notice, "isolated")

	var payload revisionPayload
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Reduced || payload.ReducedOmitted || !slices.Equal(payload.ReducedRules, notice.ReducedRules) || len(payload.ReducedPaths) != 0 || payload.ReducedPathsTotal != 0 {
		t.Fatalf("payload %+v, want the two execution entries kept, with no path and no total", payload)
	}
	w := newCmtWorld(t)
	w.mustDeliver(message)
	body := w.writer.posts()[0].body
	for _, want := range []string{
		"**This revision lowers the execution policy.**", "Execution policy lowered:\n\n```\ncanonical: required -> advisory\nreceipt: advisory -> off\n```", "/suiteward approve P2-R2 <code>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("comment lacks %q:\n%s", want, body)
		}
	}
	for _, unwanted := range []string{"reduces the protected scope", "Rules removed", "Excludes added", "Paths that the scope no longer covers"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("comment contains %q for a head that only lowers the execution policy:\n%s", unwanted, body)
		}
	}
}

// GitHub takes a comment of 65536 characters. Whatever the repository's text is made of, the body of the longest revision a payload can hold, with
// its marker, must fit, so the comment always posts.
func TestRevisionCommentFitsGitHubWhateverTheTextIsMadeOf(t *testing.T) {
	const marker = len("<!-- suiteward:") + 32 + len(":") + len(cmtRevisionKey) + len(" -->")
	for name, unit := range map[string]string{"plain": "a", "escaped by JSON": "<", "invalid UTF-8": "\xff", "backtick": "`", "bidi control": "\u202e", "newline": "\n", "control": "\x01", "3-byte": "\u20ac"} {
		t.Run(name, func(t *testing.T) {
			list := func() []string { return slices.Repeat([]string{strings.Repeat(unit, 400)}, 25) }
			notice := cmtNotice()
			notice.Added, notice.Modified, notice.Removed, notice.Reduced = list(), list(), list(), true
			notice.ReducedRules, notice.ReducedPaths = asRules(list()), list()
			target := commentTarget{RepositoryID: cmtRepoID, Repo: strings.Repeat("a", 39) + "/" + strings.Repeat("a", 100), Pull: cmtPull}
			message, err := newRevisionMessage(target, notice, "co-located", "")
			if err != nil {
				t.Fatal(err)
			}
			var payload revisionPayload
			if err := json.Unmarshal(message.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			text, err := payload.text(message.Key)
			if err != nil {
				t.Fatal(err)
			}
			if n := len(text) + marker; n >= 1<<16-4<<10 {
				t.Fatalf("the comment is %d bytes with its marker, want under GitHub's 65536 characters with 4 KiB to spare", n)
			}
		})
	}
}

// A stored payload that says its reduction detail was left out, and still holds some, or that omits the detail of a change that reduces nothing,
// cannot be rendered: the publisher refuses it without posting a comment.
func TestCommentPublisherRefusesAnInconsistentOmittedReductionDetail(t *testing.T) {
	const head = `{"repositoryId":1001,"repo":"acme/widgets","pull":7,"reference":"P2-R2","previous":"P2-R1","canonical":"V1","added":[],"modified":[],"removed":[],` +
		`"addedTotal":0,"modifiedTotal":0,"removedTotal":0,"tier":"isolated"`
	for name, payload := range map[string]string{
		"omitted with rules":          head + `,"reduced":true,"reducedOmitted":true,"reducedRules":["rule /a/**"],"reducedPathsTotal":1}`,
		"omitted with paths":          head + `,"reduced":true,"reducedOmitted":true,"reducedPaths":["tests/a_test.go"],"reducedPathsTotal":1}`,
		"omitted without a reduction": head + `,"reduced":false,"reducedOmitted":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := newCmtWorld(t)

			if err := w.pub.publish(t.Context(), cmtRevisionKey, json.RawMessage(payload)); err == nil {
				t.Fatal("the publisher rendered an inconsistent payload")
			}

			if posts := w.writer.posts(); len(posts) != 0 {
				t.Fatalf("posted %d comments, want none", len(posts))
			}
		})
	}
}

// asRules marks each entry as scope.Reductions marks a removed rule.
func asRules(entries []string) []string {
	rules := make([]string, len(entries))
	for i, e := range entries {
		rules[i] = "rule " + e
	}
	return rules
}
