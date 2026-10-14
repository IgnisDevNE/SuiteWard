package commandtext

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Outcome is the processed result an acknowledgment reports.
type Outcome uint8

const (
	Approved Outcome = iota + 1
	Revoked
	NoActiveApproval
	Rejected
	MalformedCommand
	InvalidCode
	ReplayedCode
	NoSuite
	EditedFirst
	BootstrapPromoted
	RevokedAfterPromotion
	ApprovedAfterPromotion
)

// Rejection is the domain reason of a Rejected outcome.
type Rejection uint8

const (
	Unauthorized Rejection = iota + 1
	UnknownRevision
	SupersededRevision
	ContextMismatch
	PolicyMismatch
	ObsoleteCommand
)

// Ack is what an acknowledgment states.
//   - Proposal and Revision name the revision. They are required, except for
//     MalformedCommand and EditedFirst, which have no trustworthy reference and
//     must leave both zero.
//   - Rejection is set only for Rejected, Malformed only for MalformedCommand,
//     Version (V<n>) only for BootstrapPromoted.
//   - Owner is the owner's GitHub login.
//   - Source is the id of the comment that carried the command; the body links
//     to it. It is required, except for BootstrapPromoted, which a later job
//     posts and which must leave it zero.
//   - Eligible is whether the revision's approval requirement is met after this
//     outcome (a valid approval exists); it says nothing about promotion, which
//     other requirements can still block. It is rendered for Approved, Revoked, NoActiveApproval and Rejected, and must
//     be false for the other outcomes, which never evaluate it.
type Ack struct {
	Outcome   Outcome
	Rejection Rejection
	Malformed Reason
	Proposal  int
	Revision  int
	Version   int
	Owner     string
	Source    int64
	Eligible  bool
}

// Tier is the installation tier (ADR 0027, D-TIER).
type Tier uint8

const (
	Isolated Tier = iota + 1
	CoLocated
)

// Summary is what a bootstrap summary states (ADR 0010). Paths holds at most
// 20 of the Protected files; Origin is the 40-hex origin commit SHA. Include
// and Exclude list at most 20 rules each and may be longer; IncludeTotal and
// ExcludeTotal are how many rules there are (like Protected for Paths), and the
// body counts the unlisted ones from them. A total below the list, or zero, is
// taken as the length of the list.
type Summary struct {
	Proposal                   int
	Revision                   int
	Origin                     string
	Branch                     string
	Protected                  int
	Paths                      []string
	Include                    []string
	Exclude                    []string
	IncludeTotal, ExcludeTotal int
	Tier                       Tier
}

// Acknowledge renders the acknowledgment body of a processed command. It
// states the outcome, the reference and the next action (ADR 0011).
func Acknowledge(a Ack) string {
	checkAck(a)
	body := acknowledgment(a)
	if a.Source == 0 {
		return body
	}
	// A same-page anchor needs no host name: the publisher posts on the same PR.
	return body + "\nIn reply to [your command](#issuecomment-" + strconv.FormatInt(a.Source, 10) + ").\n"
}

func acknowledgment(a Ack) string {
	ref, who := "", "@"+a.Owner
	if a.Outcome != MalformedCommand && a.Outcome != EditedFirst {
		ref = Reference(a.Proposal, a.Revision)
	}
	// Only the approval requirement is stated: other requirements may still block promotion.
	requirement := "The approval requirement for " + ref + " is not met."
	if a.Eligible {
		requirement = "The approval requirement for " + ref + " is met."
	}
	switch a.Outcome {
	case Approved:
		status := "The approval requirement for " + ref + " is not met yet."
		if a.Eligible {
			status = "The approval requirement for " + ref + " is met; SuiteWard confirms a promotion in a separate comment."
		}
		return paragraphs(
			"Approval recorded for "+ref+" by "+who+".\n"+status,
			"To withdraw this approval, comment:",
			copyable("/suiteward revoke "+ref+" <code>"),
		)
	case Revoked:
		status := "The approval requirement for " + ref + " is not met; promotion waits for a valid approval."
		if a.Eligible {
			status = "The approval requirement for " + ref + " is still met through other valid approval."
		}
		return paragraphs(
			"Revocation recorded for "+ref+" by "+who+".\nYour approval no longer counts. "+status,
			"To approve it again, comment:",
			copyable("/suiteward approve "+ref+" <code>"),
		)
	case RevokedAfterPromotion:
		// Says nothing about an approval being withdrawn: the owner may have had none active.
		return paragraphs(
			"Revoke command for "+ref+" by "+who+" was recorded.\n"+
				ref+" is already promoted, so the canonical version remains unchanged; your revocation does not undo the promotion.",
			"Reversing it needs "+rollback,
		)
	case ApprovedAfterPromotion:
		return paragraphs(
			"Approval recorded for "+ref+" by "+who+".\n"+
				ref+" is already promoted, so the canonical version remains unchanged. No further action is needed.",
			"Reversing the promotion needs "+rollback,
		)
	case NoActiveApproval:
		return paragraphs("No active approval by " + who + " for " + ref + " to withdraw; nothing changed.\n" + requirement + "\nNo action is needed.")
	case Rejected:
		reason, next := rejectionText(a.Rejection, ref, who)
		return paragraphs("Command for " + ref + " rejected by policy: " + reason + ". Nothing changed.\n" + requirement + "\n" + next)
	case MalformedCommand:
		return paragraphs("Your `/suiteward` command was not understood: " + malformedText(a.Malformed) + ". Nothing changed.\n" +
			"Comment again with a current code, in the form `/suiteward approve <ref> <code>` or `/suiteward revoke <ref> <code>`.")
	case InvalidCode:
		return paragraphs("The confirmation code for " + ref + " was not accepted; nothing changed.\n" +
			"Comment the command again with a current code from your authenticator app.")
	case ReplayedCode:
		return paragraphs("The confirmation code for " + ref + " was already used; each code works only once, so nothing changed.\n" +
			"Wait for your authenticator app to show a new code, then comment the command again.")
	case NoSuite:
		return paragraphs(ref + " was not processed: this repository's SuiteWard bootstrap is still pending, so nothing changed.\n" +
			"Wait for the bootstrap summary in an open PR and approve its baseline, then comment again with a new code.")
	case EditedFirst:
		return paragraphs("Your comment was edited before SuiteWard first read it, so it was not processed as a command; nothing changed.\n" +
			"Post the command as a new comment with a current code. Edits to a command comment are never processed.")
	case BootstrapPromoted:
		return paragraphs(fmt.Sprintf("Bootstrap complete: %s, approved by %s, is now canonical version V%d.\n", ref, who, a.Version) +
			"SuiteWard recorded an integrity assessment of the approved files at the pinned origin commit; it did not run any tests.\n" +
			"Later changes to the protected files need a new proposal and your approval.")
	}
	panic(fmt.Sprintf("commandtext: unknown outcome %d", a.Outcome))
}

// The underscore admits Enterprise Managed User logins such as octocat_acme.
var loginPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,38}$`)

// checkAck panics when the fields do not fit the outcome.
func checkAck(a Ack) {
	if !loginPattern.MatchString(a.Owner) {
		panic(fmt.Sprintf("commandtext: owner login %q is not a GitHub login", a.Owner))
	}
	if (a.Outcome == BootstrapPromoted) != (a.Source == 0) || a.Source < 0 {
		panic(fmt.Sprintf("commandtext: source comment %d does not fit outcome %d", a.Source, a.Outcome))
	}
	noReference := a.Outcome == MalformedCommand || a.Outcome == EditedFirst
	consent := a.Outcome == Approved || a.Outcome == Revoked || a.Outcome == NoActiveApproval || a.Outcome == Rejected
	switch {
	case noReference && (a.Proposal != 0 || a.Revision != 0):
		panic(fmt.Sprintf("commandtext: outcome %d carries a reference", a.Outcome))
	case !consent && a.Eligible:
		panic(fmt.Sprintf("commandtext: outcome %d does not evaluate eligibility", a.Outcome))
	case (a.Outcome == Rejected) != (a.Rejection != 0):
		panic(fmt.Sprintf("commandtext: rejection %d does not fit outcome %d", a.Rejection, a.Outcome))
	case (a.Outcome == MalformedCommand) != (a.Malformed != 0):
		panic(fmt.Sprintf("commandtext: malformed reason %d does not fit outcome %d", a.Malformed, a.Outcome))
	case (a.Outcome == BootstrapPromoted) != (a.Version != 0):
		panic(fmt.Sprintf("commandtext: version %d does not fit outcome %d", a.Version, a.Outcome))
	case a.Outcome == BootstrapPromoted:
		checkID("version", a.Version)
	}
}

func rejectionText(r Rejection, ref, who string) (reason, next string) {
	const latest = "Review the latest revision of this proposal and approve that one."
	switch r {
	case Unauthorized:
		return who + " is not authorized for this action under the project's policy",
			"Only the enrolled owner can approve or revoke; check the owner set up on this SuiteWard instance."
	case UnknownRevision:
		return ref + " is not a revision of this project",
			"Check the reference in the latest SuiteWard summary and comment again with a new code."
	case SupersededRevision:
		return ref + " has been superseded by a newer revision", latest
	case ContextMismatch:
		return ref + " no longer matches its source or integration target", latest
	case PolicyMismatch:
		return "the project's policy changed after " + ref + " was proposed", latest
	case ObsoleteCommand:
		return "a later command for " + ref + " was already processed",
			"Comment again with a new code if you still want this change."
	}
	panic(fmt.Sprintf("commandtext: unknown rejection %d", r))
}

func malformedText(r Reason) string {
	switch r {
	case UnknownVerb:
		return "the action must be `approve` or `revoke`"
	case MissingReference:
		return "the proposal reference is missing"
	case BadReference:
		return "the reference must have the form `P<n>-R<m>`"
	case MissingCode:
		return "the 6-digit confirmation code is missing"
	case BadCode:
		return "the confirmation code must be exactly 6 digits"
	case ExtraText:
		return "nothing may follow the code on the command line"
	}
	panic(fmt.Sprintf("commandtext: unknown malformed reason %d", r))
}

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// BootstrapSummary renders the bootstrap summary body (ADR 0010 step 3).
// The branch, paths and scope rules come from the repository, so they are
// shown only inside code, with control characters escaped.
func BootstrapSummary(s Summary) string {
	ref := Reference(s.Proposal, s.Revision)
	if !shaPattern.MatchString(s.Origin) {
		panic(fmt.Sprintf("commandtext: origin %q is not a 40-hex commit SHA", s.Origin))
	}
	if s.Branch == "" {
		panic("commandtext: empty default branch")
	}
	if len(s.Paths) > maxListed || s.Protected < len(s.Paths) {
		panic(fmt.Sprintf("commandtext: %d paths for %d protected files", len(s.Paths), s.Protected))
	}
	var tier string
	switch s.Tier {
	case Isolated:
		tier = "isolated"
	case CoLocated:
		tier = "co-located"
	default:
		panic(fmt.Sprintf("commandtext: unknown tier %d", s.Tier))
	}
	count := fmt.Sprintf("- Protected files: %d", s.Protected)
	if unlisted := s.Protected - len(s.Paths); unlisted > 0 {
		count += fmt.Sprintf(" (%d listed below; %d more are not listed)", len(s.Paths), unlisted)
	}
	parts := []string{
		"**SuiteWard bootstrap proposal " + ref + "**",
		"SuiteWard proposes this repository's protected baseline as " + ref + ". Nothing is canonical yet.",
		"- Origin commit: `" + s.Origin + "` on branch " + codeSpan(display(s.Branch)) + " (pinned: it does not move if the branch advances)\n" +
			count + "\n" +
			"- Installation tier: `" + tier + "`",
	}
	parts = append(parts, listing("Inventory", s.Paths, len(s.Paths)), listing("Scope include rules", s.Include, s.IncludeTotal), listing("Scope exclude rules", s.Exclude, s.ExcludeTotal))
	if s.Tier == CoLocated {
		parts = append(parts, "This SuiteWard instance runs beside the coding agent (co-located tier), so its assurance is reduced: "+
			"the agent can reach SuiteWard's credentials, database and confirmation secret.")
	}
	parts = append(parts,
		"Review the inventory and the scope. To approve this exact baseline, comment:",
		copyable("/suiteward approve "+ref+" <code>"),
		"Approving "+ref+" does not approve the other changes in this PR.",
	)
	return paragraphs(parts...)
}

// listing shows at most maxListed items and says how many of the total it left
// out; a total below the items is the number of items.
func listing(title string, items []string, total int) string {
	total = max(total, len(items))
	if total == 0 {
		return title + ": none."
	}
	if len(items) > maxListed {
		items = items[:maxListed]
	}
	if unlisted := total - len(items); unlisted > 0 {
		title += fmt.Sprintf(" (%d listed; %d more are not listed)", len(items), unlisted)
	}
	shown := make([]string, len(items))
	for i, item := range items {
		shown[i] = display(item)
	}
	content := strings.Join(shown, "\n")
	fence := strings.Repeat("`", max(3, longestBacktickRun(content)+1))
	return title + ":\n\n" + fence + "\n" + content + "\n" + fence
}

// maxListed bounds the entries of each listing; maxShown bounds the bytes of
// one entry. With the fixed text they keep a summary far below GitHub's
// 65536-character comment limit.
const (
	maxListed = 20
	maxShown  = 300
)

// display returns s as one printable line of at most maxShown bytes, plus a
// marker when it was cut: as is when every rune is graphic, otherwise
// Go-quoted so newlines, bidi controls and invalid UTF-8 are visible. The cut
// applies to the shown text, which is valid UTF-8, so it never splits a rune.
func display(s string) string {
	if !utf8.ValidString(s) || strings.IndexFunc(s, func(r rune) bool { return !strconv.IsGraphic(r) }) >= 0 {
		s = strconv.QuoteToGraphic(s)
	}
	if len(s) <= maxShown {
		return s
	}
	cut := maxShown
	for !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "… [shortened]"
}

func codeSpan(s string) string {
	delimiter := strings.Repeat("`", longestBacktickRun(s)+1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		s = " " + s + " "
	}
	return delimiter + s + delimiter
}

func longestBacktickRun(s string) int {
	longest, run := 0, 0
	for i := range len(s) {
		if s[i] != '`' {
			run = 0
			continue
		}
		run++
		longest = max(longest, run)
	}
	return longest
}

// copyable shows a command template as a code block, where GitHub keeps the
// <code> placeholder instead of reading it as an HTML tag.
func copyable(command string) string {
	return "```\n" + command + "\n```\n\nReplace `<code>` with a current code from your authenticator app."
}

func paragraphs(parts ...string) string {
	return strings.Join(parts, "\n\n") + "\n"
}

var (
	instancePattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	keyPattern      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9:_-]{0,199}$`)
)

// Marker returns the hidden marker <!-- suiteward:<instance>:<key> -->. The
// instance is 32 lowercase hex characters and the key matches
// ^[A-Za-z][A-Za-z0-9:_-]{0,199}$ (the character set cannot close the comment
// early); callers pass stored values, so anything else
// panics.
func Marker(instance, key string) string {
	if !instancePattern.MatchString(instance) {
		panic(fmt.Sprintf("commandtext: instance %q is not 32 lowercase hex characters", instance))
	}
	if !keyPattern.MatchString(key) {
		panic(fmt.Sprintf("commandtext: marker key %q is invalid", key))
	}
	return "<!-- suiteward:" + instance + ":" + key + " -->"
}

// HasMarker reports whether body ends with Marker(instance, key), ignoring
// trailing whitespace. Only the end counts: the publisher appends the marker,
// and repository text shown earlier in a body could contain any marker.
func HasMarker(body, instance, key string) bool {
	return strings.HasSuffix(strings.TrimRight(body, " \t\r\n"), Marker(instance, key))
}

// rollback is how a promotion is reversed (ADR 0018): never by a revocation.
const rollback = "a separately approved rollback (corrective) proposal: a new PR with its own exact approval, " +
	"integration and promotion of a new version, with the history kept."
