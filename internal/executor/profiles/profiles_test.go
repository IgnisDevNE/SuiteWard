package profiles_test

import (
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/profiles"
)

var (
	idPattern  = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}$`)
	pinPattern = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
)

func ids(list []profiles.Profile) []string {
	var out []string
	for _, p := range list {
		out = append(out, p.ID)
	}
	return out
}

func TestProfilesListGoWithItsPin(t *testing.T) {
	all := profiles.All()
	if len(all) != 1 {
		t.Fatalf("All() = %d entries, want 1 (go only until the cpp profile lands)", len(all))
	}
	for _, p := range all {
		if !idPattern.MatchString(p.ID) {
			t.Errorf("profile id %q does not match %s", p.ID, idPattern)
		}
		if p.Name == "" {
			t.Errorf("profile %q has no name", p.ID)
		}
		if !pinPattern.MatchString(p.Image) {
			t.Errorf("profile %q image %q is not pinned by digest", p.ID, p.Image)
		}
		if p.ImageBytes <= 0 {
			t.Errorf("profile %q ImageBytes = %d, want a positive size", p.ID, p.ImageBytes)
		}
		if len(p.BuildFiles) == 0 {
			t.Errorf("profile %q names no build file", p.ID)
		}
		for _, f := range p.BuildFiles {
			if f == "" || strings.Contains(f, "/") {
				t.Errorf("profile %q build file %q is not a base name", p.ID, f)
			}
		}
	}

	want := profiles.Profile{ID: "go", Name: "Go", Image: goprofile.Image, ImageBytes: all[0].ImageBytes, BuildFiles: []string{"go.mod"}}
	if !reflect.DeepEqual(all[0], want) {
		t.Errorf("All()[0] = %+v, want %+v", all[0], want)
	}

	// A caller that changes what it was given does not change the list.
	all[0].ID = "changed"
	all[0].Image = "changed"
	all[0].BuildFiles[0] = "changed"
	_ = append(all[:0], profiles.Profile{ID: "extra"})
	if again := profiles.All(); !reflect.DeepEqual(again, []profiles.Profile{want}) {
		t.Errorf("All() after the caller changed its copy = %+v, want %+v", again, want)
	}
}

func TestByIDRefusesUnknown(t *testing.T) {
	all := profiles.All()
	got, err := profiles.ByID("go")
	if err != nil {
		t.Fatalf(`ByID("go") error = %v`, err)
	}
	if !reflect.DeepEqual(got, all[0]) {
		t.Errorf(`ByID("go") = %+v, want %+v`, got, all[0])
	}

	// Until X18 adds cpp, it is unknown too.
	for _, id := range []string{"cpp", "", "Go"} {
		got, err := profiles.ByID(id)
		if err == nil {
			t.Errorf("ByID(%q) = %+v, want an error", id, got)
			continue
		}
		if !regexp.MustCompile(`\bgo\b`).MatchString(err.Error()) {
			t.Errorf("ByID(%q) error = %q, want it to name the known id go", id, err)
		}
	}

	got.BuildFiles[0] = "changed"
	if again, _ := profiles.ByID("go"); again.BuildFiles[0] != "go.mod" {
		t.Errorf("ByID after the caller changed its copy: BuildFiles = %q, want go.mod", again.BuildFiles)
	}
}

func TestSuggestFromPaths(t *testing.T) {
	for _, tc := range []struct {
		name  string
		paths []string
		want  []string
	}{
		{"go.mod at the root", []string{"go.mod"}, []string{"go"}},
		{"go.mod in a subfolder", []string{"tools/cli/go.mod"}, []string{"go"}},
		{"go.mod twice suggests go once", []string{"go.mod", "tools/go.mod"}, []string{"go"}},
		{"go.mod among other files", []string{"README.md", "cmd/main.go", "go.mod"}, []string{"go"}},
		{"go.sum", []string{"go.sum"}, nil},
		{"go.mod.bak", []string{"go.mod.bak"}, nil},
		{"notgo.mod", []string{"notgo.mod", "sub/notgo.mod"}, nil},
		{"go.mod as a folder", []string{"x/go.mod/y"}, nil},
		{"a backslash is nothing special", []string{`sub\go.mod`}, nil},
		{"the name in another case", []string{"GO.MOD"}, nil},
		{"an empty list", []string{}, nil},
		{"no list", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := profiles.Suggest(tc.paths); !slices.Equal(got, tc.want) {
				t.Errorf("Suggest(%q) = %q, want %q", tc.paths, got, tc.want)
			}
		})
	}

	// Every profile is suggested by its own build files, in the list's order, whatever the order of the paths.
	all := profiles.All()
	var paths []string
	for i := len(all) - 1; i >= 0; i-- {
		for _, f := range all[i].BuildFiles {
			paths = append(paths, "sub/"+f)
		}
	}
	if got := profiles.Suggest(paths); !slices.Equal(got, ids(all)) {
		t.Errorf("Suggest(%q) = %q, want the list's order %q", paths, got, ids(all))
	}
}

// The declaration keeps its own closed set because the domain imports no executor package: this keeps the two equal.
func TestRegistryMatchesDeclarationProfiles(t *testing.T) {
	got := ids(profiles.All())
	slices.Sort(got)
	if want := scope.ProfileIDs(); !slices.Equal(got, want) {
		t.Errorf("the ids of profiles.All() = %q, want the declaration's closed set %q", got, want)
	}
}
