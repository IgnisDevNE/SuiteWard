package goprofile

import (
	"maps"
	"path"
	"slices"
	"testing"
	"time"
)

const header = "package p\n\nimport \"testing\"\n\n"

// The sources of these tests are strings: no path of them exists on any disk, so a result that depends on the host's
// files or on its path separator cannot pass.
func src(body string) []byte { return []byte(header + body) }

func constrained(constraint, body string) []byte {
	return []byte("//go:build " + constraint + "\n\n" + header + body)
}

func idOf(file, name string) TestID { return TestID{Package: path.Dir(file), Name: name, File: file} }

func sameIDs(t *testing.T, what string, got, want []TestID) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s:\n got %v\nwant %v", what, got, want)
	}
}

func excludedIDs(excluded []ExcludedTest) []TestID {
	var ids []TestID
	for _, e := range excluded {
		ids = append(ids, e.TestID)
	}
	return ids
}

func reasons(excluded []ExcludedTest) map[string]string {
	out := map[string]string{}
	for _, e := range excluded {
		out[e.Name] = e.Reason
	}
	return out
}

func TestDeclaredTestsAreTheTopLevelFunctionsOfTheTestingSignature(t *testing.T) {
	declared, err := DeclaredTests(map[string][]byte{"a/sig_test.go": src(`
func TestOne(t *testing.T) {}
func TestTwo(t *testing.T) {}
func Test(t *testing.T) {}
func Test_underscore(t *testing.T) {}
func TestUnnamed(*testing.T) {}
func TestBlank(_ *testing.T) {}
func TestMainLike(t *testing.T) {}
func TestÉcole(t *testing.T) {}

func Testlower(t *testing.T) {}
func Testécole(t *testing.T) {}
func TestMain(m *testing.M) {}
func BenchmarkB(b *testing.B) {}
func FuzzF(f *testing.F) {}
func ExampleE() {}
func (s suite) TestMethod(t *testing.T) {}
func TestBench(b *testing.B) {}
func TestNoParam() {}
func TestResult(t *testing.T) int { return 0 }
func TestTwoParams(t *testing.T, n int) {}
func TestByValue(t testing.T) {}
func TestSlice(t []*testing.T) {}
func TestGeneric[T any](t *testing.T) {}

type suite struct{}
`)}, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	const file = "a/sig_test.go"
	sameIDs(t, "tests", declared.Tests, []TestID{
		idOf(file, "Test"), idOf(file, "TestBlank"), idOf(file, "TestMainLike"), idOf(file, "TestOne"), idOf(file, "TestTwo"),
		idOf(file, "TestUnnamed"), idOf(file, "Test_underscore"), idOf(file, "TestÉcole"),
	})
	sameIDs(t, "excluded", excludedIDs(declared.Excluded), nil)
}

func TestDeclaredTestsReadTheSignatureThroughTheImportName(t *testing.T) {
	declared, err := DeclaredTests(map[string][]byte{
		"p/alias_test.go": []byte("package p\n\nimport tt \"testing\"\n\nfunc TestAliased(t *tt.T) {}\n\nfunc TestPlain(t *testing.T) {}\n"),
		"p/dot_test.go":   []byte("package p\n\nimport . \"testing\"\n\nfunc TestDot(t *T) {}\n"),
		"p/other_test.go": []byte("package p\n\nimport testing \"example.com/other\"\n\nfunc TestOther(t *testing.T) {}\n"),
	}, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	// TestPlain names a package that its file does not import, and TestOther one that is not the standard library's.
	sameIDs(t, "tests", declared.Tests, []TestID{idOf("p/alias_test.go", "TestAliased"), idOf("p/dot_test.go", "TestDot")})
}

func TestDeclaredTestsFollowTheBuildContextOfTheSandbox(t *testing.T) {
	files := map[string][]byte{
		"x/plain_test.go":       src("func TestPlain(t *testing.T) {}\n"),
		"x/linux_linux_test.go": src("func TestLinux(t *testing.T) {}\n"),
		"x/win_windows_test.go": src("func TestWindows(t *testing.T) {}\n"),
		"x/mac_darwin_test.go":  src("func TestDarwin(t *testing.T) {}\n"),
		"x/amd_amd64_test.go":   src("func TestAmd64(t *testing.T) {}\n"),
		"x/arm_arm64_test.go":   src("func TestArm64(t *testing.T) {}\n"),
		"x/integration_test.go": constrained("integration", "func TestIntegration(t *testing.T) {}\n"),
		"x/old_style_test.go":   []byte("// +build integration\n\n" + header + "func TestOldStyle(t *testing.T) {}\n"),
		"x/ignore_test.go":      constrained("ignore", "func TestIgnore(t *testing.T) {}\n"),
		"x/cgo_test.go":         constrained("cgo", "func TestCgo(t *testing.T) {}\n"),
		"x/nocgo_test.go":       constrained("!cgo", "func TestNoCgo(t *testing.T) {}\n"),
		"x/unix_test.go":        constrained("unix && !windows", "func TestUnix(t *testing.T) {}\n"),
		"x/release_test.go":     constrained("go1.21", "func TestRelease(t *testing.T) {}\n"),
		"x/future_test.go":      constrained("go99.0", "func TestFuture(t *testing.T) {}\n"),
		"x/_hidden_test.go":     src("func TestHidden(t *testing.T) {}\n"),
	}
	names := func(ids []TestID) []string {
		var out []string
		for _, id := range ids {
			out = append(out, id.Name)
		}
		return out
	}
	always := []string{"TestLinux", "TestNoCgo", "TestPlain", "TestRelease", "TestUnix"}
	never := []string{"TestCgo", "TestDarwin", "TestFuture", "TestHidden", "TestIgnore", "TestIntegration", "TestOldStyle", "TestWindows"}
	for goarch, arch := range map[string][2]string{"amd64": {"TestAmd64", "TestArm64"}, "arm64": {"TestArm64", "TestAmd64"}} {
		declared, err := DeclaredTests(files, goarch)
		if err != nil {
			t.Fatal(goarch, err)
		}
		if got, want := names(declared.Tests), slices.Sorted(slices.Values(append([]string{arch[0]}, always...))); !slices.Equal(got, want) {
			t.Errorf("%s tests: got %v want %v", goarch, got, want)
		}
		if got, want := names(excludedIDs(declared.Excluded)), slices.Sorted(slices.Values(append([]string{arch[1]}, never...))); !slices.Equal(got, want) {
			t.Errorf("%s excluded: got %v want %v", goarch, got, want)
		}
		for name, reason := range reasons(declared.Excluded) {
			if reason != ReasonBuildConstraint {
				t.Errorf("%s: %s is excluded for %q", goarch, name, reason)
			}
		}
	}
}

func TestDeclaredTestsListANameOnce(t *testing.T) {
	declared, err := DeclaredTests(map[string][]byte{
		// The same test for two systems: it is runnable here, so it is not excluded because of the other file.
		"p/path_linux_test.go":   src("func TestPath(t *testing.T) {}\n"),
		"p/path_windows_test.go": src("func TestPath(t *testing.T) {}\n"),
		// An internal and an external test package of one directory may both declare a name.
		"q/dup_test.go":   src("func TestDup(t *testing.T) {}\n"),
		"q/dup_x_test.go": []byte("package p_test\n\nimport \"testing\"\n\nfunc TestDup(t *testing.T) {}\n"),
		// Excluded in two files: one entry.
		"r/e_windows_test.go": src("func TestElsewhere(t *testing.T) {}\n"),
		"r/e_darwin_test.go":  src("func TestElsewhere(t *testing.T) {}\n"),
	}, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	sameIDs(t, "tests", declared.Tests, []TestID{idOf("p/path_linux_test.go", "TestPath"), idOf("q/dup_test.go", "TestDup")})
	sameIDs(t, "excluded", excludedIDs(declared.Excluded), []TestID{idOf("r/e_darwin_test.go", "TestElsewhere")})
}

func TestDeclaredTestsRefuseAFileThatDoesNotParse(t *testing.T) {
	good := src("func TestGood(t *testing.T) {}\n")
	if _, err := DeclaredTests(map[string][]byte{"a/good_test.go": good}, "amd64"); err != nil {
		t.Fatal("a file that parses is an error:", err)
	}
	for name, bad := range map[string][]byte{
		"a syntax error":    src("func TestBad( {\n"),
		"no package clause": []byte("func TestBad(t *testing.T) {}\n"),
	} {
		// The second file is one that the build context leaves out: its tests are listed, so it has to be read.
		for _, file := range []string{"a/bad_test.go", "a/bad_windows_test.go"} {
			if _, err := DeclaredTests(map[string][]byte{"a/good_test.go": good, file: bad}, "amd64"); err == nil {
				t.Errorf("%s in %s was not an error", name, file)
			}
		}
	}
}

func TestDeclaredTestsRefuseABrokenBuildConstraint(t *testing.T) {
	if _, err := DeclaredTests(map[string][]byte{"a/ok_test.go": constrained("integration && unix", "func TestOK(t *testing.T) {}\n")}, "amd64"); err != nil {
		t.Fatal("a constraint that parses is an error:", err)
	}
	files := map[string][]byte{"a/bad_test.go": constrained("integration &&", "func TestBad(t *testing.T) {}\n")}
	if _, err := DeclaredTests(files, "amd64"); err == nil {
		t.Error("a constraint that does not parse was not an error")
	}
}

func TestDeclaredTestsSkipWhatGoDoesNotBuild(t *testing.T) {
	declared, err := DeclaredTests(map[string][]byte{
		"testdata/a_test.go":         src("func TestFixture(t *testing.T) {}\n"),
		"a/testdata/b/b_test.go":     src("func TestDeepFixture(t *testing.T) {}\n"),
		"vendor/m/m_test.go":         src("func TestVendored(t *testing.T) {}\n"),
		"_tools/t_test.go":           src("func TestUnderscoreDir(t *testing.T) {}\n"),
		"a/.hidden/h_test.go":        src("func TestDotDir(t *testing.T) {}\n"),
		"a/helper.go":                src("func TestNotATestFile(t *testing.T) {}\n"),
		"a/README.md":                []byte("func TestInProse(t *testing.T) {}"),
		"a/testdata_helpers_test.go": src("func TestRealOne(t *testing.T) {}\n"),
	}, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	sameIDs(t, "tests", declared.Tests, []TestID{idOf("a/testdata_helpers_test.go", "TestRealOne")})
	sameIDs(t, "excluded", excludedIDs(declared.Excluded), nil)
}

func TestDeclaredTestsNamePackagesByDirectoryFromTheRoot(t *testing.T) {
	declared, err := DeclaredTests(map[string][]byte{
		"root_test.go":       src("func TestRoot(t *testing.T) {}\n"),
		"a/b/c/deep_test.go": src("func TestDeep(t *testing.T) {}\n"),
	}, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	sameIDs(t, "tests", declared.Tests, []TestID{
		{Package: ".", Name: "TestRoot", File: "root_test.go"},
		{Package: "a/b/c", Name: "TestDeep", File: "a/b/c/deep_test.go"},
	})
}

func TestDeclaredTestsOfNoFilesAreNone(t *testing.T) {
	declared, err := DeclaredTests(nil, "amd64")
	if err != nil || len(declared.Tests) != 0 || len(declared.Excluded) != 0 {
		t.Fatalf("got %v, %v", declared, err)
	}
}

func TestChangedTests(t *testing.T) {
	one := func(file, body string) map[string][]byte { return map[string][]byte{file: src(body)} }
	const a = "a/a_test.go"
	cases := map[string]struct {
		head, canonical map[string][]byte
		added, changed  []TestID
	}{
		"identical": {
			head: one(a, "func TestA(t *testing.T) {}\n"), canonical: one(a, "func TestA(t *testing.T) {}\n"),
		},
		"a new function in a file that exists": {
			head:      one(a, "func TestOld(t *testing.T) {}\n\nfunc TestNew(t *testing.T) {}\n"),
			canonical: one(a, "func TestOld(t *testing.T) {}\n"),
			added:     []TestID{idOf(a, "TestNew")},
		},
		"a different body": {
			head:      one(a, "func TestA(t *testing.T) { t.Log(1) }\n"),
			canonical: one(a, "func TestA(t *testing.T) { t.Log(2) }\n"),
			changed:   []TestID{idOf(a, "TestA")},
		},
		"a comment inside the body": {
			head:      one(a, "func TestA(t *testing.T) {\n\t// why\n}\n"),
			canonical: one(a, "func TestA(t *testing.T) {\n}\n"),
			changed:   []TestID{idOf(a, "TestA")},
		},
		"the text of the declaration, not its meaning": {
			head: one(a, "func TestA(t *testing.T) { }\n"), canonical: one(a, "func TestA(t *testing.T) {}\n"),
			changed: []TestID{idOf(a, "TestA")},
		},
		"a doc comment above the function": {
			head: one(a, "// TestA checks a.\nfunc TestA(t *testing.T) {}\n"), canonical: one(a, "func TestA(t *testing.T) {}\n"),
		},
		"text between the functions": {
			head:      one(a, "func helper() {}\n\nfunc TestA(t *testing.T) {}\n\n\n"),
			canonical: one(a, "func TestA(t *testing.T) {}\n"),
		},
		"a file that only the head has": {
			head:      map[string][]byte{"b/new_test.go": src("func TestN2(t *testing.T) {}\nfunc TestN1(t *testing.T) {}\n")},
			canonical: nil,
			added:     []TestID{idOf("b/new_test.go", "TestN1"), idOf("b/new_test.go", "TestN2")},
		},
		"a function that the head removed": {
			head:      one(a, "func TestA(t *testing.T) {}\n"),
			canonical: one(a, "func TestA(t *testing.T) {}\n\nfunc TestB(t *testing.T) {}\n"),
		},
		"a function that moved to another file of its package": {
			head:      map[string][]byte{"a/moved_test.go": src("func TestA(t *testing.T) {}\n")},
			canonical: one(a, "func TestA(t *testing.T) {}\n"),
		},
		"the same name in another package is another test": {
			head:      map[string][]byte{a: src("func TestShared(t *testing.T) {}\n"), "b/b_test.go": src("func TestShared(t *testing.T) {}\n")},
			canonical: one(a, "func TestShared(t *testing.T) {}\n"),
			added:     []TestID{idOf("b/b_test.go", "TestShared")},
		},
		"a function that stops being a test": {
			head:      one(a, "func TestA(t *testing.B) {}\n"),
			canonical: one(a, "func TestA(t *testing.T) {}\n"),
		},
		"a helper that becomes a test": {
			head:      one(a, "func TestA(t *testing.T) {}\n"),
			canonical: one(a, "func TestA(t *testing.T, n int) {}\n"),
			added:     []TestID{idOf(a, "TestA")},
		},
		// go test -run selects by name alone, so both tests of that name run, and a change of either is a change.
		"the same name in the internal and the external test package": {
			head: map[string][]byte{
				"a/dup_test.go":   src("func TestDup(t *testing.T) {}\n"),
				"a/dup_x_test.go": []byte("package p_test\n\nimport \"testing\"\n\nfunc TestDup(t *testing.T) { t.Log(1) }\n"),
			},
			canonical: map[string][]byte{
				"a/dup_test.go":   src("func TestDup(t *testing.T) {}\n"),
				"a/dup_x_test.go": []byte("package p_test\n\nimport \"testing\"\n\nfunc TestDup(t *testing.T) {}\n"),
			},
			changed: []TestID{idOf("a/dup_test.go", "TestDup")},
		},
		// The file is excluded by its constraint, so the sandbox cannot run it: the caller lists the test as excluded
		// by looking it up in DeclaredTests, so it has to be here.
		"a test in a file that the build context leaves out": {
			head:  map[string][]byte{"a/int_test.go": constrained("integration", "func TestInt(t *testing.T) {}\n")},
			added: []TestID{idOf("a/int_test.go", "TestInt")},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			added, changed, err := ChangedTests(c.head, c.canonical, "amd64")
			if err != nil {
				t.Fatal(err)
			}
			sameIDs(t, "added", added, c.added)
			sameIDs(t, "changed", changed, c.changed)
		})
	}
}

func TestChangedTestsRefuseAFileThatDoesNotParse(t *testing.T) {
	good := map[string][]byte{"a/a_test.go": src("func TestA(t *testing.T) {}\n")}
	bad := map[string][]byte{"a/a_test.go": src("func TestA( {\n")}
	if _, _, err := ChangedTests(good, good, "amd64"); err != nil {
		t.Fatal("files that parse are an error:", err)
	}
	if _, _, err := ChangedTests(bad, good, "amd64"); err == nil {
		t.Error("a head that does not parse was not an error")
	}
	if _, _, err := ChangedTests(good, bad, "amd64"); err == nil {
		t.Error("a canonical that does not parse was not an error")
	}
}

// A directory with a go.mod of its own is another module: the invocation of the root module cannot name its packages,
// so its tests are excluded, whatever else leaves them out. The go.mod files of the tree are among files.
func TestDeclaredTestsExcludeWhatLivesUnderANestedModule(t *testing.T) {
	files := map[string][]byte{
		"go.mod":                    []byte("module example.com/root\n"),
		"root_test.go":              src("func TestRoot(t *testing.T) {}\n"),
		"a/a_test.go":               src("func TestA(t *testing.T) {}\n"),
		"a/go.mod.txt":              []byte("not a module file"),
		"pkg/vgo.mod":               []byte("not a module file"),
		"pkg/p_test.go":             src("func TestPkg(t *testing.T) {}\n"),
		"a/b/go.mod":                []byte("module example.com/b\n"),
		"a/b/b_test.go":             src("func TestB(t *testing.T) {}\n"),
		"nested/go.mod":             []byte("module example.com/nested\n"),
		"nested/n_test.go":          src("func TestN(t *testing.T) {}\n"),
		"nested/deep/er/d_test.go":  src("func TestDeep(t *testing.T) {}\n"),
		"nested/w_windows_test.go":  src("func TestNestedAndWindows(t *testing.T) {}\n"),
		"other/o_windows_test.go":   src("func TestWindowsOnly(t *testing.T) {}\n"),
		"nested/testdata/t_test.go": src("func TestFixture(t *testing.T) {}\n"),
		"nested/vendor/v/v_test.go": src("func TestVendored(t *testing.T) {}\n"),
	}
	declared, err := DeclaredTests(files, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	sameIDs(t, "tests", declared.Tests, []TestID{
		idOf("root_test.go", "TestRoot"), idOf("a/a_test.go", "TestA"), idOf("pkg/p_test.go", "TestPkg"),
	})
	want := map[string]string{
		"TestB": ReasonNestedModule, "TestN": ReasonNestedModule, "TestDeep": ReasonNestedModule,
		"TestNestedAndWindows": ReasonNestedModule, "TestWindowsOnly": ReasonBuildConstraint,
	}
	if got := reasons(declared.Excluded); !maps.Equal(got, want) {
		t.Errorf("excluded:\n got %v\nwant %v", got, want)
	}
	// The files of a nested module's testdata and vendor are no test of any module.
	for _, e := range declared.Excluded {
		if e.Name == "TestFixture" || e.Name == "TestVendored" {
			t.Errorf("%s is listed", e.Name)
		}
	}
}

// The tests that are run are the tests that are declared and not excluded: no package of a nested module reaches a
// go test invocation.
func TestNoPackageOfANestedModuleIsNamedInTheInvocation(t *testing.T) {
	declared, err := DeclaredTests(map[string][]byte{
		"a/a_test.go":      src("func TestA(t *testing.T) {}\n"),
		"nested/go.mod":    []byte("module example.com/nested\n"),
		"nested/n_test.go": src("func TestN(t *testing.T) {}\n"),
	}, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	argv, _ := Command(declared.Tests, time.Minute, false)
	if !slices.Contains(argv, "./a") || slices.Contains(argv, "./nested") {
		t.Errorf("packages in %q", argv)
	}
	if !slices.Contains(argv, "^(TestA)$") {
		t.Errorf("-run in %q", argv)
	}
}

// ChangedTests lists the functions of excluded files too, a nested module's among them: the caller finds the reason in
// DeclaredTests. A test whose module becomes nested in the head is the same text, so it is not changed.
func TestChangedTestsSeeANestedModule(t *testing.T) {
	canonical := map[string][]byte{"go.mod": []byte("module example.com/root\n"), "a/a_test.go": src("func TestA(t *testing.T) {}\n")}
	head := map[string][]byte{
		"go.mod":           []byte("module example.com/root\n"),
		"a/go.mod":         []byte("module example.com/a\n"),
		"a/a_test.go":      src("func TestA(t *testing.T) {}\n"),
		"n/go.mod":         []byte("module example.com/n\n"),
		"n/n_test.go":      src("func TestN(t *testing.T) {}\n"),
		"n/sub/s_test.go":  src("func TestSub(t *testing.T) {}\n"),
		"root_new_test.go": src("func TestNew(t *testing.T) {}\n"),
	}
	added, changed, err := ChangedTests(head, canonical, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	sameIDs(t, "added", added, []TestID{idOf("root_new_test.go", "TestNew"), idOf("n/n_test.go", "TestN"), idOf("n/sub/s_test.go", "TestSub")})
	sameIDs(t, "changed", changed, nil)
	declared, err := DeclaredTests(head, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := reasons(declared.Excluded), map[string]string{"TestA": ReasonNestedModule, "TestN": ReasonNestedModule, "TestSub": ReasonNestedModule}; !maps.Equal(got, want) {
		t.Errorf("excluded: got %v want %v", got, want)
	}
}
