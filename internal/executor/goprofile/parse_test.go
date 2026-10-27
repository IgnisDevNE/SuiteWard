package goprofile

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

const module = "example.com/fx"

const (
	pass     = execution.ObservedPass
	fail     = execution.ObservedFail
	skip     = execution.ObservedSkip
	absent   = execution.ObservedAbsent
	buildErr = execution.ObservedBuildError
)

// at is an expected test of a package; the declaring file plays no part in a result.
func at(dir, name string) TestID {
	file := dir + "/x_test.go"
	if dir == "." {
		file = "x_test.go"
	}
	return TestID{Package: dir, Name: name, File: file}
}

// fixture is the standard output of a real go test -json, recorded from the module in testdata/fixture-module.txt.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(filepath.Join("testdata", name+".json")))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func observe(t *testing.T, stdout []byte, exit int, expected ...TestID) map[TestID]execution.Observation {
	t.Helper()
	got, err := Parse(stdout, exit, module, expected)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func wantObservations(t *testing.T, got, want map[TestID]execution.Observation) {
	t.Helper()
	if !maps.Equal(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
}

// Each fixture is the output of the go test that its comment shows, which is what Command builds for the expected
// tests (-json -count=1 -shuffle=on -timeout=1m0s unless the comment says more), run in the sandbox of Command, with the
// exit status that the container returned. The shuffle is random: a fixture that depends on the order says so.
func TestParseReadsRecordedGoTestOutput(t *testing.T) {
	cases := []struct {
		fixture  string
		exit     int
		expected []TestID
		want     map[TestID]execution.Observation
	}{
		// -run '^(TestFail|TestGood|TestMissing|TestPass|TestRootOK|TestSkip|TestSub|TestSubFail)$' . ./good ./mixed
		{"pass-fail-skip", 1,
			[]TestID{at(".", "TestRootOK"), at("good", "TestGood"), at("mixed", "TestPass"), at("mixed", "TestFail"), at("mixed", "TestSkip"),
				at("mixed", "TestSub"), at("mixed", "TestSubFail"), at("mixed", "TestMissing")},
			map[TestID]execution.Observation{at(".", "TestRootOK"): pass, at("good", "TestGood"): pass, at("mixed", "TestPass"): pass,
				at("mixed", "TestFail"): fail, at("mixed", "TestSkip"): skip, at("mixed", "TestSub"): pass, at("mixed", "TestSubFail"): fail,
				at("mixed", "TestMissing"): absent}},
		// -run '^(TestGood|TestPass|TestRootOK|TestSkip|TestSub)$' . ./good ./mixed
		{"all-pass", 0,
			[]TestID{at(".", "TestRootOK"), at("good", "TestGood"), at("mixed", "TestPass"), at("mixed", "TestSkip"), at("mixed", "TestSub")},
			map[TestID]execution.Observation{at(".", "TestRootOK"): pass, at("good", "TestGood"): pass, at("mixed", "TestPass"): pass,
				at("mixed", "TestSkip"): skip, at("mixed", "TestSub"): pass}},
		// -run '^(TestBroken|TestGood|TestOnSource)$' ./broken ./brokensrc ./good: a compile error in a test file and in a source file; TestNeverRan is expected too, and did not build with its package
		{"build-error", 1,
			[]TestID{at("good", "TestGood"), at("broken", "TestBroken"), at("broken", "TestNeverRan"), at("brokensrc", "TestOnSource")},
			map[TestID]execution.Observation{at("good", "TestGood"): pass, at("broken", "TestBroken"): buildErr,
				at("broken", "TestNeverRan"): buildErr, at("brokensrc", "TestOnSource"): buildErr}},
		// -run '^(TestGood|TestUse)$' ./good ./usesdep: the package builds a dependency that does not
		{"dependency-build-error", 1,
			[]TestID{at("good", "TestGood"), at("usesdep", "TestUse")},
			map[TestID]execution.Observation{at("good", "TestGood"): pass, at("usesdep", "TestUse"): buildErr}},
		// -run '^(TestVet)$' ./vet: go vet fails the build of the test binary
		{"vet", 1, []TestID{at("vet", "TestVet")}, map[TestID]execution.Observation{at("vet", "TestVet"): buildErr}},
		// -run '^(TestGood|TestSetup)$' ./good ./setup: the package imports one that does not exist; FailedBuild names that one
		{"setup-failed", 1,
			[]TestID{at("good", "TestGood"), at("setup", "TestSetup")},
			map[TestID]execution.Observation{at("good", "TestGood"): pass, at("setup", "TestSetup"): buildErr}},
		// -run '^(TestDup|TestOnlyInternal)$' ./dup: TestDup is in the internal and the external test package, and the external one fails
		{"duplicate-name", 1,
			[]TestID{at("dup", "TestDup"), at("dup", "TestOnlyInternal")},
			map[TestID]execution.Observation{at("dup", "TestDup"): fail, at("dup", "TestOnlyInternal"): pass}},
		// -run '^(TestAfter|TestPanic)$' ./panics: the panic stops the binary before TestAfter runs (the shuffled order put TestPanic first)
		{"panic", 1,
			[]TestID{at("panics", "TestPanic"), at("panics", "TestAfter")},
			map[TestID]execution.Observation{at("panics", "TestPanic"): fail, at("panics", "TestAfter"): absent}},
		// -timeout=2s -run '^(TestAfterHang|TestHang)$' ./hang: TestHang starts and never ends, and go test reports no result for it; the shuffled order put it first
		{"timeout", 1,
			[]TestID{at("hang", "TestHang"), at("hang", "TestAfterHang")},
			map[TestID]execution.Observation{at("hang", "TestHang"): fail, at("hang", "TestAfterHang"): absent}},
		// -run '^(TestExitZero|TestOther)$' ./exits: a test that calls os.Exit(0); the shuffled order put TestOther first
		{"exit-in-test", 1,
			[]TestID{at("exits", "TestExitZero"), at("exits", "TestOther")},
			map[TestID]execution.Observation{at("exits", "TestExitZero"): fail, at("exits", "TestOther"): pass}},
		// -run '^(TestNothing)$' ./good: nothing matched, nothing ran, and go test says "ok"
		{"no-tests-ran", 0,
			[]TestID{at("good", "TestNothing")},
			map[TestID]execution.Observation{at("good", "TestNothing"): absent}},
		// -run '^(TestGood)$' ./good, with go.mod saying `go 1.99`: go refuses with GOTOOLCHAIN=local, and writes no event
		{"toolchain-refusal", 1,
			[]TestID{at("good", "TestGood")},
			map[TestID]execution.Observation{at("good", "TestGood"): buildErr}},
		// -run '^(TestGood)$' ./good, with a vendor/modules.txt that go.mod does not agree with: go writes no event
		{"inconsistent-vendor", 1,
			[]TestID{at("good", "TestGood")},
			map[TestID]execution.Observation{at("good", "TestGood"): buildErr}},
		// -run '^(TestGood)$' ./good ./nested, where nested/ has a go.mod of its own: go reports the pattern ./nested as a
		// setup failure and runs ./good, so the rest of the invocation stands (Command is never given such a package)
		{"nested-module-pattern", 1,
			[]TestID{at("good", "TestGood")},
			map[TestID]execution.Observation{at("good", "TestGood"): pass}},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			wantObservations(t, observe(t, fixture(t, c.fixture), c.exit, c.expected...), c.want)
		})
	}
}

func TestParseTakesTheExitStatusAsTheLastWord(t *testing.T) {
	passing := []TestID{at(".", "TestRootOK"), at("good", "TestGood"), at("mixed", "TestPass"), at("mixed", "TestSkip"), at("mixed", "TestSub")}
	mixed := []TestID{at("good", "TestGood"), at("mixed", "TestPass"), at("mixed", "TestFail"), at("mixed", "TestMissing")}
	cases := map[string]struct {
		fixture  string
		exit     int
		expected []TestID
	}{
		"a failing exit over tests that all passed or skipped":          {"all-pass", 1, passing},
		"a failing exit that is not 1":                                  {"all-pass", 137, passing},
		"a zero exit over a failure":                                    {"pass-fail-skip", 0, mixed},
		"a zero exit over a package that does not build":                {"build-error", 0, []TestID{at("good", "TestGood"), at("broken", "TestBroken")}},
		"a zero exit over a build failure of a dependency":              {"dependency-build-error", 0, []TestID{at("good", "TestGood"), at("usesdep", "TestUse")}},
		"a zero exit over a test that never reported":                   {"panic", 0, []TestID{at("panics", "TestPanic"), at("panics", "TestAfter")}},
		"a zero exit over a test that started and never ended":          {"timeout", 0, []TestID{at("hang", "TestHang"), at("hang", "TestAfterHang")}},
		"a zero exit over a failure of one duplicate of a name":         {"duplicate-name", 0, []TestID{at("dup", "TestDup")}},
		"a failing exit with nothing reported and nothing failed":       {"no-tests-ran", 1, []TestID{at("good", "TestNothing")}},
		"a zero exit over a failure of a test nobody expected":          {"pass-fail-skip", 0, []TestID{at("good", "TestGood"), at("mixed", "TestPass")}},
		"a zero exit over a build failure of a package nobody expected": {"build-error", 0, []TestID{at("good", "TestGood")}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			want := map[TestID]execution.Observation{}
			for _, id := range c.expected {
				want[id] = fail
			}
			wantObservations(t, observe(t, fixture(t, c.fixture), c.exit, c.expected...), want)
		})
	}
}

// A failing status is explained by a failure anywhere in the invocation, the tests that nobody expected included: those
// are in the report as go test wrote it. What the status then says of the expected tests is what the report says.
func TestParseLetsAnyFailureOfTheInvocationExplainAFailingStatus(t *testing.T) {
	cases := map[string]struct {
		fixture  string
		expected []TestID
		want     map[TestID]execution.Observation
	}{
		"a test that nobody expected failed": {"pass-fail-skip", []TestID{at("good", "TestGood"), at("mixed", "TestPass")},
			map[TestID]execution.Observation{at("good", "TestGood"): pass, at("mixed", "TestPass"): pass}},
		"a package that nobody expected did not build": {"build-error", []TestID{at("good", "TestGood")},
			map[TestID]execution.Observation{at("good", "TestGood"): pass}},
		"a failing test explains the status of the test that did not run": {"panic", []TestID{at("panics", "TestAfter")},
			map[TestID]execution.Observation{at("panics", "TestAfter"): absent}},
		"the expected test failed itself": {"pass-fail-skip", []TestID{at("good", "TestGood"), at("mixed", "TestFail")},
			map[TestID]execution.Observation{at("good", "TestGood"): pass, at("mixed", "TestFail"): fail}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			wantObservations(t, observe(t, fixture(t, c.fixture), 1, c.expected...), c.want)
		})
	}
	// With no expected test there is nothing to read, under either status.
	for _, exit := range []int{0, 1} {
		if got := observe(t, fixture(t, "all-pass"), exit); len(got) != 0 {
			t.Errorf("exit %d: no expected test gave %v", exit, got)
		}
	}
}

func line(action, pkg, test string) string {
	return fmt.Sprintf(`{"Action":%q,"Package":%q,"Test":%q}`, action, pkg, test)
}

func stream(lines ...string) []byte { return []byte(strings.Join(lines, "\n") + "\n") }

func TestParseTakesTheWorstOfTheResultsOfOneName(t *testing.T) {
	p := module + "/a"
	for name, c := range map[string]struct {
		results []string
		want    execution.Observation
	}{
		"pass and fail":   {[]string{"pass", "fail"}, fail},
		"fail and pass":   {[]string{"fail", "pass"}, fail},
		"skip and fail":   {[]string{"skip", "fail"}, fail},
		"pass and skip":   {[]string{"pass", "skip"}, skip},
		"skip and pass":   {[]string{"skip", "pass"}, skip},
		"pass and pass":   {[]string{"pass", "pass"}, pass},
		"skip and skip":   {[]string{"skip", "skip"}, skip},
		"a pass, a start": {[]string{"pass", "run"}, fail},
		"a twin that passes and a twin that never ends": {[]string{"run", "pass", "run"}, fail},
		"a twin that never ends and a twin that passes": {[]string{"run", "run", "pass"}, fail},
		"two twins that pass, parallel":                 {[]string{"run", "run", "pass", "pass"}, pass},
		"two twins, one skips":                          {[]string{"run", "pass", "run", "skip"}, skip},
		"a start, a fail":                               {[]string{"run", "fail"}, fail},
		"a start and more":                              {[]string{"run", "run"}, fail},
	} {
		t.Run(name, func(t *testing.T) {
			// A test of another package fails and the status is failing, so that the exit status contradicts nothing and
			// the observation is the one that the events of the name give.
			lines := []string{line("fail", module+"/b", "TestOther")}
			for _, action := range c.results {
				lines = append(lines, line(action, p, "TestA"))
			}
			wantObservations(t, observe(t, stream(lines...), 1, at("a", "TestA")), map[TestID]execution.Observation{at("a", "TestA"): c.want})
		})
	}
}

func TestParseReadsOnlyTheTopLevelTestsOfTheExpectedPackages(t *testing.T) {
	a, b := at("a", "TestA"), at("b", "TestA")
	got := observe(t, stream(
		line("pass", module+"/a", "TestA/sub"),
		line("fail", module+"/a", "TestA/other"),
		line("fail", module+"/a", "TestOther"),
		line("pass", module+"/b", "TestA"),
		line("fail", module+"/c", "TestA"),
		line("pass", "example.org/elsewhere/a", "TestA"),
	), 1, a, b)
	wantObservations(t, got, map[TestID]execution.Observation{a: absent, b: pass})
}

func TestParseNamesPackagesByTheirImportPathUnderTheModule(t *testing.T) {
	const v2 = "example.com/m/v2"
	root, deep := at(".", "TestRoot"), at("a/b", "TestDeep")
	stdout := stream(line("pass", v2, "TestRoot"), line("pass", v2+"/a/b", "TestDeep"), line("fail", "example.com/m/a/b", "TestDeep"))
	got, err := Parse(stdout, 1, v2, []TestID{root, deep})
	if err != nil {
		t.Fatal(err)
	}
	wantObservations(t, got, map[TestID]execution.Observation{root: pass, deep: pass})
	if _, err := Parse(stdout, 0, "", []TestID{root}); err == nil {
		t.Error("an empty module path was not an error")
	}
}

func TestParseGivesTwoIDsOfOneTestTheSameObservation(t *testing.T) {
	one := TestID{Package: "a", Name: "TestA", File: "a/a_test.go"}
	other := TestID{Package: "a", Name: "TestA", File: "a/a_x_test.go"}
	got := observe(t, stream(line("pass", module+"/a", "TestA")), 0, one, other)
	wantObservations(t, got, map[TestID]execution.Observation{one: pass, other: pass})
}

// Lines that are not events are not events: they come from nothing that go test wrote, and none of them is a pass.
func TestParseTrustsNoLineThatDoesNotDecode(t *testing.T) {
	clean := fixture(t, "all-pass")
	expected := []TestID{at(".", "TestRootOK"), at("good", "TestGood"), at("mixed", "TestPass"), at("mixed", "TestSkip"), at("mixed", "TestSub")}
	want := observe(t, clean, 0, expected...)

	noise := stream(
		"go: warning: something on the wrong stream",
		`{"Action":"fail","Package":"`+module+`/good","Test":"TestGood"`, // cut off before its end
		`[1,2,3]`, `"pass"`, `{"Action":5}`, `{"Action":"fail","Package":7}`, "", "   ",
	)
	wantObservations(t, observe(t, append(slices.Clone(noise), clean...), 0, expected...), want)

	// Nothing but lines that do not decode, or that name no package: nothing passed.
	forged := stream(
		`{"Action":"pass","Package":"`+module+`","Test":"TestRootOK"`,
		`{"Action":"pass","Test":"TestRootOK"}`,
		`{"Action":"pass","Package":"`+module+`"}`,
	)
	wantObservations(t, observe(t, forged, 0, at(".", "TestRootOK")), map[TestID]execution.Observation{at(".", "TestRootOK"): absent})
}

func TestParseReadsOutputThatIsLongOrHasWindowsLineEnds(t *testing.T) {
	clean := fixture(t, "pass-fail-skip")
	expected := []TestID{at("good", "TestGood"), at("mixed", "TestPass"), at("mixed", "TestFail")}
	want := observe(t, clean, 1, expected...)

	crlf := bytes.ReplaceAll(clean, []byte("\n"), []byte("\r\n"))
	wantObservations(t, observe(t, crlf, 1, expected...), want)

	// A line past every scanner's default limit stands between two events.
	long := fmt.Sprintf(`{"Action":"output","Package":%q,"Test":"TestPass","Output":%q}`, module+"/mixed", strings.Repeat("x", 1<<20))
	wantObservations(t, observe(t, slices.Concat(stream(long), clean, stream(long)), 1, expected...), want)
}

func TestReruns(t *testing.T) {
	a, b, c, d, e := at("a", "TestA"), at("a", "TestB"), at("b", "TestC"), at("b", "TestD"), at(".", "TestE")
	obs := map[TestID]execution.Observation{a: fail, b: pass, c: fail, d: buildErr, e: absent}
	for name, tc := range map[string]struct {
		obs        map[TestID]execution.Observation
		done, most int
		want       []TestID
	}{
		"the failing and the absent tests, and not a pass, a skip or a build error": {obs, 1, 3, []TestID{e, a, c}},
		"after a rerun, with one left": {obs, 2, 3, []TestID{e, a, c}},
		"a test that has had its runs": {obs, 3, 3, nil},
		"more runs than allowed":       {obs, 4, 3, nil},
		"one run and no reruns":        {obs, 1, 1, nil},
		"nothing failed":               {map[TestID]execution.Observation{a: pass, b: skip}, 1, 3, nil},
		"nothing at all":               {nil, 1, 3, nil},
		"no runs allowed":              {obs, 0, 0, nil},
	} {
		t.Run(name, func(t *testing.T) { sameIDs(t, "reruns", Reruns(tc.obs, tc.done, tc.most), tc.want) })
	}
}

// An invocation that exits with a failing status and wrote no event at all ran nothing: go refused before it built a
// package (a vendor/ that does not agree with go.mod, a toolchain that does not suit go.mod). No test of it built, and
// none is absent for want of a run. A package that go rejects as a pattern is no such case: go reports it as events.
func TestParseReadsAFailingStatusWithNoEventAsNothingBuilt(t *testing.T) {
	expected := []TestID{at("a", "TestA"), at(".", "TestRoot")}
	for name, stdout := range map[string][]byte{
		"no output":                    nil,
		"blank lines":                  []byte("\n\r\n  \n"),
		"lines that are not events":    []byte("go: inconsistent vendoring\n[1]\n{\"Action\":5}\nnull\n"),
		"objects that name no action":  []byte("{}\n{\"Package\":\"" + module + "/a\"}\n"),
		"a cut off event, and nothing": []byte(`{"Action":"pass","Package":"` + module + `/a","Test":"TestA"`),
	} {
		t.Run(name, func(t *testing.T) {
			want := map[TestID]execution.Observation{expected[0]: buildErr, expected[1]: buildErr}
			wantObservations(t, observe(t, stdout, 1, expected...), want)
			wantObservations(t, observe(t, stdout, 2, expected...), want)
		})
	}
	// A zero status over no event is no refusal: the tests were not reported, so they are absent.
	wantObservations(t, observe(t, nil, 0, expected...), map[TestID]execution.Observation{expected[0]: absent, expected[1]: absent})
	// With any event the contradiction rule decides, as before.
	wantObservations(t, observe(t, fixture(t, "no-tests-ran"), 1, at("good", "TestNothing")),
		map[TestID]execution.Observation{at("good", "TestNothing"): fail})
}
