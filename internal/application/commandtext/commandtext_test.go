package commandtext_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

const (
	instance = "0123456789abcdef0123456789abcdef"
	owner    = "octo-owner"
)

// mustPanic fails unless f panics with something other than a stub's message.
func mustPanic(t *testing.T, f func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Error("did not panic")
		} else if r == "not implemented" {
			t.Error("panicked as an unimplemented stub")
		}
	}()
	f()
}

// render reports a panic as a test error so every case shows its own failure.
func render(t *testing.T, f func() string) (body string) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("panicked: %v", r)
			body = ""
		}
	}()
	return f()
}

func command(verb commandtext.Verb, proposal, revision int, code string) commandtext.Parsed {
	return commandtext.Parsed{Kind: commandtext.KindCommand, Command: commandtext.Command{Verb: verb, Proposal: proposal, Revision: revision, Code: code}}
}

func malformed(reason commandtext.Reason) commandtext.Parsed {
	return commandtext.Parsed{Kind: commandtext.KindMalformed, Reason: reason}
}

var notCommand = commandtext.Parsed{Kind: commandtext.KindNotCommand}

func TestParse(t *testing.T) {
	cases := []struct {
		name string
		body string
		want commandtext.Parsed
	}{
		// Valid forms.
		{"approve", "/suiteward approve P1-R1 123456", command(commandtext.Approve, 1, 1, "123456")},
		{"revoke", "/suiteward revoke P42-R3 654321", command(commandtext.Revoke, 42, 3, "654321")},
		{"largest ids", "/suiteward approve P999999999-R999999999 000000", command(commandtext.Approve, 999999999, 999999999, "000000")},
		{"extra spaces and tabs", " \t /suiteward \t approve  \tP7-R2\t\t 012345 \t", command(commandtext.Approve, 7, 2, "012345")},
		{"CRLF", "/suiteward revoke P5-R1 111111\r\nthanks\r\n", command(commandtext.Revoke, 5, 1, "111111")},
		{"CRLF on a later line", "Looks good.\r\n/suiteward approve P5-R1 111111\r\n", command(commandtext.Approve, 5, 1, "111111")},
		{"command on a later line", "Reviewed the inventory.\n\n/suiteward approve P2-R4 222222", command(commandtext.Approve, 2, 4, "222222")},
		{"text before and after", "LGTM\n/suiteward approve P3-R1 333333\nShip it.\n/other text", command(commandtext.Approve, 3, 1, "333333")},
		{"after a closed fence", "```\n/suiteward approve P9-R9 999999\n```\n/suiteward approve P1-R2 121212", command(commandtext.Approve, 1, 2, "121212")},
		{"after a closed tilde fence", "~~~~sh\n/suiteward revoke P9-R9 999999\n~~~~\n/suiteward revoke P1-R2 121212", command(commandtext.Revoke, 1, 2, "121212")},
		{"after a longer closing fence", "```\n/suiteward approve P9-R9 999999\n`````\n/suiteward approve P1-R2 121212", command(commandtext.Approve, 1, 2, "121212")},

		// Not a command.
		{"empty body", "", notCommand},
		{"no command line", "Looks good to me.\nThanks!", notCommand},
		{"only inside a fence", "```\n/suiteward approve P1-R1 123456\n```", notCommand},
		{"only inside an indented tilde fence", "  ~~~\n/suiteward approve P1-R1 123456\n  ~~~\n", notCommand},
		{"in the middle of a line", "please run /suiteward approve P1-R1 123456", notCommand},
		{"suiteward-x", "/suiteward-x approve P1-R1 123456", notCommand},
		{"suitewardx", "/suitewardx approve P1-R1 123456", notCommand},
		{"uppercase", "/SUITEWARD approve P1-R1 123456", notCommand},
		{"mixed case", "/SuiteWard approve P1-R1 123456", notCommand},
		{"no-break space after the prefix", "/suiteward\u00a0approve P1-R1 123456", notCommand},
		{"quoted", "> /suiteward approve P1-R1 123456", notCommand},

		// Fences.
		{"unclosed fence hides the rest", "```\nsome code\n/suiteward approve P1-R1 123456\nmore", notCommand},
		{"unclosed tilde fence hides the rest", "~~~\n/suiteward revoke P1-R1 123456", notCommand},
		{"tilde does not close a backtick fence", "```\n~~~\n/suiteward approve P1-R1 123456\n```", notCommand},
		{"shorter fence does not close", "````\n```\n/suiteward approve P1-R1 123456\n````", notCommand},
		{"fence with info string does not close", "```\n```go\n/suiteward approve P1-R1 123456\n```", notCommand},

		// Malformed.
		{"bare prefix", "/suiteward", malformed(commandtext.UnknownVerb)},
		{"bare prefix with trailing space", "/suiteward  \t", malformed(commandtext.UnknownVerb)},
		{"unknown verb", "/suiteward merge P1-R1 123456", malformed(commandtext.UnknownVerb)},
		{"uppercase verb", "/suiteward Approve P1-R1 123456", malformed(commandtext.UnknownVerb)},
		{"missing reference", "/suiteward approve", malformed(commandtext.MissingReference)},
		{"missing reference with trailing tab", "/suiteward revoke\t", malformed(commandtext.MissingReference)},
		{"reference without revision", "/suiteward approve P1 123456", malformed(commandtext.BadReference)},
		{"lowercase reference", "/suiteward approve p1-r1 123456", malformed(commandtext.BadReference)},
		{"leading zero in proposal", "/suiteward approve P01-R1 123456", malformed(commandtext.BadReference)},
		{"leading zero in revision", "/suiteward approve P1-R01 123456", malformed(commandtext.BadReference)},
		{"P0", "/suiteward approve P0-R1 123456", malformed(commandtext.BadReference)},
		{"R0", "/suiteward approve P1-R0 123456", malformed(commandtext.BadReference)},
		{"plus sign", "/suiteward approve P+1-R1 123456", malformed(commandtext.BadReference)},
		{"minus sign", "/suiteward approve P-1-R1 123456", malformed(commandtext.BadReference)},
		{"overflow above 999999999", "/suiteward approve P1000000000-R1 123456", malformed(commandtext.BadReference)},
		{"revision overflow", "/suiteward approve P1-R1000000000 123456", malformed(commandtext.BadReference)},
		{"huge proposal", "/suiteward approve P99999999999999999999999-R1 123456", malformed(commandtext.BadReference)},
		{"fullwidth digit in reference", "/suiteward approve P\uff11-R1 123456", malformed(commandtext.BadReference)},
		{"empty proposal", "/suiteward approve P-R1 123456", malformed(commandtext.BadReference)},
		{"trailing text in reference", "/suiteward approve P1-R1x 123456", malformed(commandtext.BadReference)},
		{"missing code", "/suiteward approve P1-R1", malformed(commandtext.MissingCode)},
		{"missing code with trailing space", "/suiteward approve P1-R1 ", malformed(commandtext.MissingCode)},
		{"5-digit code", "/suiteward approve P1-R1 12345", malformed(commandtext.BadCode)},
		{"7-digit code", "/suiteward approve P1-R1 1234567", malformed(commandtext.BadCode)},
		{"letter in code", "/suiteward approve P1-R1 12345a", malformed(commandtext.BadCode)},
		{"fullwidth digit in code", "/suiteward approve P1-R1 12345\uff16", malformed(commandtext.BadCode)},
		{"signed code", "/suiteward approve P1-R1 +12345", malformed(commandtext.BadCode)},
		{"code with no-break space", "/suiteward approve P1-R1 123456\u00a0", malformed(commandtext.BadCode)},
		{"text after the code", "/suiteward approve P1-R1 123456 please", malformed(commandtext.ExtraText)},
		{"second code after the code", "/suiteward revoke P1-R1 123456 654321", malformed(commandtext.ExtraText)},

		// Only the first command line counts.
		{"first of two valid lines", "/suiteward approve P1-R1 111111\n/suiteward revoke P2-R2 222222", command(commandtext.Approve, 1, 1, "111111")},
		{"malformed first line wins over a valid later one", "/suiteward approve P1-R1\n/suiteward approve P1-R1 123456", malformed(commandtext.MissingCode)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandtext.Parse(tc.body); got != tc.want {
				t.Errorf("Parse(%q) = %+v, want %+v", tc.body, got, tc.want)
			}
		})
	}
}

func TestReference(t *testing.T) {
	cases := []struct {
		name               string
		proposal, revision int
		want               string
	}{
		{"smallest", 1, 1, "P1-R1"},
		{"typical", 42, 3, "P42-R3"},
		{"largest", 999999999, 999999999, "P999999999-R999999999"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := render(t, func() string { return commandtext.Reference(tc.proposal, tc.revision) })
			if got != tc.want {
				t.Fatalf("Reference = %q, want %q", got, tc.want)
			}
			parsed := commandtext.Parse("/suiteward revoke " + got + " 123456")
			if want := command(commandtext.Revoke, tc.proposal, tc.revision, "123456"); parsed != want {
				t.Errorf("Parse round trip = %+v, want %+v", parsed, want)
			}
		})
	}
}

func TestReferencePanicsOnImpossibleIDs(t *testing.T) {
	cases := []struct {
		name               string
		proposal, revision int
	}{
		{"proposal 0", 0, 1},
		{"revision 0", 1, 0},
		{"negative proposal", -1, 1},
		{"proposal 1000000000", 1000000000, 1},
		{"revision 1000000000", 1, 1000000000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mustPanic(t, func() { commandtext.Reference(tc.proposal, tc.revision) })
		})
	}
}

var (
	sixDigits = regexp.MustCompile(`(^|[^0-9])[0-9]{6}([^0-9]|$)`)
	testClaim = regexp.MustCompile(`(?i)\b(passed|executed|ran|succeeded)\b`)
)

// checkBody holds for every rendered body: no code value, no test claim, and
// the <code> placeholder only inside code.
func checkBody(t *testing.T, body string) {
	t.Helper()
	if sixDigits.MatchString(body) {
		t.Errorf("body contains a 6-digit value:\n%s", body)
	}
	if testClaim.MatchString(body) {
		t.Errorf("body claims a test result:\n%s", body)
	}
	if strings.Contains(body, "<!--") {
		t.Errorf("body contains a marker or HTML comment:\n%s", body)
	}
}

func TestAcknowledge(t *testing.T) {
	cases := []struct {
		name    string
		ack     commandtext.Ack
		want    []string
		notWant []string
	}{
		{
			"approved and eligible",
			commandtext.Ack{Outcome: commandtext.Approved, Proposal: 42, Revision: 3, Owner: owner, Eligible: true},
			[]string{"Approval recorded for P42-R3 by @octo-owner.", "P42-R3 is now eligible for promotion.", "To withdraw this approval, comment:", "/suiteward revoke P42-R3 <code>", "`<code>`"},
			[]string{"not yet eligible"},
		},
		{
			"approved and not yet eligible",
			commandtext.Ack{Outcome: commandtext.Approved, Proposal: 42, Revision: 3, Owner: owner},
			[]string{"Approval recorded for P42-R3 by @octo-owner.", "P42-R3 is not yet eligible for promotion", "/suiteward revoke P42-R3 <code>"},
			[]string{"is now eligible"},
		},
		{
			"revoked and still eligible",
			commandtext.Ack{Outcome: commandtext.Revoked, Proposal: 7, Revision: 1, Owner: owner, Eligible: true},
			[]string{"Revocation recorded for P7-R1 by @octo-owner.", "Your approval no longer counts.", "P7-R1 remains eligible for promotion through other valid approval.", "/suiteward approve P7-R1 <code>"},
			[]string{"awaiting valid approval", "/suiteward revoke"},
		},
		{
			"revoked and no longer eligible",
			commandtext.Ack{Outcome: commandtext.Revoked, Proposal: 7, Revision: 1, Owner: owner},
			[]string{"Revocation recorded for P7-R1 by @octo-owner.", "Your approval no longer counts.", "Promotion of P7-R1 is awaiting valid approval."},
			[]string{"remains eligible", "/suiteward revoke"},
		},
		{
			"no active approval",
			commandtext.Ack{Outcome: commandtext.NoActiveApproval, Proposal: 7, Revision: 2, Owner: owner},
			[]string{"No active approval by @octo-owner for P7-R2 to withdraw; nothing changed.", "P7-R2 is not eligible for promotion."},
			[]string{"/suiteward revoke"},
		},
		{
			"no active approval while eligible",
			commandtext.Ack{Outcome: commandtext.NoActiveApproval, Proposal: 7, Revision: 2, Owner: owner, Eligible: true},
			[]string{"No active approval by @octo-owner for P7-R2 to withdraw; nothing changed.", "P7-R2 is eligible for promotion."},
			[]string{"not eligible"},
		},
		{
			"rejected unauthorized",
			commandtext.Ack{Outcome: commandtext.Rejected, Rejection: commandtext.Unauthorized, Proposal: 3, Revision: 1, Owner: owner},
			[]string{"Command for P3-R1 rejected by policy: @octo-owner is not authorized for this action under the project's policy. Nothing changed.", "P3-R1 is not eligible for promotion."},
			[]string{"/suiteward revoke"},
		},
		{
			"rejected unknown revision",
			commandtext.Ack{Outcome: commandtext.Rejected, Rejection: commandtext.UnknownRevision, Proposal: 3, Revision: 9, Owner: owner},
			[]string{"Command for P3-R9 rejected by policy: P3-R9 is not a revision of this project. Nothing changed.", "Check the reference"},
			nil,
		},
		{
			"rejected superseded revision",
			commandtext.Ack{Outcome: commandtext.Rejected, Rejection: commandtext.SupersededRevision, Proposal: 3, Revision: 1, Owner: owner},
			[]string{"Command for P3-R1 rejected by policy: P3-R1 has been superseded by a newer revision. Nothing changed.", "latest revision"},
			nil,
		},
		{
			"rejected context mismatch",
			commandtext.Ack{Outcome: commandtext.Rejected, Rejection: commandtext.ContextMismatch, Proposal: 3, Revision: 1, Owner: owner},
			[]string{"Command for P3-R1 rejected by policy: P3-R1 no longer matches its source or integration target. Nothing changed."},
			nil,
		},
		{
			"rejected policy mismatch",
			commandtext.Ack{Outcome: commandtext.Rejected, Rejection: commandtext.PolicyMismatch, Proposal: 3, Revision: 1, Owner: owner},
			[]string{"Command for P3-R1 rejected by policy: the project's policy changed after P3-R1 was proposed. Nothing changed."},
			nil,
		},
		{
			"rejected obsolete command while eligible",
			commandtext.Ack{Outcome: commandtext.Rejected, Rejection: commandtext.ObsoleteCommand, Proposal: 3, Revision: 1, Owner: owner, Eligible: true},
			[]string{"Command for P3-R1 rejected by policy: a later command for P3-R1 was already processed. Nothing changed.", "P3-R1 is eligible for promotion."},
			nil,
		},
		{
			"malformed unknown verb",
			commandtext.Ack{Outcome: commandtext.MalformedCommand, Malformed: commandtext.UnknownVerb, Owner: owner},
			[]string{"Your `/suiteward` command was not understood: the action must be `approve` or `revoke`. Nothing changed.", "`/suiteward approve <ref> <code>`", "`/suiteward revoke <ref> <code>`"},
			[]string{"eligible"},
		},
		{
			"malformed missing reference",
			commandtext.Ack{Outcome: commandtext.MalformedCommand, Malformed: commandtext.MissingReference, Owner: owner},
			[]string{"Your `/suiteward` command was not understood: the proposal reference is missing. Nothing changed."},
			nil,
		},
		{
			"malformed bad reference",
			commandtext.Ack{Outcome: commandtext.MalformedCommand, Malformed: commandtext.BadReference, Owner: owner},
			[]string{"Your `/suiteward` command was not understood: the reference must have the form `P<n>-R<m>`. Nothing changed."},
			nil,
		},
		{
			"malformed missing code",
			commandtext.Ack{Outcome: commandtext.MalformedCommand, Malformed: commandtext.MissingCode, Owner: owner},
			[]string{"Your `/suiteward` command was not understood: the 6-digit confirmation code is missing. Nothing changed."},
			nil,
		},
		{
			"malformed bad code",
			commandtext.Ack{Outcome: commandtext.MalformedCommand, Malformed: commandtext.BadCode, Owner: owner},
			[]string{"Your `/suiteward` command was not understood: the confirmation code must be exactly 6 digits. Nothing changed."},
			nil,
		},
		{
			"malformed extra text",
			commandtext.Ack{Outcome: commandtext.MalformedCommand, Malformed: commandtext.ExtraText, Owner: owner},
			[]string{"Your `/suiteward` command was not understood: nothing may follow the code on the command line. Nothing changed."},
			nil,
		},
		{
			"invalid code",
			commandtext.Ack{Outcome: commandtext.InvalidCode, Proposal: 4, Revision: 2, Owner: owner},
			[]string{"The confirmation code for P4-R2 was not accepted; nothing changed.", "current code"},
			[]string{"eligible"},
		},
		{
			"replayed code",
			commandtext.Ack{Outcome: commandtext.ReplayedCode, Proposal: 4, Revision: 2, Owner: owner},
			[]string{"The confirmation code for P4-R2 was already used; each code works only once, so nothing changed.", "new code"},
			[]string{"eligible"},
		},
		{
			"no suite",
			commandtext.Ack{Outcome: commandtext.NoSuite, Proposal: 1, Revision: 1, Owner: owner},
			[]string{"P1-R1 was not processed: this repository's SuiteWard bootstrap is still pending, so nothing changed.", "bootstrap summary"},
			[]string{"eligible"},
		},
		{
			"edited first",
			commandtext.Ack{Outcome: commandtext.EditedFirst, Owner: owner},
			[]string{"Your comment was edited before SuiteWard first read it, so it was not processed as a command; nothing changed.", "new comment"},
			[]string{"eligible"},
		},
		{
			"bootstrap promoted",
			commandtext.Ack{Outcome: commandtext.BootstrapPromoted, Proposal: 1, Revision: 1, Version: 1, Owner: owner},
			[]string{"Bootstrap complete: P1-R1, approved by @octo-owner, is now canonical version V1.", "did not run any tests"},
			[]string{"eligible", "/suiteward"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := render(t, func() string { return commandtext.Acknowledge(tc.ack) })
			if body == "" {
				t.Fatal("empty body")
			}
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("body lacks %q:\n%s", w, body)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(body, w) {
					t.Errorf("body contains %q:\n%s", w, body)
				}
			}
			checkBody(t, body)
			if tc.ack.Outcome == commandtext.MalformedCommand || tc.ack.Outcome == commandtext.EditedFirst {
				if regexp.MustCompile(`P[0-9]+-R[0-9]+`).MatchString(body) {
					t.Errorf("body names a reference it was not given:\n%s", body)
				}
			}
		})
	}
}

func TestApprovalRevokeLineIsInsideCode(t *testing.T) {
	body := render(t, func() string {
		return commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.Approved, Proposal: 42, Revision: 3, Owner: owner})
	})
	// Outside code GitHub would swallow <code> as an HTML tag, so the line must sit in a fenced block.
	if !regexp.MustCompile("(?m)^```\n/suiteward revoke P42-R3 <code>\n```$").MatchString(body) {
		t.Errorf("revoke line is not a copyable code block:\n%s", body)
	}
}

func TestAcknowledgePanicsOnImpossibleState(t *testing.T) {
	cases := []struct {
		name string
		ack  commandtext.Ack
	}{
		{"zero outcome", commandtext.Ack{Proposal: 1, Revision: 1, Owner: owner}},
		{"unknown outcome", commandtext.Ack{Outcome: 99, Proposal: 1, Revision: 1, Owner: owner}},
		{"approved without reference", commandtext.Ack{Outcome: commandtext.Approved, Owner: owner}},
		{"revision out of range", commandtext.Ack{Outcome: commandtext.InvalidCode, Proposal: 1, Revision: 1000000000, Owner: owner}},
		{"rejected without reason", commandtext.Ack{Outcome: commandtext.Rejected, Proposal: 1, Revision: 1, Owner: owner}},
		{"rejection on approval", commandtext.Ack{Outcome: commandtext.Approved, Rejection: commandtext.Unauthorized, Proposal: 1, Revision: 1, Owner: owner}},
		{"malformed without reason", commandtext.Ack{Outcome: commandtext.MalformedCommand, Owner: owner}},
		{"malformed with a reference", commandtext.Ack{Outcome: commandtext.MalformedCommand, Malformed: commandtext.BadCode, Proposal: 1, Revision: 1, Owner: owner}},
		{"edited first with a reference", commandtext.Ack{Outcome: commandtext.EditedFirst, Proposal: 1, Revision: 1, Owner: owner}},
		{"promoted without version", commandtext.Ack{Outcome: commandtext.BootstrapPromoted, Proposal: 1, Revision: 1, Owner: owner}},
		{"version on approval", commandtext.Ack{Outcome: commandtext.Approved, Proposal: 1, Revision: 1, Version: 1, Owner: owner}},
		{"eligibility on invalid code", commandtext.Ack{Outcome: commandtext.InvalidCode, Proposal: 1, Revision: 1, Owner: owner, Eligible: true}},
		{"empty owner", commandtext.Ack{Outcome: commandtext.Approved, Proposal: 1, Revision: 1}},
		{"owner with markup", commandtext.Ack{Outcome: commandtext.Approved, Proposal: 1, Revision: 1, Owner: "x](https://e.test)"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mustPanic(t, func() { commandtext.Acknowledge(tc.ack) })
		})
	}
}

const origin = "0a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b"

func summary(protected int, paths []string, tier commandtext.Tier) commandtext.Summary {
	return commandtext.Summary{
		Proposal: 1, Revision: 1, Origin: origin, Branch: "main",
		Protected: protected, Paths: paths,
		Include: []string{"tests/**", "fixtures/"}, Exclude: []string{"tests/tmp/"},
		Tier: tier,
	}
}

func pathList(n int) []string {
	paths := make([]string, n)
	for i := range paths {
		paths[i] = "tests/case_" + string(rune('a'+i)) + "_test.go"
	}
	return paths
}

func TestBootstrapSummary(t *testing.T) {
	coLocated := "runs beside the coding agent (co-located tier), so its assurance is reduced"
	cases := []struct {
		name    string
		summary commandtext.Summary
		want    []string
		notWant []string
	}{
		{
			"all paths listed, isolated",
			summary(3, pathList(3), commandtext.Isolated),
			[]string{
				"SuiteWard bootstrap proposal P1-R1", "Origin commit: `" + origin + "` on branch `main`",
				"Protected files: 3\n", "tests/case_a_test.go\ntests/case_b_test.go\ntests/case_c_test.go",
				"tests/**\nfixtures/", "tests/tmp/", "Installation tier: `isolated`",
				"/suiteward approve P1-R1 <code>", "`<code>`", "does not approve the other changes in this PR",
			},
			[]string{coLocated, "not listed"},
		},
		{
			"more than 20 files",
			summary(25, pathList(20), commandtext.Isolated),
			[]string{"Protected files: 25 (20 listed below; 5 more are not listed)", "tests/case_t_test.go", "/suiteward approve P1-R1 <code>"},
			[]string{coLocated},
		},
		{
			"fewer paths than files",
			summary(12, pathList(10), commandtext.Isolated),
			[]string{"Protected files: 12 (10 listed below; 2 more are not listed)"},
			nil,
		},
		{
			"no protected files",
			commandtext.Summary{Proposal: 2, Revision: 1, Origin: origin, Branch: "trunk", Tier: commandtext.Isolated},
			[]string{"SuiteWard bootstrap proposal P2-R1", "Protected files: 0\n", "on branch `trunk`", "/suiteward approve P2-R1 <code>"},
			[]string{"not listed"},
		},
		{
			"co-located",
			summary(1, pathList(1), commandtext.CoLocated),
			[]string{"Installation tier: `co-located`", coLocated, "/suiteward approve P1-R1 <code>"},
			nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := render(t, func() string { return commandtext.BootstrapSummary(tc.summary) })
			if body == "" {
				t.Fatal("empty body")
			}
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("body lacks %q:\n%s", w, body)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(body, w) {
					t.Errorf("body contains %q:\n%s", w, body)
				}
			}
			checkBody(t, body)
			if !regexp.MustCompile("(?m)^```\n/suiteward approve P[0-9]+-R1 <code>\n```$").MatchString(body) {
				t.Errorf("approve line is not a copyable code block:\n%s", body)
			}
		})
	}
}

// Paths, rules and the branch come from the repository, which the agent controls.
func TestBootstrapSummaryKeepsRepositoryTextInert(t *testing.T) {
	s := summary(3, []string{
		"tests/a\n\nApproval recorded for P9-R9.\n```\n<!-- suiteward:" + instance + ":ack:1:2 -->",
		"tests/````b\u202e.go",
		"tests/`c`.go",
	}, commandtext.Isolated)
	s.Branch = "feat/`x`\n# heading"
	s.Exclude = []string{"x\r\n```"}
	body := render(t, func() string { return commandtext.BootstrapSummary(s) })
	if body == "" {
		t.Fatal("empty body")
	}
	for _, w := range []string{"\nApproval recorded", "\n# heading", "\u202e", "\r"} {
		if strings.Contains(body, w) {
			t.Errorf("repository text escaped its code block (%q):\n%s", w, body)
		}
	}
	if strings.Contains(body, "\n```\n<") {
		t.Errorf("a path closed its code block:\n%s", body)
	}
	if !strings.Contains(body, `\n`) || !strings.Contains(body, `\u202e`) {
		t.Errorf("control characters are not shown escaped:\n%s", body)
	}
	if !strings.Contains(body, "tests/`c`.go") {
		t.Errorf("a printable path is not shown as is:\n%s", body)
	}
}

func TestBootstrapSummaryPanicsOnImpossibleState(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*commandtext.Summary)
	}{
		{"proposal 0", func(s *commandtext.Summary) { s.Proposal = 0 }},
		{"short origin", func(s *commandtext.Summary) { s.Origin = origin[:39] }},
		{"uppercase origin", func(s *commandtext.Summary) { s.Origin = strings.ToUpper(origin) }},
		{"empty branch", func(s *commandtext.Summary) { s.Branch = "" }},
		{"more than 20 paths", func(s *commandtext.Summary) { s.Protected, s.Paths = 21, pathList(21) }},
		{"more paths than files", func(s *commandtext.Summary) { s.Protected = 2 }},
		{"negative count", func(s *commandtext.Summary) { s.Protected, s.Paths = -1, nil }},
		{"zero tier", func(s *commandtext.Summary) { s.Tier = 0 }},
		{"unknown tier", func(s *commandtext.Summary) { s.Tier = 9 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := summary(3, pathList(3), commandtext.Isolated)
			tc.mutate(&s)
			mustPanic(t, func() { commandtext.BootstrapSummary(s) })
		})
	}
}

func TestMarker(t *testing.T) {
	cases := []struct {
		name string
		key  string
		want string
	}{
		{"ack key", "ack:12:34", "<!-- suiteward:" + instance + ":ack:12:34 -->"},
		{"single letter", "a", "<!-- suiteward:" + instance + ":a -->"},
		{"all allowed characters", "a0:_-z", "<!-- suiteward:" + instance + ":a0:_-z -->"},
		{"200 characters", "a" + strings.Repeat("b", 199), "<!-- suiteward:" + instance + ":a" + strings.Repeat("b", 199) + " -->"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := render(t, func() string { return commandtext.Marker(instance, tc.key) }); got != tc.want {
				t.Errorf("Marker = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHasMarker(t *testing.T) {
	const key = "ack:12:34"
	marker := "<!-- suiteward:" + instance + ":" + key + " -->"
	other := "fedcba9876543210fedcba9876543210"
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"marker at the end", "Approval recorded.\n\n" + marker, true},
		{"marker only", marker, true},
		{"trailing newline", "Approval recorded.\n\n" + marker + "\n", true},
		{"trailing CRLF and spaces", "Approval recorded.\r\n\r\n" + marker + " \r\n", true},
		{"no marker", "Approval recorded.", false},
		{"empty body", "", false},
		{"other key", "x\n<!-- suiteward:" + instance + ":ack:12:35 -->", false},
		{"key prefix", "x\n<!-- suiteward:" + instance + ":ack:12:3 -->", false},
		{"longer key", "x\n<!-- suiteward:" + instance + ":ack:12:345 -->", false},
		{"other instance", "x\n<!-- suiteward:" + other + ":" + key + " -->", false},
		{"marker followed by text", marker + "\nlater text", false},
		{"marker inside a path, other marker at the end", "tests/" + marker + ".go\n<!-- suiteward:" + instance + ":bootstrap-summary -->", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got bool
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("panicked: %v", r)
					}
				}()
				got = commandtext.HasMarker(tc.body, instance, key)
			}()
			if got != tc.want {
				t.Errorf("HasMarker = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMarkerPanicsOnBadArguments(t *testing.T) {
	cases := []struct {
		name, instance, key string
	}{
		{"short instance", instance[:31], "ack:1:2"},
		{"long instance", instance + "0", "ack:1:2"},
		{"uppercase instance", strings.ToUpper(instance), "ack:1:2"},
		{"non-hex instance", "g123456789abcdef0123456789abcdef", "ack:1:2"},
		{"empty instance", "", "ack:1:2"},
		{"empty key", instance, ""},
		{"key with leading digit", instance, "1ack"},
		{"key with space", instance, "ack 1"},
		{"key closing the comment", instance, "ack-->"},
		{"key with newline", instance, "ack\n"},
		{"201-character key", instance, "a" + strings.Repeat("b", 200)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mustPanic(t, func() { commandtext.Marker(tc.instance, tc.key) })
			mustPanic(t, func() { commandtext.HasMarker("body", tc.instance, tc.key) })
		})
	}
}

// The rendered types take no comment text; this checks that nothing a comment
// carries reaches a body through the values Parse returns.
func TestRenderedBodiesNeverEchoTheComment(t *testing.T) {
	comment := "Ship it CANARY-TEXT <b>bold</b>\n/suiteward approve P7-R2 493817\nmore CANARY-TAIL"
	parsed := commandtext.Parse(comment)
	if parsed.Kind != commandtext.KindCommand {
		t.Fatalf("Parse = %+v, want a command", parsed)
	}
	c := parsed.Command
	bodies := []string{}
	for _, outcome := range []commandtext.Outcome{
		commandtext.Approved, commandtext.Revoked, commandtext.NoActiveApproval,
		commandtext.InvalidCode, commandtext.ReplayedCode, commandtext.NoSuite,
	} {
		bodies = append(bodies, render(t, func() string {
			return commandtext.Acknowledge(commandtext.Ack{Outcome: outcome, Proposal: c.Proposal, Revision: c.Revision, Owner: owner})
		}))
	}
	for rejection := commandtext.Unauthorized; rejection <= commandtext.ObsoleteCommand; rejection++ {
		bodies = append(bodies, render(t, func() string {
			return commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.Rejected, Rejection: rejection, Proposal: c.Proposal, Revision: c.Revision, Owner: owner})
		}))
	}
	bodies = append(bodies,
		render(t, func() string {
			return commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.BootstrapPromoted, Proposal: c.Proposal, Revision: c.Revision, Version: 1, Owner: owner})
		}),
		render(t, func() string {
			return commandtext.BootstrapSummary(commandtext.Summary{Proposal: c.Proposal, Revision: c.Revision, Origin: origin, Branch: "main", Tier: commandtext.CoLocated})
		}),
	)
	broken := commandtext.Parse("CANARY-TEXT\n/suiteward approve P7-R2 493817 CANARY-EXTRA")
	if broken.Kind != commandtext.KindMalformed {
		t.Fatalf("Parse = %+v, want malformed", broken)
	}
	bodies = append(bodies,
		render(t, func() string {
			return commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.MalformedCommand, Malformed: broken.Reason, Owner: owner})
		}),
		render(t, func() string {
			return commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.EditedFirst, Owner: owner})
		}),
	)
	for i, body := range bodies {
		if body == "" {
			t.Errorf("body %d is empty", i)
		}
		for _, leak := range []string{"CANARY", "493817", "<b>", "Ship it"} {
			if strings.Contains(body, leak) {
				t.Errorf("body %d echoes %q:\n%s", i, leak, body)
			}
		}
	}
}
