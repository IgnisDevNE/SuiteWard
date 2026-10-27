package run

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/client"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
)

// receiptDeclaration approves the test files and their testdata.
const receiptDeclaration = "version: 1\ninclude:\n  - \"**/*_test.go\"\n  - \"**/testdata/**\"\nexecution:\n  profile: go\n"

const (
	headLib  = "package m\n\nimport \"os\"\n\nfunc Add(a, b int) int { _ = os.Getenv(\"CI\"); return a + b }\n"
	baseLib  = "package m\n\nfunc Add(a, b int) int { return a - b }\n"
	oldTestA = "package m\n\nimport \"testing\"\n\nfunc TestOld(t *testing.T) {}\n"
	oldTestB = "package m\n\nimport \"testing\"\n\nfunc TestOld(t *testing.T) { _ = 1 }\n"
	newTests = "package m\n\nimport \"testing\"\n\nfunc TestDetects(t *testing.T) {}\n\nfunc TestInert(t *testing.T) {}\n"
)

// aReceipt is a receipt job whose head fixes Add and adds two tests and changes a third; the head also holds a test file that go cannot
// read, a test of a file that the build constraint leaves out, and a submodule, and the base holds the older Add and another submodule.
func aReceipt() *scenario {
	s := receiptScenario()
	s.head = repo{
		"go.mod": goMod, "lib.go": headLib, "extra.go": "package m\n", "lib_test.go": newTests, "old_test.go": oldTestB,
		"bad_test.go":     "package m\n\nfunc broken( {\n",
		"integ_test.go":   "//go:build integration\n\npackage m\n\nimport \"testing\"\n\nfunc TestInteg(t *testing.T) {}\n",
		"testdata/in.txt": "data\n",
	}
	s.headExtra = []client.TreeEntry{{Path: "vendored", Mode: "160000", SHA: headSHA}}
	s.base = repo{"go.mod": strings.Replace(goMod, modPath, "example.com/old", 1), "lib.go": baseLib, "old_test.go": oldTestA}
	s.baseExtra = []client.TreeEntry{{Path: "oldsub", Mode: "160000", SHA: baseSHA}}
	s.manifest = repo{".suiteward.yml": receiptDeclaration, "old_test.go": oldTestA}
	return s
}

// detects is the runtime of aReceipt: Add is wrong on the base, so TestDetects fails there and every other test passes.
func detects(c call) (sandbox.Outcome, error) {
	return goTest(c, func(_, name string) string {
		if name == "TestDetects" && strings.Contains(string(c.Files["src/lib.go"]), "a - b") {
			return "fail"
		}
		return "pass"
	}), nil
}

// A receipt runs the tests that the revision adds or changes on the head, and on the base commit with the head's approved-pattern files
// over it, tells each its way, and flags the lines of the non-test files that differ.
func TestAReceiptJobRunsTheAddedAndChangedTestsOnBothSidesAndPostsTheContractsResult(t *testing.T) {
	s := aReceipt()
	rt := newRuntime(detects)
	d := start(t, newService(t, s.serve(3)), rt)
	body := d.result().body
	assertBody(t, body, `{
		"status": "complete", "head": {"sha": "`+headSHA+`"}, "base": {"sha": "`+baseSHA+`"}, `+commonExecutor+`, `+commonSandbox+`,
		"submodules": ["oldsub", "vendored"],
		"unparsed": ["bad_test.go"],
		"markers": [{"kind": "env-read", "path": "lib.go", "line": 5}],
		"tests": [
			{"package": ".", "name": "TestDetects", "file": "lib_test.go", "kind": "added", "head": ["pass"], "base": ["fail", "fail", "fail"]},
			{"package": ".", "name": "TestInert", "file": "lib_test.go", "kind": "added", "head": ["pass"], "base": ["pass"]},
			{"package": ".", "name": "TestInteg", "file": "integ_test.go", "kind": "added", "excluded": "build-constraint"},
			{"package": ".", "name": "TestOld", "file": "old_test.go", "kind": "changed", "head": ["pass"], "base": ["pass"]}
		]
	}`)

	runs := rt.invocations()
	if len(runs) != 4 {
		t.Fatalf("%d containers ran go test; want the head once and the base three times (the failing test twice more)", len(runs))
	}
	all := []string{"TestDetects", "TestInert", "TestOld"}
	if !slices.Equal(runs[0].Names, all) || !slices.Equal(runs[1].Names, all) || !slices.Equal(runs[2].Names, []string{"TestDetects"}) || !slices.Equal(runs[3].Names, []string{"TestDetects"}) {
		t.Errorf("the runs selected %v", namesOf(runs))
	}
	head, base := runs[0], runs[1]
	if got := string(head.Files["src/lib.go"]); got != headLib {
		t.Errorf("the head side runs lib.go %q, not the head's", got)
	}
	for _, name := range []string{"src/extra.go", "src/lib_test.go", "src/bad_test.go", "src/integ_test.go", "src/testdata/in.txt", "src/old_test.go"} {
		if _, ok := head.Files[name]; !ok {
			t.Errorf("the head side lacks %s", name)
		}
	}
	// The base side is the base commit with the head's version of every file that the approved patterns match and the head adds or changes.
	if got := string(base.Files["src/lib.go"]); got != baseLib {
		t.Errorf("the base side runs lib.go %q, not the base commit's", got)
	}
	if !strings.Contains(string(base.Files["src/go.mod"]), "example.com/old") {
		t.Errorf("the base side runs the go.mod %q, not the base commit's", base.Files["src/go.mod"])
	}
	if got := string(base.Files["src/old_test.go"]); got != oldTestB {
		t.Errorf("the base side runs old_test.go %q, not the head's changed version", got)
	}
	for _, name := range []string{"src/lib_test.go", "src/testdata/in.txt", "src/integ_test.go", "src/bad_test.go"} {
		if _, ok := base.Files[name]; !ok {
			t.Errorf("the base side lacks %s, a file of the head that the approved patterns match", name)
		}
	}
	if _, ok := base.Files["src/extra.go"]; ok {
		t.Error("the base side holds extra.go: nothing of the head crosses but the files that the approved patterns match")
	}
}

func TestAReceiptWhoseFilesHoldNoTestFunctionIsCompleteAndEmpty(t *testing.T) {
	s := receiptScenario()
	s.head = repo{"go.mod": goMod, "lib.go": "package m\n", "old_test.go": oldTestA, "testdata/in.txt": "data\n"}
	s.base = repo{"go.mod": goMod, "lib.go": "package m\n", "old_test.go": oldTestA}
	s.manifest = repo{".suiteward.yml": receiptDeclaration, "old_test.go": oldTestA}
	rt := newRuntime(func(call) (sandbox.Outcome, error) { return sandbox.Outcome{}, errors.New("nothing should run") })
	d := start(t, newService(t, s.serve(3)), rt)
	assertBody(t, d.result().body, `{
		"status": "complete", "head": {"sha": "`+headSHA+`"}, "base": {"sha": "`+baseSHA+`"}, `+commonExecutor+`, `+commonSandbox+`, "tests": []
	}`)
	if n := len(rt.invocations()); n != 0 {
		t.Errorf("%d containers ran go test with no test to evaluate", n)
	}
}

func TestASideWithNoGoModReadsEveryTestAsABuildErrorAndTheOtherSideStillRuns(t *testing.T) {
	s := receiptScenario()
	s.head = repo{"go.mod": goMod, "lib.go": "package m\n", "lib_test.go": newTests}
	s.base = repo{"lib.go": "package m\n"} // the revision adds the module
	rt := newRuntime(func(c call) (sandbox.Outcome, error) { return goTest(c, pass), nil })
	d := start(t, newService(t, s.serve(3)), rt)
	assertBody(t, d.result().body, `{
		"status": "complete", "head": {"sha": "`+headSHA+`"}, "base": {"sha": "`+baseSHA+`"}, `+commonExecutor+`, `+commonSandbox+`,
		"tests": [
			{"package": ".", "name": "TestDetects", "file": "lib_test.go", "kind": "added", "head": ["pass"], "base": ["build-error"]},
			{"package": ".", "name": "TestInert", "file": "lib_test.go", "kind": "added", "head": ["pass"], "base": ["build-error"]}
		]
	}`)
	if n := len(rt.invocations()); n != 1 {
		t.Errorf("%d containers ran go test; only the head side has a module to run", n)
	}
}

func TestEachSideHasItsOwnToolchainCheckBeforeAnythingRuns(t *testing.T) {
	s := receiptScenario()
	s.head = repo{"go.mod": goMod, "lib_test.go": newTests}
	s.base = repo{"go.mod": "module example.com/m\n\ngo 1.99\n"}
	rt := newRuntime(func(c call) (sandbox.Outcome, error) { return goTest(c, pass), nil })
	d := start(t, newService(t, s.serve(3)), rt)
	assertBody(t, d.result().body, `{
		"status": "unavailable", "reason": "toolchain-mismatch", "head": {"sha": "`+headSHA+`"}, "base": {"sha": "`+baseSHA+`"}, `+commonExecutor+`, `+commonSandbox+`
	}`)
	if n := len(rt.invocations()); n != 0 {
		t.Errorf("%d containers ran go test before the toolchain of the base side was found too old", n)
	}
}
