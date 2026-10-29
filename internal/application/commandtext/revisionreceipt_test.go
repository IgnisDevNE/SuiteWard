package commandtext_test

import (
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// The receipt line of the revision comment (docs/contracts/runtime.md, "The revision comment"): one fixed line after the change report and before the approve command,
// only when the revision has a receipt, with the link of the pull request page when the instance has a report base URL, and the sentences of the level.

const (
	receiptURL = "http://127.0.0.1:8080/receipts/acme/widgets/7" // the old page, which the revision comment no longer links

	receiptLead = "Receipt: SuiteWard runs the tests this revision adds or changes, on the head and on the code before the change, and shows what it saw. It is evidence, not a verdict. "
	receiptTail = "The `SuiteWard / Receipt` check carries the counts."
)

// receiptRevision is the revision of pull request 7 with the numbers of its change report and a receipt of the level.
func receiptRevision(t *testing.T, level string) commandtext.Revision {
	t.Helper()
	r := revisionWith(t, commandtext.Isolated, smallChange()...)
	r.Receipt, r.PullURL, r.Pull = level, pullURL, 7
	return r
}

func TestRevisionCommentWithoutAReceiptHasNoReceiptLine(t *testing.T) {
	body := revisionText(t, revisionWith(t, commandtext.Isolated, smallChange()...))
	if strings.Contains(body, "Receipt") || strings.Contains(body, "/receipts/") || strings.Contains(body, "/pulls/") || strings.Contains(body, "mode") {
		t.Errorf("a revision with no receipt speaks of one:\n%s", body)
	}
}

func TestRevisionCommentStatesTheReceiptAfterTheChangesAndBeforeTheApproveCommand(t *testing.T) {
	for _, level := range []string{"advisory", "before-approval", "required"} {
		body := revisionText(t, receiptRevision(t, level))
		line := receiptLead + "Results: [Pull request page](" + pullURL + "). " + receiptTail
		report := strings.Index(body, "[Full change report]")
		at := strings.Index(body, line)
		approve := strings.Index(body, "/suiteward approve P7-R3")
		if report < 0 || at <= report || approve <= at {
			t.Errorf("%s: the receipt line is not between the change report and the approve command (%d, %d, %d):\n%s", level, report, at, approve, body)
		}
		if strings.Count(body, pullURL) != 1 {
			t.Errorf("%s: the address of the pull request page is link text as well as the target:\n%s", level, body)
		}
		if strings.Count(body, "Receipt:") != 1 {
			t.Errorf("%s: the receipt line is not stated once:\n%s", level, body)
		}
		if !strings.HasSuffix(body, "This supersedes `P7-R2`.\n") {
			t.Errorf("%s: the supersede line is not last:\n%s", level, body)
		}
		checkBodyInert(t, body)
	}
}

func TestRevisionCommentWithoutAReportBaseSendsTheReaderToTheCheck(t *testing.T) {
	r := receiptRevision(t, "advisory")
	r.PullURL = ""
	body := revisionText(t, r)
	if !strings.Contains(body, receiptLead+"Results: see the `SuiteWard / Receipt` check. "+receiptTail) || strings.Contains(body, "/receipts/") || strings.Contains(body, "/pulls/") {
		t.Errorf("the line does not send the reader to the check:\n%s", body)
	}
}

func TestRevisionCommentOfAnAdvisoryReceiptOffersTheUsualApproveCommand(t *testing.T) {
	body := revisionText(t, receiptRevision(t, "advisory"))
	if !strings.Contains(body, "To approve this exact revision, comment:\n\n```\n/suiteward approve P7-R3 <code>\n```") ||
		strings.Contains(body, "Approving waits") || strings.Contains(body, "Approving needs") || strings.Contains(body, "<fix|feature|refactor>") {
		t.Errorf("an advisory receipt changes the approval:\n%s", body)
	}
}

func TestRevisionCommentOfABeforeApprovalReceiptSaysThatApprovingWaitsForTheReceipt(t *testing.T) {
	body := revisionText(t, receiptRevision(t, "before-approval"))
	waits := strings.Index(body, "Approving waits until the receipt is complete.")
	approve := strings.Index(body, "/suiteward approve P7-R3 <code>")
	if waits < 0 || approve <= waits || strings.Contains(body, "<fix|feature|refactor>") {
		t.Errorf("the comment does not say that approving waits, before the usual command:\n%s", body)
	}
}

// Under required the approve needs a mode: the copyable command has its placeholder, in place of the usual one.
func TestRevisionCommentOfARequiredReceiptOffersTheCommandWithAModePlaceholder(t *testing.T) {
	body := revisionText(t, receiptRevision(t, "required"))
	needs := strings.Index(body, "Approving needs a mode and a receipt that meets it:")
	command := strings.Index(body, "```\n/suiteward approve P7-R3 <fix|feature|refactor> <code>\n```")
	if needs < 0 || command <= needs {
		t.Errorf("the comment does not offer the command with the mode, after saying what approving needs:\n%s", body)
	}
	if strings.Contains(body, "/suiteward approve P7-R3 <code>") || strings.Contains(body, "Approving waits") {
		t.Errorf("the comment still offers the usual command, or says that approving waits:\n%s", body)
	}
	if parsed := commandtext.Parse(body); parsed.Kind != commandtext.KindNotCommand {
		t.Errorf("the body reads as the command %+v:\n%s", parsed, body)
	}
}

func TestRevisionCommentRefusesAReceiptThatDoesNotFit(t *testing.T) {
	with := func(edit func(*commandtext.Revision)) commandtext.Revision {
		r := receiptRevision(t, "required")
		edit(&r)
		return r
	}
	for name, r := range map[string]commandtext.Revision{
		"a level that is off":                    with(func(r *commandtext.Revision) { r.Receipt = "off" }),
		"a level that is not one":                with(func(r *commandtext.Revision) { r.Receipt = "mandatory" }),
		"a level in upper case":                  with(func(r *commandtext.Revision) { r.Receipt = "REQUIRED" }),
		"a link without a receipt":               with(func(r *commandtext.Revision) { r.Receipt = "" }),
		"the link of the receipt page":           with(func(r *commandtext.Revision) { r.PullURL = receiptURL }),
		"a link of another host":                 with(func(r *commandtext.Revision) { r.PullURL = "http://example.com:8080/pulls/acme/widgets/7" }),
		"a link that is not loopback":            with(func(r *commandtext.Revision) { r.PullURL = "http://192.168.1.5:8080/pulls/acme/widgets/7" }),
		"a link over https":                      with(func(r *commandtext.Revision) { r.PullURL = "https://127.0.0.1:8080/pulls/acme/widgets/7" }),
		"a link with no port":                    with(func(r *commandtext.Revision) { r.PullURL = "http://127.0.0.1/pulls/acme/widgets/7" }),
		"a link of another pull request":         with(func(r *commandtext.Revision) { r.PullURL = "http://127.0.0.1:8080/pulls/acme/widgets/8" }),
		"a link with a pull that has a zero":     with(func(r *commandtext.Revision) { r.PullURL = "http://127.0.0.1:8080/pulls/acme/widgets/07" }),
		"a link of the change report":            with(func(r *commandtext.Revision) { r.PullURL = reportURL }),
		"a link with a query":                    with(func(r *commandtext.Revision) { r.PullURL = pullURL + "?x=1" }),
		"a link with a fragment":                 with(func(r *commandtext.Revision) { r.PullURL = pullURL + "#x" }),
		"a link with a path that climbs":         with(func(r *commandtext.Revision) { r.PullURL = "http://127.0.0.1:8080/pulls/../widgets/7" }),
		"a link with a space":                    with(func(r *commandtext.Revision) { r.PullURL = "http://127.0.0.1:8080/pulls/acme/wid gets/7" }),
		"a link with a closing parenthesis":      with(func(r *commandtext.Revision) { r.PullURL = "http://127.0.0.1:8080/pulls/acme/widgets)/7" }),
		"a link that ends with text":             with(func(r *commandtext.Revision) { r.PullURL = pullURL + " and more" }),
		"a link and no pull request":             with(func(r *commandtext.Revision) { r.Pull = 0 }),
		"a link with a negative pull request":    with(func(r *commandtext.Revision) { r.Pull = -7 }),
		"a link that names a report":             with(func(r *commandtext.Revision) { r.PullURL = "http://127.0.0.1:8080/reports/acme/widgets/P7-R3" }),
		"a link of another path under pulls":     with(func(r *commandtext.Revision) { r.PullURL = "http://127.0.0.1:8080/pulls/acme/widgets/7/x" }),
		"a link that holds a hidden character":   with(func(r *commandtext.Revision) { r.PullURL = "http://127.0.0.1:8080/pulls/acme/wid\u202egets/7" }),
		"a link that holds an HTML comment mark": with(func(r *commandtext.Revision) { r.PullURL = "http://127.0.0.1:8080/pulls/acme/<!--/7" }),
	} {
		t.Run(name, func(t *testing.T) { mustPanic(t, func() { commandtext.RevisionComment(r) }) })
	}
}

// A reduction whose detail was left out can no longer say what it lowers from its entries, so the revision says it.
func TestRevisionCommentOfAnOmittedReductionSaysWhatItLowers(t *testing.T) {
	omitted := func(policy, scope bool) commandtext.Revision {
		r := revisionWith(t, commandtext.Isolated, removalChange()...)
		r.Reduced, r.ReducedOmitted, r.LowersPolicy, r.ReducesScope = true, true, policy, scope
		return r
	}
	const notice = "The detail of this reduction was omitted because it did not fit in this comment."
	body := revisionText(t, omitted(true, false))
	for _, want := range []string{"**This revision lowers the execution policy.**", notice, "- Files: 1 added, 0 modified, 2 removed\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "reduces the protected scope") || strings.Contains(body, "from the protected files") {
		t.Errorf("a revision that only lowers the execution policy says that it reduces the scope:\n%s", body)
	}

	both := revisionText(t, omitted(true, true))
	if !strings.Contains(both, "**This revision reduces the protected scope and lowers the execution policy.**") || !strings.Contains(both, notice) ||
		!strings.Contains(both, "2 removed from the protected files") {
		t.Errorf("a revision that does both does not say so:\n%s", both)
	}

	scopeOnly := revisionText(t, omitted(false, true))
	if !strings.Contains(scopeOnly, "**This revision reduces the protected scope.**") || strings.Contains(scopeOnly, "execution policy") {
		t.Errorf("a revision that only reduces the scope speaks of the policy:\n%s", scopeOnly)
	}

	neither := revisionText(t, omitted(false, false)) // a message that was queued before the flags existed
	if !strings.Contains(neither, "**This revision reduces the protected scope.**") || strings.Contains(neither, "execution policy") {
		t.Errorf("an omitted reduction with neither flag does not read as it did:\n%s", neither)
	}
}

func TestRevisionCommentRefusesWhatAReductionHasNoDetailToSay(t *testing.T) {
	r := revision(commandtext.Isolated)
	r.LowersPolicy = true // no reduction
	mustPanic(t, func() { commandtext.RevisionComment(r) })
	r = revision(commandtext.Isolated)
	r.Reduced, r.ReducedRules, r.ReducesScope = true, []string{"rule a/**"}, true // the detail is listed: nothing says what it lowers but the entries
	mustPanic(t, func() { commandtext.RevisionComment(r) })
}
