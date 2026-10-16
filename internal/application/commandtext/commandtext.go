// Package commandtext parses the owner's PR command comments and renders the
// comment bodies SuiteWard posts (D-COMMANDS, ADRs 0010, 0011 and 0027). It
// takes plain values, returns plain values and imports no other SuiteWard
// package.
//
// Parsing: Parse returns a Parsed whose Kind is KindNotCommand, KindCommand
// (with its Command: Verb Approve or Revoke, Proposal, Revision and Code) or
// KindMalformed (with its Reason: UnknownVerb, MissingReference, BadReference,
// MissingCode, BadCode or ExtraText). Reference formats P<n>-R<m>.
//
// Rendering: Acknowledge renders an Ack (Outcome Approved, Revoked,
// NoActiveApproval, Rejected with a Rejection, MalformedCommand with a Reason,
// InvalidCode, ReplayedCode, NoSuite, EditedFirst, BootstrapPromoted,
// RevokedAfterPromotion or ApprovedAfterPromotion) and BootstrapSummary renders
// a Summary (from the default branch or from a pull request; at most 20 entries per listing, each shortened to 300 bytes);
// RevisionComment renders a Revision the same way. None takes any comment
// text, so a body can never echo a comment or a code. None adds the marker: the
// publisher appends Marker(instance, key), and HasMarker recognizes it.
//
// Values that callers take from storage (references, versions, logins, the
// origin SHA, marker arguments) are validated, and an invalid one panics: it
// is an impossible state, not user input.
package commandtext

import (
	"fmt"
	"strconv"
	"strings"
)

// Kind is the kind of a parsed comment body.
type Kind uint8

const (
	KindNotCommand Kind = iota + 1
	KindCommand
	KindMalformed
)

// Verb is the action of a command.
type Verb uint8

const (
	Approve Verb = iota + 1
	Revoke
	// Propose takes no reference and no code; Skip takes only a code.
	Propose
	Skip
)

// Reason says why a command line is malformed.
type Reason uint8

const (
	// UnknownVerb also covers a bare /suiteward line with no verb.
	UnknownVerb Reason = iota + 1
	MissingReference
	BadReference
	MissingCode
	BadCode
	ExtraText
)

// Command is a well-formed command line. Code is the TOTP code; it is never
// rendered.
type Command struct {
	Verb     Verb
	Proposal int
	Revision int
	Code     string
}

// Parsed is the result of Parse. Command is set only for KindCommand and
// Reason only for KindMalformed.
type Parsed struct {
	Kind    Kind
	Command Command
	Reason  Reason
}

const maxID = 999999999

// Parse reads the command line of a comment body (D-COMMANDS): the first line
// outside fenced code blocks whose text, trimmed of spaces and tabs, starts
// with /suiteward followed by a space, a tab or the end of the line. Other
// lines are ignored. A fence opens on a line whose trimmed text starts with
// three or more backticks or tildes and closes on a line holding only at least
// as many of the same character; an unclosed fence runs to the end of the
// body. Matching is case-sensitive and only spaces and tabs separate tokens.
func Parse(body string) Parsed {
	var fence string
	for line := range strings.SplitSeq(body, "\n") {
		text := strings.Trim(strings.TrimSuffix(line, "\r"), " \t")
		if fence != "" {
			if strings.HasPrefix(text, fence) && strings.Trim(text, fence[:1]) == "" {
				fence = ""
			}
			continue
		}
		if f := openingFence(text); f != "" {
			fence = f
			continue
		}
		rest, ok := strings.CutPrefix(text, "/suiteward")
		if ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
			return parseLine(rest)
		}
	}
	return Parsed{Kind: KindNotCommand}
}

// openingFence returns the run of fence characters that opens a code block,
// or "" when the line opens none.
func openingFence(text string) string {
	for _, c := range []string{"`", "~"} {
		if strings.HasPrefix(text, c+c+c) {
			return text[:len(text)-len(strings.TrimLeft(text, c))]
		}
	}
	return ""
}

func parseLine(rest string) Parsed {
	tokens := strings.FieldsFunc(rest, func(r rune) bool { return r == ' ' || r == '\t' })
	bad := func(reason Reason) Parsed { return Parsed{Kind: KindMalformed, Reason: reason} }
	var verb Verb
	switch {
	case len(tokens) == 0:
		return bad(UnknownVerb)
	case tokens[0] == "approve":
		verb = Approve
	case tokens[0] == "revoke":
		verb = Revoke
	default:
		return bad(UnknownVerb)
	}
	if len(tokens) < 2 {
		return bad(MissingReference)
	}
	proposal, revision, ok := parseReference(tokens[1])
	if !ok {
		return bad(BadReference)
	}
	if len(tokens) < 3 {
		return bad(MissingCode)
	}
	if len(tokens[2]) != 6 || !digits(tokens[2]) {
		return bad(BadCode)
	}
	if len(tokens) > 3 {
		return bad(ExtraText)
	}
	return Parsed{Kind: KindCommand, Command: Command{Verb: verb, Proposal: proposal, Revision: revision, Code: tokens[2]}}
}

func parseReference(ref string) (proposal, revision int, ok bool) {
	p, r, found := strings.Cut(ref, "-")
	p, okP := strings.CutPrefix(p, "P")
	r, okR := strings.CutPrefix(r, "R")
	if !found || !okP || !okR {
		return 0, 0, false
	}
	proposal, okP = parseID(p)
	revision, okR = parseID(r)
	return proposal, revision, okP && okR
}

// parseID accepts 1..999999999 in ASCII decimal with no sign or leading zero.
func parseID(s string) (int, bool) {
	if s == "" || len(s) > 9 || s[0] == '0' || !digits(s) {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		panic(fmt.Sprintf("commandtext: validated id %q did not convert: %v", s, err))
	}
	return n, true
}

func digits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Reference returns P<n>-R<m>. Callers pass stored ids, so a value outside
// 1..999999999 is an impossible state and panics.
func Reference(proposal, revision int) string {
	checkID("proposal", proposal)
	checkID("revision", revision)
	return fmt.Sprintf("P%d-R%d", proposal, revision)
}

func checkID(name string, id int) {
	if id < 1 || id > maxID {
		panic(fmt.Sprintf("commandtext: %s id %d is outside 1..%d", name, id, maxID))
	}
}
