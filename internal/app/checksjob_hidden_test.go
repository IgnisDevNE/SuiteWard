package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// A declaration shown for copying could carry invisible characters from a comment of the owner's .suiteward.yml (docs/contracts/runtime.md, decision 7 of
// M1.10): the comment that quotes it is refused by commandtext, so a declaration that holds one is left out where the comment can do without it.

// hiddenComment is a YAML comment with a zero-width space, a character that is not graphic.
const hiddenComment = "# a\u200bb\n"

func TestChecksShowThePatternAloneWhenTheHeadsDeclarationHoldsAHiddenCharacter(t *testing.T) {
	w := newCJWorld(t, true)
	hidden := reachable + hiddenComment
	files := bootFiles("v1")
	files[".suiteward.yml"] = hidden
	files["app/main.py"] = "print('x')\n"
	w.reader.repos[bootRepo].trees[cjHead2] = linkedTree{&bootTree{files: files}, map[string]string{"spec_link": "app"}}
	w.sync(map[int]string{4: bootHead, 5: cjHead2})

	w.mustCheck()

	w.want(5, checks.Failure, "invalid-source", 1)
	w.wantInvalidSource(invalidKeyOf("link", "spec_link", acceptingDeclaration(t, hidden, "spec_link"))) // the key digests the declaration as it did
	payload := w.invalidSourcePayload()
	if declaration, found := payload["declaration"]; found {
		t.Fatalf("declaration %q, want none: the comment shows the pattern alone", declaration)
	}
	var stored invalidSourcePayload
	if err := json.Unmarshal(w.scopeMessages("invalid-source:")[0].Payload, &stored); err != nil || stored.validate() != nil {
		t.Fatalf("the stored payload does not validate at publish: %v, %v", err, stored.validate())
	}
}

// A declaration that cannot be shown (a hidden character, or over the size of a notice) is never shown and never costs the comment: the
// scope notice, the runner-uncovered comment and the answer of suggest offer the patterns alone, as lines to copy (decision 7 of M1.10). The
// job neither fails nor loses the check, and what it stores is what the publisher renders.
func unshowableDeclarations() map[string]string {
	return map[string]string{
		"a hidden character":    cjDecl + hiddenComment,
		"a size over the limit": cjDecl + "# " + strings.Repeat("x", commandtext.MaxDeclaration) + "\n",
	}
}

// postedScopeNotice is the text that the publisher renders for the one scope notice of the world, and the stored payload; it fails when
// the payload does not validate where it is posted.
func (w *cjJobWorld) postedScopeNotice() (map[string]any, string) {
	w.t.Helper()
	messages := w.scopeMessages("scope-notice:")
	if len(messages) != 1 {
		w.t.Fatalf("scope notices %v, want one\nlogs: %s", w.scopeKeys("scope-notice:"), w.logs)
	}
	var stored map[string]any
	var p scopeNoticePayload
	if err := json.Unmarshal(messages[0].Payload, &stored); err != nil || json.Unmarshal(messages[0].Payload, &p) != nil {
		w.t.Fatalf("the stored payload does not decode: %v", err)
	}
	if err := p.validate(); err != nil {
		w.t.Fatalf("the stored payload does not validate at publish: %v", err)
	}
	return stored, commandtext.ScopeNoticeComment(p.notice())
}

// postedRunnerComment is postedScopeNotice for the one runner-uncovered comment.
func (w *cjJobWorld) postedRunnerComment() (map[string]any, string) {
	w.t.Helper()
	messages := w.scopeMessages("runner-uncovered:")
	if len(messages) != 1 {
		w.t.Fatalf("runner comments %v, want one\nlogs: %s", w.scopeKeys("runner-uncovered:"), w.logs)
	}
	var stored map[string]any
	var p runnerUncoveredPayload
	if err := json.Unmarshal(messages[0].Payload, &stored); err != nil || json.Unmarshal(messages[0].Payload, &p) != nil {
		w.t.Fatalf("the stored payload does not decode: %v", err)
	}
	if err := p.validate(); err != nil {
		w.t.Fatalf("the stored payload does not validate at publish: %v", err)
	}
	return stored, commandtext.RunnerUncoveredComment(p.notice())
}

func TestChecksOfferThePatternsAloneWhenTheDeclarationCannotBeShown(t *testing.T) {
	for name, declaration := range unshowableDeclarations() {
		files := withDeclaration(bootFiles("v1"), declaration)
		t.Run("the scope notice with "+name, func(t *testing.T) {
			w := newCJWorldOf(t, files)

			w.scopePull(withFiles(files, cjUtilTst), files)

			stored, text := w.postedScopeNotice()
			if _, found := stored["declaration"]; found || !reflect.DeepEqual(stored["addInclude"], []any{"*_test.go"}) {
				t.Fatalf("payload %v, want the include pattern and no declaration", stored)
			}
			if !strings.Contains(text, "```\n*_test.go\n```") || strings.Contains(text, "version: 1") {
				t.Fatalf("the notice does not show the pattern alone:\n%s", text)
			}
			w.want(5, checks.Success, "unchanged", 1)
		})
		t.Run("the runner-uncovered comment with "+name, func(t *testing.T) {
			w := newCJWorldOf(t, files)

			w.runnerPull(withFiles(files, cjJest), added(cjJest)...)

			stored, text := w.postedRunnerComment()
			_, hasProtect := stored["protect"]
			_, hasIgnore := stored["ignore"]
			if hasProtect || hasIgnore || !reflect.DeepEqual(stored["protectPatterns"], []any{"jest.config.*"}) {
				t.Fatalf("payload %v, want the runner pattern and no declaration", stored)
			}
			if !strings.Contains(text, "```\njest.config.*\n```") || strings.Contains(text, "version: 1") {
				t.Fatalf("the comment does not show the pattern alone:\n%s", text)
			}
			w.want(5, checks.InProgress, "runner-uncovered", 1)
		})
	}
}

// The runner patterns of every family that SuiteWard knows are the most a comment can list: they all fit the comment where it is posted.
func TestTheRunnerPatternsOfEveryFamilyFitThePatternsOnlyComment(t *testing.T) {
	all := inventory.RunnerPatterns([]string{"conftest.py", "jest.config.js", "vitest.config.ts", ".mocharc.js", "karma.conf.js", "playwright.config.ts", "cypress.config.ts", ".coveragerc",
		"phpunit.xml", ".rspec", "test_helper.rb", "dart_test.yaml", "codecov.yml"})
	raw, err := json.Marshal(map[string]any{"repositoryId": bootRepoID, "repo": bootRepo, "pull": 5, "head": cjHead2, "branch": "feature",
		"paths": []string{"conftest.py"}, "pathsTotal": 1, "protectPatterns": all})
	if err != nil {
		t.Fatal(err)
	}
	var p runnerUncoveredPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if err := p.validate(); err != nil {
		t.Fatalf("%d runner patterns do not fit the comment: %v", len(all), err)
	}
}

func TestSuggestOffersThePatternsAloneWhenTheCanonicalDeclarationHoldsAHiddenCharacter(t *testing.T) {
	w := newCjWorld(t)
	w.seedDeclared(cjDecl+hiddenComment, withFiles(bootFiles("v1"), cjUtilTst))
	w.syncAll(nil, cjSuggest(500, cjAt(10)))

	w.mustRun()

	w.requireAckPayload(cjID2, 500, commands.Suggested, map[string]any{"reason": "scope-notice", "branch": cjBranch, "base": "contract",
		"paths": []string{cjUtilTst}, "pathsTotal": 1, "addInclude": []string{"*_test.go"}})
	_, ack := w.requireDisposition(cjID2, 500, commands.Suggested)
	var p ackPayload
	if err := json.Unmarshal(ack.Payload, &p); err != nil || p.validate() != nil {
		t.Fatalf("the stored acknowledgment does not validate at publish: %v, %v", err, p.validate())
	}
	if text := commandtext.ScopeNoticeComment(p.notice()); !strings.Contains(text, "```\n*_test.go\n```") || strings.Contains(text, "version: 1") {
		t.Fatalf("the reply does not show the pattern alone:\n%s", text)
	}
}

// A runner file in a directory whose name holds a zero-width space is excludable, and the declaration that excludes it holds that character raw: the comment
// offers the protect choice alone, and the check says the same.
func TestChecksOfferOnlyTheProtectChoiceWhenTheIgnoreDeclarationHoldsAHiddenCharacter(t *testing.T) {
	const hidden = "a\u200bb/conftest.py"
	w := newCJWorld(t, true)

	w.runnerPull(withFiles(bootFiles("v1"), hidden), added(hidden)...)

	w.want(5, checks.InProgress, "runner-uncovered", 1)
	if len(w.scopeKeys("runner-uncovered:")) != 1 {
		t.Fatalf("runner comments %v, want one\nlogs: %s", w.scopeKeys("runner-uncovered:"), w.logs)
	}
	payload := w.runnerPayload(0)
	if _, hasIgnore := payload["ignore"]; hasIgnore || payload["protect"] == "" {
		t.Fatalf("payload %v, want a protect declaration and no ignore declaration", payload)
	}
}

// The patterns alone are the ones the declaration would add: a runner pattern that the canonical already lists is not missing, and is not offered.
func TestChecksOfferOnlyTheRunnerPatternsTheCanonicalLacks(t *testing.T) {
	files := withDeclaration(bootFiles("v1"), "version: 1\ninclude: [/tests/**]\nrunner: [pytest.ini]\n"+hiddenComment)
	w := newCJWorldOf(t, files)

	w.runnerPull(withFiles(files, "conftest.py"), added("conftest.py")...)

	stored, _ := w.postedRunnerComment()
	if want := []any{".pytest.ini", ".pytest.toml", "conftest.py", "pytest.toml"}; !reflect.DeepEqual(stored["protectPatterns"], want) {
		t.Fatalf("protectPatterns %v, want %v: pytest.ini is in the canonical's runner list", stored["protectPatterns"], want)
	}
}

// A canonical that lists every pattern of the family leaves nothing missing, and the comment still offers them: the head's approved declaration dropped them.
func TestChecksOfferTheWholeRunnerFamilyWhenTheCanonicalListsItAll(t *testing.T) {
	canonical := withDeclaration(bootFiles("v1"), "version: 1\ninclude: [/tests/**]\nrunner: [.pytest.ini, .pytest.toml, conftest.py, pytest.ini, pytest.toml]\n"+hiddenComment)
	w := newCJWorldOf(t, canonical)
	w.reader.files = map[int][]github.PullFile{5: added("conftest.py")}
	w.scopePull(withFiles(withDeclaration(canonical, reviewDecl), "conftest.py"), canonical)
	w.want(5, checks.InProgress, "approval-missing", 1)
	w.consent(contract.ApproveConsent, 201, 2, 1, 5)
	w.mustCheck()

	w.want(5, checks.InProgress, "runner-uncovered", 2)
	stored, _ := w.postedRunnerComment()
	if want := []any{".pytest.ini", ".pytest.toml", "conftest.py", "pytest.ini", "pytest.toml"}; !reflect.DeepEqual(stored["protectPatterns"], want) {
		t.Fatalf("protectPatterns %v, want %v", stored["protectPatterns"], want)
	}
}
