package commandtext

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
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
	// The explicit bootstrap (M1.7b rework): ProposeRefused and SkipRefused carry a Refusal, AlreadyProposed names the
	// existing proposal, ProposeFailed says a propose was dropped after its attempts.
	ProposeRefused
	AlreadyProposed
	ProposeFailed
	Skipped
	SkipRefused
	// NothingUncovered answers /suiteward suggest after the first contract when every test-like path is in the scope.
	NothingUncovered
	// SuggestRefused carries a Refusal: Fork, NotOpen or InvalidSource.
	SuggestRefused
	// OtherPullRequest answers an approve or a revoke whose reference names a proposal that another pull request carries.
	OtherPullRequest
	// The execution commands (M2.1): ReceiptRefused carries the Gate reason that refused an approve, ModeSet the Mode it recorded, ModeRefused and
	// UnblockRefused a Refusal, and Unblocked the Plans it lifted.
	ReceiptRefused
	ModeSet
	ModeRefused
	Unblocked
	UnblockRefused
	// lastOutcome is no outcome: it ends the list, so ValidateAck knows a new outcome added above it.
	lastOutcome
)

// Refusal is the reason of a ProposeRefused, SkipRefused or SuggestRefused outcome.
type Refusal uint8

const (
	CanonicalPresent Refusal = iota + 1
	Fork
	EmptyInventory
	Carrier
	InvalidSource
	NotOpen
	BaseNotTarget
	// NotUnavailable is the refusal of an unblock when no run of the head is unavailable.
	NotUnavailable
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
	// lastRejection is no rejection: it ends the list (see lastOutcome).
	lastRejection
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
	// Base, for NothingUncovered and for the InvalidSource refusal of a suggest, names what the head was compared with, or the file that is not a
	// valid declaration; NothingUncovered requires it.
	Base ScopeBase
	// Repo (owner/name) and Branch build the editor link of a refusal that asks to fix .suiteward.yml: SuggestRefused for an InvalidSource
	// with a Base (the file refused), and ProposeRefused for an InvalidSource with a Base of pull or default-branch.
	// Branch is the branch of that file, the pull request's head ref or the default branch; both or neither, and none is no link.
	Repo, Branch string
	// Refusal is set exactly for the refusals: ProposeRefused, SkipRefused, SuggestRefused, ModeRefused and UnblockRefused.
	Refusal Refusal
	// Gate is the reason the receipt gate refused an approve (the receipt-pending, receipt-unavailable, mode-required and mode-not-met reasons
	// of the execution package), set exactly for ReceiptRefused.
	Gate execution.Reason
	// Mode is the mode ModeSet recorded, and the mode the refused approve carried: none for a Gate of mode-required, set for mode-not-met,
	// and optional for the other reasons, whose text does not depend on it. It is none for every other outcome.
	Mode execution.Mode
	// Counts and Markers state the receipt of a ReceiptRefused whose Gate is mode-not-met, and are zero for every other outcome.
	Counts  execution.ReceiptCounts
	Markers int
	// Plans are the plans of the jobs an Unblocked lifted, at least one, and none for every other outcome.
	Plans Plans
}

// Plans says which plans of a head an unblock lifted: its canonical run, its receipt, or both.
type Plans struct{ Canonical, Receipt bool }

// Tier is the installation tier (ADR 0027, D-TIER).
type Tier uint8

const (
	Isolated Tier = iota + 1
	CoLocated
)

// Source is where a bootstrap proposal comes from (D-CHECKS, amendment of 2026-10-15). The zero value is the
// default branch.
type Source uint8

const (
	SourceBranch Source = iota
	SourcePull
)

// Summary is what a bootstrap summary states (ADR 0010). Paths holds at most
// 20 of the Protected files; Origin is the 40-hex origin commit SHA. Include
// and Exclude list at most 20 rules each and may be longer; IncludeTotal and
// ExcludeTotal are how many rules there are (like Protected for Paths), and the
// body counts the unlisted ones from them. A total below the list, or zero, is
// taken as the length of the list. Runner lists the runner rules the same way, with
// RunnerTotal; the body states that there are none for an empty one.
type Summary struct {
	Proposal                                int
	Revision                                int
	Source                                  Source
	Origin                                  string
	Branch                                  string
	Protected                               int
	Paths                                   []string
	Include                                 []string
	Runner                                  []string
	Exclude                                 []string
	IncludeTotal, RunnerTotal, ExcludeTotal int
	Tier                                    Tier
}

// Acknowledge renders the acknowledgment body of a processed command. It
// states the outcome, the reference and the next action (ADR 0011). An Ack that
// ValidateAck refuses panics.
func Acknowledge(a Ack) string {
	must(ValidateAck(a))
	return replyTo(acknowledgment(a), a.Source)
}

// replyTo links the command comment a body answers; source 0 means none. A same-page anchor needs no host name:
// the publisher posts on the same PR.
func replyTo(body string, source int64) string {
	if source == 0 {
		return body
	}
	return body + "\nIn reply to [your command](#issuecomment-" + strconv.FormatInt(source, 10) + ").\n"
}

// acknowledgment sends the outcome to the group that renders it; ValidateAck has run, so an outcome that no group takes is an impossible state.
func acknowledgment(a Ack) string {
	switch a.Outcome {
	case Approved, Revoked, NoActiveApproval, Rejected, RevokedAfterPromotion, ApprovedAfterPromotion:
		return consentAck(a)
	case InvalidCode, ReplayedCode, NoSuite, MalformedCommand, EditedFirst, OtherPullRequest:
		return commandAck(a)
	case ProposeRefused, SkipRefused, SuggestRefused, ModeRefused, UnblockRefused:
		return refusal(a)
	case ReceiptRefused, ModeSet, Unblocked:
		return executionAck(a)
	case AlreadyProposed, ProposeFailed, Skipped, NothingUncovered, BootstrapPromoted:
		return bootstrapAck(a)
	}
	panic(fmt.Sprintf("commandtext: unknown outcome %d", a.Outcome))
}

// notInGroup is the end of a group that was handed an outcome it does not render.
func notInGroup(o Outcome) string {
	panic(fmt.Sprintf("commandtext: outcome %d is not in this group", o))
}

// consentAck renders the outcomes of an approval or a revocation, which state the approval requirement of the reference.
func consentAck(a Ack) string {
	ref, who := mustReference(a), "@"+a.Owner
	// Only the approval requirement is stated: other requirements may still block promotion.
	requirement := "The approval requirement for " + ref + " is not met."
	if a.Eligible {
		requirement = "The approval requirement for " + ref + " is met."
	}
	switch a.Outcome {
	case Approved:
		status := "The approval requirement for " + ref + " is not met yet."
		if a.Eligible {
			status = requirement
		}
		return paragraphs(
			"Approval recorded for "+ref+" by "+who+".\n"+status,
			"To withdraw this approval, comment:",
			codeCommand("revoke", ref),
		)
	case Revoked:
		status := requirement
		if a.Eligible {
			status = "The approval requirement for " + ref + " is still met through other valid approval."
		}
		return paragraphs(
			"Revocation recorded for "+ref+" by "+who+".\nYour approval no longer counts. "+status,
			"To approve it again, comment:",
			codeCommand("approve", ref),
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
	}
	return notInGroup(a.Outcome)
}

// wrongCode finishes the first sentence of the acknowledgment of a wrong code: the command had no effect, but the check fails for
// it (D-COMMANDS, "A wrong command has no effect"). code names the code the next command needs.
func wrongCode(first, code string) string {
	return first + " The `SuiteWard / Contract` check may now show as failed until you comment a corrected command with " + code + ", or push."
}

// commandAck renders the outcomes of a command that could not be processed as one.
func commandAck(a Ack) string {
	ref := mustReference(a)
	// " for P4-R2", or nothing for a command that has no reference (a skip).
	of := ""
	if ref != "" {
		of = " for " + ref
	}
	switch a.Outcome {
	case InvalidCode:
		return paragraphs(wrongCode("The confirmation code"+of+" was not accepted, so the command had no effect.", "a current code"))
	case ReplayedCode:
		return paragraphs(wrongCode("The confirmation code"+of+" was already used, so the command had no effect; each code works only once.", "a new code") +
			"\nWait for your authenticator app to show a new code first.")
	case NoSuite:
		subject := "Your command"
		if ref != "" {
			subject = ref
		}
		return paragraphs(subject + " was not processed: this repository's SuiteWard bootstrap is still pending, so nothing changed.\n" +
			"Wait for SuiteWard to finish setting up this repository, then comment `/suiteward propose` on the pull request that should carry the first contract.")
	case MalformedCommand:
		return paragraphs("Your `/suiteward` command was not understood: " + malformedText(a.Malformed) + ". Nothing changed.\n" +
			"Comment again in the form `/suiteward approve <ref> <code>`, `/suiteward revoke <ref> <code>`, `/suiteward propose`, `/suiteward skip <code>`, `/suiteward suggest`, " +
			"`/suiteward mode <fix|feature|refactor>` or `/suiteward unblock <code>`, " +
			"with a current code from your authenticator app where the command takes one.")
	case OtherPullRequest:
		return paragraphs(ref + " belongs to another pull request; comment the command on that pull request. Nothing changed.")
	case EditedFirst:
		return paragraphs("Your comment was edited before SuiteWard first read it, so it was not processed as a command; nothing changed.\n" +
			"Post the command as a new comment with a current code. Edits to a command comment are never processed.")
	}
	return notInGroup(a.Outcome)
}

// bootstrapAck renders the outcomes of the bootstrap that are neither a refusal nor a consent.
func bootstrapAck(a Ack) string {
	ref, who := mustReference(a), "@"+a.Owner
	switch a.Outcome {
	case AlreadyProposed:
		return paragraphs("The baseline was already proposed as "+ref+". Nothing changed.",
			"Review the bootstrap summary SuiteWard posted for "+ref+" on this pull request, and approve it as described there.")
	case ProposeFailed:
		return paragraphs("SuiteWard could not process your `/suiteward propose` command after several attempts, so it proposed nothing. Nothing changed.\n" +
			"Please comment `/suiteward propose` again.")
	case Skipped:
		return paragraphs("Skip recorded for this pull request by "+who+".\n"+
			"This pull request may merge before the first contract: nothing was assessed. This is not a contract assessment, and no canonical contract exists yet.",
			"To start the first contract instead, comment `/suiteward propose` on the pull request that should carry it.")
	case NothingUncovered:
		subject, _ := baseNames(a.Base)
		return paragraphs("`/suiteward suggest`: every test-like path in this pull request's head is covered by " + subject + " already, so there is nothing to suggest. Nothing changed.\n" +
			"This answer is not an approval, and SuiteWard does not run tests.")
	case BootstrapPromoted:
		return paragraphs(fmt.Sprintf("Bootstrap complete: %s, approved by %s, is now canonical version V%d.\n", ref, who, a.Version) +
			"SuiteWard recorded an integrity assessment of the approved files at the pinned origin commit; it did not run any tests.\n" +
			"Later changes to the protected files need a new proposal and your approval.")
	}
	return notInGroup(a.Outcome)
}

// mustReference is the reference ValidateAck accepted for a, "" for an outcome that names none.
func mustReference(a Ack) string {
	ref, err := referenceOf(a)
	must(err)
	return ref
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
		return "the action must be `approve`, `revoke`, `propose`, `skip`, `suggest`, `mode` or `unblock`"
	case MissingReference:
		return "the proposal reference is missing"
	case BadReference:
		return "the reference must have the form `P<n>-R<m>`"
	case MissingCode:
		return "the 6-digit confirmation code is missing"
	case BadCode:
		return "the confirmation code must be exactly 6 digits"
	case ExtraText:
		return "nothing may follow the end of the command"
	case MissingMode:
		return "the mode is missing, it must be `fix`, `feature` or `refactor`"
	case BadMode:
		return "the mode must be `fix`, `feature` or `refactor`"
	}
	panic(fmt.Sprintf("commandtext: unknown malformed reason %d", r))
}

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// BootstrapSummary renders the bootstrap summary body (ADR 0010 step 3). The branch, paths and scope rules
// come from the repository, so they are shown only inside code, with control characters escaped. From a pull
// request (SourcePull), Origin is the pull request's head and there is no branch.
func BootstrapSummary(s Summary) string {
	ref := Reference(s.Proposal, s.Revision)
	if !shaPattern.MatchString(s.Origin) {
		panic(fmt.Sprintf("commandtext: origin %q is not a 40-hex commit SHA", s.Origin))
	}
	if s.Source != SourceBranch && s.Source != SourcePull {
		panic(fmt.Sprintf("commandtext: unknown bootstrap source %d", s.Source))
	}
	if (s.Branch == "") != (s.Source == SourcePull) {
		panic(fmt.Sprintf("commandtext: bootstrap source %d does not fit the default branch %q", s.Source, s.Branch))
	}
	if len(s.Paths) > maxListed || s.Protected < len(s.Paths) {
		panic(fmt.Sprintf("commandtext: %d paths for %d protected files", len(s.Paths), s.Protected))
	}
	count := fmt.Sprintf("- Protected files: %d", s.Protected)
	if unlisted := s.Protected - len(s.Paths); unlisted > 0 {
		count += fmt.Sprintf(" (%d listed below; %d more are not listed)", len(s.Paths), unlisted)
	}
	intro := "SuiteWard proposes this repository's protected baseline as " + ref + ". Nothing is canonical yet."
	origin := "- Origin commit: `" + s.Origin + "` on branch " + codeSpan(display(s.Branch)) + " (pinned: it does not move if the branch advances)"
	review := "Review the inventory and the scope. To approve this exact baseline, comment:"
	after := "Approving " + ref + " does not approve the other changes in this PR."
	if s.Source == SourcePull {
		intro = "SuiteWard proposes the protected files of this pull request, at its exact head, as this repository's first baseline " + ref + ". Nothing is canonical yet."
		origin = "- Head commit: `" + s.Origin + "` (the exact head of this pull request)"
		review = "Review the inventory and the scope. To approve this exact head, comment:"
		after = "Approving " + ref + " lets this pull request merge. SuiteWard promotes V1 only from the exact merge commit after the merge. " +
			"A later push that changes the protected files needs a new approval."
	}
	parts := []string{
		"**SuiteWard bootstrap proposal " + ref + "**",
		intro,
		origin + "\n" + count + "\n" + tierLine(s.Tier),
	}
	parts = append(parts, listing("Inventory", s.Paths, len(s.Paths)), listing("Scope include rules", s.Include, s.IncludeTotal),
		listing("Scope runner rules", s.Runner, s.RunnerTotal), listing("Scope exclude rules", s.Exclude, s.ExcludeTotal))
	if s.Tier == CoLocated {
		parts = append(parts, coLocatedNotice)
	}
	parts = append(parts, review, codeCommand("approve", ref), after)
	return paragraphs(parts...)
}

// coLocatedNotice is what a body says when the instance runs beside the coding agent (ADR 0027).
const coLocatedNotice = "This SuiteWard instance runs beside the coding agent (co-located tier), so its assurance is reduced: " +
	"the agent can reach SuiteWard's credentials, database and confirmation secret."

// tierName is the name of a tier; a value that is not one is an impossible state.
func tierName(t Tier) string {
	switch t {
	case Isolated:
		return "isolated"
	case CoLocated:
		return "co-located"
	}
	panic(fmt.Sprintf("commandtext: unknown tier %d", t))
}

// tierLine is the fact that states the installation tier.
func tierLine(t Tier) string { return "- Installation tier: `" + tierName(t) + "`" }

// listing shows at most maxListed items and says how many of the total it left
// out; a total below the items is the number of items.
func listing(title string, items []string, total int) string {
	total = max(total, len(items))
	if total == 0 {
		return title + ": none."
	}
	items = items[:min(len(items), maxListed)]
	if unlisted := total - len(items); unlisted > 0 {
		title += fmt.Sprintf(" (%d listed; %d more are not listed)", len(items), unlisted)
	}
	return title + ":\n\n" + fencedLines(items)
}

// listingAndMore is listing for paths: the entries inside a fence, then "and N more" for the ones the total counts
// beyond them. The items are at most maxListed (the validators cap them); a total below them is their number.
func listingAndMore(title string, items []string, total int) string {
	body := listing(title, items, len(items))
	if unlisted := total - len(items); unlisted > 0 {
		body += fmt.Sprintf("\n\nand %d more", unlisted)
	}
	return body
}

// fence shows block, which ends with a newline, in a code fence that a run of backticks inside it cannot close, with info after the opening fence.
func fence(info, block string) string {
	ticks := strings.Repeat("`", max(3, longestBacktickRun(block)+1))
	return ticks + info + "\n" + block + ticks
}

func fenced(block string) string { return fence("", block) }

// mixedWarning says that files may hold test settings among other content and are not suggested. They are listed only
// in text: a mixed file never goes into a declaration.
func mixedWarning(mixed []string) string {
	return "These files may hold test settings among other content, so SuiteWard does not suggest them: protecting one would put every change to it, " +
		"even a dependency update, behind an approval. Decide for yourself:\n\n" + fencedLines(mixed)
}

// submodulesWarning says that the submodules a suggested pattern reaches are outside the scope: what is inside them is not
// protected, because SuiteWard never reads a submodule.
func submodulesWarning(submodules []string) string {
	return "A suggested pattern reaches these submodules, but their contents are not protected: SuiteWard never reads a submodule, " +
		"so a change inside one needs no approval:\n\n" + fencedLines(submodules)
}

// fencedLines shows the items, one display line each, in a fence that backticks inside them cannot close.
func fencedLines(items []string) string {
	shown := make([]string, len(items))
	for i, item := range items {
		shown[i] = display(item)
	}
	return fenced(strings.Join(shown, "\n") + "\n")
}

// maxListed bounds the entries of each listing; maxShown bounds the bytes of
// one entry. With the fixed text they keep a summary far below GitHub's
// 65536-character comment limit.
const (
	maxListed = 20
	maxShown  = 300
)

// display returns s as one printable line of at most maxShown bytes, plus a
// marker when it was cut: as is when every rune is graphic and holds no HTML comment opener, otherwise
// Go-quoted so newlines, bidi controls and invalid UTF-8 are visible. An opener is written \u003c!--: Parse hides every line
// from one that holds "<!--" to the next that holds "-->", whatever Markdown makes of it (a code span shows it as text), so the
// fences that follow would be read out of step and a copyable command in one would read as a command. The cut
// applies to the shown text, which is valid UTF-8, so it never splits a rune.
func display(s string) string {
	if !shownAsIs(s) {
		s = strings.ReplaceAll(escapeHidden(strconv.QuoteToGraphic(s)), "<!--", `\u003c!--`)
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

// hidden reports whether r must never reach the owner unescaped: a rune that is not graphic, or one that is graphic to Unicode and
// draws nothing (a combining grapheme joiner, a Hangul or Khmer filler, a variation selector, a braille blank).
func hidden(r rune) bool {
	return !strconv.IsGraphic(r) || r == 0x034f || r == 0x115f || r == 0x1160 || r == 0x17b4 || r == 0x17b5 || r >= 0x180b && r <= 0x180f ||
		r == 0x2800 || r == 0x3164 || r >= 0xfe00 && r <= 0xfe0f || r == 0xffa0 || r >= 0xe0100 && r <= 0xe01ef
}

// escapeHidden escapes the hidden runes of a Go-quoted string, which strconv.QuoteToGraphic leaves as they are when they are graphic.
func escapeHidden(quoted string) string {
	var b strings.Builder
	for _, r := range quoted {
		switch {
		case !hidden(r):
			b.WriteRune(r)
		case r < 0x10000:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	return b.String()
}

// hasHidden reports whether s is not UTF-8 or holds a hidden rune: display quotes such a string.
func hasHidden(s string) bool {
	return !utf8.ValidString(s) || strings.IndexFunc(s, hidden) >= 0
}

// shownAsIs reports whether display writes s as it is: it is UTF-8 with no hidden rune and holds no HTML comment opener, so it is not quoted.
// What display shows differently from the text itself is not the text to copy, so a comment that offers the text to paste asks this too.
func shownAsIs(s string) bool {
	return !hasHidden(s) && !strings.Contains(s, "<!--")
}

// declarationLayout removes the layout of a declaration: a CRLF line ending (the form of a file written on Windows), a newline and a tab.
// A carriage return that no line feed follows is not layout.
var declarationLayout = strings.NewReplacer("\r\n", "", "\n", "", "\t", "")

// verbatim reports whether a declaration can be shown in a fence as it is: UTF-8 whose only characters that are hidden are line
// endings (LF or CRLF) and tabs. A fence shows no escape, so a bidi control or a zero-width space in it would reach the owner's clipboard unseen.
// The raw bytes must be UTF-8 before the layout is removed: removing it can join the halves of a broken sequence.
func verbatim(declaration []byte) bool {
	return utf8.Valid(declaration) && !hasHidden(declarationLayout.Replace(string(declaration)))
}

// CanShow reports whether a notice can show declaration: it is at most MaxDeclaration bytes and verbatim. The notices validate their
// declarations with it, so a caller that probes it decides what validation will.
func CanShow(declaration []byte) bool {
	return len(declaration) <= MaxDeclaration && verbatim(declaration)
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

// codeCommand shows the command verb ref <code> to copy.
func codeCommand(verb, ref string) string {
	return copyable("/suiteward " + verb + " " + ref + " <code>")
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

// Revision is what the comment of a new change-proposal revision states. Previous is the reference this revision
// supersedes (an earlier revision of the same proposal), empty for the first. Each list shows at most 20 entries
// of the total; a total below the list is taken as its length. Reduced says that the revision reduces the
// protected scope or lowers the execution policy; ReducedRules and ReducedPaths (at most 20 each, of ReducedPathsTotal paths) detail
// what it removes and are set only with Reduced. A ReducedRules entry is one that scope.Reductions writes: "rule <p>" for a
// pattern that no longer protects, "exclude <p>" for an exclude that is added, and "execution canonical: <from> -> <to>" or
// "execution receipt: <from> -> <to>" for a level of the execution policy that gets weaker. A reduction whose entries are all of the
// last kind, with no ReducedPaths and no ReducedPathsTotal, removes no path: the comment says that it lowers the execution policy.
// ReducedOmitted says that the detail did not fit the message and was left out: the comment states
// that, and counts the paths from ReducedPathsTotal; it excludes ReducedRules and ReducedPaths.
//
// Changes, when set, replaces the three lists with the numbers of the change report and names the files to look at;
// without it the comment lists the paths. ChangesUnavailable says that the files could not be read to count them, so the
// comment lists the paths and says so; it excludes Changes. ReportURL, set only with Changes, is the link to the full
// report on a loopback address of the instance, for the reference of this revision.
type Revision struct {
	Proposal, Revision                      int
	Previous                                string
	Canonical                               int
	Added, Modified, Removed                []string
	AddedTotal, ModifiedTotal, RemovedTotal int
	Reduced                                 bool
	ReducedRules, ReducedPaths              []string
	ReducedPathsTotal                       int
	ReducedOmitted                          bool
	Tier                                    Tier
	Changes                                 *Changes
	ChangesUnavailable                      bool
	ReportURL                               string
}

// RevisionComment renders the comment of a new change-proposal revision: the protected files added, modified
// and removed against the canonical (as numbers when Changes is set), the approve command for this exact revision and the
// revision it supersedes.
// It never says that tests ran. The paths come from the repository, so they are shown only inside code, bounded
// and with control characters escaped, like the bootstrap summary.
func RevisionComment(r Revision) string {
	must(validateRevision(r))
	ref := Reference(r.Proposal, r.Revision)
	parts := []string{
		"**SuiteWard contract change " + ref + "**",
		fmt.Sprintf("This pull request changes the protected files against the canonical contract V%d. SuiteWard proposes the change as %s for the owner's approval.", r.Canonical, ref),
		"- Base canonical version: V" + strconv.Itoa(r.Canonical) + "\n" + tierLine(r.Tier),
	}
	parts = append(parts, changesSection(r)...)
	if r.Reduced {
		parts = append(parts, reductionParts(r)...)
	}
	if r.Tier == CoLocated {
		parts = append(parts, coLocatedNotice)
	}
	parts = append(parts,
		"This comment is a record, not an approval. SuiteWard compares the protected files only; it does not run tests. To approve this exact revision, comment:",
		codeCommand("approve", ref),
	)
	if r.Previous != "" {
		parts = append(parts, "This supersedes `"+r.Previous+"`.")
	}
	return paragraphs(parts...)
}

// validateRevision reports why RevisionComment would panic on r.
func validateRevision(r Revision) error {
	if err := checkID("canonical version", r.Canonical); err != nil {
		return err
	}
	if r.AddedTotal < 0 || r.ModifiedTotal < 0 || r.RemovedTotal < 0 {
		return fmt.Errorf("commandtext: negative totals %d, %d and %d", r.AddedTotal, r.ModifiedTotal, r.RemovedTotal)
	}
	switch {
	case r.ReducedPathsTotal < 0 || len(r.ReducedRules) > maxListed || len(r.ReducedPaths) > maxListed:
		return fmt.Errorf("commandtext: %d reduced rules, %d reduced paths and a total of %d", len(r.ReducedRules), len(r.ReducedPaths), r.ReducedPathsTotal)
	case !r.Reduced && (len(r.ReducedRules) > 0 || len(r.ReducedPaths) > 0 || r.ReducedPathsTotal != 0):
		return errors.New("commandtext: reduction detail on a revision that does not reduce the scope")
	case slices.ContainsFunc(r.ReducedRules, func(e string) bool {
		return !strings.HasPrefix(e, "rule ") && !strings.HasPrefix(e, "exclude ") && !executionEntry(e)
	}):
		return errors.New("commandtext: a reduced rule that is none of a \"rule \", an \"exclude \", an \"execution canonical: \" and an \"execution receipt: \" entry")
	case r.ReducedOmitted && (!r.Reduced || len(r.ReducedRules) > 0 || len(r.ReducedPaths) > 0):
		return errors.New("commandtext: reduction detail omitted on a revision that does not reduce the scope or that lists it")
	}
	if _, err := reference(r.Proposal, r.Revision); err != nil {
		return err
	}
	if err := validateReport(r); err != nil {
		return err
	}
	if r.Previous == "" {
		return nil
	}
	if proposal, revision, ok := ParseReference(r.Previous); !ok || proposal != r.Proposal || revision >= r.Revision {
		return fmt.Errorf("commandtext: previous %q is not an earlier revision of proposal %d", r.Previous, r.Proposal)
	}
	return nil
}

// executionPrefixes start the entries that scope.Reductions writes for a level of the execution policy that gets weaker.
var executionPrefixes = []string{"execution canonical: ", "execution receipt: "}

// executionEntry reports whether entry lowers a level of the execution policy.
func executionEntry(entry string) bool {
	return slices.ContainsFunc(executionPrefixes, func(p string) bool { return strings.HasPrefix(entry, p) })
}

// lowersPolicyOnly reports whether the reduction of r lowers the execution policy and removes nothing from the scope: every entry
// it lists is an execution entry, and it has no path, no total of paths and no detail that was left out.
func (r Revision) lowersPolicyOnly() bool {
	return r.Reduced && len(r.ReducedRules) > 0 && len(r.ReducedPaths) == 0 && r.ReducedPathsTotal == 0 && !r.ReducedOmitted &&
		!slices.ContainsFunc(r.ReducedRules, func(e string) bool { return !executionEntry(e) })
}

// reductionParts are the paragraphs that say a revision reduces the protected scope or lowers the execution policy, with what it removes.
func reductionParts(r Revision) []string {
	var rules, excludes, levels []string // scope.Reductions marks each entry "rule <p>", "exclude <p>" or "execution <level>: <from> -> <to>"
	for _, entry := range r.ReducedRules {
		switch p, isExclude := strings.CutPrefix(entry, "exclude "); {
		case isExclude:
			excludes = append(excludes, p)
		case executionEntry(entry):
			levels = append(levels, strings.TrimPrefix(entry, "execution "))
		default:
			rules = append(rules, strings.TrimPrefix(entry, "rule "))
		}
	}
	lead := "This revision reduces the protected scope."
	switch {
	case r.lowersPolicyOnly():
		lead = "This revision lowers the execution policy."
	case len(levels) > 0:
		lead = "This revision reduces the protected scope and lowers the execution policy."
	}
	parts := []string{"**" + lead + "** Review it carefully before approving."}
	if r.ReducedOmitted {
		omitted := "The detail of this reduction was omitted because it did not fit in this comment. Compare `.suiteward.yml` with the canonical one before approving."
		if r.ReducedPathsTotal > 0 {
			omitted += fmt.Sprintf(" Paths no longer covered: %d.", r.ReducedPathsTotal)
		}
		parts = append(parts, omitted)
	}
	if len(rules) > 0 {
		parts = append(parts, listing("Rules removed from the scope", rules, len(rules)))
	}
	if len(excludes) > 0 {
		parts = append(parts, listing("Excludes added", excludes, len(excludes)))
	}
	if len(r.ReducedPaths) > 0 {
		parts = append(parts, listingAndMore("Paths that the scope no longer covers", r.ReducedPaths, r.ReducedPathsTotal))
	}
	if len(levels) > 0 {
		parts = append(parts, listing("Execution policy lowered", levels, len(levels)))
	}
	return parts
}

// BootstrapNotice renders the comment that tells the owner how to start or skip the bootstrap.
func BootstrapNotice() string {
	return paragraphs(
		"SuiteWard has no canonical contract yet. This pull request is not assessed against a contract.",
		"To start the first contract, the owner comments `/suiteward propose` on the pull request that should carry the first contract. "+
			"SuiteWard then proposes the protected files of the default branch when it has a `.suiteward.yml`, otherwise those of that pull request, for the owner's approval.",
		"To see a suggested `.suiteward.yml` for this pull request first, the owner comments `/suiteward suggest`. It grants nothing and needs no code.",
		"To let this pull request merge before the first contract, the owner comments `/suiteward skip <code>` with a current code from the authenticator app. "+
			"Nothing is assessed then.",
	)
}

// BootstrapCancelled renders the comment that says a protected push cancelled a bootstrap proposal. Callers pass
// stored ids, so one outside 1..999999999 panics.
func BootstrapCancelled(proposal, revision int) string {
	ref := Reference(proposal, revision)
	return paragraphs(
		"The bootstrap proposal "+ref+" was cancelled because the protected files changed after SuiteWard proposed it. SuiteWard does not propose again by itself.",
		"To propose the current files, comment `/suiteward propose` again.",
	)
}

// commitNote ends every sentence that sends the owner to GitHub's editor.
const commitNote = "SuiteWard never commits it for you: the commit is yours."

// editorStep is the paragraph that sends the owner to GitHub's editor: lead says what to change and where, link is the Markdown link.
func editorStep(lead, link string) string {
	return "\n\n" + lead + ", " + link + ". " + commitNote
}

// pasteStep is the sentence that sends the owner to the editor of .suiteward.yml on the pull request's branch to do action (see pasteAction).
func pasteStep(repo, branch, action string) string {
	return "To change `.suiteward.yml` on this pull request's branch, " + editorLinkText(repo, branch) + " and " + action + ". " + commitNote
}

// pasteAction is what the owner does in the editor: paste a declaration that the comment shows, add the patterns that it lists, or
// either, when it shows a declaration for one choice and patterns for the other.
func pasteAction(declaration, patterns bool) string {
	switch {
	case declaration && patterns:
		return "paste a declaration or add the patterns to its lists"
	case patterns:
		return "add the patterns to its lists"
	}
	return "paste the declaration"
}

// refusalStep is the paragraph that links to GitHub's editor for .suiteward.yml on the branch of the file a refusal asks to fix, led by lead; none
// for an acknowledgment without the link.
func refusalStep(a Ack, lead string) string {
	if a.Branch == "" {
		return ""
	}
	where := "this pull request's branch"
	if a.Base == BaseDefaultBranch {
		where = "the default branch"
	}
	return editorStep(lead+" `.suiteward.yml` on "+where, editorLinkText(a.Repo, a.Branch))
}

// refused renders a refusal: the command, why it was not processed and what to do next.
func refused(command, reason, next string) string {
	return paragraphs("`" + command + "` was not processed: " + reason + ". Nothing changed.\n" + next)
}

// refusalText is the account of one refusal: why the command was not processed, what to do next, and, for a refusal that asks to fix
// .suiteward.yml, the lead of the editor link that follows (see refusalStep).
type refusalText struct{ reason, next, edit string }

// refusalSet is the refusals of one outcome, with the command they answer.
type refusalSet struct {
	command string
	reasons map[Refusal]refusalText
}

const (
	forkReason    = "this pull request comes from a fork, and pull requests from forks are not supported yet"
	notOpenReason = "this pull request is no longer open"
)

// refusals is the one table of the refusals that an outcome may carry: ValidateAck reads which fit, and Acknowledge their text.
var refusals = map[Outcome]refusalSet{
	ProposeRefused: {"/suiteward propose", map[Refusal]refusalText{
		CanonicalPresent: {"this repository already has a canonical contract, so there is no first contract to propose",
			"Changes to the protected files are proposed, for your approval, on the pull request that makes them.", ""},
		Fork:    {forkReason, "Open the pull request from a branch of this repository, then comment `/suiteward propose` there.", ""},
		NotOpen: {notOpenReason, "Comment `/suiteward propose` on an open pull request into the default branch.", ""},
		BaseNotTarget: {"this pull request does not target the default branch, so its merge could never become the first contract",
			"Open the pull request into the default branch, then comment `/suiteward propose` there.", ""},
		EmptyInventory: {"no file matches the include rules of `.suiteward.yml`, so there is nothing to protect",
			"Fix the `include` rules so they match the files you want protected, then comment `/suiteward propose` again.", ""},
		InvalidSource: {"SuiteWard could not read the files to protect as a contract: the `.suiteward.yml` declaration is invalid, a protected path is a link, or the repository tree is too large",
			"Fix the declaration or the files, then comment `/suiteward propose` again.", "If the declaration is the problem, to change"},
	}},
	SkipRefused: {"/suiteward skip", map[Refusal]refusalText{
		CanonicalPresent: {"this repository already has a canonical contract, so there is nothing to skip", "Pull requests are assessed against that contract.", ""},
		Fork:             {forkReason, "Open the pull request from a branch of this repository.", ""},
		Carrier: {"this pull request carries the first contract proposal",
			"Review the bootstrap summary on this pull request, or comment `/suiteward propose` on another pull request to move the first contract there.", ""},
	}},
	SuggestRefused: {"/suiteward suggest", map[Refusal]refusalText{
		Fork:    {forkReason, "Open the pull request from a branch of this repository, then comment `/suiteward suggest` there.", ""},
		NotOpen: {notOpenReason, "Comment `/suiteward suggest` on an open pull request.", ""},
		InvalidSource: {"SuiteWard could not read the files of this pull request or the current `.suiteward.yml` well enough to suggest a declaration",
			"Fix the files or the declaration, then comment `/suiteward suggest` again.", ""},
	}},
	ModeRefused: {"/suiteward mode", map[Refusal]refusalText{
		Fork:    {forkReason, "Open the pull request from a branch of this repository, then comment `/suiteward mode <fix|feature|refactor>` there.", ""},
		NotOpen: {notOpenReason, "Comment `/suiteward mode <fix|feature|refactor>` on an open pull request.", ""},
	}},
	// An unblock that is refused has consumed its code, as a receipt refusal has.
	UnblockRefused: {"/suiteward unblock", map[Refusal]refusalText{
		Fork:    {forkReason, "Open the pull request from a branch of this repository. " + codeUsedAgain, ""},
		NotOpen: {notOpenReason, "Comment `/suiteward unblock <code>` on an open pull request. " + codeUsedAgain, ""},
		NotUnavailable: {"no run of this head is unavailable, so there is nothing to unblock",
			"An unblock lifts only a run that is unavailable on the current head: a pending, failing or complete run is never lifted. " + codeUsedAgain, ""},
	}},
}

// refusal renders the refusal of a propose, a skip, a suggest, a mode or an unblock. A suggest whose file is invalid names the file that failed, not quoted.
func refusal(a Ack) string {
	set := refusals[a.Outcome]
	text, known := set.reasons[a.Refusal]
	if !known {
		panic(fmt.Sprintf("commandtext: unknown refusal %d of outcome %d", a.Refusal, a.Outcome))
	}
	if a.Outcome == SuggestRefused && a.Refusal == InvalidSource && a.Base != 0 {
		_, file := baseNames(a.Base)
		text = refusalText{file + " is not a valid declaration", "Fix the declaration, then comment `/suiteward suggest` again.", "To change"}
	}
	return refused(set.command, text.reason, text.next+refusalStep(a, text.edit))
}

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,38}/[A-Za-z0-9._-]{1,100}$`)

// maxPattern bounds a suggested pattern in bytes. With 20 of them, include and runner together in any split, and the
// longest branch, the editor link stays near 7,350 bytes (7,342 measured, M1.9), below what browsers and GitHub accept.
const maxPattern = 100

// DeclarationSuggestion is what the refusal of a propose for a missing declaration states: Repo (owner/name), Branch
// (the pull request's head ref), Patterns (include patterns, possibly none), Runner (runner patterns) and Source, the id
// of the command comment, which the body links. Patterns and Runner together hold at most 20, which is the limit that
// Cut says dropped some; Mixed (at most 20 files) is shown in the text only, never in the declaration or the link, and so
// is Submodules (at most 20), the submodules whose contents the patterns do not protect. It is not an Ack because it holds
// a slice, and an Ack stays comparable.
type DeclarationSuggestion struct {
	Source     int64
	Repo       string
	Branch     string
	Patterns   []string
	Runner     []string
	Mixed      []string
	Submodules []string
	Cut        bool
	// Suggest renders the reply of /suiteward suggest in its own words, not the refusal of a propose.
	Suggest bool
}

// SuggestDeclaration renders the refusal of a propose when neither the default branch nor the pull request holds
// a .suiteward.yml: a suggested declaration and a link that opens GitHub's editor with it. The patterns and the
// branch come from the repository, so a pattern that is long or would need escaping in YAML is left out (cutting
// one would change the rule), the branch is path-escaped and the declaration is URL-encoded. The repository name
// and the branch are validated, and one that cannot be a GitHub repository or a git ref panics.
func SuggestDeclaration(a DeclarationSuggestion) string {
	must(ValidateSuggestion(a))
	usable, runner := suggestableOnly(a.Patterns), suggestableOnly(a.Runner)
	left := len(a.Patterns) + len(a.Runner) - len(usable) - len(runner)
	intro := "SuiteWard suggests this declaration, built from the test-like paths of this repository. Review it and change it as needed:"
	if a.Suggest {
		intro = "SuiteWard suggests this declaration for this pull request, built from the test-like paths of this repository. Review it and change it as needed:"
	}
	if len(usable)+len(runner) == 0 {
		usable = []string{"path/to/tests/**"}
		intro = "SuiteWard found no test-like path to suggest, so this is a minimal file. Replace the placeholder with patterns that match your tests:"
	}
	declaration := "version: 1\n" + yamlList("include", usable) + yamlList("runner", runner)
	first := "`/suiteward propose` was not processed: SuiteWard found no `.suiteward.yml` on the default branch or in the head of this pull request, " +
		"so it cannot tell which files to protect and proposed nothing. Nothing changed."
	last := "After the file is on this branch, comment `/suiteward propose` again."
	if a.Suggest { // nothing was refused, and no declaration exists
		first = "`/suiteward suggest`: SuiteWard found no `.suiteward.yml` on the default branch or in the head of this pull request, so no files are protected yet. Nothing changed."
		last = "After the file is on this branch, comment `/suiteward propose` to start the first contract."
	}
	parts := []string{first, intro + "\n\n" + fenced(declaration)}
	if left > 0 {
		parts = append(parts, fmt.Sprintf("Left out of this suggestion because they are too long or contain unusual characters: %d.", left))
	}
	if a.Cut {
		parts = append(parts, cutWarning)
	}
	if len(a.Mixed) > 0 {
		parts = append(parts, mixedWarning(a.Mixed))
	}
	if len(a.Submodules) > 0 {
		parts = append(parts, submodulesWarning(a.Submodules))
	}
	// QueryEscape writes a space as +, which the editor may keep; %20 is read the same everywhere.
	link := newLink(a.Repo, a.Branch) + "&value=" +
		strings.ReplaceAll(url.QueryEscape(declaration), "+", "%20")
	return replyTo(paragraphs(append(parts,
		"To add it on this pull request's branch, [open GitHub's editor with this file filled in]("+link+"). "+commitNote,
		last,
	)...), a.Source)
}

// suggestableOnly keeps the patterns that can be suggested, in order.
func suggestableOnly(patterns []string) []string {
	var usable []string
	for _, p := range patterns {
		if suggestable(p) {
			usable = append(usable, p)
		}
	}
	return usable
}

// yamlList is a block-style list of double-quoted patterns under key, and nothing for no patterns.
func yamlList(key string, patterns []string) string {
	if len(patterns) == 0 {
		return ""
	}
	list := key + ":\n"
	for _, p := range patterns {
		list += "  - \"" + p + "\"\n"
	}
	return list
}

// suggestable reports whether p can be written inside a double-quoted YAML scalar and a URL as it is.
func suggestable(p string) bool {
	return len(p) <= maxPattern && utf8.ValidString(p) && !strings.ContainsFunc(p, func(r rune) bool { return hidden(r) || r == '"' || r == '\\' })
}

// ValidateSuggestion reports why SuggestDeclaration would panic on a: the same checks, as an error that quotes the
// offending value. A caller that stores the suggestion calls it first, so a payload that cannot be rendered is refused
// where it is built and not retried for ever where it is published.
func ValidateSuggestion(a DeclarationSuggestion) error {
	if a.Source <= 0 {
		return fmt.Errorf("commandtext: source comment %d does not fit a suggestion", a.Source)
	}
	if err := ValidateEditTarget(a.Repo, a.Branch); err != nil {
		return err
	}
	if err := errors.Join(checkListed("suggested patterns", len(a.Patterns)+len(a.Runner)), checkListed("mixed files", len(a.Mixed)), checkListed("submodules", len(a.Submodules))); err != nil {
		return err
	}
	return nil
}

// checkListed reports a list of what that holds more than maxListed items.
func checkListed(what string, n int) error {
	if n > maxListed {
		return fmt.Errorf("commandtext: %d %s, at most %d", n, what, maxListed)
	}
	return nil
}

// ValidateEditTarget reports why a repository and a branch cannot make the link to GitHub's editor, as an error that quotes
// them: a repository that is not owner/name or a branch that is not a git ref name. The caller that stores the pair (see Ack)
// says only that it does not fit.
func ValidateEditTarget(repo, branch string) error {
	if _, name, _ := strings.Cut(repo, "/"); !repoPattern.MatchString(repo) || name == "." || name == ".." {
		return fmt.Errorf("commandtext: %q is not a GitHub repository", repo)
	}
	if !gitBranch(branch) {
		return fmt.Errorf("commandtext: %q is not a git branch name", branch)
	}
	return nil
}

// gitBranch reports whether b can be a git ref name (git check-ref-format, without its rarer rules): the branch
// is stored from GitHub, so anything else is an impossible state.
func gitBranch(b string) bool {
	bad := b == "" || len(b) > 255 || !utf8.ValidString(b) || strings.Contains(b, "..") ||
		strings.ContainsFunc(b, func(r rune) bool { return r <= ' ' || r == 0x7f || strings.ContainsRune("~^:?*[\\", r) })
	for segment := range strings.SplitSeq(b, "/") {
		bad = bad || segment == "" || segment[0] == '.'
	}
	return !bad
}
