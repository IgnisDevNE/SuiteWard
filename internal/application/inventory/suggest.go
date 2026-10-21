package inventory

import (
	"cmp"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// maxSuggestions bounds the include and runner patterns offered to the owner, together.
const maxSuggestions = 20

// Suggestion is a suggested declaration from file paths only (D-SCOPE-DISCOVERY).
type Suggestion struct {
	Include []string
	Runner  []string
	Mixed   []string
	// Submodules are the non-vendored submodules that an Include or Runner pattern reaches; their content is not protected.
	Submodules []string
	// Cut is true when maxSuggestions dropped some include or runner patterns.
	Cut bool
}

var (
	vendoredDirectories = []string{"node_modules", "vendor", ".venv", "venv", ".tox", "__pycache__", "bower_components", "jspm_packages", ".git"}

	pythonTestGlobs = []string{"test_*.py", "*_test.py", "tests.py"}
	// conventionFileGlobs are the test files of the frameworks' own conventions, each matched on the base name at any depth.
	conventionFileGlobs = append([]string{
		"*_test.go", "*.test.*", "*.spec.*", "*.cy.*", "*_spec.rb", "*_test.rb", "*_test.exs", "*_test.dart",
		"*Test.java", "*Tests.java", "*Test.kt", "*Tests.cs", "*Test.php",
	}, pythonTestGlobs...)
	// conventionDirectories hold tests at any depth.
	conventionDirectories = []string{"testdata", "__tests__", "__snapshots__", "__mocks__", "__fixtures__", "cypress"}
	// ambiguousDirectories hold tests at the top level, or below it next to a test-named file.
	ambiguousDirectories = []string{"tests", "test", "spec"}

	// runnerGroups are the files of one runner or helper family; one found brings the whole group, because a runner reads the
	// higher-priority names first and an agent could add them. The first group is pytest's, which Python test files also bring.
	runnerGroups = [][]string{
		{"pytest.toml", ".pytest.toml", "pytest.ini", ".pytest.ini", "conftest.py"},
		{"jest.config.*"}, {"vitest.config.*", "vitest.workspace.*"}, {".mocharc.*"}, {"karma.conf.*"},
		{"playwright.config.*"}, {"cypress.config.*"}, {".coveragerc", ".nycrc*", ".c8rc*"},
		{"phpunit.xml", "phpunit.xml.dist", "phpunit.dist.xml"}, {".rspec", "spec_helper.rb", "rails_helper.rb"},
		{"test_helper.*"}, {"dart_test.yaml"}, {"codecov.yml", ".codecov.yml"},
	}

	// mixedNames can hold test settings among other content, so they are reported and never suggested.
	mixedNames = []string{"package.json", "pyproject.toml", "setup.cfg", "tox.ini", "Cargo.toml", "CMakeLists.txt"}
)

// nameMatches reports whether any glob matches the base name; every glob is a constant of this file, so a malformed one is a
// programming error.
func nameMatches(globs []string, name string) bool {
	return slices.ContainsFunc(globs, func(glob string) bool {
		ok, err := path.Match(glob, name)
		if err != nil {
			panic(fmt.Sprintf("inventory: glob %q is malformed: %v", glob, err))
		}
		return ok
	})
}

// splitFile returns the directories and the base name of a file path, and false under a vendored directory.
func splitFile(p string) (dirs []string, name string, ok bool) {
	segments := strings.Split(p, "/")
	dirs, name = segments[:len(segments)-1], segments[len(segments)-1]
	return dirs, name, !slices.ContainsFunc(dirs, func(d string) bool { return slices.Contains(vendoredDirectories, d) })
}

// classify returns the include patterns and the runner patterns that file path implies; none of either means it is not
// test-like. The runner patterns are the whole group of a runner file found, and a Python test file brings the pytest
// group. Suggest and Uncovered share it, so they always agree on what counts as a test.
func classify(p string) (include, runner []string) {
	dirs, name, ok := splitFile(p)
	if !ok {
		return nil, nil
	}
	for _, glob := range conventionFileGlobs {
		if nameMatches([]string{glob}, name) {
			include = append(include, glob)
		}
	}
	testNamed := len(include) > 0
	for i, dir := range dirs {
		switch {
		case slices.Contains(conventionDirectories, dir):
			include = append(include, dir+"/")
		case slices.Contains(ambiguousDirectories, dir) && i == 0:
			include = append(include, "/"+dir+"/")
		case slices.Contains(ambiguousDirectories, dir) && i == len(dirs)-1 && testNamed:
			include = append(include, dir+"/")
		case dir == "src" && i+1 < len(dirs) && (dirs[i+1] == "test" || dirs[i+1] == "it"):
			include = append(include, "**/src/"+dirs[i+1]+"/") // the interior slash anchors a bare "src/test/"
		}
	}
	for i, group := range runnerGroups {
		if nameMatches(group, name) || (i == 0 && nameMatches(pythonTestGlobs, name)) {
			runner = append(runner, group...)
		}
	}
	return include, runner
}

// Suggest proposes a declaration from the paths of the File entries of a tree (D-SCOPE-DISCOVERY): include patterns for
// the test files and directories of the frameworks' conventions, runner patterns for the runner files found, and the mixed
// files that may hold test settings. Submodules, sorted, are the non-vendored ones that a kept pattern covers or may cover
// below, because their content is outside the scope. The lists are never nil. Include is the directory patterns then the file
// patterns, each sorted, and the sorted runner patterns. The limit of maxSuggestions holds for both together: the runner patterns, which
// no base declaration lists, keep their room (see limit) and the include patterns give way, the first being kept, and Cut is set when
// anything is dropped. Mixed and Submodules have their own limit.
func Suggest(entries []Entry) Suggestion {
	return suggest(entries, func(string) bool { return true })
}

// SuggestAgainst is Suggest for a base declaration s: a runner pattern that s lists or covers (the rule of MissingRunner) is left out before
// the limit applies, so it neither takes room nor sets Cut. Include patterns are not filtered: a file they come from is one s does not cover.
// The runner patterns that remain are missing from s, and keep their room within the limit as in Suggest.
func SuggestAgainst(entries []Entry, s scope.Scope) (Suggestion, error) {
	handled := runnerHandled(s)
	var failed error
	suggestion := suggest(entries, func(pattern string) bool {
		done, err := handled(pattern)
		failed = cmp.Or(failed, err)
		return !done
	})
	if failed != nil {
		return Suggestion{}, failed
	}
	return suggestion, nil
}

// candidates are what a tree offers before any limit: the include patterns (the directory patterns then the file patterns, each sorted), the sorted
// runner patterns, the mixed paths and the submodule paths.
type candidates struct {
	include, runner, mixed, submodules []string
}

// gather finds the candidates of entries, keeping only the runner patterns that keepRunner accepts.
func gather(entries []Entry, keepRunner func(pattern string) bool) candidates {
	directories, files, runner := map[string]bool{}, map[string]bool{}, map[string]bool{}
	var found candidates
	for _, entry := range entries {
		if entry.Kind == Submodule && !slices.ContainsFunc(strings.Split(entry.Path, "/"), func(s string) bool { return slices.Contains(vendoredDirectories, s) }) {
			found.submodules = append(found.submodules, entry.Path)
		}
		if entry.Kind != File {
			continue
		}
		_, name, ok := splitFile(entry.Path)
		if !ok {
			continue
		}
		include, runnerPatterns := classify(entry.Path)
		for _, pattern := range include {
			if strings.HasSuffix(pattern, "/") {
				directories[pattern] = true
			} else {
				files[pattern] = true
			}
		}
		for _, pattern := range runnerPatterns {
			if keepRunner(pattern) {
				runner[pattern] = true
			}
		}
		if slices.Contains(mixedNames, name) || strings.HasSuffix(name, ".csproj") {
			found.mixed = append(found.mixed, entry.Path)
		}
	}
	found.include = slices.Concat(slices.Sorted(maps.Keys(directories)), slices.Sorted(maps.Keys(files)))
	found.runner = slices.Sorted(maps.Keys(runner))
	slices.Sort(found.mixed)
	return found
}

// limit applies maxSuggestions to the include and the runner patterns together: the runner patterns come first, at most maxSuggestions of them, and the
// include patterns take the rest. The runner patterns offered are the ones missing, so a missing runner is never hidden by include patterns.
// cut says that some pattern was dropped.
func limit(include, runner []string) (keptInclude, keptRunner []string, cut bool) {
	keptRunner = runner[:min(len(runner), maxSuggestions)]
	keptInclude = include[:min(len(include), maxSuggestions-len(keptRunner))]
	return append([]string{}, keptInclude...), append([]string{}, keptRunner...), len(keptInclude)+len(keptRunner) < len(include)+len(runner)
}

// suggest is Suggest keeping only the runner patterns that keepRunner accepts.
func suggest(entries []Entry, keepRunner func(pattern string) bool) Suggestion {
	found := gather(entries, keepRunner)
	include, runner, cut := limit(found.include, found.runner)
	return Suggestion{
		Include:    include,
		Runner:     runner,
		Mixed:      append([]string{}, found.mixed[:min(len(found.mixed), maxSuggestions)]...),
		Submodules: reached(found.submodules, slices.Concat(include, runner)),
		Cut:        cut,
	}
}

// reached returns the first maxSuggestions of the sorted submodule paths that any pattern covers or may cover below. A
// submodule whose path is not valid is skipped: Suggest takes unvalidated entries, and such a path is not one a pattern
// can protect. Every pattern is one Suggest built from constants, so a pattern that does not parse is a programming
// error, like scope.mustParsePattern's.
func reached(submodules, patterns []string) []string {
	parsed := make([]scope.Pattern, 0, len(patterns))
	for _, pattern := range patterns {
		p, err := scope.ParsePattern(pattern)
		if err != nil {
			panic(fmt.Sprintf("inventory: suggested pattern %q does not parse: %v", pattern, err))
		}
		parsed = append(parsed, p)
	}
	slices.Sort(submodules)
	result := []string{}
	for _, submodule := range submodules {
		if !artifact.ValidPath(submodule) {
			continue
		}
		hit := slices.ContainsFunc(parsed, func(p scope.Pattern) bool {
			// Neither call can fail: the pattern parsed and the path is valid.
			covers, err := p.Match(submodule)
			below, belowErr := p.MayMatchBelow(submodule)
			if err != nil || belowErr != nil {
				panic(fmt.Sprintf("inventory: matching %q against %q: %v %v", submodule, p, err, belowErr))
			}
			return covers || below
		})
		if hit && len(result) < maxSuggestions {
			result = append(result, submodule)
		}
	}
	return result
}

// Uncovered returns the sorted test-like and runner file paths of entries that s does not cover. A path that an exclude of s
// matches is never returned: an exclusion is the owner's choice, and adding patterns cannot undo it.
func Uncovered(entries []Entry, s scope.Scope) ([]string, error) {
	return uncoveredWhere(entries, s, func(e Entry) bool {
		include, runner := classify(e.Path)
		return e.Kind == File && len(include)+len(runner) > 0
	})
}

// UncoveredRunners is Uncovered for the files of the runner families only (a name of runnerGroups): a test file is never one, although
// classify brings the pytest group for a Python test file. A symbolic link with a runner name counts like a file: a link the scope covers is invalid
// already, and one it does not cover can still make the runner read another file. A submodule never counts.
func UncoveredRunners(entries []Entry, s scope.Scope) ([]string, error) {
	return uncoveredWhere(entries, s, func(e Entry) bool { return (e.Kind == File || e.Kind == Symlink) && isRunnerFile(e.Path) })
}

// isRunnerFile reports whether p is not vendored and its base name is one of a runner group.
func isRunnerFile(p string) bool {
	_, name, ok := splitFile(p)
	return ok && slices.ContainsFunc(runnerGroups, func(group []string) bool { return nameMatches(group, name) })
}

// RunnerPatterns returns the runner patterns that protecting the files at paths adds: the whole group of each runner family found,
// sorted and without repeats. A path that is no runner file brings nothing.
func RunnerPatterns(paths []string) []string {
	found := map[string]bool{}
	for _, p := range paths {
		_, name, ok := splitFile(p)
		if !ok {
			continue
		}
		for _, group := range runnerGroups {
			if nameMatches(group, name) {
				for _, pattern := range group {
					found[pattern] = true
				}
			}
		}
	}
	return append([]string{}, slices.Sorted(maps.Keys(found))...)
}

// uncoveredWhere returns the sorted paths of the entries that keep accepts and s neither covers nor excludes.
func uncoveredWhere(entries []Entry, s scope.Scope, keep func(Entry) bool) ([]string, error) {
	uncovered := []string{}
	for _, entry := range entries {
		if !keep(entry) {
			continue
		}
		covered, err := s.Covers(entry.Path)
		if err != nil { // the only failure is a path that is none: a fact about the tree
			return nil, fmt.Errorf("%w: %w", ErrInvalidTree, err)
		}
		if covered {
			continue
		}
		excluded, err := excludes(s, entry.Path)
		if err != nil {
			return nil, err
		}
		if !excluded {
			uncovered = append(uncovered, entry.Path)
		}
	}
	slices.Sort(uncovered)
	return uncovered, nil
}

// excludes reports whether an exclude of s matches path.
func excludes(s scope.Scope, path string) (bool, error) {
	for _, exclude := range s.Declaration().Exclude() {
		if matched, err := exclude.Match(path); err != nil || matched {
			return matched, err
		}
	}
	return false, nil
}

// MissingRunner returns the runner patterns Suggest offers for entries that s neither lists nor covers, in Suggest's order and before its limit. A runner
// name matches no file until someone adds one, so it can be missing while Uncovered finds nothing. See runnerHandled for the rule.
func MissingRunner(entries []Entry, s scope.Scope) ([]string, error) {
	handled := runnerHandled(s)
	missing := []string{}
	for _, pattern := range gather(entries, func(string) bool { return true }).runner { // before any limit: the missing ones are all reported
		done, err := handled(pattern)
		if err != nil {
			return nil, err
		}
		if !done {
			missing = append(missing, pattern)
		}
	}
	return missing, nil
}

// runnerHandled returns the test of whether a runner pattern needs no suggestion for s. A pattern is handled when s lists the same string
// in its include, runner or exclude list (an exclusion is the owner's choice), or, for a name with no glob character, when s covers that name as
// a root path or an exclude of s matches it. Any other glob pattern is not handled.
func runnerHandled(s scope.Scope) func(pattern string) (bool, error) {
	d := s.Declaration()
	listed := map[string]bool{}
	for _, p := range slices.Concat(d.Include(), d.Runner(), d.Exclude()) {
		listed[p.String()] = true
	}
	return func(pattern string) (bool, error) {
		if listed[pattern] {
			return true, nil
		}
		if strings.ContainsAny(pattern, "*?[") {
			return false, nil
		}
		covered, err := s.Covers(pattern)
		if err != nil {
			return false, err
		}
		excluded, err := excludes(s, pattern)
		return covered || excluded, err
	}
}

// WithRunner returns s with the runner patterns extra added, sorted and without repeats. The limit of maxSuggestions include and runner
// patterns together holds: the runner patterns, which s lacks, keep their room, the include patterns past the limit are dropped, and Cut is set.
// Submodules is not recomputed: it keeps what the patterns of s reached before extra was added and before any include pattern was dropped.
func (s Suggestion) WithRunner(extra []string) Suggestion {
	runner := slices.Compact(slices.Sorted(slices.Values(slices.Concat(s.Runner, extra))))
	include, runner, cut := limit(s.Include, runner)
	s.Include, s.Runner, s.Cut = include, runner, s.Cut || cut
	return s
}
