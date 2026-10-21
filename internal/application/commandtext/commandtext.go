// Package commandtext parses the owner's PR command comments and renders the
// comment bodies SuiteWard posts (D-COMMANDS, ADRs 0010, 0011 and 0027). It
// takes plain values, returns plain values and depends only on the
// domain packages (scope) and the YAML library.
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
	// lastReason is no reason: it ends the list (see lastOutcome in render.go).
	lastReason
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
// whose own text, trimmed of spaces and tabs, starts with /suiteward followed
// by a space, a tab or the end of the line. Other lines are ignored.
//
// Parse is conservative: it never reads a line that GitHub shows as code
// through a fence, an HTML comment or an indented block, and it may skip a line
// that GitHub shows as text. It skips:
//   - A fenced code block. A fence opens on a line whose text, after any block
//     quote or list item markers (>, -, *, +, N. and N)), starts with three or
//     more backticks or tildes, whatever follows them, an HTML comment marker
//     included. It closes on a line indented at most three columns that holds
//     only at least as many of the same character. An unclosed fence runs to
//     the end of the body.
//   - A contained fence, which no line closes: one opened after a marker, or
//     indented by at least one column (it may sit in an open list item, which
//     GitHub ends with the item). It runs to the end of the body, so a
//     top-level fence indented one to three columns, and a fence in a quote or
//     a list item, hide the rest of the body though GitHub may show it as text.
//   - An HTML comment, from a line that contains <!-- up to and including the
//     next that contains -->, or to the end of the body. A comment that closes
//     on the line it opens on hides only that line. A fence marker inside a
//     comment opens nothing, and a comment marker inside a fence is code.
//   - An indented code block: a line indented four or more columns, a tab
//     counting as four and measured before trimming.
//
// Known limit: Parse models no <pre> or <code> block and no inline code span
// that crosses lines, so it reads a command that GitHub shows as code there. A
// code that is still current and unused is required, so that is no authority.
//
// The prefix and the verb match without regard to ASCII case, the reference
// keeps its P<n>-R<m> form, and only spaces and tabs separate tokens.
func Parse(body string) Parsed {
	var fence string
	contained, inComment := false, false
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if inComment {
			closed := strings.Index(line, "-->")
			inComment = closed < 0 || opensComment(line[closed+len("-->"):])
			continue
		}
		text := strings.Trim(line, " \t")
		if fence != "" {
			if !contained && closesFence(line, text, fence) {
				fence = ""
			}
			continue
		}
		if f, c := fenceStart(line, text); f != "" {
			fence, contained = f, c
			continue
		}
		if strings.Contains(line, "<!--") {
			inComment = opensComment(line)
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

// opensComment reports whether the last <!-- in s has no --> after it, so the text that follows s is inside a comment.
func opensComment(s string) bool {
	i := strings.LastIndex(s, "<!--")
	return i >= 0 && !strings.Contains(s[i+len("<!--"):], "-->")
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

// closesFence reports whether the line closes the fence: CommonMark lets a closing fence start at most three columns in.
func closesFence(line, text, fence string) bool {
	return indent(line) < 4 && strings.HasPrefix(text, fence) && strings.Trim(text, fence[:1]) == ""
}

// fenceStart returns the run that opens a fence on the line, after any block quote or list item markers, and whether the fence is
// contained: opened after a marker or indented, so possibly in a list item. Only its container ends a contained fence, and a line that
// is less indented than the container's content does that and opens a fence of its own, so no line closes a contained fence for Parse.
func fenceStart(line, text string) (fence string, contained bool) {
	inner := stripMarkers(text)
	return openingFence(inner), inner != text || indent(line) > 0
}

// stripMarkers removes the block quote and list item markers (>, -, *, +, N. and N) with the spaces after them) that start the text, so a fence
// inside a quote or a list item is seen as a fence.
func stripMarkers(text string) string {
	for {
		rest, ok := stripMarker(text)
		if !ok {
			return text
		}
		text = strings.TrimLeft(rest, " \t")
	}
}

func stripMarker(text string) (rest string, ok bool) {
	switch {
	case text == "":
		return "", false
	case text[0] == '>':
		return text[1:], true
	case strings.ContainsRune("-*+", rune(text[0])):
		return text[1:], len(text) > 1 && (text[1] == ' ' || text[1] == '\t')
	}
	n := 0
	for n < len(text) && text[n] >= '0' && text[n] <= '9' {
		n++
	}
	if n == 0 || n+1 >= len(text) || (text[n] != '.' && text[n] != ')') || (text[n+1] != ' ' && text[n+1] != '\t') {
		return "", false
	}
	return text[n+1:], true
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
