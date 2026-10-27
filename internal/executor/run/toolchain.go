package run

import (
	"go/version"

	"golang.org/x/mod/modfile"
)

// needsNewerGo reports whether the go.mod asks for a Go that is newer than pinned (for example "go1.27.3"): its go line or its toolchain
// line. A go.mod that cannot be read at all is not a mismatch: the prefetch refuses it. The lax parser reads a go.mod with a directive
// that this build of x/mod does not know, which is what a go.mod of a newer Go may hold, so that it reads as a mismatch and not as a bad
// file; it drops the toolchain line, which only the strict parser reads.
func needsNewerGo(goMod []byte, pinned string) bool {
	f, err := modfile.Parse("go.mod", goMod, nil)
	if err != nil {
		if f, err = modfile.ParseLax("go.mod", goMod, nil); err != nil {
			return false
		}
	}
	if f.Go != nil && version.Compare("go"+f.Go.Version, pinned) > 0 {
		return true
	}
	return f.Toolchain != nil && version.Compare(f.Toolchain.Name, pinned) > 0
}
