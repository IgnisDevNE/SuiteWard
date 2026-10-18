package app

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
)

// A runner name is suggested because a later file of that name can change how the tests run, so /suiteward suggest reports a name the base
// neither lists nor covers even when no file of that name exists (docs/contracts/runtime.md, "Scope discovery (M1.9)", Command
// /suiteward suggest; live run 3).

const (
	// pytestMinusConftest is the pytest family of Suggest without conftest.py, as the owner removed it; the include list covers the test file.
	pytestMinusConftest = "version: 1\ninclude:\n  - \"test_*.py\"\nrunner:\n  - pytest.toml\n  - .pytest.toml\n  - pytest.ini\n  - .pytest.ini\n"
	pythonTest          = "app/test_x.py"
)

func pythonHead(declaration string) map[string]string {
	return map[string]string{"README.md": "x", pythonTest: "x\n", ".suiteward.yml": declaration}
}

func TestSuggestReportsARunnerNameTheBaseLacksEvenWhenNoFileIsUncovered(t *testing.T) {
	w := newCjWorld(t)
	w.trees(map[string]string{"README.md": "x"}, pythonHead(pytestMinusConftest))
	w.pullFiles(changed("@@ -1,9 +1,9 @@"))
	w.syncAll([]cjComment{cjSuggest(500, cjAt(10))})
	w.mustRun()

	w.requireAckPayload(cjID1, 500, commands.Suggested, map[string]any{"reason": "scope-notice", "branch": cjBranch, "base": "pull",
		"declaration": addPatterns(t, pytestMinusConftest, nil, []string{"conftest.py"}), "addRunner": []string{"conftest.py"}})
	w.requireReview(cjID1, cjHead, []string{"conftest.py"}, suggestion("runner", 8, "  - .pytest.ini", "conftest.py"))
	w.requireNoNoticeKey()
}

// A name is not missing when the base lists the same pattern, or when the base covers it as a root path (a name with no glob character).
// A glob runner pattern is missing unless the same string is listed: another pattern that happens to match it is no promise about the next name.
func TestSuggestTreatsARunnerNameAsMissingOnlyWhenTheBaseNeitherListsNorCoversIt(t *testing.T) {
	const (
		listedGlob   = "version: 1\nrunner:\n  - jest.config.*\n"
		coveringGlob = "version: 1\nrunner:\n  - jest.*\n"
	)
	for _, tc := range []struct {
		name   string
		head   map[string]string
		decl   string
		runner []string // the runner patterns reported missing; none means nothing-uncovered
	}{
		{"a runner glob covers the names", pythonHead("version: 1\ninclude:\n  - \"test_*.py\"\nrunner:\n  - pytest.*\n  - .pytest.*\n  - conftest.py\n"), "version: 1\ninclude:\n  - \"test_*.py\"\nrunner:\n  - pytest.*\n  - .pytest.*\n  - conftest.py\n", nil},
		{"an include pattern covers the names", pythonHead("version: 1\ninclude:\n  - \"test_*.py\"\n  - pytest.*\n  - .pytest.*\n  - conftest.py\n"), "version: 1\ninclude:\n  - \"test_*.py\"\n  - pytest.*\n  - .pytest.*\n  - conftest.py\n", nil},
		{"a glob runner pattern is listed", map[string]string{"jest.config.ts": "x\n", ".suiteward.yml": listedGlob}, listedGlob, nil},
		{"a glob runner pattern is only matched by another", map[string]string{"jest.config.ts": "x\n", ".suiteward.yml": coveringGlob}, coveringGlob, []string{"jest.config.*"}},
		{"an exclude removes the name", pythonHead(pytestMinusConftest + "exclude:\n  - conftest.py\n"), pytestMinusConftest + "exclude:\n  - conftest.py\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCjWorld(t)
			w.trees(map[string]string{"README.md": "x"}, tc.head)
			w.pullFiles(nil)
			w.syncAll([]cjComment{cjSuggest(500, cjAt(10))})
			w.mustRun()

			if tc.runner == nil {
				w.requireAckPayload(cjID1, 500, commands.Suggested, map[string]any{"reason": "nothing-uncovered", "base": "pull"})
				return
			}
			w.requireAckPayload(cjID1, 500, commands.Suggested, map[string]any{"reason": "scope-notice", "branch": cjBranch, "base": "pull",
				"declaration": addPatterns(t, tc.decl, nil, tc.runner), "addRunner": tc.runner})
		})
	}
}

func TestSuggestAfterTheFirstContractReportsARunnerNameTheCanonicalLacks(t *testing.T) {
	w := newCjWorld(t)
	w.seedDeclared(cjID2, pytestMinusConftest, pythonHead(pytestMinusConftest))
	w.pullFiles(changed("@@ -1,9 +1,9 @@"))
	w.syncAll(nil, cjSuggest(500, cjAt(10)))
	w.mustRun()

	w.requireAckPayload(cjID2, 500, commands.Suggested, map[string]any{"reason": "scope-notice", "branch": cjBranch, "base": "contract",
		"declaration": addPatterns(t, pytestMinusConftest, nil, []string{"conftest.py"}), "addRunner": []string{"conftest.py"}})
	w.requireReview(cjID2, cjHead, []string{"conftest.py"}, suggestion("runner", 8, "  - .pytest.ini", "conftest.py"))
}

// A review of one set of paths is not the review of the same paths plus a runner name, so a name the paths do not explain is part of the key.
func TestSuggestKeysTheReviewOfRunnerNamesApartFromTheReviewOfThePathsAlone(t *testing.T) {
	w := newCjWorld(t)
	decl := "version: 1\ninclude:\n  - \"test_*.py\"\nrunner:\n  - pytest.toml\n  - .pytest.toml\n  - pytest.ini\n  - .pytest.ini\n"
	w.trees(map[string]string{"README.md": "x"}, withFiles(pythonHead(decl), cjUtilTst))
	w.pullFiles(changed("@@ -1,9 +1,9 @@"))
	w.syncAll([]cjComment{cjSuggest(500, cjAt(10))})
	w.mustRun()

	w.requireAckPayload(cjID1, 500, commands.Suggested, map[string]any{"reason": "scope-notice", "branch": cjBranch, "base": "pull", "paths": []string{cjUtilTst}, "pathsTotal": 1,
		"declaration": addPatterns(t, decl, []string{"*_test.go"}, []string{"conftest.py"}), "addInclude": []string{"*_test.go"}, "addRunner": []string{"conftest.py"}})
	if keys := w.scopeMessagesKeys("scope-review:"); len(keys) != 1 || keys[0] == reviewKey(cjUtilTst) {
		t.Fatalf("review keys %v; want one that is not the key of the paths alone, %s", keys, reviewKey(cjUtilTst))
	}
}

// The checks job reports what a pull request adds: a canonical that lacks a runner name no added file explains is no reason for a notice.
func TestChecksWriteNoScopeNoticeForARunnerNameTheCanonicalLacks(t *testing.T) {
	canonical := "version: 1\ninclude: [/tests/**, \"test_*.py\"]\nrunner: [pytest.toml, .pytest.toml, pytest.ini, .pytest.ini]\n"
	w := newCJWorldOf(t, withDeclaration(bootFiles("v1"), canonical))
	files := withFiles(withDeclaration(bootFiles("v1"), canonical), pythonTest)

	w.scopePull(cjHead2, files, files)

	w.wantNoScopeMessages()
}
