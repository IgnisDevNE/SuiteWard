package prefetch

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

const goModHead = "module example.com/app\n\ngo 1.21\n"

// goSumOf writes go.sum lines from "path version" entries. A version that ends in /go.mod is the go.mod line of that version.
func goSumOf(entries ...string) []byte {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e + " h1:placeholder\n")
	}
	return []byte(b.String())
}

func goModOf(directives ...string) []byte {
	return []byte(goModHead + strings.Join(directives, "\n") + "\n")
}

func namesOf(p Plan) []string {
	var names []string
	for _, f := range p.Files {
		names = append(names, f.Name)
	}
	return names
}

func wantNames(t *testing.T, p Plan, want ...string) {
	t.Helper()
	if got := namesOf(p); !slices.Equal(got, want) {
		t.Errorf("the plan names\n%q\nwant\n%q", got, want)
	}
}

var anyLimits = Limits{Modules: 100, Bytes: 1 << 20}

func TestPlanSidePlansTheModOfEveryGoModLineAndTheZipOfEveryOtherLine(t *testing.T) {
	sum := goSumOf(
		"github.com/BurntSushi/toml v1.2.0",
		"github.com/BurntSushi/toml v1.2.0/go.mod",
		"example.com/zip-only v2.0.0+incompatible",
		"example.com/mod-only v0.0.0-20200101000000-abcdefabcdef/go.mod",
		"example.com/rc v1.0.0-RC1",
		"example.com/rc v1.0.0-RC1/go.mod",
		"github.com/BurntSushi/toml v1.2.0", // a repeated line plans nothing twice
	)
	plan, err := PlanSide(goModOf(), sum, false, anyLimits)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Vendor {
		t.Error("a side with no vendor/modules.txt does not vendor")
	}
	// Sorted, and escaped as the module proxy protocol says: capitals of the path and of the version become !lowercase.
	wantNames(t, plan,
		"example.com/mod-only/@v/v0.0.0-20200101000000-abcdefabcdef.mod",
		"example.com/rc/@v/v1.0.0-!r!c1.mod",
		"example.com/rc/@v/v1.0.0-!r!c1.zip",
		"example.com/zip-only/@v/v2.0.0+incompatible.zip",
		"github.com/!burnt!sushi/toml/@v/v1.2.0.mod",
		"github.com/!burnt!sushi/toml/@v/v1.2.0.zip",
	)
}

func TestPlanSidePlansNothingForAGoSumWithNoModules(t *testing.T) {
	plan, err := PlanSide(goModOf(), nil, false, anyLimits)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Vendor || len(plan.Files) != 0 {
		t.Errorf("an empty go.sum plans %+v, want nothing", plan)
	}
}

func TestPlanSideRefusesWhatCannotBecomeAProxyPath(t *testing.T) {
	cases := []struct{ name, line string }{
		{"a path whose first element has no dot", "example v1.0.0"},
		{"a capital in the domain", "Example.com/x v1.0.0"},
		{"a parent directory", "../evil.example/x v1.0.0"},
		{"a parent directory inside the path", "example.com/../x v1.0.0"},
		{"a query character", "example.com/x?y=1 v1.0.0"},
		{"a fragment character", "example.com/x#frag v1.0.0"},
		{"an absolute path", "/example.com/x v1.0.0"},
		{"a backslash", `example.com\x v1.0.0`},
		{"a percent escape", "example.com/%2e%2e/x v1.0.0"},
		{"a version without the v", "example.com/x 1.0.0"},
		{"a version that is not canonical", "example.com/x v1.0"},
		{"a branch name", "example.com/x master"},
		{"build metadata other than incompatible", "example.com/x v1.0.0+meta"},
		{"a version with a path in it", "example.com/x v1.0.0/../../y"},
		{"a version with a query", "example.com/x v1.0.0?x=1"},
		{"a version of another major than the path", "example.com/x/v2 v1.0.0"},
		{"a go.mod suffix on nothing", "example.com/x /go.mod"},
		{"a field longer than any module path", strings.Repeat("a", 2000) + ".example/x v1.0.0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan, err := PlanSide(goModOf(), goSumOf("example.com/fine v1.0.0", c.line), false, anyLimits)
			if !errors.Is(err, ErrInstallFailed) {
				t.Errorf("a go.sum with %q: err = %v, want ErrInstallFailed", c.line, err)
			}
			if len(plan.Files) != 0 {
				t.Errorf("a refused plan still names files: %q", namesOf(plan))
			}
		})
	}
}

func TestPlanSideRefusesAGoSumLineThatIsNotThreeFields(t *testing.T) {
	for name, sum := range map[string]string{
		"two fields":  "example.com/x v1.0.0\n",
		"four fields": "example.com/x v1.0.0 h1:placeholder extra\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := PlanSide(goModOf(), []byte(sum), false, anyLimits); !errors.Is(err, ErrInstallFailed) {
				t.Errorf("err = %v, want ErrInstallFailed", err)
			}
		})
	}
}

func TestPlanSideRefusesAGoModThatDoesNotParse(t *testing.T) {
	for name, goMod := range map[string]string{
		"not a go.mod":      "this is not a go.mod\n",
		"an unknown verb":   goModHead + "\nfrobnicate example.com/x\n",
		"a bad go version":  "module example.com/app\n\ngo banana\n",
		"a bad replace arm": goModHead + "\nreplace example.com/x v1.0.0 =>\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := PlanSide([]byte(goMod), goSumOf("example.com/x v1.0.0"), false, anyLimits); !errors.Is(err, ErrInstallFailed) {
				t.Errorf("err = %v, want ErrInstallFailed", err)
			}
		})
	}
}

// The bound counts modules (the contract's limits.prefetchModules), not lines or files: a module with a go.mod line and a zip line counts once.
func TestPlanSideBoundsTheNumberOfModulesNotOfLinesOrFiles(t *testing.T) {
	two := goSumOf(
		"example.com/a v1.0.0", "example.com/a v1.0.0/go.mod",
		"example.com/b v1.0.0", "example.com/b v1.0.0/go.mod",
	)
	if plan, err := PlanSide(goModOf(), two, false, Limits{Modules: 2}); err != nil || len(plan.Files) != 4 {
		t.Errorf("two modules under a bound of 2: err = %v, files = %q; want 4 files", err, namesOf(plan))
	}
	three := append(slices.Clone(two), goSumOf("example.com/c v1.0.0/go.mod")...)
	plan, err := PlanSide(goModOf(), three, false, Limits{Modules: 2})
	if !errors.Is(err, ErrInstallFailed) {
		t.Errorf("three modules under a bound of 2: err = %v, want ErrInstallFailed", err)
	}
	if len(plan.Files) != 0 {
		t.Errorf("a refused plan still names files: %q", namesOf(plan))
	}
	// The same module at two versions is two modules.
	versions := goSumOf("example.com/a v1.0.0", "example.com/a v1.1.0")
	if _, err := PlanSide(goModOf(), versions, false, Limits{Modules: 1}); !errors.Is(err, ErrInstallFailed) {
		t.Errorf("one module path at two versions under a bound of 1: err = %v, want ErrInstallFailed", err)
	}
}

func TestPlanSideRefusesAReplaceToADirectoryOutsideTheTree(t *testing.T) {
	for name, replace := range map[string]string{
		"a parent directory":                  "replace example.com/x => ../outside",
		"the parent directory itself":         "replace example.com/x => ..",
		"an absolute directory":               "replace example.com/x => /srv/outside",
		"a way out after the cleaning":        "replace example.com/x => ./a/../../outside",
		"a Windows directory":                 `replace example.com/x => .\dir`,
		"a Windows drive":                     `replace example.com/x => C:\dir`,
		"a versioned old side":                "replace example.com/x v1.0.0 => ../outside",
		"one of several in a block":           "replace (\n\texample.com/y => ./inside\n\texample.com/x => ../outside\n)",
		"an outside replace by an inside one": "replace example.com/x => ../outside\nreplace example.com/y => ./inside",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := PlanSide(goModOf(replace), goSumOf("example.com/z v1.0.0"), false, anyLimits); !errors.Is(err, ErrInstallFailed) {
				t.Errorf("err = %v, want ErrInstallFailed", err)
			}
		})
	}
}

// A directory inside the tree needs no fetch: its go.sum lines (if any are left) are not planned, but the other modules are.
func TestPlanSideNeedsNoFetchForAReplaceToADirectoryInsideTheTree(t *testing.T) {
	sum := goSumOf(
		"example.com/x v1.0.0", "example.com/x v1.0.0/go.mod",
		"example.com/x v1.1.0",
		"example.com/y v1.0.0", "example.com/y v1.0.0/go.mod",
		"example.com/z v1.0.0",
	)
	for name, tc := range map[string]struct {
		replace string
		want    []string
	}{
		"every version of x": {
			"replace example.com/x => ./local",
			[]string{"example.com/y/@v/v1.0.0.mod", "example.com/y/@v/v1.0.0.zip", "example.com/z/@v/v1.0.0.zip"},
		},
		"a path that stays inside after the cleaning": {
			"replace example.com/x => ./a/../b",
			[]string{"example.com/y/@v/v1.0.0.mod", "example.com/y/@v/v1.0.0.zip", "example.com/z/@v/v1.0.0.zip"},
		},
		"only the version the replace names": {
			"replace example.com/x v1.0.0 => ./local",
			[]string{
				"example.com/x/@v/v1.1.0.zip",
				"example.com/y/@v/v1.0.0.mod", "example.com/y/@v/v1.0.0.zip", "example.com/z/@v/v1.0.0.zip",
			},
		},
		"a block": {
			"replace (\n\texample.com/x => ./local\n)",
			[]string{"example.com/y/@v/v1.0.0.mod", "example.com/y/@v/v1.0.0.zip", "example.com/z/@v/v1.0.0.zip"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			plan, err := PlanSide(goModOf(tc.replace), sum, false, anyLimits)
			if err != nil {
				t.Fatal(err)
			}
			wantNames(t, plan, tc.want...)
		})
	}
}

// A replace to another module version is fetched like any module: its go.sum lines are the ones that name it. The module it replaces is
// never asked of the proxy.
func TestPlanSidePlansAReplaceToAnotherModuleVersionLikeAnyModule(t *testing.T) {
	sum := goSumOf(
		"example.com/fork v1.2.3", "example.com/fork v1.2.3/go.mod",
		"example.com/x v1.0.0/go.mod",
	)
	plan, err := PlanSide(goModOf("replace example.com/x v1.0.0 => example.com/fork v1.2.3"), sum, false, anyLimits)
	if err != nil {
		t.Fatal(err)
	}
	wantNames(t, plan, "example.com/fork/@v/v1.2.3.mod", "example.com/fork/@v/v1.2.3.zip")
}

func TestPlanSideVendorsWhenTheGoDirectiveIsAtLeast114AndVendorModulesTxtExists(t *testing.T) {
	sum := goSumOf("example.com/x v1.0.0", "example.com/x v1.0.0/go.mod")
	for name, tc := range map[string]struct {
		goDirective string
		vendor      bool
		wantVendor  bool
	}{
		"go 1.14 with vendor/modules.txt":     {"go 1.14", true, true},
		"go 1.21 with vendor/modules.txt":     {"go 1.21", true, true},
		"go 1.27.3 with vendor/modules.txt":   {"go 1.27.3", true, true},
		"go 1.13 with vendor/modules.txt":     {"go 1.13", true, false},
		"go 1.9 with vendor/modules.txt":      {"go 1.9", true, false},
		"go 1.21 without vendor/modules.txt":  {"go 1.21", false, false},
		"no go directive with vendor/modules": {"", true, false}, // go does not use vendor/ for a go.mod that names no version
	} {
		t.Run(name, func(t *testing.T) {
			goMod := "module example.com/app\n\n" + tc.goDirective + "\n"
			plan, err := PlanSide([]byte(goMod), sum, tc.vendor, anyLimits)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Vendor != tc.wantVendor {
				t.Errorf("Vendor = %v, want %v", plan.Vendor, tc.wantVendor)
			}
			if tc.wantVendor && len(plan.Files) != 0 {
				t.Errorf("a vendored side fetches nothing, the plan names %q", namesOf(plan))
			}
			if !tc.wantVendor && len(plan.Files) != 3 {
				t.Errorf("a side that does not vendor plans its go.sum, the plan names %q", namesOf(plan))
			}
		})
	}
}

// Nothing is fetched for a vendored side, so nothing of its go.sum or of a replace is used and none of it is refused.
func TestPlanSideDoesNotReadTheGoSumOrTheReplacesOfAVendoredSide(t *testing.T) {
	goMod := goModOf("replace example.com/x => ../outside")
	plan, err := PlanSide(goMod, []byte("not a go.sum line\n../evil v1\n"), true, Limits{Modules: 0})
	if err != nil {
		t.Fatalf("a vendored side is refused: %v", err)
	}
	if !plan.Vendor || len(plan.Files) != 0 {
		t.Errorf("plan = %+v, want Vendor and no files", plan)
	}
}
