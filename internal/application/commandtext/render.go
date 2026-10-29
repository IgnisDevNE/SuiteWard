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
	// RunnerResponding is the refusal of an unblock when a run of the head waits for a test runner that responds: it is neither unavailable nor stuck.
	RunnerResponding
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
	// Ended are the plans whose open job the unblock ended (the test runner was not responding): a subset of Plans, and none for every other outcome.
	Ended Plans
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
	// Only whether the approval is there is stated: other requirements may still block the merge.
	requirement := ref + " does not have the approval it needs."
	if a.Eligible {
		requirement = ref + " has the approval it needs."
	}
	switch a.Outcome {
	case Approved:
		status := ref + " does not have the approval it needs yet."
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
			status = ref + " still has the approval it needs, from another valid approval."
		}
		return paragraphs(
			"Revocation recorded for "+ref+" by "+who+".\nYour approval no longer counts. "+status,
			"To approve it again, comment:",
			codeCommand("approve", ref),
		)
	case RevokedAfterPromotion:
		// Says nothing about an approval being withdrawn: the owner may have had none active.
		return paragraphs(
			"Your revoke command for "+ref+" ("+who+") was recorded, but it changes nothing.\n"+
				ref+" is already merged and is part of the approved version, so revoking cannot undo it.",
			reverseText,
		)
	case ApprovedAfterPromotion:
		return paragraphs(
			"Approval recorded for "+ref+" by "+who+".\n"+
				ref+" is already merged and is part of the approved version, so nothing changes. There is nothing more to do.",
			reverseText,
		)
	case NoActiveApproval:
		return paragraphs(who + " has no active approval of " + ref + ", so there is nothing to withdraw. Nothing changed.\n" + requirement + "\nThere is nothing to do.")
	case Rejected:
		reason, next := rejectionText(a.Rejection, ref, who)
		return paragraphs("Your command for " + ref + " was refused: " + reason + ". Nothing changed.\n" + requirement + "\n" + next)
	}
	return notInGroup(a.Outcome)
}

// wrongCode finishes the first sentence of the acknowledgment of a wrong code: the command had no effect, but the check fails for
// it (D-COMMANDS, "A wrong command has no effect"). code names the code the next command needs.
func wrongCode(first, code string) string {
	return first + " This needs your action: the `SuiteWard / Contract` check may show as failed until you comment the command again with " + code + ", or push a new commit."
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
		return paragraphs(wrongCode("The code"+of+" was not accepted, so the command did nothing.", "a current code from your authenticator app"))
	case ReplayedCode:
		return paragraphs(wrongCode("The code"+of+" was already used, so the command did nothing. Each code works only once.", "a new code") +
			"\nWait for your authenticator app to show a new code first.")
	case NoSuite:
		subject := "Your command"
		if ref != "" {
			subject += " for " + ref
		}
		return paragraphs(subject + " was not processed, because SuiteWard has not finished setting up this repository. Nothing changed.\n" +
			"Wait for SuiteWard to finish setting up this repository, then comment `/suiteward propose` on the pull request that should carry the first contract.")
	case MalformedCommand:
		return paragraphs("Your `/suiteward` command was not understood: " + malformedText(a.Malformed) + ". Nothing changed.\n" +
			"Comment it again in one of these forms: `/suiteward approve <ref> <code>`, `/suiteward revoke <ref> <code>`, `/suiteward propose`, `/suiteward skip <code>`, `/suiteward suggest`, " +
			"`/suiteward mode <fix|feature|refactor>` or `/suiteward unblock <code>`. " +
			"`<ref>` is the reference in SuiteWard's comment about the proposal. Where a command takes a code, use the current six-digit code from your authenticator app.")
	case OtherPullRequest:
		return paragraphs(ref + " belongs to another pull request. Comment your command on that pull request instead. Nothing changed.")
	case EditedFirst:
		return paragraphs("SuiteWard does not treat this comment as a command, because it was edited before SuiteWard first read it. Nothing changed.\n" +
			"Post the command again as a new comment, with a current code. SuiteWard never acts on an edited command.")
	}
	return notInGroup(a.Outcome)
}

// bootstrapAck renders the outcomes of the bootstrap that are neither a refusal nor a consent.
func bootstrapAck(a Ack) string {
	ref, who := mustReference(a), "@"+a.Owner
	switch a.Outcome {
	case AlreadyProposed:
		return paragraphs("The first contract was already proposed as "+ref+". Nothing changed.",
			"Review the summary that SuiteWard posted for "+ref+" on this pull request, and approve it as that summary describes.")
	case ProposeFailed:
		return paragraphs("SuiteWard tried several times and could not process your `/suiteward propose` command, so it proposed nothing. Nothing changed.\n" +
			"Comment `/suiteward propose` again.")
	case Skipped:
		return paragraphs("Skip recorded for this pull request by "+who+".\n"+
			"This pull request can merge before the first contract exists. SuiteWard has not checked it against any contract, because none is approved yet.",
			"To start the first contract instead, comment `/suiteward propose` on the pull request that should carry it.")
	case NothingUncovered:
		subject, _ := baseNames(a.Base)
		return paragraphs("`/suiteward suggest`: " + subject + " already covers every test-like file of this pull request, so there is nothing to suggest. Nothing changed.\n" +
			"This answer is not an approval.")
	case BootstrapPromoted:
		return paragraphs(fmt.Sprintf("Setup is complete: %s, approved by %s, is now V%d, the approved version of the contract.\n", ref, who, a.Version) +
			"SuiteWard checked the approved files at the exact commit that was approved, and recorded the result.\n" +
			"From now on, a change to the protected files needs a new proposal and your approval.")
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
		return who + " is not allowed to do this",
			"Only the owner enrolled in SuiteWard can approve or revoke. Check which GitHub account is enrolled as the owner."
	case UnknownRevision:
		return ref + " is not a revision that SuiteWard knows",
			"Check the reference in the latest SuiteWard comment, then comment the command again with a new code."
	case SupersededRevision:
		return "a newer revision has replaced " + ref, latest
	case ContextMismatch:
		return ref + " no longer matches its pull request or the branch it merges into", latest
	case PolicyMismatch:
		return "the approval rules of this project changed after " + ref + " was proposed", latest
	case ObsoleteCommand:
		return "SuiteWard already processed a later command for " + ref,
			"Comment again with a new code if you still want this change."
	}
	panic(fmt.Sprintf("commandtext: unknown rejection %d", r))
}

func malformedText(r Reason) string {
	switch r {
	case UnknownVerb:
		return "the word after `/suiteward` must be `approve`, `revoke`, `propose`, `skip`, `suggest`, `mode` or `unblock`"
	case MissingReference:
		return "the proposal reference is missing"
	case BadReference:
		return "the reference must have the form `P<n>-R<m>`"
	case MissingCode:
		return "the six-digit code from your authenticator app is missing"
	case BadCode:
		return "the code must be exactly six digits"
	case ExtraText:
		return "there is extra text after the command"
	case MissingMode:
		return "the mode (the kind of change) is missing: it must be `fix`, `feature` or `refactor`"
	case BadMode:
		return "the mode (the kind of change) must be `fix`, `feature` or `refactor`"
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
	intro := "SuiteWard proposes the protected files of this repository as its first contract, " + ref + ". Nothing is approved yet."
	origin := "- Commit: `" + s.Origin + "` on branch " + codeSpan(display(s.Branch)) + " (this exact commit is proposed, even if the branch moves on)"
	review := "Review the inventory and the rules. To approve this exact proposal, comment:"
	after := "Approving " + ref + " does not approve the other changes in this pull request."
	if s.Source == SourcePull {
		intro = "SuiteWard proposes the protected files of this pull request, at its exact commit, as this repository's first contract, " + ref + ". Nothing is approved yet."
		origin = "- Commit: `" + s.Origin + "` (the exact commit of this pull request that is proposed)"
		review = "Review the inventory and the rules. To approve this exact commit, comment:"
		after = "Approving " + ref + " lets this pull request merge. After the merge, SuiteWard makes V1 the approved version, using the exact merge commit. " +
			"A later push that changes the protected files needs a new approval."
	}
	parts := []string{
		"**SuiteWard proposes the first contract: " + ref + "**",
		intro,
		origin + "\n" + count + "\n" + tierLine(s.Tier),
	}
	parts = append(parts, listing("Inventory", s.Paths, len(s.Paths)), listing("Scope include rules", s.Include, s.IncludeTotal),
		listing("Scope runner rules", s.Runner, s.RunnerTotal), listing("Scope exclude rules", s.Exclude, s.ExcludeTotal))
	if s.Tier == CoLocated {
		parts = append(parts, CoLocatedNotice)
	}
	parts = append(parts, review, codeCommand("approve", ref), after)
	return paragraphs(parts...)
}

// CoLocatedNotice is what a body says when the instance runs beside the coding agent (ADR 0027).
const CoLocatedNotice = "SuiteWard runs on the same computer as your coding agent, so its protection is reduced. " +
	"The agent can reach SuiteWard's credentials, its database and the secret behind your six-digit codes."

// tierWords says where an installation of a tier runs, in the words of the installer; a value that is not a tier is an impossible state.
func tierWords(t Tier) string {
	switch t {
	case Isolated:
		return "on a separate server"
	case CoLocated:
		return "on the same computer as your coding agent"
	}
	panic(fmt.Sprintf("commandtext: unknown tier %d", t))
}

// tierLine is the fact that states where the installation runs.
func tierLine(t Tier) string { return "- Installation type: " + tierWords(t) }

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
	return "These files may hold test settings among other content, so SuiteWard does not suggest them. Protecting one would put every change to it, " +
		"even a dependency update, behind an approval. Decide for yourself:\n\n" + fencedLines(mixed)
}

// submodulesWarning says that the submodules a suggested pattern reaches are outside the scope: what is inside them is not
// protected, because SuiteWard never reads a submodule.
func submodulesWarning(submodules []string) string {
	return "A suggested pattern reaches these submodules, but their contents are not protected. SuiteWard never reads a submodule, " +
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

// reverseText is how a merged contract change is reversed (ADR 0018): never by a revocation.
const reverseText = "To reverse it, open a new pull request that restores the earlier files. It needs its own approval and becomes a new version. The history is kept."

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
	// Receipt is the effective receipt level (advisory, before-approval or required) of a revision whose receipt applies, and empty for one with none: the comment then
	// has the fixed receipt line. PullURL, set only with Receipt, is the link to the page of pull request Pull, which names the line's results; without it the
	// line sends the reader to the SuiteWard / Receipt check.
	Receipt string
	PullURL string
	Pull    int
	// LowersPolicy and ReducesScope say what a reduction whose detail was left out (ReducedOmitted) does, which its entries can no longer say: it lowers the execution
	// policy, reduces the protected scope, or both. Neither set is a reduction of the scope. They are set only with ReducedOmitted.
	LowersPolicy, ReducesScope bool
}

// RevisionComment renders the comment of a new change-proposal revision: the protected files added, modified
// and removed against the canonical (as numbers when Changes is set), the approve command for this exact revision and the
// revision it supersedes.
// It never says that tests ran; the fixed receipt line, which only says what the receipt is, is all it says of tests. The paths come from the repository, so they are shown only inside code, bounded
// and with control characters escaped, like the bootstrap summary.
func RevisionComment(r Revision) string {
	must(validateRevision(r))
	ref := Reference(r.Proposal, r.Revision)
	parts := []string{
		"**SuiteWard contract change " + ref + "**",
		fmt.Sprintf("This pull request changes the protected files, compared with V%d, the approved version of the contract. SuiteWard proposes the change as %s for the owner's approval.", r.Canonical, ref),
		"- Approved version: V" + strconv.Itoa(r.Canonical) + "\n" + tierLine(r.Tier),
	}
	parts = append(parts, changesSection(r)...)
	if r.Reduced {
		parts = append(parts, reductionParts(r)...)
	}
	if r.Tier == CoLocated {
		parts = append(parts, CoLocatedNotice)
	}
	parts = append(parts, approvalParts(r, ref)...)
	if r.Previous != "" {
		parts = append(parts, "This supersedes `"+r.Previous+"`.")
	}
	return paragraphs(parts...)
}

// receiptLevels are the effective receipt levels of a revision whose receipt applies.
var receiptLevels = []string{"advisory", "before-approval", "required"}

// receiptLine is the fixed line about the receipt (docs/contracts/runtime.md, "The revision comment"): what the receipt is, where its results are and where its counts are.
func receiptLine(r Revision) string {
	results := "Results: see the `SuiteWard / Receipt` check."
	if r.PullURL != "" {
		results = "Results: [Pull request page](" + r.PullURL + ")."
	}
	return "Receipt: SuiteWard runs the tests this revision adds or changes, on this commit and on the code before the change, and shows what it saw. " +
		"It is evidence, not a verdict. " + results + " The `SuiteWard / Receipt` check carries the counts."
}

// approvalParts are the paragraphs that end the comment before the superseded revision: the receipt line and what the level adds, when the revision has a receipt, and the
// approve command. Under a required receipt the approve needs a mode, and the copyable command has its placeholder.
func approvalParts(r Revision, ref string) []string {
	var parts []string
	lead, command := "This comment is a record, not an approval. To approve this exact revision, comment:", codeCommand("approve", ref)
	if r.Receipt != "" {
		parts = append(parts, receiptLine(r))
		switch r.Receipt {
		case "before-approval":
			parts = append(parts, "Approving waits until the receipt is complete.")
		case "required":
			lead, command = "This comment is a record, not an approval. To approve, name a mode (the kind of change) that the receipt meets, and comment:", modeApproveCommand(ref)
		}
	}
	return append(parts, lead, command)
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
		return !strings.HasPrefix(e, "rule ") && !strings.HasPrefix(e, "exclude ") && !ExecutionEntry(e)
	}):
		return errors.New("commandtext: a reduced rule that is none of a \"rule \", an \"exclude \", an \"execution canonical: \" and an \"execution receipt: \" entry")
	case r.ReducedOmitted && (!r.Reduced || len(r.ReducedRules) > 0 || len(r.ReducedPaths) > 0):
		return errors.New("commandtext: reduction detail omitted on a revision that does not reduce the scope or that lists it")
	}
	if _, err := reference(r.Proposal, r.Revision); err != nil {
		return err
	}
	return errors.Join(validateReceipt(r), validateReport(r), validatePrevious(r))
}

// validateReceipt reports why the receipt line of r, its link and what a reduction whose detail was left out lowers cannot be stated.
func validateReceipt(r Revision) error {
	switch {
	case (r.LowersPolicy || r.ReducesScope) && !r.ReducedOmitted:
		return errors.New("commandtext: what a reduction lowers is stated only for one whose detail was left out: otherwise its entries say it")
	case r.Receipt != "" && !slices.Contains(receiptLevels, r.Receipt):
		return fmt.Errorf("commandtext: receipt level %q is none of %q", r.Receipt, receiptLevels)
	case r.PullURL != "" && (r.Receipt == "" || !ValidPullLink(r.PullURL, r.Pull)):
		return errors.New("commandtext: the link is not the link of the loopback page of this pull request, or the revision has no receipt")
	}
	return nil
}

// validatePrevious reports why the revision that r supersedes is not an earlier revision of its proposal.
func validatePrevious(r Revision) error {
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

// ExecutionEntry reports whether entry is one that scope.Reductions writes for a level of the execution policy that gets weaker: it begins with "execution canonical: " or
// "execution receipt: ". The checks job tells the lowering of the policy from the removal of the scope by it.
func ExecutionEntry(entry string) bool {
	return slices.ContainsFunc(executionPrefixes, func(p string) bool { return strings.HasPrefix(entry, p) })
}

// lowersPolicyOnly reports whether the reduction of r lowers the execution policy and removes nothing from the scope: every entry
// it lists is an execution entry, and it has no path, no total of paths and no detail that was left out.
func (r Revision) lowersPolicyOnly() bool {
	if r.ReducedOmitted { // its entries are gone: the revision says what it lowers
		return r.Reduced && r.LowersPolicy && !r.ReducesScope
	}
	return r.Reduced && len(r.ReducedRules) > 0 && len(r.ReducedPaths) == 0 && r.ReducedPathsTotal == 0 && !r.ReducedOmitted &&
		!slices.ContainsFunc(r.ReducedRules, func(e string) bool { return !ExecutionEntry(e) })
}

// reductionParts are the paragraphs that say a revision reduces the protected scope or lowers the execution policy, with what it removes.
func reductionParts(r Revision) []string {
	var rules, excludes, levels []string // scope.Reductions marks each entry "rule <p>", "exclude <p>" or "execution <level>: <from> -> <to>"
	for _, entry := range r.ReducedRules {
		switch p, isExclude := strings.CutPrefix(entry, "exclude "); {
		case isExclude:
			excludes = append(excludes, p)
		case ExecutionEntry(entry):
			level := strings.TrimPrefix(entry, "execution ")
			levels = append(levels, strings.Replace(level, "canonical:", "approved tests:", 1))
		default:
			rules = append(rules, strings.TrimPrefix(entry, "rule "))
		}
	}
	lead := "This revision reduces the protected scope."
	switch {
	case r.lowersPolicyOnly():
		lead = "This revision lowers the test requirements."
	case len(levels) > 0 || (r.ReducedOmitted && r.LowersPolicy):
		lead = "This revision reduces the protected scope and lowers the test requirements."
	}
	parts := []string{"**" + lead + "** Review it carefully before approving."}
	if r.ReducedOmitted {
		omitted := "The details of this reduction do not fit in this comment, so they are left out. Before approving, compare `.suiteward.yml` with the one in the approved version."
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
		parts = append(parts, listing("Test requirements lowered", levels, len(levels)))
	}
	return parts
}

// BootstrapNotice renders the comment that tells the owner how to start or skip the bootstrap.
func BootstrapNotice() string {
	return paragraphs(
		"SuiteWard has no approved contract yet. This pull request is not checked against a contract.",
		"To start the first contract, the owner comments `/suiteward propose` on the pull request that should carry the first contract. "+
			"SuiteWard then proposes the protected files of the default branch if it has a `.suiteward.yml`, and otherwise those of that pull request. The owner approves the proposal.",
		"To see a suggested `.suiteward.yml` for this pull request first, the owner comments `/suiteward suggest`. It grants nothing and needs no code.",
		"To let this pull request merge before the first contract, the owner comments `/suiteward skip <code>` with a current code from the authenticator app. "+
			"SuiteWard then checks nothing for this pull request.",
	)
}

// BootstrapCancelled renders the comment that says a protected push cancelled a bootstrap proposal. Callers pass
// stored ids, so one outside 1..999999999 panics.
func BootstrapCancelled(proposal, revision int) string {
	ref := Reference(proposal, revision)
	return paragraphs(
		"The proposal "+ref+" for the first contract was cancelled, because the protected files changed after SuiteWard proposed it. SuiteWard does not propose again by itself.",
		"To propose the current files, comment `/suiteward propose` again.",
	)
}

// commitNote ends every sentence that sends the owner to GitHub's editor.
const commitNote = "SuiteWard does not make the commit for you. You make it."

// editorStep is the paragraph that sends the owner to GitHub's editor: lead says what to change and where, link is the Markdown link.
func editorStep(lead, link string) string {
	return "\n\n" + lead + ", " + link + ". " + commitNote
}

// pasteStep is the sentence that sends the owner to the editor of .suiteward.yml on the pull request's branch to do action (see pasteAction).
func pasteStep(repo, branch, action string) string {
	return "To change `.suiteward.yml` on this pull request's branch, " + editorLinkText(repo, branch) + " and " + action + ". " + commitNote
}

// pasteAction is what the owner does in the editor: paste the file that the comment shows, add the patterns that it lists, or
// either, when it shows a file for one choice and patterns for the other.
func pasteAction(declaration, patterns bool) string {
	switch {
	case declaration && patterns:
		return "paste the suggested file or add the patterns to its lists"
	case patterns:
		return "add the patterns to its lists"
	}
	return "paste the suggested file"
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
	forkReason    = "this pull request comes from a fork, and SuiteWard does not support pull requests from forks yet"
	notOpenReason = "this pull request is no longer open"
)

// refusals is the one table of the refusals that an outcome may carry: ValidateAck reads which fit, and Acknowledge their text.
var refusals = map[Outcome]refusalSet{
	ProposeRefused: {"/suiteward propose", map[Refusal]refusalText{
		CanonicalPresent: {"this repository already has an approved contract, so there is no first contract to propose",
			"Changes to the protected files are proposed, for your approval, on the pull request that makes them.", ""},
		Fork:    {forkReason, "Open the pull request from a branch of this repository, then comment `/suiteward propose` there.", ""},
		NotOpen: {notOpenReason, "Comment `/suiteward propose` on an open pull request into the default branch.", ""},
		BaseNotTarget: {"this pull request does not merge into the default branch, so it could never become the first contract",
			"Open the pull request into the default branch, then comment `/suiteward propose` there.", ""},
		EmptyInventory: {"no file matches the `include` rules of `.suiteward.yml`, so there is nothing to protect",
			"Fix the `include` rules so they match the files you want protected, then comment `/suiteward propose` again.", ""},
		InvalidSource: {"SuiteWard could not read the files to protect (the `.suiteward.yml` file is invalid, a protected path is a link, or the repository is too large)",
			"Fix the `.suiteward.yml` file or the files, then comment `/suiteward propose` again.", "If the file is the problem, to change"},
	}},
	SkipRefused: {"/suiteward skip", map[Refusal]refusalText{
		CanonicalPresent: {"this repository already has an approved contract, so there is nothing to skip", "SuiteWard checks pull requests against that contract.", ""},
		Fork:             {forkReason, "Open the pull request from a branch of this repository.", ""},
		Carrier: {"this pull request carries the proposal of the first contract",
			"Review the summary of the first contract on this pull request, or comment `/suiteward propose` on another pull request to move the first contract there.", ""},
	}},
	SuggestRefused: {"/suiteward suggest", map[Refusal]refusalText{
		Fork:    {forkReason, "Open the pull request from a branch of this repository, then comment `/suiteward suggest` there.", ""},
		NotOpen: {notOpenReason, "Comment `/suiteward suggest` on an open pull request.", ""},
		InvalidSource: {"SuiteWard could not read the files of this pull request or the current `.suiteward.yml` well enough to suggest changes to it",
			"Fix the files or the `.suiteward.yml` file, then comment `/suiteward suggest` again.", ""},
	}},
	ModeRefused: {"/suiteward mode", map[Refusal]refusalText{
		Fork:    {forkReason, "Open the pull request from a branch of this repository, then comment `/suiteward mode <fix|feature|refactor>` there.", ""},
		NotOpen: {notOpenReason, "Comment `/suiteward mode <fix|feature|refactor>` on an open pull request.", ""},
	}},
	// An unblock that is refused has consumed its code, as a receipt refusal has.
	UnblockRefused: {"/suiteward unblock", map[Refusal]refusalText{
		Fork:    {forkReason, "Open the pull request from a branch of this repository. " + codeUsedAgain, ""},
		NotOpen: {notOpenReason, "Comment `/suiteward unblock <code>` on an open pull request. " + codeUsedAgain, ""},
		NotUnavailable: {"none of the tests of this commit failed to run, so there is nothing to unblock",
			"An unblock applies only to tests that could not run, or that wait for a test runner that is not responding. " +
				"It does nothing for tests that have finished, whether they pass or fail, or that wait for GitHub or for a test runner that is responding. " + codeUsedAgain, ""},
		// RunnerResponding is told in the contract's own words, not in the template of refused (see refusal): reason is the whole first paragraph.
		RunnerResponding: {"The test runner is responding, so the tests of this commit will run. Nothing was unblocked.", codeUsedAgain, ""},
	}},
}

// refusal renders the refusal of a propose, a skip, a suggest, a mode or an unblock. A suggest whose file is invalid names the file that failed, not quoted.
func refusal(a Ack) string {
	set := refusals[a.Outcome]
	text, known := set.reasons[a.Refusal]
	if !known {
		panic(fmt.Sprintf("commandtext: unknown refusal %d of outcome %d", a.Refusal, a.Outcome))
	}
	if a.Outcome == UnblockRefused && a.Refusal == RunnerResponding {
		return paragraphs(text.reason, text.next)
	}
	if a.Outcome == SuggestRefused && a.Refusal == InvalidSource && a.Base != 0 {
		_, file := baseNames(a.Base)
		text = refusalText{file + " is not valid", "Fix it, then comment `/suiteward suggest` again.", "To change"}
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
	intro := "SuiteWard suggests this `.suiteward.yml`, built from the test-like files of this repository. Review it and change it as needed:"
	if a.Suggest {
		intro = "SuiteWard suggests this `.suiteward.yml` for this pull request, built from the test-like files of this repository. Review it and change it as needed:"
	}
	if len(usable)+len(runner) == 0 {
		usable = []string{"path/to/tests/**"}
		intro = "SuiteWard found no test-like file to suggest, so this is a minimal file. Replace the placeholder with patterns that match your tests:"
	}
	declaration := "version: 1\n" + yamlList("include", usable) + yamlList("runner", runner)
	first := "`/suiteward propose` was not processed: SuiteWard found no `.suiteward.yml` file (the file that lists the files to protect) on the default branch or in this pull request, " +
		"so it cannot tell which files to protect. It proposed nothing. Nothing changed."
	last := "After the file is on this branch, comment `/suiteward propose` again."
	if a.Suggest { // nothing was refused, and no declaration exists
		first = "`/suiteward suggest`: SuiteWard found no `.suiteward.yml` file (the file that lists the files to protect) on the default branch or in this pull request, so no files are protected yet. Nothing changed."
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
