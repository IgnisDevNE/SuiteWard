package commandtext

import (
	"errors"
	"fmt"
	"strings"
)

// SourceProblem is what makes the source of a head invalid (docs/contracts/runtime.md, "Outbox github_comment, key invalid-source:..."); its zero value is invalid.
type SourceProblem uint8

const (
	DeclarationProblem SourceProblem = iota + 1 // the head's .suiteward.yml is not a valid declaration
	LinkProblem                                 // a symbolic link the scope covers or may cover below, whose target is no regular file of the tree
)

// InvalidSourceNotice is what the comment of an invalid-source check states. Path, already cut by the caller, names the link of a
// LinkProblem and is empty for the other problems. Declaration, only for a LinkProblem, at most MaxDeclaration bytes and with no character that is not graphic besides newline (LF or CRLF) and tab, is the
// head's own .suiteward.yml with the link's anchored path added to exclude; it is empty when the head has none, and the comment then
// shows the pattern alone. Repo (owner/name) and Branch (the pull request's head ref) build the editor link; both or neither, and a
// comment without them has no link. The comment quotes no other file content.
type InvalidSourceNotice struct {
	Problem       SourceProblem
	Path          string
	Declaration   []byte
	PathCut       bool // Path was cut by the caller: it is not the link's path, so the comment offers the exclude only with a Declaration
	Repo, Branch  string
	NoDeclaration bool // LinkProblem only: the head has no .suiteward.yml (no Declaration either), so the link opens GitHub's new-file page, not an editor that would be a 404
}

// ValidateInvalidSource reports why InvalidSourceComment would panic on s, as an error.
func ValidateInvalidSource(s InvalidSourceNotice) error {
	switch {
	case s.Problem < DeclarationProblem || s.Problem > LinkProblem:
		return fmt.Errorf("commandtext: source problem %d is unknown", s.Problem)
	case (s.Problem == LinkProblem) != (s.Path != ""):
		return errors.New("commandtext: only a link problem names a path, and it must")
	case (len(s.Declaration) > 0 || s.PathCut) && s.Problem != LinkProblem:
		return errors.New("commandtext: only a link problem carries a declaration or a cut path")
	case len(s.Declaration) > MaxDeclaration || !verbatim(s.Declaration):
		return fmt.Errorf("commandtext: a declaration of %d bytes is over %d bytes or holds a character that is not graphic besides newline (LF or CRLF) and tab", len(s.Declaration), MaxDeclaration)
	case s.NoDeclaration && (s.Problem != LinkProblem || len(s.Declaration) > 0):
		return errors.New("commandtext: only a link problem of a head with no declaration says so, and it carries no declaration")
	case s.Repo != "" || s.Branch != "":
		return ValidateEditTarget(s.Repo, s.Branch)
	}
	return nil
}

// InvalidSourceComment renders the comment that explains an invalid-source check: the problem, the next step, and that the check
// stays failure until a push fixes it while any canonical contract stays in force. For a link the next step is to remove it or point
// it to a regular file, or to accept it with an exact anchored exclude, a scope reduction that the owner approves and that leaves
// nothing reached through the link protected; SuiteWard never suggests narrowing the include patterns. The path and the declaration come
// from the repository, so they are shown only inside code fences that backticks cannot close. The path is shown with its control
// characters escaped. The declaration is shown as it is, and ValidateInvalidSource refuses one that holds a character that is not graphic
// other than a newline (LF or CRLF) and a tab. An input that does not fit panics (see ValidateInvalidSource).
func InvalidSourceComment(s InvalidSourceNotice) string {
	must(ValidateInvalidSource(s))
	var problem, next string
	switch s.Problem {
	case DeclarationProblem:
		problem = "The `.suiteward.yml` of this pull request's head is not a valid declaration, so SuiteWard cannot tell which files this head protects."
		next = "Next: fix `.suiteward.yml` and push." + editSentence(s, "To change it on this pull request's branch")
	case LinkProblem:
		problem = "This pull request's head has a symbolic link that the protected scope covers or may cover below, and whose target is not a regular file of the tree, " +
			"so SuiteWard cannot tell what protecting it would mean:\n\n" + fencedLines([]string{s.Path})
		next = "Next: remove the link or point it to a regular file, and push." + acceptLink(s)
	}
	return paragraphs("**SuiteWard could not read the contract source of this pull request**", problem, next,
		"Any canonical contract already in force stays in force, and the check stays `failure` until a push fixes it.",
		"This comment is evidence, not an approval, and SuiteWard does not run tests.")
}

// editSentence is the paragraph that links to GitHub's editor for .suiteward.yml on the pull request's branch, led by lead; none for a notice without the link.
func editSentence(s InvalidSourceNotice, lead string) string {
	if s.Branch == "" {
		return ""
	}
	link := "[open GitHub's editor for it](" + editLink(s.Repo, s.Branch) + ")"
	if s.NoDeclaration {
		link = "[open GitHub's editor for a new file](" + newLink(s.Repo, s.Branch) + ")"
	}
	return editorStep(lead, link)
}

// acceptLink is the paragraphs that offer to accept the link of s with an exact anchored exclude, and warn what that means; none when
// the path has a character that exclude reads as a pattern, which would name more than the link. Without a declaration the pattern
// alone is offered, so there is none either for a path that was cut, which is not the link's path, or for one that the fence shows
// escaped, which is not the pattern to paste.
func acceptLink(s InvalidSourceNotice) string {
	if strings.ContainsAny(s.Path, `*?[\`) || (len(s.Declaration) == 0 && (s.PathCut || notGraphic(s.Path))) {
		return ""
	}
	choice := "Or accept the link by adding its exact anchored path to `exclude` in `.suiteward.yml`:\n\n" + fencedLines([]string{"/" + s.Path})
	if len(s.Declaration) > 0 {
		choice = "Or accept the link by adding its exact anchored path to `exclude`. This is the head's own `.suiteward.yml` with that path added:\n\n" + fenced(withNewline(s.Declaration))
	}
	return "\n\n" + choice + editSentence(s, "To make that change on this pull request's branch") + "\n\nAccepting the link means that nothing reached through the link is protected. It is a scope reduction, so the owner approves it like any other change to the protected files."
}
