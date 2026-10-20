package commandtext

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The shape of an acknowledgment lives here: which fields each outcome may carry, the grammar of a reference and of a login. A
// caller that stores an acknowledgment calls ValidateAck (and ParseReference, ValidLogin) where it builds it, so that
// Acknowledge, which panics on what ValidateAck refuses, never meets it where it publishes.

// maxID is the largest proposal, revision or version number.
const maxID = 999999999

// The underscore admits Enterprise Managed User logins such as octocat_acme.
var loginPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,38}$`)

// ValidLogin reports whether login can be a GitHub login.
func ValidLogin(login string) bool { return loginPattern.MatchString(login) }

// ParseReference reads P<n>-R<m>, the form Reference writes: each number is 1..999999999 in ASCII decimal with no sign and no leading zero.
func ParseReference(ref string) (proposal, revision int, ok bool) {
	p, r, found := strings.Cut(ref, "-")
	p, okP := strings.CutPrefix(p, "P")
	r, okR := strings.CutPrefix(r, "R")
	if !found || !okP || !okR {
		return 0, 0, false
	}
	proposal, okP = parseID(p)
	revision, okR = parseID(r)
	if !okP || !okR {
		return 0, 0, false
	}
	return proposal, revision, true
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

// Reference returns P<n>-R<m>. Callers pass stored ids, so a value outside
// 1..999999999 is an impossible state and panics.
func Reference(proposal, revision int) string {
	ref, err := reference(proposal, revision)
	must(err)
	return ref
}

func reference(proposal, revision int) (string, error) {
	if err := checkID("proposal", proposal); err != nil {
		return "", err
	}
	if err := checkID("revision", revision); err != nil {
		return "", err
	}
	return fmt.Sprintf("P%d-R%d", proposal, revision), nil
}

func checkID(name string, id int) error {
	if id < 1 || id > maxID {
		return fmt.Errorf("commandtext: %s id %d is outside 1..%d", name, id, maxID)
	}
	return nil
}

// must panics on err: the values a renderer takes come from storage, so one that does not fit is an impossible state.
func must(err error) {
	if err != nil {
		panic(err.Error())
	}
}

// ValidateAck reports why Acknowledge would panic on a, as an error that quotes the offending value: the fields that do not fit
// the outcome (see Ack), a login that is not a GitHub login, a reference or a version outside 1..999999999 and an editor link that
// does not fit. A caller that stores the acknowledgment says only that it does not fit.
func ValidateAck(a Ack) error {
	for _, check := range []func(Ack) error{checkKnown, checkOwnerAndSource, checkReference, checkConsent, checkBase, checkRefusalAndEditor} {
		if err := check(a); err != nil {
			return err
		}
	}
	return nil
}

func checkKnown(a Ack) error {
	switch {
	case a.Outcome < Approved || a.Outcome > OtherPullRequest:
		return fmt.Errorf("commandtext: unknown outcome %d", a.Outcome)
	case a.Rejection > ObsoleteCommand:
		return fmt.Errorf("commandtext: unknown rejection %d", a.Rejection)
	case a.Malformed > ExtraText:
		return fmt.Errorf("commandtext: unknown malformed reason %d", a.Malformed)
	}
	return nil
}

func checkOwnerAndSource(a Ack) error {
	if !ValidLogin(a.Owner) {
		return fmt.Errorf("commandtext: owner login %q is not a GitHub login", a.Owner)
	}
	if (a.Outcome == BootstrapPromoted) != (a.Source == 0) || a.Source < 0 {
		return fmt.Errorf("commandtext: source comment %d does not fit outcome %d", a.Source, a.Outcome)
	}
	return nil
}

func checkReference(a Ack) error {
	_, err := referenceOf(a)
	return err
}

// referenceOf returns the reference an outcome names and "" for one that names none. It reports a reference that does
// not fit: a skip has none, so only the code outcomes and NoSuite may come without one.
func referenceOf(a Ack) (string, error) {
	switch a.Outcome {
	case MalformedCommand, EditedFirst, ProposeRefused, ProposeFailed, Skipped, SkipRefused, SuggestRefused, NothingUncovered:
		if a.Proposal != 0 || a.Revision != 0 {
			return "", fmt.Errorf("commandtext: outcome %d carries a reference", a.Outcome)
		}
		return "", nil
	case InvalidCode, ReplayedCode, NoSuite:
		if a.Proposal == 0 && a.Revision == 0 {
			return "", nil
		}
	}
	return reference(a.Proposal, a.Revision)
}

// checkConsent checks the fields that only some outcomes carry: Eligible, Rejection, Malformed and Version.
func checkConsent(a Ack) error {
	consent := a.Outcome == Approved || a.Outcome == Revoked || a.Outcome == NoActiveApproval || a.Outcome == Rejected
	switch {
	case !consent && a.Eligible:
		return fmt.Errorf("commandtext: outcome %d does not evaluate eligibility", a.Outcome)
	case (a.Outcome == Rejected) != (a.Rejection != 0):
		return fmt.Errorf("commandtext: rejection %d does not fit outcome %d", a.Rejection, a.Outcome)
	case (a.Outcome == MalformedCommand) != (a.Malformed != 0):
		return fmt.Errorf("commandtext: malformed reason %d does not fit outcome %d", a.Malformed, a.Outcome)
	case (a.Outcome == BootstrapPromoted) != (a.Version != 0):
		return fmt.Errorf("commandtext: version %d does not fit outcome %d", a.Version, a.Outcome)
	case a.Outcome == BootstrapPromoted:
		return checkID("version", a.Version)
	}
	return nil
}

func checkBase(a Ack) error {
	if a.Base < 0 || a.Base > BaseDefaultBranch || (a.Base != 0 && a.Outcome != NothingUncovered && !namesFile(a) && !tooLargeAgainstDefault(a)) || (a.Outcome == ProposeRefused && a.Base == BaseContract) {
		return fmt.Errorf("commandtext: base %d does not fit outcome %d", a.Base, a.Outcome)
	}
	return nil
}

func checkRefusalAndEditor(a Ack) error {
	if !refusalFits(a.Outcome, a.Refusal) {
		return fmt.Errorf("commandtext: refusal %d does not fit outcome %d", a.Refusal, a.Outcome)
	}
	if a.Repo == "" && a.Branch == "" {
		return nil
	}
	if !editorFits(a) {
		return fmt.Errorf("commandtext: an editor link does not fit outcome %d with refusal %d and base %d", a.Outcome, a.Refusal, a.Base)
	}
	return ValidateEditTarget(a.Repo, a.Branch)
}

// editorFits reports whether a may carry an editor link: a refusal that names a file of a branch, or a suggest whose declaration is too large.
func editorFits(a Ack) bool {
	if a.Outcome == SuggestRefused && a.Refusal == DeclarationTooLarge {
		return true // a Base, if any, is the default branch: checkBase refuses another
	}
	return namesFile(a) && (a.Base == BasePull || a.Base == BaseDefaultBranch)
}

// tooLargeAgainstDefault reports whether a is the refusal of a suggested declaration too large for the default branch's file, which is the one Base it names.
func tooLargeAgainstDefault(a Ack) bool {
	return a.Outcome == SuggestRefused && a.Refusal == DeclarationTooLarge && a.Base == BaseDefaultBranch
}

// namesFile reports whether a is the refusal of an invalid source of a suggest or a propose, which may name the file refused in Base.
func namesFile(a Ack) bool {
	return a.Refusal == InvalidSource && (a.Outcome == SuggestRefused || a.Outcome == ProposeRefused)
}

// refusalFits reports whether refusal is one of the reasons of outcome, or none for an outcome that has none.
func refusalFits(outcome Outcome, refusal Refusal) bool {
	if set, refuses := refusals[outcome]; refuses {
		_, fits := set.reasons[refusal]
		return fits
	}
	return refusal == 0
}
