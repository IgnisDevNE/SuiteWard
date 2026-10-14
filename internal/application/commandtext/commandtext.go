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
// InvalidCode, ReplayedCode, NoSuite, EditedFirst or BootstrapPromoted) and
// BootstrapSummary renders a Summary. Neither takes any comment text, so a
// body can never echo a comment or a code. Neither adds the marker: the
// publisher appends Marker(instance, key), and HasMarker recognizes it.
//
// Values that callers take from storage (references, versions, logins, the
// origin SHA, marker arguments) are validated, and an invalid one panics: it
// is an impossible state, not user input.
package commandtext

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

const kindNotImplemented Kind = 255

// Parse reads the command line of a comment body (D-COMMANDS).
func Parse(body string) Parsed {
	return Parsed{Kind: kindNotImplemented}
}

// Reference returns P<n>-R<m>. Callers pass stored ids, so a value outside
// 1..999999999 is an impossible state and panics.
func Reference(proposal, revision int) string {
	panic("not implemented")
}
