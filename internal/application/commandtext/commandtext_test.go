package commandtext_test

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

const (
	instance = "0123456789abcdef0123456789abcdef"
	owner    = "octo-owner"
	source   = 2345678901
)

// mustPanic fails unless f panics with something other than a stub's message.
func mustPanic(t *testing.T, f func()) {
	t.Helper()
	defer func() {
		switch recover() {
		case nil:
			t.Error("did not panic")
		case "not implemented":
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

// withMode is p, a command, with the mode it names.
func withMode(p commandtext.Parsed, mode execution.Mode) commandtext.Parsed {
	p.Command.Mode = mode
	return p
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
		{"extra spaces and tabs", "  /suiteward \t approve  \tP7-R2\t\t 012345 \t", command(commandtext.Approve, 7, 2, "012345")},
		{"indented three columns", "   /suiteward approve P7-R2 012345", command(commandtext.Approve, 7, 2, "012345")},
		{"after a comment that closes on its own line", "<!-- note -->\n/suiteward approve P1-R2 121212", command(commandtext.Approve, 1, 2, "121212")},
		{"after a comment block", "<!--\nnote\n-->\n/suiteward approve P1-R2 121212", command(commandtext.Approve, 1, 2, "121212")},
		{"a fence inside an HTML comment opens nothing", "<!--\n```\n-->\n/suiteward approve P1-R2 121212", command(commandtext.Approve, 1, 2, "121212")},
		{"after an indented line", "    code\n/suiteward approve P1-R2 121212", command(commandtext.Approve, 1, 2, "121212")},
		{"a comment marker inside a fence is code", "```\n<!--\n```\n/suiteward approve P1-R2 121212", command(commandtext.Approve, 1, 2, "121212")},
		{"CRLF", "/suiteward revoke P5-R1 111111\r\nthanks\r\n", command(commandtext.Revoke, 5, 1, "111111")},
		{"CRLF on a later line", "Looks good.\r\n/suiteward approve P5-R1 111111\r\n", command(commandtext.Approve, 5, 1, "111111")},
		{"command on a later line", "Reviewed the inventory.\n\n/suiteward approve P2-R4 222222", command(commandtext.Approve, 2, 4, "222222")},
		{"text before and after", "LGTM\n/suiteward approve P3-R1 333333\nShip it.\n/other text", command(commandtext.Approve, 3, 1, "333333")},
		{"after a closed fence", "```\n/suiteward approve P9-R9 999999\n```\n/suiteward approve P1-R2 121212", command(commandtext.Approve, 1, 2, "121212")},
		{"after a closed tilde fence", "~~~~sh\n/suiteward revoke P9-R9 999999\n~~~~\n/suiteward revoke P1-R2 121212", command(commandtext.Revoke, 1, 2, "121212")},
		{"after a fence closed with three columns", "```\n/suiteward approve P9-R9 999999\n   ```\n/suiteward approve P1-R2 121212", command(commandtext.Approve, 1, 2, "121212")},
		{"a dash that is no list marker opens nothing", "---\n/suiteward approve P1-R2 121212", command(commandtext.Approve, 1, 2, "121212")},
		{"after a longer closing fence", "```\n/suiteward approve P9-R9 999999\n`````\n/suiteward approve P1-R2 121212", command(commandtext.Approve, 1, 2, "121212")},

		// The prefix and the verb are case-insensitive; the reference keeps its form.
		{"upper case prefix and verb", "/SUITEWARD APPROVE P1-R1 123456", command(commandtext.Approve, 1, 1, "123456")},
		{"mixed case approve", "/suiteward Approve P1-R1 123456", command(commandtext.Approve, 1, 1, "123456")},
		{"mixed case prefix approve", "/SuiteWard approve P1-R1 123456", command(commandtext.Approve, 1, 1, "123456")},
		{"mixed case revoke", "/suiteward REVOKE P2-R3 654321", command(commandtext.Revoke, 2, 3, "654321")},
		{"mixed case skip", "/SuiteWard skip 123456", command(commandtext.Skip, 0, 0, "123456")},
		{"mixed case suggest", "/SuiteWard suggest", command(commandtext.Suggest, 0, 0, "")},
		{"upper case suggest", "/SUITEWARD SUGGEST", command(commandtext.Suggest, 0, 0, "")},
		{"mixed case propose", "/Suiteward Propose", command(commandtext.Propose, 0, 0, "")},
		{"mixed case on a later line", "On my phone\n/SuiteWard suggest", command(commandtext.Suggest, 0, 0, "")},

		// The optional mode of approve (M2.1): the word between the reference and the code, stored as the mode it names.
		{"approve with fix", "/suiteward approve P1-R2 fix 123456", withMode(command(commandtext.Approve, 1, 2, "123456"), execution.ModeFix)},
		{"approve with feature", "/suiteward approve P1-R2 feature 123456", withMode(command(commandtext.Approve, 1, 2, "123456"), execution.ModeFeature)},
		{"approve with refactor", "/suiteward approve P1-R2 refactor 123456", withMode(command(commandtext.Approve, 1, 2, "123456"), execution.ModeRefactor)},
		{"approve with a mode in capitals", "/SUITEWARD APPROVE P1-R2 FIX 123456", withMode(command(commandtext.Approve, 1, 2, "123456"), execution.ModeFix)},
		{"approve with a mixed case mode", "/suiteward approve P1-R2 ReFactor 123456", withMode(command(commandtext.Approve, 1, 2, "123456"), execution.ModeRefactor)},
		{"approve with a mode and extra spaces", "/suiteward  approve \tP1-R2  \tfeature\t 123456 ", withMode(command(commandtext.Approve, 1, 2, "123456"), execution.ModeFeature)},
		{"approve without a mode", "/suiteward approve P1-R2 123456", command(commandtext.Approve, 1, 2, "123456")},

		// The mode verb: the owner's preview mode, no code.
		{"mode fix", "/suiteward mode fix", withMode(command(commandtext.SetMode, 0, 0, ""), execution.ModeFix)},
		{"mode feature", "/suiteward mode feature", withMode(command(commandtext.SetMode, 0, 0, ""), execution.ModeFeature)},
		{"mode refactor", "/suiteward mode refactor", withMode(command(commandtext.SetMode, 0, 0, ""), execution.ModeRefactor)},
		{"mode in capitals", "/SUITEWARD MODE REFACTOR", withMode(command(commandtext.SetMode, 0, 0, ""), execution.ModeRefactor)},
		{"mode with a mixed case word", "/SuiteWard Mode Feature", withMode(command(commandtext.SetMode, 0, 0, ""), execution.ModeFeature)},
		{"mode on a later line", "Preview this one\n/suiteward mode fix", withMode(command(commandtext.SetMode, 0, 0, ""), execution.ModeFix)},

		// The unblock verb: the owner's escape, a code and nothing else, as skip.
		{"unblock", "/suiteward unblock 123456", command(commandtext.Unblock, 0, 0, "123456")},
		{"unblock in capitals", "/SUITEWARD UNBLOCK 000000", command(commandtext.Unblock, 0, 0, "000000")},
		{"unblock with a mixed case verb", "/SuiteWard Unblock 654321", command(commandtext.Unblock, 0, 0, "654321")},

		// Not a command.
		{"empty body", "", notCommand},
		{"no command line", "Looks good to me.\nThanks!", notCommand},
		{"only inside a fence", "```\n/suiteward approve P1-R1 123456\n```", notCommand},
		{"only inside an indented tilde fence", "  ~~~\n/suiteward approve P1-R1 123456\n  ~~~\n", notCommand},
		{"in the middle of a line", "please run /suiteward approve P1-R1 123456", notCommand},
		{"suiteward-x", "/suiteward-x approve P1-R1 123456", notCommand},
		{"suitewardx", "/suitewardx approve P1-R1 123456", notCommand},
		{"prefix before the line start", "x /suiteward suggest", notCommand},
		{"suitewardx with a verb that suggests", "/suitewardx suggest", notCommand},
		{"mixed case prefix with a suffix", "/SuiteWardx suggest", notCommand},
		{"fenced mixed case", "```\n/SuiteWard suggest\n```", notCommand},
		{"long s is not an s", "/ſuiteward suggest", notCommand},
		{"no-break space after the prefix", "/suiteward\u00a0approve P1-R1 123456", notCommand},
		{"quoted", "> /suiteward approve P1-R1 123456", notCommand},

		// GitHub shows no command in a hidden comment or in an indented code block.
		{"inside an HTML comment block", "<!--\n/suiteward approve P1-R1 123456\n-->", notCommand},
		{"on the line that opens an HTML comment", "<!-- /suiteward approve P1-R1 123456\nnote\n-->", notCommand},
		{"on the line that closes an HTML comment", "<!--\nnote\n--> /suiteward approve P1-R1 123456", notCommand},
		{"after an unclosed HTML comment", "<!--\n/suiteward approve P1-R1 123456", notCommand},
		{"in an HTML comment on one line", "<!-- /suiteward approve P1-R1 123456 -->", notCommand},
		{"after text and an unclosed comment opener on the same line", "note <!-- x\n/suiteward approve P1-R1 123456", notCommand},
		{"a comment that closes and reopens on one line", "<!--\nx --> y <!--\n/suiteward approve P1-R1 123456\n-->", notCommand},
		{"a comment that closes and reopens unclosed on its opening line", "<!-- a --> b <!-- c\n/suiteward approve P1-R1 123456\n-->", notCommand},
		{"indented four spaces", "    /suiteward approve P1-R1 123456", notCommand},
		{"indented a tab", "\t/suiteward approve P1-R1 123456", notCommand},
		{"indented two spaces and a tab", "  \t/suiteward approve P1-R1 123456", notCommand},
		{"indented a space and a tab", " \t /suiteward approve P7-R2 012345", notCommand},
		{"indented four spaces on a later line, CRLF", "text\r\n    /suiteward approve P1-R1 123456\r\n", notCommand},

		// Fences.
		{"unclosed fence hides the rest", "```\nsome code\n/suiteward approve P1-R1 123456\nmore", notCommand},
		{"unclosed tilde fence hides the rest", "~~~\n/suiteward revoke P1-R1 123456", notCommand},
		{"tilde does not close a backtick fence", "```\n~~~\n/suiteward approve P1-R1 123456\n```", notCommand},
		{"shorter fence does not close", "````\n```\n/suiteward approve P1-R1 123456\n````", notCommand},
		{"fence with info string does not close", "```\n```go\n/suiteward approve P1-R1 123456\n```", notCommand},
		{"a closing fence indented four columns does not close", "```\n    ```\n/suiteward approve P1-R1 123456", notCommand},
		{"a closing fence indented a tab does not close", "```\n\t```\n/suiteward approve P1-R1 123456", notCommand},
		{"a fence in a list item", "- ```\n  /suiteward approve P1-R1 123456\n  ```", notCommand},
		{"a fence in a numbered list item", "1. ```\n   /suiteward approve P1-R1 123456\n   ```", notCommand},
		{"a tilde fence in a starred list item", "* ~~~\n  /suiteward revoke P1-R1 123456", notCommand},
		{"a fence in a block quote skips the line after it", "> ```\n/suiteward approve P1-R1 123456", notCommand},
		{"a fence in a quoted list item", "> - ```\n>   /suiteward approve P1-R1 123456", notCommand},
		{"a fence in a list item that a bare fence ends", "- ```\n```\n/suiteward approve P1-R1 123456", notCommand},
		{"a fence in a quote that a bare fence ends", "> ```\n```\n/suiteward approve P1-R1 123456", notCommand},
		{"a fence in a numbered item that a less indented fence ends", "1. ```\n  ```\n/suiteward approve P1-R1 123456", notCommand},
		{"a fence in a list item runs to the end of the body", "- ```\n  code\n  ```\n/suiteward approve P1-R2 121212", notCommand},
		{"an opener indented inside an open list item", "- item\n  ```\n  code\n```\n/suiteward approve P1-R1 123456", notCommand},
		{"an opener indented inside an open numbered item", "1. item\n   ```\n   code\n```\n/suiteward approve P1-R1 123456", notCommand},
		{"an opener indented inside a list item after a blank line", "- a\n\n  ```\n  x\n```\n/suiteward approve P1-R1 123456", notCommand},
		{"a top-level fence indented one column is skipped to the end", " ```\n```\n/suiteward approve P1-R1 123456", notCommand},
		{"a backtick fence whose info string opens a comment", "``` <!--\n-->\n/suiteward approve P1-R1 123456", notCommand},
		{"a tilde fence whose info string opens a comment", "~~~ <!--\n-->\n/suiteward approve P1-R1 123456", notCommand},
		{"a fence whose info string holds a whole comment", "``` <!-- x -->\n/suiteward approve P1-R1 123456", notCommand},
		{"a backtick run whose info string holds a backtick is no fence", "```a```\nx\n```\n/suiteward approve P1-R1 123456", notCommand},
		{"a lone carriage return ends a line", "foo\r```\n/suiteward approve P1-R1 123456", notCommand},

		// Malformed.
		{"bare prefix", "/suiteward", malformed(commandtext.UnknownVerb)},
		{"bare prefix with trailing space", "/suiteward  \t", malformed(commandtext.UnknownVerb)},
		{"unknown verb", "/suiteward merge P1-R1 123456", malformed(commandtext.UnknownVerb)},
		{"extra text after a mixed case suggest", "/SuiteWard suggest foo", malformed(commandtext.ExtraText)},
		{"missing reference after a mixed case prefix", "/SuiteWard Approve", malformed(commandtext.MissingReference)},
		{"mixed case verb, short code", "/SuiteWard Approve P1-R1 12345", malformed(commandtext.BadCode)},
		{"mixed case verb, lowercase reference", "/SuiteWard Approve p1-r1 123456", malformed(commandtext.BadReference)},
		{"long s is not an s in a verb", "/suiteward ſkip 123456", malformed(commandtext.UnknownVerb)},
		{"kelvin sign is not a k in a verb", "/suiteward revoKe P1-R1 123456", malformed(commandtext.UnknownVerb)},
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

		// A third token that is not a mode word is the code, so a misspelled mode is a bad code. After a mode word the fourth token is the code.
		{"misspelled mode", "/suiteward approve P1-R2 fixx 123456", malformed(commandtext.BadCode)},
		{"a word that is not a mode", "/suiteward approve P1-R2 features 123456", malformed(commandtext.BadCode)},
		{"mode and no code", "/suiteward approve P1-R2 fix", malformed(commandtext.MissingCode)},
		{"mode and a short code", "/suiteward approve P1-R2 fix 12345", malformed(commandtext.BadCode)},
		{"mode and a word that is no code", "/suiteward approve P1-R2 fix please", malformed(commandtext.BadCode)},
		{"mode twice", "/suiteward approve P1-R2 fix fix 123456", malformed(commandtext.BadCode)},
		{"mode and a code and more text", "/suiteward approve P1-R2 fix 123456 please", malformed(commandtext.ExtraText)},
		{"mode and two codes", "/suiteward approve P1-R2 feature 123456 654321", malformed(commandtext.ExtraText)},
		{"the mode after the code", "/suiteward approve P1-R2 123456 fix", malformed(commandtext.ExtraText)},
		{"revoke with a mode", "/suiteward revoke P1-R2 fix 123456", malformed(commandtext.BadCode)},
		{"revoke with a mode and no code", "/suiteward revoke P1-R2 refactor", malformed(commandtext.BadCode)},
		{"mode without a word", "/suiteward mode", malformed(commandtext.MissingMode)},
		{"mode with a trailing tab", "/suiteward mode\t", malformed(commandtext.MissingMode)},
		{"mode that is no mode", "/suiteward mode x", malformed(commandtext.BadMode)},
		{"mode misspelled", "/suiteward mode fixx", malformed(commandtext.BadMode)},
		{"mode with a code", "/suiteward mode 123456", malformed(commandtext.BadMode)},
		{"mode with a bad word and more text", "/suiteward mode x extra", malformed(commandtext.BadMode)},
		{"mode with more text", "/suiteward mode fix extra", malformed(commandtext.ExtraText)},
		{"mode with a code after it", "/suiteward mode feature 123456", malformed(commandtext.ExtraText)},
		{"unblock without a code", "/suiteward unblock", malformed(commandtext.MissingCode)},
		{"unblock with a trailing space", "/suiteward unblock ", malformed(commandtext.MissingCode)},
		{"unblock with a reference", "/suiteward unblock P1-R1", malformed(commandtext.BadCode)},
		{"unblock with a short code", "/suiteward unblock 12345", malformed(commandtext.BadCode)},
		{"unblock with a mode", "/suiteward unblock fix", malformed(commandtext.BadCode)},
		{"unblock with more text", "/suiteward unblock 123456 x", malformed(commandtext.ExtraText)},
		{"unblock with a reference and a code", "/suiteward unblock P1-R1 123456", malformed(commandtext.BadCode)},
		{"the mode verb is not read through a no-break space", "/suiteward mode fix", malformed(commandtext.UnknownVerb)},

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
			"approved and requirement met",
			commandtext.Ack{Outcome: commandtext.Approved, Proposal: 42, Revision: 3, Source: source, Owner: owner, Eligible: true},
			[]string{"Approval recorded for P42-R3 by @octo-owner.", "The approval requirement for P42-R3 is met.", "To withdraw this approval, comment:", "/suiteward revoke P42-R3 <code>", "`<code>`"},
			[]string{"not met", "eligible", "separate comment", "confirms"},
		},
		{
			"approved and requirement not yet met",
			commandtext.Ack{Outcome: commandtext.Approved, Proposal: 42, Revision: 3, Source: source, Owner: owner},
			[]string{"Approval recorded for P42-R3 by @octo-owner.", "The approval requirement for P42-R3 is not met yet.", "/suiteward revoke P42-R3 <code>"},
			[]string{"is met", "eligible", "other requirements"},
		},
		{
			"revoked and still eligible",
			commandtext.Ack{Outcome: commandtext.Revoked, Proposal: 7, Revision: 1, Source: source, Owner: owner, Eligible: true},
			[]string{"Revocation recorded for P7-R1 by @octo-owner.", "Your approval no longer counts.", "The approval requirement for P7-R1 is still met through other valid approval.", "/suiteward approve P7-R1 <code>"},
			[]string{"is not met", "eligible", "/suiteward revoke"},
		},
		{
			"revoked and no longer eligible",
			commandtext.Ack{Outcome: commandtext.Revoked, Proposal: 7, Revision: 1, Source: source, Owner: owner},
			[]string{"Revocation recorded for P7-R1 by @octo-owner.", "Your approval no longer counts.", "The approval requirement for P7-R1 is not met."},
			[]string{"still met", "eligible", "waits", "/suiteward revoke"},
		},
		{
			"no active approval",
			commandtext.Ack{Outcome: commandtext.NoActiveApproval, Proposal: 7, Revision: 2, Source: source, Owner: owner},
			[]string{"No active approval by @octo-owner for P7-R2 to withdraw; nothing changed.", "The approval requirement for P7-R2 is not met."},
			[]string{"eligible", "/suiteward revoke"},
		},
		{
			"no active approval while eligible",
			commandtext.Ack{Outcome: commandtext.NoActiveApproval, Proposal: 7, Revision: 2, Source: source, Owner: owner, Eligible: true},
			[]string{"No active approval by @octo-owner for P7-R2 to withdraw; nothing changed.", "The approval requirement for P7-R2 is met."},
			[]string{"not met", "eligible"},
		},
		{
			"rejected unauthorized",
			commandtext.Ack{Outcome: commandtext.Rejected, Rejection: commandtext.Unauthorized, Proposal: 3, Revision: 1, Source: source, Owner: owner},
			[]string{"Command for P3-R1 rejected by policy: @octo-owner is not authorized for this action under the project's policy. Nothing changed.", "The approval requirement for P3-R1 is not met."},
			[]string{"eligible", "/suiteward revoke"},
		},
		{
			"rejected unknown revision",
			commandtext.Ack{Outcome: commandtext.Rejected, Rejection: commandtext.UnknownRevision, Proposal: 3, Revision: 9, Source: source, Owner: owner},
			[]string{"Command for P3-R9 rejected by policy: P3-R9 is not a revision of this project. Nothing changed.", "Check the reference"},
			nil,
		},
		{
			"rejected superseded revision",
			commandtext.Ack{Outcome: commandtext.Rejected, Rejection: commandtext.SupersededRevision, Proposal: 3, Revision: 1, Source: source, Owner: owner},
			[]string{"Command for P3-R1 rejected by policy: P3-R1 has been superseded by a newer revision. Nothing changed.", "latest revision"},
			nil,
		},
		{
			"rejected context mismatch",
			commandtext.Ack{Outcome: commandtext.Rejected, Rejection: commandtext.ContextMismatch, Proposal: 3, Revision: 1, Source: source, Owner: owner},
			[]string{"Command for P3-R1 rejected by policy: P3-R1 no longer matches its source or integration target. Nothing changed."},
			nil,
		},
		{
			"rejected policy mismatch",
			commandtext.Ack{Outcome: commandtext.Rejected, Rejection: commandtext.PolicyMismatch, Proposal: 3, Revision: 1, Source: source, Owner: owner},
			[]string{"Command for P3-R1 rejected by policy: the project's policy changed after P3-R1 was proposed. Nothing changed."},
			nil,
		},
		{
			"rejected obsolete command while eligible",
			commandtext.Ack{Outcome: commandtext.Rejected, Rejection: commandtext.ObsoleteCommand, Proposal: 3, Revision: 1, Source: source, Owner: owner, Eligible: true},
			[]string{"Command for P3-R1 rejected by policy: a later command for P3-R1 was already processed. Nothing changed.", "The approval requirement for P3-R1 is met."},
			nil,
		},
		{
			"malformed unknown verb",
			commandtext.Ack{Outcome: commandtext.MalformedCommand, Malformed: commandtext.UnknownVerb, Source: source, Owner: owner},
			[]string{"Your `/suiteward` command was not understood: the action must be `approve`, `revoke`, `propose`, `skip`, `suggest`, `mode` or `unblock`. Nothing changed.", "`/suiteward approve <ref> <code>`", "`/suiteward revoke <ref> <code>`", "`/suiteward propose`", "`/suiteward skip <code>`", "`/suiteward suggest`",
				"`/suiteward mode <fix|feature|refactor>`", "`/suiteward unblock <code>`"},
			[]string{"eligible"},
		},
		{
			"malformed missing mode",
			commandtext.Ack{Outcome: commandtext.MalformedCommand, Malformed: commandtext.MissingMode, Source: source, Owner: owner},
			[]string{"Your `/suiteward` command was not understood: the mode is missing, it must be `fix`, `feature` or `refactor`. Nothing changed.", "`/suiteward mode <fix|feature|refactor>`"},
			[]string{"eligible", "code is missing"},
		},
		{
			"malformed bad mode",
			commandtext.Ack{Outcome: commandtext.MalformedCommand, Malformed: commandtext.BadMode, Source: source, Owner: owner},
			[]string{"Your `/suiteward` command was not understood: the mode must be `fix`, `feature` or `refactor`. Nothing changed.", "`/suiteward mode <fix|feature|refactor>`"},
			[]string{"eligible", "6 digits"},
		},
		{
			"malformed missing reference",
			commandtext.Ack{Outcome: commandtext.MalformedCommand, Malformed: commandtext.MissingReference, Source: source, Owner: owner},
			[]string{"Your `/suiteward` command was not understood: the proposal reference is missing. Nothing changed."},
			nil,
		},
		{
			"malformed bad reference",
			commandtext.Ack{Outcome: commandtext.MalformedCommand, Malformed: commandtext.BadReference, Source: source, Owner: owner},
			[]string{"Your `/suiteward` command was not understood: the reference must have the form `P<n>-R<m>`. Nothing changed."},
			nil,
		},
		{
			"malformed missing code",
			commandtext.Ack{Outcome: commandtext.MalformedCommand, Malformed: commandtext.MissingCode, Source: source, Owner: owner},
			[]string{"Your `/suiteward` command was not understood: the 6-digit confirmation code is missing. Nothing changed."},
			nil,
		},
		{
			"malformed bad code",
			commandtext.Ack{Outcome: commandtext.MalformedCommand, Malformed: commandtext.BadCode, Source: source, Owner: owner},
			[]string{"Your `/suiteward` command was not understood: the confirmation code must be exactly 6 digits. Nothing changed."},
			nil,
		},
		{
			"malformed extra text",
			commandtext.Ack{Outcome: commandtext.MalformedCommand, Malformed: commandtext.ExtraText, Source: source, Owner: owner},
			[]string{"Your `/suiteward` command was not understood: nothing may follow the end of the command. Nothing changed."},
			nil,
		},
		{
			"invalid code",
			commandtext.Ack{Outcome: commandtext.InvalidCode, Proposal: 4, Revision: 2, Source: source, Owner: owner},
			[]string{"The confirmation code for P4-R2 was not accepted, so the command had no effect. The `SuiteWard / Contract` check may now show as failed until you comment a corrected command with a current code, or push."},
			[]string{"eligible"},
		},
		{
			"replayed code",
			commandtext.Ack{Outcome: commandtext.ReplayedCode, Proposal: 4, Revision: 2, Source: source, Owner: owner},
			[]string{"The confirmation code for P4-R2 was already used, so the command had no effect; each code works only once. The `SuiteWard / Contract` check may now show as failed until you comment a corrected command with a new code, or push.", "new code"},
			[]string{"eligible"},
		},
		{
			"no suite",
			commandtext.Ack{Outcome: commandtext.NoSuite, Proposal: 1, Revision: 1, Source: source, Owner: owner},
			[]string{"P1-R1 was not processed: this repository's SuiteWard bootstrap is still pending, so nothing changed.", "comment `/suiteward propose`"},
			[]string{"eligible"},
		},
		{
			"other pull request",
			commandtext.Ack{Outcome: commandtext.OtherPullRequest, Proposal: 4, Revision: 2, Source: source, Owner: owner},
			[]string{"P4-R2 belongs to another pull request; comment the command on that pull request. Nothing changed."},
			[]string{"eligible", "check"},
		},
		{
			"edited first",
			commandtext.Ack{Outcome: commandtext.EditedFirst, Source: source, Owner: owner},
			[]string{"Your comment was edited before SuiteWard first read it, so it was not processed as a command; nothing changed.", "new comment"},
			[]string{"eligible"},
		},
		{
			// True whether or not an approval was active, so it never says one was withdrawn.
			"revoked after promotion",
			commandtext.Ack{Outcome: commandtext.RevokedAfterPromotion, Proposal: 7, Revision: 1, Source: source, Owner: owner},
			[]string{"Revoke command for P7-R1 by @octo-owner was recorded.", "P7-R1 is already promoted", "canonical version remains unchanged", "does not undo the promotion", "separately approved rollback"},
			[]string{"awaiting valid approval", "eligible", "no longer counts", "withdrawn", "Revocation recorded", "/suiteward"},
		},
		{
			"approved after promotion",
			commandtext.Ack{Outcome: commandtext.ApprovedAfterPromotion, Proposal: 7, Revision: 1, Source: source, Owner: owner},
			[]string{"Approval recorded for P7-R1 by @octo-owner.", "P7-R1 is already promoted", "canonical version remains unchanged", "No further action is needed", "separately approved rollback"},
			[]string{"awaiting", "eligible", "withdraw", "/suiteward", "<code>"},
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
			if err := commandtext.ValidateAck(tc.ack); err != nil {
				t.Errorf("ValidateAck refuses it: %v", err)
			}
			body := render(t, func() string { return commandtext.Acknowledge(tc.ack) })
			if body == "" {
				t.Fatal("empty body")
			}
			checkWantedText(t, body, tc.want, tc.notWant)
			checkReplyLink(t, tc.ack.Outcome, body)
			if strings.Contains(body, "http") {
				t.Errorf("body holds a host name or URL:\n%s", body)
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

// checkWantedText holds when body contains each of want and none of notWant.
func checkWantedText(t *testing.T, body string, want, notWant []string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	for _, w := range notWant {
		if strings.Contains(body, w) {
			t.Errorf("body contains %q:\n%s", w, body)
		}
	}
}

// checkReplyLink holds when body links the command comment, except for the acknowledgment of a bootstrap promotion, which no command triggered.
func checkReplyLink(t *testing.T, outcome commandtext.Outcome, body string) {
	t.Helper()
	link := "[your command](#issuecomment-2345678901)"
	if outcome == commandtext.BootstrapPromoted {
		if strings.Contains(body, "#issuecomment") {
			t.Errorf("body links a command that did not trigger it:\n%s", body)
		}
	} else if !strings.Contains(body, link) {
		t.Errorf("body lacks %q:\n%s", link, body)
	}
}

func TestApprovalRevokeLineIsInsideCode(t *testing.T) {
	body := render(t, func() string {
		return commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.Approved, Proposal: 42, Revision: 3, Source: source, Owner: owner})
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
		{"zero outcome", commandtext.Ack{Proposal: 1, Revision: 1, Source: source, Owner: owner}},
		{"unknown outcome", commandtext.Ack{Outcome: 99, Proposal: 1, Revision: 1, Source: source, Owner: owner}},
		{"approved without reference", commandtext.Ack{Outcome: commandtext.Approved, Source: source, Owner: owner}},
		{"revision out of range", commandtext.Ack{Outcome: commandtext.InvalidCode, Proposal: 1, Revision: 1000000000, Source: source, Owner: owner}},
		{"rejected without reason", commandtext.Ack{Outcome: commandtext.Rejected, Proposal: 1, Revision: 1, Source: source, Owner: owner}},
		{"rejection on approval", commandtext.Ack{Outcome: commandtext.Approved, Rejection: commandtext.Unauthorized, Proposal: 1, Revision: 1, Source: source, Owner: owner}},
		{"malformed without reason", commandtext.Ack{Outcome: commandtext.MalformedCommand, Source: source, Owner: owner}},
		{"malformed with a reference", commandtext.Ack{Outcome: commandtext.MalformedCommand, Malformed: commandtext.BadCode, Proposal: 1, Revision: 1, Source: source, Owner: owner}},
		{"edited first with a reference", commandtext.Ack{Outcome: commandtext.EditedFirst, Proposal: 1, Revision: 1, Source: source, Owner: owner}},
		{"promoted without version", commandtext.Ack{Outcome: commandtext.BootstrapPromoted, Proposal: 1, Revision: 1, Owner: owner}},
		{"version on approval", commandtext.Ack{Outcome: commandtext.Approved, Proposal: 1, Revision: 1, Version: 1, Source: source, Owner: owner}},
		{"eligibility on invalid code", commandtext.Ack{Outcome: commandtext.InvalidCode, Proposal: 1, Revision: 1, Source: source, Owner: owner, Eligible: true}},
		{"empty owner", commandtext.Ack{Outcome: commandtext.Approved, Proposal: 1, Revision: 1, Source: source}},
		{"owner with markup", commandtext.Ack{Outcome: commandtext.Approved, Proposal: 1, Revision: 1, Source: source, Owner: "x](https://e.test)"}},
		{"owner starting with underscore", commandtext.Ack{Outcome: commandtext.Approved, Proposal: 1, Revision: 1, Source: source, Owner: "_octo"}},
		{"owner starting with hyphen", commandtext.Ack{Outcome: commandtext.Approved, Proposal: 1, Revision: 1, Source: source, Owner: "-octo"}},
		{"owner of 40 characters", commandtext.Ack{Outcome: commandtext.Approved, Proposal: 1, Revision: 1, Source: source, Owner: "a" + strings.Repeat("b", 39)}},
		{"owner with a space", commandtext.Ack{Outcome: commandtext.Approved, Proposal: 1, Revision: 1, Source: source, Owner: "octo owner"}},
		{"no source command", commandtext.Ack{Outcome: commandtext.Approved, Proposal: 1, Revision: 1, Owner: owner}},
		{"negative source command", commandtext.Ack{Outcome: commandtext.Approved, Proposal: 1, Revision: 1, Source: -5, Owner: owner}},
		{"no source on malformed", commandtext.Ack{Outcome: commandtext.MalformedCommand, Malformed: commandtext.BadCode, Owner: owner}},
		{"no source on edited first", commandtext.Ack{Outcome: commandtext.EditedFirst, Owner: owner}},
		{"source on bootstrap promoted", commandtext.Ack{Outcome: commandtext.BootstrapPromoted, Proposal: 1, Revision: 1, Version: 1, Source: source, Owner: owner}},
		{"revoked after promotion without reference", commandtext.Ack{Outcome: commandtext.RevokedAfterPromotion, Source: source, Owner: owner}},
		{"eligibility after promotion", commandtext.Ack{Outcome: commandtext.RevokedAfterPromotion, Proposal: 1, Revision: 1, Source: source, Owner: owner, Eligible: true}},
		{"rejection after promotion", commandtext.Ack{Outcome: commandtext.RevokedAfterPromotion, Rejection: commandtext.Unauthorized, Proposal: 1, Revision: 1, Source: source, Owner: owner}},
		{"approved after promotion without reference", commandtext.Ack{Outcome: commandtext.ApprovedAfterPromotion, Source: source, Owner: owner}},
		{"eligibility on approved after promotion", commandtext.Ack{Outcome: commandtext.ApprovedAfterPromotion, Proposal: 1, Revision: 1, Source: source, Owner: owner, Eligible: true}},
		{"version on approved after promotion", commandtext.Ack{Outcome: commandtext.ApprovedAfterPromotion, Proposal: 1, Revision: 1, Version: 1, Source: source, Owner: owner}},
		{"no source on approved after promotion", commandtext.Ack{Outcome: commandtext.ApprovedAfterPromotion, Proposal: 1, Revision: 1, Owner: owner}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mustPanic(t, func() { commandtext.Acknowledge(tc.ack) })
			if commandtext.ValidateAck(tc.ack) == nil {
				t.Error("ValidateAck accepts it")
			}
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
		{"bootstrap summary key", "bootstrap-summary:7:P4-R1", "<!-- suiteward:" + instance + ":bootstrap-summary:7:P4-R1 -->"},
		{"bootstrap promoted key", "bootstrap-promoted:7:P4-R1", "<!-- suiteward:" + instance + ":bootstrap-promoted:7:P4-R1 -->"},
		{"uppercase first letter", "Ack:1", "<!-- suiteward:" + instance + ":Ack:1 -->"},
		{"all allowed characters", "aZ0:_-z", "<!-- suiteward:" + instance + ":aZ0:_-z -->"},
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
		{"key starting with underscore", instance, "_ack"},
		{"key starting with colon", instance, ":ack"},
		{"key with closing angle bracket", instance, "ack>"},
		{"key with bang", instance, "ack--!>"},
		{"key with dot", instance, "ack.1"},
		{"key with non-ASCII letter", instance, "acké"},
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
		commandtext.InvalidCode, commandtext.ReplayedCode, commandtext.NoSuite, commandtext.RevokedAfterPromotion, commandtext.ApprovedAfterPromotion, commandtext.OtherPullRequest,
	} {
		bodies = append(bodies, render(t, func() string {
			return commandtext.Acknowledge(commandtext.Ack{Outcome: outcome, Proposal: c.Proposal, Revision: c.Revision, Source: source, Owner: owner})
		}))
	}
	for rejection := commandtext.Unauthorized; rejection <= commandtext.ObsoleteCommand; rejection++ {
		bodies = append(bodies, render(t, func() string {
			return commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.Rejected, Rejection: rejection, Proposal: c.Proposal, Revision: c.Revision, Source: source, Owner: owner})
		}))
	}
	for _, ack := range []commandtext.Ack{
		{Outcome: commandtext.ReceiptRefused, Proposal: c.Proposal, Revision: c.Revision, Gate: execution.ReasonReceiptPending},
		{Outcome: commandtext.ReceiptRefused, Proposal: c.Proposal, Revision: c.Revision, Gate: execution.ReasonReceiptUnavailable},
		{Outcome: commandtext.ReceiptRefused, Proposal: c.Proposal, Revision: c.Revision, Gate: execution.ReasonModeRequired},
		{Outcome: commandtext.ReceiptRefused, Proposal: c.Proposal, Revision: c.Revision, Gate: execution.ReasonModeNotMet, Mode: execution.ModeFix, Counts: execution.ReceiptCounts{Inert: 1}},
		{Outcome: commandtext.ModeSet, Mode: execution.ModeRefactor},
		{Outcome: commandtext.ModeRefused, Refusal: commandtext.NotOpen},
		{Outcome: commandtext.Unblocked, Plans: commandtext.Plans{Canonical: true, Receipt: true}},
		{Outcome: commandtext.UnblockRefused, Refusal: commandtext.NotUnavailable},
	} {
		ack.Source, ack.Owner = source, owner
		bodies = append(bodies, render(t, func() string { return commandtext.Acknowledge(ack) }))
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
			return commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.MalformedCommand, Malformed: broken.Reason, Source: source, Owner: owner})
		}),
		render(t, func() string {
			return commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.EditedFirst, Source: source, Owner: owner})
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

// The comment ends at the first "-->" (or "--!>"). The key is the only
// caller-supplied text inside it, so no byte that is not a letter, digit,
// colon, underscore or hyphen may get through.
func TestMarkerKeyCannotCloseTheCommentEarly(t *testing.T) {
	for c := range 256 {
		key := "ab--" + string([]byte{byte(c)}) + "cd"
		func() {
			defer func() { _ = recover() }()
			marker := commandtext.Marker(instance, key)
			if !strings.HasSuffix(marker, " -->") || strings.Contains(strings.TrimSuffix(marker, " -->"), ">") {
				t.Errorf("key %q gives a marker that can close early: %q", key, marker)
			}
		}()
	}
	for _, key := range []string{"a-->", "a--!>", "ab -->", "ab--\n>"} {
		mustPanic(t, func() { commandtext.Marker(instance, key) })
	}
	// Hyphens alone are fine: the marker still has exactly one terminator.
	if got := render(t, func() string { return commandtext.Marker(instance, "a--b---") }); strings.Count(got, "-->") != 1 {
		t.Errorf("Marker = %q, want exactly one -->", got)
	}
}

func TestHasMarkerWithContractKeys(t *testing.T) {
	for _, key := range []string{"bootstrap-summary:7:P4-R1", "bootstrap-promoted:7:P4-R1", "ack:7:2345678901", "bootstrap-notice:7:12", "bootstrap-cancelled:7:P4-R1"} {
		body := "text\n\n" + "<!-- suiteward:" + instance + ":" + key + " -->\n"
		var got bool
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("key %q panicked: %v", key, r)
				}
			}()
			got = commandtext.HasMarker(body, instance, key)
		}()
		if !got {
			t.Errorf("HasMarker does not find the marker for %q", key)
		}
	}
}

func TestAcknowledgeLinksTheSourceCommand(t *testing.T) {
	for _, id := range []int64{1, 987654321012, 9223372036854775807} {
		body := render(t, func() string {
			return commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.Approved, Proposal: 1, Revision: 1, Source: id, Owner: owner})
		})
		want := "[your command](#issuecomment-" + strconv.FormatInt(id, 10) + ")"
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
}

func TestAcknowledgeAcceptsGitHubLogins(t *testing.T) {
	for _, login := range []string{"octocat", "a", "0", "octo-owner", "octocat_acme", "o_c-t_o", "a" + strings.Repeat("b", 38)} {
		body := render(t, func() string {
			return commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.Approved, Proposal: 1, Revision: 1, Source: source, Owner: login})
		})
		if !strings.Contains(body, "by @"+login+".") {
			t.Errorf("body lacks the login %q:\n%s", login, body)
		}
	}
}

const maxComment = 65536

// A GitHub comment holds at most 65536 characters, so the largest summary the
// types allow must fit whatever the repository's text is made of.
func TestBootstrapSummaryFitsInAComment(t *testing.T) {
	texts := map[string]string{
		"plain":            "a",
		"invalid UTF-8":    "\xff",
		"backticks":        "`",
		"bidi control":     "\u202e",
		"newline":          "\n",
		"3-byte printable": "\u20ac",
	}
	for name, unit := range texts {
		t.Run(name, func(t *testing.T) {
			long := func(n int) []string {
				items := make([]string, n)
				for i := range items {
					items[i] = strings.Repeat(unit, 4096/len(unit))
				}
				return items
			}
			s := summary(20, long(20), commandtext.CoLocated)
			s.Include, s.Exclude = long(20), long(20)
			s.Branch = strings.Repeat(unit, 4096/len(unit))
			body := render(t, func() string { return commandtext.BootstrapSummary(s) })
			if body == "" {
				t.Fatal("empty body")
			}
			if len(body) >= maxComment {
				t.Errorf("body has %d bytes, want fewer than %d", len(body), maxComment)
			}
			if !utf8.ValidString(body) {
				t.Error("body is not valid UTF-8")
			}
			if !strings.Contains(body, "[shortened]") {
				t.Errorf("shortened entries are not marked:\n%.600s", body)
			}
		})
	}
}

func TestBootstrapSummaryBoundsTheScopeRules(t *testing.T) {
	rules := func(prefix string, n int) []string {
		items := make([]string, n)
		for i := range items {
			items[i] = prefix + strconv.Itoa(i) + "/**"
		}
		return items
	}
	s := summary(3, pathList(3), commandtext.Isolated)
	s.Include, s.Exclude = rules("inc", 25), rules("exc", 21)
	body := render(t, func() string { return commandtext.BootstrapSummary(s) })
	for _, w := range []string{"inc19/**", "exc19/**", "5 more are not listed", "1 more are not listed"} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	for _, w := range []string{"inc20/**", "inc24/**", "exc20/**"} {
		if strings.Contains(body, w) {
			t.Errorf("body lists %q beyond the bound:\n%s", w, body)
		}
	}
	// Exactly 20 is not truncated.
	s.Include, s.Exclude = rules("inc", 20), rules("exc", 20)
	body = render(t, func() string { return commandtext.BootstrapSummary(s) })
	if strings.Contains(body, "not listed") {
		t.Errorf("20 rules are all listed, yet the body says some are not:\n%s", body)
	}
}

func TestBootstrapSummaryShortensLongEntries(t *testing.T) {
	s := summary(1, []string{strings.Repeat("p", 300), strings.Repeat("\u20ac", 150) + "tail"}, commandtext.Isolated)
	s.Protected = 2
	s.Include = []string{strings.Repeat("i", 301)}
	body := render(t, func() string { return commandtext.BootstrapSummary(s) })
	if !strings.Contains(body, strings.Repeat("p", 300)+"\n") {
		t.Errorf("a 300-byte path was shortened:\n%s", body)
	}
	if strings.Contains(body, "tail") {
		t.Errorf("a long path was not shortened:\n%s", body)
	}
	if strings.Contains(body, strings.Repeat("i", 301)) || !strings.Contains(body, strings.Repeat("i", 300)+"\u2026 [shortened]") {
		t.Errorf("a 301-byte rule was not shortened to 300 bytes and marked:\n%s", body)
	}
	if !utf8.ValidString(body) {
		t.Error("shortening split a UTF-8 sequence")
	}
}

func TestBootstrapSummaryCountsTheRulesTheCallerDidNotCarry(t *testing.T) {
	rules := func(prefix string, n int) []string {
		items := make([]string, n)
		for i := range items {
			items[i] = prefix + strconv.Itoa(i) + "/**"
		}
		return items
	}
	s := summary(3, pathList(3), commandtext.Isolated)
	s.Include, s.IncludeTotal = rules("inc", 20), 25
	s.Exclude, s.ExcludeTotal = rules("exc", 20), 21
	body := render(t, func() string { return commandtext.BootstrapSummary(s) })
	for _, w := range []string{"Scope include rules (20 listed; 5 more are not listed)", "Scope exclude rules (20 listed; 1 more are not listed)"} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	// A total equal to what is carried, or none, adds nothing.
	s.IncludeTotal, s.ExcludeTotal = 20, 0
	body = render(t, func() string { return commandtext.BootstrapSummary(s) })
	if strings.Contains(body, "not listed") {
		t.Errorf("every rule is listed, yet the body says some are not:\n%s", body)
	}
}

// The default-branch text is pinned: M1.7b adds a source, and the branch text stays as it was. A summary without runner rules states none.
func TestBootstrapSummaryOfTheBranchKeepsItsExactText(t *testing.T) {
	want := "**SuiteWard bootstrap proposal P1-R1**\n\n" +
		"SuiteWard proposes this repository's protected baseline as P1-R1. Nothing is canonical yet.\n\n" +
		"- Origin commit: `" + origin + "` on branch `main` (pinned: it does not move if the branch advances)\n" +
		"- Protected files: 1\n" +
		"- Installation tier: `isolated`\n\n" +
		"Inventory:\n\n```\ntests/case_a_test.go\n```\n\n" +
		"Scope include rules:\n\n```\ntests/**\nfixtures/\n```\n\n" +
		"Scope runner rules: none.\n\n" +
		"Scope exclude rules:\n\n```\ntests/tmp/\n```\n\n" +
		"Review the inventory and the scope. To approve this exact baseline, comment:\n\n" +
		"```\n/suiteward approve P1-R1 <code>\n```\n\nReplace `<code>` with a current code from your authenticator app.\n\n" +
		"Approving P1-R1 does not approve the other changes in this PR.\n"
	for name, source := range map[string]commandtext.Source{"zero": 0, "branch": commandtext.SourceBranch} {
		s := summary(1, pathList(1), commandtext.Isolated)
		s.Source = source
		if got := render(t, func() string { return commandtext.BootstrapSummary(s) }); got != want {
			t.Errorf("%s: the branch text changed:\n%s", name, got)
		}
	}
}

func pullSummary(tier commandtext.Tier) commandtext.Summary {
	s := summary(3, pathList(3), tier)
	s.Source, s.Branch = commandtext.SourcePull, ""
	return s
}

func TestBootstrapSummaryOfAPullRequestStatesWhatApprovingDoes(t *testing.T) {
	body := render(t, func() string { return commandtext.BootstrapSummary(pullSummary(commandtext.Isolated)) })
	for _, w := range []string{
		"SuiteWard bootstrap proposal P1-R1", "`" + origin + "`", "exact head of this pull request", "Nothing is canonical yet",
		"Protected files: 3\n", "tests/case_a_test.go", "tests/**", "Installation tier: `isolated`",
		"Approving P1-R1 lets this pull request merge", "only from the exact merge commit after the merge",
		"A later push that changes the protected files needs a new approval",
		"```\n/suiteward approve P1-R1 <code>\n```",
	} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	for _, w := range []string{"on branch", "default branch", "does not move", "assurance is reduced", "other changes in this PR"} {
		if strings.Contains(body, w) {
			t.Errorf("body contains %q:\n%s", w, body)
		}
	}
	checkBody(t, body)
	co := render(t, func() string { return commandtext.BootstrapSummary(pullSummary(commandtext.CoLocated)) })
	if !strings.Contains(co, "assurance is reduced") || !strings.Contains(co, "`co-located`") {
		t.Errorf("the co-located summary lacks the reduced assurance:\n%s", co)
	}
}

func TestBootstrapSummaryOfAPullRequestFitsInAComment(t *testing.T) {
	for name, unit := range map[string]string{"plain": "a", "invalid UTF-8": "\xff", "backticks": "`", "bidi control": "\u202e", "newline": "\n", "3-byte printable": "\u20ac"} {
		t.Run(name, func(t *testing.T) {
			long := func(size int) []string {
				items := make([]string, 20)
				for i := range items {
					items[i] = strings.Repeat(unit, size/len(unit))
				}
				return items
			}
			// The payload cuts entries to 150 bytes; the renderer must hold even for entries that were not cut.
			for _, size := range []int{150, 4096} {
				s := pullSummary(commandtext.CoLocated)
				s.Protected, s.Paths, s.Include, s.Exclude = 1<<62, long(size), long(size), long(size)
				s.IncludeTotal, s.ExcludeTotal, s.Proposal, s.Revision = 1<<62, 1<<62, 999999999, 999999999
				body := render(t, func() string { return commandtext.BootstrapSummary(s) })
				if body == "" || len(body) >= maxComment/2 { // headroom: half of the limit
					t.Errorf("entries of %d bytes: body has %d bytes, want 0 < n < %d", size, len(body), maxComment/2)
				}
				if !utf8.ValidString(body) {
					t.Error("body is not valid UTF-8")
				}
			}
		})
	}
}

func TestBootstrapSummaryPanicsOnASourceThatDoesNotFit(t *testing.T) {
	for name, mutate := range map[string]func(*commandtext.Summary){
		"an unknown source":     func(s *commandtext.Summary) { s.Source = 9 },
		"a pull with a branch":  func(s *commandtext.Summary) { s.Branch = "main" },
		"a pull without a head": func(s *commandtext.Summary) { s.Origin = "" },
	} {
		t.Run(name, func(t *testing.T) {
			s := pullSummary(commandtext.Isolated)
			mutate(&s)
			mustPanic(t, func() { commandtext.BootstrapSummary(s) })
		})
	}
}

// A summary without runner rules, nil or empty, states that there are none.
func TestBootstrapSummaryListsTheRunnerRulesBetweenIncludeAndExclude(t *testing.T) {
	rules := func(n int) []string {
		items := make([]string, n)
		for i := range items {
			items[i] = "run" + strconv.Itoa(i) + ".toml"
		}
		return items
	}
	s := summary(3, pathList(3), commandtext.Isolated)

	s.Runner = []string{"pytest.ini", "conftest.py"}
	body := render(t, func() string { return commandtext.BootstrapSummary(s) })
	if !strings.Contains(body, "Scope include rules:\n\n```\ntests/**\nfixtures/\n```\n\nScope runner rules:\n\n```\npytest.ini\nconftest.py\n```\n\nScope exclude rules:") {
		t.Errorf("the runner rules do not sit between the include and exclude rules:\n%s", body)
	}

	s.Runner = []string{}
	body = render(t, func() string { return commandtext.BootstrapSummary(s) })
	if !strings.Contains(body, "Scope runner rules: none.") {
		t.Errorf("an empty runner list is not stated as none:\n%s", body)
	}

	s.Runner, s.RunnerTotal = rules(20), 27
	body = render(t, func() string { return commandtext.BootstrapSummary(s) })
	if !strings.Contains(body, "Scope runner rules (20 listed; 7 more are not listed)") || !strings.Contains(body, "run19.toml") {
		t.Errorf("the runner rules do not count the ones left out:\n%s", body)
	}

	s.Runner, s.RunnerTotal = rules(25), 0
	body = render(t, func() string { return commandtext.BootstrapSummary(s) })
	if strings.Contains(body, "run20.toml") || !strings.Contains(body, "Scope runner rules (20 listed; 5 more are not listed)") {
		t.Errorf("the runner rules are not bounded to 20:\n%s", body)
	}

	s.Runner = nil
	if body := render(t, func() string { return commandtext.BootstrapSummary(s) }); !strings.Contains(body, "Scope runner rules: none.") {
		t.Errorf("a nil runner list is not stated as none:\n%s", body)
	}
}

func TestBootstrapSummaryWithRunnerRulesKeepsTheRestOfTheText(t *testing.T) {
	s := summary(1, pathList(1), commandtext.Isolated)
	without := render(t, func() string { return commandtext.BootstrapSummary(s) })
	s.Runner = []string{"pytest.ini"}
	with := render(t, func() string { return commandtext.BootstrapSummary(s) })
	if want := strings.Replace(without, "Scope runner rules: none.", "Scope runner rules:\n\n```\npytest.ini\n```", 1); with != want {
		t.Errorf("the runner section is not the only change:\n%s", with)
	}
}

func TestBootstrapSummaryWithRunnerRulesFitsInAComment(t *testing.T) {
	for name, unit := range map[string]string{"plain": "a", "invalid UTF-8": "\xff", "backticks": "`", "bidi control": "\u202e", "newline": "\n", "3-byte printable": "\u20ac"} {
		t.Run(name, func(t *testing.T) {
			long := make([]string, 20)
			for i := range long {
				long[i] = strings.Repeat(unit, 4096/len(unit))
			}
			s := pullSummary(commandtext.CoLocated)
			s.Protected, s.Paths, s.Include, s.Runner, s.Exclude = 1<<62, long, long, long, long
			s.IncludeTotal, s.RunnerTotal, s.ExcludeTotal, s.Proposal, s.Revision = 1<<62, 1<<62, 1<<62, 999999999, 999999999
			body := render(t, func() string { return commandtext.BootstrapSummary(s) })
			if body == "" || len(body) >= maxComment {
				t.Errorf("body has %d bytes, want 0 < n < %d", len(body), maxComment)
			}
			if !utf8.ValidString(body) {
				t.Error("body is not valid UTF-8")
			}
		})
	}
}

// The same text as the branch golden above, with the runner section a declaration's runner rules add.
func TestBootstrapSummaryWithRunnerRulesHasItsExactText(t *testing.T) {
	want := "**SuiteWard bootstrap proposal P1-R1**\n\n" +
		"SuiteWard proposes this repository's protected baseline as P1-R1. Nothing is canonical yet.\n\n" +
		"- Origin commit: `" + origin + "` on branch `main` (pinned: it does not move if the branch advances)\n" +
		"- Protected files: 1\n" +
		"- Installation tier: `isolated`\n\n" +
		"Inventory:\n\n```\ntests/case_a_test.go\n```\n\n" +
		"Scope include rules:\n\n```\ntests/**\nfixtures/\n```\n\n" +
		"Scope runner rules:\n\n```\npytest.ini\nconftest.py\n```\n\n" +
		"Scope exclude rules:\n\n```\ntests/tmp/\n```\n\n" +
		"Review the inventory and the scope. To approve this exact baseline, comment:\n\n" +
		"```\n/suiteward approve P1-R1 <code>\n```\n\nReplace `<code>` with a current code from your authenticator app.\n\n" +
		"Approving P1-R1 does not approve the other changes in this PR.\n"
	s := summary(1, pathList(1), commandtext.Isolated)
	s.Runner = []string{"pytest.ini", "conftest.py"}
	if got := render(t, func() string { return commandtext.BootstrapSummary(s) }); got != want {
		t.Errorf("the text with runner rules changed:\n%s", got)
	}
}
