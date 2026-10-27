package run

import (
	"slices"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
)

// nestedReceipt runs a receipt of head over base with the canonical version that manifest holds, on a go test that passes everything but
// the packages under a go.mod, which go cannot resolve (the fake of the harness reports them as go does), and gives the body that it posts
// and the runtime.
func nestedReceipt(t *testing.T, head, base, manifest repo) ([]byte, *fakeRuntime) {
	t.Helper()
	s := receiptScenario()
	s.head, s.base, s.manifest = head, base, manifest
	rt := newRuntime(func(c call) (sandbox.Outcome, error) { return goTest(c, pass), nil })
	d := start(t, newService(t, s.serve(3)), rt)
	return d.result().body, rt
}

// excludedSubBody is the result of a receipt whose one test, TestSub of ./sub, is excluded as part of another module.
func excludedSubBody() string {
	return `{
		"status": "complete", "head": {"sha": "` + headSHA + `"}, "base": {"sha": "` + baseSHA + `"}, ` + commonExecutor + `, ` + commonSandbox + `,
		"tests": [{"package": "sub", "name": "TestSub", "file": "sub/sub_test.go", "kind": "added", "excluded": "nested-module"}]
	}`
}

const subModule = "module example.com/sub\n"

// A go.mod that only the head has is the candidate's, in a directory that it may have chosen: it must not turn a test of the candidate that go
// would run into an excluded one, which neither blocks nor counts. The test runs, go cannot resolve its package on the head side, and that
// reads build-error: FAILS. On the base side, which has no such go.mod, the package resolves.
func TestAGoModThatOnlyTheHeadHasDoesNotExcludeTheTestsUnderIt(t *testing.T) {
	body, rt := nestedReceipt(t,
		repo{"go.mod": goMod, "lib.go": "package m\n", "sub/go.mod": subModule, "sub/sub_test.go": subTest},
		repo{"go.mod": goMod, "lib.go": "package m\n"},
		repo{".suiteward.yml": declaration})
	assertBody(t, body, `{
		"status": "complete", "head": {"sha": "`+headSHA+`"}, "base": {"sha": "`+baseSHA+`"}, `+commonExecutor+`, `+commonSandbox+`,
		"tests": [{"package": "sub", "name": "TestSub", "file": "sub/sub_test.go", "kind": "added", "head": ["build-error"], "base": ["pass"]}]
	}`)
	if !slices.ContainsFunc(rt.invocations(), func(c call) bool { return slices.Contains(c.Dirs, "sub") }) {
		t.Error("the test under the head's go.mod was never run")
	}
}

// A go.mod that the merge base has makes the package another module on both sides, as the base's own tests know: the exclusion stays.
func TestAGoModThatTheMergeBaseHasKeepsTheTestsUnderItExcluded(t *testing.T) {
	body, rt := nestedReceipt(t,
		repo{"go.mod": goMod, "lib.go": "package m\n", "sub/go.mod": subModule, "sub/sub_test.go": subTest},
		repo{"go.mod": goMod, "lib.go": "package m\n", "sub/go.mod": subModule},
		repo{".suiteward.yml": declaration})
	assertBody(t, body, excludedSubBody())
	if n := len(rt.invocations()); n != 0 {
		t.Errorf("%d containers ran go test for a test that is excluded", n)
	}
}

// A go.mod that the revision's manifest lists is protected: the exclusion stays, whether or not the base has it.
func TestAGoModThatTheManifestListsKeepsTheTestsUnderItExcluded(t *testing.T) {
	body, rt := nestedReceipt(t,
		repo{"go.mod": goMod, "lib.go": "package m\n", "sub/go.mod": subModule, "sub/sub_test.go": subTest},
		repo{"go.mod": goMod, "lib.go": "package m\n"},
		repo{".suiteward.yml": declaration, "sub/go.mod": subModule})
	assertBody(t, body, excludedSubBody())
	if n := len(rt.invocations()); n != 0 {
		t.Errorf("%d containers ran go test for a test that is excluded", n)
	}
}

// A nested module of the base that the head removed makes the directory part of the root module: its tests run on the head and pass, and on
// the base side, where it is still another module, go cannot resolve the package: build-error, which reads NEW API over a head pass.
func TestANestedModuleThatTheHeadRemovedRunsItsTestsAndTheBaseSideCannotBuildThem(t *testing.T) {
	body, _ := nestedReceipt(t,
		repo{"go.mod": goMod, "lib.go": "package m\n", "sub/sub_test.go": subTest},
		repo{"go.mod": goMod, "lib.go": "package m\n", "sub/go.mod": subModule},
		repo{".suiteward.yml": declaration})
	assertBody(t, body, `{
		"status": "complete", "head": {"sha": "`+headSHA+`"}, "base": {"sha": "`+baseSHA+`"}, `+commonExecutor+`, `+commonSandbox+`,
		"tests": [{"package": "sub", "name": "TestSub", "file": "sub/sub_test.go", "kind": "added", "head": ["pass"], "base": ["build-error"]}]
	}`)
}
