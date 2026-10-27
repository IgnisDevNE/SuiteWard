package run

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/client"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
)

func pass(string, string) string { return "pass" }

// A canonical job lays the whole governing manifest over the head, removes the head's approved-pattern files that the manifest lacks,
// runs the declared tests, runs again each test that failed, and posts exactly the result that the contract defines.
func TestACanonicalJobRunsTheDeclaredTestsOnTheHeadWithTheManifestLaidOver(t *testing.T) {
	s := canonicalScenario()
	s.head = repo{
		"go.mod": goMod, "lib.go": "package m\n",
		// a test that the head adds and the governing scope covers: the manifest lacks it, so it is removed
		"extra_test.go": "package m\n\nimport \"testing\"\n\nfunc TestExtra(t *testing.T) {}\n",
	}
	s.manifest = repo{".suiteward.yml": declaration, "lib_test.go": libTests}
	sv := s.serve(7)
	bRuns := 0
	rt := newRuntime(func(c call) (sandbox.Outcome, error) {
		return goTest(c, func(_, name string) string {
			if name == "TestB" {
				if bRuns++; bRuns == 1 {
					return "fail"
				}
			}
			return "pass"
		}), nil
	})
	d := start(t, newService(t, sv), rt)
	body := d.result().body

	assertBody(t, body, `{
		"status": "complete", "head": {"sha": "`+headSHA+`"}, `+commonExecutor+`, `+commonSandbox+`,
		"tests": [
			{"package": ".", "name": "TestA", "file": "lib_test.go", "kind": "declared", "head": ["pass"]},
			{"package": ".", "name": "TestB", "file": "lib_test.go", "kind": "declared", "head": ["fail", "pass"]}
		]
	}`)

	runs := rt.invocations()
	if len(runs) != 2 {
		t.Fatalf("%d containers ran go test; want the run and one rerun of the test that failed", len(runs))
	}
	if !slices.Equal(runs[0].Names, []string{"TestA", "TestB"}) || !slices.Equal(runs[1].Names, []string{"TestB"}) || !slices.Equal(runs[1].Dirs, []string{"."}) {
		t.Errorf("the runs selected %v and %v; want both tests, then TestB alone", runs[0].Names, runs[1].Names)
	}
	for i, c := range runs {
		assertTree(t, i, c)
		assertSpec(t, i, c)
	}
}

// assertTree checks the tar that a container of the canonical job extracted: the head with the manifest laid over it, and the files that
// go needs of its own.
func assertTree(t *testing.T, i int, c call) {
	t.Helper()
	for _, want := range []string{"src/go.mod", "src/lib.go", "src/lib_test.go", "src/.suiteward.yml"} {
		if _, ok := c.Files[want]; !ok {
			t.Errorf("run %d: the tar lacks %s: %v", i, want, c.Entries)
		}
	}
	if _, ok := c.Files["src/extra_test.go"]; ok {
		t.Errorf("run %d: the tar holds a test of the head that the governing scope covers and the manifest lacks", i)
	}
	if got := string(c.Files["src/lib_test.go"]); got != libTests {
		t.Errorf("run %d: lib_test.go is %q, not the manifest's", i, got)
	}
	if !hasPrefixEntry(c.Entries, goprofile.HomeDir+"/") || !hasPrefixEntry(c.Entries, goprofile.TmpDir+"/") {
		t.Errorf("run %d: go's HOME and TMPDIR have no placeholder file in the tar: %v", i, c.Entries)
	}
	if c.Entries[len(c.Entries)-1] != sandbox.CompleteMarker {
		t.Errorf("run %d: the completion marker is not the last entry", i)
	}
}

// assertSpec checks what the container was asked to run, and with what.
func assertSpec(t *testing.T, i int, c call) {
	t.Helper()
	argv, env := goprofile.Command(testIDs(c.Names, c.Dirs), 600*time.Second, false)
	spec := c.Spec
	if !slices.Equal(spec.Argv, argv) || !slices.Equal(spec.Env, env) {
		t.Errorf("run %d: argv %q env %q; want what goprofile.Command writes: %q %q", i, spec.Argv, spec.Env, argv, env)
	}
	if spec.Image != goprofile.Image || spec.Workdir != goprofile.TreeDir {
		t.Errorf("run %d: image %q workdir %q", i, spec.Image, spec.Workdir)
	}
	if l := spec.Limits; l.MemoryMiB != 3072 || l.CPUs != 4 || l.Pids != 512 || l.TmpfsMiB != 1024 || l.KillAfter < 660*time.Second {
		t.Errorf("run %d: the limits are %+v; want the job's, and a kill no sooner than 60 s after the test timeout", i, l)
	}
	if !slices.ContainsFunc(spec.Env, func(e string) bool { return strings.HasPrefix(e, "PATH=") }) {
		t.Errorf("run %d: no PATH in the environment, which env -i clears", i)
	}
}

func hasPrefixEntry(entries []string, prefix string) bool {
	return slices.ContainsFunc(entries, func(e string) bool { return strings.HasPrefix(e, prefix) })
}

func testIDs(names, dirs []string) []goprofile.TestID {
	var ids []goprofile.TestID
	for _, dir := range dirs {
		for _, name := range names {
			ids = append(ids, goprofile.TestID{Package: dir, Name: name})
		}
	}
	return ids
}

func TestADeclaredTestThatTheSandboxCannotRunIsListedExcludedAndNeverRun(t *testing.T) {
	s := canonicalScenario()
	s.head = repo{"go.mod": goMod, "lib.go": "package m\n", "sub/go.mod": "module example.com/sub\n"}
	s.headExtra = []client.TreeEntry{{Path: "vendored", Mode: "160000", SHA: headSHA}}
	s.manifest = repo{
		".suiteward.yml": declaration, "lib_test.go": libTests,
		"integ_test.go":   "//go:build integration\n\npackage m\n\nimport \"testing\"\n\nfunc TestInteg(t *testing.T) {}\n",
		"sub/sub_test.go": "package sub\n\nimport \"testing\"\n\nfunc TestSub(t *testing.T) {}\n",
	}
	rt := newRuntime(func(c call) (sandbox.Outcome, error) { return goTest(c, pass), nil })
	d := start(t, newService(t, s.serve(1)), rt)
	assertBody(t, d.result().body, `{
		"status": "complete", "head": {"sha": "`+headSHA+`"}, `+commonExecutor+`, `+commonSandbox+`,
		"submodules": ["vendored"],
		"tests": [
			{"package": ".", "name": "TestA", "file": "lib_test.go", "kind": "declared", "head": ["pass"]},
			{"package": ".", "name": "TestB", "file": "lib_test.go", "kind": "declared", "head": ["pass"]},
			{"package": ".", "name": "TestInteg", "file": "integ_test.go", "kind": "declared", "excluded": "build-constraint"},
			{"package": "sub", "name": "TestSub", "file": "sub/sub_test.go", "kind": "declared", "excluded": "nested-module"}
		]
	}`)
	for _, c := range rt.invocations() {
		if slices.Contains(c.Names, "TestInteg") || slices.Contains(c.Names, "TestSub") || slices.Contains(c.Dirs, "sub") {
			t.Errorf("an excluded test was run: %v in %v", c.Names, c.Dirs)
		}
	}
}

func TestACanonicalJobWithNoDeclaredTestIsCompleteAndRunsNothing(t *testing.T) {
	rt := newRuntime(func(call) (sandbox.Outcome, error) { return sandbox.Outcome{}, errors.New("nothing should run") })
	d := start(t, newService(t, canonicalScenario().serve(1)), rt)
	assertBody(t, d.result().body, `{"status": "complete", "head": {"sha": "`+headSHA+`"}, `+commonExecutor+`, `+commonSandbox+`, "tests": []}`)
	if n := len(rt.invocations()); n != 0 {
		t.Errorf("%d containers ran go test with no test declared", n)
	}
}

func TestAPackageThatDoesNotBuildReadsBuildErrorForEveryTestOfItAndIsNotRunAgain(t *testing.T) {
	s := canonicalScenario()
	s.head = repo{"go.mod": goMod, "lib.go": "package m\n"}
	s.manifest = repo{".suiteward.yml": declaration, "lib_test.go": strings.Replace(libTests, "func TestB(t *testing.T) {}",
		"func TestB(t *testing.T) {}\n\nfunc TestC(t *testing.T) {}\n\nfunc TestD(t *testing.T) {}\n\nfunc TestE(t *testing.T) {}\n", 1)}
	rt := newRuntime(func(c call) (sandbox.Outcome, error) {
		return goTest(c, func(_, name string) string {
			return map[string]string{"TestA": "pass", "TestB": "fail", "TestC": "", "TestD": "skip", "TestE": "build-error"}[name]
		}), nil
	})
	d := start(t, newService(t, s.serve(1)), rt)
	assertBody(t, d.result().body, `{
		"status": "complete", "head": {"sha": "`+headSHA+`"}, `+commonExecutor+`, `+commonSandbox+`,
		"tests": [
			{"package": ".", "name": "TestA", "file": "lib_test.go", "kind": "declared", "head": ["build-error"]},
			{"package": ".", "name": "TestB", "file": "lib_test.go", "kind": "declared", "head": ["build-error"]},
			{"package": ".", "name": "TestC", "file": "lib_test.go", "kind": "declared", "head": ["build-error"]},
			{"package": ".", "name": "TestD", "file": "lib_test.go", "kind": "declared", "head": ["build-error"]},
			{"package": ".", "name": "TestE", "file": "lib_test.go", "kind": "declared", "head": ["build-error"]}
		]
	}`)
	if n := len(rt.invocations()); n != 1 {
		t.Errorf("%d runs; a build error is the same on every run and is not run again", n)
	}
}

func TestAFailureAndAnAbsenceAreRunAgainUpToTheLimitAndASkipIsNot(t *testing.T) {
	s := canonicalScenario()
	s.head = repo{"go.mod": goMod, "lib.go": "package m\n"}
	s.manifest = repo{".suiteward.yml": declaration, "lib_test.go": strings.Replace(libTests, "func TestB(t *testing.T) {}",
		"func TestB(t *testing.T) {}\n\nfunc TestC(t *testing.T) {}\n\nfunc TestD(t *testing.T) {}\n", 1)}
	rt := newRuntime(func(c call) (sandbox.Outcome, error) {
		return goTest(c, func(_, name string) string {
			return map[string]string{"TestA": "pass", "TestB": "fail", "TestC": "", "TestD": "skip"}[name]
		}), nil
	})
	d := start(t, newService(t, s.serve(1)), rt)
	assertBody(t, d.result().body, `{
		"status": "complete", "head": {"sha": "`+headSHA+`"}, `+commonExecutor+`, `+commonSandbox+`,
		"tests": [
			{"package": ".", "name": "TestA", "file": "lib_test.go", "kind": "declared", "head": ["pass"]},
			{"package": ".", "name": "TestB", "file": "lib_test.go", "kind": "declared", "head": ["fail", "fail", "fail"]},
			{"package": ".", "name": "TestC", "file": "lib_test.go", "kind": "declared", "head": ["absent", "absent", "absent"]},
			{"package": ".", "name": "TestD", "file": "lib_test.go", "kind": "declared", "head": ["skip"]}
		]
	}`)
	runs := rt.invocations()
	if len(runs) != 3 || !slices.Equal(runs[1].Names, []string{"TestB", "TestC"}) || !slices.Equal(runs[2].Names, []string{"TestB", "TestC"}) {
		t.Errorf("the runs selected %v; want all four, then the failure and the absence twice", namesOf(runs))
	}
}

func namesOf(runs []call) [][]string {
	var out [][]string
	for _, c := range runs {
		out = append(out, c.Names)
	}
	return out
}

func TestAModuleWithDependenciesGetsThePrefetchedProxyFilesInTheSameTar(t *testing.T) {
	zipFile, modFile := moduleZip(t, "example.com/dep", "v1.0.0", 1000)
	proxy := proxyServing(t, map[string][]byte{"example.com/dep/@v/v1.0.0.zip": zipFile, "example.com/dep/@v/v1.0.0.mod": modFile})
	s := canonicalScenario()
	s.head = repo{
		"go.mod": goMod + "\nrequire example.com/dep v1.0.0\n",
		"go.sum": "example.com/dep v1.0.0 h1:abc=\nexample.com/dep v1.0.0/go.mod h1:def=\n",
		"lib.go": "package m\n",
	}
	s.manifest = repo{".suiteward.yml": declaration, "lib_test.go": libTests}
	rt := newRuntime(func(c call) (sandbox.Outcome, error) { return goTest(c, pass), nil })
	d := start(t, newService(t, s.serve(1)), rt, func(cfg *Config, _ *driver) { cfg.Proxy = proxy })
	if body := decode(t, d.result().body); body["status"] != "complete" {
		t.Fatalf("the run is %v, not complete", body)
	}
	c := rt.invocations()[0]
	if got := c.Files[goprofile.ProxyDir+"/example.com/dep/@v/v1.0.0.zip"]; string(got) != string(zipFile) {
		t.Errorf("the module zip is not in the tar under %s: %v", goprofile.ProxyDir, c.Entries)
	}
	if got := c.Files[goprofile.ProxyDir+"/example.com/dep/@v/v1.0.0.mod"]; string(got) != string(modFile) {
		t.Errorf("the module's go.mod is not in the tar under %s", goprofile.ProxyDir)
	}
	if !slices.Contains(c.Spec.Env, "GOPROXY=file:///work/"+goprofile.ProxyDir) {
		t.Errorf("the environment is %q; go reads the prefetched files as a file:// proxy", c.Spec.Env)
	}
}

func TestASideThatVendorsFetchesNothing(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); w.WriteHeader(http.StatusNotFound) }))
	t.Cleanup(srv.Close)
	s := canonicalScenario()
	s.head = repo{
		"go.mod": goMod + "\nrequire example.com/dep v1.0.0\n", "go.sum": "example.com/dep v1.0.0 h1:abc=\n",
		"vendor/modules.txt": "# example.com/dep v1.0.0\n", "lib.go": "package m\n",
	}
	s.manifest = repo{".suiteward.yml": declaration, "lib_test.go": libTests}
	rt := newRuntime(func(c call) (sandbox.Outcome, error) { return goTest(c, pass), nil })
	d := start(t, newService(t, s.serve(1)), rt, func(cfg *Config, _ *driver) { cfg.Proxy = Proxy{Base: srv.URL, Client: srv.Client()} })
	d.result()
	if hits.Load() != 0 {
		t.Errorf("the proxy was asked %d times for a side that vendors", hits.Load())
	}
	if env := rt.invocations()[0].Spec.Env; !slices.Contains(env, "GOPROXY=off") {
		t.Errorf("the environment is %q; a vendored side has no proxy", env)
	}
}

func TestACanonicalJobWithNoGoModReadsEveryTestAsABuildErrorWithoutRunningAnything(t *testing.T) {
	s := canonicalScenario()
	s.head = repo{"lib.go": "package m\n"}
	s.manifest = repo{".suiteward.yml": declaration, "lib_test.go": libTests}
	rt := newRuntime(func(call) (sandbox.Outcome, error) { return sandbox.Outcome{}, errors.New("nothing should run") })
	d := start(t, newService(t, s.serve(1)), rt)
	assertBody(t, d.result().body, `{
		"status": "complete", "head": {"sha": "`+headSHA+`"}, `+commonExecutor+`, `+commonSandbox+`,
		"tests": [
			{"package": ".", "name": "TestA", "file": "lib_test.go", "kind": "declared", "head": ["build-error"]},
			{"package": ".", "name": "TestB", "file": "lib_test.go", "kind": "declared", "head": ["build-error"]}
		]
	}`)
}

func TestTestsOfSeveralPackagesAreRunInOneInvocationAndNamedByTheirDirectories(t *testing.T) {
	s := canonicalScenario()
	s.head = repo{"go.mod": goMod, "pkg/sub/lib.go": "package sub\n"}
	s.manifest = repo{
		".suiteward.yml": declaration, "root_test.go": "package m\n\nimport \"testing\"\n\nfunc TestRoot(t *testing.T) {}\n",
		"pkg/sub/sub_test.go": "package sub\n\nimport \"testing\"\n\nfunc TestSub(t *testing.T) {}\n",
	}
	rt := newRuntime(func(c call) (sandbox.Outcome, error) { return goTest(c, pass), nil })
	d := start(t, newService(t, s.serve(1)), rt)
	assertBody(t, d.result().body, `{
		"status": "complete", "head": {"sha": "`+headSHA+`"}, `+commonExecutor+`, `+commonSandbox+`,
		"tests": [
			{"package": ".", "name": "TestRoot", "file": "root_test.go", "kind": "declared", "head": ["pass"]},
			{"package": "pkg/sub", "name": "TestSub", "file": "pkg/sub/sub_test.go", "kind": "declared", "head": ["pass"]}
		]
	}`)
	if runs := rt.invocations(); len(runs) != 1 || !slices.Equal(runs[0].Dirs, []string{".", "pkg/sub"}) {
		t.Errorf("the invocations were %v; want one over . and pkg/sub", namesOf(runs))
	}
}

func TestTheSandboxIsStoppedAndNothingHangsWhenItFailsBeforeReadingItsInput(t *testing.T) {
	s := canonicalScenario()
	s.head = repo{"go.mod": goMod, "lib.go": "package m\n"}
	s.manifest = repo{".suiteward.yml": declaration, "lib_test.go": libTests}
	rt := newRuntime(nil)
	rt.skipStdin = true // the runner refuses before it reads the standard input: the composing side must not wait for a reader forever
	d := start(t, newService(t, s.serve(1)), rt)
	assertBody(t, d.result().body, `{"status": "unavailable", "reason": "sandbox-unavailable", "head": {"sha": "`+headSHA+`"}, `+commonExecutor+`, `+commonSandbox+`}`)
}

// Without a declaration the scope is the protected defaults alone, so a head test that the manifest lacks stays, and is not expected.
func TestACanonicalJobWithNoDeclarationUsesTheProtectedDefaultsOnly(t *testing.T) {
	s := canonicalScenario()
	s.head = repo{"go.mod": goMod, "lib.go": "package m\n", "extra_test.go": "package m\n\nimport \"testing\"\n\nfunc TestExtra(t *testing.T) {}\n"}
	s.manifest = repo{"lib_test.go": libTests}
	rt := newRuntime(func(c call) (sandbox.Outcome, error) { return goTest(c, pass), nil })
	d := start(t, newService(t, s.serve(1)), rt)
	assertBody(t, d.result().body, `{
		"status": "complete", "head": {"sha": "`+headSHA+`"}, `+commonExecutor+`, `+commonSandbox+`,
		"tests": [
			{"package": ".", "name": "TestA", "file": "lib_test.go", "kind": "declared", "head": ["pass"]},
			{"package": ".", "name": "TestB", "file": "lib_test.go", "kind": "declared", "head": ["pass"]}
		]
	}`)
	if _, ok := rt.invocations()[0].Files["src/extra_test.go"]; !ok {
		t.Error("the head's test outside the protected defaults was removed")
	}
}
