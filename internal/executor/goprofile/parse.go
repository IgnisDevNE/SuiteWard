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
// Only the top-level tests of the expected packages are read, and each line on its own:
//   - a package whose build failed (its package-level fail event names FailedBuild, a compile error, a vet failure or an
//     import that does not resolve alike) gives build-error to every expected test of it;
//   - a test's pass, fail and skip give pass, fail and skip. A name that reports twice (an internal and an external test
//     package may both declare it, and -run selects by name alone) is the worst of its results: fail, then skip, then
//     pass;
//   - a test that started and never ended (a timeout, which go test reports for the package only) is fail;
//   - a test that no event mentions is absent.
//
// A line that is not an event (it does not decode, or it is not an object) is not trusted and is skipped: it cannot give
// a pass, and a test that only such a line mentions is absent.
//
// Last, the exit status is read against the report, as go test's own: when they contradict each other, the report was
// not go test's, and every expected test reads fail. That is a failing status over expected tests that all passed or
// skipped, and a zero status over a failure or a package that did not build. The expected tests are all the rule looks
// at: a test that was not named, with the name of one that was in another package of the invocation, can fail the
// status and so fail every test that was named.
func Parse(stdout []byte, exitCode int, module string, expected []TestID) (map[TestID]execution.Observation, error) {
	if module == "" {
		return nil, errors.New("go test output: no module path to name the packages by")
	}
	r := read(stdout)
	obs := make(map[TestID]execution.Observation, len(expected))
	for _, id := range expected {
		obs[id] = r.observation(importPath(module, id.Package), id.Name)
	}
	if contradicts(obs, exitCode) {
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

// report is what the events say: the packages that did not build, the tests that started, and their results.
type report struct {
	notBuilt map[string]bool
	started  map[key]bool
	results  map[key][]execution.Observation
}

var resultOf = map[string]execution.Observation{
	"pass": execution.ObservedPass,
	"fail": execution.ObservedFail,
	"skip": execution.ObservedSkip,
}

func read(stdout []byte) report {
	r := report{notBuilt: map[string]bool{}, started: map[key]bool{}, results: map[key][]execution.Observation{}}
	for _, line := range bytes.Split(stdout, []byte("\n")) {
		var ev event
		if err := json.Unmarshal(bytes.TrimSpace(line), &ev); err != nil {
			continue
		}
		k := key{ev.Package, ev.Test}
		switch {
		case ev.Test == "":
			if ev.Action == "fail" && ev.FailedBuild != "" {
				r.notBuilt[ev.Package] = true
			}
		case strings.Contains(ev.Test, "/"): // a subtest: its parent reports for it
		case ev.Action == "run":
			r.started[k] = true
		default:
			if o, ok := resultOf[ev.Action]; ok {
				r.results[k] = append(r.results[k], o)
			}
		}
	}
	return r
}

func (r report) observation(pkg, name string) execution.Observation {
	k := key{pkg, name}
	switch {
	case r.notBuilt[pkg]:
		return execution.ObservedBuildError
	case len(r.results[k]) > 0:
		return slices.MaxFunc(r.results[k], func(a, b execution.Observation) int { return cmp.Compare(severity(a), severity(b)) })
	case r.started[k]:
		return execution.ObservedFail
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

// contradicts is whether the exit status says what the report does not.
func contradicts(obs map[TestID]execution.Observation, exitCode int) bool {
	failed, allFine := false, true
	for _, o := range obs {
		failed = failed || o == execution.ObservedFail || o == execution.ObservedBuildError
		allFine = allFine && (o == execution.ObservedPass || o == execution.ObservedSkip)
	}
	if exitCode == 0 {
		return failed
	}
	return allFine
}

// Reruns lists, sorted, the tests of obs (their latest results) that failed and may run again: a test is run limit times
// in all, its first run included, and done is how many it has had. A build error is the same on every run, and an absent
// test has no result to confirm, so neither is run again.
func Reruns(obs map[TestID]execution.Observation, done, limit int) []TestID {
	if done >= limit {
		return nil
	}
	var failing []TestID
	for id, o := range obs {
		if o == execution.ObservedFail {
			failing = append(failing, id)
		}
	}
	slices.SortFunc(failing, func(a, b TestID) int {
		return cmp.Or(cmp.Compare(a.Package, b.Package), cmp.Compare(a.Name, b.Name), cmp.Compare(a.File, b.File))
	})
	return failing
}
