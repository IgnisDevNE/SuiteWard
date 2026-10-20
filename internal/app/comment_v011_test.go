package app

import (
	"encoding/json"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// A message that v0.1.1 queued and the relay delivers after an upgrade carries none of the fields added since (addInclude, addRunner,
// protectPatterns, quoting). It must validate and post what v0.1.1 rendered for it: the bodies below are the output of the v0.1.1 sources
// (commandtext.ScopeNoticeComment, RunnerUncoveredComment and ScopeSuggestionBody at the tag), not of this tree's renderer, except for the review bodies, which M1.10 changed on purpose. The payloads are the JSON that
// v0.1.1's own marshaling wrote (a full declaration, or protect and ignore, and no later field), frozen here so that no fixture edit can change them.

var (
	v011ScopeNoticePayload     = json.RawMessage(`{"repositoryId":1001,"repo":"acme/widgets","pull":7,"head":"cccccccccccccccccccccccccccccccccccccccc","branch":"feature/x","paths":["pkg/a_test.go","pkg/b_test.go"],"pathsTotal":3,"declaration":"version: 1\ninclude:\n  - /tests/**\n  - \"*_test.go\"\n","mixed":["package.json"]}`)
	v011RunnerUncoveredPayload = json.RawMessage(`{"repositoryId":1001,"repo":"acme/widgets","pull":7,"head":"cccccccccccccccccccccccccccccccccccccccc","branch":"feature/x","paths":["jest.config.js","pkg/conftest.py"],"pathsTotal":3,"protect":"version: 1\ninclude:\n  - /tests/**\nrunner:\n  - jest.config.*\n","ignore":"version: 1\ninclude:\n  - /tests/**\nexclude:\n  - jest.config.js\n"}`)
	v011ScopeReviewPayload     = json.RawMessage(`{"repositoryId":1001,"repo":"acme/widgets","pull":7,"head":"cccccccccccccccccccccccccccccccccccccccc","suggestions":[{"list":"include","line":3,"anchor":"  - /tests/**","patterns":["*_test.go"]},{"list":"runner","line":6,"anchor":"- conftest.py","patterns":[".pytest.ini","pytest.ini"]}]}`)
)

const (
	v011ScopeNotice = "**SuiteWard scope notice**\n\nTest-like files in this pull request's head that the current contract does not protect:\n\n```\npkg/a_test.go\npkg/b_test.go\n```\n\nand 1 more\n\nThese files are not protected by the current contract: SuiteWard assesses only the protected files, so a change to them needs no approval.\n\nSuiteWard suggests this declaration, the current canonical `.suiteward.yml` with the new patterns added. Review it and change it as needed:\n\n```\nversion: 1\ninclude:\n  - /tests/**\n  - \"*_test.go\"\n```\n\nThese files may hold test settings among other content, so SuiteWard does not suggest them: protecting one would put every change to it, even a dependency update, behind an approval. Decide for yourself:\n\n```\npackage.json\n```\n\nTo change `.suiteward.yml` on this pull request's branch, [open GitHub's editor for it](https://github.com/acme/widgets/edit/feature/x/.suiteward.yml) and paste the declaration. SuiteWard never commits it for you: the commit is yours.\n\nCommitting it creates a protected change, which the owner approves like any other change to the protected files. Nothing is protected until that change is approved and promoted.\n\nThis comment is a suggestion, not an approval. It does not change the check, and SuiteWard does not run tests.\n"

	v011RunnerUncovered = "**SuiteWard found runner files outside the protected scope**\n\nRunner files that this pull request adds or changes and the current contract does not protect:\n\n```\njest.config.js\npkg/conftest.py\n```\n\nand 1 more\n\nA runner file can change how the protected tests run without touching any of them: a `conftest.py` can skip them, and a `jest.config.js` can narrow which files Jest runs. So SuiteWard keeps the check `in_progress` until the owner decides what to do with these files.\n\nThere are two ways out. Each is a commit to `.suiteward.yml` that the owner approves like any other change to the protected files.\n\n**Protect the files.** This declaration is the current canonical `.suiteward.yml` with the runner patterns of these files added to `runner`. Review it and change it as needed:\n\n```\nversion: 1\ninclude:\n  - /tests/**\nrunner:\n  - jest.config.*\n```\n\n**Ignore the files.** This declaration is the current canonical `.suiteward.yml` with the paths of these files added to `exclude`. Excluding a path removes it from the protected scope, a reduction that the owner approves too, and each pattern names exactly one file:\n\n```\nversion: 1\ninclude:\n  - /tests/**\nexclude:\n  - jest.config.js\n```\n\nTo change `.suiteward.yml` on this pull request's branch, [open GitHub's editor for it](https://github.com/acme/widgets/edit/feature/x/.suiteward.yml) and paste the declaration. SuiteWard never commits it for you: the commit is yours.\n\nWhen this pull request's diff shows the lists of `.suiteward.yml`, SuiteWard also posts each choice as a review comment, and GitHub's **Commit suggestion** button applies it in one click.\n\nNothing is protected or ignored until the owner approves the new revision and it is promoted.\n\nThis comment is a suggestion, not an approval, and SuiteWard does not run tests.\n"

	// v0.1.1 double-quoted every new item of a suggestion (- "*_test.go", - ".pytest.ini"). M1.10 decision 9 makes a suggestion with no
	// quoting follow its anchor line, so the review bodies are the one text that differs from what v0.1.1 rendered, in quote style only:
	// a plain anchor gets plain items, and single quotes where a pattern needs them.
	reviewIncludeSuggestion = "SuiteWard suggests adding these patterns to this list; committing the suggestion is a protected change that needs the owner's approval.\n\n```suggestion\n  - /tests/**\n  - '*_test.go'\n```\n"
	reviewRunnerSuggestion  = "SuiteWard suggests adding these patterns to this list; committing the suggestion is a protected change that needs the owner's approval.\n\n```suggestion\n- conftest.py\n- .pytest.ini\n- pytest.ini\n```\n"
)

func TestCommentPublisherPostsAV011ScopeNoticeAsV011RenderedIt(t *testing.T) {
	w := newCmtWorld(t)

	if err := w.pub.publish(t.Context(), cmtScopeKey, v011ScopeNoticePayload); err != nil {
		t.Fatalf("publish: %v", err)
	}

	w.wantPost(cmtScopeKey, v011ScopeNotice)
}

func TestCommentPublisherPostsAV011RunnerUncoveredCommentAsV011RenderedIt(t *testing.T) {
	w := newCmtWorld(t)

	if err := w.pub.publish(t.Context(), cmtRunnerKey, v011RunnerUncoveredPayload); err != nil {
		t.Fatalf("publish: %v", err)
	}

	w.wantPost(cmtRunnerKey, v011RunnerUncovered)
}

func TestCommentPublisherPostsAV011ScopeReviewWithoutQuotingFollowingItsAnchor(t *testing.T) {
	w := newCmtWorld(t)

	if err := w.pub.publish(t.Context(), cmtReviewKey, v011ScopeReviewPayload); err != nil {
		t.Fatalf("publish: %v", err)
	}

	posted := w.writer.reviewPosted
	if len(posted) != 2 {
		t.Fatalf("%d review comments, want 2", len(posted))
	}
	for i, want := range []struct{ list, text string }{{"include", reviewIncludeSuggestion}, {"runner", reviewRunnerSuggestion}} {
		if body := cmtTrimmed(want.text) + "\n\n" + commandtext.Marker(cmtInstance, cmtReviewKey+":"+want.list); posted[i].body != body {
			t.Errorf("%s review comment =\n%s\nwant\n%s", want.list, posted[i].body, body)
		}
	}
}
