package commandtext

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// RunnerUncovered is what the comment of a runner-uncovered check states (docs/contracts/runtime.md, "Check reason runner-uncovered").
// Repo (owner/name) and Branch (the pull request's head ref) build the editor link. Paths holds at most 20 of the PathsTotal runner
// files that the pull request adds or changes outside the protected scope, each already cut by the caller. Protect is the full
// declaration that protects them, Ignore the full declaration that excludes them; Ignore is empty when the paths cannot be named in
// an exclude list or the declaration cannot be shown, and the comment then offers only to protect them. When Protect cannot be shown
// either (see MaxDeclaration), it is empty and ProtectPatterns holds the runner patterns that the comment lists to add to the `runner` list.
type RunnerUncovered struct {
	Repo, Branch    string
	Paths           []string
	PathsTotal      int
	Protect         []byte
	ProtectPatterns []string
	Ignore          []byte
}

// maxRunnerPatterns bounds ProtectPatterns: the runner names of all the families of runner files that SuiteWard knows (25 today), with room to grow.
const maxRunnerPatterns = 30

// ValidateRunnerUncovered reports why RunnerUncoveredComment would panic on n, as an error: a caller that stores the notice calls it
// first, so a payload that cannot be rendered is refused where it is built.
func ValidateRunnerUncovered(n RunnerUncovered) error {
	if err := ValidateEditTarget(n.Repo, n.Branch); err != nil {
		return err
	}
	switch {
	case len(n.Paths) == 0 || len(n.Paths) > maxListed || n.PathsTotal < len(n.Paths):
		return fmt.Errorf("commandtext: %d paths of a total of %d in a runner-uncovered notice, want 1 to %d, and a total that is not below them", len(n.Paths), n.PathsTotal, maxListed)
	case len(n.Protect) == 0 && (len(n.ProtectPatterns) == 0 || len(n.ProtectPatterns) > maxRunnerPatterns || slices.Contains(n.ProtectPatterns, "")):
		return fmt.Errorf("commandtext: a notice with no protect declaration lists 1 to %d runner patterns, none empty, and has %d", maxRunnerPatterns, len(n.ProtectPatterns))
	case len(n.Protect) > 0 && len(n.ProtectPatterns) > 0:
		return errors.New("commandtext: a notice offers the protect choice as a declaration or as patterns, not both")
	case !CanShow(n.Protect):
		return fmt.Errorf("commandtext: a protect declaration of %d bytes is over %d bytes or holds a character that is not graphic besides newline (LF or CRLF) and tab", len(n.Protect), MaxDeclaration)
	case !CanShow(n.Ignore):
		return fmt.Errorf("commandtext: an ignore declaration of %d bytes is over %d bytes or holds a character that is not graphic besides newline (LF or CRLF) and tab", len(n.Ignore), MaxDeclaration)
	}
	return nil
}

// RunnerUncoveredComment renders the comment that tells the owner which runner files of a pull request the current contract does not
// protect, why that blocks the check, and the two ways out with their full declarations and the link to GitHub's editor. It is a
// suggestion: it commits nothing, approves nothing and never says that tests ran. The paths and the declarations come from the
// repository, so they are shown only inside code fences that backticks cannot close. The paths are shown with their control characters
// escaped. A declaration is shown as it is, and ValidateRunnerUncovered refuses one that holds a character that is not graphic other than
// a newline (LF or CRLF) and a tab. The branch is path-escaped. An input that does not fit panics (see ValidateRunnerUncovered).
func RunnerUncoveredComment(n RunnerUncovered) string {
	must(ValidateRunnerUncovered(n))
	parts := []string{
		"**SuiteWard found runner files that the contract does not protect**",
		listingAndMore("Runner files that this pull request adds or changes and the current contract does not protect", n.Paths, n.PathsTotal),
		"A runner file sets up how tests run. It can change how the protected tests run without touching any of them: a `conftest.py` can skip them, and a `jest.config.js` can narrow which files Jest runs. " +
			"So SuiteWard keeps the check waiting (`in_progress`) until the owner decides what to do with these files.",
		"There are two ways out. Each is a change to `.suiteward.yml` that the owner approves like any other change to the protected files.",
		"**Protect the files.** " + protectChoice(n),
	}
	if len(n.Ignore) > 0 {
		parts = append(parts, "**Ignore the files.** This is the approved version's `.suiteward.yml` with the paths of these files added to `exclude`. "+
			"Excluding a path stops protecting it. The owner approves that too, and each pattern names exactly one file:\n\n"+fenced(withNewline(n.Ignore)))
	} else {
		parts = append(parts, "SuiteWard cannot offer to ignore these files. A path with a character that `exclude` reads as a pattern cannot be named there exactly, and a file that cannot be shown here is not offered. Protect them, or write the `exclude` entries yourself.")
	}
	parts = append(parts,
		pasteStep(n.Repo, n.Branch, pasteAction(len(n.Protect) > 0 || len(n.Ignore) > 0, len(n.Protect) == 0)),
		"When this pull request's diff shows the lists of `.suiteward.yml`, SuiteWard also posts each choice as a review comment, and GitHub's **Commit suggestion** button applies it in one click.",
		"Nothing is protected or ignored until the owner approves the new revision and it is merged.",
		"This comment is a suggestion, not an approval.",
	)
	return paragraphs(parts...)
}

// protectChoice is the protect choice of n: its declaration, or the runner patterns to add when the declaration cannot be shown.
func protectChoice(n RunnerUncovered) string {
	if len(n.Protect) == 0 {
		return "Add these runner patterns to the `runner` list of the approved version's `.suiteward.yml`. Review them and change them as needed:\n\n" + fencedLines(n.ProtectPatterns)
	}
	return "This is the approved version's `.suiteward.yml` with the runner patterns of these files added to `runner`. Review it and change it as needed:\n\n" + fenced(withNewline(n.Protect))
}

func withNewline(declaration []byte) string {
	text := string(declaration)
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text
}

// RunnerChoice is one of the two ways out of a runner-uncovered check.
type RunnerChoice uint8

const (
	ProtectChoice RunnerChoice = iota + 1 // add the runner patterns of the files to the runner list
	IgnoreChoice                          // add the paths of the files to the exclude list
)

// maxKeyIndent bounds the indent of a list that a suggestion creates.
const maxKeyIndent = 40

// RunnerChoiceBody renders the body of a review comment that suggests one choice on .suiteward.yml: one sentence, then a suggestion
// block that holds anchorLine (the list item the comment is on) unchanged, followed by one item per pattern, written as scope.AddPatterns
// writes it (see suggestedItems). With newList empty the items follow the anchor's list at indent, the spaces before its dash, in the
// quoting that quoting names (see Quoting). With newList (runner for the protect choice, exclude for the ignore choice) the anchor ends another list, and the block continues with that key at indent and its items two spaces deeper, in plain style: quoting is then empty.
// An input that does not fit panics (see ValidateRunnerChoice).
func RunnerChoiceBody(choice RunnerChoice, anchorLine, indent string, patterns []string, newList string, quoting Quoting) string {
	must(ValidateRunnerChoice(choice, anchorLine, indent, patterns, newList, quoting))
	var items string
	if newList == "" {
		items = suggestedItems(patterns, indent, quoting.style())
	} else { // a list the block creates has no items to follow
		items = "\n" + indent + newList + ":" + suggestedItems(patterns, indent+"  ", 0)
	}
	content := anchorLine + items
	sentence := "SuiteWard suggests protecting the runner files of this pull request by adding their patterns to the `runner` list. Committing the suggestion changes the protected files, so it needs the owner's approval."
	if choice == IgnoreChoice {
		sentence = "SuiteWard suggests ignoring the runner files of this pull request by adding their paths to the `exclude` list. Committing the suggestion stops protecting those files, so it needs the owner's approval."
	}
	return paragraphs(sentence, fence("suggestion", content+"\n"))
}

// choiceLists are the lists that the choices create: the runner list for the protect choice, the exclude list for the ignore choice.
var choiceLists = map[RunnerChoice]string{ProtectChoice: "runner", IgnoreChoice: "exclude"}

// ValidateRunnerChoice reports why RunnerChoiceBody would panic on its arguments, as an error. The items are written in the
// style quoting names (see Quoting), but a pattern that cannot be written inside a double-quoted YAML scalar is still refused, not left
// out: the caller decides what to do.
func ValidateRunnerChoice(choice RunnerChoice, anchorLine, indent string, patterns []string, newList string, quoting Quoting) error {
	if err := errors.Join(validateChoiceQuoting(quoting, newList), validatePatterns(patterns), validateAnchor(anchorLine)); err != nil {
		return err
	}
	switch {
	case choice != ProtectChoice && choice != IgnoreChoice:
		return fmt.Errorf("commandtext: runner choice %d is unknown", choice)
	case strings.Trim(indent, " ") != "" || len(indent) > maxKeyIndent:
		return fmt.Errorf("commandtext: indent %q is not at most %d spaces", indent, maxKeyIndent)
	case newList == "" && !strings.HasPrefix(anchorLine, indent+"- "):
		return fmt.Errorf("commandtext: the anchor line %q is not a list item at indent %q", anchorLine, indent)
	case newList != "" && !strings.HasPrefix(strings.TrimLeft(anchorLine, " "), "- "):
		return fmt.Errorf("commandtext: the anchor line %q is not a list item", anchorLine)
	case newList != "" && newList != choiceLists[choice]:
		return fmt.Errorf("commandtext: the list %q is not the one choice %d creates", newList, choice)
	}
	return nil
}

// validateChoiceQuoting reports a quoting that an existing list does not have, or that a list the choice creates has: a created list is written plain.
func validateChoiceQuoting(quoting Quoting, newList string) error {
	switch {
	case newList == "":
		return quoting.validate()
	case quoting != "":
		return fmt.Errorf("commandtext: a created list has no quoting, not %q", quoting)
	}
	return nil
}
