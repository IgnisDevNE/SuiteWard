package commandtext

import (
	"fmt"
	"net/url"
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
	// The explicit bootstrap (M1.7b rework): ProposeRefused and SkipRefused carry a Refusal, AlreadyProposed names the
	// existing proposal, ProposeFailed says a propose was dropped after its attempts.
	ProposeRefused
	AlreadyProposed
	ProposeFailed
	Skipped
	SkipRefused
)

// Refusal is the reason of a ProposeRefused or SkipRefused outcome.
type Refusal uint8

const (
	CanonicalPresent Refusal = iota + 1
	Fork
	EmptyInventory
	Carrier
	InvalidSource
	NotOpen
	BaseNotTarget
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
	// Refusal is set exactly for ProposeRefused and SkipRefused.
	Refusal Refusal
}

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
// taken as the length of the list.
type Summary struct {
	Proposal                   int
	Revision                   int
	Source                     Source
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

func acknowledgment(a Ack) string {
	ref, who := referenceOf(a), "@"+a.Owner
	// " for P4-R2", or nothing for a command that has no reference (a skip).
	of := ""
	if ref != "" {
		of = " for " + ref
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
			status = "The approval requirement for " + ref + " is met."
		}
		return paragraphs(
			"Approval recorded for "+ref+" by "+who+".\n"+status,
			"To withdraw this approval, comment:",
			copyable("/suiteward revoke "+ref+" <code>"),
		)
	case Revoked:
		status := "The approval requirement for " + ref + " is not met."
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
			"Comment again in the form `/suiteward approve <ref> <code>`, `/suiteward revoke <ref> <code>`, `/suiteward propose` or `/suiteward skip <code>`, " +
			"with a current code from your authenticator app where the command takes one.")
	case InvalidCode:
		return paragraphs("The confirmation code" + of + " was not accepted; nothing changed.\n" +
			"Comment the command again with a current code from your authenticator app.")
	case ReplayedCode:
		return paragraphs("The confirmation code" + of + " was already used; each code works only once, so nothing changed.\n" +
			"Wait for your authenticator app to show a new code, then comment the command again.")
	case NoSuite:
		subject := "Your command"
		if ref != "" {
			subject = ref
		}
		return paragraphs(subject + " was not processed: this repository's SuiteWard bootstrap is still pending, so nothing changed.\n" +
			"Wait for SuiteWard to finish setting up this repository, then comment `/suiteward propose` on the pull request that should carry the first contract.")
	case ProposeRefused:
		return proposeRefusal(a.Refusal)
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
	case SkipRefused:
		return skipRefusal(a.Refusal)
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
	referenceOf(a) // called for its panic on a reference that does not fit the outcome
	consent := a.Outcome == Approved || a.Outcome == Revoked || a.Outcome == NoActiveApproval || a.Outcome == Rejected
	switch {
	case !consent && a.Eligible:
		panic(fmt.Sprintf("commandtext: outcome %d does not evaluate eligibility", a.Outcome))
	case (a.Outcome == Rejected) != (a.Rejection != 0):
		panic(fmt.Sprintf("commandtext: rejection %d does not fit outcome %d", a.Rejection, a.Outcome))
	case (a.Outcome == MalformedCommand) != (a.Malformed != 0):
		panic(fmt.Sprintf("commandtext: malformed reason %d does not fit outcome %d", a.Malformed, a.Outcome))
	case (a.Outcome == BootstrapPromoted) != (a.Version != 0):
		panic(fmt.Sprintf("commandtext: version %d does not fit outcome %d", a.Version, a.Outcome))
	case !refusalFits(a.Outcome, a.Refusal):
		panic(fmt.Sprintf("commandtext: refusal %d does not fit outcome %d", a.Refusal, a.Outcome))
	case a.Outcome == BootstrapPromoted:
		checkID("version", a.Version)
	}
}

// referenceOf returns the reference an outcome names and "" for one that names none. It panics when the reference
// does not fit: a skip has none, so only the code outcomes and NoSuite may come without one.
func referenceOf(a Ack) string {
	switch a.Outcome {
	case MalformedCommand, EditedFirst, ProposeRefused, ProposeFailed, Skipped, SkipRefused:
		if a.Proposal != 0 || a.Revision != 0 {
			panic(fmt.Sprintf("commandtext: outcome %d carries a reference", a.Outcome))
		}
		return ""
	case InvalidCode, ReplayedCode, NoSuite:
		if a.Proposal == 0 && a.Revision == 0 {
			return ""
		}
	}
	return Reference(a.Proposal, a.Revision)
}

// refusalFits reports whether refusal is one of the reasons of outcome, or none for an outcome that has none.
func refusalFits(outcome Outcome, refusal Refusal) bool {
	switch outcome {
	case ProposeRefused:
		return refusal == CanonicalPresent || refusal == Fork || refusal == NotOpen || refusal == BaseNotTarget || refusal == EmptyInventory || refusal == InvalidSource
	case SkipRefused:
		return refusal == CanonicalPresent || refusal == Fork || refusal == Carrier
	}
	return refusal == 0
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
		return "the action must be `approve`, `revoke`, `propose` or `skip`"
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
	tier := tierName(s.Tier)
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
		origin + "\n" + count + "\n- Installation tier: `" + tier + "`",
	}
	parts = append(parts, listing("Inventory", s.Paths, len(s.Paths)), listing("Scope include rules", s.Include, s.IncludeTotal), listing("Scope exclude rules", s.Exclude, s.ExcludeTotal))
	if s.Tier == CoLocated {
		parts = append(parts, coLocatedNotice)
	}
	parts = append(parts, review, copyable("/suiteward approve "+ref+" <code>"), after)
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
	return title + ":\n\n" + fenced(strings.Join(shown, "\n")+"\n")
}

// listingAndMore is listing for paths: the entries inside a fence, then "and N more" for the ones the total counts
// beyond them. The items are at most maxListed; a total below them is their number.
func listingAndMore(title string, items []string, total int) string {
	body := listing(title, items, len(items))
	if unlisted := total - len(items); unlisted > 0 {
		body += fmt.Sprintf("\n\nand %d more", unlisted)
	}
	return body
}

// fenced shows block, which ends with a newline, in a code fence that a run of backticks inside it cannot close.
func fenced(block string) string {
	fence := strings.Repeat("`", max(3, longestBacktickRun(block)+1))
	return fence + "\n" + block + fence
}

// mixedWarning says that files may hold test settings among other content and are not suggested. They are listed only
// in text: a mixed file never goes into a declaration.
func mixedWarning(mixed []string) string {
	shown := make([]string, len(mixed))
	for i, f := range mixed {
		shown[i] = display(f)
	}
	return "These files may hold test settings among other content, so SuiteWard does not suggest them: protecting one would put every change to it, " +
		"even a dependency update, behind an approval. Decide for yourself:\n\n" + fenced(strings.Join(shown, "\n")+"\n")
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

// Revision is what the comment of a new change-proposal revision states. Previous is the reference this revision
// supersedes (an earlier revision of the same proposal), empty for the first. Each list shows at most 20 entries
// of the total; a total below the list is taken as its length. Reduced says that the revision reduces the
// protected scope; ReducedRules and ReducedPaths (at most 20 each, of ReducedPathsTotal paths) detail what it removes and
// are set only with Reduced.
type Revision struct {
	Proposal, Revision                      int
	Previous                                string
	Canonical                               int
	Added, Modified, Removed                []string
	AddedTotal, ModifiedTotal, RemovedTotal int
	Reduced                                 bool
	ReducedRules, ReducedPaths              []string
	ReducedPathsTotal                       int
	Tier                                    Tier
}

// RevisionComment renders the comment of a new change-proposal revision: the protected files added, modified
// and removed against the canonical, the approve command for this exact revision and the revision it supersedes.
// It never says that tests ran. The paths come from the repository, so they are shown only inside code, bounded
// and with control characters escaped, like the bootstrap summary.
func RevisionComment(r Revision) string {
	ref := Reference(r.Proposal, r.Revision)
	checkID("canonical version", r.Canonical)
	tier := tierName(r.Tier)
	if r.AddedTotal < 0 || r.ModifiedTotal < 0 || r.RemovedTotal < 0 {
		panic(fmt.Sprintf("commandtext: negative totals %d, %d and %d", r.AddedTotal, r.ModifiedTotal, r.RemovedTotal))
	}
	switch {
	case r.ReducedPathsTotal < 0 || len(r.ReducedRules) > maxListed || len(r.ReducedPaths) > maxListed:
		panic(fmt.Sprintf("commandtext: %d reduced rules, %d reduced paths and a total of %d", len(r.ReducedRules), len(r.ReducedPaths), r.ReducedPathsTotal))
	case !r.Reduced && (len(r.ReducedRules) > 0 || len(r.ReducedPaths) > 0 || r.ReducedPathsTotal != 0):
		panic("commandtext: reduction detail on a revision that does not reduce the scope")
	}
	var supersedes string
	if r.Previous != "" {
		proposal, revision, ok := parseReference(r.Previous)
		if !ok || proposal != r.Proposal || revision >= r.Revision {
			panic(fmt.Sprintf("commandtext: previous %q is not an earlier revision of proposal %d", r.Previous, r.Proposal))
		}
		supersedes = "This supersedes `" + r.Previous + "`."
	}
	parts := []string{
		"**SuiteWard contract change " + ref + "**",
		fmt.Sprintf("This pull request changes the protected files against the canonical contract V%d. SuiteWard proposes the change as %s for the owner's approval.", r.Canonical, ref),
		"- Base canonical version: V" + strconv.Itoa(r.Canonical) + "\n- Installation tier: `" + tier + "`",
		listing("Protected files added", r.Added, r.AddedTotal), listing("Protected files modified", r.Modified, r.ModifiedTotal), listing("Protected files removed", r.Removed, r.RemovedTotal),
	}
	if r.Reduced {
		parts = append(parts, "**This revision reduces the protected scope.** Review it carefully before approving.")
		if len(r.ReducedRules) > 0 {
			parts = append(parts, listing("Rules removed from the scope", r.ReducedRules, len(r.ReducedRules)))
		}
		if len(r.ReducedPaths) > 0 {
			parts = append(parts, listingAndMore("Paths that the scope no longer covers", r.ReducedPaths, r.ReducedPathsTotal))
		}
	}
	if r.Tier == CoLocated {
		parts = append(parts, coLocatedNotice)
	}
	parts = append(parts,
		"This comment is a record, not an approval. SuiteWard compares the protected files only; it does not run tests. To approve this exact revision, comment:",
		copyable("/suiteward approve "+ref+" <code>"),
	)
	if supersedes != "" {
		parts = append(parts, supersedes)
	}
	return paragraphs(parts...)
}

// BootstrapNotice renders the comment that tells the owner how to start or skip the bootstrap.
func BootstrapNotice() string {
	return paragraphs(
		"SuiteWard has no canonical contract yet. This pull request is not assessed against a contract.",
		"To start the first contract, the owner comments `/suiteward propose` on the pull request that should carry the first contract. "+
			"SuiteWard then proposes the protected files of the default branch when it has a `.suiteward.yml`, otherwise those of that pull request, for the owner's approval.",
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

// refused renders a refusal: the command, why it was not processed and what to do next.
func refused(command, reason, next string) string {
	return paragraphs("`" + command + "` was not processed: " + reason + ". Nothing changed.\n" + next)
}

func skipRefusal(r Refusal) string {
	const skip = "/suiteward skip"
	switch r {
	case CanonicalPresent:
		return refused(skip, "this repository already has a canonical contract, so there is nothing to skip", "Pull requests are assessed against that contract.")
	case Fork:
		return refused(skip, "this pull request comes from a fork, and pull requests from forks are not supported yet",
			"Open the pull request from a branch of this repository.")
	case Carrier:
		return refused(skip, "this pull request carries the first contract proposal",
			"Review the bootstrap summary on this pull request, or comment `/suiteward propose` on another pull request to move the first contract there.")
	}
	panic(fmt.Sprintf("commandtext: unknown skip refusal %d", r))
}

func proposeRefusal(r Refusal) string {
	const propose = "/suiteward propose"
	switch r {
	case CanonicalPresent:
		return refused(propose, "this repository already has a canonical contract, so there is no first contract to propose",
			"Changes to the protected files are proposed, for your approval, on the pull request that makes them.")
	case Fork:
		return refused(propose, "this pull request comes from a fork, and pull requests from forks are not supported yet",
			"Open the pull request from a branch of this repository, then comment `/suiteward propose` there.")
	case NotOpen:
		return refused(propose, "this pull request is no longer open",
			"Comment `/suiteward propose` on an open pull request into the default branch.")
	case BaseNotTarget:
		return refused(propose, "this pull request does not target the default branch, so its merge could never become the first contract",
			"Open the pull request into the default branch, then comment `/suiteward propose` there.")
	case EmptyInventory:
		return refused(propose, "no file matches the include rules of `.suiteward.yml`, so there is nothing to protect",
			"Fix the `include` rules so they match the files you want protected, then comment `/suiteward propose` again.")
	case InvalidSource:
		return refused(propose, "SuiteWard could not read the files to protect as a contract: the `.suiteward.yml` declaration is invalid, a protected path is a link, or the repository tree is too large",
			"Fix the declaration or the files, then comment `/suiteward propose` again.")
	}
	panic(fmt.Sprintf("commandtext: unknown propose refusal %d", r))
}

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,38}/[A-Za-z0-9._-]{1,100}$`)

// maxPattern bounds a suggested pattern in bytes. With 20 of them, include and runner together in any split, and the
// longest branch, the editor link stays near 7,350 bytes (7,342 measured, M1.9), below what browsers and GitHub accept.
const maxPattern = 100

// DeclarationSuggestion is what the refusal of a propose for a missing declaration states: Repo (owner/name), Branch
// (the pull request's head ref), Patterns (include patterns, possibly none), Runner (runner patterns) and Source, the id
// of the command comment, which the body links. Patterns and Runner together hold at most 20, which is the limit that
// Cut says dropped some; Mixed (at most 20 files) is shown in the text only, never in the declaration or the link. It is
// not an Ack because it holds a slice, and an Ack stays comparable.
type DeclarationSuggestion struct {
	Source   int64
	Repo     string
	Branch   string
	Patterns []string
	Runner   []string
	Mixed    []string
	Cut      bool
}

// SuggestDeclaration renders the refusal of a propose when neither the default branch nor the pull request holds
// a .suiteward.yml: a suggested declaration and a link that opens GitHub's editor with it. The patterns and the
// branch come from the repository, so a pattern that is long or would need escaping in YAML is left out (cutting
// one would change the rule), the branch is path-escaped and the declaration is URL-encoded. The repository name
// and the branch are validated, and one that cannot be a GitHub repository or a git ref panics.
func SuggestDeclaration(a DeclarationSuggestion) string {
	if err := ValidateSuggestion(a); err != nil {
		panic(err.Error())
	}
	usable, runner := suggestableOnly(a.Patterns), suggestableOnly(a.Runner)
	left := len(a.Patterns) + len(a.Runner) - len(usable) - len(runner)
	intro := "SuiteWard suggests this declaration, built from the test-like paths of this repository. Review it and change it as needed:"
	if len(usable)+len(runner) == 0 {
		usable = []string{"path/to/tests/**"}
		intro = "SuiteWard found no test-like path to suggest, so this is a minimal file. Replace the placeholder with patterns that match your tests:"
	}
	declaration := "version: 1\n" + yamlList("include", usable) + yamlList("runner", runner)
	parts := []string{
		"`/suiteward propose` was not processed: SuiteWard found no `.suiteward.yml` on the default branch or in the head of this pull request, " +
			"so it cannot tell which files to protect and proposed nothing. Nothing changed.",
		intro + "\n\n" + fenced(declaration),
	}
	if left > 0 {
		parts = append(parts, fmt.Sprintf("Left out of this suggestion because they are too long or contain unusual characters: %d.", left))
	}
	if a.Cut {
		parts = append(parts, "This list was cut at the limit of 20 patterns, so test-like paths may be missing from it. Please review it and add what is missing.")
	}
	if len(a.Mixed) > 0 {
		parts = append(parts, mixedWarning(a.Mixed))
	}
	// QueryEscape writes a space as +, which the editor may keep; %20 is read the same everywhere.
	link := "https://github.com/" + a.Repo + "/new/" + pathEscape(a.Branch) + "?filename=.suiteward.yml&value=" +
		strings.ReplaceAll(url.QueryEscape(declaration), "+", "%20")
	return replyTo(paragraphs(append(parts,
		"To add it on this pull request's branch, [open GitHub's editor with this file filled in]("+link+"). SuiteWard never commits it for you: the commit is yours.",
		"After the file is on this branch, comment `/suiteward propose` again.",
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
	return len(p) <= maxPattern && utf8.ValidString(p) && !strings.ContainsFunc(p, func(r rune) bool { return !strconv.IsGraphic(r) || r == '"' || r == '\\' })
}

// ValidateSuggestion reports why SuggestDeclaration would panic on a: the same checks, as an error that quotes the
// offending value. A caller that stores the suggestion calls it first, so a payload that cannot be rendered is refused
// where it is built and not retried for ever where it is published.
func ValidateSuggestion(a DeclarationSuggestion) error {
	if a.Source <= 0 {
		return fmt.Errorf("commandtext: source comment %d does not fit a suggestion", a.Source)
	}
	if err := checkRepoAndBranch(a.Repo, a.Branch); err != nil {
		return err
	}
	if len(a.Patterns)+len(a.Runner) > maxListed {
		return fmt.Errorf("commandtext: %d suggested patterns, at most %d", len(a.Patterns)+len(a.Runner), maxListed)
	}
	if len(a.Mixed) > maxListed {
		return fmt.Errorf("commandtext: %d mixed files, at most %d", len(a.Mixed), maxListed)
	}
	return nil
}

// checkRepoAndBranch reports a repository that is not owner/name or a branch that is not a git ref name.
func checkRepoAndBranch(repo, branch string) error {
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
