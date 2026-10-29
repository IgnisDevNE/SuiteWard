package app

import (
	"encoding/json"
	"strings"
	"testing"
)

// A message that v0.1.1 queued and the relay delivers after an upgrade carries none of the fields added since (addInclude, addRunner,
// protectPatterns, quoting). The notice and the runner-uncovered comment must still decode, validate and post, in the words of this tree: the bodies below are what
// this tree renders for those payloads today, and the wording has moved since v0.1.1, so they say nothing of what v0.1.1 rendered. The payloads are the JSON that
// v0.1.1's own marshaling wrote (a full declaration, or protect and ignore, and no later field), frozen here so that no fixture edit can change them.

var (
	v011ScopeNoticePayload     = json.RawMessage(`{"repositoryId":1001,"repo":"acme/widgets","pull":7,"head":"cccccccccccccccccccccccccccccccccccccccc","branch":"feature/x","paths":["pkg/a_test.go","pkg/b_test.go"],"pathsTotal":3,"declaration":"version: 1\ninclude:\n  - /tests/**\n  - \"*_test.go\"\n","mixed":["package.json"]}`)
	v011RunnerUncoveredPayload = json.RawMessage(`{"repositoryId":1001,"repo":"acme/widgets","pull":7,"head":"cccccccccccccccccccccccccccccccccccccccc","branch":"feature/x","paths":["jest.config.js","pkg/conftest.py"],"pathsTotal":3,"protect":"version: 1\ninclude:\n  - /tests/**\nrunner:\n  - jest.config.*\n","ignore":"version: 1\ninclude:\n  - /tests/**\nexclude:\n  - jest.config.js\n"}`)
	v011ScopeReviewPayload     = json.RawMessage(`{"repositoryId":1001,"repo":"acme/widgets","pull":7,"head":"cccccccccccccccccccccccccccccccccccccccc","suggestions":[{"list":"include","line":3,"anchor":"  - /tests/**","patterns":["*_test.go"]},{"list":"runner","line":6,"anchor":"- conftest.py","patterns":[".pytest.ini","pytest.ini"]}]}`)
)

const (
	v011ScopeNoticeToday = "**SuiteWard scope notice**\n\nTest-like files in this pull request that the current contract does not protect:\n\n```\npkg/a_test.go\npkg/b_test.go\n```\n\nand 1 more\n\nThe current contract does not protect these files. SuiteWard checks only the protected files, so a change to them needs no approval.\n\nSuiteWard suggests this file: the approved version's `.suiteward.yml` with the new patterns added. Review it and change it as needed:\n\n```\nversion: 1\ninclude:\n  - /tests/**\n  - \"*_test.go\"\n```\n\nThese files may hold test settings among other content, so SuiteWard does not suggest them. Protecting one would put every change to it, even a dependency update, behind an approval. Decide for yourself:\n\n```\npackage.json\n```\n\nTo change `.suiteward.yml` on this pull request's branch, [open GitHub's editor for it](https://github.com/acme/widgets/edit/feature/x/.suiteward.yml) and paste the suggested file. SuiteWard does not make the commit for you. You make it.\n\nMaking this commit changes the protected files. The owner approves it like any other change to them. Nothing is protected until that change is approved and merged.\n\nThis comment is a suggestion, not an approval. It does not change the check.\n"

	v011RunnerUncoveredToday = "**SuiteWard found runner files that the contract does not protect**\n\nRunner files that this pull request adds or changes and the current contract does not protect:\n\n```\njest.config.js\npkg/conftest.py\n```\n\nand 1 more\n\nA runner file sets up how tests run. It can change how the protected tests run without touching any of them: a `conftest.py` can skip them, and a `jest.config.js` can narrow which files Jest runs. So SuiteWard keeps the check waiting (`in_progress`) until the owner decides what to do with these files.\n\nThere are two ways out. Each is a change to `.suiteward.yml` that the owner approves like any other change to the protected files.\n\n**Protect the files.** This is the approved version's `.suiteward.yml` with the runner patterns of these files added to `runner`. Review it and change it as needed:\n\n```\nversion: 1\ninclude:\n  - /tests/**\nrunner:\n  - jest.config.*\n```\n\n**Ignore the files.** This is the approved version's `.suiteward.yml` with the paths of these files added to `exclude`. Excluding a path stops protecting it. The owner approves that too, and each pattern names exactly one file:\n\n```\nversion: 1\ninclude:\n  - /tests/**\nexclude:\n  - jest.config.js\n```\n\nTo change `.suiteward.yml` on this pull request's branch, [open GitHub's editor for it](https://github.com/acme/widgets/edit/feature/x/.suiteward.yml) and paste the suggested file. SuiteWard does not make the commit for you. You make it.\n\nWhen this pull request's diff shows the lists of `.suiteward.yml`, SuiteWard also posts each choice as a review comment, and GitHub's **Commit suggestion** button applies it in one click.\n\nNothing is protected or ignored until the owner approves the new revision and it is merged.\n\nThis comment is a suggestion, not an approval.\n"
)

func TestCommentPublisherStillPostsAScopeNoticeThatV011Queued(t *testing.T) {
	w := newCmtWorld(t)

	if err := w.pub.publish(t.Context(), cmtScopeKey, v011ScopeNoticePayload); err != nil {
		t.Fatalf("publish: %v", err)
	}

	w.wantPost(cmtScopeKey, v011ScopeNoticeToday)
}

func TestCommentPublisherStillPostsARunnerUncoveredCommentThatV011Queued(t *testing.T) {
	w := newCmtWorld(t)

	if err := w.pub.publish(t.Context(), cmtRunnerKey, v011RunnerUncoveredPayload); err != nil {
		t.Fatalf("publish: %v", err)
	}

	w.wantPost(cmtRunnerKey, v011RunnerUncoveredToday)
}

// The suggestions of a v0.1.1 scope review carry no quoting, and a review comment has no style to follow its anchor in: the payload is refused.
func TestCommentPublisherRefusesAV011ScopeReviewWithoutQuoting(t *testing.T) {
	w := newCmtWorld(t)

	if err := w.pub.publish(t.Context(), cmtReviewKey, v011ScopeReviewPayload); err == nil {
		t.Fatal("publish accepted a scope review whose suggestions carry no quoting")
	}
	if len(w.writer.reviewPosted) != 0 {
		t.Fatalf("%d review comments posted, want none", len(w.writer.reviewPosted))
	}
}

// v0.1.1 stored a revision's removed rules and added excludes in one list, each marked "rule <p>" or "exclude <p>". A message queued then and
// delivered after an upgrade renders them under their own headings, without the marks.
func TestCommentPublisherSplitsTheReductionOfAV011RevisionPayload(t *testing.T) {
	payload := json.RawMessage(`{"repositoryId":1001,"repo":"acme/widgets","pull":7,"reference":"P2-R2","previous":"P2-R1","canonical":"V1",` +
		`"added":["tests/new_test.go"],"modified":["tests/a_test.go"],"removed":[],"addedTotal":1,"modifiedTotal":1,"removedTotal":0,"reduced":true,` +
		`"reducedRules":["rule x","exclude y"],"reducedPaths":["x","y"],"reducedPathsTotal":2,"tier":"isolated"}`)
	w := newCmtWorld(t)

	if err := w.pub.publish(t.Context(), cmtRevisionKey, payload); err != nil {
		t.Fatalf("publish: %v", err)
	}

	posts := w.writer.posts()
	if len(posts) != 1 {
		t.Fatalf("posted %d comments, want 1", len(posts))
	}
	for _, want := range []string{"Rules removed from the scope:\n\n```\nx\n```", "Excludes added:\n\n```\ny\n```"} {
		if !strings.Contains(posts[0].body, want) {
			t.Errorf("the comment lacks %q:\n%s", want, posts[0].body)
		}
	}
	if strings.Contains(posts[0].body, "rule x") || strings.Contains(posts[0].body, "exclude y") {
		t.Errorf("the comment shows the marks that sort the entries:\n%s", posts[0].body)
	}
}
