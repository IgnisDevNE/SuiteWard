package goprofile

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// Parse turns the standard output and the exit status of one go test -json invocation (one side) into one observation
// for each expected test. module is the module path of the tree that was tested: go test names a package by its import
// path, which is module for the root and module/dir for the others.
//
// A line is an event when it decodes as a JSON object that names an Action. Any other line is not trusted and is
// skipped: it cannot give a pass, and a test that only such a line mentions is absent.
//
// An invocation that exits with a failing status and wrote no event at all ran nothing: go refused before it built a
// package (a vendor/ that does not agree with go.mod, a toolchain that does not suit go.mod), so
// every expected test reads build-error.
//
// Otherwise only the top-level tests of the expected packages are read, and each line on its own:
//   - a package whose build failed (its package-level fail event names FailedBuild, a compile error, a vet failure or an
//     import that does not resolve alike) gives build-error to every expected test of it. go names such a package by its import
//     path, except one it cannot resolve (a directory under another module), which it names by the pattern of the invocation
//     (./dir): a setup failure that reads the same;
//   - a name with a start that no result ended is fail: a timeout, which go test reports for the package only, or a twin
//     of a name that hangs after the other passed. A name that has not started has no such start;
//   - a test's pass, fail and skip give pass, fail and skip. A name that reports twice (an internal and an external test
//     package may both declare it, and -run selects by name alone) is the worst of its results: fail, then skip, then
//     pass;
//   - a test that no event mentions is absent.
//
// Last, the exit status is read against the report, as go test's own: when they contradict each other, the report was
// not go test's, and every expected test reads fail. The report shows a failure when any test of the invocation failed
// (a subtest, and a test that nobody expected, included), any package did not build, or an expected test reads fail
// or build-error (that includes one that started and never ended). A failing status with no such failure contradicts
// the report, and so does a zero status with one. So a test that was not named but has the name of one that was (go test
// applies -run to every package) explains a failing status when it fails, and fails the invocation when the status is
// zero.
//
// The rule catches a report that contradicts the status, not one that agrees with it. A package whose init prints the
// events of go test's own framing (each line starts with the byte \x16) and then exits with status 0 forges any result,
// and nothing contradicts it. The markers (forged-event and exit-in-init of Markers) are the only signals that SuiteWard
// looks for, and code that builds the byte at run time ([]byte{22}, rune('\x16')) or exits through a helper evades both.
func Parse(stdout []byte, exitCode int, module string, expected []TestID) (map[TestID]execution.Observation, error) {
	if module == "" {
		return nil, errors.New("go test output: no module path to name the packages by")
	}
	r := read(stdout)
	obs := make(map[TestID]execution.Observation, len(expected))
	if exitCode != 0 && r.events == 0 {
		for _, id := range expected {
			obs[id] = execution.ObservedBuildError
		}
		return obs, nil
	}
	for _, id := range expected {
		obs[id] = r.observation(importPath(module, id.Package), pattern(id.Package), id.Name)
	}
	if r.failure(obs) == (exitCode == 0) {
		for id := range obs {
			obs[id] = execution.ObservedFail
		}
	}
	return obs, nil
}

// event is what Parse reads of a test2json event.
type event struct {
	Action, Package, Test, FailedBuild string
}

// report is what the events say: how many there are, the packages that did not build, the starts of each test that no
// result has ended yet, their results, and whether any test of any package, a subtest included, failed.
type report struct {
	events     int
	notBuilt   map[string]bool
	open       map[key]int
	results    map[key][]execution.Observation
	testFailed bool
}

var resultOf = map[string]execution.Observation{
	"pass": execution.ObservedPass,
	"fail": execution.ObservedFail,
	"skip": execution.ObservedSkip,
}

func read(stdout []byte) report {
	r := report{notBuilt: map[string]bool{}, open: map[key]int{}, results: map[key][]execution.Observation{}}
	for _, line := range bytes.Split(stdout, []byte("\n")) {
		var ev event
		if err := json.Unmarshal(bytes.TrimSpace(line), &ev); err != nil || ev.Action == "" {
			continue
		}
		r.events++
		k := key{ev.Package, ev.Test}
		if ev.Test != "" && ev.Action == "fail" {
			r.testFailed = true
		}
		switch {
		case ev.Test == "":
			if ev.Action == "fail" && ev.FailedBuild != "" {
				r.notBuilt[ev.Package] = true
			}
		case strings.Contains(ev.Test, "/"): // a subtest: its parent reports for it
		case ev.Action == "run":
			r.open[k]++
		default:
			if o, ok := resultOf[ev.Action]; ok {
				r.results[k] = append(r.results[k], o)
				if r.open[k] > 0 {
					r.open[k]--
				}
			}
		}
	}
	return r
}

func (r report) observation(pkg, dirPattern, name string) execution.Observation {
	k := key{pkg, name}
	switch {
	case r.notBuilt[pkg] || r.notBuilt[dirPattern]:
		return execution.ObservedBuildError
	case r.open[k] > 0:
		return execution.ObservedFail
	case len(r.results[k]) > 0:
		return slices.MaxFunc(r.results[k], func(a, b execution.Observation) int { return cmp.Compare(severity(a), severity(b)) })
	}
	return execution.ObservedAbsent
}

// severity ranks the results of one name: fail over skip over pass.
func severity(o execution.Observation) int {
	switch o {
	case execution.ObservedFail:
		return 2
	case execution.ObservedSkip:
		return 1
	}
	return 0
}

func importPath(module, dir string) string {
	if dir == "." {
		return module
	}
	return module + "/" + dir
}

// failure is whether the report shows the invocation failing: a test of it failed, a package did not build, or an
// expected test reads fail or build-error.
func (r report) failure(obs map[TestID]execution.Observation) bool {
	failed := r.testFailed || len(r.notBuilt) > 0
	for _, o := range obs {
		failed = failed || o == execution.ObservedFail || o == execution.ObservedBuildError
	}
	return failed
}

// Reruns lists, sorted, the tests of obs (their latest results) that failed or are absent and may run again: a test is
// run limit times in all, its first run included, and done is how many it has had. A build error is the same on every
// run, so it is not run again. An absent test may have been cut short by another test that hung or crashed, so it is.
func Reruns(obs map[TestID]execution.Observation, done, limit int) []TestID {
	if done >= limit {
		return nil
	}
	var again []TestID
	for id, o := range obs {
		if o == execution.ObservedFail || o == execution.ObservedAbsent {
			again = append(again, id)
		}
	}
	slices.SortFunc(again, func(a, b TestID) int {
		return cmp.Or(cmp.Compare(a.Package, b.Package), cmp.Compare(a.Name, b.Name), cmp.Compare(a.File, b.File))
	})
	return again
}
