package app

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// The receipt of a revision comment (docs/contracts/runtime.md, "The revision comment"): the payload carries the level that the checks job read from the canonical
// declaration, the publisher adds the link of the receipt page from the settings, and a reduction whose detail does not fit keeps what it lowers.

func receiptMessage(t *testing.T, receipt string) governance.OutboxMessage {
	t.Helper()
	message, err := newRevisionMessage(cmtTarget(), cmtNotice(), "isolated", receipt)
	if err != nil {
		t.Fatalf("newRevisionMessage: %v", err)
	}
	return message
}

func TestRevisionMessageCarriesTheReceiptLevelItIsGiven(t *testing.T) {
	for _, level := range []string{"advisory", "before-approval", "required"} {
		var payload revisionPayload
		if err := json.Unmarshal(receiptMessage(t, level).Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Receipt != level {
			t.Errorf("receipt %q, want %q", payload.Receipt, level)
		}
	}
	var raw map[string]any
	if err := json.Unmarshal(receiptMessage(t, "").Payload, &raw); err != nil {
		t.Fatal(err)
	}
	if _, has := raw["receipt"]; has {
		t.Errorf("a revision with no receipt has the field: %v", raw)
	}
}

func TestRevisionMessageRefusesAReceiptLevelNobodyCanRender(t *testing.T) {
	for _, level := range []string{"off", "mandatory", "REQUIRED", " required"} {
		if m, err := newRevisionMessage(cmtTarget(), cmtNotice(), "isolated", level); err == nil {
			t.Errorf("level %q: built %s", level, m.Payload)
		}
	}
}

// With its detail dropped, a reduction that only lowers the execution policy keeps saying so: it must not read as a reduction of the scope.
func TestRevisionMessageKeepsWhatAReductionLowersWhenItsDetailIsDropped(t *testing.T) {
	list := func() []string { return slices.Repeat([]string{strings.Repeat("<", 150)}, 20) }
	execEntries := func() []string {
		return slices.Repeat([]string{"execution canonical: " + strings.Repeat("<", 150)}, 20)
	}
	for name, tc := range map[string]struct {
		rules, paths     []string
		policy, scope    bool
		lead, unwanted   string
		numbersSayDelete bool
	}{
		"only the policy": {execEntries(), nil, true, false, "**This revision lowers the test requirements.**", "reduces the protected scope", true},
		"the scope alone": {asRules(list()), list(), false, true, "**This revision reduces the protected scope.**", "test requirements", false},
		"both":            {append(asRules(list()), execEntries()[0]), list(), true, true, "**This revision reduces the protected scope and lowers the test requirements.**", "", false},
	} {
		t.Run(name, func(t *testing.T) {
			notice := cmtNotice()
			notice.Added, notice.Modified, notice.Removed, notice.Reduced = list(), list(), list(), true
			notice.ReducedRules, notice.ReducedPaths = tc.rules, tc.paths

			message := cmtRevisionMessage(t, notice, "isolated")

			var payload revisionPayload
			if err := json.Unmarshal(message.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if !payload.ReducedOmitted || len(payload.ReducedRules) != 0 || payload.ReducedPolicy != tc.policy || payload.ReducedScope != tc.scope {
				t.Fatalf("payload %+v, want the detail dropped and policy %v, scope %v kept", payload, tc.policy, tc.scope)
			}
			w := newCmtWorld(t)
			w.mustDeliver(message)
			body := w.writer.posts()[0].body
			for _, want := range []string{tc.lead, "left out", "/suiteward approve P2-R2 <code>"} {
				if !strings.Contains(body, want) {
					t.Errorf("comment lacks %q:\n%s", want, body)
				}
			}
			if tc.unwanted != "" && strings.Contains(body, tc.unwanted) {
				t.Errorf("comment contains %q:\n%s", tc.unwanted, body)
			}
		})
	}
}

// A detail that fits is not summarized: the entries say it, and the two fields stay out of the payload.
func TestRevisionMessageThatKeepsItsReductionDetailHasNoLoweringFields(t *testing.T) {
	notice := cmtNotice()
	notice.Reduced, notice.ReducedRules = true, []string{"execution canonical: required -> advisory", "rule /a/**"}
	var raw map[string]any
	if err := json.Unmarshal(cmtRevisionMessage(t, notice, "isolated").Payload, &raw); err != nil {
		t.Fatal(err)
	}
	if _, has := raw["reducedPolicy"]; has {
		t.Errorf("payload %v has the fields of a dropped detail", raw)
	}
	if _, has := raw["reducedScope"]; has {
		t.Errorf("payload %v has the fields of a dropped detail", raw)
	}
}

func receiptPayload(t *testing.T, edit func(*revisionPayload)) json.RawMessage {
	t.Helper()
	var payload revisionPayload
	if err := json.Unmarshal(receiptMessage(t, "required").Payload, &payload); err != nil {
		t.Fatal(err)
	}
	edit(&payload)
	return cmtMust(json.Marshal(payload))
}

func TestRevisionCommentPostsTheReceiptLineWithTheLinkOfThePullRequestPage(t *testing.T) {
	w := newCmtWorld(t)
	w.pub.reportBase = "http://127.0.0.1:8080"
	w.mustDeliver(receiptMessage(t, "required"))
	body := w.writer.posts()[0].body
	for _, want := range []string{
		"Receipt: SuiteWard runs the tests this revision adds or changes, on this commit and on the code before the change, and shows what it saw. It is evidence, not a verdict. " +
			"Results: [Pull request page](http://127.0.0.1:8080/pulls/acme/widgets/7). The `SuiteWard / Receipt` check carries the counts.",
		"To approve, name a mode (the kind of change) that the receipt meets, and comment:", "```\n/suiteward approve P2-R2 <fix|feature|refactor> <code>\n```",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("comment lacks %q:\n%s", want, body)
		}
	}
}

func TestRevisionCommentPostsTheReceiptLineWithoutALinkWhenTheInstanceHasNoReportBase(t *testing.T) {
	w := newCmtWorld(t)
	w.mustDeliver(receiptMessage(t, "advisory"))
	body := w.writer.posts()[0].body
	if !strings.Contains(body, "Results: see the `SuiteWard / Receipt` check. The `SuiteWard / Receipt` check carries the counts.") || strings.Contains(body, "/receipts/") || strings.Contains(body, "/pulls/") ||
		!strings.Contains(body, "```\n/suiteward approve P2-R2 <code>\n```") {
		t.Errorf("comment:\n%s", body)
	}
}

// A link that the renderer refuses drops the link and never the line: the receipt is part of what the owner approves under.
func TestRevisionCommentKeepsTheReceiptLineWhenItsLinkIsRefused(t *testing.T) {
	for _, base := range []string{"https://127.0.0.1:8080", "http://example.com:8080", "http://127.0.0.1:8080/x", "http://127.0.0.1:8080 "} {
		w := newCmtWorld(t)
		w.pub.reportBase = base
		w.mustDeliver(receiptMessage(t, "before-approval"))
		body := w.writer.posts()[0].body
		if !strings.Contains(body, "Results: see the `SuiteWard / Receipt` check.") || !strings.Contains(body, "Approving waits until the receipt is complete.") || strings.Contains(body, "/receipts/") || strings.Contains(body, "/pulls/") {
			t.Errorf("base %q: the line was lost or kept a refused link:\n%s", base, body)
		}
		if !strings.Contains(w.logs.String(), "receipt line") {
			t.Errorf("base %q: the refused link is not logged: %s", base, w.logs.String())
		}
	}
}

// The link of the pull request page and the link of the change report are independent: the one that cannot be rendered is the one that goes.
func TestRevisionCommentKeepsTheChangeReportLinkNextToTheReceiptLink(t *testing.T) {
	w := newCmtWorld(t)
	w.pub.reportBase = cmtReportBase
	w.reports.report, w.reports.err = revisionReport{Base: "V1", Report: smallReport(t)}, nil
	w.mustDeliver(receiptMessage(t, "advisory"))
	body := w.writer.posts()[0].body
	if !strings.Contains(body, "[Full change report]("+cmtReportBase+"/reports/acme/widgets/P2-R2)") || !strings.Contains(body, "Results: [Pull request page]("+cmtReportBase+"/pulls/acme/widgets/7).") {
		t.Errorf("comment lacks one of the two links:\n%s", body)
	}
	if strings.Index(body, "[Full change report]") > strings.Index(body, "Receipt:") || strings.Index(body, "Receipt:") > strings.Index(body, "/suiteward approve") {
		t.Errorf("the receipt line is not between the change report and the approve command:\n%s", body)
	}
}

func TestCommentPublisherRefusesAnInconsistentReceiptPayload(t *testing.T) {
	for name, edit := range map[string]func(*revisionPayload){
		"a level that is off":                        func(p *revisionPayload) { p.Receipt = "off" },
		"a level nobody knows":                       func(p *revisionPayload) { p.Receipt = cmtLeakText },
		"a lowered policy without an omitted detail": func(p *revisionPayload) { p.Reduced, p.ReducedPolicy = true, true },
		"a lowered scope without an omitted detail":  func(p *revisionPayload) { p.Reduced, p.ReducedScope = true, true },
		"a lowered policy on a revision that is not": func(p *revisionPayload) { p.ReducedPolicy = true },
		"a lowering that the omitted detail contradicts": func(p *revisionPayload) {
			p.Reduced, p.ReducedOmitted, p.ReducedRules, p.ReducedPolicy = true, true, []string{"rule a"}, true
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newCmtWorld(t)
			err := w.pub.publish(t.Context(), cmtRevisionKey, receiptPayload(t, edit))
			if err == nil || strings.Contains(err.Error(), cmtLeakText) {
				t.Fatalf("err = %v, want one that does not quote the payload", err)
			}
			if posts := w.writer.posts(); len(posts) != 0 {
				t.Fatalf("posted %d comments, want none", len(posts))
			}
		})
	}
}

// The checks job reads the canonical declaration and the two manifests when it writes the message (docs/contracts/runtime.md, "The revision comment").

func (w *cjJobWorld) revisionReceipt() (string, bool) {
	w.t.Helper()
	messages := w.revisionMessages()
	if len(messages) != 1 {
		w.t.Fatalf("%d revision comments, want 1 (keys %v)\nlogs: %s", len(messages), w.revisionKeys(), w.logs)
	}
	var payload map[string]any
	if err := json.Unmarshal(messages[0].Payload, &payload); err != nil {
		w.t.Fatal(err)
	}
	receipt, has := payload["receipt"]
	if !has {
		return "", false
	}
	return receipt.(string), true
}

func TestChecksWriteTheReceiptLevelOfARevisionWhoseReceiptApplies(t *testing.T) {
	for _, level := range []string{"advisory", "before-approval", "required"} {
		t.Run(level, func(t *testing.T) {
			w := newExecWorld(t, execDeclaration("off", level))

			w.execPull(execHeadFiles(execDeclaration("off", level), "v2")) // a test of the include changes

			if got, has := w.revisionReceipt(); !has || got != level {
				t.Fatalf("receipt %q (set %v), want %q", got, has, level)
			}
		})
	}
}

func TestChecksWriteNoReceiptForARevisionWhoseReceiptDoesNotApplyOrIsOff(t *testing.T) {
	changed := execDeclaration("advisory", "required") + "# a comment\n" // the declaration changes: protected, and no file of the include
	for name, tc := range map[string]struct {
		canonical string
		head      map[string]string
	}{
		"the level is off":             {execDeclaration("advisory", "off"), execHeadFiles(execDeclaration("advisory", "off"), "v2")},
		"no execution block":           {"version: 1\ninclude: [/tests/**]\n", execHeadFiles("version: 1\ninclude: [/tests/**]\n", "v2")},
		"no file of the include moved": {execDeclaration("advisory", "required"), execHeadFiles(changed, "v1")},
	} {
		t.Run(name, func(t *testing.T) {
			w := newExecWorld(t, tc.canonical)

			w.execPull(tc.head)

			if got, has := w.revisionReceipt(); has {
				t.Fatalf("receipt %q on a revision whose receipt does not apply", got)
			}
		})
	}
}

// A head is assessed once the policy can be read, so that its comment, which is posted once and never edited, states the receipt.
func TestChecksWaitToWriteTheRevisionWhileTheExecutionPolicyCannotBeRead(t *testing.T) {
	decl := execDeclaration("off", "required")
	w := newExecWorld(t, decl)
	healthy := w.check.contents
	w.check.contents = &failingFirstRead{ContentReader: healthy, failures: 1} // the read of the policy fails, the next ones do not
	w.tree(cjHead2, execHeadFiles(decl, "v2"))
	w.sync(map[int]string{5: cjHead2})
	w.check.liveness.Saw(w.clock.Add(1))

	if err := w.runChecks(); err == nil {
		t.Fatal("the run succeeded while the policy could not be read")
	}

	if keys := w.revisionKeys(); len(keys) != 0 {
		t.Fatalf("the revision was written while the policy could not be read: %v", keys)
	}
	w.want(5, checks.InProgress, "assessment-pending", 1)

	w.execRun()

	if got, has := w.revisionReceipt(); !has || got != "required" {
		t.Fatalf("receipt %q (set %v), want required once the policy can be read", got, has)
	}
}

// failingFirstRead is a content reader whose first reads fail.
type failingFirstRead struct {
	governance.ContentReader
	failures int
}

func (f *failingFirstRead) Read(ctx context.Context, digest artifact.Digest) ([]byte, error) {
	if f.failures > 0 {
		f.failures--
		return nil, errors.New("the content store is unreachable")
	}
	return f.ContentReader.Read(ctx, digest)
}

// What a reduction lowers is told from the entries that scope.Reductions writes, exactly as the renderer takes them: the same two prefixes, and nothing that merely begins
// with the same word.
func TestReductionKindsTellsTheExecutionEntriesOfScopeReductionsFromEveryOtherRule(t *testing.T) {
	for name, tc := range map[string]struct {
		rules, paths  []string
		policy, scope bool
	}{
		"the canonical level":               {[]string{"execution canonical: required -> advisory"}, nil, true, false},
		"the receipt level":                 {[]string{"execution receipt: advisory -> off"}, nil, true, false},
		"both levels":                       {[]string{"execution canonical: required -> off", "execution receipt: required -> off"}, nil, true, false},
		"a rule":                            {[]string{"rule /a/**"}, nil, false, true},
		"an exclude":                        {[]string{"exclude /a/b_test.go"}, nil, false, true},
		"a path":                            {nil, []string{"a/b_test.go"}, false, true},
		"a level and a rule":                {[]string{"execution receipt: advisory -> off", "rule /a/**"}, nil, true, true},
		"a level and a path":                {[]string{"execution canonical: required -> advisory"}, []string{"a/b_test.go"}, true, true},
		"a word that begins like an entry":  {[]string{"execution policy: required -> off"}, nil, false, true},
		"an entry of another kind of level": {[]string{"executions receipt: advisory -> off"}, nil, false, true},
		"an exclude that names an entry":    {[]string{"exclude execution canonical: x"}, nil, false, true},
		"an entry without its colon":        {[]string{"execution canonical required -> off"}, nil, false, true},
		"an entry in upper case":            {[]string{"Execution canonical: required -> off"}, nil, false, true},
		"nothing":                           {nil, nil, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			policy, scope := reductionKinds(governance.RevisionNotice{ReducedRules: tc.rules, ReducedPaths: tc.paths})
			if policy != tc.policy || scope != tc.scope {
				t.Errorf("reductionKinds = policy %v, scope %v; want %v and %v", policy, scope, tc.policy, tc.scope)
			}
		})
	}
}
