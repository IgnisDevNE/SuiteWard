package goprofile

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCommandIsOneGoTestOfTheNamedTestsInTheirPackages(t *testing.T) {
	tests := []TestID{
		{"b/c", "TestB", "b/c/b_test.go"},
		{".", "TestRoot", "root_test.go"},
		{"a", "TestAA", "a/a_test.go"},
		{"a", "TestA", "a/a_test.go"},
		{"a", "TestA", "a/a_x_test.go"},
	}
	argv, _ := Command(tests, 10*time.Minute, false)
	want := []string{
		"go", "test", "-json", "-count=1", "-shuffle=on", "-timeout=10m0s",
		"-run", "^(TestA|TestAA|TestB|TestRoot)$",
		".", "./a", "./b/c",
	}
	if !slices.Equal(argv, want) {
		t.Errorf("got  %q\nwant %q", argv, want)
	}
}

// -run is a regular expression and argv is no shell: the pattern holds no quotes of its own, and a name is quoted for
// the regular expression whatever it holds.
func TestCommandAnchorsAndEscapesTheRunPattern(t *testing.T) {
	argv, _ := Command([]TestID{{"a", "Test(x)", "a/a_test.go"}, {"a", "TestA", "a/a_test.go"}}, time.Second, false)
	run := slices.Index(argv, "-run")
	if run < 0 || run+1 == len(argv) {
		t.Fatalf("no -run pattern in %q", argv)
	}
	if got, want := argv[run+1], `^(Test\(x\)|TestA)$`; got != want {
		t.Errorf("-run %q, want %q", got, want)
	}
	if !slices.Contains(argv, "-timeout=1s") {
		t.Errorf("no -timeout=1s in %q", argv)
	}
}

// A package whose name starts with a dash must not read as a flag.
func TestCommandPrefixesEveryPackageWithADot(t *testing.T) {
	argv, _ := Command([]TestID{{"-weird", "TestA", "-weird/a_test.go"}}, time.Minute, false)
	if !slices.Contains(argv, "./-weird") || slices.Contains(argv, "-weird") {
		t.Errorf("packages in %q", argv)
	}
}

func TestCommandEnvironmentIsExactlyTheSandboxs(t *testing.T) {
	// Nothing of the executor's own environment reaches the sandbox.
	t.Setenv("CI", "true")
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOPROXY", "https://example.invalid")
	tests := []TestID{{"a", "TestA", "a/a_test.go"}}
	common := []string{
		"GOENV=off",
		"GOTOOLCHAIN=local",
		"GOWORK=off",
		"GOFLAGS=",
		"GOSUMDB=off",
		"HOME=/work/.suiteward/home",
		"TMPDIR=/work/.suiteward/tmp",
		"GOCACHE=/work/.suiteward/gocache",
		"GOMODCACHE=/work/.suiteward/gomodcache",
		"PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	for vendor, proxy := range map[bool]string{false: "GOPROXY=file:///work/.suiteward/proxy", true: "GOPROXY=off"} {
		_, env := Command(tests, time.Minute, vendor)
		want := slices.Sorted(slices.Values(append([]string{proxy}, common...)))
		if got := slices.Sorted(slices.Values(env)); !slices.Equal(got, want) {
			t.Errorf("vendor=%v:\n got %q\nwant %q", vendor, got, want)
		}
	}
}

// The files that the prefetch and the source stream put in the tar are where go looks for them.
func TestCommandEnvironmentPointsAtTheDirectoriesTheCallerFills(t *testing.T) {
	_, env := Command([]TestID{{"a", "TestA", "a/a_test.go"}}, time.Minute, false)
	for key, dir := range map[string]string{"GOPROXY": "file:///work/" + ProxyDir, "HOME": "/work/" + HomeDir, "TMPDIR": "/work/" + TmpDir} {
		if !slices.Contains(env, key+"="+dir) {
			t.Errorf("%s is not %s in %q", key, dir, env)
		}
	}
	for _, dir := range []string{ProxyDir, HomeDir, TmpDir} {
		if strings.HasPrefix(dir, "/") || strings.Contains(dir, "..") {
			t.Errorf("%q is not a relative tar name", dir)
		}
	}
}

// The tree is extracted into /work/<TreeDir> and go's own state is under /work/<StateDir>: neither is inside the other,
// so no path of the tree can be a file that go reads as its own (an env file, a cache entry, a module proxy file).
func TestTheTreeAndGosStateAreDisjoint(t *testing.T) {
	inside := func(dir, parent string) bool { return strings.HasPrefix(dir+"/", parent+"/") }
	if TreeDir == "" || StateDir == "" || inside(TreeDir, StateDir) || inside(StateDir, TreeDir) {
		t.Errorf("the tree %q and go's state %q overlap", TreeDir, StateDir)
	}
	for _, dir := range []string{ProxyDir, HomeDir, TmpDir} {
		if !inside(dir, StateDir) {
			t.Errorf("%q is not under %q", dir, StateDir)
		}
	}
}
