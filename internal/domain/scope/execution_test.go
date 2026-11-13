package scope_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// withExecution is a declaration whose execution section is body (flow style, or a block that starts on the next line).
func withExecution(body string) string {
	return "version: 1\nexecution: " + body + "\n"
}

func TestParseDeclarationReadsTheExecutionPolicy(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want scope.Execution
		ok   bool
	}{
		{"every field", withExecution("{profile: go, canonical: required, receipt: before-approval}"), scope.Execution{Profile: "go", Canonical: "required", Receipt: "before-approval"}, true},
		{"a profile alone defaults both levels to advisory", withExecution("{profile: go}"), scope.Execution{Profile: "go", Canonical: "advisory", Receipt: "advisory"}, true},
		{"the defaults spelled out", withExecution("{profile: go, canonical: advisory, receipt: advisory}"), scope.Execution{Profile: "go", Canonical: "advisory", Receipt: "advisory"}, true},
		{"off is a level, not a boolean", withExecution("{profile: go, canonical: off, receipt: off}"), scope.Execution{Profile: "go", Canonical: "off", Receipt: "off"}, true},
		{"quoted scalars", withExecution(`{profile: "go", canonical: "required", receipt: 'required'}`), scope.Execution{Profile: "go", Canonical: "required", Receipt: "required"}, true},
		{"block style among the other keys", "version: 1\nexecution:\n  receipt: required\n  profile: go\ninclude: [src/]\n", scope.Execution{Profile: "go", Canonical: "advisory", Receipt: "required"}, true},
		{"the profile after its levels", withExecution("{canonical: required, receipt: off, profile: go}"), scope.Execution{Profile: "go", Canonical: "required", Receipt: "off"}, true},
		{"an empty section runs nothing", withExecution("{}"), scope.Execution{}, false},
		{"no section runs nothing", "version: 1\n", scope.Execution{}, false},
		{"a declaration with lists and no section runs nothing", goldenDeclaration, scope.Execution{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := mustParse(t, []byte(tt.raw)).Execution()
			if got != tt.want || ok != tt.ok {
				t.Fatalf("Execution() = %+v, %v, want %+v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
	if got, ok := scope.NoDeclaration().Execution(); ok || got != (scope.Execution{}) {
		t.Fatalf("NoDeclaration().Execution() = %+v, %v, want no policy", got, ok)
	}
}

func TestParseDeclarationReadsEveryLevel(t *testing.T) {
	for _, level := range []string{"off", "advisory", "required"} {
		got, _ := mustParse(t, []byte(withExecution("{profile: go, canonical: "+level+"}"))).Execution()
		if got.Canonical != level || got.Receipt != "advisory" {
			t.Errorf("canonical %s: Execution() = %+v", level, got)
		}
	}
	for _, level := range []string{"off", "advisory", "before-approval", "required"} {
		got, _ := mustParse(t, []byte(withExecution("{profile: go, receipt: "+level+"}"))).Execution()
		if got.Receipt != level || got.Canonical != "advisory" {
			t.Errorf("receipt %s: Execution() = %+v", level, got)
		}
	}
}

// Each refused declaration comes with the valid declaration that differs from it by the one thing that is refused, so
// that a refusal cannot come from anything but that: the whole schema without the section is already refused today.
func TestParseDeclarationRefusesAnInvalidExecutionSection(t *testing.T) {
	tests := []struct{ name, raw, valid string }{
		{"an unknown key", withExecution("{profile: go, extra: x}"), withExecution("{profile: go}")},
		{"a misspelt key", withExecution("{profile: go, Canonical: off}"), withExecution("{profile: go, canonical: off}")},
		{"an unknown profile", withExecution("{profile: rust}"), withExecution("{profile: go}")},
		{"a profile in another case", withExecution("{profile: Go}"), withExecution("{profile: go}")},
		{"an empty profile", withExecution(`{profile: ""}`), withExecution("{profile: go}")},
		{"an unknown canonical level", withExecution("{profile: go, canonical: strict}"), withExecution("{profile: go, canonical: required}")},
		{"a receipt level as a canonical level", withExecution("{profile: go, canonical: before-approval}"), withExecution("{profile: go, canonical: required}")},
		{"an unknown receipt level", withExecution("{profile: go, receipt: strict}"), withExecution("{profile: go, receipt: required}")},
		{"an empty level", withExecution(`{profile: go, receipt: ""}`), withExecution("{profile: go, receipt: off}")},
		{"a level in another case", withExecution("{profile: go, canonical: Required}"), withExecution("{profile: go, canonical: required}")},
		{"a canonical level without a profile", withExecution("{canonical: required}"), withExecution("{profile: go, canonical: required}")},
		{"a receipt level without a profile", withExecution("{receipt: off}"), withExecution("{profile: go, receipt: off}")},
		{"both levels without a profile", withExecution("{canonical: advisory, receipt: advisory}"), withExecution("{profile: go, canonical: advisory, receipt: advisory}")},
		{"a level in block style without a profile", "version: 1\nexecution:\n  canonical: required\n", "version: 1\nexecution:\n  profile: go\n  canonical: required\n"},
		{"a scalar section", withExecution("go"), withExecution("{profile: go}")},
		{"a sequence section", withExecution("[go]"), withExecution("{profile: go}")},
		{"a null section", withExecution("~"), withExecution("{profile: go}")},
		{"a boolean section", withExecution("true"), withExecution("{profile: go}")},
		{"an empty section node", "version: 1\nexecution:\n", withExecution("{}")},
		{"a duplicate profile", withExecution("{profile: go, profile: go}"), withExecution("{profile: go}")},
		{"a duplicate level", withExecution("{profile: go, canonical: off, canonical: off}"), withExecution("{profile: go, canonical: off}")},
		{"a duplicate section", withExecution("{profile: go}") + "execution: {profile: go}\n", withExecution("{profile: go}")},
		{"an integer profile", withExecution("{profile: 1}"), withExecution("{profile: go}")},
		{"a sequence profile", withExecution("{profile: [go]}"), withExecution("{profile: go}")},
		{"a mapping profile", withExecution("{profile: {a: go}}"), withExecution("{profile: go}")},
		{"a null profile", withExecution("{profile: ~}"), withExecution("{profile: go}")},
		{"a boolean level", withExecution("{profile: go, canonical: true}"), withExecution("{profile: go, canonical: off}")},
		{"an integer level", withExecution("{profile: go, canonical: 1}"), withExecution("{profile: go, canonical: off}")},
		{"a sequence level", withExecution("{profile: go, receipt: [off]}"), withExecution("{profile: go, receipt: off}")},
		{"a null level", withExecution("{profile: go, receipt: ~}"), withExecution("{profile: go, receipt: off}")},
		{"a key that is not a string", withExecution("{1: go}"), withExecution("{profile: go}")},
		{"a merge key", "version: 1\nexecution:\n  <<: {profile: go}\n", "version: 1\nexecution:\n  profile: go\n"},
		{"an alias is not followed", withExecution("{profile: &p go, receipt: *p}"), withExecution("{profile: &p go, receipt: off}")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := scope.ParseDeclaration([]byte(tt.valid)); err != nil {
				t.Fatalf("ParseDeclaration(%q) error = %v, want the valid twin accepted", tt.valid, err)
			}
			_, err := scope.ParseDeclaration([]byte(tt.raw))
			if !errors.Is(err, scope.ErrInvalidDeclaration) {
				t.Fatalf("ParseDeclaration(%q) error = %v, want ErrInvalidDeclaration", tt.raw, err)
			}
			if errors.Is(err, scope.ErrInvalidPattern) {
				t.Fatalf("ParseDeclaration(%q) error = %v, want no ErrInvalidPattern", tt.raw, err)
			}
		})
	}
}

// AddPatterns rewrites the file through its YAML nodes; the policy must come out as it went in.
func TestAddPatternsKeepsTheExecutionPolicy(t *testing.T) {
	const raw = "version: 1\nexecution:\n  profile: go\n  canonical: required\n  receipt: off\ninclude:\n  - src/**\n"
	want, _ := mustParse(t, []byte(raw)).Execution()
	for name, edit := range map[string]func() ([]byte, error){
		"AddPatterns": func() ([]byte, error) { return scope.AddPatterns([]byte(raw), []string{"docs/"}, []string{"Makefile"}) },
		"AddExcludes": func() ([]byte, error) { return scope.AddExcludes([]byte(raw), []string{"src/gen/"}) },
	} {
		edited, err := edit()
		if err != nil {
			t.Fatalf("%s error = %v", name, err)
		}
		if got, ok := mustParse(t, edited).Execution(); !ok || got != want {
			t.Fatalf("%s left Execution() = %+v, %v, want %+v\n%s", name, got, ok, want, edited)
		}
	}
}

func TestReductionsReportAWeakerExecutionPolicy(t *testing.T) {
	const full = "{profile: go, canonical: required, receipt: required}"
	tests := []struct {
		name                string
		governing, proposed string
		want                []string
	}{
		{"an unchanged policy", withExecution(full), withExecution(full), []string{}},
		{"canonical from required to advisory", withExecution("{profile: go, canonical: required}"), withExecution("{profile: go, canonical: advisory}"), []string{"execution canonical: required -> advisory"}},
		{"canonical from advisory to off", withExecution("{profile: go}"), withExecution("{profile: go, canonical: off}"), []string{"execution canonical: advisory -> off"}},
		{"canonical from required to off", withExecution("{profile: go, canonical: required}"), withExecution("{profile: go, canonical: off}"), []string{"execution canonical: required -> off"}},
		{"receipt from required to before-approval", withExecution("{profile: go, receipt: required}"), withExecution("{profile: go, receipt: before-approval}"), []string{"execution receipt: required -> before-approval"}},
		{"receipt from before-approval to advisory", withExecution("{profile: go, receipt: before-approval}"), withExecution("{profile: go}"), []string{"execution receipt: before-approval -> advisory"}},
		{"receipt from advisory to off", withExecution("{profile: go}"), withExecution("{profile: go, receipt: off}"), []string{"execution receipt: advisory -> off"}},
		{"receipt from required to off", withExecution("{profile: go, receipt: required}"), withExecution("{profile: go, receipt: off}"), []string{"execution receipt: required -> off"}},
		{"both levels lowered", withExecution(full), withExecution("{profile: go, canonical: advisory, receipt: before-approval}"), []string{"execution canonical: required -> advisory", "execution receipt: required -> before-approval"}},
		{"one level lowered and one raised", withExecution("{profile: go, canonical: required, receipt: off}"), withExecution("{profile: go, canonical: off, receipt: required}"), []string{"execution canonical: required -> off"}},
		{"the profile removed lowers every level to off", withExecution(full), withExecution("{}"), []string{"execution canonical: required -> off", "execution profile: go . removed", "execution receipt: required -> off"}},
		{"the profile removed from the defaults", withExecution("{profile: go}"), withExecution("{}"), []string{"execution canonical: advisory -> off", "execution profile: go . removed", "execution receipt: advisory -> off"}},
		{"the section removed", withExecution(full), "version: 1\n", []string{"execution canonical: required -> off", "execution profile: go . removed", "execution receipt: required -> off"}},
		{"the declaration deleted", withExecution("{profile: go, receipt: before-approval}"), absent, []string{"execution canonical: advisory -> off", "execution profile: go . removed", "execution receipt: before-approval -> off"}},
		{"removing a profile whose levels are off reports the profile only", withExecution("{profile: go, canonical: off, receipt: off}"), "version: 1\n", []string{"execution profile: go . removed"}},
		{"removing an empty section", withExecution("{}"), "version: 1\n", []string{}},
		{"a policy raised", withExecution("{profile: go, canonical: advisory, receipt: advisory}"), withExecution(full), []string{}},
		{"a profile added", "version: 1\n", withExecution("{profile: go}"), []string{}},
		{"a section added to no declaration", absent, withExecution(full), []string{}},
		{
			"sorted among the rules and excludes",
			"version: 1\ninclude: [a]\nexecution: {profile: go, canonical: required, receipt: off}\n",
			"version: 1\nexclude: [b]\nexecution: {profile: go, canonical: advisory, receipt: off}\n",
			[]string{"exclude b", "execution canonical: required -> advisory", "rule a"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scope.Reductions(scopeOf(t, tt.governing), scopeOf(t, tt.proposed))
			if !reflect.DeepEqual(got, tt.want) || !slices.IsSorted(got) {
				t.Fatalf("Reductions() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// reductionCase is a governing and a proposed declaration, and the entries that Reductions reports from the first to the second.
type reductionCase struct {
	name                string
	governing, proposed string
	want                []string
}

func checkReductions(t *testing.T, tests []reductionCase) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scope.Reductions(scopeOf(t, tt.governing), scopeOf(t, tt.proposed))
			if !reflect.DeepEqual(got, tt.want) || !slices.IsSorted(got) {
				t.Fatalf("Reductions() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// A running pair of the governing declaration that the proposed one does not hold is reported as removed, with "." for the root. The
// scalar form is the pair (go, .); a folder that moves is the removal of the old pair.
func TestReductionsReportARemovedProfile(t *testing.T) {
	root := withProfiles("[{profile: go}]")
	checkReductions(t, []reductionCase{
		{"the scalar form is the pair at the root", withExecution("{profile: go}"), withProfiles("[{profile: go, folder: core}]"), []string{"execution profile: go . removed"}},
		{"the root and the scalar form are one pair", withExecution("{profile: go}"), root, []string{}},
		{"a folder that moves", withProfiles("[{profile: go, folder: core}]"), withProfiles("[{profile: go, folder: lib}]"), []string{"execution profile: go core removed"}},
		{"a nested folder", withProfiles("[{profile: go}, {profile: go, folder: tools/gen}]"), root, []string{"execution profile: go tools/gen removed"}},
		{"the root beside a nested folder", withProfiles("[{profile: go}, {profile: go, folder: core}]"), withProfiles("[{profile: go, folder: core}]"), []string{"execution profile: go . removed"}},
		{"a folder as it is declared", withProfiles(`[{profile: go}, {profile: go, folder: "+lib/a b"}]`), root, []string{"execution profile: go +lib/a b removed"}},
		{
			"several, sorted",
			withProfiles("[{profile: go, folder: lib}, {profile: go}, {profile: go, folder: core}]"),
			withProfiles("[{profile: go, folder: core}]"),
			[]string{"execution profile: go . removed", "execution profile: go lib removed"},
		},
		{
			"the levels that stay are not reported",
			withExecution("{profiles: [{profile: go}, {profile: go, folder: core}], canonical: required, receipt: required}"),
			withExecution("{profiles: [{profile: go, folder: core}], canonical: required, receipt: required}"),
			[]string{"execution profile: go . removed"},
		},
		{
			"the last running profile comes with both levels, all sorted together",
			withProfiles("[{profile: go}, {profile: go, folder: core}]"),
			"version: 1\n",
			[]string{"execution canonical: advisory -> off", "execution profile: go . removed", "execution profile: go core removed", "execution receipt: advisory -> off"},
		},
		{
			"sorted among the rules and the excludes",
			"version: 1\ninclude: [a]\nexecution: {profiles: [{profile: go}]}\n",
			"version: 1\nexclude: [b]\n",
			[]string{"exclude b", "execution canonical: advisory -> off", "execution profile: go . removed", "execution receipt: advisory -> off", "rule a"},
		},
		{"a profile that is removed, beside one that stays protected only", withProfiles("[{profile: go}, {profile: go, folder: core, run: false}]"), "version: 1\n", []string{"execution canonical: advisory -> off", "execution profile: go . removed", "execution receipt: advisory -> off"}},
	})
}

// A running pair that the proposed declaration keeps with run false is reported as made protected only. The strings are by pair: an enclosing
// running entry of the profile that goes on running does not hide it.
func TestReductionsReportAProfileMadeProtectedOnly(t *testing.T) {
	checkReductions(t, []reductionCase{
		{
			"the only entry",
			withProfiles("[{profile: go}]"), withProfiles("[{profile: go, run: false}]"),
			[]string{"execution canonical: advisory -> off", "execution profile: go . -> protected only", "execution receipt: advisory -> off"},
		},
		{
			"a nested entry, while the root goes on running",
			withProfiles("[{profile: go}, {profile: go, folder: core}]"), withProfiles("[{profile: go}, {profile: go, folder: core, run: false}]"),
			[]string{"execution profile: go core -> protected only"},
		},
		{
			"the root, while a nested entry goes on running",
			withProfiles("[{profile: go}, {profile: go, folder: core}]"), withProfiles("[{profile: go, run: false}, {profile: go, folder: core}]"),
			[]string{"execution profile: go . -> protected only"},
		},
		{
			"the scalar form",
			withExecution("{profile: go}"), withProfiles("[{profile: go, run: false}, {profile: go, folder: core}]"),
			[]string{"execution profile: go . -> protected only"},
		},
		{
			"one made protected only and another removed",
			withProfiles("[{profile: go, folder: a}, {profile: go, folder: b}, {profile: go, folder: c}]"), withProfiles("[{profile: go, folder: a, run: false}, {profile: go, folder: c}]"),
			[]string{"execution profile: go a -> protected only", "execution profile: go b removed"},
		},
	})
}

// A pair that is added, whether it runs or not, takes nothing away; nor does a protected-only pair that goes, since it ran nothing, or one
// that starts to run.
func TestReductionsIgnoreAnAddedProtectedOnlyEntry(t *testing.T) {
	root := withProfiles("[{profile: go}]")
	checkReductions(t, []reductionCase{
		{"beside a running root", root, withProfiles("[{profile: go}, {profile: go, folder: core, run: false}]"), []string{}},
		{"to a declaration with no profile", "version: 1\n", withProfiles("[{profile: go, run: false}]"), []string{}},
		{"to no declaration", absent, withProfiles("[{profile: go, run: false}]"), []string{}},
		{"a protected-only pair that is removed", withProfiles("[{profile: go}, {profile: go, folder: core, run: false}]"), root, []string{}},
		{"a protected-only pair that starts to run", withProfiles("[{profile: go}, {profile: go, folder: core, run: false}]"), withProfiles("[{profile: go}, {profile: go, folder: core}]"), []string{}},
		{"every pair protected only, then none", withProfiles("[{profile: go, run: false}]"), "version: 1\n", []string{}},
		{"a protected-only pair that moves", withProfiles("[{profile: go}, {profile: go, folder: a, run: false}]"), withProfiles("[{profile: go}, {profile: go, folder: b, run: false}]"), []string{}},
	})
}

func TestReductionsIgnoreAnAddedRunningProfile(t *testing.T) {
	core := withProfiles("[{profile: go, folder: core}]")
	checkReductions(t, []reductionCase{
		{"the root beside a folder", core, withProfiles("[{profile: go}, {profile: go, folder: core}]"), []string{}},
		{"a folder beside the root", withProfiles("[{profile: go}]"), withProfiles("[{profile: go}, {profile: go, folder: core}]"), []string{}},
		{"a folder beside the scalar form", withExecution("{profile: go}"), withProfiles("[{profile: go}, {profile: go, folder: core}]"), []string{}},
		{"to a declaration with no profile", "version: 1\n", core, []string{}},
		{"to no declaration", absent, core, []string{}},
		{"the same list", core, core, []string{}},
	})
}
