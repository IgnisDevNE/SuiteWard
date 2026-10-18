package inventory

import (
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

// nameMatches reports whether any glob matches the base name; every glob is a constant of this file.
func nameMatches(globs []string, name string) bool {
	return slices.ContainsFunc(globs, func(glob string) bool { ok, _ := path.Match(glob, name); return ok })
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
// patterns, each sorted, followed in the limit by the sorted runner patterns; past maxSuggestions the first are kept and Cut
// is set. Mixed and Submodules have their own limit.
func Suggest(entries []Entry) Suggestion {
	directories, files, runner := map[string]bool{}, map[string]bool{}, map[string]bool{}
	var mixed, submodules []string
	for _, entry := range entries {
		if entry.Kind == Submodule && !slices.ContainsFunc(strings.Split(entry.Path, "/"), func(s string) bool { return slices.Contains(vendoredDirectories, s) }) {
			submodules = append(submodules, entry.Path)
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
			runner[pattern] = true
		}
		if slices.Contains(mixedNames, name) || strings.HasSuffix(name, ".csproj") {
			mixed = append(mixed, entry.Path)
		}
	}

	includeCount := len(directories) + len(files)
	all := slices.Concat(slices.Sorted(maps.Keys(directories)), slices.Sorted(maps.Keys(files)), slices.Sorted(maps.Keys(runner)))
	kept := all[:min(len(all), maxSuggestions)]
	slices.Sort(mixed)
	return Suggestion{
		Include:    append([]string{}, kept[:min(includeCount, len(kept))]...),
		Runner:     append([]string{}, kept[min(includeCount, len(kept)):]...),
		Mixed:      append([]string{}, mixed[:min(len(mixed), maxSuggestions)]...),
		Submodules: reached(submodules, kept),
		Cut:        len(all) > len(kept),
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
	uncovered := []string{}
	for _, entry := range entries {
		if entry.Kind != File {
			continue
		}
		if include, runner := classify(entry.Path); len(include)+len(runner) == 0 {
			continue
		}
		covered, err := s.Covers(entry.Path)
		if err != nil {
			return nil, err
		}
		if covered {
			continue
		}
		excluded := false
		for _, exclude := range s.Declaration().Exclude() {
			if excluded, err = exclude.Match(entry.Path); err != nil || excluded {
				break
			}
		}
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
