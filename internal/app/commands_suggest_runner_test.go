package app

import (
	"slices"
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
	w.requireReview(cjID1, pytestMinusConftest, []string{"conftest.py"}, suggestion("runner", 8, "  - .pytest.ini", "conftest.py"))
	w.requireNoNoticeKey()
}

// A name is not missing when the base lists the same pattern, or when the base covers it as a root path (a name with no glob character).
// A glob runner pattern is missing unless the same string is listed: another pattern that happens to match it is no promise about the next name.
func TestSuggestTreatsARunnerNameAsMissingOnlyWhenTheBaseNeitherListsNorCoversIt(t *testing.T) {
	const (
		excludedGlob = "version: 1\nexclude:\n  - jest.config.*\n"
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
		{"an exclude lists the glob", map[string]string{"jest.config.ts": "x\n", ".suiteward.yml": excludedGlob}, excludedGlob, nil},
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
	w.seedDeclared(pytestMinusConftest, pythonHead(pytestMinusConftest))
	w.pullFiles(changed("@@ -1,9 +1,9 @@"))
	w.syncAll(nil, cjSuggest(500, cjAt(10)))
	w.mustRun()

	w.requireAckPayload(cjID2, 500, commands.Suggested, map[string]any{"reason": "scope-notice", "branch": cjBranch, "base": "contract",
		"declaration": addPatterns(t, pytestMinusConftest, nil, []string{"conftest.py"}), "addRunner": []string{"conftest.py"}})
	w.requireReview(cjID2, pytestMinusConftest, []string{"conftest.py"}, suggestion("runner", 8, "  - .pytest.ini", "conftest.py"))
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
	if keys := w.scopeMessagesKeys("scope-review:"); len(keys) != 1 || keys[0] == reviewKey(decl, cjUtilTst) {
		t.Fatalf("review keys %v; want one that is not the key of the paths alone, %s", keys, reviewKey(decl, cjUtilTst))
	}
}

// The checks job reports what a pull request adds: a canonical that lacks a runner name no added file explains is no reason for a notice.
func TestChecksWriteNoScopeNoticeForARunnerNameTheCanonicalLacks(t *testing.T) {
	canonical := "version: 1\ninclude: [/tests/**, \"test_*.py\"]\nrunner: [pytest.toml, .pytest.toml, pytest.ini, .pytest.ini]\n"
	w := newCJWorldOf(t, withDeclaration(bootFiles("v1"), canonical))
	files := withFiles(withDeclaration(bootFiles("v1"), canonical), pythonTest)

	w.scopePull(files, files)

	w.wantNoScopeMessages()
}

// The runner patterns a missing path brings (the pytest family for a Python test file) are reported only when the base neither lists nor covers
// them (A3 pull request 7, rc.7: the reply listed the whole pytest family as missing, though the canonical listed it).
func TestSuggestReportsNoRunnerNameTheBaseAlreadyListsForAnUncoveredTestFile(t *testing.T) {
	const decl = "version: 1\ninclude:\n  - /tests/**\nrunner:\n  - .pytest.ini\n  - .pytest.toml\n  - pytest.ini\n  - pytest.toml\n  - conftest.py\n  - jest.config.*\n"
	w := newCjWorld(t)
	w.seedDeclared(decl, map[string]string{"README.md": "x", ".suiteward.yml": decl, "app/calc_test.py": "x\n", "web/sum.test.js": "x\n"})
	w.pullFiles(nil)
	w.syncAll(nil, cjSuggest(500, cjAt(10)))
	w.mustRun()

	include := []string{"*.test.*", "*_test.py"}
	w.requireAckPayload(cjID2, 500, commands.Suggested, map[string]any{"reason": "scope-notice", "branch": cjBranch, "base": "contract", "paths": []string{"app/calc_test.py", "web/sum.test.js"}, "pathsTotal": 2,
		"declaration": addPatterns(t, decl, include, nil), "addInclude": include})
}

// The limit of 20 patterns holds for the missing ones: names the base lists do not use up room, nor set Cut.
func TestSuggestDoesNotCutForRunnerNamesTheBaseAlreadyLists(t *testing.T) {
	const decl = "version: 1\ninclude:\n  - /tests/**\nrunner:\n  - .pytest.ini\n  - .pytest.toml\n  - pytest.ini\n  - pytest.toml\n  - conftest.py\n"
	// One test file for each of 16 convention globs: with the 5 pytest names that is 21 patterns, 16 of them missing.
	files := []string{"a_test.go", "a.test.js", "a.spec.js", "a.cy.js", "a_spec.rb", "a_test.rb", "a_test.exs", "a_test.dart", "ATest.java", "ATests.java", "ATest.kt", "ATests.cs", "ATest.php", "test_a.py", "a_test.py", "tests.py"}
	include := []string{"*.cy.*", "*.spec.*", "*.test.*", "*Test.java", "*Test.kt", "*Test.php", "*Tests.cs", "*Tests.java", "*_spec.rb", "*_test.dart", "*_test.exs", "*_test.go", "*_test.py", "*_test.rb", "test_*.py", "tests.py"}
	w := newCjWorld(t)
	w.seedDeclared(decl, withFiles(map[string]string{"README.md": "x", ".suiteward.yml": decl}, files...))
	w.pullFiles(nil)
	w.syncAll(nil, cjSuggest(500, cjAt(10)))
	w.mustRun()

	sorted := slices.Sorted(slices.Values(files))
	w.requireAckPayload(cjID2, 500, commands.Suggested, map[string]any{"reason": "scope-notice", "branch": cjBranch, "base": "contract", "paths": sorted, "pathsTotal": len(files),
		"declaration": addPatterns(t, decl, include, nil), "addInclude": include})
}
