package commandtext

import (
	"errors"
	"fmt"
)

// SourceProblem is what makes the source of a head invalid (docs/contracts/runtime.md, "Outbox github_comment, key invalid-source:..."); its zero value is invalid.
type SourceProblem uint8

const (
	DeclarationProblem SourceProblem = iota + 1 // the head's .suiteward.yml is not a valid declaration
	LinkProblem                                 // a symbolic link the scope covers or may cover below, whose target is no regular file of the tree
)

// InvalidSourceNotice is what the comment of an invalid-source check states. Path, already cut by the caller, names the link of a
// LinkProblem and is empty for the other problems: the comment never quotes the declaration or any other file content.
type InvalidSourceNotice struct {
	Problem SourceProblem
	Path    string
}

// ValidateInvalidSource reports why InvalidSourceComment would panic on s, as an error.
func ValidateInvalidSource(s InvalidSourceNotice) error {
	switch {
	case s.Problem < DeclarationProblem || s.Problem > LinkProblem:
		return fmt.Errorf("commandtext: source problem %d is unknown", s.Problem)
	case (s.Problem == LinkProblem) != (s.Path != ""):
		return errors.New("commandtext: only a link problem names a path, and it must")
	}
	return nil
}

// InvalidSourceComment renders the comment that explains an invalid-source check: the problem, the next step, and that the check
// stays failure until a push fixes it while any canonical contract stays in force. The path comes from the repository, so it is
// shown only inside a code fence that backticks cannot close, with control characters escaped. An input that does not fit panics
// (see ValidateInvalidSource).
func InvalidSourceComment(s InvalidSourceNotice) string {
	if err := ValidateInvalidSource(s); err != nil {
		panic(err.Error())
	}
	var problem, next string
	switch s.Problem {
	case DeclarationProblem:
		problem = "The `.suiteward.yml` of this pull request's head is not a valid declaration, so SuiteWard cannot tell which files this head protects."
		next = "Next: fix `.suiteward.yml` and push."
	case LinkProblem:
		problem = "This pull request's head has a symbolic link that the protected scope covers or may cover below, and whose target is not a regular file of the tree, " +
			"so SuiteWard cannot tell what protecting it would mean:\n\n" + fencedLines([]string{s.Path})
		next = "Next: remove the link, point it to a file, or narrow the pattern that reaches it, and push."
	}
	return paragraphs("**SuiteWard could not read the contract source of this pull request**", problem, next,
		"Any canonical contract already in force stays in force, and the check stays `failure` until a push fixes it.",
		"This comment is evidence, not an approval, and SuiteWard does not run tests.")
}
