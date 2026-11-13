// Package profiles is the list of the sandbox profiles: one constant of the release that the service (the image of a lease), the
// test runner (its pins) and the installer (the page, profile add, doctor and the upgrade) all read. It reads no file, runs no
// command and asks no registry. The declaration keeps its own closed set of ids (scope.ProfileIDs), because the domain imports
// no executor package; a test keeps the two equal.
package profiles

import (
	"fmt"
	"slices"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
)

// Profile is one entry of the list.
type Profile struct {
	ID         string   // ^[a-z][a-z0-9]{0,15}$, from the declaration's closed set
	Name       string   // the words of the installer page
	Image      string   // pinned by digest
	ImageBytes int64    // the size the page shows: fixed in the code, never asked of a registry
	BuildFiles []string // base names that suggest the profile
}

// list is the order of All and of Suggest. Each profile reads its image from its own package, so scripts/check-pins.ps1 keeps one
// copy of a digest to check.
var list = []Profile{
	// ImageBytes is `podman image inspect --format '{{.Size}}'` of Image, linux/amd64, on the rootless connection of
	// podman-machine-default (2026-11-13), the same measure as the C/C++ image's 411507626.
	{ID: "go", Name: "Go", Image: goprofile.Image, ImageBytes: 266516173, BuildFiles: []string{"go.mod"}},
}

// All returns the profiles in the list's order. The result is a copy: a caller cannot change the list.
func All() []Profile {
	out := slices.Clone(list)
	for i := range out {
		out[i].BuildFiles = slices.Clone(out[i].BuildFiles)
	}
	return out
}

// ByID returns the profile of an id. An unknown id, an empty one and one in the wrong case included, is an error that names the
// known ids.
func ByID(id string) (Profile, error) {
	all := All()
	for _, p := range all {
		if p.ID == id {
			return p, nil
		}
	}
	known := make([]string, len(all))
	for i, p := range all {
		known[i] = p.ID
	}
	return Profile{}, fmt.Errorf("unknown profile %q, the known profiles are %s", id, strings.Join(known, ", "))
}

// Suggest returns the ids of the profiles that some path names a build file of, in the list's order and each once. A path is
// repository-relative with "/"; only its last segment counts, and it must equal a build file (go.mod, not go.mod.bak). No file is
// read, so a profile is only suggested: the owner decides.
func Suggest(paths []string) []string {
	var ids []string
	for _, p := range list {
		if slices.ContainsFunc(paths, func(path string) bool {
			return slices.Contains(p.BuildFiles, path[strings.LastIndexByte(path, '/')+1:])
		}) {
			ids = append(ids, p.ID)
		}
	}
	return ids
}
