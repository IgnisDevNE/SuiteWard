package commandtext

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// MaxDeclaration bounds the declaration bytes a notice shows. Fenced against a run of backticks the block can take
// three times its size, which with the other lists keeps the notice below three quarters of GitHub's comment limit.
const MaxDeclaration = 8192

// cutWarning is the line that SuggestDeclaration shows for a list cut at the limit, word for word.
const cutWarning = "This list was cut at the limit of 20 patterns, so test-like paths may be missing from it. Please review it and add what is missing."

// ScopeBase says what the reply of /suiteward suggest compared the head with. The zero value is no base: the notice the
// checks job writes, which is not a reply to a command.
type ScopeBase int

const (
	BasePull          ScopeBase = iota + 1 // the head's own .suiteward.yml, before the first contract
	BaseContract                           // the canonical declaration, after the first contract
	BaseDefaultBranch                      // the default branch's .suiteward.yml, before the first contract, when the head has none
)

// ScopeNotice is what the scope-notice comment states. Repo (owner/name) and Branch (the pull request's head ref) build
// the editor link. Paths holds at most 20 of the PathsTotal test-like paths that the current contract does not protect,
// each already cut by the caller; a total below the list is taken as its length. Declaration is the full declaration to
// commit, Mixed (at most 20) the mixed files found and Submodules (at most 20) the submodules whose contents the patterns
// do not protect. Source is the id of the command comment that asked for the notice, zero when a job writes it unprompted. Declaration
// is empty when the full file cannot be shown (see MaxDeclaration): the comment then lists AddInclude and AddRunner, which it needs, as lines to copy.
type ScopeNotice struct {
	Source      int64
	Repo        string
	Branch      string
	Paths       []string
	PathsTotal  int
	Declaration []byte
	Mixed       []string
	Submodules  []string
	Cut         bool // the limit of 20 patterns cut the new patterns, so the declaration may lack some
	// Base, with a Source, makes the notice the reply of /suiteward suggest: it names the base, lists AddInclude and AddRunner
	// (the patterns the base lacks, at most 20 together) and ends with the next step. Without a base both lists stay empty, unless there is no Declaration. With a base
	// Paths may be empty: a runner name can be missing before any file of that name exists.
	Base       ScopeBase
	AddInclude []string
	AddRunner  []string
	// DefaultBranch, only with BaseDefaultBranch, is the branch whose .suiteward.yml the reply asks to change, for the editor link; none: no link.
	DefaultBranch string
}

// ValidateScopeNotice reports why ScopeNoticeComment would panic on n, as an error: a caller that stores the notice
// calls it first, so a payload that cannot be rendered is refused where it is built.
func ValidateScopeNotice(n ScopeNotice) error {
	if n.Source < 0 {
		return fmt.Errorf("commandtext: source comment %d does not fit a notice", n.Source)
	}
	if err := ValidateEditTarget(n.Repo, n.Branch); err != nil {
		return err
	}
	if err := validateNoticeLists(n); err != nil {
		return err
	}
	switch {
	case len(n.Declaration) == 0 && len(n.AddInclude)+len(n.AddRunner) == 0:
		return errors.New("commandtext: a notice with no declaration lists the patterns to add, and has none")
	case len(n.Declaration) > MaxDeclaration || !verbatim(n.Declaration):
		return fmt.Errorf("commandtext: a declaration of %d bytes is over %d bytes or holds a character that is not graphic besides newline (LF or CRLF) and tab", len(n.Declaration), MaxDeclaration)
	}
	return validateNoticeBase(n)
}

// validateNoticeLists reports a list of a notice that is too long, or a notice with no path that is not a reply with new patterns.
func validateNoticeLists(n ScopeNotice) error {
	if len(n.Paths) > maxListed || (len(n.Paths) == 0 && (n.Base == 0 || len(n.AddInclude)+len(n.AddRunner) == 0)) {
		return fmt.Errorf("commandtext: %d paths in a notice, want 1 to %d (none only in a reply with new patterns)", len(n.Paths), maxListed)
	}
	return errors.Join(checkListed("mixed files", len(n.Mixed)), checkListed("submodules", len(n.Submodules)))
}

// validateNoticeBase reports a base, the patterns it lacks or a default branch that does not fit the notice.
func validateNoticeBase(n ScopeNotice) error {
	switch {
	case n.Base < 0 || n.Base > BaseDefaultBranch:
		return fmt.Errorf("commandtext: base %d is unknown", n.Base)
	case n.Base != 0 && n.Source == 0:
		return errors.New("commandtext: a base states the reply to a command")
	case n.Base == 0 && len(n.Declaration) > 0 && len(n.AddInclude)+len(n.AddRunner) > 0:
		return errors.New("commandtext: new patterns are listed only with a base, or in place of the declaration")
	case n.DefaultBranch != "" && (n.Base != BaseDefaultBranch || !gitBranch(n.DefaultBranch)):
		return errors.New("commandtext: a default branch fits only the reply for the default branch, and must be a git branch name")
	}
	return checkListed("new patterns", len(n.AddInclude)+len(n.AddRunner))
}

// ScopeNoticeComment renders the comment that tells the owner which test-like paths of a pull request the current
// contract does not protect, with the full declaration to commit and the link to GitHub's editor for it. It is a
// suggestion: it changes nothing, grants nothing and never says that tests ran. The paths and the declaration come from
// the repository, so they are shown only inside code fences that backticks cannot close. The paths are shown with their
// control characters escaped. The declaration is shown as it is, and ValidateScopeNotice refuses one that holds a character
// that is not graphic other than a newline (LF or CRLF) and a tab. The branch is path-escaped. An input that does not fit panics (see
// ValidateScopeNotice).
func ScopeNoticeComment(n ScopeNotice) string {
	must(ValidateScopeNotice(n))
	if n.Base != 0 {
		return scopeSuggestReply(n)
	}
	parts := []string{
		"**SuiteWard scope notice**",
		listingAndMore("Test-like files in this pull request's head that the current contract does not protect", n.Paths, n.PathsTotal),
		"These files are not protected by the current contract: SuiteWard assesses only the protected files, so a change to them needs no approval.",
	}
	parts = append(parts, missingLists(n)...)
	parts = append(parts, suggestedDeclaration("the current canonical `.suiteward.yml`", "new", n.Declaration))
	if n.Cut {
		parts = append(parts, cutWarning)
	}
	if len(n.Mixed) > 0 {
		parts = append(parts, mixedWarning(n.Mixed))
	}
	if len(n.Submodules) > 0 {
		parts = append(parts, submodulesWarning(n.Submodules))
	}
	parts = append(parts,
		pasteStep(n.Repo, n.Branch, pasteAction(len(n.Declaration) > 0, len(n.Declaration) == 0)),
		"Committing it creates a protected change, which the owner approves like any other change to the protected files. Nothing is protected until that change is approved and promoted.",
		"This comment is a suggestion, not an approval. It does not change the check, and SuiteWard does not run tests.",
	)
	return replyTo(paragraphs(parts...), n.Source)
}

// baseNames are how a reply names the base the head was compared with, and how it names that base's file.
func baseNames(base ScopeBase) (subject, file string) {
	switch base {
	case BasePull:
		return "this pull request's `.suiteward.yml`", "this pull request's `.suiteward.yml`"
	case BaseDefaultBranch:
		return "the default branch's `.suiteward.yml`", "the default branch's `.suiteward.yml`"
	}
	return "the current contract", "the current canonical `.suiteward.yml`"
}

// missingLists is the paragraphs that list the patterns that the base lacks, each list as lines to copy.
func missingLists(n ScopeNotice) []string {
	var parts []string
	if len(n.AddInclude) > 0 {
		parts = append(parts, "Patterns missing from the `include` list:\n\n"+fencedLines(n.AddInclude))
	}
	if len(n.AddRunner) > 0 {
		parts = append(parts, "Patterns missing from the `runner` list:\n\n"+fencedLines(n.AddRunner),
			"Runner names matter before a file of that name exists: a file with one of these names, added later, can change how the tests run.")
	}
	return parts
}

// suggestedDeclaration is the paragraph that offers declaration, file (named by file) with the patterns (called adjective) added, or, for no
// declaration, offers the patterns alone: the lists above it are all it has to show.
func suggestedDeclaration(file, adjective string, declaration []byte) string {
	if len(declaration) == 0 {
		return "SuiteWard suggests adding the " + adjective + " patterns to " + file + ". Review them and change them as needed."
	}
	return "SuiteWard suggests this declaration, " + file + " with the " + adjective + " patterns added. Review it and change it as needed:\n\n" + fenced(withNewline(declaration))
}

// scopeSuggestReply renders the notice as the reply of /suiteward suggest: it names the base the head was compared with, shows
// the patterns that base lacks and the declaration with them, if there is one, and ends with the next step.
func scopeSuggestReply(n ScopeNotice) string {
	subject, file := baseNames(n.Base)
	has := len(n.Declaration) > 0
	action, put := pasteAction(has, !has), "this declaration"
	if !has {
		put = "these patterns"
	}
	var parts []string
	if len(n.Paths) == 0 {
		parts = []string{"`/suiteward suggest`: compared with " + subject + ", no test-like file of the head of this pull request is uncovered, but some patterns are missing. Nothing changed."}
	} else {
		parts = []string{
			"`/suiteward suggest`: compared with " + subject + ", the head of this pull request has test-like files that are not covered. Nothing changed.",
			listingAndMore("Test-like files in this pull request's head that "+subject+" does not cover", n.Paths, n.PathsTotal),
		}
	}
	parts = append(parts, missingLists(n)...)
	parts = append(parts, suggestedDeclaration(file, "missing", n.Declaration))
	if n.Cut {
		parts = append(parts, cutWarning)
	}
	if len(n.Mixed) > 0 {
		parts = append(parts, mixedWarning(n.Mixed))
	}
	if len(n.Submodules) > 0 {
		parts = append(parts, submodulesWarning(n.Submodules))
	}
	if n.Base != BaseDefaultBranch { // that file is neither on this pull request's branch nor in its diff: no link to edit it here, no review on it
		parts = append(parts, pasteStep(n.Repo, n.Branch, action),
			"When this pull request's diff shows the lists of `.suiteward.yml`, SuiteWard also posts these lines as a review comment, and GitHub's **Commit suggestion** button applies them in one click.")
	} else if n.DefaultBranch != "" {
		parts = append(parts, "To change `.suiteward.yml` on the default branch, [open GitHub's editor for it]("+editLink(n.Repo, n.DefaultBranch)+") and "+action+". "+
			"GitHub offers a new branch and a pull request when the default branch is protected.")
	}
	parts = append(parts, "This comment is a suggestion, not an approval. It does not change the check, and SuiteWard does not run tests.")
	switch n.Base {
	case BaseContract:
		parts = append(parts, "Committing it creates a protected change: approve the new revision SuiteWard posts, like any other change to the protected files. Nothing is protected until that change is approved and promoted.")
	case BasePull:
		parts = append(parts, "Nothing is protected until the first contract is approved and promoted.",
			"Once the file is committed on this branch, comment this command to start the first contract:\n\n"+fenced("/suiteward propose\n"))
	case BaseDefaultBranch:
		parts = append(parts, "Nothing is protected until the first contract is approved and promoted.",
			"The first contract is proposed from the default branch's `.suiteward.yml`, so put "+put+" on the default branch first: commit or merge it there. "+commitNote+" "+
				"Then comment this command to start the first contract:\n\n"+fenced("/suiteward propose\n"))
	}
	return replyTo(paragraphs(parts...), n.Source)
}

// editLink is GitHub's editor for .suiteward.yml on a branch; the branch is path-escaped segment by segment.
func editLink(repo, branch string) string {
	return "https://github.com/" + repo + "/edit/" + pathEscape(branch) + "/.suiteward.yml"
}

// newLink is GitHub's new-file page for .suiteward.yml on a branch, with no content: the file does not exist there yet.
func newLink(repo, branch string) string {
	return "https://github.com/" + repo + "/new/" + pathEscape(branch) + "?filename=.suiteward.yml"
}

func pathEscape(branch string) string {
	segments := strings.Split(branch, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

// ScopeSuggestionBody renders the body of a review comment that suggests new patterns for a list of .suiteward.yml: one
// sentence, then a suggestion block that holds anchorLine (the list item the comment is on) unchanged, followed by one item
// per pattern at the same indentation, in the quoting that quoting names (see Quoting), as scope.AddPatterns writes it (see suggestedItems). An
// input that does not fit panics (see ValidateScopeSuggestion).
func ScopeSuggestionBody(anchorLine, indent string, patterns []string, quoting Quoting) string {
	must(ValidateScopeSuggestion(anchorLine, indent, patterns, quoting))
	content := anchorLine + suggestedItems(patterns, indent, quoting.styleOf(anchorLine))
	return paragraphs("SuiteWard suggests adding these patterns to this list; committing the suggestion is a protected change that needs the owner's approval.",
		fence("suggestion", content+"\n"))
}

// styleOf is the quoting of the items to write: the one q names, or for the zero value the quoting of the item that anchorLine holds, which is all that a
// payload written before the style was carried knows of the list: a list of mixed quoting then follows that item, where scope.AddPatterns writes plain.
func (q Quoting) styleOf(anchorLine string) yaml.Style {
	switch q {
	case QuotingPlain:
		return 0
	case QuotingSingle:
		return yaml.SingleQuotedStyle
	case QuotingDouble:
		return yaml.DoubleQuotedStyle
	}
	return quotingOf(anchorLine)
}

// validate reports a style that is none of the three and not the zero value.
func (q Quoting) validate() error {
	if q != "" && q != QuotingPlain && q != QuotingSingle && q != QuotingDouble {
		return fmt.Errorf("commandtext: quoting %q is unknown", q)
	}
	return nil
}

// quotingOf is the quoting of the item that anchorLine holds: double, single or plain.
func quotingOf(anchorLine string) yaml.Style {
	item := strings.TrimLeft(strings.TrimPrefix(strings.TrimLeft(anchorLine, " "), "-"), " ")
	switch {
	case strings.HasPrefix(item, `"`):
		return yaml.DoubleQuotedStyle
	case strings.HasPrefix(item, "'"):
		return yaml.SingleQuotedStyle
	}
	return 0
}

// suggestedItems is the line break and the list item of each pattern at indent, written in style as scope.AddPatterns writes them: by
// the encoder that writes the new declaration, so what the style allows is its decision (a plain item that YAML would read as another
// scalar or as syntax gets the quotes it needs). Style 0 is plain, the style of a list that is created.
func suggestedItems(patterns []string, indent string, style yaml.Style) string {
	var items strings.Builder
	for _, p := range patterns {
		item := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!str", Value: p, Style: style}}}
		line, err := yaml.Marshal(item)
		if err != nil {
			panic(fmt.Sprintf("commandtext: a validated pattern %q cannot be written as a list item: %v", p, err))
		}
		items.WriteString("\n" + indent + strings.TrimSuffix(string(line), "\n"))
	}
	return items.String()
}

// ValidateScopeSuggestion reports why ScopeSuggestionBody would panic on its arguments, as an error. The items are written in the
// style quoting names (see Quoting), but a pattern that cannot be written inside a double-quoted YAML scalar is still refused, not left
// out: the caller decides what to do.
func ValidateScopeSuggestion(anchorLine, indent string, patterns []string, quoting Quoting) error {
	if err := quoting.validate(); err != nil {
		return err
	}
	switch {
	case len(patterns) == 0 || len(patterns) > maxListed:
		return fmt.Errorf("commandtext: %d suggested patterns, want 1 to %d", len(patterns), maxListed)
	case strings.Trim(indent, " ") != "":
		return fmt.Errorf("commandtext: indent %q is not spaces", indent)
	case len(anchorLine) > 1000 || !utf8.ValidString(anchorLine) || strings.ContainsAny(anchorLine, "\r\n"):
		return errors.New("commandtext: the anchor line is over 1000 bytes, not UTF-8 or not one line")
	case !strings.HasPrefix(anchorLine, indent+"- "):
		return fmt.Errorf("commandtext: the anchor line %q is not a list item at indent %q", anchorLine, indent)
	}
	for _, p := range patterns {
		if p == "" || !suggestable(p) {
			return fmt.Errorf("commandtext: pattern %q cannot be suggested", p)
		}
	}
	return nil
}

// Quoting is the quoting style of the list that a review suggestion extends, as scope.AddPatterns would write its new items: the quoting
// that every existing item shares, plain when they differ. The zero value follows the anchor line, which is all that a payload written before
// the style was carried knows of the list.
type Quoting string

const (
	QuotingPlain  Quoting = "plain"
	QuotingSingle Quoting = "single"
	QuotingDouble Quoting = "double"
)
