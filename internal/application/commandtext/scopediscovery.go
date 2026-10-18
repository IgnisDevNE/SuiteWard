package commandtext

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
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
// do not protect. Source is the id of the command comment that asked for the notice, zero when a job writes it unprompted.
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
	// (the patterns the base lacks, at most 20 together) and ends with the next step. Without a base both lists stay empty. With a base
	// Paths may be empty: a runner name can be missing before any file of that name exists.
	Base       ScopeBase
	AddInclude []string
	AddRunner  []string
}

// ValidateScopeNotice reports why ScopeNoticeComment would panic on n, as an error: a caller that stores the notice
// calls it first, so a payload that cannot be rendered is refused where it is built.
func ValidateScopeNotice(n ScopeNotice) error {
	if n.Source < 0 {
		return fmt.Errorf("commandtext: source comment %d does not fit a notice", n.Source)
	}
	if err := checkRepoAndBranch(n.Repo, n.Branch); err != nil {
		return err
	}
	switch {
	case len(n.Paths) > maxListed || (len(n.Paths) == 0 && (n.Base == 0 || len(n.AddInclude)+len(n.AddRunner) == 0)):
		return fmt.Errorf("commandtext: %d paths in a notice, want 1 to %d (none only in a reply with new patterns)", len(n.Paths), maxListed)
	case len(n.Mixed) > maxListed:
		return fmt.Errorf("commandtext: %d mixed files, at most %d", len(n.Mixed), maxListed)
	case len(n.Submodules) > maxListed:
		return fmt.Errorf("commandtext: %d submodules, at most %d", len(n.Submodules), maxListed)
	case len(n.Declaration) == 0 || len(n.Declaration) > MaxDeclaration || !utf8.Valid(n.Declaration):
		return fmt.Errorf("commandtext: a declaration of %d bytes is empty, over %d bytes or not UTF-8", len(n.Declaration), MaxDeclaration)
	case n.Base < 0 || n.Base > BaseDefaultBranch:
		return fmt.Errorf("commandtext: base %d is unknown", n.Base)
	case n.Base != 0 && n.Source == 0:
		return errors.New("commandtext: a base states the reply to a command")
	case n.Base == 0 && len(n.AddInclude)+len(n.AddRunner) > 0:
		return errors.New("commandtext: new patterns are listed only with a base")
	case len(n.AddInclude)+len(n.AddRunner) > maxListed:
		return fmt.Errorf("commandtext: %d new patterns, at most %d", len(n.AddInclude)+len(n.AddRunner), maxListed)
	}
	return nil
}

// ScopeNoticeComment renders the comment that tells the owner which test-like paths of a pull request the current
// contract does not protect, with the full declaration to commit and the link to GitHub's editor for it. It is a
// suggestion: it changes nothing, grants nothing and never says that tests ran. The paths and the declaration come from
// the repository, so they are shown only inside code fences that backticks cannot close, with control characters
// escaped; the branch is path-escaped. An input that does not fit panics (see ValidateScopeNotice).
func ScopeNoticeComment(n ScopeNotice) string {
	if err := ValidateScopeNotice(n); err != nil {
		panic(err.Error())
	}
	declaration := string(n.Declaration)
	if !strings.HasSuffix(declaration, "\n") {
		declaration += "\n"
	}
	if n.Base != 0 {
		return scopeSuggestReply(n, declaration)
	}
	parts := []string{
		"**SuiteWard scope notice**",
		listingAndMore("Test-like files in this pull request's head that the current contract does not protect", n.Paths, n.PathsTotal),
		"These files are not protected by the current contract: SuiteWard assesses only the protected files, so a change to them needs no approval.",
		"SuiteWard suggests this declaration, the current canonical `.suiteward.yml` with the new patterns added. Review it and change it as needed:\n\n" + fenced(declaration),
	}
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
		"To change `.suiteward.yml` on this pull request's branch, [open GitHub's editor for it]("+editLink(n.Repo, n.Branch)+") and paste the declaration. SuiteWard never commits it for you: the commit is yours.",
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

// scopeSuggestReply renders the notice as the reply of /suiteward suggest: it names the base the head was compared with, shows
// the patterns that base lacks and the declaration with them, and ends with the next step. declaration ends with a newline.
func scopeSuggestReply(n ScopeNotice, declaration string) string {
	subject, file := baseNames(n.Base)
	var parts []string
	if len(n.Paths) == 0 {
		parts = []string{"`/suiteward suggest`: compared with " + subject + ", no test-like file of the head of this pull request is uncovered, but some patterns are missing. Nothing changed."}
	} else {
		parts = []string{
			"`/suiteward suggest`: compared with " + subject + ", the head of this pull request has test-like files that are not covered. Nothing changed.",
			listingAndMore("Test-like files in this pull request's head that "+subject+" does not cover", n.Paths, n.PathsTotal),
		}
	}
	if len(n.AddInclude) > 0 {
		parts = append(parts, "Patterns missing from the `include` list:\n\n"+fencedLines(n.AddInclude))
	}
	if len(n.AddRunner) > 0 {
		parts = append(parts, "Patterns missing from the `runner` list:\n\n"+fencedLines(n.AddRunner),
			"Runner names matter before a file of that name exists: a file with one of these names, added later, can change how the tests run.")
	}
	parts = append(parts, "SuiteWard suggests this declaration, "+file+" with the missing patterns added. Review it and change it as needed:\n\n"+fenced(declaration))
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
		parts = append(parts, "To change `.suiteward.yml` on this pull request's branch, [open GitHub's editor for it]("+editLink(n.Repo, n.Branch)+") and paste the declaration. "+
			"SuiteWard never commits it for you: the commit is yours.",
			"When this pull request's diff shows the lists of `.suiteward.yml`, SuiteWard also posts these lines as a review comment, and GitHub's **Commit suggestion** button applies them in one click.")
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
			"The first contract is proposed from the default branch's `.suiteward.yml`, so put this declaration on the default branch first: commit or merge it there. SuiteWard never commits it for you: the commit is yours. "+
				"Then comment this command to start the first contract:\n\n"+fenced("/suiteward propose\n"))
	}
	return replyTo(paragraphs(parts...), n.Source)
}

// editLink is GitHub's editor for .suiteward.yml on a branch; the branch is path-escaped segment by segment.
func editLink(repo, branch string) string {
	return "https://github.com/" + repo + "/edit/" + pathEscape(branch) + "/.suiteward.yml"
}

func pathEscape(branch string) string {
	segments := strings.Split(branch, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

// ScopeSuggestionBody renders the body of a review comment that suggests new patterns for a list of .suiteward.yml: one
// sentence, then a suggestion block that holds anchorLine (the list item the comment is on) unchanged, followed by one
// quoted item per pattern at the same indentation. An input that does not fit panics (see ValidateScopeSuggestion).
func ScopeSuggestionBody(anchorLine, indent string, patterns []string) string {
	if err := ValidateScopeSuggestion(anchorLine, indent, patterns); err != nil {
		panic(err.Error())
	}
	content := anchorLine
	for _, p := range patterns {
		content += "\n" + indent + "- \"" + p + "\""
	}
	fence := strings.Repeat("`", max(3, longestBacktickRun(content)+1))
	return paragraphs("SuiteWard suggests adding these patterns to this list; committing the suggestion is a protected change that needs the owner's approval.",
		fence+"suggestion\n"+content+"\n"+fence)
}

// ValidateScopeSuggestion reports why ScopeSuggestionBody would panic on its arguments, as an error. A pattern that
// cannot be written inside a double-quoted YAML scalar is refused, not left out: the caller decides what to do.
func ValidateScopeSuggestion(anchorLine, indent string, patterns []string) error {
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
