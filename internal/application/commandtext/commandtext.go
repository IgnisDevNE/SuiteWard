// Package commandtext parses the owner's PR command comments and renders the
// comment bodies SuiteWard posts (D-COMMANDS, ADRs 0010, 0011 and 0027). It
// takes plain values, returns plain values and imports no other SuiteWard
// package.
//
// Parsing: Parse returns a Parsed whose Kind is KindNotCommand, KindCommand
// (with its Command: Verb Approve, Revoke, Propose, Skip or Suggest, Proposal, Revision and Code; Propose and Suggest have
// neither reference nor code, Skip only a code) or KindMalformed (with its Reason: UnknownVerb, MissingReference, BadReference,
// MissingCode, BadCode or ExtraText). Reference writes P<n>-R<m> and ParseReference reads it.
//
// Rendering: Acknowledge renders an Ack (Outcome Approved, Revoked,
// NoActiveApproval, Rejected with a Rejection, MalformedCommand with a Reason,
// InvalidCode, ReplayedCode, NoSuite, EditedFirst, BootstrapPromoted,
// RevokedAfterPromotion, ApprovedAfterPromotion, OtherPullRequest, ProposeRefused, SkipRefused or SuggestRefused with a Refusal, AlreadyProposed,
// ProposeFailed, Skipped or NothingUncovered), BootstrapSummary renders
// a Summary (from the default branch or from a pull request; at most 20 entries per listing, each shortened to 300 bytes),
// RevisionComment renders a Revision the same way (with the rules and paths a reduction removes), and BootstrapNotice and
// BootstrapCancelled render the comments of a pull request before the first contract. SuggestDeclaration renders a
// DeclarationSuggestion (include and runner patterns, a cut notice and the mixed files it does not suggest) as the reply
// to a propose without a declaration, or, with Suggest set, as the reply to /suiteward suggest. After the first contract, ScopeNoticeComment renders a ScopeNotice, the test-like
// paths that the contract does not protect with the declaration to commit, and ScopeSuggestionBody renders the body of a
// review comment that suggests new patterns for a list of .suiteward.yml. RunnerUncoveredComment renders the comment of a
// runner-uncovered check, with RunnerChoiceBody for its review suggestions, InvalidSourceComment the comment of an
// invalid-source check, CheckSummary the output of a check run and MergeComment the outcome of a merged pull request.
// None takes any comment text, so a body can never echo a comment or a code. None adds the marker: the
// publisher appends Marker(instance, key), and HasMarker recognizes it.
//
// Values that callers take from storage (references, versions, logins, the
// origin SHA, marker arguments) are validated, and an invalid one panics: it
// is an impossible state, not user input. A caller that stores a payload checks it where it builds it with
// ValidateAck, ValidateSuggestion, ValidateScopeNotice, ValidateScopeSuggestion, ValidateRunnerUncovered,
// ValidateRunnerChoice, ValidateInvalidSource or ValidateEditTarget: each reports as an error what its renderer panics on.
// A declaration that a comment shows in a fence is shown as it is, so those validators refuse one that holds a character that
// is not graphic other than a newline (LF or CRLF) and a tab.
package commandtext

import "strings"

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
	// Suggest takes neither reference nor code.
	Suggest
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

const prefix = "/suiteward"

// Parse reads the command line of a comment body (D-COMMANDS): the first line
// that GitHub shows as text whose own text, trimmed of spaces and tabs, starts
// with /suiteward followed by a space, a tab or the end of the line. Other
// lines are ignored. GitHub shows no text in a fenced code block, in an HTML
// comment or in an indented code block, so Parse skips them. A fence opens on a
// line whose trimmed text starts with three or more backticks or tildes and
// closes on a line holding only at least as many of the same character; an
// unclosed fence runs to the end of the body. An HTML comment hides every line
// from one that contains <!-- up to and including the next that contains -->,
// or to the end of the body, and a comment that closes on the line it opens on
// hides only that line; a fence marker inside a comment opens nothing, and a
// comment marker inside a fence is code. A line indented four or more columns,
// a tab counting as four and measured before trimming, is an indented code
// block. The prefix and the verb match without regard to ASCII case, the reference keeps its P<n>-R<m> form, and only
// spaces and tabs separate tokens.
func Parse(body string) Parsed {
	var fence string
	inComment := false
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if inComment {
			inComment = !strings.Contains(line, "-->")
			continue
		}
		text := strings.Trim(line, " \t")
		if fence != "" {
			if strings.HasPrefix(text, fence) && strings.Trim(text, fence[:1]) == "" {
				fence = ""
			}
			continue
		}
		if _, after, opens := strings.Cut(line, "<!--"); opens {
			inComment = !strings.Contains(after, "-->")
			continue
		}
		if f := openingFence(text); f != "" {
			fence = f
			continue
		}
		if indent(line) >= 4 {
			continue
		}
		if len(text) >= len(prefix) && asciiLower(text[:len(prefix)]) == prefix {
			rest := text[len(prefix):]
			if rest == "" || rest[0] == ' ' || rest[0] == '\t' {
				return parseLine(rest)
			}
		}
	}
	return Parsed{Kind: KindNotCommand}
}

// indent is the width of the spaces and tabs that start the line, a tab counting as four columns.
func indent(line string) (columns int) {
	for _, c := range line {
		switch c {
		case ' ':
			columns++
		case '\t':
			columns += 4
		default:
			return columns
		}
	}
	return columns
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
	case asciiLower(tokens[0]) == "approve":
		verb = Approve
	case asciiLower(tokens[0]) == "revoke":
		verb = Revoke
	case asciiLower(tokens[0]) == "propose":
		if len(tokens) > 1 {
			return bad(ExtraText)
		}
		return Parsed{Kind: KindCommand, Command: Command{Verb: Propose}}
	case asciiLower(tokens[0]) == "suggest":
		if len(tokens) > 1 {
			return bad(ExtraText)
		}
		return Parsed{Kind: KindCommand, Command: Command{Verb: Suggest}}
	case asciiLower(tokens[0]) == "skip":
		return parseSkip(tokens[1:])
	default:
		return bad(UnknownVerb)
	}
	if len(tokens) < 2 {
		return bad(MissingReference)
	}
	proposal, revision, ok := ParseReference(tokens[1])
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

// parseSkip reads the arguments of skip: a code and nothing else, so a reference is a bad code.
func parseSkip(args []string) Parsed {
	switch {
	case len(args) == 0:
		return Parsed{Kind: KindMalformed, Reason: MissingCode}
	case len(args[0]) != 6 || !digits(args[0]):
		return Parsed{Kind: KindMalformed, Reason: BadCode}
	case len(args) > 1:
		return Parsed{Kind: KindMalformed, Reason: ExtraText}
	}
	return Parsed{Kind: KindCommand, Command: Command{Verb: Skip, Code: args[0]}}
}

func digits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// asciiLower lowers A-Z only, so no other Unicode character (the long s, the Kelvin sign) folds into a letter of the
// prefix or a verb.
func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}
